package thorchain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var errSolvencyVoterQuotaExceeded = errors.New("solvency voter creation limit reached; retry in a later THORChain window")

type solvencyVoterQuota struct {
	chain  common.Chain
	signer cosmos.AccAddress
	window uint64
	used   uint64
}

// checkSolvencyVoterQuota is called only for a new voter, with authenticated
// active signers. It selects one available allowance without writing state.
// The caller commits the charge only after processing succeeds, in the same
// transaction cache as the voter. An exhausted signer cannot block other
// verified signers from funding creation of a shared quorum payload.
//
// TODO: Add bounded retention for solvency voters, including replay protection
// and expiry rules for pending voters. This quota only limits creation rate;
// honest fragmentation and sustained spam still accumulate records.
func checkSolvencyVoterQuota(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, signers []cosmos.AccAddress) (*solvencyVoterQuota, error) {
	windowBlocks, limit := chain.SolvencyVoterQuota()
	if windowBlocks <= 0 || limit == 0 {
		return nil, nil
	}
	height := ctx.BlockHeight()
	if height <= 0 {
		return nil, fmt.Errorf("invalid THORChain height for solvency admission: %d", height)
	}
	window := uint64(height / windowBlocks)

	// Never depend on local arrival order or map iteration for consensus state.
	ordered := append([]cosmos.AccAddress(nil), signers...)
	sort.Slice(ordered, func(i, j int) bool { return bytes.Compare(ordered[i], ordered[j]) < 0 })
	for _, signer := range ordered {
		previousWindow, used, err := k.GetSolvencyVoterQuota(ctx, chain, signer)
		if err != nil {
			return nil, fmt.Errorf("read solvency voter quota: %w", err)
		}
		if previousWindow > window {
			return nil, fmt.Errorf("solvency voter quota is ahead of current window")
		}
		if previousWindow != window {
			used = 0
		}
		if used < limit {
			return &solvencyVoterQuota{chain: chain, signer: signer, window: window, used: used + 1}, nil
		}
	}
	return nil, errSolvencyVoterQuotaExceeded
}

func (q *solvencyVoterQuota) commit(ctx cosmos.Context, k keeper.Keeper) {
	if q != nil {
		k.SetSolvencyVoterQuota(ctx, q.chain, q.signer, q.window, q.used)
	}
}

func processSolvencyAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter *keeper.SolvencyVoter,
	attester cosmos.AccAddress,
	active NodeAccounts,
	s *common.Solvency,
	shouldSlashForDuplicate bool,
) error {
	k := mgr.Keeper()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := observationDelayFlexibility(ctx, k, voter.Chain)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_solvency"),
		telemetry.NewLabel("chain", telemChain(s.Chain)),
	}))

	slasher := mgr.Slasher()

	// The exact-duplicate penalty below remains enabled even when ordinary
	// solvency observation slash accounting is disabled: replaying an identical
	// observation is independent of balance fragmentation.
	slashObservations := voter.Chain.SupportsSolvencyObservationSlashing()

	if !voter.Sign(attester) {
		// Slash for the network having to handle the extra message/s.
		if shouldSlashForDuplicate {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		ctx.Logger().Info("signer already signed MsgSolvency", "signer", attester.String(), "id", s.Id)
		return nil
	}

	if !voter.HasConsensus(active) {
		// Before consensus, slash until consensus.
		if slashObservations {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		return nil
	}

	// from this point , solvency reach consensus
	if voter.ConsensusBlockHeight > 0 {
		// After consensus, only decrement slash points if within the ObservationDelayFlexibility period.
		if slashObservations && (voter.ConsensusBlockHeight+observeFlex) >= ctx.BlockHeight() {
			slasher.DecSlashPoints(slashCtx, lackOfObservationPenalty, attester)
		}
		// solvency tx already processed
		return nil
	}
	vault, err := k.GetVault(ctx, voter.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return fmt.Errorf("fail to get vault: %w", err)
	}

	voter.ConsensusBlockHeight = ctx.BlockHeight()

	// If solvency stops reaching consensus for a chain, insolvencyCheck never runs
	// for it again and a drained wallet goes undetected. This gauge going flat
	// while the block height advances is the only signal of that.
	telemetry.SetGaugeWithLabels(
		[]string{"thornode", "solvency", "last_consensus_height"},
		float32(ctx.BlockHeight()),
		// Voters are per vault: without the pubkey a retiring vault that stops
		// reaching consensus is hidden behind the active one's updates.
		[]metrics.Label{telemetry.NewLabel("chain", telemChain(voter.Chain)), telemetry.NewLabel("pubkey", voter.PubKey.String())},
	)

	// This signer brings the voter to consensus; increment the signer's slash points like the before-consensus signers,
	// then decrement all the signers' slash points and increment the non-signers' slash points.
	if slashObservations {
		slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		signers := voter.GetSigners()
		nonSigners := getNonSigners(active, signers)
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
		slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		// Fragmented balances must not count as missed observations for churn-out either.
		queueObservationTally(ctx, k, observationTallyKindSolvency, voter.Chain, voter.Id.String())
	}

	// Do checks for whether to act on this consensus or not.
	const StopSolvencyCheckKey = `StopSolvencyCheck`
	stopSolvencyCheck, err := k.GetMimir(ctx, StopSolvencyCheckKey)
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "key", StopSolvencyCheckKey, "error", err)
	}
	if stopSolvencyCheck > 0 && stopSolvencyCheck < ctx.BlockHeight() {
		return nil
	}
	// stop solvency checker can be chain specific
	stopSolvencyCheckChain, err := k.GetMimir(ctx, fmt.Sprintf("%s%s", StopSolvencyCheckKey, voter.Chain.String()))
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "key", StopSolvencyCheckKey+voter.Chain.String(), "error", err)
	}
	if stopSolvencyCheckChain > 0 && stopSolvencyCheckChain < ctx.BlockHeight() {
		return nil
	}
	haltChainKey := fmt.Sprintf(`SolvencyHalt%sChain`, voter.Chain)
	haltChain, err := k.GetMimir(ctx, haltChainKey)
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "error", err)
	}
	// If the solvency message is from a height which does not reflect inbounds
	// reflected in the supermajority-observation vault balances,
	// do not act on it.
	lastChainHeight, err := k.GetLastChainHeight(ctx, voter.Chain)
	if err != nil {
		ctx.Logger().Error("fail to get last chain height", "chain", voter.Chain, "error", err)
	}
	// According to the validate msg.Id check, the Height is consistent for all the voter's messages.
	if !slashObservations {
		// Balance reads on chains without ordinary observation slashing may
		// not be pinned to the canonical report height.
		// These #3098 metrics mitigate the operational risk by exposing stale
		// consensus discards; they do not change the existing discard decision.
		heightResult := "not_stale"
		if err != nil {
			heightResult = "unavailable"
		} else {
			heightDelta := lastChainHeight - s.Height
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "solvency", "last_chain_height_delta"},
				float32(heightDelta),
				[]metrics.Label{telemetry.NewLabel("chain", telemChain(voter.Chain))},
			)
		}
		if s.Height < lastChainHeight {
			heightResult = "stale"
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "solvency", "height_check_total"},
			float32(1),
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(voter.Chain)),
				telemetry.NewLabel("result", heightResult),
			},
		)
	}
	if s.Height < lastChainHeight {
		// A chain whose solvency scanner persistently lags observation hits this
		// branch every cycle, which means its insolvency detection is entirely
		// inert. Today that is only an Info log.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "solvency", "report_stale"},
			1,
			[]metrics.Label{telemetry.NewLabel("chain", telemChain(voter.Chain))},
		)
		ctx.Logger().Info("solvency message consensus for height before last chain height inbound supermajority observation", "chain", voter.Chain, "vault pubkey", voter.PubKey, "last chain height", lastChainHeight, "solvency message height", s.Height)
		return nil
	}

	isInsolvent := insolvencyCheck(ctx, mgr, vault, voter.Coins, voter.Chain)

	if isInsolvent {
		// Flag this specific vault as insolvent so it is excluded from
		// inbound-address election on every chain it serves, not just the
		// halted one. The insolvency is usually caused by a compromised key,
		// which leaves the vault's other (still-solvent, unhalted) chains
		// exposed to fresh deposits until the vault stops being advertised.
		flagVaultInsolvent(ctx, k, voter.PubKey, voter.Chain)
	} else {
		// Vault is solvent again on this chain; clear its per-chain exclusion
		// flag so it can be re-elected for inbound addresses once solvent on
		// every chain it serves.
		clearVaultInsolvent(ctx, k, voter.PubKey, voter.Chain)
	}

	// If the chain was halted this block, leave it halted without overriding.
	// (For instance if halted because of a different vault which is insolvent.)
	// Also don't unhalt if the chain was manually halted for a future height
	// or indefinitely ('1'). The per-vault flag was already updated above so
	// vault recovery is not blocked on chain-wide halt state.
	if haltChain >= ctx.BlockHeight() || haltChain == 1 {
		return nil
	}

	// If insolvent: halt the chain if unhalted, or refresh the halt height if already halted
	// from an earlier block. Refreshing the halt height prevents a different solvent vault
	// from unhalting the chain while this vault remains insolvent.
	if isInsolvent {
		if haltChain <= 0 {
			// Attribute the halt to the automatic solvency checker. Transition only:
			// the refresh branch below re-fires every cycle while a vault stays
			// insolvent. thornode_chain_halted carries no cause.
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "solvency", "chain_halt"},
				1,
				[]metrics.Label{telemetry.NewLabel("chain", telemChain(voter.Chain))},
			)
			k.SetMimir(ctx, haltChainKey, ctx.BlockHeight())
			mimirEvent := NewEventSetMimir(strings.ToUpper(haltChainKey), strconv.FormatInt(ctx.BlockHeight(), 10))
			if err := mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
				ctx.Logger().Error("fail to emit set_mimir event", "error", err)
			}
			ctx.Logger().Info("chain is insolvent, halt until it is resolved", "chain", voter.Chain)
		} else if haltChain > 1 && haltChain < ctx.BlockHeight() {
			// Refresh halt height to current block so a solvent vault can't unhalt next block
			k.SetMimir(ctx, haltChainKey, ctx.BlockHeight())
		}
	}

	// If not insolvent and the chain is halted from an earlier block height, unhalt the chain.
	// A different insolvent vault on the same chain will have refreshed haltChain to the current
	// block, preventing this unhalt via the guard at line 106 (haltChain >= ctx.BlockHeight()).
	if !isInsolvent && haltChain > 1 {
		// if the chain was halted by previous solvency checker, auto unhalt it
		ctx.Logger().Info("auto un-halt", "chain", voter.Chain, "previous halt height", haltChain, "current block height", ctx.BlockHeight())
		k.SetMimir(ctx, haltChainKey, 0)
		mimirEvent := NewEventSetMimir(strings.ToUpper(haltChainKey), "0")
		if err := mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
			ctx.Logger().Error("fail to emit set_mimir event", "error", err)
		}
	}

	return nil
}

