package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"cosmossdk.io/log"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"gitlab.com/thorchain/thornode/v3/app/upgrades"
	"gitlab.com/thorchain/thornode/v3/app/upgrades/standard"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const replayChainID = "determinism-fixture"

var replayTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

func replayNodeAddress() cosmos.AccAddress { return cosmos.AccAddress(bytes.Repeat([]byte{2}, 20)) }

// These synthetic records contain no live network addresses or private keys. They
// exercise THORChain's real genesis import, keeper writes, export, and app commit.
func replayGenesis(t *testing.T, app *THORChainApp, seed int64, reverse bool) GenesisState {
	t.Helper()
	state := app.DefaultGenesis()
	var tc thorchain.GenesisState
	require.NoError(t, json.Unmarshal(state[thorchain.ModuleName], &tc))
	// Four pools rather than two: Go's iteration over a small map is skewed
	// toward one order, and more entries make a map-ordered write diverge in
	// more replays.
	assets := []common.Asset{common.BTCAsset, common.ETHAsset, common.DOGEAsset, common.LTCAsset}
	if reverse {
		slices.Reverse(assets)
	}
	for _, asset := range assets {
		pool := thorchain.NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(100000000000 + seed))
		pool.BalanceAsset = cosmos.NewUint(10000000000)
		pool.LPUnits = cosmos.NewUint(10000000000)
		tc.Pools = append(tc.Pools, pool)
	}
	tc.Reserve = 1000000000
	consensusKey := ed25519.GenPrivKeyFromSecret([]byte("determinism fixture consensus key")).PubKey()
	consensusBech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, consensusKey)
	require.NoError(t, err)
	vaultBech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub,
		secp256k1.GenPrivKeyFromSecret([]byte("determinism fixture vault key")).PubKey())
	require.NoError(t, err)
	vaultKey := common.PubKey(vaultBech32)
	tc.NodeAccounts = append(tc.NodeAccounts, thorchain.NewNodeAccount(
		replayNodeAddress(), thorchain.NodeActive, common.PubKeySet{Secp256k1: vaultKey}, consensusBech32,
		cosmos.NewUint(100000000000000), common.Address(replayNodeAddress().String()), 1,
	))
	tc.NodeAccounts[0].Version = constants.SWVersion.String()
	for i := 1; i <= 2; i++ {
		txID, err := common.NewTxID(fmt.Sprintf("%064X", i))
		require.NoError(t, err)
		inbound := common.Tx{
			ID: txID, Chain: common.BTCChain,
			FromAddress: common.Address("bc1qfixturefrom"), ToAddress: common.Address("bc1qfixturevault"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000))},
			Gas:   common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
			Memo:  "SWAP:ETH.ETH",
		}
		observed := common.NewObservedTx(inbound, 5, vaultKey, 5)
		tc.ObservedTxInVoters = append(tc.ObservedTxInVoters, thorchain.NewObservedTxVoter(txID, []common.ObservedTx{observed}))
	}
	tc.TxOuts = []thorchain.TxOut{{Height: 10, TxArray: []thorchain.TxOutItem{{
		Chain: common.BTCChain, ToAddress: common.Address("bc1qfixturedestination"),
		VaultPubKey: vaultKey, InHash: tc.ObservedTxInVoters[0].TxID,
		Coin:   common.NewCoin(common.BTCAsset, cosmos.NewUint(10000000)),
		MaxGas: common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))}, GasRate: 10,
	}}}}
	swap := thorchain.NewMsgSwap(
		tc.ObservedTxInVoters[0].Txs[0].Tx, common.ETHAsset, common.NoopAddress,
		cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil,
		thorchain.MarketSwap, 1, 0, thortypes.SwapVersion_v2,
		cosmos.AccAddress(bytes.Repeat([]byte{1}, 20)),
	)
	require.NoError(t, swap.ValidateBasic())
	tc.SwapQueueItems = []thorchain.MsgSwap{*swap}
	tc.NetworkFees = []thorchain.NetworkFee{
		thorchain.NewNetworkFee(common.BTCChain, 250, 10),
		thorchain.NewNetworkFee(common.ETHChain, 21000, 20),
	}
	// The module's InitGenesis decodes with its legacy amino codec, including
	// numeric NodeType values. Match the format it also uses for ExportGenesis.
	state[thorchain.ModuleName] = thorchain.ModuleCdc.MustMarshalJSON(&tc)
	return state
}

func replayApp(t *testing.T, db dbm.DB) *THORChainApp {
	t.Helper()
	return NewChainApp(log.NewNopLogger(), db, nil, true,
		NewTestAppOptionsWithFlagHome(t.TempDir()), nil, baseapp.SetChainID(replayChainID))
}

