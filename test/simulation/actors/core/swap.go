package core

import (
	"errors"
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"regexp"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	ecommon "github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SwapActor
////////////////////////////////////////////////////////////////////////////////////////

type SwapActor struct {
	Actor

	user *User

	// starting balances
	from        common.Asset
	fromAddress common.Address
	fromBalance sdkmath.Uint
	to          common.Asset
	toAddress   common.Address
	toBalance   sdkmath.Uint

	swapAmount sdkmath.Uint
	swapTxID   string

	// expected range for received amount, including outbound fee
	requiredOutboundSpend sdkmath.Uint
	minExpected           sdkmath.Uint
	maxExpected           sdkmath.Uint
	// expectedOutboundVault lets specialized swap actors prove a specific vault
	// produced the outbound instead of only checking the final amount. It is the
	// FROST key for XMR and the primary vault key for other chains.
	expectedOutboundVault common.PubKey

	// Required verification actors must fail rather than skip an unfunded swap.
	requireOutbound bool
	useTexAddress   bool

	xmrSwapSlotAcquired bool

	getSwapStages  func(string) (openapi.TxStagesResponse, error)
	getSwapDetails func(string) (openapi.TxDetailsResponse, error)
}

var (
	recommendedMinAmountInPattern = regexp.MustCompile(`recommended_min_amount_in:\s*(\d+)`)
	xmrSwapSlot                   = make(chan struct{}, 1)
)

func swapQuoteRecommendedMinAttempts(from, to common.Asset) int {
	if from.Chain.Equals(common.XMRChain) || to.Chain.Equals(common.XMRChain) {
		return 64
	}
	return 4
}

func NewSwapActor(from, to common.Asset, rng *rand.Rand) *Actor {
	return &newSwapActor(from, to, rng).Actor
}

// NewZECSwapActor requires a completed ZEC swap using the selected address format.
func NewZECSwapActor(from common.Asset, useTexAddress bool, rng *rand.Rand) *Actor {
	a := newSwapActor(from, common.ZECAsset, rng)
	a.requireOutbound = true
	a.useTexAddress = useTexAddress
	if useTexAddress {
		a.Name += " (TEX)"
	} else {
		a.Name += " (transparent)"
	}
	return &a.Actor
}

func newSwapActor(from, to common.Asset, rng *rand.Rand) *SwapActor {
	a := &SwapActor{
		Actor: *NewActor(fmt.Sprintf("Swap %s => %s", from, to), rng),
		from:  from,
		to:    to,
	}
	a.Timeout = swapActorTimeout(from, to)

	// lock an account with from balance
	a.Ops = append(a.Ops, a.acquireUser)

	// generate swap quote
	a.Ops = append(a.Ops, a.getQuote)

	// send swap inbound
	if from.Chain.IsEVM() && !from.IsGasAsset() {
		a.Ops = append(a.Ops, a.sendTokenSwap)
	} else {
		a.Ops = append(a.Ops, a.sendSwap)
	}

	// verify the swap within expected range
	a.Ops = append(a.Ops, a.verifyOutbound)

	return a
}

func swapActorTimeout(from, to common.Asset) time.Duration {
	if from.Chain.Equals(common.XMRChain) || to.Chain.Equals(common.XMRChain) {
		return 8 * time.Minute
	}
	if from.Chain.Equals(common.SOLChain) || to.Chain.Equals(common.SOLChain) {
		return 4 * time.Minute
	}
	if from.Chain.Equals(common.TRONChain) || to.Chain.Equals(common.TRONChain) {
		return 6 * time.Minute
	}
	return 2 * time.Minute
}

func getRecommendedMinAmountIn(quote openapi.QuoteSwapResponse) (sdkmath.Uint, bool) {
	if quote.RecommendedMinAmountIn == nil || *quote.RecommendedMinAmountIn == "" {
		return sdkmath.ZeroUint(), false
	}
	return sdkmath.NewUintFromString(*quote.RecommendedMinAmountIn), true
}

func getRecommendedMinAmountInFromError(err error) (sdkmath.Uint, bool) {
	matches := recommendedMinAmountInPattern.FindStringSubmatch(err.Error())
	if len(matches) != 2 {
		return sdkmath.ZeroUint(), false
	}
	return sdkmath.NewUintFromString(matches[1]), true
}

func retryAmountAtRecommendedMin(currentAmount, minAmount sdkmath.Uint) sdkmath.Uint {
	base := minAmount
	if currentAmount.GT(minAmount) {
		base = currentAmount
	}

	buffer := base.QuoUint64(100)
	if buffer.IsZero() {
		buffer = sdkmath.OneUint()
	}
	return base.Add(buffer)
}

func getSwapQuoteWithRecommendedMin(from, to common.Asset, amount, maxAmount sdkmath.Uint, destination, refundAddress common.Address, extended bool) (openapi.QuoteSwapResponse, sdkmath.Uint, error) {
	currentAmount := amount
	for attempt := 0; attempt < swapQuoteRecommendedMinAttempts(from, to); attempt++ {
		quote, err := thornode.GetSwapQuote(from, to, currentAmount, destination, refundAddress, extended)
		if err != nil {
			minAmount, ok := getRecommendedMinAmountInFromError(err)
			if ok {
				nextAmount := retryAmountAtRecommendedMin(currentAmount, minAmount)
				if !maxAmount.IsZero() && nextAmount.GT(maxAmount) {
					return openapi.QuoteSwapResponse{}, nextAmount, fmt.Errorf("recommended minimum swap amount exceeds spendable balance")
				}
				currentAmount = nextAmount
				continue
			}
			return openapi.QuoteSwapResponse{}, currentAmount, err
		}

		minAmount, ok := getRecommendedMinAmountIn(quote)
		if ok && currentAmount.LT(minAmount) {
			if !maxAmount.IsZero() && minAmount.GT(maxAmount) {
				return openapi.QuoteSwapResponse{}, minAmount, fmt.Errorf("recommended minimum swap amount exceeds spendable balance")
			}
			currentAmount = minAmount
			continue
		}

		return quote, currentAmount, nil
	}

	return openapi.QuoteSwapResponse{}, currentAmount, fmt.Errorf("failed to settle swap quote amount within recommended minimum")
}

func swapSpendableBalance(asset common.Asset, balance sdkmath.Uint) sdkmath.Uint {
	if balance.IsZero() {
		return sdkmath.ZeroUint()
	}

	if !asset.IsGasAsset() {
		return balance
	}

	// Preserve enough of the gas asset balance to avoid draining the account below
	// chain dust/reserve requirements while still allowing late-stage swaps to use
	// whatever balance remains.
	reserve := asset.Chain.DustThreshold()
	if asset.Chain.IsUTXO() {
		if !balance.GT(reserve) {
			return sdkmath.ZeroUint()
		}
		return balance.Sub(reserve)
	}

	fivePercent := balance.QuoUint64(20)
	if fivePercent.GT(reserve) {
		reserve = fivePercent
	}
	if !balance.GT(reserve) {
		return sdkmath.ZeroUint()
	}

	return balance.Sub(reserve)
}

func swapAmountForBalance(asset common.Asset, target, balance sdkmath.Uint) sdkmath.Uint {
	spendable := swapSpendableBalance(asset, balance)
	if spendable.IsZero() {
		return sdkmath.ZeroUint()
	}
	if spendable.LT(target) {
		return spendable
	}
	return target
}

func accountAssetAmount(account *common.Account, asset common.Asset) sdkmath.Uint {
	if account == nil {
		return sdkmath.ZeroUint()
	}

	coin := account.Coins.GetCoin(asset)
	if coin.Amount.IsNil() {
		return sdkmath.ZeroUint()
	}

	return coin.Amount
}

func swapQuoteExpectedRange(quote openapi.QuoteSwapResponse) (sdkmath.Uint, sdkmath.Uint, sdkmath.Uint) {
	// store expected range to fail if received amount is outside 10% tolerance
	quoteOut := sdkmath.NewUintFromString(quote.ExpectedAmountOut)
	tolerance := quoteOut.QuoUint64(10)
	if quote.Fees.Outbound != nil {
		outboundFee := sdkmath.NewUintFromString(*quote.Fees.Outbound)
		quoteOut = quoteOut.Add(outboundFee)

		// handle 3x gas rate fluctuation (add 2x outbound fee to tolerance)
		tolerance = tolerance.Add(outboundFee.MulUint64(2))
	}
	return quoteOut, common.SafeSub(quoteOut, tolerance), quoteOut.Add(tolerance)
}

func swapQuoteDriftRange(from, to common.Asset, originalMin, originalMax, currentMin, currentMax sdkmath.Uint) (sdkmath.Uint, sdkmath.Uint) {
	// Parallel simulation actors can move pools between outbound execution and
	// the refreshed quote, so leave a narrow margin around the current quote.
	buffer := currentMax.QuoUint64(100)
	if buffer.IsZero() {
		buffer = sdkmath.OneUint()
	}

	// XMR inbounds/outbounds have materially longer finality in the simulator.
	// The refreshed quote is taken after the finalized swap may already have
	// moved pools, so accept the full pre/post quote envelope for XMR paths.
	if !from.Chain.Equals(common.XMRChain) && !to.Chain.Equals(common.XMRChain) {
		return common.SafeSub(currentMin, buffer), currentMax.Add(buffer)
	}

	minExpected := originalMin
	if currentMin.LT(minExpected) {
		minExpected = currentMin
	}
	maxExpected := originalMax
	if currentMax.GT(maxExpected) {
		maxExpected = currentMax
	}
	return minExpected, maxExpected
}

func setQuoteExpectations(a *SwapActor, quote openapi.QuoteSwapResponse) {
	a.requiredOutboundSpend, a.minExpected, a.maxExpected = swapQuoteExpectedRange(quote)
}

func outboundAmountPlusGas(out openapi.Tx, action openapi.TxOutItem, to common.Asset) (sdkmath.Uint, error) {
	outAmountPlusGas := cosmos.NewUintFromString(out.Coins[0].Amount)

	gasCoins := out.Gas
	if len(gasCoins) == 0 {
		gasCoins = action.MaxGas
	}

	for _, gasCoin := range gasCoins {
		if gasCoin.Asset != to.String() {
			continue
		}
		outAmountPlusGas = outAmountPlusGas.Add(cosmos.NewUintFromString(gasCoin.Amount))
	}

	return outAmountPlusGas, nil
}

func (a *SwapActor) updateSwapAmount(next sdkmath.Uint, reason string) error {
	if next.GT(a.fromBalance) {
		return fmt.Errorf("%s %s exceeds user balance %s", reason, next, a.fromBalance)
	}

	if !next.Equal(a.swapAmount) {
		a.Log().Info().
			Stringer("before", a.swapAmount).
			Stringer("after", next).
			Str("reason", reason).
			Msg("adjusted swap amount")
	}

	a.swapAmount = next
	return nil
}

func (a *SwapActor) releaseUserSelection() {
	if a.user != nil {
		a.user.Release()
	}
	a.user = nil
	a.fromAddress = common.NoAddress
	a.fromBalance = sdkmath.ZeroUint()
	a.toAddress = common.NoAddress
	a.toBalance = sdkmath.ZeroUint()
	a.swapTxID = ""
	a.requiredOutboundSpend = sdkmath.ZeroUint()
	a.minExpected = sdkmath.ZeroUint()
	a.maxExpected = sdkmath.ZeroUint()
}

func (a *SwapActor) acquireXMRSwapSlot(config *OpConfig) OpResult {
	if (!a.from.Chain.Equals(common.XMRChain) && !a.to.Chain.Equals(common.XMRChain)) || a.xmrSwapSlotAcquired {
		return OpResult{Continue: true}
	}

	select {
	case xmrSwapSlot <- struct{}{}:
		a.xmrSwapSlotAcquired = true
		a.Log().Info().Msg("acquired XMR swap slot")
		return OpResult{Continue: true}
	default:
		a.Log().Info().Msg("waiting for XMR swap slot")
		return OpResult{Continue: false}
	}
}

func (a *SwapActor) releaseXMRSwapSlot() {
	if !a.xmrSwapSlotAcquired {
		return
	}

	<-xmrSwapSlot
	a.xmrSwapSlotAcquired = false
	a.Log().Info().Msg("released XMR swap slot")
}

// isPostSelectionFundingError detects balance failures that can happen after a
// simulation user is selected but before the swap is submitted.
func isPostSelectionFundingError(err error) bool {
	if errors.Is(err, ErrSimTxUncertain) {
		return false
	}
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "insufficient balance") ||
		strings.Contains(msg, "insufficient funds")
}

