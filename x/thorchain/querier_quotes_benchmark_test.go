package thorchain

import (
	"fmt"
	"os"
	"testing"
	"time"

	sdklog "cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var keyThornodeBench = storetypes.NewKVStoreKey(StoreKey)

// fundModuleBench funds a module for benchmarking (no check.C dependency)
func fundModuleBench(b testing.TB, ctx cosmos.Context, k keeper.Keeper, name string, amt uint64) {
	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(amt))
	err := k.MintToModule(ctx, ModuleName, coin)
	if err != nil {
		b.Fatal(err)
	}
	err = k.SendFromModuleToModule(ctx, ModuleName, name, common.NewCoins(coin))
	if err != nil {
		b.Fatal(err)
	}
}

// setupBenchmarkManager creates a test context and manager for benchmarking
// This is a standalone version of setupManagerForTest that works with testing.B
func setupBenchmarkManager(b testing.TB) (cosmos.Context, *Mgrs) {
	b.Helper()
	return setupBenchmarkManagerWith(b, true)
}

// setupBenchmarkManagerWith is setupBenchmarkManager; bootstrapValidator=false
// leaves out its random active validator, for fixtures that seed their own
// validator set (a validator outside it would be churned out in EndBlock).
func setupBenchmarkManagerWith(b testing.TB, bootstrapValidator bool) (cosmos.Context, *Mgrs) {
	b.Helper()

	// Ensure we're using mocknet and have proper version set
	os.Setenv("NET", "mocknet")
	constants.SWVersion = GetCurrentVersion()

	SetupConfigForTest()
	keyAcc := cosmos.NewKVStoreKey(authtypes.StoreKey)
	keyBank := cosmos.NewKVStoreKey(banktypes.StoreKey)
	keyUpgrade := cosmos.NewKVStoreKey(upgradetypes.StoreKey)
	keyWasm := cosmos.NewKVStoreKey(wasmtypes.StoreKey)

	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, sdklog.NewNopLogger(), storemetrics.NewNoOpMetrics())
	// nil db: rootmulti prefixes its own db per store. Handing every store the
	// same raw memdb makes their IAVL versions collide, so Commit() panics and
	// the fixture can never be measured on a committed tree.
	ms.MountStoreWithDB(keyAcc, cosmos.StoreTypeIAVL, nil)
	ms.MountStoreWithDB(keyThornodeBench, cosmos.StoreTypeIAVL, nil)
	ms.MountStoreWithDB(keyBank, cosmos.StoreTypeIAVL, nil)
	ms.MountStoreWithDB(keyWasm, cosmos.StoreTypeIAVL, nil)

	err := ms.LoadLatestVersion()
	if err != nil {
		b.Fatal(err)
	}

	ctx := cosmos.NewContext(ms, tmproto.Header{ChainID: "thorchain"}, false, logger())
	ctx = ctx.WithBlockHeight(1000000) // Use high block height for benchmarking
	ctx = ctx.WithBlockTime(time.Now())

	encodingConfig := testutil.MakeTestEncodingConfig(
		bank.AppModuleBasic{},
		auth.AppModuleBasic{},
	)

	ak := authkeeper.NewAccountKeeper(
		encodingConfig.Codec,
		runtime.NewKVStoreService(keyAcc),
		authtypes.ProtoBaseAccount,
		map[string][]string{
			types.ModuleName:             {authtypes.Minter, authtypes.Burner},
			types.AsgardName:             {},
			types.BondName:               {},
			types.ReserveName:            {},
			types.LendingName:            {},
			types.AffiliateCollectorName: {},
			types.TreasuryName:           {},
			types.RUNEPoolName:           {},
			types.TCYStakeName:           {},
			types.TCYClaimingName:        {},
			types.POLReserveName:         {},
			types.StableReserveName:      {},
		},
		authcodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		sdk.GetConfig().GetBech32AccountAddrPrefix(),
		authtypes.NewModuleAddress(ModuleName).String(),
	)

	bk := bankkeeper.NewBaseKeeper(
		encodingConfig.Codec,
		runtime.NewKVStoreService(keyBank),
		ak,
		nil,
		authtypes.NewModuleAddress(ModuleName).String(),
		sdklog.NewNopLogger(),
	)

	wasmDir := b.TempDir()
	wk := wasmkeeper.NewKeeper(
		encodingConfig.Codec,
		runtime.NewKVStoreService(keyWasm),
		ak, bk,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, wasmDir,
		wasmtypes.DefaultNodeConfig(), wasmtypes.VMConfig{}, wasmkeeper.BuiltInCapabilities(),
		authtypes.NewModuleAddress(ModuleName).String(),
	)

	err = wk.SetParams(ctx, wasmtypes.Params{
		CodeUploadAccess:             wasmtypes.AllowNobody,
		InstantiateDefaultPermission: wasmtypes.AccessTypeNobody,
	})
	if err != nil {
		b.Fatal(err)
	}

	err = bk.MintCoins(ctx, ModuleName, cosmos.Coins{
		cosmos.NewCoin(common.RuneAsset().Native(), cosmos.NewInt(200_000_000_00000000)),
	})
	if err != nil {
		b.Fatal(err)
	}

	uk := upgradekeeper.NewKeeper(
		nil,
		runtime.NewKVStoreService(keyUpgrade),
		encodingConfig.Codec,
		b.TempDir(),
		nil,
		authtypes.NewModuleAddress(ModuleName).String(),
	)

	k := kv1.NewKeeper(encodingConfig.Codec, keyThornodeBench, bk, ak, uk)

	// Set up a validator node account (required for swap simulations)
	if bootstrapValidator {
		err = k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive))
		if err != nil {
			b.Fatal(err)
		}
	}

	fundModuleBench(b, ctx, k, ModuleName, 10_000*common.One)
	fundModuleBench(b, ctx, k, AsgardName, 100_000_000*common.One)
	fundModuleBench(b, ctx, k, ReserveName, 100_000_000*common.One)

	err = k.SaveNetworkFee(ctx, common.ETHChain, NetworkFee{
		Chain:              common.ETHChain,
		TransactionSize:    1,
		TransactionFeeRate: 375_000, // 375,000 gwei
	})
	if err != nil {
		b.Fatal(err)
	}

	err = k.SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain:              common.BTCChain,
		TransactionSize:    1,
		TransactionFeeRate: 6423600,
	})
	if err != nil {
		b.Fatal(err)
	}

	mgr := NewManagers(k, encodingConfig.Codec, keyThornodeBench, bk, ak, uk, wk)

	err = mgr.LoadManagerIfNecessary(ctx)
	if err != nil {
		b.Fatal(err)
	}
	mgr.gasMgr.BeginBlock()

	return ctx, mgr
}