func initReplayGenesis(t *testing.T, app *THORChainApp, genesis GenesisState) {
	t.Helper()
	raw, err := json.Marshal(genesis)
	require.NoError(t, err)
	_, err = app.InitChain(&abci.RequestInitChain{ChainId: replayChainID, Time: replayTime, AppStateBytes: raw})
	require.NoError(t, err)
}

func commitReplayGenesis(t *testing.T, app *THORChainApp, genesis GenesisState) []byte {
	t.Helper()
	initReplayGenesis(t, app, genesis)
	_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: 1, Time: replayTime.Add(time.Second),
		Hash: bytes.Repeat([]byte{1}, 32), NextValidatorsHash: bytes.Repeat([]byte{2}, 32),
	})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	hash := bytes.Clone(app.LastCommitID().Hash)
	require.NotEmpty(t, hash)
	return hash
}

func compareReplayHash(expected, actual []byte) error {
	if !bytes.Equal(expected, actual) {
		return fmt.Errorf("committed app hash diverged: expected %X, got %X", expected, actual)
	}
	return nil
}

func replayTHORState(t *testing.T, app *THORChainApp) string {
	t.Helper()
	ctx := app.NewContextLegacy(true, cmtproto.Header{ChainID: replayChainID, Height: app.LastBlockHeight()})
	state, err := app.ModuleManager.ExportGenesisForModules(ctx, app.AppCodec(), []string{thorchain.ModuleName})
	require.NoError(t, err)
	require.NotEmpty(t, state[thorchain.ModuleName])
	return string(state[thorchain.ModuleName])
}

func TestConsensusDeterminismSimulationScope(t *testing.T) {
	app := replayApp(t, dbm.NewMemDB())
	var modules []string
	for name, registered := range app.ModuleManager.Modules {
		if _, ok := registered.(module.AppModuleSimulation); ok {
			modules = append(modules, name)
		}
	}
	sort.Strings(modules)
	require.NotEmpty(t, modules)
	require.NotContains(t, modules, thorchain.ModuleName)
	t.Logf("SDK simulation modules: %v", modules)
}

func TestConsensusDeterminismReplay(t *testing.T) {
	// Go randomizes map iteration per range statement, but a small map takes
	// some orders far more often than others. A local mutation that wrote the
	// last pool of a map iteration to a mimir was missed by 1 in 5 suite runs
	// with two pools and six attempts; four pools and eight attempts keep the
	// expected miss rate for that write below 0.2%.
	const attempts = 8
	for _, seed := range []int64{17, 41} {
		var reference []byte
		var referenceState string
		for attempt := 0; attempt < attempts; attempt++ {
			db := dbm.NewMemDB()
			app := replayApp(t, db)
			hash := commitReplayGenesis(t, app, replayGenesis(t, app, seed, attempt%2 == 1))
			if attempt == 0 {
				reference = hash
				referenceState = replayTHORState(t, app)
			} else {
				require.NoError(t, compareReplayHash(reference, hash), "seed %d attempt %d", seed, attempt)
				require.JSONEq(t, referenceState, replayTHORState(t, app), "seed %d attempt %d", seed, attempt)
			}
		}
	}
}

func TestConsensusDeterminismDetectsDivergence(t *testing.T) {
	dbA, dbB := dbm.NewMemDB(), dbm.NewMemDB()
	appA, appB := replayApp(t, dbA), replayApp(t, dbB)
	hashA := commitReplayGenesis(t, appA, replayGenesis(t, appA, 17, false))
	hashB := commitReplayGenesis(t, appB, replayGenesis(t, appB, 18, false))
	require.ErrorContains(t, compareReplayHash(hashA, hashB), "committed app hash diverged")
}