// solvencyHaltVaultKeyPrefix flags a specific vault as insolvent on a specific
// chain. It is keyed by both vault pubkey and chain because a vault can be
// insolvent on one chain while remaining solvent on others; the per-chain
// granularity lets a recovered chain clear its own flag without prematurely
// re-electing a vault that is still insolvent elsewhere.
const solvencyHaltVaultKeyPrefix = "SolvencyHaltVault-"

// solvencyHaltVaultKey returns the Mimir key flagging a vault insolvent on a chain.
func solvencyHaltVaultKey(pubKey common.PubKey, chain common.Chain) string {
	return fmt.Sprintf("%s%s-%s", solvencyHaltVaultKeyPrefix, pubKey.String(), chain.String())
}

// flagVaultInsolvent records that the given vault is insolvent on the given
// chain so it can be excluded from inbound-address election. A compromised key
// that drains one chain leaves the same vault's other chains exposed, so the
// whole vault must stop being advertised for deposits, not just the halted chain.
// It only writes on the transition into insolvency, so the stored height records
// when the vault was first flagged and a still-insolvent vault does not incur a
// state write every solvency cycle.
func flagVaultInsolvent(ctx cosmos.Context, k keeper.Keeper, pubKey common.PubKey, chain common.Chain) {
	key := solvencyHaltVaultKey(pubKey, chain)
	if flagged, err := k.GetMimir(ctx, key); err == nil && flagged > 0 {
		return
	}
	// Transition into insolvency only - the guard above makes the steady state
	// free. This is the moment a vault stops being advertised for deposits, which
	// no existing metric exposes.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "vault", "insolvent_flagged"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(chain)),
			telemetry.NewLabel("pubkey", pubKey.String()),
		},
	)
	k.SetMimir(ctx, key, ctx.BlockHeight())
}

