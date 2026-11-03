package core

import (
	"fmt"
	rand "math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// ArbActor
////////////////////////////////////////////////////////////////////////////////////////

type ArbActor struct {
	Actor

	account     *User
	thorAddress cosmos.AccAddress

	// originalPools maps the asset to the first seen available pool state (arb target)
	originalPools map[string]types.Pool
}

func NewArbActor(rng *rand.Rand) *Actor {
	a := &ArbActor{
		Actor:         *NewActor("Arbitrage", rng),
		originalPools: make(map[string]types.Pool),
	}
	a.Timeout = time.Hour
	// Once backgrounded, arbitrage can run alongside the foreground DAG. A slower
	// retry interval keeps it from overwhelming the sim with continuous rebalance
	// deposits while still providing ongoing pool stabilization.
	a.Interval = 5 * time.Second

	// init pool balances
	a.Ops = append(a.Ops, a.init)

	// lock an account to use for arb
	a.Ops = append(a.Ops, a.acquireUser)

	// enable trade assets
	a.Ops = append(a.Ops, a.enableTradeAssets)

	// convert all assets to trade assets
	a.Ops = append(a.Ops, a.bootstrapTradeAssets)

	// arb until pools are drained
	a.Ops = append(a.Ops, a.arb)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *ArbActor) init(config *OpConfig) OpResult {
	// get all pools
	pools, err := thornode.GetPools()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pools")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	for _, pool := range pools {
		a.originalPools[pool.Asset] = types.Pool{
			BalanceRune:  cosmos.NewUintFromString(pool.BalanceRune),
			BalanceAsset: cosmos.NewUintFromString(pool.BalanceAsset),
		}
	}

	return OpResult{
		Continue: true,
	}
}

func (a *ArbActor) acquireUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		// skip users already being used
		if !user.Acquire() {
			continue
		}

		cl := a.Log().With().Str("user", user.Name()).Logger()
		a.SetLogger(cl)

		// set acquired account and amounts in state context
		a.account = user

		// set thorchain address for later use
		thorAddress, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thor address")
			user.Release()
			continue
		}
		a.thorAddress = thorAddress

		break
	}

	// continue if we acquired a user
	if a.account != nil {
		a.Log().Info().Msg("acquired user")
		return OpResult{
			Continue: true,
		}
	}

	// remain pending if no user is available
	a.Log().Info().Msg("waiting for user with sufficient balance")
	return OpResult{
		Continue: false,
	}
}

func (a *ArbActor) enableTradeAssets(config *OpConfig) OpResult {
	node := config.NodeUsers[0]
	// wait to acquire the node user
	if !node.Acquire() {
		return OpResult{
			Continue: false,
		}
	}
	// Release all the node users at the end of the function.
	defer node.Release()

	accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{
			Continue: false,
		}
	}
	mimirMsg := types.NewMsgMimir("TradeAccountsEnabled", 1, accAddr)
	txid, err := node.Thorchain.Broadcast(mimirMsg)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}
	a.Log().Info().
		Stringer("txid", txid).
		Msg("broadcasted admin mimir tx to enable trade assets")

	return OpResult{
		Continue: true,
	}
}

func (a *ArbActor) bootstrapTradeAssets(config *OpConfig) OpResult {
	// get all pools
	pools, err := thornode.GetPools()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pools")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	chainLocks := struct {
		sync.Mutex
		m map[common.Chain]*sync.Mutex
	}{m: make(map[common.Chain]*sync.Mutex)}

	bootstrap := func(pool openapi.Pool) {
		var asset common.Asset
		asset, err = common.NewAsset(pool.Asset)
		if err != nil {
			a.Log().Fatal().Err(err).Str("asset", pool.Asset).Msg("failed to create asset")
		}

		// lock chain
		chainLocks.Lock()
		chainLock, ok := chainLocks.m[asset.Chain]
		if !ok {
			chainLock = &sync.Mutex{}
			chainLocks.m[asset.Chain] = chainLock
		}
		chainLocks.Unlock()
		chainLock.Lock()
		defer chainLock.Unlock()

		// get deposit parameters for 90% of asset balance
		client := a.account.ChainClients[asset.Chain]
		memo := fmt.Sprintf("trade+:%s", a.thorAddress)
		var l1Acct *common.Account
		l1Acct, err = a.account.ChainClients[asset.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Fatal().Err(err).Msg("failed to get L1 account")
		}
		depositAmount := l1Acct.Coins.GetCoin(asset).Amount.QuoUint64(10).MulUint64(9)

		// make deposit
		var txid string
		if asset.Chain.IsEVM() && !asset.IsGasAsset() {
			txid, err = DepositL1Token(a.Log(), client, asset, memo, depositAmount)
		} else {
			txid, err = DepositL1(a.Log(), client, asset, memo, depositAmount)
		}
		if err != nil {
			a.Log().Fatal().
				Err(err).
				Str("asset", asset.String()).
				Msg("failed to deposit trade asset")
		}
		a.Log().Info().
			Stringer("asset", asset).
			Str("txid", txid).
			Msg("deposited trade asset")
	}

	// deposit trade assets for all pools
	wg := sync.WaitGroup{}
	for _, pool := range pools {
		wg.Add(1)
		go func(pool openapi.Pool) {
			defer wg.Done()
			bootstrap(pool)
		}(pool)
	}
	wg.Wait()

	// Once the actor moves to the background, it should stop monopolizing its user.
	// Foreground swaps still need both sim users to remain schedulable.
	a.account.Release()

	// mark actor as backgrounded
	a.Log().Info().Msg("moving arbitrage actor to background")
	a.Background()

	return OpResult{
		Continue: true,
	}
}

