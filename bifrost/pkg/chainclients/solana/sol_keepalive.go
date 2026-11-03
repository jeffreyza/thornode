package solana

import (
	"fmt"
	"math/big"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

////////////////////////////////////////////////////////////////////////////////////////
// sol_keepalive.go — vault keep-alive self-sign
//
// When trading is halted (or the chain is simply quiet), organic outbounds stop and
// the newest signature for a vault address ages out of a non-archival RPC's history
// retention. The scanner's getSignaturesForAddress until-cursor then anchors to a
// signature the RPC can no longer resolve (-32020), wedging observation for the
// vault. Instead of forcing every node onto archival RPCs, the keep-alive lands one
// minimal on-chain transaction per stale vault so the cursor re-anchors at a recent
// slot.
//
// The keep-alive is a TSS-signed vault→self transfer with the `consolidate` memo —
// the same "network sends to itself" primitive the UTXO clients use for UTXO
// consolidation. Thornode's ConsolidateHandler accepts any asgard self-send without
// requiring a scheduled TxOutItem, and the generic observation accounting nets the
// vault record to minus the transaction fee. No thornode change is involved.
//
// Node coordination mirrors the UTXO consolidate: every vault member evaluates the
// same trigger against the same chain facts (current slot, newest vault signature)
// and constructs a byte-identical transaction (the amount is derived from the
// newest signature's slot), so TSS keysign parties converge without any explicit
// coordination. Nodes that are not members of a vault skip it.
////////////////////////////////////////////////////////////////////////////////////////

const (
	// keepAlivePollInterval is how often the loop checks the THORChain block height
	// for a cadence-boundary crossing. Kept well under the boundary spacing so every
	// member detects the same crossing promptly; at ~300ms slots every second of
	// detection delay spends ~3 of the 150 blocks the anchor blockhash stays valid.
	keepAlivePollInterval = 2 * time.Second

	// keepAliveThorBlockCadence is the spacing, in THORChain blocks, of the
	// keep-alive evaluation boundaries (~5 min at 6s/block). The keysign anchor for
	// a crossing is boundary × this cadence — a THORChain height that is (a) ≤ the
	// tip by construction (a node only fires after crossing it), so it is a VALID
	// signing_height the keysign-party query accepts, and (b) identical across every
	// member crossing the same boundary, so they batch into one keysign session
	// (go-tss batches by {algo, height}) and seed an identical self-send amount. A
	// Solana slot cannot serve here: it far exceeds the THORChain tip, so the party
	// query rejects it ("block height not available yet").
	keepAliveThorBlockCadence = 50

	// keepAliveFeeMarginLamports is the balance headroom required beyond the
	// self-send amount so the transaction fee is always payable.
	keepAliveFeeMarginLamports = 100_000
)

// keepAliveLoop watches the THORChain block height and fires the keep-alive once
// per shared cadence boundary. The keysign height MUST be a valid THORChain height
// (≤ tip — the keysign-party query rejects anything higher) AND identical across
// members (go-tss batches keysign tasks by {algo, height}). Deriving the anchor
// from a THORChain-block boundary satisfies both: THORChain height is consensus
// state every member agrees on, boundary×cadence is ≤ the firing node's tip, and
// every member crossing the same boundary derives the same value — exactly the
// property scheduled outbounds get from their consensus txOut.Height. The Solana
// scan slot is still read, but only as the staleness reference (a Solana slot is
// invalid as a THORChain signing height).
func (c *SOLClient) keepAliveLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(keepAlivePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			thorHeight, err := c.bridge.GetBlockHeight()
			if err != nil || thorHeight <= 0 {
				continue
			}
			// Keep-alive on its cadence. A fresh node (last == 0) seeds the baseline
			// from the current boundary WITHOUT signing (that boundary's height can be a
			// full cadence behind the tip -> an already-expired blockhash), self-healing
			// on the next boundary. A transient scan-slot read failure retries without
			// advancing the baseline.
			if kaBoundary := c.nextKeepAliveThorMultiple(thorHeight); kaBoundary > c.getKeepAliveThorMultiple() {
				advance := true
				if c.getKeepAliveThorMultiple() != 0 {
					scanSlot, err := c.solScanner.ScanHeight()
					if err != nil || scanSlot <= 0 {
						advance = false
					} else {
						c.keepAliveCheck(scanSlot, kaBoundary*keepAliveThorBlockCadence)
					}
				}
				if advance {
					c.setKeepAliveThorMultiple(kaBoundary)
				}
			}

			// Durable-nonce provisioning on its OWN finer cadence, independent of the
			// keep-alive boundary. Same fresh-node seed (skip the first stale boundary).
			if provBoundary := c.nextNonceProvisionThorMultiple(thorHeight); provBoundary > c.getNonceProvisionThorMultiple() {
				if c.getNonceProvisionThorMultiple() != 0 {
					c.nonceProvisionCheck(provBoundary * nonceProvisionThorBlockCadence)
				}
				c.setNonceProvisionThorMultiple(provBoundary)
			}
		}
	}
}

