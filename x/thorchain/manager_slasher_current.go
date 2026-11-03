package thorchain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"cosmossdk.io/core/comet"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Slasher is current implementation of slasher
type SlasherImpl struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newSlasher create a new instance of Slasher
func newSlasher(keeper keeper.Keeper, eventMgr EventManager) *SlasherImpl {
	return &SlasherImpl{keeper: keeper, eventMgr: eventMgr}
}

// BeginBlock called when a new block get proposed to detect whether there are duplicate vote
func (s *SlasherImpl) BeginBlock(ctx cosmos.Context, constAccessor constants.ConstantValues) {
	var doubleSignEvidence []comet.Evidence
	// Iterate through any newly discovered evidence of infraction
	// Slash any validators (and since-unbonded liquidity within the unbonding period)
	// who contributed to valid infractions
	for i := range ctx.CometInfo().GetEvidence().Len() {
		evidence := ctx.CometInfo().GetEvidence().Get(i)
		switch evidence.Type() {
		case comet.DuplicateVote:
			doubleSignEvidence = append(doubleSignEvidence, evidence)
		default:
			ctx.Logger().Error("ignored unknown evidence type", "type", evidence.Type)
		}
	}

	// Identify validators which didn't sign the previous block
	var missingSignAddresses []crypto.Address
	var successfulSignAddresses []crypto.Address
	for i := range ctx.CometInfo().GetLastCommit().Votes().Len() {
		voteInfo := ctx.CometInfo().GetLastCommit().Votes().Get(i)
		if voteInfo.GetBlockIDFlag() != comet.BlockIDFlagAbsent {
			successfulSignAddresses = append(successfulSignAddresses, voteInfo.Validator().Address())
		} else {
			missingSignAddresses = append(missingSignAddresses, voteInfo.Validator().Address())
		}
	}

	// Derive Active node validator addresses once.
	nas, err := s.keeper.ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to list active validators", "error", err)
		return
	}
	var validatorAddresses []nodeAddressValidatorAddressPair
	for _, na := range nas {
		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		if err != nil {
			ctx.Logger().Error("fail to derive validator address", "error", err)
			continue
		}
		var pair nodeAddressValidatorAddressPair
		pair.nodeAddress = na.NodeAddress
		pair.validatorAddress = pk.Address()
		validatorAddresses = append(validatorAddresses, pair)
	}

	// Act on double signs.
	for _, evidence := range doubleSignEvidence {
		if err := s.HandleDoubleSign(ctx, evidence.Validator().Address(), evidence.Height(), constAccessor, validatorAddresses); err != nil {
			ctx.Logger().Error("fail to slash for double signing a block", "error", err)
		}
	}

	// Act on missing signs.
	for _, missingSignAddress := range missingSignAddresses {
		if err := s.HandleMissingSign(ctx, missingSignAddress, constAccessor, validatorAddresses); err != nil {
			ctx.Logger().Error("fail to slash for missing signing a block", "error", err)
		}
	}

	// Act on successful signs.
	for _, successfulSignAddress := range successfulSignAddresses {
		if err := s.HandleSuccessfulSign(ctx, successfulSignAddress, constAccessor, validatorAddresses); err != nil {
			ctx.Logger().Error("fail to mark for successfully signing a block", "error", err)
		}
	}
}

// HandleDoubleSign - slashes a validator for signing two blocks at the same
// block height
// https://blog.cosmos.network/consensus-compare-casper-vs-tendermint-6df154ad56ae
func (s *SlasherImpl) HandleDoubleSign(ctx cosmos.Context, addr crypto.Address, infractionHeight int64, constAccessor constants.ConstantValues, validatorAddresses []nodeAddressValidatorAddressPair) error {
	// check if we're recent enough to slash for this behavior
	maxAge := constAccessor.GetInt64Value(constants.DoubleSignMaxAge)
	if (ctx.BlockHeight() - infractionHeight) > maxAge {
		ctx.Logger().Info("double sign detected but too old to be slashed", "infraction height", infractionHeight, "address", addr.String())
		return nil
	}

	doubleBlockSignSlashPoints := s.keeper.GetConfigInt64(ctx, constants.DoubleBlockSignSlashPoints)

	// First search active validators, then fall back to all validators with bond
	// to handle churned-out validators whose evidence arrives after churn.
	var nodeAddress cosmos.AccAddress
	for _, pair := range validatorAddresses {
		if addr.String() == pair.validatorAddress.String() {
			nodeAddress = pair.nodeAddress
			break
		}
	}
	if nodeAddress.Empty() {
		allValidators, err := s.keeper.ListValidatorsWithBond(ctx)
		if err != nil {
			return fmt.Errorf("fail to list validators with bond: %w", err)
		}
		for _, na := range allValidators {
			pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
			if err != nil {
				continue
			}
			if addr.String() == pk.Address().String() {
				nodeAddress = na.NodeAddress
				break
			}
		}
	}
	if nodeAddress.Empty() {
		return fmt.Errorf("could not find node account with validator address: %s", addr)
	}

	na, err := s.keeper.GetNodeAccount(ctx, nodeAddress)
	if err != nil {
		return err
	}

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("address", na.NodeAddress.String()),
		telemetry.NewLabel("reason", "double_block_sign"),
	}))
	if err := s.keeper.IncNodeAccountSlashPoints(slashCtx, na.NodeAddress, doubleBlockSignSlashPoints); err != nil {
		return fmt.Errorf("fail to increase node account slash point: %w", err)
	}

	return s.keeper.SetNodeAccount(ctx, na)
}

// HandleSuccessfulSign - decrement missing blocks from a validator for signing a block
func (s *SlasherImpl) HandleSuccessfulSign(ctx cosmos.Context, addr crypto.Address, constAccessor constants.ConstantValues, validatorAddresses []nodeAddressValidatorAddressPair) error {
	for _, pair := range validatorAddresses {
		if addr.String() != pair.validatorAddress.String() {
			continue
		}

		na, err := s.keeper.GetNodeAccount(ctx, pair.nodeAddress)
		if err != nil {
			return err
		}

		if na.MissingBlocks == 0 {
			return nil
		}

		// decrement the number of blocks that weren't signed
		na.MissingBlocks -= 1

		return s.keeper.SetNodeAccount(ctx, na)
	}

	return fmt.Errorf("could not find active node account with validator address: %s", addr)
}

// HandleMissingSign - slashes a validator for not signing a block
func (s *SlasherImpl) HandleMissingSign(ctx cosmos.Context, addr crypto.Address, constAccessor constants.ConstantValues, validatorAddresses []nodeAddressValidatorAddressPair) error {
	missBlockSignSlashPoints := s.keeper.GetConfigInt64(ctx, constants.MissBlockSignSlashPoints)
	maxTrack := s.keeper.GetConfigInt64(ctx, constants.MaxTrackMissingBlock)

	for _, pair := range validatorAddresses {
		if addr.String() != pair.validatorAddress.String() {
			continue
		}

		na, err := s.keeper.GetNodeAccount(ctx, pair.nodeAddress)
		if err != nil {
			return err
		}

		slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("address", na.NodeAddress.String()),
			telemetry.NewLabel("reason", "miss_block_sign"),
		}))
		if err := s.keeper.IncNodeAccountSlashPoints(slashCtx, na.NodeAddress, missBlockSignSlashPoints); err != nil {
			ctx.Logger().Error("fail to increase node account slash points", "error", err, "address", na.NodeAddress.String())
		}

		// increment the number of blocks that weren't signed
		na.MissingBlocks += 1
		if na.MissingBlocks > uint64(maxTrack) {
			na.MissingBlocks = uint64(maxTrack)
		}

		return s.keeper.SetNodeAccount(ctx, na)
	}

	return fmt.Errorf("could not find active node account with validator address: %s", addr)
}

// xmrSigningPeriodMultiplier scales SigningTransactionPeriod for XMR
// outbounds. FROST threshold signing is a multi-round p2p protocol that
// takes noticeably longer than ECDSA/EDDSA keysign, and XMR chain
// confirmation + observation round-trip adds further delay. Processing
// XMR outbounds at the same window as other chains races the slasher
// against in-flight signatures — the ToI gets rescheduled before its
// observation arrives, and when the original vault's signed tx finally
// lands on-chain thornode emits a "missing tx out" security event.
// Process XMR outbounds from a 3x-older batch to give FROST and chain
// confirmation enough headroom.
const xmrSigningPeriodMultiplier = types.ExtendedOutboundSigningPeriodMultiplier

// staleOutboundObservationSigningPeriods bounds how long a non-finalised
// observed outbound can pin reassignment or treasury recovery.
const staleOutboundObservationSigningPeriods = 3

// maxRecoveryTxIDNonce bounds the free-key search in generateRecoveryTxID so a
// misbehaving swap queue lookup cannot spin EndBlock.
const maxRecoveryTxIDNonce = 100

// errRecoveryTxIDExhausted marks a recovery that found no free swap ID. The
// ID includes the block height, so LackSigning reschedules the item to retry
// at a later height instead of stranding it.
var errRecoveryTxIDExhausted = errors.New("recovery swap id space exhausted")

// evmAggregatorSigningPeriodMultiplier scales SigningTransactionPeriod for
// EVM swap-out aggregator outbounds. The signer broadcasts a transferOutAndCall
// transaction, but the EVM scanner may not observe and finalise the outbound
// before the standard signing window expires. Rescheduling inside that window
// can create a direct fallback outbound while the original aggregator call is
// still in flight, duplicating the outbound.
const evmAggregatorSigningPeriodMultiplier = types.ExtendedOutboundSigningPeriodMultiplier

// evmTokenSigningPeriodMultiplier scales SigningTransactionPeriod for EVM token
// outbounds. The signer may need to submit an ERC20 approval transaction before
// the actual transfer, so a valid token outbound can observe after the standard
// signing window even when the signer is behaving correctly.
const evmTokenSigningPeriodMultiplier = types.ExtendedOutboundSigningPeriodMultiplier