// clearVaultInsolvent clears the per-chain insolvency flag once the vault is
// observed solvent again on that chain, allowing it to be re-elected for deposits.
// It only writes when the flag is actually set, so the common case - a solvent
// vault reporting solvency every cycle - does not create a zeroed entry for every
// vault+chain and bloat the Mimir store.
func clearVaultInsolvent(ctx cosmos.Context, k keeper.Keeper, pubKey common.PubKey, chain common.Chain) {
	key := solvencyHaltVaultKey(pubKey, chain)
	flagged, err := k.GetMimir(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to read solvency-halt vault mimir", "pub_key", pubKey.String(), "chain", chain.String(), "error", err)
		return
	}
	if flagged > 0 {
		k.SetMimir(ctx, key, 0)
	}
}

// isVaultInsolvent reports whether the vault is flagged insolvent on any of the
// chains it serves. On KV read failure it returns true so callers fail closed:
// an unverifiable vault must not be advertised for deposits.
func isVaultInsolvent(ctx cosmos.Context, k keeper.Keeper, vault Vault) (bool, error) {
	for _, chain := range vault.GetChains() {
		flagged, err := k.GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, chain))
		if err != nil {
			return true, err
		}
		if flagged > 0 {
			return true, nil
		}
	}
	return false, nil
}

