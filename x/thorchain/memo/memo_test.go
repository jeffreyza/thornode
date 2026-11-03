package thorchain

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"
	upgradetypes "cosmossdk.io/x/upgrade/types"
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
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemoSuite struct {
	ctx sdk.Context
	k   keeper.Keeper
}

func TestPackage(t *testing.T) { TestingT(t) }

var _ = Suite(&MemoSuite{})

func (s *MemoSuite) SetUpSuite(c *C) {
	types.SetupConfigForTest()
	keyAcc := cosmos.NewKVStoreKey(authtypes.StoreKey)
	keyBank := cosmos.NewKVStoreKey(banktypes.StoreKey)
	keyThorchain := cosmos.NewKVStoreKey(types.StoreKey)
	keyUpgrade := cosmos.NewKVStoreKey(upgradetypes.StoreKey)

	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(keyAcc, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(keyThorchain, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(keyBank, cosmos.StoreTypeIAVL, db)
	err := ms.LoadLatestVersion()
	c.Assert(err, IsNil)

	ctx := cosmos.NewContext(ms, tmproto.Header{ChainID: "thorchain"}, false, log.NewNopLogger())
	s.ctx = ctx.WithBlockHeight(18)

	encodingConfig := testutil.MakeTestEncodingConfig(
		bank.AppModuleBasic{},
		auth.AppModuleBasic{},
	)

	ak := authkeeper.NewAccountKeeper(
		encodingConfig.Codec,
		runtime.NewKVStoreService(keyAcc),
		authtypes.ProtoBaseAccount,
		map[string][]string{
			types.ModuleName:  {authtypes.Minter, authtypes.Burner},
			types.AsgardName:  {},
			types.BondName:    {},
			types.ReserveName: {},
			types.LendingName: {},
		},
		authcodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		sdk.GetConfig().GetBech32AccountAddrPrefix(),
		authtypes.NewModuleAddress(types.ModuleName).String(),
	)

	bk := bankkeeper.NewBaseKeeper(
		encodingConfig.Codec,
		runtime.NewKVStoreService(keyBank),
		ak,
		nil,
		authtypes.NewModuleAddress(types.ModuleName).String(),
		log.NewNopLogger(),
	)
	c.Assert(bk.MintCoins(ctx, types.ModuleName, cosmos.Coins{
		cosmos.NewCoin(common.RuneAsset().Native(), cosmos.NewInt(200_000_000_00000000)),
	}), IsNil)
	uk := upgradekeeper.NewKeeper(
		nil,
		runtime.NewKVStoreService(keyUpgrade),
		encodingConfig.Codec,
		c.MkDir(),
		nil,
		authtypes.NewModuleAddress(types.ModuleName).String(),
	)
	s.k = kv1.NewKVStore(encodingConfig.Codec, keyThorchain, bk, ak, uk, types.GetCurrentVersion())
}

func (s *MemoSuite) TestTxType(c *C) {
	for _, trans := range []TxType{TxAdd, TxWithdraw, TxSwap, TxOutbound, TxDonate, TxBond, TxUnbond, TxLeave} {
		tx, err := StringToTxType(trans.String())
		c.Assert(err, IsNil)
		c.Check(tx, Equals, trans)
		c.Check(tx.IsEmpty(), Equals, false)
	}
}

func (s *MemoSuite) TestParseWithAbbreviated(c *C) {
	ctx := s.ctx
	k := s.k

	// happy paths
	memo, err := ParseMemoWithTHORNames(ctx, k, "d:"+common.RuneAsset().String())
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxDonate), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	memo, err = ParseMemoWithTHORNames(ctx, k, "+:"+common.RuneAsset().String())
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxAdd), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	_, err = ParseMemoWithTHORNames(ctx, k, "add:BTC.BTC:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:xxxx")
	c.Assert(err, NotNil)

	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("-:%s:25", common.RuneAsset().String()))
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxWithdraw), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetAmount().Uint64(), Equals, uint64(25), Commentf("%d", memo.GetAmount().Uint64()))
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	memo, err = ParseMemoWithTHORNames(ctx, k, "=:r:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:87e7")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Equal(cosmos.NewUint(870000000)), Equals, true)
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)
	c.Check(memo.GetAsset().String(), Equals, "THOR.RUNE")

	// custom refund address
	refundAddr := types.GetRandomTHORAddress()
	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:b:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a/%s:87e7", refundAddr.String()))
	c.Assert(err, IsNil)
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetRefundAddress().String(), Equals, refundAddr.String())

	// if refund address is present, but destination is not, should return an err
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:b:/%s:87e7", refundAddr.String()))
	c.Assert(err, NotNil)

	// test multiple affiliates
	affiliate1 := types.GetRandomTHORAddress()
	affiliate2 := types.GetRandomTHORAddress()
	affiliate3 := types.GetRandomTHORAddress()
	ms := fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s/%s:10/20/30",
		affiliate1.String(), affiliate2.String(), affiliate3.String())
	memo, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, IsNil)
	c.Check(len(memo.GetAffiliates()), Equals, 3)
	c.Check(len(memo.GetAffiliatesBasisPoints()), Equals, 3)
	c.Check(memo.GetAffiliates()[0], Equals, affiliate1.String())
	c.Check(memo.GetAffiliatesBasisPoints()[0].Uint64(), Equals, uint64(10))
	c.Check(memo.GetAffiliates()[1], Equals, affiliate2.String())
	c.Check(memo.GetAffiliatesBasisPoints()[1].Uint64(), Equals, uint64(20))
	c.Check(memo.GetAffiliates()[2], Equals, affiliate3.String())
	c.Check(memo.GetAffiliatesBasisPoints()[2].Uint64(), Equals, uint64(30))

	// thornames + rune addrs
	affRune1 := types.GetRandomTHORAddress()
	affRune2 := types.GetRandomTHORAddress()
	affRune3 := types.GetRandomTHORAddress()
	ms = fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s/%s:10/20/30",
		affRune1.String(), affRune2.String(), affRune3.String())
	memo, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, IsNil)
	c.Check(memo.GetAffiliatesBasisPoints()[0].Uint64(), Equals, uint64(10))
	c.Check(memo.GetAffiliates()[1], Equals, affRune2.String())
	c.Check(memo.GetAffiliatesBasisPoints()[1].Uint64(), Equals, uint64(20))
	c.Check(memo.GetAffiliates()[2], Equals, affRune3.String())
	c.Check(memo.GetAffiliatesBasisPoints()[2].Uint64(), Equals, uint64(30))

	// one affiliate bps defined, should apply to all affiliates
	affTest1 := types.GetRandomTHORAddress()
	affTest2 := types.GetRandomTHORAddress()
	affTest3 := types.GetRandomTHORAddress()
	ms = fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s/%s:10",
		affTest1.String(), affTest2.String(), affTest3.String())
	memo, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, IsNil)
	c.Check(memo.GetAffiliatesBasisPoints()[0].Uint64(), Equals, uint64(10))
	c.Check(memo.GetAffiliatesBasisPoints()[1].Uint64(), Equals, uint64(10))
	c.Check(memo.GetAffiliatesBasisPoints()[2].Uint64(), Equals, uint64(10))

	// affiliates + bps mismatch
	affMismatch1 := types.GetRandomTHORAddress()
	affMismatch2 := types.GetRandomTHORAddress()
	affMismatch3 := types.GetRandomTHORAddress()
	ms = fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s/%s:10/20",
		affMismatch1.String(), affMismatch2.String(), affMismatch3.String())
	_, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "affiliate thornames and affiliate fee bps count mismatch")

	// total affiliate fee too high
	affHigh1 := types.GetRandomTHORAddress()
	affHigh2 := types.GetRandomTHORAddress()
	affHigh3 := types.GetRandomTHORAddress()
	ms = fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s/%s:10000/10000/10000",
		affHigh1.String(), affHigh2.String(), affHigh3.String())
	_, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "total affiliate fee basis points can't be more than 10000")

	// test whitespace trimming in affiliates (edge case)
	ms = "=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0/thor1dheycdevq39qlkxs2a6wuuzyn4aqy7qcw5m9sq/thor1xyerxdp4xcmnswfsxyerxdp4xcmnswfstmd8p6:10/20/30"
	memo, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, IsNil)
	c.Check(len(memo.GetAffiliates()), Equals, 3)
	c.Check(memo.GetAffiliates()[0], Equals, "thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0")
	c.Check(memo.GetAffiliates()[1], Equals, "thor1dheycdevq39qlkxs2a6wuuzyn4aqy7qcw5m9sq")
	c.Check(memo.GetAffiliates()[2], Equals, "thor1xyerxdp4xcmnswfsxyerxdp4xcmnswfstmd8p6")

	// test empty affiliate parts (should be filtered out)
	ms = "=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0/thor1dheycdevq39qlkxs2a6wuuzyn4aqy7qcw5m9sq:10/30"
	memo, err = ParseMemoWithTHORNames(ctx, k, ms)
	c.Assert(err, IsNil)
	c.Check(len(memo.GetAffiliates()), Equals, 2)
	c.Check(memo.GetAffiliates()[0], Equals, "thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0")
	c.Check(memo.GetAffiliates()[1], Equals, "thor1dheycdevq39qlkxs2a6wuuzyn4aqy7qcw5m9sq")
	c.Check(memo.GetAffiliatesBasisPoints()[0].Uint64(), Equals, uint64(10))
	c.Check(memo.GetAffiliatesBasisPoints()[1].Uint64(), Equals, uint64(30))

	// test streaming swap
	memo, err = ParseMemoWithTHORNames(ctx, k, "=:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/10/20")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Equal(cosmos.NewUint(1200)), Equals, true)
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)
	swapMemo, ok := memo.(SwapMemo)
	c.Assert(ok, Equals, true)
	c.Check(swapMemo.GetStreamQuantity(), Equals, uint64(20), Commentf("%d", swapMemo.GetStreamQuantity()))
	c.Check(swapMemo.GetStreamInterval(), Equals, uint64(10))
	c.Check(swapMemo.String(), Equals, "=:THOR.RUNE:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/10/20")

	memo, err = ParseMemoWithTHORNames(ctx, k, "=:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a://")
	c.Assert(err, IsNil)
	c.Check(memo.GetSlipLimit().String(), Equals, "0")
	swapMemo, ok = memo.(SwapMemo)
	c.Assert(ok, Equals, true)
	c.Check(swapMemo.GetStreamQuantity(), Equals, uint64(0))
	c.Check(swapMemo.GetStreamInterval(), Equals, uint64(0))

	// wacky lending tests
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/10/20abc", common.RuneAsset()))
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/10/////", common.RuneAsset()))
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/10/-20", common.RuneAsset()))
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/-10/20", common.RuneAsset()))
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1200/102103980982304982058230492830429384080/20", common.RuneAsset()))
	c.Assert(err, NotNil)

	// a price limit that overflows big.Float to +Inf must be rejected (refund),
	// not silently parsed as a zero limit that disables price protection
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:1e700000000/10/20", common.RuneAsset()))
	c.Assert(err, NotNil)
	// a negative price limit is rejected as a parse error, not via panic recovery
	_, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("=:%s:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:-1/10/20", common.RuneAsset()))
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "panicked"), Equals, false, Commentf("%s", err))

	memo, err = ParseMemoWithTHORNames(ctx, k, "=:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Uint64(), Equals, uint64(0))
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.GetDexAggregator(), Equals, "")

	memo, err = ParseMemoWithTHORNames(ctx, k, "=:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::::123:0x2354234523452345:1234444")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(memo.GetDexAggregator(), Equals, "123")
	c.Check(memo.GetDexTargetAddress(), Equals, "0x2354234523452345")
	c.Check(memo.GetDexTargetLimit().Equal(cosmos.NewUint(1234444)), Equals, true)

	// test dex agg limit with scientific notation - long number
	memo, err = ParseMemoWithTHORNames(ctx, k, "=:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::::123:0x2354234523452345:1425e18")
	c.Assert(err, IsNil)
	c.Check(memo.GetDexTargetLimit().Equal(cosmos.NewUintFromString("1425000000000000000000")), Equals, true) // noting the large number overflows `cosmos.NewUint`

	memo, err = ParseMemoWithTHORNames(ctx, k, "OUT:MUKVQILIHIAUSEOVAXBFEZAJKYHFJYHRUUYGQJZGFYBYVXCXYNEMUOAIQKFQLLCX")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxOutbound), Equals, true, Commentf("%s", memo.GetType()))
	c.Check(memo.IsOutbound(), Equals, true)
	c.Check(memo.IsInbound(), Equals, false)
	c.Check(memo.IsInternal(), Equals, false)

	memo, err = ParseMemoWithTHORNames(ctx, k, "m=<:25BTC.BTC:1003ETH.ETH:900")
	c.Assert(err, IsNil)
	modMemo, ok := memo.(ModifyLimitSwapMemo)
	c.Assert(ok, Equals, true)
	c.Check(modMemo.Source.Equals(common.NewCoin(common.BTCAsset, cosmos.NewUint(25))), Equals, true)
	c.Check(modMemo.Target.Equals(common.NewCoin(common.ETHAsset, cosmos.NewUint(1003))), Equals, true)
	c.Check(modMemo.ModifiedTargetAmount.Uint64(), Equals, uint64(900))

	// Test modify limit swap with trade assets
	memo, err = ParseMemoWithTHORNames(ctx, k, "m=<:10000000BTC~BTC:50000000ETH~ETH:40000000")
	c.Assert(err, IsNil)
	modMemo, ok = memo.(ModifyLimitSwapMemo)
	c.Assert(ok, Equals, true)
	btcTradeAsset, _ := common.NewAsset("BTC~BTC")
	ethTradeAsset, _ := common.NewAsset("ETH~ETH")
	c.Check(modMemo.Source.Equals(common.NewCoin(btcTradeAsset, cosmos.NewUint(10000000))), Equals, true)
	c.Check(modMemo.Target.Equals(common.NewCoin(ethTradeAsset, cosmos.NewUint(50000000))), Equals, true)
	c.Check(modMemo.ModifiedTargetAmount.Uint64(), Equals, uint64(40000000))

	memo, err = ParseMemoWithTHORNames(ctx, k, "REFUND:MUKVQILIHIAUSEOVAXBFEZAJKYHFJYHRUUYGQJZGFYBYVXCXYNEMUOAIQKFQLLCX")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRefund), Equals, true)
	c.Check(memo.IsOutbound(), Equals, true)

	memo, err = ParseMemoWithTHORNames(ctx, k, "leave:whatever")
	c.Assert(err, NotNil)
	c.Check(memo.IsType(TxLeave), Equals, true)

	addr := types.GetRandomBech32Addr()
	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("leave:%s", addr.String()))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxLeave), Equals, true)
	c.Check(memo.GetAccAddress().String(), Equals, addr.String())

	memo, err = ParseMemoWithTHORNames(ctx, k, "migrate:100")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxMigrate), Equals, true)
	c.Check(memo.IsInternal(), Equals, true)

	memo, err = ParseMemoWithTHORNames(ctx, k, "ragnarok:100")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRagnarok), Equals, true)
	c.Check(memo.IsOutbound(), Equals, true)

	memo, err = ParseMemoWithTHORNames(ctx, k, "reserve")
	c.Check(err, IsNil)
	c.Check(memo.IsType(TxReserve), Equals, true)
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	memo, err = ParseMemoWithTHORNames(ctx, k, "noop")
	c.Check(err, IsNil)
	c.Check(memo.IsType(TxNoOp), Equals, true)
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	memo, err = ParseMemoWithTHORNames(ctx, k, "noop:novault")
	c.Check(err, IsNil)
	c.Check(memo.IsType(TxNoOp), Equals, true)
	c.Check(memo.IsInbound(), Equals, true)
	c.Check(memo.IsInternal(), Equals, false)
	c.Check(memo.IsOutbound(), Equals, false)

	// unhappy paths
	_, err = ParseMemoWithTHORNames(ctx, k, "")
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "bogus")
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "CREATE") // missing symbol
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "c:") // bad symbol
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "-:eth") // withdraw basis points is optional
	c.Assert(err, IsNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "-:eth:twenty-two") // bad amount
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "=:eth:bad_DES:5.6") // bad destination
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, ">:eth:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:five") // bad slip limit
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "!:key:val") // not enough arguments
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "!:bogus:key:value") // bogus admin command type
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "nextpool:whatever")
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "migrate")
	c.Assert(err, NotNil)
}

