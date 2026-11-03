package solana

import (
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// solNonceReattestMinAgeBlocks mirrors the THORChain rule (x/thorchain
// handler_sol_nonce_attest.go): a committed nonce younger than this cannot be
// replaced, so do not attest before then.
const solNonceReattestMinAgeBlocks = int64(600)

// solNonceAttestPoster is implemented by the production thorchain bridge.
type solNonceAttestPoster interface {
	PostSolNonceAttest(vaultPubKey common.PubKey, nonceAccount, nonce string, slot int64, lastTx common.TxID) (common.TxID, error)
}

// observedTxChecker reports whether THORChain has a finalised, non-reverted
// observation of a transaction (the production bridge implements it).
type observedTxChecker interface {
	GetObservedTxVoterUncached(txID string) (*ttypes.QueryObservedTxVoter, bool, error)
}

// finalisedOnTHORChain returns (finalised, known) for a tx; known=false on read error
// or when no checker is available.
func finalisedOnTHORChain(chk observedTxChecker, txID string) (finalised, known bool) {
	if chk == nil {
		return false, false
	}
	voter, found, err := chk.GetObservedTxVoterUncached(txID)
	if err != nil {
		return false, false
	}
	return found && voter != nil && voter.FinalisedHeight > 0 && !voter.Reverted, true
}

// solNonceAccountReader reads the chain state a re-attestation is built from.
type solNonceAccountReader interface {
	GetNonceAccount(pubkey string, minContextSlot uint64) (nonce, authority string, err error)
	GetSignaturesForAddressUntil(address string, params rpc.GetSignaturesForAddressUntilParams) ([]rpc.SignaturesForAddressResult, error)
	GetTransaction(signature string) (*rpc.TransactionResult, error)
}

// solNonceReattestation is what this validator would attest for one vault.
type solNonceReattestation struct {
	nonceAccount, nonce string
	slot                int64
	lastTx              common.TxID
}

// reattestVaultNonces re-attests every SOL asgard whose on-chain durable nonce has
// moved past its committed value without a committed observation. Every active
// validator runs it (THORChain counts 2/3 of ALL active validators, not only vault
// members), including under HaltSigningSOL: it observes, it does not sign.
func (c *SOLClient) reattestVaultNonces(currentHeight int64) {
	poster, ok := c.bridge.(solNonceAttestPoster)
	if !ok {
		return
	}
	var chain solNonceAccountReader = c.nonceReader
	if chain == nil {
		if c.rpcClient == nil {
			return
		}
		chain = c.rpcClient
	}
	reader, ok := c.bridge.(solVaultNonceReader)
	if !ok {
		return
	}
	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		c.logger.Err(err).Msg("fail to get asgards for durable-nonce re-attestation")
		return
	}
	for _, vault := range vaults {
		if vault.PubKeyEddsa.IsEmpty() {
			continue
		}
		committed, err := reader.GetSolVaultNonce(vault.PubKey.String(), 0)
		if err != nil || committed.IsEmpty() {
			continue
		}
		addr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
		if err != nil {
			continue
		}
		chk, _ := c.bridge.(observedTxChecker)
		att, ok := solNonceReattestationFor(chain, chk, c.logger, addr.String(), committed, currentHeight)
		if !ok {
			continue
		}
		if _, err := poster.PostSolNonceAttest(vault.PubKey, att.nonceAccount, att.nonce, att.slot, att.lastTx); err != nil {
			c.logger.Err(err).Str("vault", vault.PubKey.String()).Msg("fail to post durable-nonce re-attestation")
		}
	}
}