func (a *ArbActor) arb(config *OpConfig) OpResult {
	// Hold the arb account only while actively evaluating/broadcasting a trade so
	// foreground actors can reuse the user between background iterations.
	if !a.account.Acquire() {
		a.Log().Info().Msg("arb user busy, waiting to retry")
		return OpResult{
			Continue: false,
		}
	}
	defer a.account.Release()

	// get all pools
	pools, err := thornode.GetPools()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pools")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	// if pools are drained then we are done
	if len(pools) == 0 {
		a.Log().Info().Msg("pools are drained, nothing more to arb")
		return OpResult{
			Finish: true,
			Error:  nil,
		}
	}

	// gather pools we have seen
	arbPools := []openapi.Pool{}
	for _, pool := range pools {
		// skip unavailable pools and those with no liquidity
		if pool.BalanceRune == "0" || pool.BalanceAsset == "0" || pool.Status != types.PoolStatus_Available.String() {
			continue
		}

		arbPools = append(arbPools, pool)
	}

	// skip if there are not enough pools to arb
	if len(arbPools) < 2 {
		a.Log().Info().Msg("not enough pools to arb")
		return OpResult{
			Continue: false,
		}
	}

	// sort pools by price change
	priceChangeBps := func(pool openapi.Pool) int64 {
		originalPool := a.originalPools[pool.Asset]
		originalPrice := originalPool.BalanceRune.MulUint64(1e8).Quo(originalPool.BalanceAsset)
		currentPrice := cosmos.NewUintFromString(pool.BalanceRune).MulUint64(1e8).Quo(cosmos.NewUintFromString(pool.BalanceAsset))
		return int64(currentPrice.MulUint64(constants.MaxBasisPts).Quo(originalPrice).Uint64()) - int64(constants.MaxBasisPts)
	}
	sort.Slice(arbPools, func(i, j int) bool {
		return priceChangeBps(arbPools[i]) > priceChangeBps(arbPools[j])
	})

	send := arbPools[0]
	receive := arbPools[len(arbPools)-1]

	// skip if none have diverged more than 1% basis points
	if priceChangeBps(send)-priceChangeBps(receive) < 100 {
		a.Log().Info().
			Int64("maxShift", priceChangeBps(send)).
			Int64("minShift", priceChangeBps(receive)).
			Msg("pools have not diverged enough to arb")
		return OpResult{
			Continue: false,
		}
	}

	adjustmentBps := int64(50)

	// build the swap
	minRuneDepth := common.Min(cosmos.NewUintFromString(send.BalanceRune).Uint64(), cosmos.NewUintFromString(receive.BalanceRune).Uint64())
	runeValue := cosmos.NewUint(uint64(adjustmentBps)).MulUint64(minRuneDepth).QuoUint64(2).QuoUint64(constants.MaxBasisPts)
	assetAmount := runeValue.Mul(cosmos.NewUintFromString(send.BalanceAsset)).Quo(cosmos.NewUintFromString(send.BalanceRune))
	memo := fmt.Sprintf("=:%s", strings.Replace(receive.Asset, ".", "~", 1))
	asset, err := common.NewAsset(strings.Replace(send.Asset, ".", "~", 1))
	if err != nil {
		a.Log().Fatal().Err(err).Str("asset", send.Asset).Msg("failed to create asset")
	}
	coin := common.NewCoin(asset, assetAmount)

	// build the swap
	deposit := types.NewMsgDeposit(common.NewCoins(coin), memo, a.thorAddress)
	a.Log().Info().Interface("deposit", deposit).Msg("arbing most diverged pool")

	// broadcast the swap
	txid, err := a.account.Thorchain.Broadcast(deposit)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Stringer("txid", txid).Str("memo", memo).Msg("broadcasted arb tx")

	return OpResult{
		Continue: false,
	}
}