// TestAddLiquidityMemoCompositeAddressParsing keeps keeper-backed and keeperless
// parsing aligned while treating an invalid optional asset address as absent.
func (s *MemoSuite) TestAddLiquidityMemoCompositeAddressParsing(c *C) {
	pairAddr := types.GetRandomTHORAddress()
	assetAddr := "45v8VYPut8S9wY9fTvn1gKiGN5AMMzsCK9kLNcEpEESygy4vU3Jf3uE4Zyqv2bDLwTToH8ArrsZ3W28kmKUKJkjY8fN9anA"
	validMemo := fmt.Sprintf("+:XMR.XMR:%s/%s", pairAddr, assetAddr)
	invalidMemo := fmt.Sprintf("+:XMR.XMR:%s/not-an-address", pairAddr)
	missingPairMemo := fmt.Sprintf("+:XMR.XMR:/%s", assetAddr)
	nonExplicitChainMemo := fmt.Sprintf("+:BTC.BTC:%s/%s", pairAddr, types.GetRandomBTCAddress())

	parsers := []func(string) (Memo, error){
		func(memo string) (Memo, error) {
			return ParseMemo(types.GetCurrentVersion(), memo)
		},
		func(memo string) (Memo, error) {
			return ParseMemoWithTHORNames(s.ctx, s.k, memo)
		},
	}

	for _, parse := range parsers {
		parsed, err := parse(validMemo)
		c.Assert(err, IsNil)
		addMemo, ok := parsed.(AddLiquidityMemo)
		c.Assert(ok, Equals, true)
		c.Check(addMemo.GetDestination(), Equals, pairAddr)
		c.Check(addMemo.GetAssetAddress().String(), Equals, assetAddr)

		parsed, err = parse(invalidMemo)
		c.Assert(err, IsNil)
		addMemo, ok = parsed.(AddLiquidityMemo)
		c.Assert(ok, Equals, true)
		c.Check(addMemo.GetDestination(), Equals, pairAddr)
		c.Check(addMemo.GetAssetAddress().IsEmpty(), Equals, true)

		_, err = parse(missingPairMemo)
		c.Assert(err, NotNil)

		// ADD composite addresses are XMR-scoped. On chains with an observable
		// sender, accepting this field would silently discard the asset address.
		_, err = parse(nonExplicitChainMemo)
		c.Assert(err, NotNil)
	}
}