func isEVMAggregatorTxOut(toi TxOutItem) bool {
	return toi.Chain.IsEVM() && txOutHasAggregator(toi)
}

func isEVMTokenTxOut(toi TxOutItem) bool {
	return toi.Chain.IsEVM() &&
		!isEVMAggregatorTxOut(toi) &&
		!toi.Coin.Asset.Equals(toi.Chain.GetGasAsset())
}

func isObservedEVMAggregatorOutbound(tx common.ObservedTx) bool {
	return tx.Tx.Chain.IsEVM() && observedTxHasAggregator(tx)
}

func isObservedEVMTokenOutbound(tx common.ObservedTx) bool {
	if !tx.Tx.Chain.IsEVM() || len(tx.Tx.Coins) == 0 || isObservedEVMAggregatorOutbound(tx) {
		return false
	}
	return !tx.Tx.Coins[0].Asset.Equals(tx.Tx.Chain.GetGasAsset())
}

func txOutSigningPeriod(toi TxOutItem, signingTransPeriod int64) int64 {
	return toi.SigningTransactionPeriod(signingTransPeriod)
}

// LackSigning slash account that fail to sign tx
func (s *SlasherImpl) LackSigning(ctx cosmos.Context, mgr Manager) error {
	var resultErr error
	maxOutboundAttempts := mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundAttempts)
	signingTransPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	if signingTransPeriod == 0 {
		return fmt.Errorf("invalid signing transaction period: %d", signingTransPeriod)
	}
	enableMemolessOutbound := mgr.Keeper().GetConfigInt64(ctx, constants.EnableMemolessOutbound)

	if ctx.BlockHeight() < signingTransPeriod {
		return nil
	}
	// round up to nearest multiple of RescheduleCoalesceBlocks
	rescheduleHeight := ctx.BlockHeight()
	rescheduleCoalesceBlocks := mgr.Keeper().GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks)
	if rescheduleCoalesceBlocks > 1 {
		overBlocks := rescheduleHeight % rescheduleCoalesceBlocks
		if overBlocks != 0 {
			rescheduleHeight += rescheduleCoalesceBlocks - overBlocks
		}
	}

	// Each pass examines a different TxOut batch and processes items whose
	// timing class matches the pass. Standard outbounds are checked at the
	// SigningTransactionPeriod. XMR, EVM aggregator, and EVM token outbounds
	// are checked at longer periods because their sign/broadcast/observe
	// round-trips can exceed the standard window.
	xmrSigningTransPeriod := signingTransPeriod * xmrSigningPeriodMultiplier
	evmAggregatorSigningTransPeriod := signingTransPeriod * evmAggregatorSigningPeriodMultiplier
	evmTokenSigningTransPeriod := signingTransPeriod * evmTokenSigningPeriodMultiplier
	type rescheduleBatch struct {
		height            int64
		wantXMR           bool
		wantEVMAggregator bool
		wantEVMToken      bool
	}
	batches := []rescheduleBatch{
		{height: ctx.BlockHeight() - signingTransPeriod, wantXMR: false},
	}
	if ctx.BlockHeight() >= xmrSigningTransPeriod {
		batches = append(batches, rescheduleBatch{
			height:  ctx.BlockHeight() - xmrSigningTransPeriod,
			wantXMR: true,
		})
	}
	if ctx.BlockHeight() >= evmAggregatorSigningTransPeriod {
		batches = append(batches, rescheduleBatch{
			height:            ctx.BlockHeight() - evmAggregatorSigningTransPeriod,
			wantEVMAggregator: true,
		})
	}
	if ctx.BlockHeight() >= evmTokenSigningTransPeriod {
		batches = append(batches, rescheduleBatch{
			height:       ctx.BlockHeight() - evmTokenSigningTransPeriod,
			wantEVMToken: true,
		})
	}

	siblingOutHashes := solSiblingCache{}
	for _, batch := range batches {
		height := batch.height
		wantXMR := batch.wantXMR
		wantEVMAggregator := batch.wantEVMAggregator
		wantEVMToken := batch.wantEVMToken
		txs, err := s.keeper.GetTxOut(ctx, height)
		if err != nil {
			return fmt.Errorf("fail to get txout from block height(%d): %w", height, err)
		}

		for i, toi := range txs.TxArray {
			if toi.Chain.Equals(common.XMRChain) != wantXMR {
				// XMR items are processed in the XMR extended-period pass.
				continue
			}
			if isEVMAggregatorTxOut(toi) != wantEVMAggregator {
				// EVM aggregator items are processed in their extended-period pass.
				continue
			}
			if isEVMTokenTxOut(toi) != wantEVMToken {
				// EVM token items are processed in their extended-period pass.
				continue
			}
			if !common.CurrentChainNetwork.SoftEquals(toi.ToAddress.GetNetwork(toi.Chain)) {
				continue // skip this transaction
			}
			if toi.OutHash.IsEmpty() {
				itemSigningTransPeriod := txOutSigningPeriod(toi, signingTransPeriod)

				// Slash node account for not sending funds
				vault, err := s.keeper.GetVault(ctx, toi.VaultPubKey)
				if err != nil {
					ctx.Logger().Error("Unable to get vault", "error", err, "vault pub key", toi.VaultPubKey.String())
					resultErr = fmt.Errorf("failed to get vault: %w", err)
					// the outbound is skipped entirely this pass - it never reaches
					// the reschedule success counter below
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "outbound", "reschedule_failed"},
						float32(1),
						[]metrics.Label{
							telemetry.NewLabel("chain", toi.Chain.String()),
							telemetry.NewLabel("reason", "no_vault"),
						},
					)
					continue
				}

				// if the vault is frozen, reschedule to same vault with no changes
				frozen := false
				if len(vault.Frozen) > 0 {
					var chains common.Chains
					chains, err = common.NewChains(vault.Frozen)
					if err != nil {
						ctx.Logger().Error("failed to convert chains", "error", err)
					}
					if chains.Has(toi.Coin.Asset.GetChain()) {
						etx := common.Tx{
							ID:        toi.InHash,
							Chain:     toi.Chain,
							ToAddress: toi.ToAddress,
							Coins:     []common.Coin{toi.Coin},
							Gas:       toi.MaxGas,
							Memo:      toi.Memo,
						}
						eve := NewEventSecurity(etx, "frozen vault reschedule")
						if err = mgr.EventMgr().EmitEvent(ctx, eve); err != nil {
							ctx.Logger().Error("fail to emit security event", "error", err)
						}
						frozen = true
					}
				}

				memo, _ := ParseMemoWithTHORNames(ctx, s.keeper, toi.GetMemo()) // ignore err
				if memo.IsInternal() {
					// there is a different mechanism for rescheduling outbound
					// transactions for migration transactions
					continue
				}
				var voter ObservedTxVoter
				if memo.IsType(TxRagnarok) {
					// A Ragnarok outbound has no ObservedTxInVoter,
					// so check MaxOutboundAttempts against the memo height.
					ragnarokHeight := memo.GetBlockHeight()
					// Though a negative is not expected to cause problems,
					// nevertheless keep the default 0 as the minimum.
					if ragnarokHeight > 0 {
						voter.FinalisedHeight = ragnarokHeight
					}
				} else {
					voter, err = s.keeper.GetObservedTxInVoter(ctx, toi.InHash)
					if err != nil {
						ctx.Logger().Error("fail to get observed tx voter", "error", err)
						resultErr = fmt.Errorf("failed to get observed tx voter: %w", err)
						telemetry.IncrCounterWithLabels(
							[]string{"thornode", "outbound", "reschedule_failed"},
							float32(1),
							[]metrics.Label{
								telemetry.NewLabel("chain", toi.Chain.String()),
								telemetry.NewLabel("reason", "no_voter"),
							},
						)
						continue
					}
				}

				// Durable-nonce reissue guard (SOL). A durable copy of this outbound never
				// expires, so re-issuing it elsewhere while that copy can still land pays
				// twice. See solDurableReissue for the rule and its proof.
				durableHold, durableKeepVault := s.solDurableReissue(ctx, mgr, toi, vault, height, voter.FinalisedHeight, siblingOutHashes)
				if durableHold {
					etx := common.Tx{
						ID:        toi.InHash,
						Chain:     toi.Chain,
						ToAddress: toi.ToAddress,
						Coins:     []common.Coin{toi.Coin},
						Gas:       toi.MaxGas,
						Memo:      toi.Memo,
					}
					eve := NewEventSecurity(etx, "durable nonce outbound landed but not matched; holding")
					if err = mgr.EventMgr().EmitEvent(ctx, eve); err != nil {
						ctx.Logger().Error("fail to emit security event", "error", err)
					}
					// Terminal: LackSigning examines each TxOut height once, so a held item is
					// never re-examined. Resolving it (confirm the landed payment, then mark
					// the item done) is a manual/ops job.
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "outbound", "abandoned"},
						float32(1),
						[]metrics.Label{
							telemetry.NewLabel("chain", toi.Chain.String()),
							telemetry.NewLabel("asset", toi.Coin.Asset.String()),
							telemetry.NewLabel("reason", "durable_nonce_hold"),
						},
					)
					continue
				}

				// If vault is inactive, check if it still has sufficient funds to fulfill
				// the outbound. If so, reschedule to the same vault (retired vault recovery).
				// If the vault has been drained (e.g. migration completed before this outbound
				// was signed), fall through to active vault selection so the outbound can be
				// fulfilled from a vault that actually holds the funds.
				if vault.Status == InactiveVault {
					maxRetiredVaultRecoveryAttempts := mgr.Keeper().GetConfigInt64(ctx, constants.MaxRetiredVaultRecoveryAttempts)
					age := ctx.BlockHeight() - voter.FinalisedHeight
					attempts := age / itemSigningTransPeriod
					if attempts >= maxRetiredVaultRecoveryAttempts {
						ctx.Logger().Info("too many attempts for retired vault recovery",
							"hash", toi.InHash,
							"attempts", attempts,
							"max", maxRetiredVaultRecoveryAttempts)
						// dropped on the floor: no reschedule, no recovery, no event
						telemetry.IncrCounterWithLabels(
							[]string{"thornode", "outbound", "abandoned"},
							float32(1),
							[]metrics.Label{
								telemetry.NewLabel("chain", toi.Chain.String()),
								telemetry.NewLabel("asset", toi.Coin.Asset.String()),
								telemetry.NewLabel("reason", "retired_vault_max_attempts"),
							},
						)
						continue
					}

					// Check if the inactive vault has enough funds for the outbound.
					// Include gas cost when the gas asset is the same as the outbound asset.
					requiredAmount := toi.Coin.Amount
					for _, gas := range toi.MaxGas {
						if gas.Asset.Equals(toi.Coin.Asset) {
							requiredAmount = requiredAmount.Add(gas.Amount)
						}
					}
					vaultBalance := vault.GetCoin(toi.Coin.Asset).Amount
					if vaultBalance.GTE(requiredAmount) {
						// Vault still has funds — reschedule to the same vault for signing.
						err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, toi, rescheduleHeight)
						if err != nil {
							ctx.Logger().Error("fail to add outbound to queue for retired vault recovery", "error", err)
							resultErr = fmt.Errorf("failed to add outbound to queue for retired vault recovery: %w", err)
						} else {
							txs.TxArray[i].OutHash = common.BlankTxID
							if durableKeepVault {
								// Same as the main reschedule below: keep the vault's later
								// durable siblings on one queue snapshot.
								reissueSolDurableAt(ctx, mgr, vault, height+1, nil, rescheduleHeight, false, siblingOutHashes)
							}
						}
						continue
					}

					// Vault has been drained — fall through to active vault selection.
					ctx.Logger().Info("inactive vault has insufficient funds, reassigning to active vault",
						"hash", toi.InHash,
						"vault", vault.PubKey,
						"asset", toi.Coin.Asset,
						"required", requiredAmount,
						"available", vaultBalance)
				}

				if maxOutboundAttempts > 0 && durableKeepVault {
					// A durable copy may still land, so there is no treasury recovery; surface
					// an outbound its vault has failed to sign for too long (e.g. the vault
					// cannot reach its keysign threshold) instead of retrying it silently.
					if attempts := (ctx.BlockHeight() - voter.FinalisedHeight) / itemSigningTransPeriod; attempts >= maxOutboundAttempts {
						ctx.Logger().Error("durable nonce outbound past max attempts; kept on its vault",
							"hash", toi.InHash, "vault", vault.PubKey, "attempts", attempts)
						telemetry.IncrCounterWithLabels(
							[]string{"thornode", "outbound", "durable_nonce_stuck"},
							float32(1),
							[]metrics.Label{
								telemetry.NewLabel("chain", toi.Chain.String()),
								telemetry.NewLabel("asset", toi.Coin.Asset.String()),
							},
						)
					}
				}
				if maxOutboundAttempts > 0 && !durableKeepVault {
					age := ctx.BlockHeight() - voter.FinalisedHeight
					attempts := age / itemSigningTransPeriod
					if attempts >= maxOutboundAttempts {
						// Skip treasury recovery if there's already an outbound associated or
						// a non-stale observation of the outbound (outbound is in progress).
						hasOutboundAssociated := len(voter.OutTxs) > 0
						hasOutboundObservation := s.hasActiveOutboundObservation(ctx, mgr, itemSigningTransPeriod, voter.FinalisedHeight, toi.InHash)
						if hasOutboundAssociated || hasOutboundObservation {
							ctx.Logger().Info("skipping treasury recovery, outbound in progress",
								"hash", toi.InHash,
								"has_outbound", hasOutboundAssociated,
								"has_observation", hasOutboundObservation)
							continue
						}

						// For user-facing outbounds (refund, outbound, ragnarok), send to treasury recovery
						if memo.IsOutbound() {
							treasuryErr := s.sendFailedOutboundToTreasury(ctx, mgr, toi, vault, memo)
							switch {
							case errors.Is(treasuryErr, errRecoveryTxIDExhausted):
								// Every recovery ID for this height is taken; reschedule so
								// recovery is retried at a later height with fresh IDs.
								ctx.Logger().Info("recovery swap ids exhausted, rescheduling for retry", "hash", toi.InHash)
								err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, toi, rescheduleHeight)
								if err != nil {
									ctx.Logger().Error("fail to add outbound to queue for recovery retry", "error", err)
									resultErr = fmt.Errorf("failed to add outbound to queue for recovery retry: %w", err)
								} else {
									txs.TxArray[i].OutHash = common.BlankTxID
								}
							case treasuryErr != nil:
								ctx.Logger().Error("failed to send outbound to treasury recovery", "error", treasuryErr, "hash", toi.InHash)
								// max attempts reached AND recovery could not be
								// queued: the funds are stranded with no further
								// mechanism
								telemetry.IncrCounterWithLabels(
									[]string{"thornode", "outbound", "treasury_recovery_failed"},
									float32(1),
									[]metrics.Label{
										telemetry.NewLabel("chain", toi.Chain.String()),
										telemetry.NewLabel("asset", toi.Coin.Asset.String()),
									},
								)
							default:
								txs.TxArray[i].OutHash = common.BlankTxID
							}
						} else {
							// For non-outbound transactions (shouldn't happen often), just log and drop
							ctx.Logger().Info("txn dropped, too many attempts (non-outbound)", "hash", toi.InHash)
							telemetry.IncrCounterWithLabels(
								[]string{"thornode", "outbound", "abandoned"},
								float32(1),
								[]metrics.Label{
									telemetry.NewLabel("chain", toi.Chain.String()),
									telemetry.NewLabel("asset", toi.Coin.Asset.String()),
									telemetry.NewLabel("reason", "non_outbound_max_attempts"),
								},
							)
						}
						continue
					}
				}

				if !frozen && !durableKeepVault && s.needsNewVault(ctx, mgr, vault, itemSigningTransPeriod, voter.FinalisedHeight, toi) {
					var active types.Vaults
					active, err = s.keeper.GetAsgardVaultsByStatus(ctx, ActiveVault)
					if err != nil {
						return fmt.Errorf("fail to get active asgard vaults: %w", err)
					}
					// A compromised-vault keysign refusal makes the outbound eligible
					// for rescheduling; do not pick that same compromised vault again.
					active = filterCompromisedVaults(ctx, s.keeper, active)
					active = filterActiveVaultsForOutboundChain(active, toi.Chain)
					// Deduct the asset's pending outbound funds to represent only the available funds.
					pendingOutbounds := mgr.Keeper().GetPendingOutbounds(ctx, toi.Coin.Asset)
					for i := range active {
						active[i].DeductVaultPendingOutbounds(pendingOutbounds)

						// If the currently-assigned vault is an ActiveVault and the only one with enough funds for the outbound,
						// the item should be reassigned to the same vault rather than assigned to another vault without enough funds;
						// this is especially important for GAIA outbounds, for which insufficient-funds failures
						// have THORChain-unobserved gas costs (causing churn-migration-jamming vault insolvency).
						// As such, re-add the (now free) funds of the outbound being replaced.
						if active[i].PubKey.Equals(toi.VaultPubKey) {
							// This is a local availability simulation, not a real credit:
							// bypass AddFunds so deficits do not consume the paper restore.
							paperRestored := common.NewCoins(toi.Coin).Add(toi.MaxGas...)
							active[i].Coins = active[i].Coins.Add(paperRestored...)
						}
					}

					available := active
					mainCoin := toi.Coin
					var maxGasCoin common.Coin
					maxGasCoin, err = mgr.GasMgr().GetMaxGas(ctx, toi.Chain)
					if err != nil {
						ctx.Logger().Error("fail to get max gas", "error", err)
					}
					if mainCoin.Asset.Equals(maxGasCoin.Asset) {
						// If the main coin and the gas coin are the same asset,
						// ensure the assigned vault has enough available for both.
						mainCoin.Amount = mainCoin.Amount.Add(maxGasCoin.Amount)
					} else {
						// If the gas coin isn't the main asset,
						// directly ensure the assigned vault has enough available for it.
						available = active.Has(maxGasCoin)
					}
					available = available.Has(mainCoin)

					if len(available) == 0 {
						// we need to give it somewhere to send from, even if that
						// asgard doesn't have enough funds. This is because if we
						// don't the transaction will just be dropped on the floor,
						// which is bad. Instead it may try to send from an asgard that
						// doesn't have enough funds, fail, and then get rescheduled
						// again later. Maybe by then the network will have enough
						// funds to satisfy.
						// TODO add split logic to send it out from multiple asgards in
						// this edge case.
						ctx.Logger().Error("unable to determine asgard vault to send funds, trying first asgard")
						// deliberately assigning to a vault without enough funds,
						// expecting failure and reschedule - an unbounded retry
						// loop. A non-zero rate means this asset is over-committed
						// against vault balances network-wide.
						telemetry.IncrCounterWithLabels(
							[]string{"thornode", "outbound", "no_vault_available"},
							float32(1),
							[]metrics.Label{
								telemetry.NewLabel("chain", toi.Chain.String()),
								telemetry.NewLabel("asset", toi.Coin.Asset.String()),
							},
						)
						if len(active) > 0 {
							// Fall back on the vault with the most available funds.
							vault = active.SortBy(mainCoin.Asset)[0]
						}
					} else {
						// Use InHash for vault selection if available, otherwise use block height.
						// For internal protocol transactions (ragnarok, migrations, etc.), InHash
						// is set to BlankTxID since there is no associated inbound transaction.
						inHashValue := toi.InHash.Int64()
						rep := int(inHashValue + ctx.BlockHeight()/signingTransPeriod)
						if vault.PubKey.Equals(available[rep%len(available)].PubKey) {
							// looks like the new vault is going to be the same as the
							// old vault, increment rep to ensure a differ asgard is
							// chosen (if there is more than one option)
							rep++
						}
						vault = available[rep%len(available)]
					}
					if !memo.IsType(TxRagnarok) {
						// update original toi action in observed tx
						// check observedTx has done status. Skip if it does already.
						voterTx := voter.GetTx(NodeAccounts{})
						if voterTx.IsDone(len(voter.Actions)) {
							if len(voterTx.OutHashes) > 0 && len(voterTx.GetOutHashes()) > 0 {
								txs.TxArray[i].OutHash = voterTx.GetOutHashes()[0]
							}
							continue
						}

						// update the actions in the voter with the new vault pubkey
						for i, action := range voter.Actions {
							if action.Equals(toi) {
								voter.Actions[i].VaultPubKey = vault.PubKey
								voter.Actions[i].VaultPubKeyEddsa = txOutVaultPubKeyEddsa(toi.Chain, vault.PubKeyEddsa)
								voter.Actions[i].VaultPubKeyFrost = vault.PubKeyFrost
								if txOutHasAggregator(toi) {
									ctx.Logger().Info("clearing aggregator fields on outbound reassignment", "hash", toi.InHash)

									// Here, simultaneously clear the Aggregator information for a reassigned TxOutItem and its Actions item
									// so that a SwapOut will send the THORChain output asset instead of cycling and swallowingif
									// (and maybe failing with slashes) if something goes wrong.
									toi.Aggregator = ""
									toi.AggregatorTargetAsset = ""
									toi.AggregatorTargetLimit = nil
									voter.Actions[i].Aggregator = ""
									voter.Actions[i].AggregatorTargetAsset = ""
									voter.Actions[i].AggregatorTargetLimit = nil
								}
							}
						}
						s.keeper.SetObservedTxInVoter(ctx, voter)

					}
					// Save the toi to as a new toi, select Asgard to send it this time.
					toi.VaultPubKey = vault.PubKey
					toi.VaultPubKeyEddsa = txOutVaultPubKeyEddsa(toi.Chain, vault.PubKeyEddsa)
					toi.VaultPubKeyFrost = vault.PubKeyFrost

					// update max gas - update voter first, only update txOut if voter succeeds
					if !maxGasCoin.IsEmpty() {
						if err = updateTxOutGas(ctx, s.keeper, toi, common.Gas{maxGasCoin}); err != nil {
							ctx.Logger().Error("Failed to update MaxGas of action in ObservedTxVoter", "hash", toi.InHash, "error", err)
						} else {
							toi.MaxGas = common.Gas{maxGasCoin}
						}
					}
					// Equals checks GasRate so update actions GasRate too (before updating in the queue item)
					// for future updates of MaxGas, which must match for matchActionItem in AddOutTx.
					// Only update txOut GasRate if voter update succeeds to prevent permanent desync.
					gasRate := int64(mgr.GasMgr().GetGasRate(ctx, toi.Chain).Uint64())
					if err = updateTxOutGasRate(ctx, s.keeper, toi, gasRate); err != nil {
						ctx.Logger().Error("Failed to update GasRate of action in ObservedTxVoter", "hash", toi.InHash, "error", err)
					} else {
						toi.GasRate = gasRate
					}
				}

				// if a pool with the asset name doesn't exist, skip rescheduling
				if !toi.Coin.IsRune() && !s.keeper.PoolExist(ctx, toi.Coin.Asset) {
					ctx.Logger().Error("fail to add outbound to queue", "error", "coin is not rune and does not have an associated pool")
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "outbound", "reschedule_failed"},
						float32(1),
						[]metrics.Label{
							telemetry.NewLabel("chain", toi.Chain.String()),
							telemetry.NewLabel("reason", "no_pool"),
						},
					)
					continue
				}

				// Apply memoless outbound logic before rescheduling
				applyMemolessOutboundLogic(mgr.GetVersion(), &toi, enableMemolessOutbound)

				err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, toi, rescheduleHeight)
				if err != nil {
					ctx.Logger().Error("fail to add outbound to queue", "error", err)
					resultErr = fmt.Errorf("failed to add outbound to queue: %w", err)
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "outbound", "reschedule_failed"},
						float32(1),
						[]metrics.Label{
							telemetry.NewLabel("chain", toi.Chain.String()),
							telemetry.NewLabel("reason", "queue_add_failed"),
						},
					)
					continue
				}
				// an outbound only lands here when it went unsigned for a whole
				// signing period - a sustained rate is the stuck-outbound alarm
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "outbound", "rescheduled"},
					float32(1),
					[]metrics.Label{
						telemetry.NewLabel("chain", toi.Chain.String()),
						telemetry.NewLabel("asset", toi.Coin.Asset.String()),
					},
				)
				// because the txout item has been rescheduled, thus mark the replaced tx out item as already send out, even it is not
				// in this way bifrost will not send it out again cause node to be slashed
				txs.TxArray[i].OutHash = common.BlankTxID
				if durableKeepVault {
					// Bifrost serializes a vault's durable outbounds on the queue snapshot
					// at each item's keysign height. Re-adding only this item would leave
					// its later siblings on their own heights, each snapshot showing the
					// other still pending, so neither ever signs until the nonce moves.
					// Move them to the same height so they share one snapshot; they all
					// carry the same unconsumed nonce, so at most one copy lands.
					reissueSolDurableAt(ctx, mgr, vault, height+1, nil, rescheduleHeight, false, siblingOutHashes)
				}
			}
		}
		if !txs.IsEmpty() {
			if err := s.keeper.SetTxOut(ctx, txs); err != nil {
				return fmt.Errorf("fail to save tx out : %w", err)
			}
		}
	}

	return resultErr
}