// finishAfterPostSelectionFundingError releases any acquired simulation slots
// and ends this actor when its selected user can no longer fund the swap.
func (a *SwapActor) finishAfterPostSelectionFundingError(err error) OpResult {
	a.releaseUserSelection()
	a.releaseXMRSwapSlot()
	if a.requireOutbound {
		return OpResult{Finish: true, Error: fmt.Errorf("required swap cannot be funded: %w", err)}
	}
	a.Log().Warn().Err(err).Msg("selected user can no longer fund swap; skipping actor")
	return OpResult{Finish: true}
}

func (a *SwapActor) userSpendableBalance(user *User) (sdkmath.Uint, error) {
	client, ok := user.ChainClients[a.from.Chain]
	if !ok {
		return sdkmath.ZeroUint(), fmt.Errorf("missing chain client for %s", a.from.Chain)
	}

	fromAcct, err := client.GetAccount(nil)
	if err != nil {
		return sdkmath.ZeroUint(), err
	}
	fromBalance := accountAssetAmount(fromAcct, a.from)
	return swapSpendableBalance(a.from, fromBalance), nil
}

func (a *SwapActor) acquireUserForAmount(config *OpConfig, targetAmount sdkmath.Uint, allowPartial bool) OpResult {
	a.swapAmount = targetAmount
	a.fromBalance = sdkmath.ZeroUint()
	a.toBalance = sdkmath.ZeroUint()

	var selectedUser *User
	selectedAvailableBalance := sdkmath.ZeroUint()
	selectedSpendableBalance := sdkmath.ZeroUint()
	selectedSwapAmount := sdkmath.ZeroUint()
	lockedUsers := 0
	availableUsers := 0
	insufficientBalanceUsers := 0
	accountErrorUsers := 0
	strictReadableUsers := 0
	strictInsufficientUsers := 0

	for _, user := range config.Users {
		// skip users already being used
		if !user.Acquire() {
			lockedUsers++
			if !allowPartial {
				spendableBalance, err := a.userSpendableBalance(user)
				if err != nil {
					a.Log().Error().Err(err).Msg("failed to get locked user from account")
					accountErrorUsers++
					continue
				}
				strictReadableUsers++
				if spendableBalance.LT(targetAmount) {
					strictInsufficientUsers++
				}
			}
			continue
		}
		availableUsers++

		cl := a.Log().With().
			Str("user", user.Name()).
			Stringer("from", a.from).
			Stringer("to", a.to).
			Logger()
		a.SetLogger(cl)

		// skip users that don't have from asset balance
		fromAcct, err := user.ChainClients[a.from.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get from account")
			accountErrorUsers++
			user.Release()
			continue
		}
		fromBalance := accountAssetAmount(fromAcct, a.from)
		spendableBalance := swapSpendableBalance(a.from, fromBalance)
		candidateAmount := swapAmountForBalance(a.from, targetAmount, fromBalance)
		if !allowPartial {
			strictReadableUsers++
			if spendableBalance.LT(targetAmount) {
				strictInsufficientUsers++
			}
		}
		if candidateAmount.IsZero() {
			a.Log().Error().Msg("user has insufficient from balance")
			insufficientBalanceUsers++
			user.Release()
			continue
		}
		if !allowPartial && spendableBalance.LT(targetAmount) {
			a.Log().Info().
				Stringer("required_amount", targetAmount).
				Stringer("available_balance", spendableBalance).
				Msg("user balance below required swap amount")
			insufficientBalanceUsers++
			user.Release()
			continue
		}

		// Prefer the user with the most spendable balance so late-stage swaps can
		// keep progressing even after earlier actors have partially drained assets.
		if selectedUser != nil && !spendableBalance.GT(selectedSpendableBalance) {
			user.Release()
			continue
		}

		if selectedUser != nil {
			selectedUser.Release()
		}
		selectedUser = user
		selectedAvailableBalance = spendableBalance
		selectedSpendableBalance = spendableBalance
		selectedSwapAmount = candidateAmount
	}

	if selectedUser != nil {
		cl := a.Log().With().
			Str("user", selectedUser.Name()).
			Stringer("from", a.from).
			Stringer("to", a.to).
			Logger()
		a.SetLogger(cl)
		a.toBalance = sdkmath.ZeroUint()

		toAcct, err := selectedUser.ChainClients[a.to.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Warn().Err(err).Msg("failed to get to account")
			selectedUser.Release()
			return OpResult{
				Continue: false,
			}
		}

		a.user = selectedUser
		a.fromBalance = selectedAvailableBalance

		// get l1 address to store in state context
		fromAddress, err := selectedUser.PubKey(a.from.Chain).GetAddress(a.from.Chain)
		if err != nil {
			a.Log().Fatal().Err(err).Msg("failed to get L1 address")
		}
		a.fromAddress = fromAddress
		toAddress, err := selectedUser.PubKey(a.to.Chain).GetAddress(a.to.Chain)
		if err != nil {
			a.Log().Fatal().Err(err).Msg("failed to get L1 address")
		}
		a.toAddress = toAddress
		if a.useTexAddress {
			a.toAddress, err = toAddress.ToTexAddress()
			if err != nil {
				a.releaseUserSelection()
				return OpResult{Finish: true, Error: err}
			}
		}

		// get to asset balance for tracking
		a.toBalance = accountAssetAmount(toAcct, a.to)

		if err := a.updateSwapAmount(selectedSwapAmount, "available user balance"); err != nil {
			a.releaseUserSelection()
			return OpResult{
				Error:  err,
				Finish: true,
			}
		}

		a.Log().Info().Msg("acquired user")
		return OpResult{
			Continue: true,
		}
	}

	if !allowPartial && strictReadableUsers > 0 && accountErrorUsers == 0 && strictInsufficientUsers == strictReadableUsers {
		if a.requireOutbound {
			a.releaseUserSelection()
			a.releaseXMRSwapSlot()
			return OpResult{Finish: true, Error: fmt.Errorf("required swap amount %s exceeds every available user balance", targetAmount)}
		}
		a.Log().Info().
			Int("available_users", availableUsers).
			Int("locked_users", lockedUsers).
			Int("strict_readable_users", strictReadableUsers).
			Stringer("target_amount", targetAmount).
			Msg("skipping swap because required amount exceeds every available user balance")
		return OpResult{
			Finish: true,
		}
	}

	// Remain pending if selection may become possible after another actor releases
	// a user or a transient account read recovers.
	a.Log().Info().
		Int("available_users", availableUsers).
		Int("locked_users", lockedUsers).
		Int("account_error_users", accountErrorUsers).
		Stringer("target_amount", targetAmount).
		Bool("allow_partial", allowPartial).
		Msg("waiting for user with sufficient balance")
	return OpResult{
		Continue: false,
	}
}