// solNonceReattestationFor decides whether to re-attest a vault's nonce, and what.
// It attests only when
//   - the committed value is at least solNonceReattestMinAgeBlocks old,
//   - the vault's nonce account (authority = the vault) holds a different value, and
//   - every transaction the VAULT signed that touched the nonce account since the
//     committed one (set_by_tx) FAILED on-chain.
//
// The last rule is the safety condition. A failed tx moves no funds, so re-anchoring
// past it cannot hide a payment. If a vault-signed tx SUCCEEDED, it may be an
// unobserved landed outbound. Re-anchoring past it would release the reissue guard
// and could pay twice, so that case is left to observation (or the break-glass).
// Successful txs the vault did NOT sign (anyone can send lamports to the public
// nonce address) can neither advance the nonce nor move vault funds, so they are
// ignored; otherwise a dust transfer would block re-attestation.
func solNonceReattestationFor(r solNonceAccountReader, chk observedTxChecker, logger zerolog.Logger, vaultAddr string, committed thorclient.SolVaultNonce, currentHeight int64) (solNonceReattestation, bool) {
	if committed.SetByTx == "" || currentHeight-committed.UpdatedHeight < solNonceReattestMinAgeBlocks {
		return solNonceReattestation{}, false
	}
	expected, err := common.SolNonceAccountAddress(vaultAddr)
	if err != nil || expected != committed.NonceAccount {
		return solNonceReattestation{}, false
	}
	nonce, authority, err := r.GetNonceAccount(committed.NonceAccount, 0)
	if err != nil || authority != vaultAddr || nonce == committed.Nonce {
		return solNonceReattestation{}, false
	}
	sigs, err := r.GetSignaturesForAddressUntil(committed.NonceAccount, rpc.GetSignaturesForAddressUntilParams{
		Commitment: "finalized",
		Until:      committed.SetByTx,
		Limit:      100,
	})
	if err != nil || len(sigs) == 0 || len(sigs) >= 100 {
		// Unreadable, nothing visible, or too many to prove all failed.
		return solNonceReattestation{}, false
	}
	var newest *rpc.SignaturesForAddressResult
	for i, sig := range sigs { // newest first
		signed, known := signedByVault(r, sig.Signature, vaultAddr)
		if !known {
			return solNonceReattestation{}, false
		}
		if !signed {
			continue // not the vault's: cannot advance its nonce or move its funds
		}
		if sig.Err == nil {
			// A successful vault tx is safe to re-anchor past only once THORChain has a
			// finalised observation of it (the reissue guard then sees any landed copy);
			// that covers an observed tx whose nonce votes split and never committed.
			finalised, known := finalisedOnTHORChain(chk, sig.Signature)
			if !known {
				return solNonceReattestation{}, false
			}
			if !finalised {
				logger.Warn().Str("nonce_account", committed.NonceAccount).Str("tx", sig.Signature).
					Msg("durable nonce advanced by a successful unobserved vault tx; not re-attesting (needs observation or break-glass)")
				return solNonceReattestation{}, false
			}
		}
		if newest == nil {
			newest = &sigs[i]
		}
	}
	if newest == nil {
		return solNonceReattestation{}, false // no vault tx explains the move
	}
	// The newest vault tx (failed, or successful and finalised on THORChain) is the one
	// that set the on-chain value.
	return solNonceReattestation{
		nonceAccount: committed.NonceAccount,
		nonce:        nonce,
		slot:         int64(newest.Slot),
		lastTx:       common.TxID(newest.Signature),
	}, true
}

// signedByVault reports whether the vault is one of the tx's required signers
// (known=false when the tx cannot be read).
func signedByVault(r solNonceAccountReader, signature, vaultAddr string) (signed, known bool) {
	tx, err := r.GetTransaction(signature)
	// getTransaction answers "result": null (decoded as an empty result, no error)
	// for a pruned, unknown or not-yet-visible signature. That is UNKNOWN, not
	// "not the vault's": treating it as known would skip a landed vault payment
	// and let re-attestation anchor past it.
	if err != nil || tx == nil || tx.Slot == 0 || len(tx.Transaction.Message.AccountKeys) == 0 {
		return false, false
	}
	keys := tx.Transaction.Message.AccountKeys
	n := tx.Transaction.Message.Header.NumRequiredSignatures
	for i := 0; i < n && i < len(keys); i++ {
		if keys[i] == vaultAddr {
			return true, true
		}
	}
	return false, true
}
