package thorchain

import (
	"errors"
	"fmt"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerSwapSuite struct{}

var _ = Suite(&HandlerSwapSuite{})

func (s *HandlerSwapSuite) TestValidate(c *C) {
	ctx, _ := setupKeeperForTest(c)

	keeper := &TestSwapHandleKeeper{
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
	}

	handler := NewSwapHandler(NewDummyMgrWithKeeper(keeper))

	txID := GetRandomTxHash()
	signerDOGEAddr := GetRandomDOGEAddress()
	observerAddr := keeper.activeNodeAccount.NodeAddress
	tx := common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.OneUint()),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		"",
	)
	msg := NewMsgSwap(tx, common.DOGEAsset, signerDOGEAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// bad aggregator reference
	msg.Aggregator = "zzzzzz"
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// invalid msg
	msg = &MsgSwap{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

type TestSwapHandleKeeper struct {
	keeper.KVStoreDummy
	pools             map[common.Asset]Pool
	activeNodeAccount NodeAccount
	synthSupply       cosmos.Uint
	haltChain         int64
	derivedAssets     bool
	mimirs            map[string]int64
}

func (k *TestSwapHandleKeeper) GetConfigInt64(ctx cosmos.Context, key constants.ConstantName) int64 {
	if key.String() == "PoolCycle" {
		return 43200
	}
	val, _ := k.GetMimir(ctx, key.String())
	return val
}

func (k *TestSwapHandleKeeper) PoolExist(_ cosmos.Context, asset common.Asset) bool {
	asset = asset.GetLayer1Asset()
	_, ok := k.pools[asset]
	return ok
}

func (k *TestSwapHandleKeeper) GetPool(_ cosmos.Context, asset common.Asset) (Pool, error) {
	asset = asset.GetLayer1Asset()
	if pool, ok := k.pools[asset]; ok {
		return pool, nil
	}
	pool := NewPool()
	pool.Asset = asset
	return pool, nil
}

func (k *TestSwapHandleKeeper) GetPools(_ cosmos.Context) (pools Pools, err error) {
	for _, v := range k.pools {
		pools = append(pools, v)
	}
	return
}

func (k *TestSwapHandleKeeper) SetPool(_ cosmos.Context, pool Pool) error {
	asset := pool.Asset.GetLayer1Asset()
	k.pools[asset] = pool
	return nil
}

// IsActiveObserver see whether it is an active observer
func (k *TestSwapHandleKeeper) IsActiveObserver(_ cosmos.Context, addr cosmos.AccAddress) bool {
	return k.activeNodeAccount.NodeAddress.Equals(addr)
}

func (k *TestSwapHandleKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.activeNodeAccount.NodeAddress.Equals(addr) {
		return k.activeNodeAccount, nil
	}
	return NodeAccount{}, errors.New("not exist")
}

func (k *TestSwapHandleKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return NodeAccounts{k.activeNodeAccount}, nil
}

func (k *TestSwapHandleKeeper) AddToLiquidityFees(_ cosmos.Context, _ common.Asset, _ cosmos.Uint) error {
	return nil
}

func (k *TestSwapHandleKeeper) AddToSwapSlip(ctx cosmos.Context, asset common.Asset, fs cosmos.Int) error {
	return nil
}

func (k *TestSwapHandleKeeper) GetTotalSupply(_ cosmos.Context, _ common.Asset) cosmos.Uint {
	return k.synthSupply
}

func (k *TestSwapHandleKeeper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if k.mimirs != nil {
		if val, ok := k.mimirs[key]; ok {
			return val, nil
		}
	}
	if key == "MaxSynthPerPoolDepth" {
		return 5000, nil
	}
	if key == "EnableDerivedAssets" {
		if k.derivedAssets {
			return 1, nil
		}
		return 0, nil
	}
	return k.haltChain, nil
}

func (k *TestSwapHandleKeeper) SetMimir(ctx cosmos.Context, key string, value int64) {
	if k.mimirs == nil {
		k.mimirs = make(map[string]int64)
	}
	k.mimirs[key] = value
}

func (k *TestSwapHandleKeeper) GetMimirWithRef(ctx cosmos.Context, template string, ref ...any) (int64, error) {
	key := fmt.Sprintf(template, ref...)
	return k.GetMimir(ctx, key)
}

func (k *TestSwapHandleKeeper) MintToModule(_ cosmos.Context, _ string, _ common.Coin) error {
	return nil
}

func (k *TestSwapHandleKeeper) BurnFromModule(_ cosmos.Context, _ string, _ common.Coin) error {
	return nil
}

func (k *TestSwapHandleKeeper) SendFromModuleToModule(_ cosmos.Context, _, _ string, _ common.Coins) error {
	return nil
}

func (k *TestSwapHandleKeeper) GetModuleAddress(_ string) (common.Address, error) {
	return GetRandomTHORAddress(), nil
}

func (s *HandlerSwapSuite) TestValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pools := make(map[common.Asset]Pool)
	pools[pool.Asset] = pool
	keeper := &TestSwapHandleKeeper{
		pools:             pools,
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		synthSupply:       cosmos.ZeroUint(),
	}
	keeper.activeNodeAccount.Bond = cosmos.NewUint(10_000_000 * common.One)
	mgr.K = keeper
	mgr.txOutStore = NewTxStoreDummy()
	mgr.constAccessor = constants.NewDummyConstants(
		map[constants.ConstantName]int64{},
		map[constants.ConstantName]bool{
			constants.StrictBondLiquidityRatio: false,
		},
		map[constants.ConstantName]string{},
	)

	handler := NewSwapHandler(mgr)

	txID := GetRandomTxHash()
	signerDOGEAddr := GetRandomDOGEAddress()
	observerAddr := keeper.activeNodeAccount.NodeAddress
	tx := common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*100)),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		"",
	)
	msg := NewMsgSwap(tx, common.DOGEAsset.GetSyntheticAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// check that derived assets are no allowed
	msg.TargetAsset = common.BTCAsset.GetDerivedAsset()
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	// enable derived assets and try again
	keeper.derivedAssets = true
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)
	msg.TargetAsset = common.DOGEAsset.GetSyntheticAsset()

	// check that minting synths halts after hitting pool limit
	keeper.synthSupply = cosmos.NewUint(common.One * 200)
	mgr.K = keeper
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerSwapSuite) TestValidationWithStreamingSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, "MaxStreamingSwapLength", 3850)
	mgr.Keeper().SetMimir(ctx, "MinBPStreamingSwap", 4)
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.txOutStore = NewTxStoreDummy()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	handler := NewSwapHandler(mgr)

	txID := GetRandomTxHash()
	signerDOGEAddr := GetRandomDOGEAddress()
	observerAddr := na.NodeAddress
	tx := common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*100)),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		"",
	)

	// happy path
	msg := NewMsgSwap(tx, common.DOGEAsset.GetSyntheticAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, observerAddr)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// test mimir shutdown
	mgr.Keeper().SetMimir(ctx, "StreamingSwapPause", 1)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	mgr.Keeper().SetMimir(ctx, "StreamingSwapPause", 0)

	// check that validation fails due to synth cap
	mgr.Keeper().SetMimir(ctx, "MaxSynthPerPoolDepth", 1)
	// first swap should fail as it include the total value of the streaming swap
	c.Assert(handler.validate(ctx, *msg), NotNil)
	// second swap should NOT fail as the synth cap is ignored
	swp := msg.GetStreamingSwap()
	swp.Count = 3
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(handler.validate(ctx, *msg), IsNil)
}