func (a *SwapActor) getQuoteWithUserRetry(config *OpConfig) (openapi.QuoteSwapResponse, OpResult) {
	for attempt := 0; attempt < 4; attempt++ {
		if a.user == nil {
			result := a.acquireUserForAmount(config, a.swapAmount, false)
			if !result.Continue || result.Finish {
				a.releaseXMRSwapSlot()
				return openapi.QuoteSwapResponse{}, result
			}
		}

		// XMR paths carry high fees and longer finality relative to the small
		// mocknet pools. Hold the slot from quote through finalisation, but not
		// while selecting a source user, so an unsatisfiable quote cannot block
		// all XMR paths.
		if result := a.acquireXMRSwapSlot(config); !result.Continue || result.Finish {
			a.releaseUserSelection()
			return openapi.QuoteSwapResponse{}, result
		}

		quote, quoteAmount, err := getSwapQuoteWithRecommendedMin(a.from, a.to, a.swapAmount, a.fromBalance, a.toAddress, a.fromAddress, a.from.Chain.IsUTXO())
		if err != nil {
			if quoteAmount.GT(a.fromBalance) {
				a.Log().Info().
					Stringer("required_amount", quoteAmount).
					Stringer("user_balance", a.fromBalance).
					Msg("quote minimum exceeds selected user balance, retrying user selection")
				a.releaseUserSelection()
				a.releaseXMRSwapSlot()
				a.swapAmount = quoteAmount
				continue
			}

			if !quoteAmount.Equal(a.swapAmount) {
				if updateErr := a.updateSwapAmount(quoteAmount, "recommended minimum inbound amount"); updateErr != nil {
					a.releaseUserSelection()
					a.releaseXMRSwapSlot()
					return openapi.QuoteSwapResponse{}, OpResult{
						Error:  updateErr,
						Finish: true,
					}
				}
				continue
			}

			a.Log().Error().Err(err).Str("amount", a.swapAmount.String()).Msg("failed to get swap quote")
			a.releaseUserSelection()
			a.releaseXMRSwapSlot()
			return openapi.QuoteSwapResponse{}, OpResult{
				Continue: false,
			}
		}

		if quoteAmount.GT(a.fromBalance) {
			a.Log().Info().
				Stringer("required_amount", quoteAmount).
				Stringer("user_balance", a.fromBalance).
				Msg("quote minimum exceeds selected user balance, retrying user selection")
			a.releaseUserSelection()
			a.releaseXMRSwapSlot()
			a.swapAmount = quoteAmount
			continue
		}

		if err := a.updateSwapAmount(quoteAmount, "recommended minimum inbound amount"); err != nil {
			a.releaseUserSelection()
			a.releaseXMRSwapSlot()
			return openapi.QuoteSwapResponse{}, OpResult{
				Error:  err,
				Finish: true,
			}
		}

		return quote, OpResult{
			Continue: true,
		}
	}

	a.Log().Info().
		Stringer("amount", a.swapAmount).
		Msg("waiting for user that can satisfy the quoted minimum amount")
	a.releaseUserSelection()
	a.releaseXMRSwapSlot()
	return openapi.QuoteSwapResponse{}, OpResult{
		Continue: false,
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *SwapActor) acquireUser(config *OpConfig) OpResult {
	if a.from.Chain.Equals(common.XMRChain) || a.to.Chain.Equals(common.XMRChain) {
		if result := waitForXMRNetworkFee(a.Log(), config); !result.Continue {
			return result
		}
	}
	// swap 0.25% of from pool depth
	pool, err := thornode.GetPool(a.from)
	if err != nil {
		return OpResult{
			Continue: false,
		}
	}
	targetAmount := sdkmath.NewUintFromString(pool.BalanceAsset).QuoUint64(400)
	return a.acquireUserForAmount(config, targetAmount, true)
}

func (a *SwapActor) getQuote(config *OpConfig) OpResult {
	quote, result := a.getQuoteWithUserRetry(config)
	if !result.Continue || result.Finish {
		if result.Finish {
			a.releaseXMRSwapSlot()
		}
		return result
	}
	setQuoteExpectations(a, quote)
	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) sendSwap(config *OpConfig) OpResult {
	if result := a.ensureXMRVaultSpendable(config, a.requiredOutboundSpend); !result.Continue || result.Finish {
		return result
	}

	// get inbound address
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}

	// if on a utxo chain, shorten the to asset to fuzzy match
	to := a.to.String()
	if a.from.Chain.IsUTXO() && !a.to.IsGasAsset() {
		to = strings.Split(to, "-")[0]
	}

	// create tx out
	memo := fmt.Sprintf("=:%s:%s", to, a.toAddress)
	tx := SimTx{
		Chain:     a.from.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.from, a.swapAmount),
		Memo:      memo,
	}

	client := a.user.ChainClients[a.from.Chain]

	txid, err := SignAndBroadcastTx(client, tx)
	if err != nil {
		if isPostSelectionFundingError(err) {
			return a.finishAfterPostSelectionFundingError(err)
		}
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return SimTxErrorResult(err)
	}
	a.swapTxID = txid

	a.Log().Info().Str("txid", txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) sendTokenSwap(config *OpConfig) OpResult {
	if result := a.ensureXMRVaultSpendable(config, a.requiredOutboundSpend); !result.Continue || result.Finish {
		return result
	}

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

	token := evm.Tokens(a.from.Chain)[a.from]

	// convert amount to token decimals
	factor := big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil)
	tokenAmount := a.swapAmount.Mul(cosmos.NewUintFromBigInt(factor))
	tokenAmount = tokenAmount.QuoUint64(common.One)

	// approve the router
	eRouterAddr := ecommon.HexToAddress(routerAddr.String())
	tx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: common.Address(token.Address),
		ABI:      evm.ERC20ABI(),
		Method:   "approve",
		Args:     []interface{}{eRouterAddr, tokenAmount.BigInt()},
	}

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Fatal().Msg("failed to get evm client")
	}

	// sign approve transaction
	signed, err := client.SignContractTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// broadcast approve transaction
	txid, err := client.BroadcastTx(signed)
	if err != nil {
		if isPostSelectionFundingError(err) {
			return a.finishAfterPostSelectionFundingError(err)
		}
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}
	a.Log().Info().Str("txid", txid).Msg("broadcasted router approve tx")

	// call depositWithExpiry
	memo := fmt.Sprintf("=:%s:%s", a.to, a.toAddress)
	expiry := time.Now().Add(time.Hour).Unix()
	eInboundAddr := ecommon.HexToAddress(inboundAddr.String())
	eTokenAddr := ecommon.HexToAddress(token.Address)
	tx = SimContractTx{
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

	// sign deposit transaction
	signed, err = client.SignContractTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// broadcast deposit transaction
	txid, err = client.BroadcastTx(signed)
	if err != nil {
		if isPostSelectionFundingError(err) {
			return a.finishAfterPostSelectionFundingError(err)
		}
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.swapTxID = txid

	a.Log().Info().Str("txid", txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) verifyOutbound(config *OpConfig) OpResult {
	getStages, getDetails := a.getSwapStages, a.getSwapDetails
	if getStages == nil {
		getStages = thornode.GetTxStages
	}
	if getDetails == nil {
		getDetails = thornode.GetTxDetails
	}

	// get swap stages
	stages, err := getStages(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx stages")
		return OpResult{
			Continue: false,
		}
	}

	// wait for outbound to be marked complete
	if stages.SwapFinalised != nil && stages.SwapFinalised.Completed {
		a.releaseXMRSwapSlot()
	}
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{
			Continue: false,
		}
	}

	// get tx details
	details, err := getDetails(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{
			Continue: false,
		}
	}

	// verify exactly one out transaction
	if len(details.OutTxs) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one out transaction"),
			Finish: true,
		}
	}

	// verify exactly one action
	if len(details.Actions) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one action"),
			Finish: true,
		}
	}

	// verify action is not refund
	// For memoless outbounds, Memo may be nil but OriginalMemo contains the actual memo
	actionMemo := ""
	if details.Actions[0].Memo != nil {
		actionMemo = *details.Actions[0].Memo
	} else if details.Actions[0].OriginalMemo != nil {
		actionMemo = *details.Actions[0].OriginalMemo
	}
	if strings.HasPrefix(actionMemo, "REFUND:") {
		return OpResult{
			Error:  fmt.Errorf("expected action to not be a refund"),
			Finish: true,
		}
	}

	// verify outbound amount + max gas within expected range
	action := details.Actions[0]
	out := details.OutTxs[0]
	if a.to.Chain.Equals(common.ZECChain) {
		// Both the action and observation must use the wallet's Base58 address.
		address, addressErr := a.user.PubKey(common.ZECChain).GetAddress(common.ZECChain)
		if addressErr != nil {
			err = addressErr
		} else if action.Chain != common.ZECChain.String() || out.GetChain() != common.ZECChain.String() ||
			action.ToAddress != address.String() || out.GetToAddress() != address.String() {
			err = fmt.Errorf("expected ZEC action and outbound to use transparent address %s, got %s and %s", address, action.ToAddress, out.GetToAddress())
		}
	}
	if !a.expectedOutboundVault.IsEmpty() {
		vaultPubKey := action.GetVaultPubKey()
		if a.to.Chain.Equals(common.XMRChain) {
			vaultPubKey = action.GetVaultPubKeyFrost()
		}
		if vaultPubKey == "" {
			err = fmt.Errorf("expected outbound vault %s, got empty vault pubkey", a.expectedOutboundVault)
		} else if vaultPubKey != a.expectedOutboundVault.String() {
			err = fmt.Errorf("expected outbound vault %s, got %s", a.expectedOutboundVault, vaultPubKey)
		}
	}
	outAmountPlusGas, convertErr := outboundAmountPlusGas(out, action, a.to)
	if convertErr != nil {
		a.Log().Warn().Err(convertErr).Msg("failed to convert asset")
		return OpResult{
			Continue: false,
		}
	}

	// retrieve L1 balance
	toAcct, accountErr := a.user.ChainClients[a.to.Chain].GetAccount(nil)
	if accountErr != nil {
		a.Log().Warn().Err(accountErr).Msg("failed to get to account")
		return OpResult{
			Continue: false,
		}
	}

	// check received amount
	currentToBalance := accountAssetAmount(toAcct, a.to)
	received := common.SafeSub(currentToBalance, a.toBalance)
	a.Log().Info().
		Stringer("received", received).
		Stringer("currentToBalance", currentToBalance).
		Stringer("outAmountPlusGas", outAmountPlusGas).
		Stringer("minExpected", a.minExpected).
		Stringer("maxExpected", a.maxExpected).
		Msg("swap complete")

	// fail if received amount is outside expected range
	if outAmountPlusGas.LT(a.minExpected) || outAmountPlusGas.GT(a.maxExpected) {
		currentQuote, quoteErr := thornode.GetSwapQuote(a.from, a.to, a.swapAmount, a.toAddress, a.fromAddress, a.from.Chain.IsUTXO())
		if quoteErr != nil {
			a.Log().Warn().Err(quoteErr).Msg("failed to refresh quote after original expectation drifted")
			err = fmt.Errorf("out amount plus gas outside tolerance")
		} else {
			_, currentMinExpected, currentMaxExpected := swapQuoteExpectedRange(currentQuote)
			minExpected, maxExpected := swapQuoteDriftRange(a.from, a.to, a.minExpected, a.maxExpected, currentMinExpected, currentMaxExpected)
			a.Log().Info().
				Stringer("currentMinExpected", currentMinExpected).
				Stringer("currentMaxExpected", currentMaxExpected).
				Stringer("driftMinExpected", minExpected).
				Stringer("driftMaxExpected", maxExpected).
				Msg("refreshed quote after original expectation drifted")
			if outAmountPlusGas.LT(minExpected) || outAmountPlusGas.GT(maxExpected) {
				err = fmt.Errorf("out amount plus gas outside tolerance")
			}
		}
	}

	// release user
	a.releaseXMRSwapSlot()
	a.user.Release()

	return OpResult{
		Finish: true,
		Error:  err,
	}
}