// getOriginalSwapDetails retrieves the original swap transaction details for a failed outbound
// Returns the original transaction, swap memo, and any error encountered
func (s *SlasherImpl) getOriginalSwapDetails(ctx cosmos.Context, mgr Manager, inboundHash common.TxID) (ObservedTx, SwapMemo, error) {
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inboundHash)
	if err != nil {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("could not get observed tx voter: %w", err)
	}

	if voter.Tx.Tx.ID.IsEmpty() {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("voter tx ID is empty")
	}

	originalMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), voter.Tx.Tx.Memo)
	if err != nil {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("could not parse original memo: %w", err)
	}

	if !originalMemo.IsType(TxSwap) {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("original tx is not a swap (type: %s)", originalMemo.GetType())
	}

	swapMemo, ok := originalMemo.(SwapMemo)
	if !ok {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("could not cast to SwapMemo")
	}

	if len(voter.Tx.Tx.Coins) == 0 {
		return ObservedTx{}, SwapMemo{}, fmt.Errorf("voter tx has no coins")
	}

	return voter.Tx, swapMemo, nil
}

// generateRecoveryTxID creates a unique transaction ID for a recovery swap by hashing
// the original transaction ID with the recovery type suffix, block height and the
// failed TxOutItem's identity. The block height and recovery type prevent collisions
// with the original swap in the swap queue, especially important for streaming swaps
// that may still be in progress when the recovery is triggered. The TxOutItem hash
// distinguishes sibling items that share one inbound hash (split outbounds, multi-coin
// refunds, Ragnarok payouts with a blank InHash) and are recovered in the same block.
// Should the ID still be taken in a swap queue (e.g. byte-identical
// TxOutItems), a nonce is mixed in until a free key is found, so a queued
// recovery swap is never silently overwritten.
func (s *SlasherImpl) generateRecoveryTxID(ctx cosmos.Context, mgr Manager, originalTxID common.TxID, recoveryType string, toi TxOutItem) (common.TxID, error) {
	base := fmt.Sprintf("%s:%s:%d:%s", originalTxID.String(), recoveryType, ctx.BlockHeight(), toi.Hash())
	for nonce := 0; nonce < maxRecoveryTxIDNonce; nonce++ {
		hashInput := base
		if nonce > 0 {
			hashInput = fmt.Sprintf("%s:%d", base, nonce)
		}
		txID := common.TxID(fmt.Sprintf("%X", crypto.Sha256([]byte(hashInput))))
		if !mgr.Keeper().HasSwapQueueItem(ctx, txID, 0) && !mgr.Keeper().HasAdvSwapQueueItem(ctx, txID, 0) {
			return txID, nil
		}
	}
	return "", fmt.Errorf("no free %s recovery swap id for %s: %w", recoveryType, originalTxID, errRecoveryTxIDExhausted)
}

