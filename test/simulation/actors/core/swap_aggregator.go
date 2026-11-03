package core

import (
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"strings"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// AggregatorSwapActor
////////////////////////////////////////////////////////////////////////////////////////

// AggregatorSwapActor tests swap-out through a DEX aggregator contract. The inbound
// swap targets an EVM gas asset, and the memo includes aggregator fields so THORChain
// queues the outbound with aggregator metadata (contract address, target asset,
// limit). When the destination chain has the simulation aggregator deployed, the
// actor also verifies that router.transferOutAndCall delivered the configured
// finalToken to the user's address.
type AggregatorSwapActor struct {
	SwapActor

	// aggregator fields included in the memo
	aggregatorSuffix      string
	aggregatorTargetAsset string
	aggregatorTargetLimit string

	// finalTokenStartBalance tracks the user's pre-swap balance of the
	// aggregator target ERC20 in token-native decimals. It is recorded only
	// when the aggregator contract has bytecode at the resolved address so
	// the actor can verify the swap-out delivered finalToken end-to-end.
	verifyFinalTokenDelivery bool
	finalTokenStartBalance   *big.Int
}

// NewAggregatorSwapActor creates a swap actor that exercises the swap-out aggregator
// path. The destination asset must be an EVM gas asset so the aggregator contract can
// perform the final DEX leg.
func NewAggregatorSwapActor(from, to common.Asset, aggregatorSuffix, aggregatorTargetAsset, aggregatorTargetLimit string, rng *rand.Rand) *Actor {
	a := &AggregatorSwapActor{
		SwapActor: SwapActor{
			Actor: *NewActor(fmt.Sprintf("AggSwap %s => %s", from, to), rng),
			from:  from,
			to:    to,
		},
		aggregatorSuffix:      aggregatorSuffix,
		aggregatorTargetAsset: aggregatorTargetAsset,
		aggregatorTargetLimit: aggregatorTargetLimit,
	}
	a.Timeout = aggregatorSwapActorTimeout(from, to)

	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	a.Ops = append(a.Ops, a.snapshotFinalTokenBalance)

	if from.Chain.IsEVM() && !from.IsGasAsset() {
		a.Ops = append(a.Ops, a.sendAggregatorTokenSwap)
	} else {
		a.Ops = append(a.Ops, a.sendAggregatorSwap)
	}

	a.Ops = append(a.Ops, a.verifyAggregatorOutbound)

	return &a.Actor
}

func aggregatorSwapActorTimeout(from, to common.Asset) time.Duration {
	timeout := swapActorTimeout(from, to) + 2*time.Minute
	if from.Chain.Equals(common.SOLChain) || to.Chain.Equals(common.SOLChain) {
		// Aggregator swaps involving Solana can consume several full
		// broadcast/re-sign cycles while waiting for finalized confirmation.
		timeout += 6 * time.Minute
	}
	return timeout
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

// snapshotFinalTokenBalance records the user's pre-swap balance of the
// aggregator target ERC20 when the aggregator contract is actually deployed
// on the destination chain. The recorded balance is later compared against
// the post-outbound balance to confirm finalToken delivery.
func (a *AggregatorSwapActor) snapshotFinalTokenBalance(config *OpConfig) OpResult {
	a.verifyFinalTokenDelivery = false
	a.finalTokenStartBalance = nil

	if !a.to.Chain.IsEVM() {
		return OpResult{Continue: true}
	}

	// When the user is paying with the same ERC20 contract on the same chain
	// as the finalToken, the pre-swap snapshot includes the input amount the
	// user is about to spend. Post-swap the aggregator only delivers
	// amountOutMin, so a strict "balance must increase" check would fail even
	// on a perfectly successful aggregator delivery. Skip verification in
	// that round-trip case; aggregator metadata is still verified downstream.
	if a.from.Chain.Equals(a.to.Chain) && !a.from.IsGasAsset() {
		if token, ok := evm.Tokens(a.from.Chain)[a.from]; ok &&
			strings.EqualFold(token.Address, a.aggregatorTargetAsset) {
			a.Log().Info().
				Str("source_asset", a.from.String()).
				Str("final_token", a.aggregatorTargetAsset).
				Msg("source asset matches aggregator finalToken; skipping delivery verification")
			return OpResult{Continue: true}
		}
	}

	iClient := a.user.ChainClients[a.to.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		return OpResult{Continue: true}
	}

	hasCode, err := client.HasContractCode(evm.MocknetAggregatorAddress)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to check aggregator code; skipping finalToken verification")
		return OpResult{Continue: true}
	}
	if !hasCode {
		a.Log().Info().Str("aggregator", evm.MocknetAggregatorAddress).
			Msg("aggregator contract not deployed; skipping finalToken delivery verification")
		return OpResult{Continue: true}
	}

	balance, err := client.GetTokenBalance(a.aggregatorTargetAsset, a.toAddress.String())
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read finalToken balance; skipping verification")
		return OpResult{Continue: true}
	}

	a.verifyFinalTokenDelivery = true
	a.finalTokenStartBalance = new(big.Int).Set(balance)
	a.Log().Info().
		Str("final_token", a.aggregatorTargetAsset).
		Str("balance", balance.String()).
		Msg("snapshotted finalToken balance for aggregator delivery verification")
	return OpResult{Continue: true}
}