func TestConsensusDeterminismRestartAndExport(t *testing.T) {
	db := dbm.NewMemDB()
	app := replayApp(t, db)
	hash := commitReplayGenesis(t, app, replayGenesis(t, app, 17, false))
	// FinalizeBlock may spend a small amount from Reserve (e.g. block rewards).
	// The exported state must preserve the committed balance on re-import.
	committedCtx := app.NewContextLegacy(true, cmtproto.Header{ChainID: replayChainID, Height: app.LastBlockHeight()})
	committedReserve := app.ThorchainKeeper.GetRuneBalanceOfModule(committedCtx, thorchain.ReserveName)
	require.False(t, committedReserve.IsZero())
	exported, err := app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var genesis GenesisState
	require.NoError(t, json.Unmarshal(exported.AppState, &genesis))
	var tc thorchain.GenesisState
	require.NoError(t, thorchain.ModuleCdc.UnmarshalJSON(genesis[thorchain.ModuleName], &tc))
	require.Len(t, tc.Pools, 4)
	require.NotEmpty(t, tc.NodeAccounts)
	require.Equal(t, thorchain.NodeActive, tc.NodeAccounts[0].Status)
	require.NotEmpty(t, tc.ObservedTxInVoters)
	require.NotEmpty(t, tc.TxOuts)
	require.Len(t, tc.NetworkFees, 2)

	restarted := replayApp(t, db)
	require.Equal(t, hash, restarted.LastCommitID().Hash)
	reexported, err := restarted.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var after GenesisState
	require.NoError(t, json.Unmarshal(reexported.AppState, &after))
	require.JSONEq(t, string(genesis[thorchain.ModuleName]), string(after[thorchain.ModuleName]))

	committedPool, err := app.ThorchainKeeper.GetPool(committedCtx, common.BTCAsset)
	require.NoError(t, err)
	var continuationHash []byte
	var continuationState string
	for attempt := 0; attempt < 2; attempt++ {
		imported := replayApp(t, dbm.NewMemDB())
		initReplayGenesis(t, imported, genesis)
		ctx := imported.NewContextLegacy(false, cmtproto.Header{ChainID: replayChainID, Height: 1, Time: replayTime})
		importedState, err := imported.ModuleManager.ExportGenesisForModules(ctx, imported.AppCodec(), []string{thorchain.ModuleName})
		require.NoError(t, err)
		require.JSONEq(t, string(genesis[thorchain.ModuleName]), string(importedState[thorchain.ModuleName]))
		require.Equal(t, committedReserve, imported.ThorchainKeeper.GetRuneBalanceOfModule(ctx, thorchain.ReserveName))
		pool, err := imported.ThorchainKeeper.GetPool(ctx, common.BTCAsset)
		require.NoError(t, err)
		require.Equal(t, committedPool, pool)
		_, err = imported.FinalizeBlock(&abci.RequestFinalizeBlock{
			Height: 1, Time: replayTime.Add(time.Second),
			Hash: bytes.Repeat([]byte{1}, 32), NextValidatorsHash: bytes.Repeat([]byte{2}, 32),
		})
		require.NoError(t, err)
		_, err = imported.Commit()
		require.NoError(t, err)
		state := replayTHORState(t, imported)
		if attempt == 0 {
			continuationHash = bytes.Clone(imported.LastCommitID().Hash)
			continuationState = state
		} else {
			require.NoError(t, compareReplayHash(continuationHash, imported.LastCommitID().Hash))
			require.JSONEq(t, continuationState, state)
		}
	}
}

const (
	legacyName        = "fixture"
	legacyNamedAmount = 1234
	legacyPaidAmount  = 5678
)

func legacyNamedOwner() cosmos.AccAddress { return cosmos.AccAddress(bytes.Repeat([]byte{3}, 20)) }
func legacyPayee() cosmos.AccAddress      { return cosmos.AccAddress(bytes.Repeat([]byte{4}, 20)) }

// seedLegacyCollectors writes the pre-19 owner-keyed affiliate collector
// records, in their wire format, for one owner with a THORName and one without,
// and funds the collector module to match. Without legacy records the 18->19
// migration returns before it writes anything.
func seedLegacyCollectors(t *testing.T, app *THORChainApp, ctx cosmos.Context) [][]byte {
	t.Helper()
	k := app.ThorchainKeeper
	tn := thorchain.NewTHORName(legacyName, ctx.BlockHeight()+1000, nil)
	tn.Owner = legacyNamedOwner()
	k.SetTHORName(ctx, tn)
	store := ctx.KVStore(app.GetKey(thortypes.StoreKey))
	var keys [][]byte
	for _, item := range []struct {
		owner  cosmos.AccAddress
		amount uint64
	}{{legacyNamedOwner(), legacyNamedAmount}, {legacyPayee(), legacyPaidAmount}} {
		bz := protowire.AppendTag(nil, 1, protowire.BytesType)
		bz = protowire.AppendBytes(bz, item.owner)
		bz = protowire.AppendTag(bz, 2, protowire.BytesType)
		bz = protowire.AppendString(bz, fmt.Sprint(item.amount))
		key := k.GetKey("affcol/", item.owner.String())
		store.Set(key, bz)
		keys = append(keys, key)
	}
	require.NoError(t, k.SendFromModuleToModule(ctx, thorchain.ReserveName, thorchain.AffiliateCollectorName,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(legacyNamedAmount+legacyPaidAmount)))))
	return keys
}