func (s *HandlerSwapSuite) TestValidateRejectsDestinationEqualsTargetContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	// the target asset's own token contract, as a destination address.
	contractAddr := common.Address("0xA0b86991c6218b36c1D19D4a2e9Eb0cE3606eB48")

	// Available ETH.USDC pool so a valid wallet-dest swap passes validation cleanly.
	pool := NewPool()
	pool.Asset = usdc
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	handler := NewSwapHandler(mgr)
	signerAddr := GetRandomTHORAddress()
	observerAddr := na.NodeAddress

	buildMsg := func(dest common.Address) *MsgSwap {
		memoStr := "=:" + usdc.String() + ":" + dest.String()
		tx := common.NewTx(
			GetRandomTxHash(), signerAddr, signerAddr,
			common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*100))},
			common.Gas{common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000))},
			memoStr,
		)
		return NewMsgSwap(tx, usdc, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
	}

	// destination == target asset's token contract -> refunded at validation.
	err = handler.validate(ctx, *buildMsg(contractAddr))
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "token contract"), Equals, true, Commentf("got: %v", err))

	// a normal recipient wallet passes validation outright — proves the guard
	// does not over-match ordinary addresses.
	walletAddr := common.Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Assert(handler.validate(ctx, *buildMsg(walletAddr)), IsNil)
}