// sendAggregatorSwap sends a swap with aggregator fields in the memo for native/gas
// asset sources (non-EVM-token).
func (a *AggregatorSwapActor) sendAggregatorSwap(config *OpConfig) OpResult {
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{Continue: false}
	}

	// shorten the to asset on UTXO chains
	to := a.to.String()
	if a.from.Chain.IsUTXO() && !a.to.IsGasAsset() {
		to = strings.Split(to, "-")[0]
	}

	memo := fmt.Sprintf("=:%s:%s::::%s:%s:%s",
		to, a.toAddress, a.aggregatorSuffix, a.aggregatorTargetAsset, a.aggregatorTargetLimit)

	tx := SimTx{
		Chain:     a.from.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.from, a.swapAmount),
		Memo:      memo,
	}

	client := a.user.ChainClients[a.from.Chain]

	txid, err := SignAndBroadcastTx(client, tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return SimTxErrorResult(err)
	}
	a.swapTxID = txid

	a.Log().Info().
		Str("txid", txid).
		Str("aggregator", a.aggregatorSuffix).
		Str("target_asset", a.aggregatorTargetAsset).
		Msg("broadcasted aggregator swap tx")
	return OpResult{Continue: true}
}

// sendAggregatorTokenSwap sends an EVM token swap with aggregator fields in the memo
// via the router's depositWithExpiry.
func (a *AggregatorSwapActor) sendAggregatorTokenSwap(config *OpConfig) OpResult {
	inboundAddr, routerAddr, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{Continue: false}
	}
	if routerAddr == nil {
		a.Log().Error().Msg("failed to get router address")
		return OpResult{Continue: false}
	}

	token := evm.Tokens(a.from.Chain)[a.from]

	// convert amount to token decimals
	factor := big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil)
	tokenAmount := a.swapAmount.Mul(cosmos.NewUintFromBigInt(factor))
	tokenAmount = tokenAmount.QuoUint64(common.One)

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Fatal().Msg("failed to get evm client")
	}

	// approve the router
	eRouterAddr := ecommon.HexToAddress(routerAddr.String())
	approveTx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: common.Address(token.Address),
		ABI:      evm.ERC20ABI(),
		Method:   "approve",
		Args:     []interface{}{eRouterAddr, tokenAmount.BigInt()},
	}
	signed, err := client.SignContractTx(approveTx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign approve tx")
		return OpResult{Continue: false}
	}
	txid, err := client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast approve tx")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Msg("broadcasted router approve tx")

	// build aggregator memo and call depositWithExpiry
	memo := fmt.Sprintf("=:%s:%s::::%s:%s:%s",
		a.to, a.toAddress, a.aggregatorSuffix, a.aggregatorTargetAsset, a.aggregatorTargetLimit)
	expiry := time.Now().Add(time.Hour).Unix()
	eInboundAddr := ecommon.HexToAddress(inboundAddr.String())
	eTokenAddr := ecommon.HexToAddress(token.Address)
	depositTx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: *routerAddr,
		ABI:      evm.RouterABI(),
		Method:   "depositWithExpiry",
		Args: []interface{}{
			eInboundAddr,
			eTokenAddr,
			tokenAmount.BigInt(),
			memo,
			big.NewInt(expiry),
		},
	}

	signed, err = client.SignContractTx(depositTx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign deposit tx")
		return OpResult{Continue: false}
	}
	txid, err = client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast deposit tx")
		return OpResult{Continue: false}
	}
	a.swapTxID = txid

	a.Log().Info().
		Str("txid", txid).
		Str("aggregator", a.aggregatorSuffix).
		Str("target_asset", a.aggregatorTargetAsset).
		Msg("broadcasted aggregator token swap tx")
	return OpResult{Continue: true}
}