// queueRecoverySwap is a helper function that queues a swap for failed outbound recovery.
// It handles the common logic of creating a swap transaction, determining the queue version,
// and emitting the appropriate events.
func (s *SlasherImpl) queueRecoverySwap(
	ctx cosmos.Context,
	mgr Manager,
	toi TxOutItem,
	vault Vault,
	txID common.TxID,
	targetAsset common.Asset,
	destination common.Address,
	memo string,
	recoveryType string,
) error {
	// Get vault's address for the chain of this coin for tracking purposes.
	vaultAddr, err := vault.GetAddress(toi.Coin.Asset.GetChain())
	if err != nil {
		return fmt.Errorf("failed to get vault address for chain %s: %w", toi.Coin.Asset.GetChain(), err)
	}

	// Generate a unique transaction ID for the recovery swap to prevent collisions
	// with the original swap and with sibling recoveries in the queue
	recoveryTxID, err := s.generateRecoveryTxID(ctx, mgr, txID, recoveryType, toi)
	if err != nil {
		return err
	}

	// Create Tx for the swap.
	// Design notes:
	// - ID uses a uniquely generated hash to prevent swap queue collisions with the original transaction.
	// - Chain is set to THORChain because this is an internal protocol-initiated swap,
	//   not an observed transaction from an external chain. The swap handler doesn't
	//   validate or use the Chain field, so this is safe.
	// - FromAddress uses the external chain address (e.g., BTC address) for tracking
	//   purposes. The swap handler only uses FromAddress for validation checks (e.g.,
	//   NoopAddress comparisons) and doesn't enforce chain-address consistency.
	// - Gas is empty because this is an internal operation. The swap handler doesn't
	//   use the Gas field - gas is only relevant for external chain transactions.
	// - Coins contains the external chain asset (e.g., BTC) because the vault holds
	//   these funds in THORChain's accounting, and we're swapping from that asset.
	recoveryTx := common.Tx{
		ID:          recoveryTxID,
		Chain:       common.THORChain,
		FromAddress: vaultAddr,
		ToAddress:   vaultAddr,
		Coins:       common.Coins{toi.Coin},
		Memo:        memo,
		Gas:         common.Gas{},
	}

	// Get Asgard module address for signer
	asgardAddr := mgr.Keeper().GetModuleAccAddress(AsgardName)

	// Determine which swap queue to use and set appropriate version
	enableAdvQueue := mgr.Keeper().GetConfigInt64(ctx, constants.EnableAdvSwapQueue)
	swapVersion := types.SwapVersion_v1
	if enableAdvQueue > 0 {
		swapVersion = types.SwapVersion_v2
	}

	// Create swap message
	swapMsg := NewMsgSwap(
		recoveryTx,
		targetAsset,       // target asset
		destination,       // destination
		cosmos.ZeroUint(), // tradeTarget (market order)
		common.NoAddress,  // no affiliate
		cosmos.ZeroUint(), // no affiliate fee
		"",                // no aggregator
		"",                // no agg target
		nil,               // no agg limit
		MarketSwap,        // swap type
		0,                 // quantity (let handler decide)
		1,                 // interval
		swapVersion,       // v1 for legacy queue, v2 for advanced queue
		asgardAddr,        // Asgard module account as signer
	)

	// The advanced queue validates on insert, but the legacy queue would
	// persist an invalid swap (e.g. a failed swap refund reverse-swapping into
	// its own asset) that only fails on execution. Reject it here for both so
	// the caller can fall back.
	if err := swapMsg.ValidateBasic(); err != nil {
		return fmt.Errorf("invalid %s recovery swap: %w", recoveryType, err)
	}

	// Add to swap queue
	if enableAdvQueue > 0 {
		if err := mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, mgr, swapMsg); err != nil {
			return fmt.Errorf("failed to add %s recovery swap to adv queue: %w", recoveryType, err)
		}
	} else {
		if err := mgr.Keeper().SetSwapQueueItem(ctx, *swapMsg, 0); err != nil {
			return fmt.Errorf("failed to add %s recovery swap to queue: %w", recoveryType, err)
		}
	}

	ctx.Logger().Info("recovery swap queued",
		"original_hash", txID,
		"recovery_hash", recoveryTxID,
		"recovery_type", recoveryType,
		"from_asset", toi.Coin.Asset,
		"to_asset", targetAsset,
		"destination", destination)

	// Emit failed outbound recovery event
	recoveryEvent := NewEventFailedOutboundRecovery(
		txID,
		toi.Coin,
		recoveryType,
		destination,
		memo,
	)
	if err := mgr.EventMgr().EmitEvent(ctx, recoveryEvent); err != nil {
		ctx.Logger().Error("failed to emit recovery event", "error", err)
	}

	// every increment is a user who did not receive their funds; a burst on one
	// chain is the signature of a chain client that cannot broadcast.
	// recoveryType is a compile-time literal at both call sites.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "outbound", "treasury_recovery"},
		float32(1),
		[]metrics.Label{
			telemetry.NewLabel("chain", toi.Chain.String()),
			telemetry.NewLabel("asset", toi.Coin.Asset.String()),
			telemetry.NewLabel("recovery_type", recoveryType),
		},
	)

	return nil
}