// insolvencyCheck compare the coins in vault against the coins report by solvency message
// insolvent usually means vault has more coins than wallet
// return true means the vault is insolvent , the network should halt , otherwise false
func insolvencyCheck(ctx cosmos.Context, mgr Manager, vault Vault, coins common.Coins, chain common.Chain) bool {
	adjustVault, pendingOutbounds, err := excludePendingOutboundFromVault(ctx, mgr, vault, chain)
	if err != nil {
		ctx.Logger().Error("fail to exclude pending outbound from vault, assuming insolvent", "error", err)
		return true
	}
	originalAmounts := make(map[string]cosmos.Uint, len(vault.Coins))
	for _, c := range vault.Coins {
		if !c.Asset.Chain.Equals(chain) {
			continue
		}
		originalAmounts[c.Asset.String()] = c.Amount
	}
	// Get the permitted solvency gap in RUNE (converted from USD)
	permittedGapInRune := mgr.Keeper().DollarConfigInRune(ctx, constants.PermittedSolvencyGapUSD)
	// Use the coin in vault as baseline , wallet can have more coins than vault
	for _, c := range adjustVault.Coins {
		if !c.Asset.Chain.Equals(chain) {
			continue
		}
		if c.IsEmpty() {
			// Pending outbounds can fully cover, or intentionally over-cover,
			// the vault balance while outbound recovery handles the liability.
			// The asset is still a real asset in this vault, and its adjusted
			// balance reaching zero is exactly how a gap resolves, so write the
			// zero here too: skipping it leaves a previously positive
			// solvency_gap_rune pinned at its last value until retention expires,
			// while insolvencyCheck returns false and the caller clears the flag.
			// Guarded on the asset rather than the coin so a malformed coin with
			// an empty asset never becomes a label value.
			if !c.Asset.IsEmpty() {
				telemetry.SetGaugeWithLabels(
					[]string{"thornode", "solvency", "gap_rune"},
					0,
					[]metrics.Label{
						telemetry.NewLabel("asset", c.Asset.String()),
						telemetry.NewLabel("pubkey", vault.PubKey.String()),
					},
				)
			}
			continue
		}
		walletCoin := coins.GetCoin(c.Asset)
		pendingAmount := pendingOutbounds[c.Asset.String()]
		originalAmount := originalAmounts[c.Asset.String()]

		// Compute the gap between vault and wallet balances.
		gap := cosmos.ZeroUint()
		if walletCoin.IsEmpty() {
			gap = c.Amount
		} else if c.Amount.GT(walletCoin.Amount) {
			gap = c.Amount.Sub(walletCoin.Amount)
		}
		if gap.IsZero() {
			// Write the zero: without it a resolved gap leaves the series pinned at
			// its last non-zero value and the alert never clears. Labelled by vault
			// as well as asset: insolvencyCheck runs once per reporting vault, so
			// without the pubkey a solvent retiring vault would zero out the active
			// vault's real gap on the same asset during churn.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "solvency", "gap_rune"},
				0,
				[]metrics.Label{
					telemetry.NewLabel("asset", c.Asset.String()),
					telemetry.NewLabel("pubkey", vault.PubKey.String()),
				},
			)
			continue
		}

		// Convert the gap to RUNE value using the pool
		pool, err := mgr.Keeper().GetPool(ctx, c.Asset.GetLayer1Asset())
		if err != nil {
			if pendingRagnarokOutboundsExplainGap(ctx, mgr, nil, c.Asset, originalAmount, walletCoin.Amount, pendingAmount) {
				ctx.Logger().Error(
					"missing pool solvency gap explained by pending ragnarok outbounds",
					"asset", c.Asset.String(),
					"original", originalAmount.String(),
					"wallet", walletCoin.Amount.String(),
					"pending", pendingAmount.String(),
					"gap", gap.String(),
				)
				continue
			}
			ctx.Logger().Error("fail to get pool for solvency check", "asset", c.Asset.String(), "error", err)
			// If we can't get the pool, treat as insolvent to be safe
			return true
		}
		gapInRune := pool.AssetValueInRune(gap)
		// Export the gap itself, not just the bool insolvencyCheck reduces it to.
		// A gap growing but still under PermittedSolvencyGapUSD is the earliest
		// warning of a slow leak, hours before the automatic chain halt. Same
		// {asset, pubkey} label set as the zero-write above - one series per
		// reporting vault, so vaults cannot overwrite each other's gap.
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "solvency", "gap_rune"},
			telem(gapInRune),
			[]metrics.Label{
				telemetry.NewLabel("asset", c.Asset.String()),
				telemetry.NewLabel("pubkey", vault.PubKey.String()),
			},
		)
		// If pool has no liquidity, we can't value the gap in RUNE.
		// Treat as insolvent to be conservative: a zero-liquidity pool should not
		// allow a real insolvency to go undetected.
		if gapInRune.IsZero() && !gap.IsZero() {
			if pendingRagnarokOutboundsExplainGap(ctx, mgr, &pool, c.Asset, originalAmount, walletCoin.Amount, pendingAmount) {
				ctx.Logger().Error(
					"unpriceable solvency gap explained by pending ragnarok outbounds",
					"asset", c.Asset.String(),
					"original", originalAmount.String(),
					"wallet", walletCoin.Amount.String(),
					"pending", pendingAmount.String(),
					"gap", gap.String(),
				)
				continue
			}
			ctx.Logger().Info("cannot value gap for solvency check (pool has no liquidity), treating as insolvent", "asset", c.Asset.String(), "gap", gap.String())
			return true
		}
		if gapInRune.GT(permittedGapInRune) {
			ctx.Logger().Info("vault insolvent", "asset", c.Asset.String(), "vault amount", c.Amount.String(), "wallet amount", walletCoin.Amount.String(), "gap", gap.String(), "gap in rune", gapInRune.String(), "permitted gap in rune", permittedGapInRune.String())
			return true
		}
	}
	return false
}

func pendingRagnarokOutboundsExplainGap(
	ctx cosmos.Context,
	mgr Manager,
	pool *Pool,
	asset common.Asset,
	originalAmount cosmos.Uint,
	walletAmount cosmos.Uint,
	pendingAmount cosmos.Uint,
) bool {
	if originalAmount.IsNil() || originalAmount.IsZero() || pendingAmount.IsNil() || pendingAmount.IsZero() {
		return false
	}
	if walletAmount.IsNil() {
		walletAmount = cosmos.ZeroUint()
	}
	if !mgr.Keeper().IsRagnarok(ctx, []common.Asset{asset.GetLayer1Asset()}) {
		return false
	}
	if pool != nil && !(pool.Status == PoolStaged || pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero()) {
		return false
	}
	if !originalAmount.GT(walletAmount) {
		return false
	}
	shortage := originalAmount.Sub(walletAmount)
	return pendingAmount.GTE(shortage)
}