func (s *HandlerSwapSuite) TestHandle(c *C) {
	ctx, mgr := setupManagerForTest(c)
	keeper := &TestSwapHandleKeeper{
		pools:             make(map[common.Asset]Pool),
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		synthSupply:       cosmos.ZeroUint(),
	}
	mgr.K = keeper
	mgr.txOutStore = NewTxStoreDummy()
	handler := NewSwapHandler(mgr)

	result, err := handler.Run(ctx, NewMsgMimir("what", 1, GetRandomBech32Addr()))
	c.Check(err, NotNil)
	c.Check(result, IsNil)
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	txID := GetRandomTxHash()
	signerDOGEAddr := GetRandomDOGEAddress()
	observerAddr := keeper.activeNodeAccount.NodeAddress
	// no pool
	tx := common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.OneUint()),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		"",
	)
	msg := NewMsgSwap(tx, common.DOGEAsset, signerDOGEAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// swap of only 0.00000001 would emit 0, thus rejected.
	_, _, err = handler.handleWithEmit(ctx, *msg)
	c.Assert(err.Error(), Equals, "zero emit asset")

	tx = common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One)),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		"",
	)
	msgSwapPriceProtection := NewMsgSwap(tx, common.DOGEAsset, signerDOGEAddr, cosmos.NewUint(2*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
	result, _, err = handler.handleWithEmit(ctx, *msgSwapPriceProtection)
	c.Assert(err.Error(), Equals, errors.New("emit asset 192196256 less than price limit 200000000").Error())
	c.Assert(result, IsNil)

	poolTCAN := NewPool()
	tCanAsset, err := common.NewAsset("DOGE.TCAN-014")
	c.Assert(err, IsNil)
	poolTCAN.Asset = tCanAsset
	poolTCAN.BalanceAsset = cosmos.NewUint(334850000)
	poolTCAN.BalanceRune = cosmos.NewUint(2349500000)
	c.Assert(mgr.Keeper().SetPool(ctx, poolTCAN), IsNil)
	dogeAddr := GetRandomDOGEAddress()
	m, err := ParseMemo(mgr.GetVersion(), "swap:DOGE.DOGE:"+dogeAddr.String()+":121800000")
	c.Assert(err, IsNil)
	txIn := NewObservedTx(
		common.NewTx(
			GetRandomTxHash(), signerDOGEAddr, GetRandomDOGEAddress(),
			common.Coins{
				common.NewCoin(tCanAsset, cosmos.NewUint(20000000)),
			},
			common.Gas{
				common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
			},
			"swap:DOGE.DOGE:"+signerDOGEAddr.String()+":121800000",
		),
		1,
		GetRandomPubKey(), 1,
	)
	msgSwapFromTxIn, err := getMsgSwapFromMemo(ctx, mgr.Keeper(), m.(SwapMemo), txIn, observerAddr)
	c.Assert(err, IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)

	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil) // reset the pool
	_, err = handler.Run(ctx, msgSwapFromTxIn.(*MsgSwap))
	c.Assert(err, IsNil)
	c.Assert(processSwapQueues(ctx, mgr), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)

	result, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	msgSwap := NewMsgSwap(GetRandomTx(), common.EmptyAsset, GetRandomDOGEAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	result, err = handler.Run(ctx, msgSwap)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// When chain is halted , swap should respect it
	keeper.haltChain = 1
	result, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	keeper.haltChain = 0
}

func (s *HandlerSwapSuite) TestHandleRejectsWrongNetworkZECDestination(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewSwapHandler(mgr)
	observerAddr := GetRandomValidatorNode(NodeActive).NodeAddress
	_, tex, _, wrongNetworkTex := zecTxOutAddresses()
	wrongNetworkP2PKH := common.Address("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkP2PKH = "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU"
	}
	for _, dest := range []common.Address{wrongNetworkTex, wrongNetworkP2PKH} {
		tx := common.NewTx(GetRandomTxHash(), GetRandomRUNEAddress(), GetRandomRUNEAddress(),
			common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}, common.Gas{}, "")
		msg := NewMsgSwap(tx, common.ZECAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
		_, _, err := handler.handleWithEmit(ctx, *msg)
		c.Assert(err, NotNil, Commentf("destination: %s", dest))
		c.Check(err.Error(), Equals, fmt.Sprintf("address(%s) is not same network", dest))
	}

	// A current-network destination passes the check and fails later on the
	// missing pool instead.
	tx := common.NewTx(GetRandomTxHash(), GetRandomRUNEAddress(), GetRandomRUNEAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}, common.Gas{}, "")
	msg := NewMsgSwap(tx, common.ZECAsset, tex, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, observerAddr)
	_, _, err := handler.handleWithEmit(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Not(Matches), ".*is not same network.*")
}