// sendFailedSwapReverseSwap creates a reverse swap to refund the original sender when a swap outbound fails
func (s *SlasherImpl) sendFailedSwapReverseSwap(ctx cosmos.Context, mgr Manager, toi TxOutItem, vault Vault, originalTx ObservedTx, swapMemo SwapMemo, inboundHash common.TxID) error {
	ctx.Logger().Info("attempting reverse swap for failed swap outbound", "hash", toi.InHash, "coin", toi.Coin)

	// Determine destination address: use custom refund address if specified, otherwise original sender
	destination := originalTx.Tx.FromAddress
	if !swapMemo.GetRefundAddress().IsEmpty() {
		refundAddress := swapMemo.GetRefundAddress()
		chain := originalTx.Tx.Coins[0].Asset.GetChain()
		validRefundAddress := refundAddress.IsChain(chain)
		if chain.Equals(common.DASHChain) {
			validRefundAddress = validRefundAddress && refundAddress.IsCurrentNetwork(chain)
		}
		if validRefundAddress {
			destination = refundAddress
			ctx.Logger().Info("using custom refund address for reverse swap", "address", destination)
		} else {
			ctx.Logger().Info("invalid custom refund address, using original sender",
				"refund_address", refundAddress,
				"expected_chain", chain)
		}
	}

	// XMR unknown sender guard: if the destination is still an XMR unknown
	// sender marker, skip the reverse swap so the caller falls through to
	// treasury recovery immediately rather than attempting outbounds to an
	// undeliverable address that would fail MaxOutboundAttempts times first.
	destinationInfo := common.ResolveObservedSender(originalTx.Tx.Coins[0].Asset.GetChain(), destination)
	if originalTx.Tx.Coins[0].Asset.GetChain().Equals(common.XMRChain) && destinationInfo.RefundAddressRequired {
		return fmt.Errorf("cannot reverse swap to XMR unknown sender address %s; falling back to treasury", destination)
	}

	// Queue reverse swap to original asset
	targetAsset := originalTx.Tx.Coins[0].Asset
	memo := fmt.Sprintf("=:%s:%s", targetAsset, destination)
	return s.queueRecoverySwap(ctx, mgr, toi, vault, inboundHash, targetAsset, destination, memo, "reverse_swap")
}

// sendFailedOutboundToTreasury creates a swap to convert failed outbound assets to RUNE
// and send them to the treasury address when MaxOutboundAttempts is reached.
// For swap outbounds, attempts to perform a reverse swap instead.
func (s *SlasherImpl) sendFailedOutboundToTreasury(ctx cosmos.Context, mgr Manager, toi TxOutItem, vault Vault, memo mem.Memo) error {
	ctx.Logger().Info("max attempts reached for outbound", "hash", toi.InHash, "coin", toi.Coin)

	// Extract the inbound hash from the memo (format: "OUT:<hash>" or "REFUND:<hash>")
	inboundHash := toi.InHash
	if memo.IsType(TxOutbound) {
		if outboundMemo, ok := memo.(OutboundMemo); ok {
			if !outboundMemo.GetTxID().IsEmpty() {
				inboundHash = outboundMemo.GetTxID()
			}
		}
	} else if memo.IsType(TxRefund) {
		if refundMemo, ok := memo.(RefundMemo); ok {
			if !refundMemo.GetTxID().IsEmpty() {
				inboundHash = refundMemo.GetTxID()
			}
		}
		// As an additional safety measure, explicitly parse REFUND:<hash> format
		// from the memo string in case the typed memo didn't extract it
		if inboundHash.Equals(toi.InHash) && strings.HasPrefix(toi.Memo, "REFUND:") {
			parts := strings.SplitN(toi.Memo, ":", 2)
			if len(parts) == 2 && len(parts[1]) > 0 {
				if txID, err := common.NewTxID(parts[1]); err == nil && !txID.IsEmpty() {
					inboundHash = txID
				}
			}
		}
	}

	// Try to get the original transaction to check if this was a swap
	originalTx, swapMemo, err := s.getOriginalSwapDetails(ctx, mgr, inboundHash)
	if err == nil {
		// This is a failed swap outbound - attempt reverse swap
		ctx.Logger().Info("detected failed swap outbound, attempting reverse swap",
			"hash", inboundHash,
			"original_asset", originalTx.Tx.Coins[0].Asset,
			"failed_asset", toi.Coin.Asset)

		reverseErr := s.sendFailedSwapReverseSwap(ctx, mgr, toi, vault, originalTx, swapMemo, inboundHash)
		if reverseErr == nil {
			// Reverse swap succeeded
			return nil
		}
		// ID exhaustion is transient: hand it back so the caller reschedules
		// and the sender is refunded at a later height, not the treasury.
		if errors.Is(reverseErr, errRecoveryTxIDExhausted) {
			return reverseErr
		}
		// Reverse swap failed, log error and fall back to treasury recovery
		ctx.Logger().Error("reverse swap failed, falling back to treasury recovery",
			"error", reverseErr,
			"hash", inboundHash)
	}

	// Either not a swap, or reverse swap failed - proceed with treasury recovery
	ctx.Logger().Info("sending to treasury recovery", "hash", inboundHash, "coin", toi.Coin)

	// Queue swap to treasury
	return s.queueRecoverySwap(ctx, mgr, toi, vault, inboundHash, common.RuneAsset(), common.TreasuryAddress, fmt.Sprintf("=:RUNE:%s", common.TreasuryAddress), "treasury")
}