// setupBenchmarkPools creates realistic pool data for testing
func setupBenchmarkPools(ctx cosmos.Context, mgr *Mgrs) error {
	// Setup realistic pools with significant liquidity
	pools := []struct {
		asset        common.Asset
		runeBalance  uint64
		assetBalance uint64
	}{
		{common.BTCAsset, 100000000, 2000},   // ~1 BTC = 50000 RUNE
		{common.ETHAsset, 50000000, 20000},   // ~1 ETH = 2500 RUNE
		{common.AVAXAsset, 10000000, 250000}, // ~1 AVAX = 40 RUNE
		{common.ATOMAsset, 5000000, 500000},  // ~1 ATOM = 10 RUNE
	}

	for _, p := range pools {
		pool := NewPool()
		pool.Asset = p.asset
		pool.BalanceRune = cosmos.NewUint(p.runeBalance * common.One)
		pool.BalanceAsset = cosmos.NewUint(p.assetBalance * common.One)
		pool.Status = PoolAvailable
		if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
			return err
		}
	}

	// Setup active vault for outbound info
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		common.NewCoin(common.AVAXAsset, cosmos.NewUint(10000*common.One)),
	}
	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		return err
	}

	return nil
}

// BenchmarkQuoteSwapDisabled benchmarks quote API with advanced swap queue disabled (baseline)
func BenchmarkQuoteSwapDisabled(b *testing.B) {
	ctx, mgr := setupBenchmarkManager(b)

	// Disable advanced swap queue (baseline)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	if err := setupBenchmarkPools(ctx, mgr); err != nil {
		b.Fatal(err)
	}

	qs := queryServer{mgr: mgr}

	// Test cases: different swap scenarios
	testCases := []struct {
		name      string
		fromAsset string
		toAsset   string
		amount    string
	}{
		{"BTC_to_RUNE", "BTC.BTC", "THOR.RUNE", "100000000"},        // 1 BTC
		{"RUNE_to_BTC", "THOR.RUNE", "BTC.BTC", "5000000000000"},    // 50000 RUNE
		{"ETH_to_BTC", "ETH.ETH", "BTC.BTC", "1000000000000000000"}, // 1 ETH (double swap)
		{"Small_swap", "BTC.BTC", "THOR.RUNE", "10000000"},          // 0.1 BTC
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			req := &types.QueryQuoteSwapRequest{
				FromAsset: tc.fromAsset,
				ToAsset:   tc.toAsset,
				Amount:    tc.amount,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := qs.queryQuoteSwap(ctx, req)
				if err != nil {
					b.Fatalf("Quote failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkQuoteSwapEnabled benchmarks quote API with advanced swap queue enabled
func BenchmarkQuoteSwapEnabled(b *testing.B) {
	ctx, mgr := setupBenchmarkManager(b)

	// Enable advanced swap queue
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	if err := setupBenchmarkPools(ctx, mgr); err != nil {
		b.Fatal(err)
	}

	qs := queryServer{mgr: mgr}

	// Test cases: different swap scenarios
	testCases := []struct {
		name      string
		fromAsset string
		toAsset   string
		amount    string
	}{
		{"BTC_to_RUNE", "BTC.BTC", "THOR.RUNE", "100000000"},        // 1 BTC
		{"RUNE_to_BTC", "THOR.RUNE", "BTC.BTC", "5000000000000"},    // 50000 RUNE
		{"ETH_to_BTC", "ETH.ETH", "BTC.BTC", "1000000000000000000"}, // 1 ETH (double swap)
		{"Small_swap", "BTC.BTC", "THOR.RUNE", "10000000"},          // 0.1 BTC
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			req := &types.QueryQuoteSwapRequest{
				FromAsset: tc.fromAsset,
				ToAsset:   tc.toAsset,
				Amount:    tc.amount,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := qs.queryQuoteSwap(ctx, req)
				if err != nil {
					b.Fatalf("Quote failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkQuoteSwapEnabledWithQueue benchmarks quote API with advanced swap queue enabled and populated
func BenchmarkQuoteSwapEnabledWithQueue(b *testing.B) {
	ctx, mgr := setupBenchmarkManager(b)

	// Enable advanced swap queue
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	if err := setupBenchmarkPools(ctx, mgr); err != nil {
		b.Fatal(err)
	}

	// Populate the advanced swap queue with some swaps to simulate realistic conditions
	for i := 0; i < 50; i++ {
		swap := MsgSwap{
			Tx: common.Tx{
				ID:          common.TxID(fmt.Sprintf("tx%d", i)),
				Chain:       common.BTCChain,
				FromAddress: GetRandomBTCAddress(),
				ToAddress:   GetRandomTHORAddress(),
				Coins: common.Coins{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(10000000)),
				},
			},
			TargetAsset:             common.RuneAsset(),
			TradeTarget:             cosmos.ZeroUint(),
			Signer:                  GetRandomBech32Addr(),
			AggregatorTargetAddress: "",
		}

		if err := mgr.Keeper().SetAdvSwapQueueItem(ctx, swap); err != nil {
			b.Fatal(err)
		}
	}

	qs := queryServer{mgr: mgr}

	// Test cases: different swap scenarios
	testCases := []struct {
		name      string
		fromAsset string
		toAsset   string
		amount    string
	}{
		{"BTC_to_RUNE", "BTC.BTC", "THOR.RUNE", "100000000"},        // 1 BTC
		{"RUNE_to_BTC", "THOR.RUNE", "BTC.BTC", "5000000000000"},    // 50000 RUNE
		{"ETH_to_BTC", "ETH.ETH", "BTC.BTC", "1000000000000000000"}, // 1 ETH (double swap)
		{"Small_swap", "BTC.BTC", "THOR.RUNE", "10000000"},          // 0.1 BTC
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			req := &types.QueryQuoteSwapRequest{
				FromAsset: tc.fromAsset,
				ToAsset:   tc.toAsset,
				Amount:    tc.amount,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := qs.queryQuoteSwap(ctx, req)
				if err != nil {
					b.Fatalf("Quote failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkQuoteSwapStreaming benchmarks streaming swap quotes
func BenchmarkQuoteSwapStreaming(b *testing.B) {
	// Test both enabled and disabled
	modes := []struct {
		name    string
		enabled int64
	}{
		{"Disabled", 0},
		{"Enabled", 1},
	}

	for _, mode := range modes {
		b.Run(mode.name, func(b *testing.B) {
			ctx, mgr := setupBenchmarkManager(b)
			mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), mode.enabled)

			if err := setupBenchmarkPools(ctx, mgr); err != nil {
				b.Fatal(err)
			}

			qs := queryServer{mgr: mgr}

			req := &types.QueryQuoteSwapRequest{
				FromAsset:         "BTC.BTC",
				ToAsset:           "ETH.ETH",
				Amount:            "500000000", // 5 BTC
				StreamingInterval: "10",
				StreamingQuantity: "10",
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := qs.queryQuoteSwap(ctx, req)
				if err != nil {
					b.Fatalf("Quote failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkQuoteSwapComparison runs side-by-side comparison
func BenchmarkQuoteSwapComparison(b *testing.B) {
	modes := []struct {
		name    string
		enabled int64
	}{
		{"Queue_Disabled", 0},
		{"Queue_Enabled", 1},
	}

	for _, mode := range modes {
		b.Run(mode.name, func(b *testing.B) {
			ctx, mgr := setupBenchmarkManager(b)
			mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), mode.enabled)

			if err := setupBenchmarkPools(ctx, mgr); err != nil {
				b.Fatal(err)
			}

			qs := queryServer{mgr: mgr}

			req := &types.QueryQuoteSwapRequest{
				FromAsset: "BTC.BTC",
				ToAsset:   "THOR.RUNE",
				Amount:    "100000000",
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := qs.queryQuoteSwap(ctx, req)
				if err != nil {
					b.Fatalf("Quote failed: %v", err)
				}
			}
		})
	}
}