func (s *HandlerSwapSuite) TestHandleStreamingSwap(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.txOutStore = NewTxStoreDummy()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	handler := NewSwapHandler(mgr)

	txID := GetRandomTxHash()
	signerDOGEAddr := GetRandomDOGEAddress()
	// no pool
	tx := common.NewTx(
		txID,
		signerDOGEAddr,
		signerDOGEAddr,
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(2_123400000)),
		},
		common.Gas{
			common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
		},
		fmt.Sprintf("=:DOGE.DOGE:%s", signerDOGEAddr),
	)
	msg := NewMsgSwap(tx, common.DOGEAsset, signerDOGEAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 3, 5, types.SwapVersion_v1, na.NodeAddress)
	swp := msg.GetStreamingSwap()
	swp.Deposit = tx.Coins[0].Amount
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)
	// Enable advanced swap queue so streaming swaps are handled by the queue manager
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	_, _, err = handler.handleWithEmit(ctx, *msg)
	c.Assert(err, IsNil)

	// ensure we don't add items into txout for streaming swaps. That is
	// handled in the swap queue manager
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)

	swp, err = mgr.Keeper().GetStreamingSwap(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(swp.In.String(), Equals, "707800000")
	c.Check(swp.Out.String(), Equals, "617319586")

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	_, _, err = handler.handleWithEmit(ctx, *msg)
	c.Assert(err, IsNil)
	swp, err = mgr.Keeper().GetStreamingSwap(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(swp.In.String(), Equals, "1415600000")
	c.Check(swp.Out.String(), Equals, "1165237487")

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	_, _, err = handler.handleWithEmit(ctx, *msg)
	c.Assert(err, IsNil)
	swp, err = mgr.Keeper().GetStreamingSwap(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(swp.In.String(), Equals, "2123400000")
	c.Check(swp.Out.String(), Equals, "1654583341")

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	_, _, err = handler.handleWithEmit(ctx, *msg)
	c.Assert(err, NotNil)
	swp, err = mgr.Keeper().GetStreamingSwap(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(swp.In.String(), Equals, "2123400000")
	c.Check(swp.Out.String(), Equals, "1654583341")
}

func (s *HandlerSwapSuite) TestHandleWithEmitUsesStateStreamingQuantityForV2Swaps(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	handler := NewSwapHandler(mgr)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomDOGEAddress(),
		GetRandomDOGEAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One))},
		common.Gas{common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000))},
		"=:DOGE.DOGE:"+GetRandomDOGEAddress().String(),
	)

	msg := NewMsgSwap(tx, common.DOGEAsset, GetRandomDOGEAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, na.NodeAddress)
	msg.State = &types.SwapState{
		Quantity:   5,
		Interval:   1,
		Count:      0,
		Deposit:    cosmos.NewUint(5 * common.One),
		In:         cosmos.ZeroUint(),
		Out:        cosmos.ZeroUint(),
		Withdrawn:  cosmos.ZeroUint(),
		LastHeight: 0,
	}

	_, _, err := handler.handleWithEmit(ctx, *msg)
	c.Assert(err, IsNil)

	foundSwapEvent := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != types.SwapEventType {
			continue
		}
		foundSwapEvent = true
		var quantityAttr, countAttr string
		for _, attr := range evt.Attributes {
			switch attr.Key {
			case "streaming_swap_quantity":
				quantityAttr = attr.Value
			case "streaming_swap_count":
				countAttr = attr.Value
			}
		}
		c.Check(quantityAttr, Equals, "5")
		c.Check(countAttr, Equals, "1")
	}
	c.Check(foundSwapEvent, Equals, true)
}