func (s *MemoSuite) TestParse(c *C) {
	ctx := s.ctx
	k := s.k

	thorAddr := types.GetRandomTHORAddress()
	thorAccAddr, _ := thorAddr.AccAddress()
	name := types.NewTHORName("hello", 50, []types.THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = thorAccAddr
	k.SetTHORName(ctx, name)

	// happy paths
	memo, err := ParseMemoWithTHORNames(ctx, k, "d:"+common.RuneAsset().String())
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxDonate), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.String(), Equals, "DONATE:"+common.RuneAsset().String())

	memo, err = ParseMemoWithTHORNames(ctx, k, "ADD:"+common.RuneAsset().String())
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxAdd), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.String(), Equals, "+:THOR.RUNE")

	_, err = ParseMemoWithTHORNames(ctx, k, "ADD:BTC.BTC")
	c.Assert(err, IsNil)
	memo, err = ParseMemoWithTHORNames(ctx, k, "ADD:BTC.BTC:bc1qwqdg6squsna38e46795at95yu9atm8azzmyvckulcc7kytlcckxswvvzej")
	c.Assert(err, IsNil)
	c.Check(memo.GetDestination().String(), Equals, "bc1qwqdg6squsna38e46795at95yu9atm8azzmyvckulcc7kytlcckxswvvzej")
	c.Check(memo.IsType(TxAdd), Equals, true, Commentf("MEMO: %+v", memo))

	_, err = ParseMemoWithTHORNames(ctx, k, "ADD:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:"+thorAddr.String()+":1000")
	c.Assert(err, IsNil)

	memo, err = ParseMemoWithTHORNames(ctx, k, "TCY:"+thorAddr.String())
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYClaim), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetAddress(), Equals, thorAddr)

	memo, err = ParseMemoWithTHORNames(ctx, k, "tcy+")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYStake), Equals, true, Commentf("MEMO: %+v", memo))

	memo, err = ParseMemoWithTHORNames(ctx, k, "tcy-:10000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYUnstake), Equals, true, Commentf("MEMO: %+v", memo))

	// trade account unit tests
	trAccAddr := types.GetRandomBech32Addr()
	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("trade+:%s", trAccAddr))
	c.Assert(err, IsNil)
	tr1, ok := memo.(TradeAccountDepositMemo)
	c.Assert(ok, Equals, true)
	c.Check(tr1.GetAccAddress().Equals(trAccAddr), Equals, true)

	ethAddr := types.GetRandomETHAddress()
	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("trade-:%s", ethAddr))
	c.Assert(err, IsNil)
	tr2, ok := memo.(TradeAccountWithdrawalMemo)
	c.Assert(ok, Equals, true)
	fmt.Println(tr2)
	c.Check(tr2.GetAddress().Equals(ethAddr), Equals, true)

	memo, err = ParseMemoWithTHORNames(ctx, k, "WITHDRAW:"+common.RuneAsset().String()+":25")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxWithdraw), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetAmount().Equal(cosmos.NewUint(25)), Equals, true, Commentf("%d", memo.GetAmount().Uint64()))

	memo, err = ParseMemoWithTHORNames(ctx, k, "SWAP:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:870000000:hello:100")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Equal(cosmos.NewUint(870000000)), Equals, true)
	c.Check(len(memo.GetAffiliates()), Equals, 1)
	c.Check(len(memo.GetAffiliatesBasisPoints()), Equals, 1)
	c.Check(memo.GetAffiliates()[0], Equals, "hello")
	c.Check(memo.GetAffiliatesBasisPoints()[0].Uint64(), Equals, uint64(100))

	memo, err = ParseMemoWithTHORNames(ctx, k, "SWAP:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Uint64(), Equals, uint64(0))

	memo, err = ParseMemoWithTHORNames(ctx, k, "SWAP:"+common.RuneAsset().String()+":0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:")
	c.Assert(err, IsNil)
	c.Check(memo.GetAsset().String(), Equals, common.RuneAsset().String())
	c.Check(memo.IsType(TxSwap), Equals, true, Commentf("MEMO: %+v", memo))
	c.Check(memo.GetDestination().String(), Equals, "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Check(memo.GetSlipLimit().Uint64(), Equals, uint64(0))

	whiteListAddr := types.GetRandomBech32Addr()
	bondProvider := types.GetRandomBech32Addr()
	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("BOND:%s:%s", whiteListAddr, bondProvider))
	c.Assert(err, IsNil)
	c.Assert(memo.IsType(TxBond), Equals, true)
	c.Assert(memo.GetAccAddress().String(), Equals, whiteListAddr.String())
	bondParser, _ := newParser(ctx, k, k.GetVersion(), fmt.Sprintf("BOND:%s:%s", whiteListAddr.String(), bondProvider.String()))
	mem, err := bondParser.ParseBondMemo()
	c.Assert(err, IsNil)
	c.Assert(mem.BondProviderAddress.String(), Equals, bondProvider.String())
	c.Assert(mem.NodeOperatorFee, Equals, int64(-1))
	bondParser, _ = newParser(ctx, k, k.GetVersion(), fmt.Sprintf("BOND:%s:%s:0", whiteListAddr.String(), bondProvider.String()))
	mem, err = bondParser.ParseBondMemo()
	c.Assert(err, IsNil)
	c.Assert(mem.BondProviderAddress.String(), Equals, bondProvider.String())
	c.Assert(mem.NodeOperatorFee, Equals, int64(0))
	bondParser, _ = newParser(ctx, k, k.GetVersion(), fmt.Sprintf("BOND:%s:%s:1000", whiteListAddr.String(), bondProvider.String()))
	mem, err = bondParser.ParseBondMemo()
	c.Assert(err, IsNil)
	c.Assert(mem.BondProviderAddress.String(), Equals, bondProvider.String())
	c.Assert(mem.NodeOperatorFee, Equals, int64(1000))

	memo, err = ParseMemoWithTHORNames(ctx, k, fmt.Sprintf("BONDYIELD:%s:%d", whiteListAddr, constants.MaxBasisPts))
	c.Assert(err, IsNil)
	c.Assert(memo.IsType(TxBondYield), Equals, true)
	c.Assert(memo.GetAccAddress().String(), Equals, whiteListAddr.String())
	bondYieldMemo, ok := memo.(BondYieldMemo)
	c.Assert(ok, Equals, true)
	c.Assert(bondYieldMemo.GetBps(), Equals, constants.MaxBasisPts)
	memoParser, _ := newParser(ctx, k, k.GetVersion(), fmt.Sprintf("BONDYIELD:%s:2500:yieldname", whiteListAddr.String()))
	bondYieldMemo, err = memoParser.ParseBondYieldMemo()
	c.Assert(err, IsNil)
	c.Assert(bondYieldMemo.GetBps(), Equals, uint64(2500))
	c.Assert(bondYieldMemo.PayoutTarget, Equals, "yieldname")
	memoParser, _ = newParser(ctx, k, k.GetVersion(), fmt.Sprintf("BONDYIELD:%s:AUTO:2500:yieldname", whiteListAddr.String()))
	_, err = memoParser.ParseBondYieldMemo()
	c.Assert(err, NotNil)

	memo, err = ParseMemoWithTHORNames(ctx, k, "leave:"+types.GetRandomBech32Addr().String())
	c.Assert(err, IsNil)
	c.Assert(memo.IsType(TxLeave), Equals, true)

	memo, err = ParseMemoWithTHORNames(ctx, k, "unbond:"+whiteListAddr.String()+":300")
	c.Assert(err, IsNil)
	c.Assert(memo.IsType(TxUnbond), Equals, true)
	c.Assert(memo.GetAccAddress().String(), Equals, whiteListAddr.String())
	c.Assert(memo.GetAmount().Equal(cosmos.NewUint(300)), Equals, true)
	bondParser, _ = newParser(ctx, k, k.GetVersion(), fmt.Sprintf("UNBOND:%s:400:%s", whiteListAddr.String(), bondProvider.String()))
	unbondMemo, err := bondParser.ParseUnbondMemo()
	c.Assert(err, IsNil)
	c.Assert(unbondMemo.BondProviderAddress.String(), Equals, bondProvider.String())

	memo, err = ParseMemoWithTHORNames(ctx, k, "migrate:100")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxMigrate), Equals, true)
	c.Check(memo.GetBlockHeight(), Equals, int64(100))
	c.Check(memo.String(), Equals, "MIGRATE:100")

	txID := types.GetRandomTxHash()
	memo, err = ParseMemoWithTHORNames(ctx, k, "OUT:"+txID.String())
	c.Check(err, IsNil)
	c.Check(memo.IsOutbound(), Equals, true)
	c.Check(memo.GetTxID(), Equals, txID)
	c.Check(memo.String(), Equals, "OUT:"+txID.String())

	refundMemo := "REFUND:" + txID.String()
	memo, err = ParseMemoWithTHORNames(ctx, k, refundMemo)
	c.Check(err, IsNil)
	c.Check(memo.GetTxID(), Equals, txID)
	c.Check(memo.String(), Equals, refundMemo)

	ragnarokMemo := "RAGNAROK:1024"
	memo, err = ParseMemoWithTHORNames(ctx, k, ragnarokMemo)
	c.Check(err, IsNil)
	c.Check(memo.IsType(TxRagnarok), Equals, true)
	c.Check(memo.GetBlockHeight(), Equals, int64(1024))
	c.Check(memo.String(), Equals, ragnarokMemo)

	baseMemo := MemoBase{}
	c.Check(baseMemo.String(), Equals, "")
	c.Check(baseMemo.GetAmount().Uint64(), Equals, cosmos.ZeroUint().Uint64())
	c.Check(baseMemo.GetDestination(), Equals, common.NoAddress)
	c.Check(baseMemo.GetSlipLimit().Uint64(), Equals, cosmos.ZeroUint().Uint64())
	c.Check(baseMemo.GetTxID(), Equals, common.TxID(""))
	c.Check(baseMemo.GetAccAddress().Empty(), Equals, true)
	c.Check(baseMemo.IsEmpty(), Equals, true)
	c.Check(baseMemo.GetBlockHeight(), Equals, int64(0))

	// swap memo parsing

	// aff fee too high, should be reset to 10_000
	_, err = ParseMemoWithTHORNames(ctx, k, "swap:eth.eth:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:100:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:20000")
	c.Assert(err, NotNil)

	// aff fee valid, don't change
	memo, err = ParseMemoWithTHORNames(ctx, k, "swap:eth.eth:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:100:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:5000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxSwap), Equals, true)
	c.Check(memo.String(), Equals, "=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:100:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:5000")

	// add memo parsing

	_, err = ParseMemoWithTHORNames(ctx, k, "add:eth.eth:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:20000")
	c.Assert(err, NotNil)

	// aff fee valid, don't change
	memo, err = ParseMemoWithTHORNames(ctx, k, "add:btc.btc:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:5000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxAdd), Equals, true)
	c.Check(memo.String(), Equals, "+:BTC.BTC:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj:5000")

	// aff fee savers memo
	_, err = ParseMemoWithTHORNames(ctx, k, "+:BSC/BNB::t:0")
	// should fail, thorname not registered
	c.Assert(err.Error(), Equals, "MEMO: +:BSC/BNB::t:0\nPARSE FAILURE(S): cannot parse 't' as an Address: t is not recognizable")
	// register thorname
	thorname := types.NewTHORName("t", 50, []types.THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	k.SetTHORName(ctx, thorname)
	_, err = ParseMemoWithTHORNames(ctx, k, "+:BSC/BNB::t:15")
	c.Assert(err, IsNil)

	// no address or aff fee
	memo, err = ParseMemoWithTHORNames(ctx, k, "add:btc.btc")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxAdd), Equals, true)
	c.Check(memo.String(), Equals, "+:BTC.BTC")

	// no aff fee
	memo, err = ParseMemoWithTHORNames(ctx, k, "add:btc.btc:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxAdd), Equals, true)
	c.Check(memo.String(), Equals, "+:BTC.BTC:thor1z83z5t9vqxys8nhpkxk5zp6zym0lalcp8ywhvj")

	// unhappy paths
	memo, err = ParseMemoWithTHORNames(ctx, k, "")
	c.Assert(err, NotNil)
	c.Assert(memo.IsEmpty(), Equals, true)
	_, err = ParseMemoWithTHORNames(ctx, k, "bogus")
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "CREATE") // missing symbol
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "CREATE:") // bad symbol
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "withdraw") // not enough parameters
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "withdraw:eth") // withdraw basis points is optional
	c.Assert(err, IsNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "withdraw:eth:twenty-two") // bad amount
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "swap") // not enough parameters
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "swap:eth:PROVIDER-1:5.6") // bad destination
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "swap:eth:bad_DES:5.6") // bad destination
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "swap:eth:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a:five") // bad slip limit
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "admin:key:val") // not enough arguments
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "admin:bogus:key:value") // bogus admin command type
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "migrate:abc")
	c.Assert(err, NotNil)

	_, err = ParseMemoWithTHORNames(ctx, k, "withdraw:A")
	c.Assert(err, IsNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "leave")
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "out") // not enough parameter
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "bond") // not enough parameter
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "refund") // not enough parameter
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "ragnarok") // not enough parameter
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "ragnarok:what") // not enough parameter
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "bond:what") // invalid address
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "whatever") // not support
	c.Assert(err, NotNil)

	_, err = ParseMemoWithTHORNames(ctx, k, "tcy:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a") // invalid thor address
	c.Assert(err, NotNil)
	_, err = ParseMemoWithTHORNames(ctx, k, "tcy-") // emptu bps
	c.Assert(err, NotNil)
}