// ShouldKeepAlive reports whether the THORChain height has crossed a new keep-alive
// cadence boundary since this node last acted. Because THORChain height is
// consensus state, every member crosses boundaries together.
func (c *SOLClient) ShouldKeepAlive(thorHeight int64) bool {
	return c.nextKeepAliveThorMultiple(thorHeight) > c.getKeepAliveThorMultiple()
}

// nextKeepAliveThorMultiple returns the boundary this node should act on for the
// given THORChain height. Mirrors nextSolvencyReportSlotMultiple: a fresh node
// jumps straight to the current boundary instead of replaying history, and
// otherwise advances one boundary at a time so members with slightly different
// tips still converge on the same next boundary.
func (c *SOLClient) nextKeepAliveThorMultiple(thorHeight int64) int64 {
	current := c.getKeepAliveThorMultiple()
	if keepAliveThorBlockCadence <= 0 {
		return current
	}
	crossedMultiple := thorHeight / keepAliveThorBlockCadence
	if thorHeight%keepAliveThorBlockCadence == 0 {
		crossedMultiple--
	}
	if crossedMultiple <= current {
		return current
	}
	if current == 0 && crossedMultiple > 1 {
		return crossedMultiple
	}
	return current + 1
}

func (c *SOLClient) getKeepAliveThorMultiple() int64 {
	c.keepAliveThorMultipleMu.Lock()
	defer c.keepAliveThorMultipleMu.Unlock()
	return c.keepAliveThorMultiple
}

func (c *SOLClient) setKeepAliveThorMultiple(v int64) {
	c.keepAliveThorMultipleMu.Lock()
	defer c.keepAliveThorMultipleMu.Unlock()
	c.keepAliveThorMultiple = v
}

// keepAliveCheck evaluates every vault this node is a member of at a shared
// cadence boundary. currentSlot is the current Solana scan slot (the staleness
// reference); anchorHeight is the boundary's canonical THORChain height (boundary
// × cadence) — identical across members and ≤ tip — handed to SignTx as the keysign
// height and used to seed the self-send amount, so every member's keysign
// coordinates on the same valid value.
func (c *SOLClient) keepAliveCheck(currentSlot, anchorHeight int64) {
	refreshBlocks, err := c.bridge.GetMimir(constants.MimirKeySignatureRefreshBlocks)
	if err != nil {
		c.logger.Err(err).Msgf("fail to get %s mimir", constants.MimirKeySignatureRefreshBlocks)
		return
	}
	if refreshBlocks <= 0 {
		return // keep-alive disabled
	}

	nodeStatus, err := c.bridge.FetchNodeStatus()
	if err != nil {
		c.logger.Err(err).Msg("fail to get node status")
		return
	}
	if nodeStatus != ttypes.NodeStatus_Active {
		return
	}
	if signing.IsSigningHalted(c.bridge, c.cfg.ChainID, c.logger) {
		return
	}
	// The signed blockhash is now derived from the consensus keysign height
	// (consensusBlockHash), not the scanner, so the scanner no longer feeds the
	// signed message. An unhealthy scanner still signals a degraded node that
	// should not be initiating keysigns, so keep it as a liveness gate.
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

	// currentSlot (Solana scan slot) gates staleness; anchorHeight (the shared
	// THORChain boundary) is the keysign height + amount seed. Deliberately NOT
	// c.rpcClient.GetSlot()/a per-node GetBlockHeight read: those desync the party
	// and message. anchorHeight comes from the shared boundary the loop crossed.
	for _, vault := range vaults {
		if !vault.Contains(nodePubKey) {
			// not a member of this vault, its members handle the keep-alive
			continue
		}
		if vault.PubKeyEddsa.IsEmpty() {
			continue
		}
		if err := c.keepAliveVault(vault, uint64(currentSlot), refreshBlocks, anchorHeight); err != nil {
			c.logger.Err(err).
				Str("vault_pubkey", vault.PubKey.String()).
				Msg("fail to keep alive vault")
		}
	}
}