// Starts the THORChain module at version 18 and runs every registered
// migration up to the current module version. The 18->19 step converts the
// seeded legacy affiliate collectors, the nonzero Reserve and other THORChain
// records must survive the upgrade, and the result must replay identically.
func TestConsensusDeterminismMigration18To19(t *testing.T) {
	var reference []byte
	var referenceState string
	for attempt := 0; attempt < 2; attempt++ {
		app := replayApp(t, dbm.NewMemDB())
		commitReplayGenesis(t, app, replayGenesis(t, app, 17, false))
		height := app.LastBlockHeight() + 1
		header := cmtproto.Header{ChainID: replayChainID, Height: height, Time: replayTime.Add(2 * time.Second)}
		_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
			Height: height, Time: header.Time, Hash: bytes.Repeat([]byte{3}, 32),
			NextValidatorsHash: bytes.Repeat([]byte{2}, 32),
		})
		require.NoError(t, err)
		// FinalizeBlock has already flushed its branched store into the root
		// store, so writes through NewContextLegacy(false, ...) would be dropped by
		// Commit. Migrate on the root store's working state, which Commit persists.
		ctx := app.NewUncachedContext(false, header)
		legacyKeys := seedLegacyCollectors(t, app, ctx)
		payeeBefore := app.ThorchainKeeper.GetBalanceOf(ctx, legacyPayee(), common.RuneAsset()).Amount
		before := app.ThorchainKeeper.GetRuneBalanceOfModule(ctx, thorchain.ReserveName)
		require.False(t, before.IsZero())
		poolBefore, err := app.ThorchainKeeper.GetPool(ctx, common.BTCAsset)
		require.NoError(t, err)
		versions := app.ModuleManager.GetVersionMap()
		versions[thorchain.ModuleName] = 18
		handler := standard.CreateUpgradeHandler(app.ModuleManager, app.configurator, &upgrades.AppKeepers{
			ThorchainKeeper:       app.ThorchainKeeper,
			AccountKeeper:         &app.AccountKeeper,
			ParamsKeeper:          &app.ParamsKeeper,
			ConsensusParamsKeeper: &app.ConsensusParamsKeeper,
			Codec:                 app.AppCodec(),
			GetStoreKey:           app.GetKey,
		})
		updated, err := handler(ctx, upgradetypes.Plan{Name: "3.20.0", Height: ctx.BlockHeight()}, versions)
		require.NoError(t, err)
		require.Equal(t, thorchain.AppModule{}.ConsensusVersion(), updated[thorchain.ModuleName])
		require.NoError(t, app.UpgradeKeeper.SetModuleVersionMap(ctx, updated))
		_, err = app.Commit()
		require.NoError(t, err)

		// Verify the migration from committed state, not the write context.
		committed := app.NewContextLegacy(true, cmtproto.Header{ChainID: replayChainID, Height: app.LastBlockHeight()})
		committedVersions, err := app.UpgradeKeeper.GetModuleVersionMap(committed)
		require.NoError(t, err)
		require.Equal(t, thorchain.AppModule{}.ConsensusVersion(), committedVersions[thorchain.ModuleName])
		node, err := app.ThorchainKeeper.GetNodeAccount(committed, replayNodeAddress())
		require.NoError(t, err)
		require.Equal(t, "3.20.0", node.Version)
		require.Equal(t, before, app.ThorchainKeeper.GetRuneBalanceOfModule(committed, thorchain.ReserveName))
		poolAfter, err := app.ThorchainKeeper.GetPool(committed, common.BTCAsset)
		require.NoError(t, err)
		require.Equal(t, poolBefore, poolAfter)
		// The migration moved both legacy balances: the THORName owner's into
		// the named collector, the owner without a THORName's to its account.
		named, err := app.ThorchainKeeper.GetAffiliateCollector(committed, legacyName)
		require.NoError(t, err)
		require.Equal(t, cosmos.NewUint(legacyNamedAmount), named.RuneAmount)
		require.Equal(t, cosmos.NewUint(legacyNamedAmount),
			app.ThorchainKeeper.GetRuneBalanceOfModule(committed, thorchain.AffiliateCollectorName))
		require.Equal(t, payeeBefore.AddRaw(legacyPaidAmount),
			app.ThorchainKeeper.GetBalanceOf(committed, legacyPayee(), common.RuneAsset()).Amount)
		store := committed.KVStore(app.GetKey(thortypes.StoreKey))
		for _, key := range legacyKeys {
			require.False(t, store.Has(key), "legacy collector key %q survived", key)
		}
		state := replayTHORState(t, app)
		if attempt == 0 {
			reference = bytes.Clone(app.LastCommitID().Hash)
			referenceState = state
		} else {
			require.NoError(t, compareReplayHash(reference, app.LastCommitID().Hash))
			require.JSONEq(t, referenceState, state)
		}
	}
}
