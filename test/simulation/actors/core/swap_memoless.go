package core

import (
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"strconv"
	"time"

	sdkmath "cosmossdk.io/math"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SwapMemolessActor
////////////////////////////////////////////////////////////////////////////////////////

//go:generate stringer -type=MemolessType
type MemolessType int

const (
	MemolessTypeRef    MemolessType = iota // "r:<ref>" memo
	MemolessTypeAmount                     // reference encoded in amount
)

type MemolessTransferMode int

const (
	MemolessTransferModeRouter MemolessTransferMode = iota
	MemolessTransferModeDirectERC20
)

func (m MemolessTransferMode) String() string {
	switch m {
	case MemolessTransferModeRouter:
		return "router"
	case MemolessTransferModeDirectERC20:
		return "direct_erc20"
	default:
		return fmt.Sprintf("unknown_%d", m)
	}
}

type SwapMemolessActor struct {
	SwapActor

	transferMode     MemolessTransferMode
	memolessType     MemolessType
	registeredMemo   string
	registrationTxID common.TxID
	memolessRef      uint64
	swapMemo         string
	swapTx           SimTx
	signedSwap       []byte
}

func NewSwapMemolessActor(from, to common.Asset, memolessType MemolessType, rng *rand.Rand) *Actor {
	return newSwapMemolessActor(from, to, memolessType, MemolessTransferModeRouter, rng)
}

func NewDirectERC20SwapMemolessActor(from, to common.Asset, rng *rand.Rand) *Actor {
	return newSwapMemolessActor(from, to, MemolessTypeAmount, MemolessTransferModeDirectERC20, rng)
}

func newSwapMemolessActor(from, to common.Asset, memolessType MemolessType, transferMode MemolessTransferMode, rng *rand.Rand) *Actor {
	name := fmt.Sprintf("SwapMemoless %s => %s", from, to)
	if transferMode == MemolessTransferModeDirectERC20 {
		name = fmt.Sprintf("SwapMemoless direct ERC20 %s => %s", from, to)
	}

	a := &SwapMemolessActor{
		SwapActor: SwapActor{
			Actor: *NewActor(name, rng),
			from:  from,
			to:    to,
		},
		transferMode: transferMode,
		memolessType: memolessType,
	}
	a.Timeout = swapMemolessActorTimeout(from, to)

	a.Ops = append(a.Ops, a.updateLogContext)

	// lock an account with from balance
	a.Ops = append(a.Ops, a.acquireUser)

	// generate swap quote
	a.Ops = append(a.Ops, a.getQuote)

	switch memolessType {
	case MemolessTypeRef:
		a.Ops = append(a.Ops, a.registerSwap)
		a.Ops = append(a.Ops, a.getMemolessRef)
		a.Ops = append(a.Ops, a.prepareSwapMemolessByRef)
		a.Ops = append(a.Ops, a.requotePreparedSwap)
		a.Ops = append(a.Ops, a.signSwap)
		a.Ops = append(a.Ops, a.broadcastSwap)
	case MemolessTypeAmount:
		a.Ops = append(a.Ops, a.registerSwap)
		a.Ops = append(a.Ops, a.getMemolessRef)
		a.Ops = append(a.Ops, a.prepareSwapMemolessByAmount)
		a.Ops = append(a.Ops, a.requoteEncodedSwap)
		a.Ops = append(a.Ops, a.signSwap)
		a.Ops = append(a.Ops, a.broadcastSwap)
	}

	// verify the swap within expected range
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

func swapMemolessActorTimeout(from, to common.Asset) time.Duration {
	timeout := 5 * time.Minute
	if from.Chain.Equals(common.XMRChain) || to.Chain.Equals(common.XMRChain) {
		timeout += 2 * time.Minute
	}
	if from.Chain.Equals(common.SOLChain) || to.Chain.Equals(common.SOLChain) {
		timeout += 2 * time.Minute
	}
	return timeout
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *SwapMemolessActor) updateLogContext(config *OpConfig) OpResult {
	a.SetLogger(log.With().
		Stringer("type", a.memolessType).
		Str("transfer_mode", a.transferMode.String()).
		Logger())
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) getQuote(config *OpConfig) OpResult {
	for attempt := 0; attempt < 4; attempt++ {
		quote, result := a.getQuoteWithUserRetry(config)
		if !result.Continue || result.Finish {
			return result
		}

		if a.memolessType == MemolessTypeAmount {
			requiredAmount := a.maxAmountWithEncodedRefAtOrAbove(a.swapAmount)
			if requiredAmount.GT(a.fromBalance) {
				targetAmount := a.swapAmount
				a.Log().Info().
					Stringer("required_amount", requiredAmount).
					Stringer("user_balance", a.fromBalance).
					Msg("memoless amount encoding may exceed selected user balance, retrying user selection")
				a.releaseUserSelection()
				a.releaseXMRSwapSlot()
				result := a.acquireUserForAmount(config, requiredAmount, false)
				if !result.Continue || result.Finish {
					return result
				}
				a.swapAmount = targetAmount
				continue
			}
		}

		setQuoteExpectations(&a.SwapActor, quote)
		return OpResult{
			Continue: true,
		}
	}

	a.Log().Info().
		Stringer("amount", a.swapAmount).
		Msg("waiting for user that can satisfy memoless amount encoding headroom")
	return OpResult{
		Continue: false,
	}
}

func (a *SwapMemolessActor) registerSwap(config *OpConfig) OpResult {
	// build the swap
	coin := common.NewCoin(common.RuneNative, sdkmath.ZeroUint())
	a.registeredMemo = buildMemolessSwapRegistrationMemo(a.from, a.to, a.toAddress, a.fromAddress)
	registerMemo := fmt.Sprintf("REFERENCE:%s:%s", a.from, a.registeredMemo)

	a.Log().Info().
		Str("registerMemo", registerMemo).
		Msg("registering memoless swap")

	thorAddress, err := a.user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}
	thorAccAddr, err := thorAddress.AccAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor acc address")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}
	deposit := types.NewMsgDeposit(common.NewCoins(coin), registerMemo, thorAccAddr)
	a.Log().Info().Interface("deposit", deposit).Msg("registering memoless swap")

	// broadcast the registration with blocking to wait for block commitment
	a.registrationTxID, err = a.user.Thorchain.BroadcastWithBlocking(deposit)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().
		Stringer("txid", a.registrationTxID).
		Str("memo", registerMemo).
		Msg("broadcasted register tx")
	return OpResult{
		Continue: true,
	}
}

func buildMemolessSwapRegistrationMemo(from, to common.Asset, destination, refundAddress common.Address) string {
	address := destination.String()
	if from.GetLayer1Asset().GetChain().RequiresExplicitRefundAddress() {
		address += "/" + refundAddress.String()
	}
	return fmt.Sprintf("=:%s:%s", to, address)
}

func (a *SwapMemolessActor) getMemolessRef(config *OpConfig) OpResult {
	// Retry logic to handle race condition between tx broadcast and state query
	// The transaction may take 1-2 blocks to be fully committed to state
	maxAttempts := 60 // 60 attempts * 500ms = 30 seconds timeout
	var referenceMemo openapi.ReferenceMemoResponse
	var err error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		referenceMemo, err = thornode.GetMemoHash(a.registrationTxID.String())
		if err != nil {
			a.Log().Debug().Err(err).Int("attempt", attempt+1).Msg("failed to get reference memo, retrying")
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if referenceMemo.Memo == "" {
			a.Log().Debug().Int("attempt", attempt+1).Msg("reference memo not found yet, retrying")
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Found the memo, break out of retry loop
		break
	}

	// Final check after all retries
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get reference memo after retries")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	if referenceMemo.Memo == "" {
		a.Log().Error().Stringer("registrationTxID", a.registrationTxID).Msg("reference memo not found after retries")
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("reference memo not found after retries"),
		}
	}

	if referenceMemo.Memo != a.registeredMemo {
		a.Log().Error().
			Str("expected", a.registeredMemo).
			Str("actual", referenceMemo.Memo).
			Msg("reference memo does not match")
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("reference memo does not match"),
		}
	}

	a.memolessRef, err = strconv.ParseUint(referenceMemo.Reference, 10, 64)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to parse memoless ref")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	a.Log().Info().Uint64("ref", a.memolessRef).Msg("got memoless ref")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) prepareSwapMemolessByRef(config *OpConfig) OpResult {
	// encode reference memo with leading zeros
	modulusStr := fmt.Sprintf("%d", a.amountModulus())
	memolessRefFormatString := fmt.Sprintf("r:%%0%dd", len(modulusStr)-1)
	a.swapMemo = fmt.Sprintf(memolessRefFormatString, a.memolessRef)
	a.Log().Info().Str("memo", a.swapMemo).Msg("prepared memoless swap memo")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) requotePreparedSwap(config *OpConfig) OpResult {
	quote, quoteAmount, err := getSwapQuoteWithRecommendedMin(a.from, a.to, a.swapAmount, a.fromBalance, a.toAddress, a.fromAddress, a.from.Chain.IsUTXO())
	if err != nil {
		a.Log().Error().Err(err).Str("amount", a.swapAmount.String()).Msg("failed to get quote for prepared memoless swap")
		return OpResult{
			Continue: false,
		}
	}

	if err := a.updateSwapAmount(quoteAmount, "recommended minimum inbound amount after memoless reference registration"); err != nil {
		return OpResult{
			Error:  err,
			Finish: true,
		}
	}

	setQuoteExpectations(&a.SwapActor, quote)
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) prepareSwapMemolessByAmount(config *OpConfig) OpResult {
	// encode the memoless ref in the amount
	modulus := a.amountModulus()
	scalingDivisor := a.amountScalingDivisor()
	a.Log().Info().
		Uint64("modulus", modulus).
		Uint64("scaling_divisor", scalingDivisor).
		Msg("preparing memoless by amount")
	before := a.swapAmount

	if scalingDivisor > 1 {
		normalizedAmount := a.swapAmount.QuoUint64(scalingDivisor)
		normalizedAmount = normalizedAmount.Sub(normalizedAmount.Mod(sdkmath.NewUint(modulus))) // clear last digits
		normalizedAmount = normalizedAmount.Add(sdkmath.NewUint(a.memolessRef))
		a.swapAmount = normalizedAmount.MulUint64(scalingDivisor)
	} else {
		a.swapAmount = a.swapAmount.Sub(a.swapAmount.Mod(sdkmath.NewUint(modulus))) // clear last digits
		a.swapAmount = a.swapAmount.Add(sdkmath.NewUint(a.memolessRef))
	}

	a.Log().Info().
		Stringer("before", before).
		Stringer("after", a.swapAmount).
		Msg("encoded memoless ref in amount")

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) requoteEncodedSwap(config *OpConfig) OpResult {
	for attempt := 0; attempt < 4; attempt++ {
		quote, quoteAmount, err := getSwapQuoteWithRecommendedMin(a.from, a.to, a.swapAmount, a.fromBalance, a.toAddress, a.fromAddress, a.from.Chain.IsUTXO())
		if err != nil {
			a.Log().Error().Err(err).Str("amount", a.swapAmount.String()).Msg("failed to get quote for encoded memoless swap amount")
			return OpResult{
				Continue: false,
			}
		}

		if quoteAmount.GT(a.swapAmount) {
			encodedAmount := a.amountWithEncodedRefAtOrAbove(quoteAmount)
			if encodedAmount.Equal(a.swapAmount) {
				return OpResult{
					Error:  fmt.Errorf("failed to raise memoless encoded amount to recommended minimum"),
					Finish: true,
				}
			}

			if err := a.updateSwapAmount(encodedAmount, "recommended minimum inbound amount after memoless encoding"); err != nil {
				return OpResult{
					Error:  err,
					Finish: true,
				}
			}
			continue
		}

		setQuoteExpectations(&a.SwapActor, quote)
		return OpResult{
			Continue: true,
		}
	}

	return OpResult{
		Error:  fmt.Errorf("failed to settle memoless encoded amount within recommended minimum"),
		Finish: true,
	}
}