func (s *MemoSuite) TestParseTHORNameMemo(c *C) {
	ctx := s.ctx
	k := s.k

	addr := types.GetRandomTHORAddress()

	// happy path: full memo with multiplier (positions: ~:name:chain:addr:owner:asset:expiry:multiplier)
	memo, err := ParseMemoWithTHORNames(ctx, k, "~:test:THOR:"+addr.String()+"::::500")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTHORName), Equals, true)
	tMemo, ok := memo.(ManageTHORNameMemo)
	c.Assert(ok, Equals, true)
	c.Check(tMemo.GetName(), Equals, "test")
	c.Check(tMemo.GetChain(), Equals, common.THORChain)
	c.Check(tMemo.GetAddress(), Equals, addr)
	c.Check(tMemo.PreferredAssetOutboundFeeMultiplier, Equals, int64(500))

	// omitted multiplier defaults to -1 (sentinel for "not provided")
	memo, err = ParseMemoWithTHORNames(ctx, k, "~:test2:THOR:"+addr.String())
	c.Assert(err, IsNil)
	tMemo, ok = memo.(ManageTHORNameMemo)
	c.Assert(ok, Equals, true)
	c.Check(tMemo.PreferredAssetOutboundFeeMultiplier, Equals, int64(-1))

	// explicit 0 parses as 0 (reset to global default)
	memo, err = ParseMemoWithTHORNames(ctx, k, "~:test3:THOR:"+addr.String()+"::::0")
	c.Assert(err, IsNil)
	tMemo, ok = memo.(ManageTHORNameMemo)
	c.Assert(ok, Equals, true)
	c.Check(tMemo.PreferredAssetOutboundFeeMultiplier, Equals, int64(0))

	// preferred asset with multiplier (skip owner and expiry)
	memo, err = ParseMemoWithTHORNames(ctx, k, "~:test4:THOR:"+addr.String()+"::BTC.BTC::400")
	c.Assert(err, IsNil)
	tMemo, ok = memo.(ManageTHORNameMemo)
	c.Assert(ok, Equals, true)
	c.Check(tMemo.PreferredAsset, Equals, common.BTCAsset)
	c.Check(tMemo.PreferredAssetOutboundFeeMultiplier, Equals, int64(400))
}