func (s *HandlerSwapSuite) TestSwapSynthERC20(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	handler := NewSwapHandler(mgr)

	pool := NewPool()
	asset, err := common.NewAsset("ETH.AAVE-0X7FC66500C84A76AD7E9C93437BFC5AC33E2DDAE9")
	c.Assert(err, IsNil)
	pool.Asset = asset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	m, err := ParseMemo(mgr.GetVersion(), "=:ETH/AAVE-0X7FC66:thor1x0jkvqdh2hlpeztd5zyyk70n3efx6mhudkmnn2::thor1a427q3v96psuj4fnughdw8glt5r7j38lj7rkp8:100")
	c.Assert(err, IsNil)
	swapM, ok := m.(SwapMemo)
	c.Assert(ok, Equals, true)
	swapM.Asset = fuzzyAssetMatch(ctx, mgr.K, swapM.Asset)
	txIn := NewObservedTx(
		common.NewTx(
			"832B575FC2E92057BE1E1D69277B5AF690ADDF3E98E76FFC67232F846D87CB45", "0xd58610f89265a2fb637ac40edf59141ff873b266", "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
			common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(20000000)),
			},
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
			},
			"=:ETH/AAVE-0X7FC66:thor1x0jkvqdh2hlpeztd5zyyk70n3efx6mhudkmnn2::thor1a427q3v96psuj4fnughdw8glt5r7j38lj7rkp8:100",
		),
		1,
		GetRandomPubKey(), 1,
	)
	observerAddr, err := GetRandomTHORAddress().AccAddress()
	c.Assert(err, IsNil)
	msgSwapFromTxIn, err := getMsgSwapFromMemo(ctx, mgr.Keeper(), m.(SwapMemo), txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err := handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(res, IsNil)
	c.Assert(err, NotNil)
}

func (s *HandlerSwapSuite) TestDoubleSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	keeper := &TestSwapHandleKeeper{
		pools:             make(map[common.Asset]Pool),
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		synthSupply:       cosmos.ZeroUint(),
	}
	mgr.K = keeper
	mgr.txOutStore = NewTxStoreDummy()
	handler := NewSwapHandler(mgr)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	poolTCAN := NewPool()
	tCanAsset, err := common.NewAsset("DOGE.TCAN-014")
	c.Assert(err, IsNil)
	poolTCAN.Asset = tCanAsset
	poolTCAN.BalanceAsset = cosmos.NewUint(334850000)
	poolTCAN.BalanceRune = cosmos.NewUint(2349500000)
	c.Assert(mgr.Keeper().SetPool(ctx, poolTCAN), IsNil)

	signerDOGEAddr := GetRandomDOGEAddress()
	observerAddr := keeper.activeNodeAccount.NodeAddress

	// double swap - happy path
	m, err := ParseMemo(mgr.GetVersion(), "swap:DOGE.DOGE:"+signerDOGEAddr.String())
	c.Assert(err, IsNil)
	txIn := NewObservedTx(
		common.NewTx(
			GetRandomTxHash(), signerDOGEAddr, GetRandomDOGEAddress(),
			common.Coins{
				common.NewCoin(tCanAsset, cosmos.NewUint(20000000)),
			},
			common.Gas{
				common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
			},
			"swap:DOGE.DOGE:"+signerDOGEAddr.String(),
		),
		1,
		GetRandomPubKey(), 1,
	)
	msgSwapFromTxIn, err := getMsgSwapFromMemo(ctx, mgr.Keeper(), m.(SwapMemo), txIn, observerAddr)
	c.Assert(err, IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)

	_, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, IsNil)
	c.Assert(processSwapQueues(ctx, mgr), IsNil)

	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	// double swap , RUNE not enough to pay for transaction fee
	testnetDOGEAddr := GetRandomDOGEAddress()
	m1, err := ParseMemo(mgr.GetVersion(), "swap:DOGE.DOGE:"+testnetDOGEAddr.String())
	c.Assert(err, IsNil)
	txIn1 := NewObservedTx(
		common.NewTx(
			GetRandomTxHash(), signerDOGEAddr, GetRandomDOGEAddress(),
			common.Coins{
				common.NewCoin(tCanAsset, cosmos.NewUint(10000)),
			},
			common.Gas{
				common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000)),
			},
			"swap:DOGE.DOGE:"+testnetDOGEAddr.String(),
		),
		1,
		GetRandomPubKey(), 1,
	)
	msgSwapFromTxIn1, err := getMsgSwapFromMemo(ctx, mgr.Keeper(), m1.(SwapMemo), txIn1, observerAddr)
	c.Assert(err, IsNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)
	_, err = handler.Run(ctx, msgSwapFromTxIn1)
	c.Assert(err, IsNil)
	// This would actually error with ErrNotEnoughToPayFee from the txout manager,
	// but here mgr.txOutStore is a TxOutStoreDummy and without vaults set the happy path would also fail;
	// this test only checks the behaviour of the swap handler, not what happens after a TxOutItem is sent to the txout manager.

	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
}