// SlashVault thorchain keep monitoring the outbound tx from asgard pool
// usually the txout is triggered by thorchain itself by
// adding an item into the txout array, refer to TxOutItem for the detail, the
// TxOutItem contains a specific coin and amount.  if somehow thorchain
// discover signer send out fund more than the amount specified in TxOutItem,
// it will slash the node account who does that by taking 1.5 * extra fund from
// node account's bond and subsidise the pool that actually lost it.
func (s *SlasherImpl) SlashVault(ctx cosmos.Context, vaultPK common.PubKey, coins common.Coins, mgr Manager) error {
	if coins.IsEmpty() {
		return nil
	}

	vault, err := s.keeper.GetVault(ctx, vaultPK)
	if err != nil {
		return fmt.Errorf("fail to get slash vault (pubkey %s), %w", vaultPK, err)
	}
	membership := vault.GetMembership()

	// sum the total bond of membership of the vault
	totalBond := cosmos.ZeroUint()
	for _, member := range membership {
		na, err := s.keeper.GetNodeAccountByPubKey(ctx, member)
		if err != nil {
			ctx.Logger().Error("fail to get node account bond", "pk", member, "error", err)
			continue
		}
		totalBond = totalBond.Add(na.Bond)
	}

	for _, coin := range coins {
		if coin.IsEmpty() {
			continue
		}
		pool, err := s.keeper.GetPool(ctx, coin.Asset)
		if err != nil {
			ctx.Logger().Error("fail to get pool for slash", "asset", coin.Asset, "error", err)
			continue
		}
		// THORChain doesn't even have a pool for the asset
		if pool.IsEmpty() {
			ctx.Logger().Error("cannot slash for an empty pool", "asset", coin.Asset)
			continue
		}

		stolenAssetValue := coin.Amount
		vaultAmount := vault.GetCoin(coin.Asset).Amount
		if stolenAssetValue.GT(vaultAmount) {
			stolenAssetValue = vaultAmount
		}
		if stolenAssetValue.GT(pool.BalanceAsset) {
			stolenAssetValue = pool.BalanceAsset
		}

		// stolenRuneValue is the value in RUNE of the missing funds
		stolenRuneValue := pool.AssetValueInRune(stolenAssetValue)

		if stolenRuneValue.IsZero() {
			continue
		}

		penaltyPts := mgr.Keeper().GetConfigInt64(ctx, constants.SlashPenalty)
		// total slash amount is penaltyPts the RUNE value of the missing funds
		totalRuneToSlash := common.GetUncappedShare(cosmos.NewUint(uint64(penaltyPts)), cosmos.NewUint(10_000), stolenRuneValue)
		totalRuneSlashed := cosmos.ZeroUint()
		pauseOnSlashThreshold := mgr.Keeper().GetConfigInt64(ctx, constants.PauseOnSlashThreshold)
		if pauseOnSlashThreshold > 0 && totalRuneToSlash.GTE(cosmos.NewUint(uint64(pauseOnSlashThreshold))) {
			// set mimirs to pause signing
			haltsignKey := fmt.Sprintf(constants.MimirTemplateHaltSigning, coin.Asset.Chain)
			s.keeper.SetMimir(ctx, haltsignKey, ctx.BlockHeight())
			// an automatic PauseOnSlashThreshold trip means a theft/overspend
			// large enough to halt the chain; thornode_chain_halted alone cannot
			// distinguish this from a routine operator mimir halt
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "slash", "auto_halt"},
				float32(1),
				[]metrics.Label{
					telemetry.NewLabel("chain", coin.Asset.Chain.String()),
					telemetry.NewLabel("kind", "signing"),
				},
			)
			mimirEvent1 := NewEventSetMimir(haltsignKey, strconv.FormatInt(ctx.BlockHeight(), 10))
			if err := mgr.EventMgr().EmitEvent(ctx, mimirEvent1); err != nil {
				ctx.Logger().Error("fail to emit set_mimir event", "error", err)
			}
			// set mimirs to pause trading
			halttradeKey := fmt.Sprintf(constants.MimirTemplateHaltTrading, coin.Asset.Chain)
			s.keeper.SetMimir(ctx, halttradeKey, ctx.BlockHeight())
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "slash", "auto_halt"},
				float32(1),
				[]metrics.Label{
					telemetry.NewLabel("chain", coin.Asset.Chain.String()),
					telemetry.NewLabel("kind", "trading"),
				},
			)
			mimirEvent2 := NewEventSetMimir(halttradeKey, strconv.FormatInt(ctx.BlockHeight(), 10))
			if err := mgr.EventMgr().EmitEvent(ctx, mimirEvent2); err != nil {
				ctx.Logger().Error("fail to emit set_mimir event", "error", err)
			}
		}
		for _, member := range membership {
			na, err := s.keeper.GetNodeAccountByPubKey(ctx, member)
			if err != nil {
				ctx.Logger().Error("fail to get node account for slash", "pk", member, "error", err)
				continue
			}
			if na.Bond.IsZero() {
				ctx.Logger().Info("validator's bond is zero, can't be slashed", "node address", na.NodeAddress.String())
				continue
			}
			runeSlashed := s.slashAndUpdateNodeAccount(ctx, na, coin, vault, totalBond, totalRuneToSlash)
			totalRuneSlashed = totalRuneSlashed.Add(runeSlashed)
		}

		//  2/3 of the total slashed RUNE value to asgard
		//  1/3 of the total slashed RUNE value to reserve
		runeToAsgard := stolenRuneValue

		// stolenRuneValue is the total value in RUNE of stolen coins, but totalRuneSlashed is
		// the total amount able to be slashed from Nodes, credit the pool only totalRuneSlashed
		if totalRuneSlashed.LT(stolenRuneValue) {
			// this should theoretically never happen
			ctx.Logger().Info("total slashed bond amount is less than RUNE value", "slashed_bond", totalRuneSlashed.String(), "rune_value", stolenRuneValue.String())
			// broken invariant: bond could not cover the theft, so the pool is
			// credited less RUNE than the asset it lost
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "vault", "slash", "shortfall_rune"},
				telem(common.SafeSub(stolenRuneValue, totalRuneSlashed)),
				[]metrics.Label{
					telemetry.NewLabel("asset", coin.Asset.String()),
					telemetry.NewLabel("vault_type", vault.Type.String()),
				},
			)
			runeToAsgard = totalRuneSlashed
		}
		runeToReserve := common.SafeSub(totalRuneSlashed, runeToAsgard)

		if !runeToReserve.IsZero() {
			if err := s.keeper.SendFromModuleToModule(ctx, BondName, ReserveName, common.NewCoins(common.NewCoin(common.RuneAsset(), runeToReserve))); err != nil {
				ctx.Logger().Error("fail to send slash funds to reserve module", "pk", vaultPK, "error", err)
			}
		}
		if !runeToAsgard.IsZero() {
			if err := s.keeper.SendFromModuleToModule(ctx, BondName, AsgardName, common.NewCoins(common.NewCoin(common.RuneAsset(), runeToAsgard))); err != nil {
				ctx.Logger().Error("fail to send slash fund to asgard module", "pk", vaultPK, "error", err)
			}
			s.updatePoolFromSlash(ctx, pool, common.NewCoin(coin.Asset, stolenAssetValue), runeToAsgard, mgr)
		}
	}

	return nil
}

// slashAndUpdateNodeAccount slashes a NodeAccount a portion of the value of coin based on their
// portion of the total bond of the offending Vault's membership. Return the amount of RUNE slashed
func (s *SlasherImpl) slashAndUpdateNodeAccount(ctx cosmos.Context, na types.NodeAccount, coin common.Coin, vault types.Vault, totalBond, totalSlashAmountInRune cosmos.Uint) cosmos.Uint {
	slashAmountRune := common.GetSafeShare(na.Bond, totalBond, totalSlashAmountInRune)
	if slashAmountRune.GT(na.Bond) {
		ctx.Logger().Info("slash amount is larger than bond", "slash amount", slashAmountRune, "bond", na.Bond)
		slashAmountRune = na.Bond
	}

	ctx.Logger().Info("slash node account", "node address", na.NodeAddress.String(), "amount", slashAmountRune.String(), "total slash amount", totalSlashAmountInRune)
	na.Bond = common.SafeSub(na.Bond, slashAmountRune)

	bondEvent := NewEventBond(slashAmountRune, BondCost, common.Tx{}, &na, nil)
	if err := s.eventMgr.EmitEvent(ctx, bondEvent); err != nil {
		ctx.Logger().Error("fail to emit bond event", "error", err)
	}

	metricLabels, _ := ctx.Context().Value(constants.CtxMetricLabels).([]metrics.Label)
	slashAmountRuneFloat, _ := new(big.Float).SetInt(slashAmountRune.BigInt()).Float32()
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "bond_slash"},
		slashAmountRuneFloat,
		append(
			metricLabels,
			telemetry.NewLabel("address", na.NodeAddress.String()),
			telemetry.NewLabel("coin_symbol", coin.Asset.Symbol.String()),
			telemetry.NewLabel("coin_chain", string(coin.Asset.Chain)),
			telemetry.NewLabel("vault_type", vault.Type.String()),
			telemetry.NewLabel("vault_status", vault.Status.String()),
		),
	)

	if err := s.keeper.SetNodeAccount(ctx, na); err != nil {
		ctx.Logger().Error("fail to save node account for slash", "error", err)
	}

	return slashAmountRune
}

// IncSlashPoints will increase the given account's slash points
func (s *SlasherImpl) IncSlashPoints(ctx cosmos.Context, point int64, addresses ...cosmos.AccAddress) {
	for _, addr := range addresses {
		if err := s.keeper.IncNodeAccountSlashPoints(ctx, addr, point); err != nil {
			ctx.Logger().Error("fail to increase node account slash point", "error", err, "address", addr.String())
		}
	}
}

// DecSlashPoints will decrease the given account's slash points
func (s *SlasherImpl) DecSlashPoints(ctx cosmos.Context, point int64, addresses ...cosmos.AccAddress) {
	for _, addr := range addresses {
		if err := s.keeper.DecNodeAccountSlashPoints(ctx, addr, point); err != nil {
			ctx.Logger().Error("fail to decrease node account slash point", "error", err, "address", addr.String())
		}
	}
}