func (a *SwapMemolessActor) signSwap(config *OpConfig) OpResult {
	if a.from.Chain.IsEVM() && !a.from.IsGasAsset() {
		if a.transferMode == MemolessTransferModeDirectERC20 {
			return a.signDirectTokenTransfer()
		}
		return a.signTokenSwap()
	}

	// get inbound address
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}

	// create tx
	tx := SimTx{
		Chain:       a.from.Chain,
		ToAddress:   inboundAddr,
		Coin:        common.NewCoin(a.from, a.swapAmount),
		Memo:        a.swapMemo,
		OneShotMemo: true,
	}
	a.swapTx = tx

	client := a.user.ChainClients[a.from.Chain]
	if a.from.Chain.Equals(common.TRONChain) || a.from.Chain.Equals(common.SOLChain) {
		a.signedSwap = nil
		return OpResult{
			Continue: true,
		}
	}

	// sign transaction
	a.signedSwap, err = client.SignTx(tx)
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) signDirectTokenTransfer() OpResult {
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}

	token, ok := evm.Tokens(a.from.Chain)[a.from]
	if !ok {
		a.Log().Error().Stringer("asset", a.from).Msg("failed to find token metadata for source asset")
		return OpResult{
			Continue: false,
		}
	}

	tokenAmount := a.tokenAmount(token.Decimals)

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Error().Msg("failed to get evm client")
		return OpResult{
			Continue: false,
		}
	}

	eInboundAddr := ecommon.HexToAddress(inboundAddr.String())
	directTx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: common.Address(token.Address),
		ABI:      evm.ERC20ABI(),
		Method:   "transfer",
		Args:     []interface{}{eInboundAddr, tokenAmount.BigInt()},
	}

	a.signedSwap, err = client.SignContractTx(directTx)
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to sign direct token memoless swap tx")
		return OpResult{
			Continue: false,
		}
	}

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) signTokenSwap() OpResult {
	// get router address
	inboundAddr, routerAddr, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}
	if routerAddr == nil {
		a.Log().Error().Msg("failed to get router address")
		return OpResult{
			Continue: false,
		}
	}

	token, ok := evm.Tokens(a.from.Chain)[a.from]
	if !ok {
		a.Log().Error().Stringer("asset", a.from).Msg("failed to find token metadata for source asset")
		return OpResult{
			Continue: false,
		}
	}

	tokenAmount := a.tokenAmount(token.Decimals)

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Error().Msg("failed to get evm client")
		return OpResult{
			Continue: false,
		}
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

	signedApprove, err := client.SignContractTx(approveTx)
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to sign router approve tx")
		return OpResult{
			Continue: false,
		}
	}

	approveTxID, err := client.BroadcastTx(signedApprove)
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to broadcast router approve tx")
		return OpResult{
			Continue: false,
		}
	}
	a.Log().Info().Str("txid", approveTxID).Msg("broadcasted router approve tx")

	// call depositWithExpiry with memoless memo (or empty memo for amount mode)
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
			a.swapMemo,
			big.NewInt(expiry),
		},
	}

	a.signedSwap, err = client.SignContractTx(depositTx)
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to sign token memoless swap tx")
		return OpResult{
			Continue: false,
		}
	}

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) broadcastSwap(config *OpConfig) OpResult {
	if result := a.ensureXMRVaultSpendable(config, a.requiredOutboundSpend); !result.Continue || result.Finish {
		return result
	}

	client := a.user.ChainClients[a.from.Chain]
	var err error
	if a.from.Chain.Equals(common.TRONChain) || a.from.Chain.Equals(common.SOLChain) {
		a.swapTxID, err = SignAndBroadcastTx(client, a.swapTx)
	} else {
		a.swapTxID, err = client.BroadcastTx(a.signedSwap)
	}
	if err != nil {
		if result, ok := a.finishAfterMemolessFundingError(err); ok {
			return result
		}
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return SimTxErrorResult(err)
	}

	a.Log().Info().
		Str("txid", a.swapTxID).
		Stringer("amount", a.swapAmount).
		Str("memo", a.swapMemo).
		Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// finishAfterMemolessFundingError keeps memoless swaps from retrying forever
// after a selected user loses the spendable balance needed for the inbound.
func (a *SwapMemolessActor) finishAfterMemolessFundingError(err error) (OpResult, bool) {
	if !isPostSelectionFundingError(err) {
		return OpResult{}, false
	}
	return a.finishAfterPostSelectionFundingError(err), true
}

func (a *SwapMemolessActor) amountModulus() uint64 {
	memolessRefCount := constants.NewConstantValue().GetInt64Value(constants.MemolessTxnRefCount)
	txnRefLength := len(fmt.Sprintf("%d", memolessRefCount))
	var modulus uint64 = 1
	for i := 0; i < txnRefLength; i++ {
		modulus *= 10
	}
	return modulus
}

func (a *SwapMemolessActor) amountScalingDivisor() uint64 {
	// ExtractReferenceFromAmount normalizes decimals < 1e8 by dividing the observed
	// amount before modulo reference extraction. Mirror that behavior here so
	// amount-encoded references can be generated for low-decimal gas assets and tokens.
	decimals := int(common.THORChainDecimals)
	if a.from.IsGasAsset() {
		decimals = int(a.from.Chain.GetGasAssetDecimal())
	} else if a.from.Chain.IsEVM() {
		token, ok := evm.Tokens(a.from.Chain)[a.from]
		if !ok {
			return 1
		}
		decimals = token.Decimals
	}

	if decimals >= int(common.THORChainDecimals) {
		return 1
	}

	divisor := uint64(1)
	for i := decimals; i < int(common.THORChainDecimals); i++ {
		divisor *= 10
	}
	return divisor
}

func (a *SwapMemolessActor) amountWithEncodedRefAtOrAbove(target sdkmath.Uint) sdkmath.Uint {
	modulus := a.amountModulus()
	scalingDivisor := a.amountScalingDivisor()

	baseAmount := target.Uint64()
	if scalingDivisor == 1 {
		remainder := baseAmount % modulus
		if remainder <= a.memolessRef {
			return sdkmath.NewUint(baseAmount - remainder + a.memolessRef)
		}
		return sdkmath.NewUint(baseAmount - remainder + modulus + a.memolessRef)
	}

	normalizedAmount := baseAmount / scalingDivisor
	if baseAmount%scalingDivisor != 0 {
		normalizedAmount++
	}
	remainder := normalizedAmount % modulus
	if remainder <= a.memolessRef {
		return sdkmath.NewUint(normalizedAmount - remainder + a.memolessRef).MulUint64(scalingDivisor)
	}
	return sdkmath.NewUint(normalizedAmount - remainder + modulus + a.memolessRef).MulUint64(scalingDivisor)
}

func (a *SwapMemolessActor) maxAmountWithEncodedRefAtOrAbove(target sdkmath.Uint) sdkmath.Uint {
	modulus := a.amountModulus()
	scalingDivisor := a.amountScalingDivisor()

	baseAmount := target.Uint64()
	if scalingDivisor == 1 {
		remainder := baseAmount % modulus
		return sdkmath.NewUint(baseAmount - remainder + modulus - 1)
	}

	normalizedAmount := baseAmount / scalingDivisor
	if baseAmount%scalingDivisor != 0 {
		normalizedAmount++
	}
	remainder := normalizedAmount % modulus
	return sdkmath.NewUint(normalizedAmount - remainder + modulus - 1).MulUint64(scalingDivisor)
}

func (a *SwapMemolessActor) tokenAmount(decimals int) sdkmath.Uint {
	factor := sdkmath.NewUintFromBigInt(big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	tokenAmount := a.swapAmount.Mul(factor)
	return tokenAmount.QuoUint64(common.One)
}