func (s *HandlerSwapSuite) TestSwapOutDexIntegration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	keeper := &TestSwapHandleKeeper{
		pools:             make(map[common.Asset]Pool),
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		synthSupply:       cosmos.ZeroUint(),
	}
	mgr.K = keeper
	mgr.txOutStore = NewTxStoreDummy()
	handler := NewSwapHandler(mgr)

	pool := NewPool()
	asset, err := common.NewAsset("ETH.ETH")
	c.Assert(err, IsNil)
	pool.Asset = asset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	swapMemo := "swap:ETH.ETH:" + types.GetRandomETHAddress().String() + "::::ec49fd2:" + types.GetRandomETHAddress().String()
	m, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), swapMemo)
	c.Assert(err, IsNil)

	txIn := NewObservedTx(
		common.NewTx(
			GetRandomTxHash(), GetRandomTHORAddress(), GetRandomTHORAddress(),
			common.Coins{
				common.NewCoin(common.RuneNative, cosmos.NewUint(2000000000)),
			},
			common.Gas{
				common.NewCoin(common.RuneNative, cosmos.NewUint(20000000)),
			},
			swapMemo,
		),
		1,
		GetRandomPubKey(), 1,
	)

	observerAddr, err := GetRandomTHORAddress().AccAddress()
	c.Assert(err, IsNil)
	msgSwapFromTxIn, err := getMsgSwapFromMemo(ctx, mgr.Keeper(), m.(SwapMemo), txIn, observerAddr)
	c.Assert(err, IsNil)
	// when SwapOut Dex integration has been disabled by mimir , it should return an error cause refund
	mgr.Keeper().SetMimir(ctx, constants.SwapOutDexAggregationDisabled.String(), 1)
	res, err := handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(res, IsNil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "swap out dex integration disabled")

	mgr.Keeper().SetMimir(ctx, constants.SwapOutDexAggregationDisabled.String(), 0)

	// when target asset address is empty , swap should fail
	swapM, ok := m.(SwapMemo)
	c.Assert(ok, Equals, true)
	swapM.DexTargetAddress = ""
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
	c.Assert(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(strings.HasPrefix(err.Error(), "aggregator target asset address is empty"), Equals, true)

	// When the target asset is not ETH.ETH, it should fail
	swapM, ok = m.(SwapMemo)
	c.Assert(ok, Equals, true)
	AAVEAsset, err := common.NewAsset("ETH.AAVE-0X7FC66500C84A76AD7E9C93437BFC5AC33E2DDAE9")
	c.Assert(err, IsNil)
	swapM.Asset = AAVEAsset
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
	c.Assert(err.Error(), Equals, "target asset (ETH.AAVE-0X7FC66500C84A76AD7E9C93437BFC5AC33E2DDAE9) is not gas asset , can't use dex feature")

	// when specified aggregator is not white list , swap should fail
	swapM, ok = m.(SwapMemo)
	c.Assert(ok, Equals, true)
	swapM.DexAggregator = "whatever"
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
	c.Assert(err.Error(), Equals, "whatever aggregator not found")

	// when aggregator target address is not valid , but we don't care
	swapM, ok = m.(SwapMemo)
	c.Assert(ok, Equals, true)
	swapM.DexTargetAddress = "whatever"
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)

	// when aggregator target address and target chain doesn't match , don't care
	swapM, ok = m.(SwapMemo)
	c.Assert(ok, Equals, true)
	swapM.DexTargetAddress = GetRandomDOGEAddress().String()
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)

	mgr.TxOutStore().ClearOutboundItems(ctx)
	// normal swap with DEX
	swapM, ok = m.(SwapMemo)
	c.Assert(ok, Equals, true)
	msgSwapFromTxIn, err = getMsgSwapFromMemo(ctx, mgr.Keeper(), swapM, txIn, observerAddr)
	c.Assert(err, IsNil)
	res, err = handler.Run(ctx, msgSwapFromTxIn)
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	c.Assert(processSwapQueues(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Aggregator, Equals, "0xd31f7e39afECEc4855fecc51b693F9A0Cec49fd2")
	c.Assert(items[0].AggregatorTargetAsset, Equals, swapM.DexTargetAddress)
	c.Assert(items[0].AggregatorTargetLimit, IsNil)
}