func (c *SOLClient) keepAliveVault(vault ttypes.Vault, currentSlot uint64, refreshBlocks, anchorHeight int64) error {
	addr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultAddr := addr.String()

	// The newest signature for the address, regardless of whether it was an
	// observable transfer — any transaction re-anchors the scanner cursor.
	sigs, err := c.rpcClient.GetSignaturesForAddressUntil(vaultAddr, rpc.GetSignaturesForAddressUntilParams{
		Commitment: "finalized",
		Limit:      1,
	})
	if err != nil {
		return fmt.Errorf("fail to get newest signature: %w", err)
	}
	if len(sigs) == 0 {
		// A vault with no history has no cursor to keep fresh.
		return nil
	}
	newest := sigs[0]
	if !keepAliveDue(currentSlot, newest.Slot, refreshBlocks) {
		return nil
	}

	// Seed the amount from the shared anchorHeight, NOT the per-node newest.Slot:
	// the amount is serialized into the TSS-signed Solana message, so a per-node
	// seed would make the message bytes differ across members and the keysign could
	// not pair. anchorHeight is identical across the whole party.
	amount := keepAliveAmount(uint64(anchorHeight))

	// Balance guard: the transfer momentarily debits the full amount before
	// crediting it back, so the account must hold amount plus fee headroom.
	balance, err := c.rpcClient.GetBalance(vaultAddr, "finalized", 0)
	if err != nil {
		return fmt.Errorf("fail to get vault balance: %w", err)
	}
	requiredLamports := new(big.Int).Add(
		convertToLamports(amount),
		big.NewInt(keepAliveFeeMarginLamports),
	)
	if balance.Cmp(requiredLamports) < 0 {
		c.logger.Info().
			Str("vault", vaultAddr).
			Str("balance", balance.String()).
			Str("required", requiredLamports.String()).
			Msg("vault balance too low for keep-alive, skipping")
		return nil
	}

	toi := stypes.TxOutItem{
		Height:           anchorHeight,
		Chain:            common.SOLChain,
		ToAddress:        addr,
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		Coins: common.Coins{
			common.NewCoin(common.SOLAsset, amount),
		},
		Memo:    mem.NewConsolidateMemo().String(),
		MaxGas:  common.Gas{common.SOLChain.NativeGasToThorCoin(cosmos.NewUint(baseFeePerSigLamports))},
		GasRate: baseFeePerSigLamports,
	}

	c.logger.Info().
		Str("vault", vaultAddr).
		Uint64("newest_sig_slot", newest.Slot).
		Uint64("current_slot", currentSlot).
		Int64("anchor_height", anchorHeight).
		Str("amount", amount.String()).
		Msg("vault signature is stale, sending keep-alive self-sign")

	// anchorHeight is the shared, valid keysign height: every member derives the
	// same THORChain-block boundary (≤ tip), so they batch into one keysign session
	// — the property scheduled outbounds get from their consensus txOut.Height.
	signed, _, _, err := c.SignTx(toi, anchorHeight)
	if err != nil {
		return fmt.Errorf("fail to sign keep-alive tx: %w", err)
	}
	if len(signed) == 0 {
		// not selected for keysign, or the signer cache already covers this round
		return nil
	}

	txid, err := c.BroadcastTx(toi, signed)
	if err != nil {
		return fmt.Errorf("fail to broadcast keep-alive tx: %w", err)
	}
	c.logger.Info().
		Str("vault", vaultAddr).
		Str("txid", txid).
		Msg("broadcast keep-alive self-sign")
	return nil
}

// solSlotsPerThorchainBlock converts the THORChain-block threshold to Solana slots.
// Both sides come from common.Chain.ApproximateBlockMilliseconds, the single SOL
// slot-time constant the scanner pacing also uses (6000ms / 300ms = 20).
func solSlotsPerThorchainBlock() uint64 {
	return uint64(common.THORChain.ApproximateBlockMilliseconds() / common.SOLChain.ApproximateBlockMilliseconds())
}

// keepAliveDue returns true when the newest signature for a vault is older than
// the refresh threshold. Both inputs are chain facts, so every node evaluates the
// same result.
func keepAliveDue(currentSlot, newestSigSlot uint64, refreshBlocks int64) bool {
	if refreshBlocks <= 0 || currentSlot <= newestSigSlot {
		return false
	}
	thresholdSlots := uint64(refreshBlocks) * solSlotsPerThorchainBlock()
	return currentSlot-newestSigSlot > thresholdSlots
}

// keepAliveAmount derives the self-send amount (1e8 base units) from the slot of
// the newest signature being refreshed. All nodes derive an identical amount (so
// TSS parties sign identical bytes), each refresh round gets a distinct amount
// (so its CacheHash never collides with a previous round in the signer cache),
// and the result always clears the scanner dust threshold (so both sides of the
// self-send are observed and the vault record nets exactly minus the fee).
func keepAliveAmount(anchorSlot uint64) cosmos.Uint {
	dust := common.SOLChain.DustThreshold()
	jitter := anchorSlot % dust.Uint64()
	return dust.MulUint64(2).AddUint64(jitter)
}
