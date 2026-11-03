package solana

import (
	"errors"
	"fmt"
	"math/big"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// sol_nonce_provision.go — durable-nonce account provisioning + churn close
//
// A vault's SOL outbounds anchor on a durable nonce (AdvanceNonceAccount) instead of
// a recent blockhash so they cannot expire during the multi-party TSS keysign +
// broadcast window. That requires each vault to own an initialized nonce account at a
// deterministic address. Provisioning creates it; churn closes it to reclaim rent.
//
// Both run bifrost-autonomously, off the outbound path — modelled on the keep-alive
// self-sign (sol_keepalive.go): they fire on the SAME shared THORChain-block cadence
// boundary the keep-alive uses, so the keysign anchor height (boundary × cadence) is
// (a) ≤ tip and (b) identical across every vault member, so members batch into one
// keysign session (go-tss batches by {algo, height}) and — with all other tx fields
// being pure functions of the vault — sign byte-identical transactions. Nodes that
// are not members of a vault skip it.
//
// Chicken-egg / churn safety (Itz's question): the create-nonce tx itself uses a
// recent (consensus) blockhash — a nonce cannot anchor its own creation — so its
// keysign is still subject to the ~150-slot expiry. It lands anyway because it runs
// at NORMAL cadence (not the churn storm), retries every boundary with a fresh
// blockhash, and has no slasher deadline: one good roll ever suffices. Crucially,
// outbounds NEVER block on it — until the nonce account exists, SignTx's
// durableNonceForVault returns ok=false and the outbound falls back to the
// recent-blockhash path. So a not-yet-provisioned or slow-to-provision vault is never
// worse than today, and churn is never locked. The only state in which provisioning
// cannot complete is a PERSISTENTLY >expiry keysign latency — a regime already fully
// broken for ordinary outbounds; durable nonce self-heals the moment latency dips
// below the window even once.
////////////////////////////////////////////////////////////////////////////////////////

// nonceProvisionFeeMarginLamports is the balance headroom required beyond the rent
// funding so the create-nonce transaction fee is always payable.
const nonceProvisionFeeMarginLamports = 100_000

// nonceProvisionThorBlockCadence is the durable-nonce PROVISIONING cadence in
// THORChain blocks (~2 min at 6s/block). Chosen below the keep-alive's 50 for faster
// provisioning while staying safely above one create keysign's full life (keysign
// ~45s + Solana finalization ~13s + margin): a create attempt finalizes before the
// next boundary, so the same vault's create never overlaps itself and the idempotent
// finalized check simply skips a re-fire. boundary × cadence is a shared consensus
// height, so every member's create keysign converges. A create fires at most once per
// vault (idempotent once the account exists), so the TSS load is one-shot, not recurring.
const nonceProvisionThorBlockCadence = 20

func (c *SOLClient) getNonceProvisionThorMultiple() int64 {
	c.nonceProvisionThorMultipleMu.Lock()
	defer c.nonceProvisionThorMultipleMu.Unlock()
	return c.nonceProvisionThorMultiple
}

func (c *SOLClient) setNonceProvisionThorMultiple(v int64) {
	c.nonceProvisionThorMultipleMu.Lock()
	defer c.nonceProvisionThorMultipleMu.Unlock()
	c.nonceProvisionThorMultiple = v
}

// nextNonceProvisionThorMultiple mirrors nextKeepAliveThorMultiple with the finer
// provisioning cadence: a fresh node jumps to the current boundary, otherwise advances
// one boundary at a time so members with slightly different tips converge.
func (c *SOLClient) nextNonceProvisionThorMultiple(thorHeight int64) int64 {
	current := c.getNonceProvisionThorMultiple()
	if nonceProvisionThorBlockCadence <= 0 {
		return current
	}
	crossed := thorHeight / nonceProvisionThorBlockCadence
	if thorHeight%nonceProvisionThorBlockCadence == 0 {
		crossed--
	}
	if crossed <= current {
		return current
	}
	if current == 0 && crossed > 1 {
		return crossed
	}
	return current + 1
}

// vaultHasPendingSOLOutbound reports whether any not-yet-observed SOL outbound in the
// pending set belongs to the vault (by either pubkey). Used to skip a create keysign
// when a vault has an outbound in flight, so the create never contends for the same
// vault's TSS party (D). Pure so it is unit-testable.
func vaultHasPendingSOLOutbound(pending []thorclient.PendingOutbound, vaultPubKey, vaultEddsa string) bool {
	sol := common.SOLChain.String()
	for _, p := range pending {
		if p.Chain != sol {
			continue
		}
		if (vaultPubKey != "" && p.VaultPubKey == vaultPubKey) ||
			(vaultEddsa != "" && p.VaultPubKeyEddsa == vaultEddsa) {
			return true
		}
	}
	return false
}

// nonceProvisionCheck evaluates every vault this node is a member of at a shared
// keep-alive cadence boundary (anchorHeight = boundary × cadence, identical across
// members and ≤ tip). Active vaults are provisioned with a nonce account when the
// MimirKeySOLDurableNonce gate is on; retiring vaults have their nonce account closed
// unconditionally so reclaimed rent is never stranded (a no-op when none exists).
//
// This mimir gates PROVISIONING (here) and the SignTx durable path; both read it at a
// shared consensus height (durableNonceMimirAtHeight), so provisioning and signing
// agree on whether the feature is on and a governance flip cannot fragment a keysign
// (N3/N10).
func (c *SOLClient) nonceProvisionCheck(anchorHeight int64) {
	nodeStatus, err := c.bridge.FetchNodeStatus()
	if err != nil {
		c.logger.Err(err).Msg("fail to get node status")
		return
	}
	if nodeStatus != ttypes.NodeStatus_Active {
		return
	}
	// Re-attest wedged vault nonces before the mimir read and the signing-halt check:
	// it observes and does not sign, it is needed for vaults already durable whatever
	// SOLDurableNonce says (the mimir gates only new provisioning), and HaltSigningSOL
	// is exactly when it is needed.
	c.reattestVaultNonces(anchorHeight)

	// Read the gate at the shared anchor height (not a live per-node read), matching
	// SignTx, so provisioning and signing agree on whether the feature is on (N3/N10).
	enabled, err := c.durableNonceMimirAtHeight(anchorHeight)
	if err != nil {
		c.logger.Err(err).Msgf("fail to get %s mimir at height %d", constants.MimirKeySOLDurableNonce, anchorHeight)
		return
	}
	provisioningEnabled := enabled > 0
	if signing.IsSigningHalted(c.bridge, c.cfg.ChainID, c.logger) {
		return
	}
	// An unhealthy scanner signals a degraded node that should not initiate keysigns.
	if !c.IsBlockScannerHealthy() {
		return
	}

	nodePubKey := c.pubkeyMgr.GetNodePubKey(common.SigningAlgoSecp256k1)
	if nodePubKey.IsEmpty() {
		return
	}

	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		c.logger.Err(err).Msg("fail to get current asgards")
		return
	}

	// D: a create keysign must never contend with an outbound keysign for the same
	// vault. Read the not-yet-observed outbound queue AT THE BOUNDARY HEIGHT (shared
	// consensus state), so every member derives the same skip set — otherwise nodes
	// would split on whether to join the create keysign. If we cannot read it, skip
	// provisioning this boundary (fail closed: never risk a collision) and retry.
	reader, ok := c.bridge.(pendingOutboundReader)
	if !ok {
		return
	}
	pending, perr := reader.GetPendingOutbounds(anchorHeight)
	if perr != nil {
		c.logger.Err(perr).Int64("anchor_height", anchorHeight).
			Msg("fail to read pending outbounds at boundary; skipping durable-nonce provisioning this cadence")
		return
	}

	for _, vault := range vaults {
		if !vault.Contains(nodePubKey) {
			// not a member of this vault, its members handle it
			continue
		}
		if vault.PubKeyEddsa.IsEmpty() {
			continue
		}
		// N8 (3.20.4): the retiring-vault close trigger is DISABLED. Closing a nonce
		// account at the START of Retiring strips durable-nonce protection from exactly
		// the churn-migration outbounds this feature exists to protect, and it was not
		// serialised against in-flight signing. For 3.20.4 we do not close: a retiring
		// vault's nonce account simply strands its rent (~0.0015 SOL per vault per churn,
		// accepted + documented). The follow-up design is close-when-drained, sequenced
		// before the final migrate; its close-tx primitives (types.NewCloseNonceAccountTransaction
		// / WithdrawNonce) are retained + tested for that follow-up. See the MR "NOT
		// safe to enable" section (N8/N11).
		if vault.Status == ttypes.VaultStatus_ActiveVault && provisioningEnabled {
			// Skip the create if the vault has any pending SOL outbound at this boundary
			// (its outbound keysign may be in flight) — retry next boundary. Derived from
			// the shared boundary-height queue, so all members skip identically (D).
			if vaultHasPendingSOLOutbound(pending, vault.PubKey.String(), vault.PubKeyEddsa.String()) {
				continue
			}
			if err := c.provisionNonceForVault(vault, anchorHeight); err != nil {
				c.logger.Err(err).Str("vault_pubkey", vault.PubKey.String()).
					Msg("fail to provision durable-nonce account")
			}
		}
	}
}