// Helper function to process both swap queues
func processSwapQueues(ctx cosmos.Context, mgr Manager) error {
	if err := mgr.SwapQ().EndBlock(ctx, mgr); err != nil {
		return err
	}
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		if err := mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false); err != nil {
			return err
		}
	}
	return nil
}

// TestSwapDynamicFeeIntegration exercises the end-to-end wiring that the
// DynamicFeeMgr unit tests alone cannot cover: two full epoch cycles through
// the manager used at runtime (mgr.DynamicFeeMgr()), followed by
// getMaxSwapQuantity consulting that same manager via the affiliate tuple.
// A regression that mis-threads the (affiliates, affiliateBps, sourceAsset,
// targetAsset) tuple through getMaxSwapQuantity — or that wires a different
// DynamicFeeMgr into the swap-sizing path than the one EndBlock seals to —
// would make the affiliate case collapse back to the trivial quantity=1
// branch. The bare-call assertion catches both hazards cheaply.
func (s *HandlerSwapSuite) TestSwapDynamicFeeIntegration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Enable the feature with a short epoch so EndBlock seals deterministically
	// at blocks 10 and 20. Leave L1SlipMinBps=0 so the dynamic bps is the only
	// thing that can produce a non-zero minSlip in getMaxSwapQuantity — without
	// this, the baseline L1 floor would mask whether the dynamic lookup fired.
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, constants.L1SlipMinBps.String(), 0)

	registerThorname(ctx, k, "bentley", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	affiliates := []string{"bentley"}
	affiliateBps := []cosmos.Uint{cosmos.NewUint(100)}

	// --- Epoch 1 (block 10) ---
	mgr.DynamicFeeMgr().CreditSwap(
		ctx, affiliates, affiliateBps,
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10*common.One), cosmos.NewUint(100),
	)
	acc1, err := k.GetDynamicFeeAccumulator(ctx, "bentley", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc1.VolumeTor.Equal(cosmos.NewUint(10*common.One)), Equals, true,
		Commentf("CreditSwap must populate the accumulator via the runtime DynamicFeeMgr"))

	ctx = ctx.WithBlockHeight(10)
	c.Assert(mgr.DynamicFeeMgr().EndBlock(ctx), IsNil)

	rec1, err := k.GetDynamicFeeRecord(ctx, "bentley", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec1.DynamicBps, Equals, uint64(2),
		Commentf("epoch 1 seals as cold_start_probe from floor=1"))

	// --- Epoch 2 (block 20) ---
	mgr.DynamicFeeMgr().CreditSwap(
		ctx, affiliates, affiliateBps,
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(20*common.One), cosmos.NewUint(300),
	)
	ctx = ctx.WithBlockHeight(20)
	c.Assert(mgr.DynamicFeeMgr().EndBlock(ctx), IsNil)

	rec2, err := k.GetDynamicFeeRecord(ctx, "bentley", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec2.DynamicBps, Equals, uint64(3),
		Commentf("epoch 2 seals again as cold_start_probe from 2→3"))

	// --- Assert the dynamic floor visibly reshapes next-swap pricing ---
	swp := StreamingSwap{
		TxID:     GetRandomTxHash(),
		Interval: 1,
		Deposit:  cosmos.NewUint(10 * common.One),
	}

	qWithAffiliate, err := getMaxSwapQuantity(ctx, mgr,
		common.BTCAsset, common.RuneAsset(), swp, affiliates, affiliateBps)
	c.Assert(err, IsNil)

	qWithoutAffiliate, err := getMaxSwapQuantity(ctx, mgr,
		common.BTCAsset, common.RuneAsset(), swp, nil, nil)
	c.Assert(err, IsNil)

	c.Check(qWithoutAffiliate, Equals, uint64(1),
		Commentf("no affiliate + L1SlipMinBps=0 → zero floor → trivial quantity"))
	c.Check(qWithAffiliate > qWithoutAffiliate, Equals, true,
		Commentf("whitelisted affiliate must surface its sealed dynamic bps to getMaxSwapQuantity (got %d vs %d)",
			qWithAffiliate, qWithoutAffiliate))
}
