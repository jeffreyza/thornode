package solana

import (
	"encoding/base64"
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

const (
	// solSysvarOwner owns the sysvar accounts, which cannot be credited.
	solSysvarOwner = "Sysvar1111111111111111111111111111111111111"
	// solRentExemptFallbackLamports is the rent-exempt minimum of a 0-data account,
	// used only when the RPC cannot be asked.
	solRentExemptFallbackLamports = 890_880
	// durableRejectTTL bounds how long a rejected outbound is remembered for the
	// serialize gate; LackSigning re-issues it at a new height well before that.
	durableRejectTTL = time.Hour
)

// durablePreSignReject runs the pre-sign filters on a built durable outbound and
// returns the reason it would fail on-chain, or "" to sign it.
//
// A durable tx that lands and FAILS still pays its fee and consumes the nonce, but
// is never observed, so the vault's committed nonce goes stale and its SOL
// outbounds wedge. The filters refuse to sign what would fail:
//   - a recipient that cannot be credited (executable program account, sysvar),
//   - a new recipient funded below the rent-exempt minimum,
//   - a vault that cannot pay amount + fee, or would be left below rent-exempt,
//   - anything the runtime rejects in simulation (against the nonce the tx carries).
//
// Not signing is always safe: nothing exists that could land, and LackSigning
// re-issues the outbound. The filters only reduce failures — a durable tx never
// expires, so state can still change before it lands; the break-glass
// SOLNonceClear-<vault> and nonce re-attestation cover the rest. RPC failures fail
// OPEN (sign as before): a filter must not become a liveness gate.
func (c *SOLClient) durablePreSignReject(tx stypes.TxOutItem, solTx types.Transaction) string {
	if c.rpcClient == nil || len(tx.Coins) != 1 {
		return ""
	}
	amount := convertToLamports(tx.Coins[0].Amount).Uint64()
	fee := uint64(baseFeePerSigLamports) * uint64(len(solTx.Signatures))
	rentExempt, err := c.rpcClient.GetMinimumBalanceForRentExemption(0)
	if err != nil || rentExempt == 0 {
		rentExempt = solRentExemptFallbackLamports
	}

	to, err := c.rpcClient.GetAccountMeta(tx.ToAddress.String())
	if err != nil {
		c.logger.Warn().Err(err).Str("to", tx.ToAddress.String()).Msg("pre-sign filter: fail to read recipient; not filtering")
	} else {
		if to.Exists && (to.Executable || to.Owner == solSysvarOwner) {
			return "durable_filter_recipient_not_creditable"
		}
		if !to.Exists && amount < rentExempt {
			return "durable_filter_recipient_below_rent_exempt"
		}
	}

	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(tx.Chain)
	if err == nil {
		from, fErr := c.rpcClient.GetAccountMeta(vaultAddr.String())
		switch {
		case fErr != nil:
			c.logger.Warn().Err(fErr).Str("vault", vaultAddr.String()).Msg("pre-sign filter: fail to read vault; not filtering")
		case from.Lamports < amount+fee:
			return "durable_filter_vault_balance"
		default:
			if post := from.Lamports - amount - fee; post > 0 && post < rentExempt {
				return "durable_filter_vault_below_rent_exempt"
			}
		}
	}

	raw, err := solTx.Serialize()
	if err != nil {
		return ""
	}
	execErr, err := c.rpcClient.SimulateTransaction(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		c.logger.Warn().Err(err).Msg("pre-sign filter: simulation unavailable; not filtering")
		return ""
	}
	if execErr != nil {
		c.logger.Warn().RawJSON("sim_err", execErr).Interface("tx", tx).Msg("pre-sign filter: simulation failed")
		return "durable_filter_simulation"
	}
	return ""
}

func durableRejectKey(inHash, to, asset, amount string, height int64) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", inHash, to, asset, amount, height)
}

// markDurableRejected remembers an outbound this node refused to sign, so the
// serialize gate does not hold its later siblings behind it.
func (c *SOLClient) markDurableRejected(tx stypes.TxOutItem, height int64) {
	if len(tx.Coins) != 1 {
		return
	}
	now := time.Now()
	c.durableRejects.Range(func(k, v any) bool {
		if exp, ok := v.(time.Time); ok && now.After(exp) {
			c.durableRejects.Delete(k)
		}
		return true
	})
	key := durableRejectKey(tx.InHash.String(), tx.ToAddress.String(), tx.Coins[0].Asset.String(), tx.Coins[0].Amount.String(), height)
	c.durableRejects.Store(key, now.Add(durableRejectTTL))
}

// durableRejected reports whether this node refused to sign the queued outbound.
func (c *SOLClient) durableRejected(p thorclient.PendingOutbound) bool {
	v, ok := c.durableRejects.Load(durableRejectKey(p.InHash, p.ToAddress, p.Coin.Asset, p.Coin.Amount, p.Height))
	if !ok {
		return false
	}
	exp, ok := v.(time.Time)
	return ok && time.Now().Before(exp)
}