func excludePendingOutboundFromVault(ctx cosmos.Context, mgr Manager, vault Vault, chain common.Chain) (Vault, map[string]cosmos.Uint, error) {
	// Deep copy vault coins to avoid mutating the caller's slice backing array.
	// Element-by-element copy ensures the pass-by-value contract is honored,
	// even if future code modifies Amount in-place rather than via SafeSub.
	coinsCopy := make(common.Coins, len(vault.Coins))
	for i, c := range vault.Coins {
		coinsCopy[i] = common.NewCoin(c.Asset, c.Amount)
	}
	vault.Coins = coinsCopy
	pendingOutbounds := make(map[string]cosmos.Uint)

	// Scan the longest live signing period for the reported chain, then apply the
	// exact per-item period in deductVaultBlockPendingOutbound. This keeps
	// solvency accounting aligned with LackSigning without retaining ordinary
	// outbounds for too long.
	signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	startHeight := ctx.BlockHeight() - thortypes.MaxTxOutSigningTransactionPeriodForChain(chain, signingPeriod)
	if startHeight < 1 {
		startHeight = 1
	}
	for i := startHeight; i < ctx.BlockHeight(); i++ {
		blockOut, err := mgr.Keeper().GetTxOut(ctx, i)
		if err != nil {
			ctx.Logger().Error("fail to get block tx out", "error", err)
			return vault, pendingOutbounds, fmt.Errorf("fail to get block tx out, err: %w", err)
		}
		vault, pendingOutbounds = deductVaultBlockPendingOutbound(ctx, mgr, vault, blockOut, pendingOutbounds, chain, signingPeriod)
	}
	return vault, pendingOutbounds, nil
}

func deductVaultBlockPendingOutbound(ctx cosmos.Context, mgr Manager, vault Vault, block *TxOut, pendingOutbounds map[string]cosmos.Uint, chain common.Chain, signingPeriod int64) (Vault, map[string]cosmos.Uint) {
	for _, txOutItem := range block.TxArray {
		if !txOutItem.Chain.Equals(chain) {
			continue
		}
		if ctx.BlockHeight()-block.Height > txOutItem.SigningTransactionPeriod(signingPeriod) {
			continue
		}
		if !txOutItem.VaultPubKey.Equals(vault.PubKey) {
			continue
		}
		// only still outstanding txout will be considered
		if !txOutItem.OutHash.IsEmpty() {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(txOutItem.Memo), "RAGNAROK") &&
			!mgr.Keeper().IsRagnarok(ctx, []common.Asset{txOutItem.Coin.Asset.GetLayer1Asset()}) {
			continue
		}
		// deduct the gas asset from the vault as well
		var gasCoin common.Coin
		if !txOutItem.MaxGas.IsEmpty() {
			gasCoin = txOutItem.MaxGas.ToCoins().GetCoin(txOutItem.Chain.GetGasAsset())
		}
		for i, vaultCoin := range vault.Coins {
			if vaultCoin.Asset.Equals(txOutItem.Coin.Asset) {
				addPendingOutbound(pendingOutbounds, txOutItem.Coin)
				vault.Coins[i].Amount = common.SafeSub(vault.Coins[i].Amount, txOutItem.Coin.Amount)
			}
			if vaultCoin.Asset.Equals(gasCoin.Asset) {
				addPendingOutbound(pendingOutbounds, gasCoin)
				vault.Coins[i].Amount = common.SafeSub(vault.Coins[i].Amount, gasCoin.Amount)
			}
		}
	}
	return vault, pendingOutbounds
}

func addPendingOutbound(pendingOutbounds map[string]cosmos.Uint, coin common.Coin) {
	if coin.IsEmpty() {
		return
	}
	key := coin.Asset.String()
	if existing, ok := pendingOutbounds[key]; ok {
		pendingOutbounds[key] = existing.Add(coin.Amount)
		return
	}
	pendingOutbounds[key] = coin.Amount
}