// updatePoolFromSlash updates a pool's depths and emits appropriate events after a slash
func (s *SlasherImpl) updatePoolFromSlash(ctx cosmos.Context, pool types.Pool, stolenAsset common.Coin, runeCreditAmt cosmos.Uint, mgr Manager) {
	pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, stolenAsset.Amount)
	pool.BalanceRune = pool.BalanceRune.Add(runeCreditAmt)
	if err := s.keeper.SetPool(ctx, pool); err != nil {
		ctx.Logger().Error("fail to save pool for slash", "asset", pool.Asset, "error", err)
	}
	stolenAmtInt64 := int64(math.MaxInt64)
	if stolenAsset.Amount.Uint64() <= uint64(math.MaxInt64) {
		stolenAmtInt64 = int64(stolenAsset.Amount.Uint64())
	}
	creditAmtInt64 := int64(math.MaxInt64)
	if runeCreditAmt.Uint64() <= uint64(math.MaxInt64) {
		creditAmtInt64 = int64(runeCreditAmt.Uint64())
	}
	poolSlashAmt := []PoolAmt{
		{
			Asset:  pool.Asset,
			Amount: 0 - stolenAmtInt64,
		},
		{
			Asset:  common.RuneAsset(),
			Amount: creditAmtInt64,
		},
	}
	eventSlash := NewEventSlash(pool.Asset, poolSlashAmt)
	if err := mgr.EventMgr().EmitEvent(ctx, eventSlash); err != nil {
		ctx.Logger().Error("fail to emit slash event", "error", err)
	}
}

// hasActiveOutboundObservation reports whether an observed link should still
// block MaxOutboundAttempts recovery. Fresh or finalised observations remain a
// double-send guard; only stale non-finalised observations are ignored.
func (s *SlasherImpl) hasActiveOutboundObservation(ctx cosmos.Context, mgr Manager, staleSigningTransPeriod, startHeight int64, inHash common.TxID) bool {
	outhashes := mgr.Keeper().GetObservedLink(ctx, inHash)
	for _, hash := range outhashes {
		voter, err := mgr.Keeper().GetObservedTxOutVoter(ctx, hash)
		if err != nil {
			ctx.Logger().Error("fail to get txout voter", "hash", hash, "error", err)
			return true
		}
		staleStatus := s.staleNonFinalOutboundObservation(ctx, voter, staleSigningTransPeriod, startHeight)
		if staleStatus.stale {
			// The observed link proves only that signers reported a broadcast.
			// If it never finalises, continuing to treat it as in progress blocks
			// both reassignment and MaxOutboundAttempts recovery indefinitely.
			logStaleOutboundObservation(ctx, staleStatus, "ignoring stale non-finalised outbound observation",
				"inhash", inHash,
				"outhash", hash,
				"start_height", startHeight,
				"observation_height", staleStatus.observationHeight,
				"voter_height", voter.Height,
				"unfinalized_height", voter.UnfinalizedHeight,
				"stale_since_height", staleStatus.staleSinceHeight,
				"reason", "non-finalised observation exceeded stale window and no longer proves outbound is in progress")
			continue
		}
		return true
	}
	return false
}

// staleOutboundObservationStatus carries the height metadata used to decide
// whether a stale observation should still get the one high-signal Info log.
type staleOutboundObservationStatus struct {
	stale             bool
	firstStaleWindow  bool
	observationHeight int64
	staleSinceHeight  int64
}

// staleNonFinalOutboundObservation bounds the "vault says it sent it" heuristic.
// A non-finalised outbound observation that never reaches finality should not
// pin reassignment or recovery forever.
func (s *SlasherImpl) staleNonFinalOutboundObservation(ctx cosmos.Context, voter ObservedTxVoter, signingTransPeriod, startHeight int64) staleOutboundObservationStatus {
	status := staleOutboundObservationStatus{}
	if signingTransPeriod <= 0 || voter.FinalisedHeight > 0 {
		return status
	}

	// Prefer the consensus height for voters that reached non-final consensus.
	// Fall back to the inbound/retry clock for sub-consensus signer observations.
	observationHeight := voter.UnfinalizedHeight
	if observationHeight == 0 {
		observationHeight = voter.Height
	}
	if observationHeight == 0 {
		observationHeight = startHeight
	}
	if observationHeight <= 0 {
		return status
	}

	expiryHeight := observationHeight + (staleOutboundObservationSigningPeriods * signingTransPeriod)
	status.observationHeight = observationHeight
	status.staleSinceHeight = expiryHeight + 1
	if ctx.BlockHeight() <= expiryHeight {
		return status
	}

	status.stale = true
	// LackSigning re-evaluates rescheduled items on signing-period boundaries,
	// so an exact expiryHeight+1 check could miss the first actionable check.
	status.firstStaleWindow = ctx.BlockHeight() <= expiryHeight+signingTransPeriod
	return status
}

// logStaleOutboundObservation emits Info only while the stale condition first
// becomes actionable; repeated stale checks after that window are Debug noise.
func logStaleOutboundObservation(ctx cosmos.Context, status staleOutboundObservationStatus, msg string, keyvals ...interface{}) {
	keyvals = append(keyvals,
		"first_stale_window", status.firstStaleWindow,
		"stale_observation_signing_periods", staleOutboundObservationSigningPeriods)
	if status.firstStaleWindow {
		ctx.Logger().Info(msg, keyvals...)
		return
	}
	ctx.Logger().Debug(msg, keyvals...)
}

// outboundObservationFulfils reports whether an observed outbound plausibly
// fulfils toi, disregarding which vault sent it: the same destination, the same
// asset, and an amount the outbound handler would accept.
//
// The destination is matched with the same comparator CommonOutboundTxHandler
// and matchActionItem use, so an unrelated transaction cannot stand in for the
// scheduled outbound and the slasher's notion of "in progress" cannot diverge
// from the handlers' notion of "done".
func outboundObservationFulfils(ctx cosmos.Context, mgr Manager, tx ObservedTx, toi TxOutItem) bool {
	return len(tx.Tx.Coins) == 1 &&
		tx.Tx.Coins[0].Asset.Equals(toi.Coin.Asset) &&
		common.EquivalentAddresses(toi.Chain, tx.Tx.ToAddress, toi.ToAddress) &&
		outboundObservationAmountMatches(ctx, mgr, tx.Tx, toi)
}

// outboundObservationAmountMatches applies the amount tolerances
// CommonOutboundTxHandler already allows when deciding that an observed
// transaction is the scheduled outbound: the exact amount, the amount rounded
// to the chain's precision, or - for a gas asset - the same total spend once
// the gas is accounted for.
//
// Unlike the handler this does not withhold the match when the signer overspent
// MaxGas. That refinement exists so the handler can slash rather than mark the
// action done; here the question is only whether funds have already left the
// vault, and they have. Treating an overspending broadcast as "not this
// outbound" would invite a second payment, which is the failure this guard
// exists to prevent.
func outboundObservationAmountMatches(ctx cosmos.Context, mgr Manager, tx common.Tx, toi TxOutItem) bool {
	scheduled := common.Coins{toi.Coin}
	if tx.Coins.EqualsEx(scheduled) {
		return true
	}

	// The scheduled amount may carry more precision than the chain can express.
	pool, err := mgr.Keeper().GetPool(ctx, toi.Coin.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "asset", toi.Coin.Asset, "error", err)
	} else if !pool.IsEmpty() {
		rounded := common.NewCoin(toi.Coin.Asset, cosmos.RoundToDecimal(toi.Coin.Amount, pool.Decimals))
		if tx.Coins.EqualsEx(common.Coins{rounded}) {
			return true
		}
	}

	// A gas asset outbound pays its own gas, so the observed amount differs from
	// the scheduled one by however much gas was actually spent.
	gasAsset := toi.Chain.GetGasAsset()
	if !toi.Coin.Asset.Equals(gasAsset) {
		return false
	}
	intendToSpend := toi.Coin.Amount.Add(toi.MaxGas.ToCoins().GetCoin(gasAsset).Amount)
	// This can be one node's observation before consensus. Adding its amount
	// and gas can overflow Uint, so compare using a guarded subtraction instead.
	observedAmount := tx.Coins.GetCoin(gasAsset).Amount
	observedGas := tx.Gas.ToCoins().GetCoin(gasAsset).Amount
	return observedAmount.LTE(intendToSpend) && intendToSpend.Sub(observedAmount).Equal(observedGas)
}