// provisionNonceForVault creates and initializes the vault's durable-nonce account
// if it does not already exist. Idempotent: it checks the finalized chain first and
// skips when the account is already present, so re-running every boundary (and across
// nodes) is safe. The vault funds the rent + fee; a vault too poor to cover it is
// skipped (its outbounds keep using the recent-blockhash path). The recent blockhash
// is the consensus one for anchorHeight, so all members' create txs are byte-identical.
func (c *SOLClient) provisionNonceForVault(vault ttypes.Vault, anchorHeight int64) error {
	vaultAddr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return fmt.Errorf("fail to parse vault pubkey: %w", err)
	}
	nonceAddr, err := c.nonceAddressForVault(vaultPubKey)
	if err != nil {
		return fmt.Errorf("fail to derive nonce address: %w", err)
	}

	// Derive the consensus blockhash + anchor slot for this boundary up front: the
	// blockhash seeds a byte-identical create tx across members, and the anchor slot
	// pins the idempotency existence check to one finalized frontier (N3/N10).
	recentBlockhash, anchorSlot, err := c.cachedConsensusBlockHash(anchorHeight)
	if err != nil {
		return fmt.Errorf("fail to derive consensus blockhash: %w", err)
	}
	if recentBlockhash == "" || anchorSlot <= 0 {
		// No finalized consensus block for this height yet; retry next boundary.
		return nil
	}

	// Idempotent: already provisioned -> nothing to do (read at the anchor slot).
	if _, _, gErr := c.rpcClient.GetNonceAccount(nonceAddr.String(), uint64(anchorSlot)); gErr == nil {
		return nil
	} else if !errors.Is(gErr, rpc.ErrNonceAccountNotProvisioned) {
		// transport error; retry next boundary
		return fmt.Errorf("fail to check nonce account: %w", gErr)
	}

	balance, err := c.rpcClient.GetBalance(vaultAddr.String(), "finalized", 0)
	if err != nil {
		return fmt.Errorf("fail to get vault balance: %w", err)
	}
	required := new(big.Int).Add(
		new(big.Int).SetUint64(nonceAccountRentLamports),
		big.NewInt(nonceProvisionFeeMarginLamports),
	)
	if balance.Cmp(required) < 0 {
		c.logger.Info().
			Str("vault", vaultAddr.String()).
			Str("balance", balance.String()).
			Str("required", required.String()).
			Msg("vault balance too low to provision durable-nonce account, skipping")
		return nil
	}

	// The address is public, so anyone can pre-fund it, and CreateAccountWithSeed then
	// fails ("account already in use") at every boundary. A System-owned account with
	// no data is adopted in place instead; any other state cannot be provisioned here,
	// so skip it without a keysign.
	meta, err := c.rpcClient.GetAccountMeta(nonceAddr.String())
	if err != nil {
		return fmt.Errorf("fail to read nonce address: %w", err)
	}
	solTx, ok := nonceProvisionTx(meta, vaultPubKey, nonceAddr, recentBlockhash)
	if !ok {
		c.logger.Warn().Str("vault", vaultAddr.String()).Str("nonce_account", nonceAddr.String()).
			Str("owner", meta.Owner).Uint64("space", meta.Space).
			Msg("durable-nonce address holds an account that cannot be provisioned; skipping")
		return nil
	}
	c.logger.Info().
		Str("vault", vaultAddr.String()).
		Str("nonce_account", nonceAddr.String()).
		Int64("anchor_height", anchorHeight).
		Msg("provisioning durable-nonce account")

	txid, err := c.signAndBroadcastNonceTx(solTx, vault, anchorHeight, "nonce-provision")
	if err != nil {
		return err
	}
	if txid != "" {
		c.logger.Info().
			Str("vault", vaultAddr.String()).
			Str("nonce_account", nonceAddr.String()).
			Str("txid", txid).
			Msg("broadcast durable-nonce provisioning tx")
	}
	return nil
}