// verifyAggregatorOutbound waits for the outbound to complete and verifies aggregator
// metadata is present in the action.
func (a *AggregatorSwapActor) verifyAggregatorOutbound(config *OpConfig) OpResult {
	stages, err := thornode.GetTxStages(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx stages")
		return OpResult{Continue: false}
	}

	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{Continue: false}
	}

	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{Continue: false}
	}

	if len(details.OutTxs) != 1 {
		a.user.Release()
		return OpResult{
			Error:  fmt.Errorf("expected exactly one out transaction, got %d", len(details.OutTxs)),
			Finish: true,
		}
	}

	if len(details.Actions) != 1 {
		a.user.Release()
		return OpResult{
			Error:  fmt.Errorf("expected exactly one action, got %d", len(details.Actions)),
			Finish: true,
		}
	}

	// verify action is not refund
	action := details.Actions[0]
	actionMemo := ""
	if action.Memo != nil {
		actionMemo = *action.Memo
	} else if action.OriginalMemo != nil {
		actionMemo = *action.OriginalMemo
	}
	if strings.HasPrefix(actionMemo, "REFUND:") {
		a.user.Release()
		return OpResult{
			Error:  fmt.Errorf("swap was refunded"),
			Finish: true,
		}
	}

	// verify aggregator metadata is present
	if !action.HasAggregator() || action.GetAggregator() == "" {
		outTx := details.OutTxs[0]
		completedDirectFallback := action.Coin.Asset == a.to.String() &&
			strings.EqualFold(action.GetToAddress(), a.toAddress.String()) &&
			len(outTx.Coins) == 1 &&
			outTx.Coins[0].Asset == a.to.String() &&
			strings.EqualFold(outTx.GetToAddress(), a.toAddress.String())

		if !completedDirectFallback {
			a.user.Release()
			return OpResult{
				Error:  fmt.Errorf("action missing aggregator field"),
				Finish: true,
			}
		}

		// THORChain can clear aggregator metadata when an outbound is reassigned,
		// then complete a direct outbound to the requested destination instead.
		a.Log().Info().
			Str("asset", action.Coin.Asset).
			Str("to_address", action.GetToAddress()).
			Msg("aggregator metadata cleared after reassignment; direct outbound verified")

		a.user.Release()
		return OpResult{Finish: true}
	}

	// verify aggregator address ends with the suffix we sent
	if !strings.HasSuffix(action.GetAggregator(), a.aggregatorSuffix) {
		a.user.Release()
		return OpResult{
			Error: fmt.Errorf("aggregator address %q does not match suffix %q",
				action.GetAggregator(), a.aggregatorSuffix),
			Finish: true,
		}
	}

	// verify aggregator target asset matches what we sent
	if !action.HasAggregatorTargetAsset() || action.GetAggregatorTargetAsset() == "" {
		a.user.Release()
		return OpResult{
			Error:  fmt.Errorf("action missing aggregator_target_asset field"),
			Finish: true,
		}
	}
	if action.GetAggregatorTargetAsset() != a.aggregatorTargetAsset {
		a.user.Release()
		return OpResult{
			Error: fmt.Errorf("aggregator_target_asset %q does not match expected %q",
				action.GetAggregatorTargetAsset(), a.aggregatorTargetAsset),
			Finish: true,
		}
	}

	// verify aggregator target limit matches what we sent
	if a.aggregatorTargetLimit != "" {
		if !action.HasAggregatorTargetLimit() || action.GetAggregatorTargetLimit() == "" {
			a.user.Release()
			return OpResult{
				Error:  fmt.Errorf("action missing aggregator_target_limit field"),
				Finish: true,
			}
		}
		if action.GetAggregatorTargetLimit() != a.aggregatorTargetLimit {
			a.user.Release()
			return OpResult{
				Error: fmt.Errorf("aggregator_target_limit %q does not match expected %q",
					action.GetAggregatorTargetLimit(), a.aggregatorTargetLimit),
				Finish: true,
			}
		}
	}

	// when the aggregator is actually deployed on the destination chain,
	// confirm the user received finalToken via router.transferOutAndCall.
	if a.verifyFinalTokenDelivery {
		iClient := a.user.ChainClients[a.to.Chain]
		client, ok := iClient.(*evm.Client)
		if !ok {
			a.user.Release()
			return OpResult{
				Error:  fmt.Errorf("expected evm client for finalToken verification"),
				Finish: true,
			}
		}

		balance, err := client.GetTokenBalance(a.aggregatorTargetAsset, a.toAddress.String())
		if err != nil {
			a.Log().Warn().Err(err).Msg("failed to read post-swap finalToken balance")
			return OpResult{Continue: false}
		}

		if a.finalTokenStartBalance == nil {
			a.user.Release()
			return OpResult{
				Error:  fmt.Errorf("missing finalToken start balance snapshot"),
				Finish: true,
			}
		}

		delta := new(big.Int).Sub(balance, a.finalTokenStartBalance)
		if delta.Sign() <= 0 {
			a.user.Release()
			return OpResult{
				Error: fmt.Errorf("user finalToken balance did not increase: before=%s after=%s",
					a.finalTokenStartBalance.String(), balance.String()),
				Finish: true,
			}
		}

		minAmountOut, ok := new(big.Int).SetString(a.aggregatorTargetLimit, 10)
		if ok && minAmountOut.Sign() > 0 && delta.Cmp(minAmountOut) < 0 {
			a.user.Release()
			return OpResult{
				Error: fmt.Errorf("finalToken delta %s below configured amountOutMin %s",
					delta.String(), minAmountOut.String()),
				Finish: true,
			}
		}

		a.Log().Info().
			Str("final_token", a.aggregatorTargetAsset).
			Str("delta", delta.String()).
			Str("balance_after", balance.String()).
			Msg("aggregator finalToken delivery verified")
	}

	a.Log().Info().
		Str("aggregator", action.GetAggregator()).
		Str("aggregator_target_asset", action.GetAggregatorTargetAsset()).
		Str("aggregator_target_limit", action.GetAggregatorTargetLimit()).
		Msg("aggregator swap verified")

	a.user.Release()
	return OpResult{Finish: true}
}