// needsNewVault reports whether an unfulfilled TxOutItem should be reassigned to
// another vault.
//
// Reassignment sends the outbound a second time if the assigned vault did in
// fact broadcast it, so every observed link is weighed before deciding - not
// just the first. A link is written on the first attestation, before the
// outbound voter reaches consensus, so any single node can have an arbitrary
// hash linked to this inbound; if that stray link is examined first, returning
// on it hides the assigned vault's own majority observation recorded under a
// later hash, and the outbound is paid twice.
//
// Only members of the assigned vault carry weight for the majority: that is the
// strong "the vault says it sent it" signal. Every matching signer earns the
// bounded waiting window though - outbound observations are submitted by any
// active observer, not just vault members, so an outsider majority attesting
// that funds left this vault's address is exactly what a reassignment would
// pay twice. staleNonFinalOutboundObservation caps how long that window runs.
func (s *SlasherImpl) needsNewVault(ctx cosmos.Context, mgr Manager, vault Vault, signingTransPeriod, startHeight int64, toi TxOutItem) bool {
	for _, hash := range mgr.Keeper().GetObservedLink(ctx, toi.InHash) {
		voter, err := mgr.Keeper().GetObservedTxOutVoter(ctx, hash)
		if err != nil {
			ctx.Logger().Error("fail to get txout voter", "hash", hash, "error", err)
			continue
		}
		if voter.FinalisedHeight > 0 {
			// Finalised observed txouts should have nothing to do with unfulfilled TxOutItems.
			// This finalised txout might for instance be from an output
			// that was split into multiple outbounds from initially-different vaults.
			continue
		}
		// in the event there are multiple observed txouts for a given inhash, we
		// focus on the observations that could actually be this outbound: the
		// assigned vault as sender, and a payload the handlers would accept as
		// fulfilling the item.
		signers := make(map[string]bool)
		for _, tx1 := range voter.Txs {
			if !txOutPubKeyMatchesObserved(toi.Chain, tx1.ObservedPubKey, toi) ||
				!outboundObservationFulfils(ctx, mgr, tx1, toi) {
				continue
			}

			for _, signer := range tx1.Signers {
				signers[signer] = true
			}
		}
		if len(signers) == 0 {
			continue
		}

		staleStatus := s.staleNonFinalOutboundObservation(ctx, voter, signingTransPeriod, startHeight)
		if staleStatus.stale {
			logStaleOutboundObservation(ctx, staleStatus, "allowing vault reassignment for stale non-finalised outbound observation",
				"inhash", toi.InHash,
				"outhash", hash,
				"start_height", startHeight,
				"observation_height", staleStatus.observationHeight,
				"voter_height", voter.Height,
				"unfinalized_height", voter.UnfinalizedHeight,
				"stale_since_height", staleStatus.staleSinceHeight)
			continue
		}

		count := vaultSignerCount(vault, signers)

		// if a simple majority of vault members have observed the outbound,
		// then we should not reschedule. If a vault says it sent it, it
		// sent it and shouldn't get another vault to send it (potentially
		// a second time)
		if count > 0 && HasSimpleMajority(count, len(vault.Membership)) {
			return false
		}

		// A sub-majority observation is not yet the vault saying it sent the
		// outbound, so it only earns a bounded window for the remaining
		// observations to arrive. Active nodes outside the vault watch this
		// chain too and their attestation that funds left the vault's address
		// is what a reassignment would double-spend, so they earn the window as
		// well; staleNonFinalOutboundObservation already bounds it. Keep
		// scanning: a later link may still hold the majority this one lacks.
		maxHeight := startHeight + ((int64(len(signers)) + 1) * signingTransPeriod)
		if maxHeight >= ctx.BlockHeight() {
			return false
		}
	}

	return true
}

// solDurableReissue decides how LackSigning may re-issue a SOL outbound toi,
// issued at TxOut height issuedHeight, whose vault has a durable nonce committed.
//
//   - hold: a copy of toi from its vault LANDED and was finally observed, yet toi is
//     not done (its observation did not match toi and did not set OutHash). Any
//     re-issue could pay a second time, so it is not re-issued at all. This is
//     terminal (see the caller); recovery is a manual/ops job.
//   - keepVault: the vault's committed nonce has not advanced since issuedHeight, so
//     a durable copy signed at issuedHeight may still land. Re-issue only on the
//     same vault — never reassign, never treasury-recover.
//
// Why it cannot pay twice. Every durable copy of toi on the vault was signed at some
// TxOut height h <= issuedHeight with the nonce committed at the END of block h
// (signers read it at ?height=h). The committed nonce changes only when the final,
// supermajority observation of the vault tx that consumed the previous value is
// committed, and only the vault can consume it. So:
//   - Same-vault copies share one nonce and exclude each other on-chain.
//   - updated_height > issuedHeight means the value the latest copy used was
//     consumed by a committed tx. (A re-attestation, MsgSolNonceAttest, commits past
//     txs that FAILED on-chain; bifrost attests only when every vault-signed tx since
//     the previous commit failed, and a failed copy pays nothing.) That tx was not a
//     copy of toi — a landed copy's final observation is linked to toi.InHash (from
//     its memo, or for a memoless outbound from the memo recovered at quorum) and
//     would trigger hold — so every vault copy is dead and a copy on another vault is
//     the only one that can land. Ragnarok payments have no inbound link (see the MR
//     known gaps).
//
// Vaults without a committed nonce (legacy recent-blockhash copies, which expire long
// before a reschedule) and non-SOL chains are unaffected.
func (s *SlasherImpl) solDurableReissue(ctx cosmos.Context, mgr Manager, toi TxOutItem, vault Vault, issuedHeight, inboundHeight int64, siblings solSiblingCache) (hold, keepVault bool) {
	if !toi.Chain.Equals(common.SOLChain) {
		return false, false
	}
	record, found, err := activeSolVaultNonce(ctx, s.keeper, vault.PubKey)
	if err != nil {
		// Unknown: be conservative, keep the outbound on its vault.
		ctx.Logger().Error("fail to get sol vault nonce; keeping outbound on its vault", "vault", vault.PubKey, "error", err)
		return false, true
	}
	if !found {
		return false, false
	}
	if s.solDurableCopyLanded(ctx, mgr, toi, inboundHeight, siblings) {
		return true, true
	}
	return false, record.UpdatedHeight <= issuedHeight
}

// solDurableCopyLanded reports whether a copy of toi from its assigned vault landed
// and was finally observed: a linked outbound voter whose FINAL consensus tx (not any
// single vote in voter.Txs, which one validator could forge) is from the assigned
// vault and plausibly fulfils toi. (needsNewVault deliberately ignores finalised
// links; for a durable outbound a finalised copy that did not mark toi done is
// exactly the case that must not be paid again.) A payment already matched to
// another item of the same inbound (e.g. an equal split part to the same
// destination) is not a copy of toi; see solOutHashMatchedToSibling.
func (s *SlasherImpl) solDurableCopyLanded(ctx cosmos.Context, mgr Manager, toi TxOutItem, inboundHeight int64, siblings solSiblingCache) bool {
	for _, hash := range s.keeper.GetObservedLink(ctx, toi.InHash) {
		voter, err := s.keeper.GetObservedTxOutVoter(ctx, hash)
		if err != nil {
			ctx.Logger().Error("fail to get txout voter", "hash", hash, "error", err)
			continue
		}
		// voter.Tx is set to the final supermajority tx when FinalisedHeight is set.
		tx := voter.Tx
		if voter.FinalisedHeight <= 0 || tx.IsEmpty() || !tx.IsFinal() {
			continue
		}
		if !txOutPubKeyMatchesObserved(toi.Chain, tx.ObservedPubKey, toi) ||
			!outboundObservationFulfils(ctx, mgr, tx, toi) {
			continue
		}
		if s.solOutHashMatchedToSibling(ctx, mgr, toi, hash, inboundHeight, siblings) {
			continue
		}
		return true
	}
	return false
}

// solSiblingScanMaxBlocks bounds the TxOut blocks scanned for an inbound's matched
// outbound hashes (~1 day). A sibling matched further back is not found, which only
// makes the guard hold (never re-issue) on that payment.
const solSiblingScanMaxBlocks = int64(14400)

// solSiblingCache holds, per inbound hash, the OutHash of every item of that inbound
// marked done in the scanned TxOut window. It lives for one LackSigning pass so each
// inbound's window is read at most once per EndBlock, however many of its items or
// linked payments are examined.
type solSiblingCache map[string]map[string]bool

// solOutHashMatchedToSibling reports whether hash is the OutHash of another item of
// toi's inbound, in any TxOut block since the inbound (bounded by
// solSiblingScanMaxBlocks) up to the next reschedule-coalesce height (rescheduled
// items are placed there). Only a matched item proves the payment completed
// something else: the inbound voter's OutTxs also records landed payments that
// matched no item (e.g. gas above MaxGas), and those must still count as a landed
// copy of toi. Only reached for a linked, finalised payment that fulfils toi.
func (s *SlasherImpl) solOutHashMatchedToSibling(ctx cosmos.Context, mgr Manager, toi TxOutItem, hash common.TxID, inboundHeight int64, siblings solSiblingCache) bool {
	key := toi.InHash.String()
	matched, ok := siblings[key]
	if !ok {
		matched = s.solMatchedOutHashes(ctx, mgr, toi.InHash, inboundHeight)
		if siblings != nil {
			siblings[key] = matched
		}
	}
	return matched[hash.String()]
}

// solMatchedOutHashes scans the TxOut window once and returns the OutHash of every
// done item of inHash.
func (s *SlasherImpl) solMatchedOutHashes(ctx cosmos.Context, mgr Manager, inHash common.TxID, inboundHeight int64) map[string]bool {
	end := ctx.BlockHeight()
	if coalesce := mgr.Keeper().GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks); coalesce > 1 {
		if over := end % coalesce; over != 0 {
			end += coalesce - over
		}
	}
	start := inboundHeight
	if floor := ctx.BlockHeight() - solSiblingScanMaxBlocks; start < floor {
		start = floor
	}
	if start < 1 {
		start = 1
	}
	matched := make(map[string]bool)
	for h := start; h <= end; h++ {
		txOut, err := s.keeper.GetTxOut(ctx, h)
		if err != nil {
			continue
		}
		for _, item := range txOut.TxArray {
			if item.InHash.Equals(inHash) && !item.OutHash.IsEmpty() && !item.OutHash.Equals(common.BlankTxID) {
				matched[item.OutHash.String()] = true
			}
		}
	}
	return matched
}

// vaultSignerCount counts the members of the given vault that are among the
// given observation signers.
func vaultSignerCount(vault Vault, signers map[string]bool) int {
	var count int
	for _, member := range vault.Membership {
		pk, err := common.NewPubKey(member)
		if err != nil {
			continue
		}
		addr, err := pk.GetAddress(common.THORChain)
		if err != nil {
			continue
		}
		if signers[addr.String()] {
			count++
		}
	}
	return count
}