// signAndBroadcastNonceTx TSS-signs an autonomous nonce management transaction (the
// vault is account index 0 / the sole signer) and broadcasts it. Unlike a scheduled
// outbound it does NOT post a keysign failure to THORChain on error — there is no
// scheduled TxOutItem to slash against, and a failed keysign simply retries on the
// next cadence boundary with a fresh blockhash. Returns an empty txid (nil error)
// when this node was not selected for the keysign — another member broadcasts.
func (c *SOLClient) signAndBroadcastNonceTx(solTx types.Transaction, vault ttypes.Vault, anchorHeight int64, memo string) (string, error) {
	// Hold the per-vault lock through keysign+broadcast — the SAME mutex the outbound
	// signer takes (GetVaultLock) — so a nonce keysign never runs concurrently with an
	// outbound keysign for this vault on this node (no local TSS-party contention).
	lock := c.GetVaultLock(vault.PubKey.String())
	lock.Lock()
	defer lock.Unlock()

	toi := stypes.TxOutItem{
		Chain:            common.SOLChain,
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		Height:           anchorHeight,
		Memo:             memo,
	}

	signedTx, err := c.signTransaction(solTx, toi, anchorHeight)
	if err != nil {
		if errors.Is(err, tss.ErrNotSelectedForKeysign) {
			return "", nil
		}
		return "", fmt.Errorf("fail to sign nonce tx: %w", err)
	}

	signedBytes, err := signedTx.Serialize()
	if err != nil {
		return "", fmt.Errorf("fail to serialize signed nonce tx: %w", err)
	}

	txid, err := c.BroadcastTx(toi, signedBytes)
	if err != nil {
		return "", fmt.Errorf("fail to broadcast nonce tx: %w", err)
	}
	return txid, nil
}

// nonceProvisionTx picks how to provision the vault's nonce account from the current
// state of its address: create it when absent, adopt it in place when it is a plain
// System account someone pre-funded, and nothing (ok=false) for any other state.
func nonceProvisionTx(meta rpc.AccountMeta, vault, nonceAddr types.PublicKey, recentBlockhash string) (types.Transaction, bool) {
	switch {
	case !meta.Exists:
		return types.NewCreateNonceAccountTransaction(vault, nonceAddr, solNonceSeed, nonceAccountRentLamports, recentBlockhash), true
	case meta.Owner == types.SystemProgramID.String() && meta.Space == 0 && !meta.Executable:
		return types.NewAdoptNonceAccountTransaction(vault, nonceAddr, solNonceSeed, nonceAccountRentLamports, recentBlockhash), true
	default:
		return types.Transaction{}, false
	}
}