func (s *MemoSuite) TestGetUintWithScientificNotation(c *C) {
	// Create a parser instance for testing
	p := parser{
		parts: []string{""},
		errs:  make([]error, 0),
	}

	// Test case 1: Regular number (no scientific notation)
	p.parts[0] = "12345"
	result := p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "12345")

	// Test case 2: Scientific notation - small exponent
	p.parts[0] = "123e2"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "12300")

	// Test case 3: Scientific notation - large number (primary issue)
	p.parts[0] = "8914517914040814862e2"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "891451791404081486200", Commentf("Expected exact precision for large scientific notation"))

	// Test case 4: Another large number from the issue
	p.parts[0] = "877419668427886729900"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "877419668427886729900")

	// Test case 5: Very large number that would overflow float64 precision
	p.parts[0] = "9223372036854775807123456789e2"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "922337203685477580712345678900")

	// Test case 6: Scientific notation with uppercase E
	p.parts[0] = "456E3"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "456000")

	// Test case 7: Zero exponent
	p.parts[0] = "789e0"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "789")

	// Test case 8: Maximum allowed exponent (boundary test)
	p.parts[0] = "123e74"
	result = p.getUintWithScientificNotation(0, false, 0)
	// 123 * 10^74 should work without error
	c.Check(result.String(), Matches, "12300000000000000000000000000000000000000000000000000000000000000000000000000")

	// ERROR CONDITION TESTS

	// Reset errors for error testing
	p.errs = make([]error, 0)

	// ERROR 1: Empty required field
	p.parts[0] = ""
	result = p.getUintWithScientificNotation(0, true, 999)
	c.Check(result.String(), Equals, "999", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Equals, "required index idx value is empty")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 2: Invalid scientific notation format (multiple 'e')
	p.parts[0] = "123e4e5"
	result = p.getUintWithScientificNotation(0, true, 888)
	c.Check(result.String(), Equals, "888", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Equals, "cannot parse '123e4e5' as scientific notation")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 3: Invalid scientific notation format (no exponent)
	p.parts[0] = "123e"
	result = p.getUintWithScientificNotation(0, true, 777)
	c.Check(result.String(), Equals, "777", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse exponent '' in scientific notation:.*")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 4: Invalid coefficient (negative number)
	p.parts[0] = "-123e2"
	result = p.getUintWithScientificNotation(0, true, 666)
	c.Check(result.String(), Equals, "666", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse coefficient '-123' in scientific notation:.*")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 5: Invalid coefficient (non-numeric)
	p.parts[0] = "abce2"
	result = p.getUintWithScientificNotation(0, true, 555)
	c.Check(result.String(), Equals, "555", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse coefficient 'abc' in scientific notation:.*")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 6: Invalid exponent (non-numeric)
	p.parts[0] = "123eabc"
	result = p.getUintWithScientificNotation(0, true, 444)
	c.Check(result.String(), Equals, "444", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse exponent 'abc' in scientific notation:.*")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 7: Negative exponent (fractional values not supported)
	p.parts[0] = "123e-2"
	result = p.getUintWithScientificNotation(0, true, 333)
	c.Check(result.String(), Equals, "333", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Equals, "the memo parser does not support fractional values: 123e-2")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 8: Exponent exceeds maximum allowed value (now 77)
	p.parts[0] = "123e78" // Changed from e75 to e78
	result = p.getUintWithScientificNotation(0, true, 222)
	c.Check(result.String(), Equals, "222", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Equals, "exponent 78 exceeds maximum allowed value of 77: 123e78") // Updated error message

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 9: Invalid uint parsing (non-scientific notation)
	p.parts[0] = "not_a_digit"
	result = p.getUintWithScientificNotation(0, true, 111)
	c.Check(result.String(), Equals, "111", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse 'not_a_digit' as an uint:.*")

	// Reset errors
	p.errs = make([]error, 0)

	// ERROR 10: Invalid uint parsing (negative regular number)
	p.parts[0] = "-456"
	result = p.getUintWithScientificNotation(0, true, 222)
	c.Check(result.String(), Equals, "222", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "cannot parse '-456' as an uint:.*")

	// EDGE CASE TESTS

	// Reset errors
	p.errs = make([]error, 0)

	// Test optional field with empty value (should not error)
	p.parts[0] = "" // empty value
	result = p.getUintWithScientificNotation(0, false, 999)
	c.Check(result.String(), Equals, "999", Commentf("Should return default value without error"))
	c.Check(len(p.errs), Equals, 0, Commentf("Should not add error for optional empty field"))

	// Test optional field with invalid scientific notation (should still error since value is not empty)
	p.parts[0] = "123e4e5" // invalid scientific notation
	result = p.getUintWithScientificNotation(0, false, 888)
	c.Check(result.String(), Equals, "888", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1, Commentf("Should still error for optional field with invalid non-empty value"))
	c.Check(p.errs[0].Error(), Equals, "cannot parse '123e4e5' as scientific notation")

	// OVERFLOW PROTECTION TESTS - Add these after line 830

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Maximum safe exponent (77) with small coefficient - should work
	p.parts[0] = "1e77"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(len(p.errs), Equals, 0, Commentf("1e77 should be within 256-bit bounds"))
	c.Check(result.String(), Equals, "100000000000000000000000000000000000000000000000000000000000000000000000000000") // 10^77

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Exponent 78 should fail (exceeds maxExponent of 77)
	p.parts[0] = "1e78"
	result = p.getUintWithScientificNotation(0, true, 999)
	c.Check(result.String(), Equals, "999", Commentf("Should return default value"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Equals, "exponent 78 exceeds maximum allowed value of 77: 1e78")

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Large coefficient with small exponent that would overflow
	// Maximum 256-bit value is 2^256 - 1 ≈ 1.16 × 10^77
	// So coefficient * 10^1 > max should fail
	maxUint256 := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	overflowCoeff := maxUint256.QuoUint64(5) // This * 10 will overflow
	p.parts[0] = fmt.Sprintf("%se1", overflowCoeff.String())
	result = p.getUintWithScientificNotation(0, true, 888)
	c.Check(result.String(), Equals, "888", Commentf("Should return default value due to overflow"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "coefficient .* would cause overflow when multiplied by 10\\^1: .*")

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Edge case - exactly at the boundary (should work)
	// Calculate max coefficient for exponent 2: maxUint256 / 100
	maxCoeffForExp2 := maxUint256.QuoUint64(100)
	p.parts[0] = fmt.Sprintf("%se2", maxCoeffForExp2.String())
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(len(p.errs), Equals, 0, Commentf("Boundary case should not overflow"))
	c.Check(result.GT(cosmos.ZeroUint()), Equals, true, Commentf("Boundary result should be > 0"))

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Just over the boundary (should fail)
	overBoundaryCoeff := maxCoeffForExp2.AddUint64(1)
	p.parts[0] = fmt.Sprintf("%se2", overBoundaryCoeff.String())
	result = p.getUintWithScientificNotation(0, true, 777)
	c.Check(result.String(), Equals, "777", Commentf("Should return default value due to overflow"))
	c.Check(len(p.errs), Equals, 1)
	c.Check(p.errs[0].Error(), Matches, "coefficient .* would cause overflow when multiplied by 10\\^2: .*")

	// Reset errors
	p.errs = make([]error, 0)

	// Test case: Zero coefficient should never overflow
	p.parts[0] = "0e77"
	result = p.getUintWithScientificNotation(0, false, 0)
	c.Check(result.String(), Equals, "0", Commentf("Zero should never overflow"))
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoSuite) TestScientificNotationInMemos(c *C) {
	ctx := s.ctx
	k := s.k

	// Test in swap memos with DEX target limits
	memo, err := ParseMemoWithTHORNames(ctx, k,
		"=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::::dex:target:1e18")
	c.Assert(err, IsNil)
	c.Check(memo.GetDexTargetLimit().String(), Equals, "1000000000000000000")

	// Test overflow in DEX target limits
	_, err = ParseMemoWithTHORNames(ctx, k,
		"=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::::dex:target:999999999999999999999999999999999999999e50")
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "overflow"), Equals, true)

	// Test in other memo types that use scientific notation
	// Add similar tests for any other fields that use getUintWithScientificNotation
}

func (s *MemoSuite) TestScientificNotationProperties(c *C) {
	// Property: For any valid scientific notation string that doesn't overflow,
	// parsing it should give the same result as manual calculation

	maxUint256 := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")

	for i := 0; i < 100; i++ {
		// #nosec G404 - Using weak random generator is acceptable for tests
		coeff := rand.Int63n(1000) + 1
		// #nosec G404 - Using weak random generator is acceptable for tests
		exp := rand.Int63n(77)

		scientificStr := fmt.Sprintf("%de%d", coeff, exp)

		// Calculate multiplier first to check if it would overflow
		multiplier := cosmos.NewUint(1)
		for j := int64(0); j < exp; j++ {
			multiplier = multiplier.MulUint64(10)
		}

		// Check if coeff * multiplier would overflow
		coeffUint := cosmos.NewUint(uint64(coeff))
		if coeffUint.GT(maxUint256.Quo(multiplier)) {
			// Skip this test case as it would overflow
			continue
		}

		p := parser{
			parts: []string{scientificStr},
			errs:  make([]error, 0),
		}

		result := p.getUintWithScientificNotation(0, false, 0)

		// Calculate expected result manually (we know it won't overflow now)
		expected := coeffUint.Mul(multiplier)

		c.Check(result.String(), Equals, expected.String(),
			Commentf("Failed for %s", scientificStr))
		c.Check(len(p.errs), Equals, 0)
	}
}

func (s *MemoSuite) TestScientificNotationErrorHandling(c *C) {
	errorCases := []struct {
		input       string
		expectedErr string
		required    bool
	}{
		{"", "required index idx value is empty", true},
		{"123e4e5", "cannot parse '123e4e5' as scientific notation", true},
		{"123e", "cannot parse exponent '' in scientific notation", true},
		{"-123e2", "cannot parse coefficient '-123'", true},
		{"abce2", "cannot parse coefficient 'abc'", true},
		{"123eabc", "cannot parse exponent 'abc'", true},
		{"123e-2", "the memo parser does not support fractional values", true},
		{"123e78", "exponent 78 exceeds maximum allowed value of 77", true},
		{"1e78", "exponent 78 exceeds maximum allowed value of 77", true}, // Simpler overflow case
	}

	for _, tc := range errorCases {
		p := parser{
			parts: []string{tc.input},
			errs:  make([]error, 0),
		}

		result := p.getUintWithScientificNotation(0, tc.required, 999)

		if tc.expectedErr != "" {
			c.Check(len(p.errs), Equals, 1, Commentf("Input: %s, Expected error but got %d errors", tc.input, len(p.errs)))
			if len(p.errs) > 0 { // Safety check
				c.Check(p.errs[0].Error(), Matches, fmt.Sprintf(".*%s.*", tc.expectedErr),
					Commentf("Input: %s, Error: %s", tc.input, p.errs[0].Error()))
			}
			c.Check(result.String(), Equals, "999") // Should return default
		}
	}
}

func (s *MemoSuite) TestScientificNotationBoundaries(c *C) {
	// Test the exact 256-bit boundary
	maxUint256 := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")

	// Test 10^77 vs 10^78
	ten77 := cosmos.NewUintFromString("100000000000000000000000000000000000000000000000000000000000000000000000000000")

	// Verify 10^77 < 2^256-1
	c.Check(ten77.LT(maxUint256), Equals, true, Commentf("10^77 should be less than 2^256-1"))

	// Test boundary coefficients for different exponents
	testCases := []struct {
		exp        int64
		maxCoeff   string
		shouldWork bool
	}{
		{1, "11579208923731619542357098500868790785326998466564056403945758400791312963993", true},
		{2, "1157920892373161954235709850086879078532699846656405640394575840079131296399", true},
		{77, "1", true},
		{78, "1", false}, // Should fail due to exponent limit
	}

	for _, tc := range testCases {
		p := parser{
			parts: []string{fmt.Sprintf("%se%d", tc.maxCoeff, tc.exp)},
			errs:  make([]error, 0),
		}

		result := p.getUintWithScientificNotation(0, false, 0)

		if tc.shouldWork {
			c.Check(len(p.errs), Equals, 0, Commentf("Should work: %se%d", tc.maxCoeff, tc.exp))
			// Also verify the result is not the default value (0)
			c.Check(result.GT(cosmos.ZeroUint()), Equals, true, Commentf("Result should be > 0 for: %se%d", tc.maxCoeff, tc.exp))
		} else {
			c.Check(len(p.errs), Equals, 1, Commentf("Should fail: %se%d", tc.maxCoeff, tc.exp))
			// For failed cases, result should be the default value (0)
			c.Check(result.String(), Equals, "0", Commentf("Result should be default for failed case: %se%d", tc.maxCoeff, tc.exp))
		}
	}
}

func (s *MemoSuite) TestReferenceWriteMemoValidation(c *C) {
	ctx := s.ctx
	k := s.k

	// Test valid embedded memo
	thorAddr := types.GetRandomTHORAddress()
	validMemo := fmt.Sprintf("reference:ETH:=:THOR.RUNE:%s", thorAddr.String())
	parsed, err := ParseMemoWithTHORNames(ctx, k, validMemo)
	c.Assert(err, IsNil)
	c.Assert(parsed.GetType(), Equals, TxReferenceWriteMemo)
	refMemo, ok := parsed.(ReferenceWriteMemo)
	c.Assert(ok, Equals, true)
	c.Assert(refMemo.Memo, Equals, fmt.Sprintf("=:THOR.RUNE:%s", thorAddr.String()))

	// Test invalid embedded memo - malformed swap
	invalidMemo1 := "reference:ETH:=:INVALID_ASSET:address"
	_, err = ParseMemoWithTHORNames(ctx, k, invalidMemo1)
	c.Assert(err, Not(IsNil))
	c.Assert(strings.Contains(err.Error(), "embedded memo is invalid"), Equals, true)

	// Test invalid embedded memo - empty parts
	invalidMemo2 := "reference:ETH:=:::"
	_, err = ParseMemoWithTHORNames(ctx, k, invalidMemo2)
	c.Assert(err, Not(IsNil))
	c.Assert(strings.Contains(err.Error(), "embedded memo is invalid"), Equals, true)

	// Test invalid embedded memo - completely malformed
	invalidMemo3 := "reference:ETH:NOTAMEMO"
	_, err = ParseMemoWithTHORNames(ctx, k, invalidMemo3)
	c.Assert(err, Not(IsNil))
	c.Assert(strings.Contains(err.Error(), "embedded memo is invalid"), Equals, true)
}

func (s *MemoSuite) TestParseMemoWithTHORNamesCached(c *C) {
	// CacheContext discards THORName writes so they do not leak into other tests.
	baseCtx, _ := s.ctx.CacheContext()
	k := s.k

	thorAddr := types.GetRandomTHORAddress()
	resolveMemo := "=:THOR.RUNE:cachetest"
	otherMemo := "=:THOR.RUNE:" + thorAddr.String()

	// no cache on the context: falls through to an uncached parse
	_, err := ParseMemoWithTHORNamesCached(baseCtx, k, resolveMemo)
	c.Assert(err, NotNil)
	c.Check(baseCtx.Value(memoCacheKey{}), IsNil)

	ctx := WithMemoCache(baseCtx)

	// miss: the unregistered THORName fails to parse and the error is cached
	_, err = ParseMemoWithTHORNamesCached(ctx, k, resolveMemo)
	c.Assert(err, NotNil)
	cachedErr := err

	name := types.NewTHORName("cachetest", 50, []types.THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	k.SetTHORName(ctx, name)

	// hit: the cached error is reused even though the THORName now resolves
	_, err = ParseMemoWithTHORNamesCached(ctx, k, resolveMemo)
	c.Check(err, Equals, cachedErr)
	_, err = ParseMemoWithTHORNames(ctx, k, resolveMemo)
	c.Assert(err, IsNil)

	// a distinct memo string is a separate entry
	mem, err := ParseMemoWithTHORNamesCached(ctx, k, otherMemo)
	c.Assert(err, IsNil)
	c.Check(mem.GetDestination(), Equals, thorAddr)

	// a fresh cache does not share entries with the previous one
	freshCtx := WithMemoCache(baseCtx)
	mem, err = ParseMemoWithTHORNamesCached(freshCtx, k, resolveMemo)
	c.Assert(err, IsNil)
	c.Check(mem.GetDestination(), Equals, thorAddr)

	// hit: the cached success survives the THORName changing underneath it
	newAddr := types.GetRandomTHORAddress()
	name.Aliases = []types.THORNameAlias{{Chain: common.THORChain, Address: newAddr}}
	k.SetTHORName(freshCtx, name)
	mem, err = ParseMemoWithTHORNamesCached(freshCtx, k, resolveMemo)
	c.Assert(err, IsNil)
	c.Check(mem.GetDestination(), Equals, thorAddr)
	mem, err = ParseMemoWithTHORNames(freshCtx, k, resolveMemo)
	c.Assert(err, IsNil)
	c.Check(mem.GetDestination(), Equals, newAddr)

	// contexts derived from a cached context share its cache
	childCtx, _ := freshCtx.CacheContext()
	mem, err = ParseMemoWithTHORNamesCached(childCtx, k, resolveMemo)
	c.Assert(err, IsNil)
	c.Check(mem.GetDestination(), Equals, thorAddr)
}

func (s *MemoSuite) TestParseMemoWithTHORNamesCachedSharesSlices(c *C) {
	ctx := WithMemoCache(s.ctx)
	aff1 := types.GetRandomTHORAddress()
	aff2 := types.GetRandomTHORAddress()
	ms := fmt.Sprintf("=:e:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a::%s/%s:10/20", aff1, aff2)

	first, err := ParseMemoWithTHORNamesCached(ctx, s.k, ms)
	c.Assert(err, IsNil)
	second, err := ParseMemoWithTHORNamesCached(ctx, s.k, ms)
	c.Assert(err, IsNil)

	// cached Memo values alias the same backing arrays, so callers must not mutate them
	c.Assert(len(first.GetAffiliates()), Equals, 2)
	c.Check(&first.GetAffiliates()[0] == &second.GetAffiliates()[0], Equals, true)
	c.Check(&first.GetAffiliatesBasisPoints()[0] == &second.GetAffiliatesBasisPoints()[0], Equals, true)
}