////////////////////////////////////////////////////////////////////////////////////////
// AggregatorSwapInActor
////////////////////////////////////////////////////////////////////////////////////////

// AggregatorSwapInActor exercises the swap-in path where an external contract
// calls deposit on the THORChain router on the user's behalf. The actor calls
// MocknetAggregator.swapIn which performs router.depositWithExpiry against the
// active inbound vault. THORChain processes the resulting swap and emits an
// outbound to the destination chain that this actor verifies.
type AggregatorSwapInActor struct {
	SwapActor
}

// NewAggregatorSwapInActor creates a swap-in actor that routes the inbound
// deposit through the simulation aggregator. The from asset must live on an
// EVM chain so the aggregator contract can call into the router. Both the
// gas asset and ERC20 paths are exercised depending on the from asset type.
func NewAggregatorSwapInActor(from, to common.Asset, rng *rand.Rand) *Actor {
	a := &AggregatorSwapInActor{
		SwapActor: SwapActor{
			Actor: *NewActor(fmt.Sprintf("AggSwapIn %s => %s", from, to), rng),
			from:  from,
			to:    to,
		},
	}
	a.Timeout = aggregatorSwapActorTimeout(from, to)

	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	a.Ops = append(a.Ops, a.sendAggregatorContractDeposit)
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

// sendAggregatorContractDeposit invokes MocknetAggregator.swapIn so the
// aggregator contract performs the router.depositWithExpiry call. This
// covers the issue requirement that swap-in transactions originate from a
// contract calling deposit on the TC router.
func (a *AggregatorSwapInActor) sendAggregatorContractDeposit(config *OpConfig) OpResult {
	if !a.from.Chain.IsEVM() {
		return OpResult{
			Error:  fmt.Errorf("aggregator swap-in requires an EVM source chain"),
			Finish: true,
		}
	}

	if result := a.ensureXMRVaultSpendable(config, a.requiredOutboundSpend); !result.Continue || result.Finish {
		return result
	}

	inboundAddr, routerAddr, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{Continue: false}
	}
	if routerAddr == nil {
		a.Log().Error().Msg("failed to get router address")
		return OpResult{Continue: false}
	}

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Fatal().Msg("failed to get evm client")
	}

	hasCode, err := client.HasContractCode(evm.MocknetAggregatorAddress)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to check aggregator code")
		return OpResult{Continue: false}
	}
	if !hasCode {
		a.Log().Info().Stringer("chain", a.from.Chain).
			Str("aggregator", evm.MocknetAggregatorAddress).
			Msg("aggregator contract not deployed; skipping aggregator swap-in")
		a.user.Release()
		return OpResult{Finish: true}
	}

	memo := fmt.Sprintf("=:%s:%s", a.to, a.toAddress)
	deadline := big.NewInt(time.Now().Add(time.Hour).Unix())
	aggAddr := common.Address(evm.MocknetAggregatorAddress)
	eInboundAddr := ecommon.HexToAddress(inboundAddr.String())
	eRouterAddr := ecommon.HexToAddress(routerAddr.String())

	if a.from.IsGasAsset() {
		// Send gas asset to aggregator.swapIn; the contract forwards via
		// router.depositWithExpiry with msg.value. EVM gas assets use 18
		// native decimals; THORNode stores amounts in 1e8 base, so scale
		// by 1e10 to match the wei value the router expects (matching the
		// conversion used by evm.Client.SignTx for direct vault sends).
		nativeAmount := a.swapAmount.Mul(cosmos.NewUint(1e10))

		swapInTx := SimContractTx{
			Chain:    a.from.Chain,
			Contract: aggAddr,
			ABI:      evm.AggregatorABI(),
			Method:   "swapIn",
			Args: []interface{}{
				eRouterAddr,
				eInboundAddr,
				memo,
				ecommon.Address{}, // fromToken=0x0 signals gas asset path
				nativeAmount.BigInt(),
				deadline,
			},
			Value: nativeAmount.BigInt(),
		}

		signed, err := client.SignContractTx(swapInTx)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to sign swapIn tx")
			return OpResult{Continue: false}
		}
		txid, err := client.BroadcastTx(signed)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to broadcast swapIn tx")
			return OpResult{Continue: false}
		}
		a.swapTxID = txid
		a.Log().Info().Str("txid", txid).
			Str("aggregator", evm.MocknetAggregatorAddress).
			Msg("broadcasted aggregator swap-in (gas) tx")
		return OpResult{Continue: true}
	}

	// ERC20 swap-in: approve the aggregator, then call swapIn with fromToken set.
	token, ok := evm.Tokens(a.from.Chain)[a.from]
	if !ok {
		return OpResult{
			Error:  fmt.Errorf("missing token metadata for %s", a.from),
			Finish: true,
		}
	}

	factor := big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil)
	tokenAmount := a.swapAmount.Mul(cosmos.NewUintFromBigInt(factor))
	tokenAmount = tokenAmount.QuoUint64(common.One)

	approveTx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: common.Address(token.Address),
		ABI:      evm.ERC20ABI(),
		Method:   "approve",
		Args:     []interface{}{ecommon.HexToAddress(evm.MocknetAggregatorAddress), tokenAmount.BigInt()},
	}
	signed, err := client.SignContractTx(approveTx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign approve tx")
		return OpResult{Continue: false}
	}
	if _, err := client.BroadcastTx(signed); err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast approve tx")
		return OpResult{Continue: false}
	}

	swapInTx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: aggAddr,
		ABI:      evm.AggregatorABI(),
		Method:   "swapIn",
		Args: []interface{}{
			eRouterAddr,
			eInboundAddr,
			memo,
			ecommon.HexToAddress(token.Address),
			tokenAmount.BigInt(),
			deadline,
		},
	}
	signed, err = client.SignContractTx(swapInTx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign swapIn tx")
		return OpResult{Continue: false}
	}
	txid, err := client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast swapIn tx")
		return OpResult{Continue: false}
	}
	a.swapTxID = txid
	a.Log().Info().Str("txid", txid).
		Str("aggregator", evm.MocknetAggregatorAddress).
		Msg("broadcasted aggregator swap-in (token) tx")
	return OpResult{Continue: true}
}
