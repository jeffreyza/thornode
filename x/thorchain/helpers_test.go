package thorchain

import (
	"fmt"
	"strings"

	"cosmossdk.io/math"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HelperSuite struct{}

var _ = Suite(&HelperSuite{})

type TestRefundBondKeeper struct {
	keeper.KVStoreDummy
	pool    Pool
	na      NodeAccount
	vaults  Vaults
	modules map[string]int64
	consts  constants.ConstantValues
}

func (k *TestRefundBondKeeper) GetConfigInt64(ctx cosmos.Context, key constants.ConstantName) int64 {
	return k.consts.GetInt64Value(key)
}

func (k *TestRefundBondKeeper) GetAsgardVaultsByStatus(_ cosmos.Context, _ VaultStatus) (Vaults, error) {
	return k.vaults, nil
}

func (k *TestRefundBondKeeper) VaultExists(_ cosmos.Context, pk common.PubKey) bool {
	return true
}

func (k *TestRefundBondKeeper) GetLeastSecure(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) Vault {
	return vaults[0]
}

func (k *TestRefundBondKeeper) GetPool(_ cosmos.Context, asset common.Asset) (Pool, error) {
	if k.pool.Asset.Equals(asset) {
		return k.pool, nil
	}
	return NewPool(), errKaboom
}

func (k *TestRefundBondKeeper) SetNodeAccount(_ cosmos.Context, na NodeAccount) error {
	k.na = na
	return nil
}

func (k *TestRefundBondKeeper) SetPool(_ cosmos.Context, p Pool) error {
	if k.pool.Asset.Equals(p.Asset) {
		k.pool = p
		return nil
	}
	return errKaboom
}

func (k *TestRefundBondKeeper) SetBondProviders(ctx cosmos.Context, _ BondProviders) error {
	return nil
}

func (k *TestRefundBondKeeper) GetBondProviders(ctx cosmos.Context, add cosmos.AccAddress) (BondProviders, error) {
	return BondProviders{}, nil
}

func (k *TestRefundBondKeeper) SendFromModuleToModule(_ cosmos.Context, from, to string, coins common.Coins) error {
	k.modules[from] -= int64(coins[0].Amount.Uint64())
	k.modules[to] += int64(coins[0].Amount.Uint64())
	return nil
}

func (k *TestRefundBondKeeper) SendFromModuleToAccount(_ cosmos.Context, from string, _ cosmos.AccAddress, coins common.Coins) error {
	k.modules[from] -= int64(coins[0].Amount.Uint64())
	return nil
}

func (s *HelperSuite) TestRefundBondHappyPath(c *C) {
	ctx, _ := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(12098 * common.One)
	pk := GetRandomPubKey()
	na.PubKeySet.Secp256k1 = pk
	keeper := &TestRefundBondKeeper{
		modules: make(map[string]int64),
		consts:  constants.GetConstantValues(GetCurrentVersion()),
	}
	na.Status = NodeStandby
	mgr := NewDummyMgrWithKeeper(keeper)
	tx := GetRandomTx()
	tx.FromAddress, _ = common.NewAddress(na.BondAddress.String())
	err := refundBond(ctx, tx, na.NodeAddress, cosmos.ZeroUint(), &na, mgr)
	c.Assert(err, IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (s *HelperSuite) TestRefundBondDisableRequestToLeaveNode(c *C) {
	ctx, _ := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(12098 * common.One)
	pk := GetRandomPubKey()
	na.PubKeySet.Secp256k1 = pk
	keeper := &TestRefundBondKeeper{
		modules: make(map[string]int64),
		consts:  constants.GetConstantValues(GetCurrentVersion()),
	}
	na.Status = NodeStandby
	na.RequestedToLeave = true
	mgr := NewDummyMgrWithKeeper(keeper)
	tx := GetRandomTx()
	err := refundBond(ctx, tx, na.NodeAddress, cosmos.ZeroUint(), &na, mgr)
	c.Assert(err, IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	c.Assert(err, IsNil)
	c.Assert(keeper.na.Status == NodeDisabled, Equals, true)
}

func (s *HelperSuite) TestDollarsPerRune(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-ETH-BUSD-BD1", 1) // enable BUSD pool as a TOR anchor
	busd, err := common.NewAsset("ETH.BUSD-BD1")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = busd
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(85515078103667)
	pool.BalanceAsset = cosmos.NewUint(709802235538353)
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)

	runeUSDPrice := telem(mgr.Keeper().DollarsPerRune(ctx))
	c.Assert(runeUSDPrice, Equals, float32(8.300317))

	// Now try with a second pool, identical depths.
	mgr.Keeper().SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1) // enable USDC pool as a TOR anchor
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	pool = NewPool()
	pool.Asset = usdc
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(85515078103667)
	pool.BalanceAsset = cosmos.NewUint(709802235538353)
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)

	runeUSDPrice = telem(mgr.Keeper().DollarsPerRune(ctx))
	c.Assert(runeUSDPrice, Equals, float32(8.300317))
}

func (s *HelperSuite) TestTelem(c *C) {
	value := cosmos.NewUint(12047733)
	c.Assert(value.Uint64(), Equals, uint64(12047733))
	c.Assert(telem(value), Equals, float32(0.12047733))
}

type addGasFeesKeeperHelper struct {
	keeper.Keeper
	errGetNetwork bool
	errSetNetwork bool
	errGetPool    bool
	errSetPool    bool
}

func newAddGasFeesKeeperHelper(keeper keeper.Keeper) *addGasFeesKeeperHelper {
	return &addGasFeesKeeperHelper{
		Keeper: keeper,
	}
}

func (h *addGasFeesKeeperHelper) GetNetwork(ctx cosmos.Context) (Network, error) {
	if h.errGetNetwork {
		return Network{}, errKaboom
	}
	return h.Keeper.GetNetwork(ctx)
}

func (h *addGasFeesKeeperHelper) SetNetwork(ctx cosmos.Context, data Network) error {
	if h.errSetNetwork {
		return errKaboom
	}
	return h.Keeper.SetNetwork(ctx, data)
}

func (h *addGasFeesKeeperHelper) SetPool(ctx cosmos.Context, pool Pool) error {
	if h.errSetPool {
		return errKaboom
	}
	return h.Keeper.SetPool(ctx, pool)
}

func (h *addGasFeesKeeperHelper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if h.errGetPool {
		return Pool{}, errKaboom
	}
	return h.Keeper.GetPool(ctx, asset)
}

type addGasFeeTestHelper struct {
	ctx cosmos.Context
	na  NodeAccount
	mgr Manager
}

func newAddGasFeeTestHelper(c *C) addGasFeeTestHelper {
	ctx, mgr := setupManagerForTest(c)
	keeper := newAddGasFeesKeeperHelper(mgr.Keeper())
	mgr.K = keeper
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.BalanceAsset = cosmos.NewUint(100 * common.One)
	poolBTC.BalanceRune = cosmos.NewUint(100 * common.One)
	poolBTC.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, poolBTC), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, na.PubKeySet.Secp256k1, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	// TODO:  Perhaps make this vault entirely unrelated to the NodeAccount pubkey, such as with an addGasFeeTestHelper 'vault' field.
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	version := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(version)
	mgr.gasMgr = newGasMgr(constAccessor, keeper)
	return addGasFeeTestHelper{
		ctx: ctx,
		mgr: mgr,
		na:  na,
	}
}

func (s *HelperSuite) TestAddGasFees(c *C) {
	testCases := []struct {
		name        string
		txCreator   func(helper addGasFeeTestHelper) ObservedTx
		runner      func(helper addGasFeeTestHelper, tx ObservedTx) error
		expectError bool
		validator   func(helper addGasFeeTestHelper, c *C)
	}{
		{
			name: "empty Gas should just return nil",
			txCreator: func(helper addGasFeeTestHelper) ObservedTx {
				return GetRandomObservedTx()
			},

			expectError: false,
		},
		{
			name: "normal ETH gas",
			txCreator: func(helper addGasFeeTestHelper) ObservedTx {
				tx := ObservedTx{
					Tx: common.Tx{
						ID:          GetRandomTxHash(),
						Chain:       common.ETHChain,
						FromAddress: GetRandomETHAddress(),
						ToAddress:   GetRandomETHAddress(),
						Coins: common.Coins{
							common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
							common.NewCoin(common.RuneAsset(), cosmos.NewUint(8*common.One)),
						},
						Gas: common.Gas{
							common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
						},
						Memo: "",
					},
					Status:         common.Status_done,
					OutHashes:      nil,
					BlockHeight:    helper.ctx.BlockHeight(),
					Signers:        []string{helper.na.NodeAddress.String()},
					ObservedPubKey: helper.na.PubKeySet.Secp256k1,
				}
				return tx
			},
			runner: func(helper addGasFeeTestHelper, tx ObservedTx) error {
				return addGasFees(helper.ctx, helper.mgr, tx)
			},
			expectError: false,
			validator: func(helper addGasFeeTestHelper, c *C) {
				expected := common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))
				c.Assert(helper.mgr.GasMgr().GetGas(), HasLen, 1)
				c.Assert(helper.mgr.GasMgr().GetGas()[0].Equals(expected), Equals, true)
			},
		},
		{
			name: "normal BTC gas",
			txCreator: func(helper addGasFeeTestHelper) ObservedTx {
				tx := ObservedTx{
					Tx: common.Tx{
						ID:          GetRandomTxHash(),
						Chain:       common.BTCChain,
						FromAddress: GetRandomBTCAddress(),
						ToAddress:   GetRandomBTCAddress(),
						Coins: common.Coins{
							common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One)),
						},
						Gas: common.Gas{
							common.NewCoin(common.BTCAsset, cosmos.NewUint(2000)),
						},
						Memo: "",
					},
					Status:         common.Status_done,
					OutHashes:      nil,
					BlockHeight:    helper.ctx.BlockHeight(),
					Signers:        []string{helper.na.NodeAddress.String()},
					ObservedPubKey: helper.na.PubKeySet.Secp256k1,
				}
				return tx
			},
			runner: func(helper addGasFeeTestHelper, tx ObservedTx) error {
				return addGasFees(helper.ctx, helper.mgr, tx)
			},
			expectError: false,
			validator: func(helper addGasFeeTestHelper, c *C) {
				expected := common.NewCoin(common.BTCAsset, cosmos.NewUint(2000))
				c.Assert(helper.mgr.GasMgr().GetGas(), HasLen, 1)
				c.Assert(helper.mgr.GasMgr().GetGas()[0].Equals(expected), Equals, true)
			},
		},
	}
	for _, tc := range testCases {
		helper := newAddGasFeeTestHelper(c)
		tx := tc.txCreator(helper)
		var err error
		if tc.runner == nil {
			err = addGasFees(helper.ctx, helper.mgr, tx)
		} else {
			err = tc.runner(helper, tx)
		}

		if err != nil && !tc.expectError {
			c.Errorf("test case: %s,didn't expect error however it got : %s", tc.name, err)
			c.FailNow()
		}
		if err == nil && tc.expectError {
			c.Errorf("test case: %s, expect error however it didn't", tc.name)
			c.FailNow()
		}
		if !tc.expectError && tc.validator != nil {
			tc.validator(helper, c)
			continue
		}
	}
}

// TestAddGasFeesDeficit asserts that addGasFees records gas deficits
// and halts when the gas deficit crosses the max-gas-based threshold.
func (s *HelperSuite) TestAddGasFeesDeficit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(4444)
	k := mgr.Keeper()

	// Vault has 100 wei of ETH — far less than the gas the observed tx claims.
	vault := GetRandomVault()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Pool needed by addGasFees' downstream gas-manager bookkeeping.
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	tx := ObservedTx{
		Tx: common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   GetRandomETHAddress(),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		},
		ObservedPubKey: vault.PubKey,
		BlockHeight:    ctx.BlockHeight(),
	}

	c.Assert(addGasFees(ctx, mgr, tx), IsNil)

	updated, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updated.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(updated.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(common.One-100)), Equals, true)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight(),
		Commentf("HaltSigningETH should fire when addGasFees creates deficit"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight(),
		Commentf("HaltETHTrading should fire when addGasFees creates deficit"))
}

func (s *HelperSuite) TestEmitPoolStageCostEvent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	emitPoolBalanceChangedEvent(ctx,
		NewPoolMod(common.BTCAsset, cosmos.NewUint(1000), false, cosmos.ZeroUint(), false), "test", mgr)
	found := false
	for _, e := range ctx.EventManager().Events() {
		if strings.EqualFold(e.Type, types.PoolBalanceChangeEventType) {
			found = true
			break
		}
	}
	c.Assert(found, Equals, true)
}

func (s *HelperSuite) TestIsSynthMintPause(c *C) {
	ctx, mgr := setupManagerForTest(c)

	mgr.Keeper().SetMimir(ctx, constants.MaxSynthPerPoolDepth.String(), 1500)

	pool := types.Pool{
		Asset:        common.BTCAsset,
		BalanceAsset: cosmos.NewUint(100 * common.One),
		BalanceRune:  cosmos.NewUint(100 * common.One),
	}
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	coins := cosmos.NewCoins(cosmos.NewCoin("btc/btc", cosmos.NewInt(29*common.One))) // 29% utilization
	c.Assert(mgr.coinKeeper.MintCoins(ctx, ModuleName, coins), IsNil)

	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.ZeroUint()), IsNil)

	// A swap that outputs 0.5 synth BTC would not surpass the synth utilization cap (29% -> 29.5%)
	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.NewUint(0.5*common.One)), IsNil)
	// A swap that outputs 1 synth BTC would not surpass the synth utilization cap (29% -> 30%)
	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.NewUint(1*common.One)), IsNil)
	// A swap that outputs 1.1 synth BTC would surpass the synth utilization cap (29% -> 30.1%)
	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.NewUint(1.1*common.One)), NotNil)

	coins = cosmos.NewCoins(cosmos.NewCoin("btc/btc", cosmos.NewInt(1*common.One))) // 30% utilization
	c.Assert(mgr.coinKeeper.MintCoins(ctx, ModuleName, coins), IsNil)

	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.ZeroUint()), IsNil)

	coins = cosmos.NewCoins(cosmos.NewCoin("btc/btc", cosmos.NewInt(1*common.One))) // 31% utilization
	c.Assert(mgr.coinKeeper.MintCoins(ctx, ModuleName, coins), IsNil)

	c.Assert(isSynthMintPaused(ctx, mgr, common.BTCAsset, cosmos.ZeroUint()), NotNil)
}

func (s *HelperSuite) TestUpdateTxOutGas(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create ObservedVoter and add a TxOut
	txVoter := GetRandomObservedTxVoter()
	txOut := GetRandomTxOutItem()
	txVoter.Actions = append(txVoter.Actions, txOut)
	mgr.Keeper().SetObservedTxInVoter(ctx, txVoter)

	// Try to set new gas, should return error as TxOut InHash doesn't match
	newGas := common.Gas{common.NewCoin(common.DOGEAsset, cosmos.NewUint(2000000))}
	err := updateTxOutGas(ctx, mgr.K, txOut, newGas)
	c.Assert(err.Error(), Equals, fmt.Sprintf("fail to find tx out in ObservedTxVoter %s", txOut.InHash))

	// Update TxOut InHash to match, should update gas
	txOut.InHash = txVoter.TxID
	txVoter.Actions[1] = txOut
	mgr.Keeper().SetObservedTxInVoter(ctx, txVoter)

	// Err should be Nil
	err = updateTxOutGas(ctx, mgr.K, txOut, newGas)
	c.Assert(err, IsNil)

	// Keeper should have updated gas of TxOut in Actions
	txVoter, err = mgr.Keeper().GetObservedTxInVoter(ctx, txVoter.TxID)
	c.Assert(err, IsNil)

	didUpdate := false
	for _, item := range txVoter.Actions {
		if item.Equals(txOut) && item.MaxGas.Equals(newGas) {
			didUpdate = true
			break
		}
	}

	c.Assert(didUpdate, Equals, true)
}

func (s *HelperSuite) TestUpdateTxOutGasRate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create ObservedVoter and add a TxOut
	txVoter := GetRandomObservedTxVoter()
	txOut := GetRandomTxOutItem()
	txVoter.Actions = append(txVoter.Actions, txOut)
	mgr.Keeper().SetObservedTxInVoter(ctx, txVoter)

	// Try to set new gas rate, should return error as TxOut InHash doesn't match
	newGasRate := int64(25)
	err := updateTxOutGasRate(ctx, mgr.K, txOut, newGasRate)
	c.Assert(err.Error(), Equals, fmt.Sprintf("fail to find tx out in ObservedTxVoter %s", txOut.InHash))

	// Update TxOut InHash to match, should update gas
	txOut.InHash = txVoter.TxID
	txVoter.Actions[1] = txOut
	mgr.Keeper().SetObservedTxInVoter(ctx, txVoter)

	// Err should be Nil
	err = updateTxOutGasRate(ctx, mgr.K, txOut, newGasRate)
	c.Assert(err, IsNil)

	// Now that the actions have been updated (dependent on Equals which checks GasRate),
	// update the GasRate in the outbound queue item.
	txOut.GasRate = newGasRate

	// Keeper should have updated gas of TxOut in Actions
	txVoter, err = mgr.Keeper().GetObservedTxInVoter(ctx, txVoter.TxID)
	c.Assert(err, IsNil)

	didUpdate := false
	for _, item := range txVoter.Actions {
		if item.Equals(txOut) && item.GasRate == newGasRate {
			didUpdate = true
			break
		}
	}

	c.Assert(didUpdate, Equals, true)
}

func (s *HelperSuite) TestPOLPoolValue(c *C) {
	ctx, mgr := setupManagerForTest(c)

	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(20 * common.One)
	btcPool.LPUnits = cosmos.NewUint(1600)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	coin := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(10*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)

	lps := LiquidityProviders{
		{
			Asset:             btcPool.Asset,
			RuneAddress:       GetRandomRUNEAddress(),
			AssetAddress:      GetRandomBTCAddress(),
			LastAddHeight:     5,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
		{
			Asset:             btcPool.Asset,
			RuneAddress:       polAddress,
			AssetAddress:      common.NoAddress,
			LastAddHeight:     10,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
	}
	for _, lp := range lps {
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}

	value, err := polPoolValue(ctx, mgr)
	c.Assert(err, IsNil)
	c.Check(value.Uint64(), Equals, uint64(150023441162), Commentf("%d", value.Uint64()))
}

// This including the test of getTotalEffectiveBond.
func (s *HelperSuite) TestSecurityBond(c *C) {
	nas := make(NodeAccounts, 0)
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(0), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ := getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(0), Commentf("%d", totalEffectiveBond.Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(10), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(10), Commentf("%d", totalEffectiveBond.Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(30), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(30), Commentf("%d", totalEffectiveBond.Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(30), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(50), Commentf("%d", totalEffectiveBond.Uint64()))
	// Only 20 of the top-bond's node is effective.

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(60), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(90), Commentf("%d", totalEffectiveBond.Uint64()))
	// Only 30 of the top-bond's node is effective.

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
		NodeAccount{Bond: cosmos.NewUint(50)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(100), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(140), Commentf("%d", totalEffectiveBond.Uint64()))
	// Only 40 of the top-bond's node is effective.

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
		NodeAccount{Bond: cosmos.NewUint(50)},
		NodeAccount{Bond: cosmos.NewUint(60)},
	}
	c.Assert(getEffectiveSecurityBond(nas).Uint64(), Equals, uint64(100), Commentf("%d", getEffectiveSecurityBond(nas).Uint64()))
	totalEffectiveBond, _ = getTotalEffectiveBond(nas)
	c.Assert(totalEffectiveBond.Uint64(), Equals, uint64(180), Commentf("%d", totalEffectiveBond.Uint64()))
	// Only 40 each of the top-bonds two nodes is effective.
}

func (s *HelperSuite) TestGetHardBondCap(c *C) {
	nas := make(NodeAccounts, 0)
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(0), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(10), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(20), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(20), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(30), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
		NodeAccount{Bond: cosmos.NewUint(50)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(40), Commentf("%d", getHardBondCap(nas).Uint64()))

	nas = NodeAccounts{
		NodeAccount{Bond: cosmos.NewUint(10)},
		NodeAccount{Bond: cosmos.NewUint(20)},
		NodeAccount{Bond: cosmos.NewUint(30)},
		NodeAccount{Bond: cosmos.NewUint(40)},
		NodeAccount{Bond: cosmos.NewUint(50)},
		NodeAccount{Bond: cosmos.NewUint(60)},
	}
	c.Assert(getHardBondCap(nas).Uint64(), Equals, uint64(40), Commentf("%d", getHardBondCap(nas).Uint64()))
}

func (s *HelperSuite) TestIsTronZeroGasTx(c *C) {
	testCases := []struct {
		Chain  common.Chain
		Asset  common.Asset
		Amount uint64
		Result bool
	}{
		{
			Chain:  common.THORChain,
			Asset:  common.TCY,
			Amount: 1,
			Result: false,
		},
		{
			Chain:  common.ETHChain,
			Asset:  common.ETHAsset,
			Amount: 123456789,
			Result: false,
		},
		{
			Chain:  common.TRONChain,
			Asset:  common.ETHAsset,
			Amount: 1,
			Result: false,
		},
		{
			Chain:  common.TRONChain,
			Asset:  common.TRXAsset,
			Amount: 2,
			Result: false,
		},
		{
			Chain:  common.TRONChain,
			Asset:  common.TRXAsset,
			Amount: 1,
			Result: true,
		},
	}

	for _, tc := range testCases {
		coin := common.Coin{Asset: tc.Asset, Amount: math.NewUint(tc.Amount)}

		tx := GetRandomTx()
		tx.Chain = tc.Chain
		tx.Gas = common.Gas{}.Add(coin)

		obsTx := NewObservedTx(tx, 7, GetRandomPubKey(), 3)

		c.Assert(isTronZeroGasTx(obsTx), Equals, tc.Result)
	}
}

func (HandlerSuite) TestIsSignedByActiveNodeAccounts(c *C) {
	ctx, mgr := setupManagerForTest(c)

	r := isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{})
	c.Check(r, Equals, false,
		Commentf("empty signers should return false"))

	nodeAddr := GetRandomBech32Addr()
	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{nodeAddr})
	c.Check(r, Equals, false,
		Commentf("empty node account should return false"))

	nodeAccount1 := GetRandomValidatorNode(NodeWhiteListed)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount1), IsNil)
	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{nodeAccount1.NodeAddress})
	c.Check(r, Equals, false,
		Commentf("non-active node account should return false"))

	nodeAccount1.Status = NodeActive
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount1), IsNil)
	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{nodeAccount1.NodeAddress})
	c.Check(r, Equals, true,
		Commentf("active node account should return true"))

	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{nodeAccount1.NodeAddress, nodeAddr})
	c.Check(r, Equals, false,
		Commentf("should return false if any signer is not an active validator"))

	nodeAccount1.Type = NodeTypeVault
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount1), IsNil)
	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{nodeAccount1.NodeAddress})
	c.Check(r, Equals, false,
		Commentf("non-validator node should return false"))

	asgardAddr := mgr.Keeper().GetModuleAccAddress(AsgardName)
	r = isSignedByActiveNodeAccounts(ctx, mgr.Keeper(), []cosmos.AccAddress{asgardAddr})
	c.Check(r, Equals, true,
		Commentf("asgard module address should return true"))
}

func (HandlerSuite) TestWillSwapSucceed(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set up some pools
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	pool2 := NewPool()
	pool2.Asset = common.ETHAsset
	pool2.Status = PoolAvailable
	pool2.BalanceRune = cosmos.NewUint(100_000 * common.One)
	pool2.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool2.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool2), IsNil)

	// Set Network fees
	networkFee := NewNetworkFee(common.ETHChain, 1, 1000)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.ETHChain, networkFee), IsNil)

	networkFee = NewNetworkFee(common.BTCChain, 1000, 10)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, networkFee), IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{
			{Asset: common.BTCAsset, Amount: cosmos.NewUint(37500)},
		},
		"",
	)

	// swap from BTC to ETH

	// no limit, should succeed
	msg := NewMsgSwap(tx, common.ETHAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)

	// no limit, but small swap, should fail
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}
	msg = NewMsgSwap(tx, common.ETHAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, false)

	// limit too high, should fail
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	msg = NewMsgSwap(tx, common.ETHAsset, GetRandomBTCAddress(), cosmos.NewUint(100*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, false)

	// limit not too high, should succeed
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	msg = NewMsgSwap(tx, common.ETHAsset, GetRandomBTCAddress(), cosmos.NewUint(1*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)

	runeTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomTHORAddress(),
		GetRandomTHORAddress(),
		common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One*50))},
		common.Gas{
			{Asset: common.RuneNative, Amount: cosmos.NewUint(20000)},
		},
		"",
	)

	// swaps from RUNE

	// swap from RUNE no limit, should succeed
	msg = NewMsgSwap(runeTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)

	// swap from RUNE, no limit, but small swap, should fail
	runeTx.Coins = common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(1))}
	msg = NewMsgSwap(runeTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, false)

	// swap from RUNE, limit too high, should fail
	runeTx.Coins = common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One*50))}
	msg = NewMsgSwap(runeTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(100*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, false)

	// swap from RUNE, limit not too high, should succeed
	msg = NewMsgSwap(runeTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(0.01*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)

	// swaps to RUNE

	// swap to RUNE, no limit, should succeed
	msg = NewMsgSwap(tx, common.RuneNative, GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)

	// swap to RUNE, limit too high, should fail
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	msg = NewMsgSwap(tx, common.RuneNative, GetRandomTHORAddress(), cosmos.NewUint(100_000*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, false)

	// swap to RUNE, limit not too high, should succeed
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	msg = NewMsgSwap(tx, common.RuneNative, GetRandomTHORAddress(), cosmos.NewUint(1*common.One), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(willSwapOutputExceedLimitAndFees(ctx, mgr, *msg), Equals, true)
}

func (HandlerSuite) TestNewSwapMemo(c *C) {
	ctx, mgr := setupManagerForTest(c)
	addr := GetRandomBTCAddress()
	memo := NewSwapMemo(ctx, mgr, common.BTCAsset, addr, cosmos.ZeroUint(), "test", cosmos.ZeroUint())
	c.Assert(memo, Equals, fmt.Sprintf("=:BTC.BTC:%s:0:test:0", addr.String()))

	memo = NewSwapMemo(ctx, mgr, common.BTCAsset, addr, cosmos.NewUint(100), "test", cosmos.NewUint(50))
	c.Assert(memo, Equals, fmt.Sprintf("=:BTC.BTC:%s:100:test:50", addr.String()))

	addr = GetRandomTHORAddress()
	memo = NewSwapMemo(ctx, mgr, common.RuneNative, addr, cosmos.NewUint(0), "", cosmos.NewUint(0))
	c.Assert(memo, Equals, fmt.Sprintf("=:THOR.RUNE:%s:0::0", addr.String()))
}

func (HandlerSuite) TestIsPeriodLastBlock(c *C) {
	ctx, _ := setupManagerForTest(c)
	var blockVar int64

	blockVar = 10
	ctx = ctx.WithBlockHeight(10)
	result := IsPeriodLastBlock(ctx, blockVar)
	c.Assert(result, Equals, true)

	blockVar = 100
	ctx = ctx.WithBlockHeight(100)
	result = IsPeriodLastBlock(ctx, blockVar)
	c.Assert(result, Equals, true)

	blockVar = 90
	ctx = ctx.WithBlockHeight(89)
	result = IsPeriodLastBlock(ctx, blockVar)
	c.Assert(result, Equals, false)

	blockVar = 100
	ctx = ctx.WithBlockHeight(101)
	result = IsPeriodLastBlock(ctx, blockVar)
	c.Assert(result, Equals, false)
}

type SettleSwapTestKeeper struct {
	keeper.KVStoreDummy
	advSwapItems  map[string]types.MsgSwap
	failRemove    bool
	voter         ObservedTxVoter
	pools         map[common.Asset]Pool
	overrideVault bool
	vaultStatus   VaultStatus
	vaultCoins    common.Coins
}

func (k *SettleSwapTestKeeper) GetObservedTxInVoter(_ cosmos.Context, txID common.TxID) (ObservedTxVoter, error) {
	if !k.voter.Tx.IsEmpty() {
		return k.voter, nil
	}
	return ObservedTxVoter{}, fmt.Errorf("voter not found")
}

func (k *SettleSwapTestKeeper) RemoveAdvSwapQueueIndex(_ cosmos.Context, msg types.MsgSwap) error {
	if k.failRemove {
		return fmt.Errorf("failed to remove from index")
	}
	return nil
}

func (k *SettleSwapTestKeeper) RemoveAdvSwapQueueItem(_ cosmos.Context, txID common.TxID, index int) error {
	if k.failRemove {
		return fmt.Errorf("failed to remove from queue")
	}
	delete(k.advSwapItems, txID.String())
	return nil
}

func (k *SettleSwapTestKeeper) GetPool(_ cosmos.Context, asset common.Asset) (Pool, error) {
	if pool, ok := k.pools[asset.GetLayer1Asset()]; ok {
		return pool, nil
	}
	return Pool{}, fmt.Errorf("pool not found: %s", asset)
}

func (k *SettleSwapTestKeeper) GetVault(_ cosmos.Context, pk common.PubKey) (Vault, error) {
	if k.overrideVault {
		vault := NewVault(0, k.vaultStatus, AsgardVault, pk, common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
		vault.Coins = k.vaultCoins
		return vault, nil
	}
	// Return a dummy vault for testing
	vault := NewVault(0, ActiveVault, AsgardVault, pk, common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
	}
	return vault, nil
}

func (k *SettleSwapTestKeeper) GetTHORNameIterator(ctx cosmos.Context) cosmos.Iterator {
	return nil
}

func (k *SettleSwapTestKeeper) GetTHORName(ctx cosmos.Context, _ string) (THORName, error) {
	return THORName{}, fmt.Errorf("thorname not found")
}

type SettleSwapRetryVaultErrorKeeper struct {
	keeper.Keeper
}

func (k *SettleSwapRetryVaultErrorKeeper) GetVault(_ cosmos.Context, _ common.PubKey) (Vault, error) {
	return Vault{}, fmt.Errorf("vault not found")
}

func seedSettleSwapRetryState(c *C, ctx cosmos.Context, k keeper.Keeper, tx common.Tx, observedPubKey common.PubKey) {
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	voter := NewObservedTxVoter(tx.ID, nil)
	voter.Tx = ObservedTx{
		Tx:             tx,
		ObservedPubKey: observedPubKey,
	}
	k.SetObservedTxInVoter(ctx, voter)
}

func (HelperSuite) TestSettleSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// Test 1: Successful swap settlement with outbound
	txID := GetRandomTxHash()

	// Create a pool for BTC to avoid refund errors
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable

	keeper := &SettleSwapTestKeeper{
		advSwapItems: make(map[string]types.MsgSwap),
		pools:        make(map[common.Asset]Pool),
		voter: ObservedTxVoter{
			Tx: ObservedTx{
				Tx: common.Tx{
					ID: txID,
				},
				ObservedPubKey: GetRandomPubKey(),
			},
		},
	}
	keeper.pools[common.BTCAsset] = btcPool
	mgr.K = keeper
	mgr.txOutStore = NewTxStoreDummy()
	ethAddr := GetRandomETHAddress()
	tx := common.NewTx(
		txID,
		ethAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"SWAP:ETH.ETH",
	)
	msg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: ethAddr,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(1000), // Full deposit used
			Out:     cosmos.NewUint(750),
		},
	}

	err := settleSwap(ctx, mgr, msg, "test settlement")
	c.Assert(err, IsNil)

	// Verify outbound was scheduled
	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 1)
	c.Assert(outItems[0].Coin.Amount.Equal(cosmos.NewUint(750)), Equals, true)
	c.Assert(outItems[0].Coin.Asset.Equals(common.ETHAsset), Equals, true)

	// Test 2: Settlement with refund of remaining deposit
	mgr.TxOutStore().ClearOutboundItems(ctx)
	msg2 := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: ethAddr,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(600), // Only 600 used
			Out:     cosmos.NewUint(550),
		},
	}

	err = settleSwap(ctx, mgr, msg2, "partial swap settlement")
	c.Assert(err, IsNil)

	// Verify both outbound and refund were scheduled
	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 2) // One for swap out, one for refund

	// Test 3: Settlement with aggregator
	mgr.TxOutStore().ClearOutboundItems(ctx)
	aggTargetLimit := cosmos.NewUint(100)
	msg3 := types.MsgSwap{
		Tx:                      tx,
		TargetAsset:             common.ETHAsset,
		Destination:             ethAddr,
		Aggregator:              "0xd31f7e39afECEc4855fecc51b693F9A0Cec49fd2",
		AggregatorTargetAddress: "0x1234567890123456789012345678901234567890",
		AggregatorTargetLimit:   &aggTargetLimit,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(1000),
			Out:     cosmos.NewUint(900),
		},
	}

	err = settleSwap(ctx, mgr, msg3, "swap with aggregator")
	c.Assert(err, IsNil)

	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 1)
	c.Assert(outItems[0].Aggregator, Equals, "0xd31f7e39afECEc4855fecc51b693F9A0Cec49fd2")
	c.Assert(outItems[0].AggregatorTargetAsset, Equals, "0x1234567890123456789012345678901234567890")

	// Test 4: Settlement with no outbound (all deposit refunded)
	mgr.TxOutStore().ClearOutboundItems(ctx)
	msg4 := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: ethAddr,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.ZeroUint(), // Nothing swapped
			Out:     cosmos.ZeroUint(),
		},
	}

	err = settleSwap(ctx, mgr, msg4, "no swap occurred")
	c.Assert(err, IsNil)

	// Only refund should be scheduled
	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 1)
	c.Assert(outItems[0].Coin.Amount.Equal(cosmos.NewUint(1000)), Equals, true)
	c.Assert(outItems[0].Coin.Asset.Equals(common.BTCAsset), Equals, true) // Refund in original asset

	// Test 5: Settlement with savers add memo (should not schedule outbound)
	mgr.TxOutStore().ClearOutboundItems(ctx)
	saversTx := common.NewTx(
		GetRandomTxHash(),
		ethAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000))},
		common.Gas{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))},
		"+:BTC/BTC", // Add liquidity memo (savers)
	)
	msg5 := types.MsgSwap{
		Tx:          saversTx,
		TargetAsset: common.BTCAsset,
		Destination: GetRandomBTCAddress(),
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(1000),
			Out:     cosmos.NewUint(900),
		},
	}

	err = settleSwap(ctx, mgr, msg5, "savers add settlement")
	c.Assert(err, IsNil)

	// No outbound should be scheduled for savers add
	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 0)

	// Test 6: Error handling - fail to remove from queue (should still succeed)
	keeper.failRemove = true
	err = settleSwap(ctx, mgr, msg, "test with removal failure")
	c.Assert(err, IsNil) // Should not fail, just log errors
	keeper.failRemove = false
}

func (HelperSuite) TestSettleSwapDoesNotDuplicateOutboundOnRetry(c *C) {
	ctx, mgr := setupManagerForTest(c)

	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"SWAP:ETH.ETH",
	)

	baseKeeper := mgr.Keeper()
	observedPubKey := GetRandomPubKey()
	seedSettleSwapRetryState(c, ctx, baseKeeper, tx, observedPubKey)
	mgr.K = &SettleSwapRetryVaultErrorKeeper{Keeper: baseKeeper}

	msg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: GetRandomETHAddress(),
		TradeTarget: cosmos.NewUint(1),
		SwapType:    types.SwapType_limit,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(600),
			Out:     cosmos.NewUint(550),
		},
	}

	c.Assert(settleSwap(ctx, mgr, msg, "expired limit order"), NotNil)
	c.Assert(settleSwap(ctx, mgr, msg, "expired limit order"), NotNil)

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(outItems), Equals, 0, Commentf("failed settlement must not leave a partial outbound behind"))
}

func (HelperSuite) TestSettleSwapRefundFromInactiveVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	txID := GetRandomTxHash()
	observedPubKey := GetRandomPubKey()

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable

	// Keeper returns an inactive vault with no coins (simulates churned-out vault)
	k := &SettleSwapTestKeeper{
		advSwapItems: make(map[string]types.MsgSwap),
		pools:        make(map[common.Asset]Pool),
		voter: ObservedTxVoter{
			Tx: ObservedTx{
				Tx: common.Tx{
					ID: txID,
				},
				ObservedPubKey: observedPubKey,
			},
		},
		overrideVault: true,
		vaultStatus:   InactiveVault,
		vaultCoins:    common.Coins{}, // empty — funds migrated away
	}
	k.pools[common.BTCAsset] = btcPool
	mgr.K = k
	mgr.txOutStore = NewTxStoreDummy()

	btcAddr := GetRandomBTCAddress()
	tx := common.NewTx(
		txID,
		btcAddr,
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"SWAP:ETH.ETH",
	)
	msg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: GetRandomETHAddress(),
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(600), // Only 600 used, 400 to refund
			Out:     cosmos.NewUint(550),
		},
	}

	err := settleSwap(ctx, mgr, msg, "expired limit order")
	c.Assert(err, IsNil)

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	// Expect 2 items: swap outbound + refund
	c.Assert(len(outItems), Equals, 2)

	// Find the refund item (the one in the original BTC asset)
	var refundItem *TxOutItem
	for i, item := range outItems {
		if item.Coin.Asset.Equals(common.BTCAsset) {
			refundItem = &outItems[i]
			break
		}
	}
	c.Assert(refundItem, NotNil)
	c.Assert(refundItem.Coin.Amount.Equal(cosmos.NewUint(400)), Equals, true)
	// VaultPubKey should be empty so prepareTxOutItem uses dynamic vault discovery
	c.Assert(refundItem.VaultPubKey.IsEmpty(), Equals, true)
}

// TestRefundTxSkipsObservedVaultWithoutEffectiveFunds reproduces the accounting
// race that caused the ETH vault mismatch.
//
// This unit test covers the scheduling/accounting side. It does not model L1
// wallet balance, signing, broadcast, or final outbound observation. The
// important behavior is not the exact chain amounts from mainnet, but the
// ordering:
//   - an inbound is observed from an active vault, so refundTx would normally
//     preselect that same observed vault for the refund
//   - the observed vault still has enough raw vault balance for the refund
//   - older outbounds assigned to that same vault are still pending, so the
//     effective available vault balance is lower than the raw vault balance
//   - refundTx must not force the refund back to that observed vault, because
//     doing so can schedule an outbound that later observes against an already
//     drained vault balance and hits SafeSub clamping.
//
// In this fixture the observed vault has 200 ETH in vault.Coins and the refund
// is only 169 ETH, so a raw vault balance check would pass. The same vault also
// has a pending 200 ETH outbound, so after pending-outbound deduction it has no
// effective ETH available for a new 169 ETH refund. The expected result is that
// refundTx clears the preselected observed vault and lets normal txout
// discovery pick the other active vault that actually has available ETH.
func (HelperSuite) TestRefundTxSkipsObservedVaultWithoutEffectiveFunds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	var err error

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// This is the vault that observed the inbound. Its vault balance
	// intentionally has enough ETH to cover the refund amount below. Without the
	// fix, refundTx would keep this vault selected because active vaults were
	// trusted without checking pending outbounds.
	observedVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	observedVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, observedVault), IsNil)

	// This vault represents the safe fallback that normal txout discovery should
	// choose after refundTx declines to force the refund from observedVault.
	availableVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	availableVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, availableVault), IsNil)

	// Simulate an older ETH outbound already assigned to observedVault but not
	// yet observed on L1. This mirrors the incident window: the vault balance
	// has not been debited yet, but those funds are already committed and must
	// be treated as unavailable for any newly-created refund. Whether the L1
	// wallet balance can actually sign/broadcast later is outside this unit
	// test. The bug being tested here is the bad vault preselection.
	pendingOut := NewTxOut(ctx.BlockHeight())
	pendingOut.TxArray = append(pendingOut.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: observedVault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
		},
		InHash: GetRandomTxHash(),
		Memo:   NewOutboundMemo(GetRandomTxHash()).String(),
	})
	c.Assert(mgr.Keeper().SetTxOut(ctx, pendingOut), IsNil)

	// Create the inbound that will be refunded. The refund amount is 169 ETH:
	// less than the observed vault's raw 200 ETH vault balance, but greater
	// than its effective available vault balance after the pending 200 ETH
	// outbound.
	txID := GetRandomTxHash()
	fromAddr := GetRandomETHAddress()
	var toAddr common.Address
	toAddr, err = observedVault.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(
		txID,
		fromAddr,
		toAddr,
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(169*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"SWAP:BTC.BTC",
	)
	observedTx := ObservedTx{
		Tx:             tx,
		ObservedPubKey: observedVault.PubKey,
		BlockHeight:    ctx.BlockHeight(),
	}
	voter := NewObservedTxVoter(txID, nil)
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// This should schedule exactly one refund outbound. The fix changes only the
	// source vault selection: refundTx should clear observedVault before calling
	// TryAddTxOutItem, allowing txout discovery to choose a vault with effective
	// available funds.
	c.Assert(refundTx(ctx, observedTx, mgr, CodeSwapFail, "incident simulation", ""), IsNil)

	// refundTx schedules through TryAddTxOutItem, which may delay the outbound.
	// The chosen txout height is stored on the inbound voter, so read that
	// height instead of assuming the refund landed in the current block.
	voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(voter.OutboundHeight > 0, Equals, true)
	var blockOut *TxOut
	blockOut, err = mgr.Keeper().GetTxOut(ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)

	// Fetch the generated refund item. The txout block can also contain the
	// older pending outbound seeded above, so identify this refund by its inbound
	// hash rather than assuming it is the only item in the block.
	var refundItem TxOutItem
	foundRefundItem := false
	for _, item := range blockOut.TxArray {
		if item.InHash.Equals(txID) {
			refundItem = item
			foundRefundItem = true
			break
		}
	}
	c.Assert(foundRefundItem, Equals, true)

	// The regression assertion: a raw vault balance check would have kept
	// observedVault, but the effective balance check must route the refund to
	// availableVault instead.
	c.Assert(refundItem.InHash.Equals(txID), Equals, true)
	c.Assert(refundItem.GetMemo(), Equals, NewRefundMemo(txID).String())
	c.Assert(refundItem.VaultPubKey.Equals(availableVault.PubKey), Equals, true)
}

func (HelperSuite) TestRefundTxKeepsInactiveObservedVaultWithExactRefundFunds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	var err error

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)

	// Inactive-vault refund recovery is intentionally pinned to the observed
	// vault when that vault can fund the total refund spend. For gas-asset
	// refunds the txout layer deducts MaxGas from the refund coin amount, so a
	// 100000 BTC refund with 10000 BTC MaxGas spends 90000 as value and 10000 as
	// gas. Requiring 110000 here would incorrectly reroute away from the
	// originating inactive vault.
	observedVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	observedVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100_000)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, observedVault), IsNil)

	// A larger active vault exists and would be selected by normal txout
	// discovery if refundTx cleared observedVault. The assertion below confirms
	// the inactive origin vault remains selected instead.
	availableVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	availableVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000_000)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, availableVault), IsNil)

	txID := GetRandomTxHash()
	fromAddr := GetRandomBTCAddress()
	var toAddr common.Address
	toAddr, err = observedVault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(
		txID,
		fromAddr,
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100_000))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
		"ADD:BTC.BTC",
	)
	observedTx := ObservedTx{
		Tx:             tx,
		ObservedPubKey: observedVault.PubKey,
		BlockHeight:    ctx.BlockHeight(),
	}
	voter := NewObservedTxVoter(txID, nil)
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	c.Assert(refundTx(ctx, observedTx, mgr, CodeSwapFail, "inactive vault", ""), IsNil)

	voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(voter.OutboundHeight > 0, Equals, true)
	var blockOut *TxOut
	blockOut, err = mgr.Keeper().GetTxOut(ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)

	var refundItem TxOutItem
	foundRefundItem := false
	for _, item := range blockOut.TxArray {
		if item.InHash.Equals(txID) {
			refundItem = item
			foundRefundItem = true
			break
		}
	}
	c.Assert(foundRefundItem, Equals, true)

	c.Assert(refundItem.VaultPubKey.Equals(observedVault.PubKey), Equals, true)
	c.Assert(refundItem.Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(refundItem.Coin.Amount.Add(refundItem.MaxGas[0].Amount).Equal(cosmos.NewUint(100_000)), Equals, true)
}

// TestRefundTxReroutePreventsObservedOutboundClamp extends the prevention test
// through outbound observation. It uses the fixed refundTx path to create the
// refund, then observes both L1 outbounds:
//   - the older outbound drains the original observed vault balance
//   - the refund outbound is observed from the replacement vault selected by
//     txout discovery
//   - no Vault.SubFundsWithClamping clamp happens and HaltSigningETH/HaltETHTrading stay unset
//
// This demonstrates the practical fix: once refundTx stops forcing the refund
// from the effectively-drained observed vault, the later L1 observation no
// longer creates the accounting gap reproduced in the outbound-handler test.
func (HelperSuite) TestRefundTxReroutePreventsObservedOutboundClamp(c *C) {
	// This test uses the real keeper-backed manager because it needs both sides
	// of the prevention story: refundTx scheduling and later outbound
	// observation. The companion outbound-handler test seeds a bad txout
	// directly; this one lets the fixed refundTx create the refund txout.
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	k := mgr.Keeper()
	var err error

	// A single active validator is enough to reach outbound observation
	// consensus in this fixture while still exercising the real
	// MsgObservedTxOut handler path.
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1_000_000 * common.One)
	na.Version = mgr.GetVersion().String()
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// Make ETH refundable and observable through the standard pool/gas path.
	// The exact pool depth is not important; it keeps fee handling from
	// short-circuiting the test.
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// The original observed vault starts with enough raw vault balance to cover
	// the refund. The bug was that refundTx used this raw vault balance and did
	// not account for pending outbounds already assigned to the same vault.
	observedVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	observedVault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	observedVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	c.Assert(k.SetVault(ctx, observedVault), IsNil)

	// This vault is the intended safe replacement. If refundTx clears the
	// observed vault, normal txout discovery should route the refund here.
	availableVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	availableVault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	availableVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500*common.One)),
	}
	c.Assert(k.SetVault(ctx, availableVault), IsNil)

	var observedVaultAddr common.Address
	observedVaultAddr, err = observedVault.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Keep the amounts aligned with the incident-path test:
	//   - drainAmount reduces observedVault to 16.60108653 ETH after gas
	//   - refundAmount is 169.23065786 ETH
	// If the refund stayed on observedVault, observing it after the drain would
	// hit the Vault.SubFundsWithClamping clamp. The fix should route it to availableVault.
	drainInHash := GetRandomTxHash()
	refundInHash := GetRandomTxHash()
	drainAmount := cosmos.NewUint(18_339_891_346)  // Leaves 16.60108653 ETH after 1 wei gas.
	refundAmount := cosmos.NewUint(16_923_065_786) // 169.23065786 ETH.
	l1Gas := cosmos.NewUint(1)

	// Seed the older pending outbound first. refundTx must treat this amount as
	// already committed from observedVault even though the vault balance has not
	// been debited yet.
	drainItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      drainInHash,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: observedVault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, drainAmount),
		Memo:        NewOutboundMemo(drainInHash).String(),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, l1Gas),
		},
	}
	txOut := NewTxOut(ctx.BlockHeight())
	txOut.TxArray = append(txOut.TxArray, drainItem)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	// Link the seeded older outbound to an inbound voter so that, when its L1
	// tx is later observed, CommonOutboundTxHandler treats it as a valid
	// scheduled outbound and writes the OutHash instead of slashing it.
	drainVoter := NewObservedTxVoter(drainInHash, nil)
	drainVoter.Height = ctx.BlockHeight()
	drainVoter.OutboundHeight = ctx.BlockHeight()
	drainVoter.Actions = []TxOutItem{drainItem}
	drainVoter.Tx = common.ObservedTx{
		Tx: common.Tx{
			ID:          drainInHash,
			Chain:       common.ETHChain,
			FromAddress: drainItem.ToAddress,
			ToAddress:   observedVaultAddr,
			Coins:       common.NewCoins(drainItem.Coin),
			Memo:        "SWAP:BTC.BTC",
		},
		ObservedPubKey: observedVault.PubKey,
		BlockHeight:    ctx.BlockHeight(),
	}
	k.SetObservedTxInVoter(ctx, drainVoter)

	// Create the inbound that will be refunded from observedVault. Its raw vault
	// balance is enough for the refund, but its effective vault balance after the
	// pending drainItem is not. The fixed refundTx should therefore clear
	// observedVault and let txout discovery choose availableVault.
	refundTxIn := common.NewTx(
		refundInHash,
		GetRandomETHAddress(),
		observedVaultAddr,
		common.Coins{common.NewCoin(common.ETHAsset, refundAmount)},
		common.Gas{common.NewCoin(common.ETHAsset, l1Gas)},
		"SWAP:BTC.BTC",
	)
	refundObservedTx := ObservedTx{
		Tx:             refundTxIn,
		ObservedPubKey: observedVault.PubKey,
		BlockHeight:    ctx.BlockHeight(),
	}
	refundVoter := NewObservedTxVoter(refundInHash, nil)
	refundVoter.Tx = refundObservedTx
	k.SetObservedTxInVoter(ctx, refundVoter)

	// This is the prevention point. The pending drainItem makes observedVault's
	// effective available vault balance insufficient, so fixed refundTx should
	// not force the refund from observedVault even though its raw vault balance
	// still looks large enough.
	c.Assert(refundTx(ctx, refundObservedTx, mgr, CodeSwapFail, "incident simulation", ""), IsNil)

	// Read back the outbound generated by refundTx. A delayed txout height is
	// possible, so use the height stored on the inbound voter.
	refundVoter, err = k.GetObservedTxInVoter(ctx, refundInHash)
	c.Assert(err, IsNil)
	refundOutboundHeight := ctx.BlockHeight()
	if refundVoter.OutboundHeight > refundOutboundHeight {
		refundOutboundHeight = refundVoter.OutboundHeight
	}
	var refundBlockOut *TxOut
	refundBlockOut, err = k.GetTxOut(ctx, refundOutboundHeight)
	c.Assert(err, IsNil)

	var refundItem TxOutItem
	foundRefundItem := false
	for _, item := range refundBlockOut.TxArray {
		if item.InHash.Equals(refundInHash) {
			refundItem = item
			foundRefundItem = true
			break
		}
	}
	c.Assert(foundRefundItem, Equals, true)
	c.Assert(refundItem.GetMemo(), Equals, NewRefundMemo(refundInHash).String())
	// The core fix assertion: the generated refund is scheduled from the
	// available replacement vault, not from the observed vault that has already
	// committed most of its vault balance to pending outbounds.
	c.Assert(refundItem.VaultPubKey.Equals(availableVault.PubKey), Equals, true)

	handler := NewObservedTxOutHandler(mgr)
	observeOutbound := func(observeCtx cosmos.Context, item TxOutItem) {
		// Unit-test stand-in for "the L1 tx was broadcast and then observed".
		// The from address is derived from the item vault so MsgObservedTxOut
		// validates as an outbound from the selected vault.
		var vault Vault
		vault, err = k.GetVault(observeCtx, item.VaultPubKey)
		c.Assert(err, IsNil)
		var fromAddr common.Address
		fromAddr, err = vault.GetAddress(item.Chain)
		c.Assert(err, IsNil)

		observedTx := common.NewObservedTx(common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       item.Chain,
			FromAddress: fromAddr,
			ToAddress:   item.ToAddress,
			Coins:       common.NewCoins(item.Coin),
			Gas: common.Gas{
				common.NewCoin(item.Chain.GetGasAsset(), l1Gas),
			},
			Memo: item.Memo,
		}, observeCtx.BlockHeight(), item.VaultPubKey, observeCtx.BlockHeight())

		// Use the real outbound observation handler to run consensus, outbound
		// matching, gas accounting, clamp detection, and vault balance debit.
		msg := NewMsgObservedTxOut(ObservedTxs{observedTx}, na.NodeAddress)
		_, err = handler.handle(observeCtx, *msg)
		c.Assert(err, IsNil)
	}

	// Observe the older outbound first. This reproduces the live ordering where
	// another L1 outbound drained the observed vault before the refund was
	// observed.
	observeOutbound(ctx, drainItem)
	var vaultAfterDrain Vault
	vaultAfterDrain, err = k.GetVault(ctx, observedVault.PubKey)
	c.Assert(err, IsNil)
	// 200 ETH vault balance - 183.39891346 ETH drain - 1 wei gas.
	// The observed vault is now below the refund amount, but the refund is no
	// longer assigned to this vault.
	c.Assert(vaultAfterDrain.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1_660_108_653)), Equals, true)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	// Draining the observed vault is not itself a clamp. The chain should still
	// be unhalted before the rerouted refund is observed.
	c.Assert(haltSigning <= 0, Equals, true)
	c.Assert(haltTrading <= 0, Equals, true)

	// Observe the refund after the drain. If refundTx had scheduled this from
	// observedVault, this is where SafeSub would clamp and the incident-path test
	// would halt. Since it was rerouted to availableVault, it should account
	// normally.
	ctx = ctx.WithBlockHeight(refundOutboundHeight)
	if ctx.BlockHeight() <= 100 {
		ctx = ctx.WithBlockHeight(101)
	}
	observeOutbound(ctx, refundItem)

	haltSigning, err = k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	haltTrading, err = k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	// The key end-to-end assertion: observing the refund produced by fixed
	// refundTx does not trigger MR 4802's clamp detector.
	c.Assert(haltSigning <= 0, Equals, true)
	c.Assert(haltTrading <= 0, Equals, true)

	var vaultAfterRefund Vault
	vaultAfterRefund, err = k.GetVault(ctx, availableVault.PubKey)
	c.Assert(err, IsNil)
	// The replacement vault had enough vault balance, so after refund amount and
	// gas are debited it remains positive rather than being clamped to zero.
	c.Assert(vaultAfterRefund.GetCoin(common.ETHAsset).Amount.IsZero(), Equals, false)
}

// TestHandleRefundOutboundWithDrainedVaultBalanceRecreatesIncidentPath models
// the THORNode-side lifecycle of the ETH refund incident.
//
// A unit test cannot broadcast a real Ethereum transaction. Instead, the L1
// broadcast is represented by a scheduled TxOutItem and then a later
// MsgObservedTxOut carrying the L1 tx hash. That is the point where THORNode
// learns that the L1 wallet balance was sufficient and the transaction really
// left the wallet.
//
// The fixture intentionally seeds the bad refund TxOutItem directly rather than
// calling refundTx. With the refundTx fix in helpers.go, this bad preselection
// should no longer be created for new refunds. Seeding it here simulates a
// pre-fix or already-in-flight outbound and verifies the later observation path:
//   - vault balance starts at 200 ETH
//   - a 169.23065786 ETH refund is already scheduled from the observed vault
//   - another outbound is observed first and drains the vault balance to about
//     16.60108653 ETH
//   - the refund L1 tx is observed later
//   - addGasFees deducts gas, then Vault.SubFundsWithClamping tries to debit 169.23065786
//     ETH from only 16.60108652 ETH
//   - SafeSub clamps the vault balance to zero
//   - the MR 4802 clamp detector sets HaltSigningETH and HaltETHTrading so the
//     accounting gap is surfaced instead of silently persisting
func (HelperSuite) TestHandleRefundOutboundWithDrainedVaultBalanceRecreatesIncidentPath(c *C) {
	// Use the real manager/keeper stack here instead of the lightweight dummy
	// keeper used by some handler tests. This test needs persistent TxOut,
	// ObservedTxVoter, vault, pool, and mimir state so it can exercise the same
	// accounting path that live observations use.
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	k := mgr.Keeper()
	var err error

	// One active node is enough for final outbound consensus in this unit test:
	// HasSuperMajority(1, 1) is true. That keeps the fixture small while still
	// going through MsgObservedTxOut -> processTxOutAttestation ->
	// handleObservedTxOutQuorum.
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1_000_000 * common.One)
	na.Version = mgr.GetVersion().String()
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// Make ETH a known available pool so outbound handling can process gas and
	// fees normally. The exact pool depths are not the incident focus; they just
	// keep the handler on the standard successful accounting path.
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Start from a calm internal state: the vault balance says the observed vault
	// has 200 ETH. The test does not store L1 wallet balance, but the later
	// observed refund tx represents that the L1 wallet balance was high enough
	// to broadcast the refund.
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	var vaultAddr common.Address
	vaultAddr, err = vault.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Use values close to the live incident:
	//   - refundAmount is the 169.23065786 ETH refund size
	//   - drainAmount is chosen so that, after a 1 wei gas debit, vault balance
	//     becomes 16.60108653 ETH
	//   - the refund observation then pays another 1 wei gas first, leaving
	//     16.60108652 ETH available for the 169.23065786 ETH coin debit
	drainInHash := GetRandomTxHash()
	refundInHash := GetRandomTxHash()
	drainAmount := cosmos.NewUint(18_339_891_346)  // Leaves 16.60108653 ETH after 1 wei gas.
	refundAmount := cosmos.NewUint(16_923_065_786) // 169.23065786 ETH.
	l1Gas := cosmos.NewUint(1)

	// This first scheduled outbound stands in for the other vault activity that
	// was observed before the refund. Once observed, it lowers the vault balance
	// enough that the refund can no longer be fully debited from accounting.
	drainItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      drainInHash,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, drainAmount),
		Memo:        NewOutboundMemo(drainInHash).String(),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, l1Gas),
		},
	}

	// This is the bad, pre-fix scheduled refund. It represents the state after
	// refundTx wrongly forced the observed vault and Bifrost had an outbound
	// ready to sign/broadcast from that vault.
	refundItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      refundInHash,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, refundAmount),
		Memo:        NewRefundMemo(refundInHash).String(),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, l1Gas),
		},
	}

	txOut := NewTxOut(ctx.BlockHeight())
	txOut.TxArray = append(txOut.TxArray, drainItem, refundItem)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	// The inbound voters link observed L1 outbounds back to their scheduled
	// actions. CommonOutboundTxHandler uses these actions to mark each TxOutItem
	// with the observed L1 tx hash. This matters because the live issue was not
	// just a vault balance subtraction. The outbound also needed to be recognized
	// as a valid, scheduled tx rather than a malicious extra send.
	for _, item := range []TxOutItem{drainItem, refundItem} {
		inboundVoter := NewObservedTxVoter(item.InHash, nil)
		inboundVoter.Height = ctx.BlockHeight()
		inboundVoter.OutboundHeight = ctx.BlockHeight()
		inboundVoter.Actions = []TxOutItem{item}
		// The inbound tx details are only used here to give the outbound handler
		// an action list and correlation point. The outbound observation below is
		// the simulated L1 transaction that actually spends from the vault.
		inboundVoter.Tx = common.ObservedTx{
			Tx: common.Tx{
				ID:          item.InHash,
				Chain:       common.ETHChain,
				FromAddress: item.ToAddress,
				ToAddress:   vaultAddr,
				Coins:       common.NewCoins(item.Coin),
				Memo:        "SWAP:BTC.BTC",
			},
			ObservedPubKey: vault.PubKey,
			BlockHeight:    ctx.BlockHeight(),
		}
		k.SetObservedTxInVoter(ctx, inboundVoter)
	}

	handler := NewObservedTxOutHandler(mgr)
	observeOutbound := func(observeCtx cosmos.Context, item TxOutItem) common.TxID {
		// This helper is the unit-test stand-in for "the ETH tx was broadcast on
		// L1 and later observed by nodes". A fresh outHash is the L1 tx hash that
		// CommonOutboundTxHandler will write into the scheduled TxOutItem.
		outHash := GetRandomTxHash()
		observedTx := common.NewObservedTx(common.Tx{
			ID:          outHash,
			Chain:       common.ETHChain,
			FromAddress: vaultAddr,
			ToAddress:   item.ToAddress,
			Coins:       common.NewCoins(item.Coin),
			Gas: common.Gas{
				common.NewCoin(common.ETHAsset, l1Gas),
			},
			Memo: item.Memo,
		}, observeCtx.BlockHeight(), vault.PubKey, observeCtx.BlockHeight())

		// Calling the handler, instead of directly calling Vault.SubFundsWithClamping, keeps
		// the test on the real path: consensus processing, outbound matching,
		// gas accounting, clamp detection, coin debit, and txout OutHash update.
		msg := NewMsgObservedTxOut(ObservedTxs{observedTx}, na.NodeAddress)
		_, err = handler.handle(observeCtx, *msg)
		c.Assert(err, IsNil)
		return outHash
	}

	// First L1 observation: another outbound from the same vault is observed
	// before the refund. This debits the vault balance and creates the live
	// incident shape: the refund is still pending, but the vault balance is now
	// far below the refund amount.
	drainOutHash := observeOutbound(ctx, drainItem)

	var vaultAfterDrain Vault
	vaultAfterDrain, err = k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	// 200 ETH vault balance - 183.39891346 ETH drain - 1 wei gas
	// = 16.60108653 ETH. This is still positive, so no clamp or signing/trading
	// halt should happen on the first outbound observation.
	c.Assert(vaultAfterDrain.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1_660_108_653)), Equals, true)
	// The refund is still pending, but the vault balance is now much lower than
	// the refund amount. This is the fragile in-flight state from the incident.
	c.Assert(vaultAfterDrain.GetCoin(common.ETHAsset).Amount.LT(refundAmount), Equals, true)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	// No deficit has been recorded yet. A low vault balance while the refund is
	// still pending is not enough by itself. The accounting gap surfaces when
	// the refund L1 tx is later observed and accounting tries to debit it.
	c.Assert(haltSigning <= 0, Equals, true)
	c.Assert(haltTrading <= 0, Equals, true)

	// Second L1 observation: the refund has now really left the L1 wallet and
	// is observed by THORNode. This is where addGasFees and debitVault run
	// against an already-drained vault balance.
	//
	// This call is also where the test models the "extra funds in the L1 wallet"
	// condition from the incident. The unit test does not track an unobserved
	// inbound that temporarily increased the external wallet; observing the
	// refund outbound here is the proof that the L1 wallet balance was sufficient
	// to sign and broadcast the tx even though the vault balance was already too
	// low for THORNode accounting to debit it cleanly.
	ctx = ctx.WithBlockHeight(101)
	var vaultBeforeRefund Vault
	vaultBeforeRefund, err = k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	// Capture the pre-refund vault balance so the test can explicitly calculate
	// the final accounting gap recorded as vault deficit.
	vaultBalanceBeforeRefund := vaultBeforeRefund.GetCoin(common.ETHAsset).Amount

	refundOutHash := observeOutbound(ctx, refundItem)

	var vaultAfterRefund Vault
	vaultAfterRefund, err = k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	// The tracked coin balance is drained, and the missed debit is recorded as
	// vault deficit rather than disappearing as phantom credit.
	c.Assert(vaultAfterRefund.GetCoin(common.ETHAsset).Amount.IsZero(), Equals, true)

	// addGasFees debits gas before the coin debit. The accounting gap is the
	// refund coin amount that debitVault could not subtract from settled coins
	// because only the remaining post-gas vault balance was available.
	availableForRefundCoinDebit := common.SafeSub(vaultBalanceBeforeRefund, l1Gas)
	accountingGap := common.SafeSub(refundAmount, availableForRefundCoinDebit)
	c.Assert(accountingGap.Equal(cosmos.NewUint(15_262_957_134)), Equals, true)
	c.Assert(vaultAfterRefund.GetDeficit(common.ETHAsset).Amount.Equal(accountingGap), Equals, true)

	// Once the deficit is recorded, signing and trading are halted while
	// Bifrost can continue observing the chain.
	haltSigning, err = k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight())
	haltTrading, err = k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight())

	// Both scheduled outbounds were matched to observed L1 tx hashes. This proves
	// the test did not create an unrecognized/malicious outbound path. It followed
	// normal outbound observation and accounting for scheduled txouts.
	var txOutAfter *TxOut
	txOutAfter, err = k.GetTxOut(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(txOutAfter.TxArray, HasLen, 2)
	c.Assert(txOutAfter.TxArray[0].OutHash.Equals(drainOutHash), Equals, true)
	c.Assert(txOutAfter.TxArray[1].OutHash.Equals(refundOutHash), Equals, true)
}

func (s *HelperSuite) TestSettleSwapLimitSwapBasic(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test: Basic limit swap settlement should work without errors
	txID := GetRandomTxHash()
	currentBlockHeight := int64(100)
	ctx = ctx.WithBlockHeight(currentBlockHeight)

	ethAddr := GetRandomETHAddress()
	tx := common.NewTx(
		txID,
		ethAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	limitSwapMsg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Destination: GetRandomBTCAddress(),
		SwapType:    types.SwapType_limit,
		TradeTarget: cosmos.NewUint(5000000), // 0.05 BTC
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1 * common.One),
			In:      cosmos.NewUint(1 * common.One),
			Out:     cosmos.NewUint(5000000),
		},
	}

	// Settle the limit swap
	err := settleSwap(ctx, mgr, limitSwapMsg, "limit swap expired")
	c.Assert(err, IsNil)

	// Verify that settleSwap correctly handled the limit swap
	// The specific event emission logic is tested separately
	c.Assert(limitSwapMsg.IsLimitSwap(), Equals, true)
}

func (s *HelperSuite) TestSettleSwapMarketSwapBasic(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test: Market swap settlement should work without errors
	txID := GetRandomTxHash()
	currentBlockHeight := int64(100)
	ctx = ctx.WithBlockHeight(currentBlockHeight)

	ethAddr := GetRandomETHAddress()
	tx := common.NewTx(
		txID,
		ethAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	marketSwapMsg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Destination: GetRandomBTCAddress(),
		SwapType:    types.SwapType_market, // Market swap, not limit
		State: &types.SwapState{
			Deposit: cosmos.NewUint(1 * common.One),
			In:      cosmos.NewUint(1 * common.One),
			Out:     cosmos.NewUint(5000000),
		},
	}

	// Settle the market swap
	err := settleSwap(ctx, mgr, marketSwapMsg, "market swap completed")
	c.Assert(err, IsNil)

	// Verify that settleSwap correctly handled the market swap
	c.Assert(marketSwapMsg.IsLimitSwap(), Equals, false)
}

func (s *HelperSuite) TestSettleSwapLimitSwapWithDifferentReasons(c *C) {
	ctx, mgr := setupManagerForTest(c)

	currentBlockHeight := int64(150)
	ctx = ctx.WithBlockHeight(currentBlockHeight)

	ethAddr := GetRandomETHAddress()

	testCases := []struct {
		reason string
		txID   common.TxID
	}{
		{"limit swap completed", GetRandomTxHash()},
		{"limit swap cancelled", GetRandomTxHash()},
		{"limit swap expired", GetRandomTxHash()},
		{"limit swap failed", GetRandomTxHash()},
	}

	for i, testCase := range testCases {
		tx := common.NewTx(
			testCase.txID,
			ethAddr,
			GetRandomETHAddress(),
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
			"swap:BTC.BTC",
		)

		limitSwapMsg := types.MsgSwap{
			Tx:          tx,
			TargetAsset: common.BTCAsset,
			Destination: GetRandomBTCAddress(),
			SwapType:    types.SwapType_limit,
			TradeTarget: cosmos.NewUint(5000000),
			State: &types.SwapState{
				Deposit: cosmos.NewUint(1 * common.One),
				In:      cosmos.NewUint(1 * common.One),
				Out:     cosmos.NewUint(5000000),
			},
		}

		// Settle the limit swap with different reason
		err := settleSwap(ctx, mgr, limitSwapMsg, testCase.reason)
		c.Assert(err, IsNil, Commentf("Test case %d failed: %s", i, testCase.reason))

		// Verify it's still recognized as a limit swap
		c.Assert(limitSwapMsg.IsLimitSwap(), Equals, true)
	}
}

func (s *HelperSuite) TestSettleSwapStreamingLimitSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	// Test: Streaming limit swap (quantity > 1) should settle correctly and
	// still schedule the final outbound once the full swap is complete.
	txID := GetRandomTxHash()
	currentBlockHeight := int64(200)
	ctx = ctx.WithBlockHeight(currentBlockHeight)

	ethAddr := GetRandomETHAddress()
	tx := common.NewTx(
		txID,
		ethAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:ETH.ETH",
	)

	streamingLimitSwapMsg := types.MsgSwap{
		Tx:          tx,
		TargetAsset: common.ETHAsset,
		Destination: ethAddr,
		SwapType:    types.SwapType_limit,
		TradeTarget: cosmos.NewUint(50000000),
		State: &types.SwapState{
			Quantity: 10, // Streaming swap with 10 sub-swaps
			Count:    10, // All sub-swaps completed
			Deposit:  cosmos.NewUint(10 * common.One),
			In:       cosmos.NewUint(10 * common.One),
			Out:      cosmos.NewUint(50000000),
		},
	}

	// Settle the streaming limit swap
	err := settleSwap(ctx, mgr, streamingLimitSwapMsg, "streaming limit swap completed")
	c.Assert(err, IsNil)

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].Coin.Asset.Equals(common.ETHAsset), Equals, true)
	c.Assert(outItems[0].Coin.Amount.Equal(cosmos.NewUint(50000000)), Equals, true)
	c.Assert(outItems[0].ToAddress.Equals(streamingLimitSwapMsg.Destination), Equals, true)

	// Verify it's recognized as a limit swap
	c.Assert(streamingLimitSwapMsg.IsLimitSwap(), Equals, true)
	c.Assert(streamingLimitSwapMsg.State.Quantity, Equals, uint64(10))
}

func (s *HelperSuite) TestLeadingZeros(c *C) {
	// Test case where string length is less than the given length
	// The function should pad the string with leading zeros.
	c.Assert(leadingZeros(5, "123"), Equals, "00123")

	// Test case where string length is greater than the given length
	// The function should truncate the string to the given length.
	c.Assert(leadingZeros(2, "12345"), Equals, "12")

	// Test case where string length is equal to the given length
	// The function should return the string as it is.
	c.Assert(leadingZeros(5, "12345"), Equals, "12345")

	// Test case where string is empty
	// The function should return a string with the given length filled with zeros.
	c.Assert(leadingZeros(5, ""), Equals, "00000")

	// Test case where length is zero
	// The function should return an empty string regardless of the input string.
	c.Assert(leadingZeros(0, "12345"), Equals, "")
}

func (s *HelperSuite) TestApplyMemolessOutboundLogic(c *C) {
	// Generate random transaction hashes for testing
	txHash1 := GetRandomTxHash()
	txHash2 := GetRandomTxHash()
	txHash3 := GetRandomTxHash()
	txHash4 := GetRandomTxHash()
	txHash5 := GetRandomTxHash()
	txHash6 := GetRandomTxHash()
	refundHash := GetRandomTxHash()

	tests := []struct {
		name                   string
		memo                   string
		originalMemo           string
		aggregator             string
		enableMemolessOutbound int64
		expectedMemo           string
		expectedOriginalMemo   string
	}{
		// Memoless enabled scenarios
		{
			name:                   "basic memo clearing - regular memo gets cleared",
			memo:                   fmt.Sprintf("OUT:%s", txHash1),
			originalMemo:           "",
			aggregator:             "",
			enableMemolessOutbound: 1,
			expectedMemo:           "",
			expectedOriginalMemo:   fmt.Sprintf("OUT:%s", txHash1),
		},
		{
			name:                   "MIGRATE prefix preservation",
			memo:                   "MIGRATE:123",
			originalMemo:           "",
			aggregator:             "",
			enableMemolessOutbound: 1,
			expectedMemo:           "MIGRATE:123",
			expectedOriginalMemo:   "MIGRATE:123",
		},
		{
			name:                   "RAGNAROK prefix preservation",
			memo:                   "RAGNAROK:456",
			originalMemo:           "",
			aggregator:             "",
			enableMemolessOutbound: 1,
			expectedMemo:           "RAGNAROK:456",
			expectedOriginalMemo:   "RAGNAROK:456",
		},
		{
			name:                   "aggregator preservation",
			memo:                   fmt.Sprintf("OUT:%s", txHash2),
			originalMemo:           "",
			aggregator:             "0x123",
			enableMemolessOutbound: 1,
			expectedMemo:           fmt.Sprintf("OUT:%s", txHash2),
			expectedOriginalMemo:   fmt.Sprintf("OUT:%s", txHash2),
		},
		{
			name:                   "passthrough data preservation",
			memo:                   fmt.Sprintf("OUT:%s|extra|data", txHash3),
			originalMemo:           "",
			aggregator:             "",
			enableMemolessOutbound: 1,
			expectedMemo:           fmt.Sprintf("OUT:%s|extra|data", txHash3),
			expectedOriginalMemo:   fmt.Sprintf("OUT:%s|extra|data", txHash3),
		},
		{
			name:                   "original memo persistence - not overwritten",
			memo:                   fmt.Sprintf("OUT:%s", txHash4),
			originalMemo:           fmt.Sprintf("OUT:%s", txHash5),
			aggregator:             "",
			enableMemolessOutbound: 1,
			expectedMemo:           "",
			expectedOriginalMemo:   fmt.Sprintf("OUT:%s", txHash5),
		},
		// Memoless disabled scenarios
		{
			name:                   "memo restoration when disabled",
			memo:                   "",
			originalMemo:           fmt.Sprintf("OUT:%s", txHash6),
			aggregator:             "",
			enableMemolessOutbound: 0,
			expectedMemo:           fmt.Sprintf("OUT:%s", txHash6),
			expectedOriginalMemo:   fmt.Sprintf("OUT:%s", txHash6),
		},
		{
			name:                   "no restoration when original memo empty",
			memo:                   fmt.Sprintf("REFUND:%s", refundHash),
			originalMemo:           "",
			aggregator:             "",
			enableMemolessOutbound: 0,
			expectedMemo:           fmt.Sprintf("REFUND:%s", refundHash),
			expectedOriginalMemo:   fmt.Sprintf("REFUND:%s", refundHash),
		},
	}

	for _, tt := range tests {
		c.Logf("Running test: %s", tt.name)

		// Create TxOutItem with test data
		toi := TxOutItem{
			Memo:         tt.memo,
			OriginalMemo: tt.originalMemo,
			Aggregator:   tt.aggregator,
		}

		// Apply the memoless outbound logic
		applyMemolessOutboundLogic(GetCurrentVersion(), &toi, tt.enableMemolessOutbound)

		// Verify results
		c.Assert(toi.Memo, Equals, tt.expectedMemo, Commentf("Test: %s - Memo mismatch", tt.name))
		c.Assert(toi.OriginalMemo, Equals, tt.expectedOriginalMemo, Commentf("Test: %s - OriginalMemo mismatch", tt.name))
	}
}

func (s *HelperSuite) TestGetMaxSwapQuantityRapidInterval(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	k.SetMimir(ctx, "L1SlipMinBps", 100)             // 1%
	k.SetMimir(ctx, "StreamingSwapMaxLength", 14400) // max non-native streaming length

	// interval=0 (rapid) should not force max quantity to zero.
	quantity, err := getMaxSwapQuantity(ctx, mgr, common.ETHAsset, common.BTCAsset, StreamingSwap{
		Interval: 0,
		Deposit:  cosmos.NewUint(1000 * common.One),
	}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(quantity, Equals, uint64(1000), Commentf("rapid interval should preserve non-zero quantity cap"))

	// interval=0 should still be bounded by max length constraints using effective interval=1.
	quantity, err = getMaxSwapQuantity(ctx, mgr, common.ETHAsset, common.BTCAsset, StreamingSwap{
		Interval: 0,
		Deposit:  cosmos.NewUint(50000 * common.One),
	}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(quantity, Equals, uint64(14400), Commentf("rapid interval should respect max length cap"))
}

// TestGetMaxSwapQuantityHonorsDynamicFloor verifies that when a whitelisted
// thorname has a dynamic-fee floor for the pair, the streaming sub-swap size
// uses that floor rather than the static L1SlipMinBps — so users don't get
// carved into sub-swaps smaller than what the floor will charge anyway.
func (s *HelperSuite) TestGetMaxSwapQuantityHonorsDynamicFloor(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	k.SetMimir(ctx, "L1SlipMinBps", 10) // static floor 10 bps
	k.SetMimir(ctx, "StreamingSwapMaxLength", 14400)
	setDynamicFeeTestMimirs(ctx, k)

	registerThorname(ctx, k, "mytn", 1)
	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname:   "mytn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 50,
	}), IsNil)

	affiliates := []string{"mytn"}
	affBps := []cosmos.Uint{cosmos.NewUint(100)}
	deposit := cosmos.NewUint(1000 * common.One)

	// No affiliate context: uses static 10 bps → larger quantity allowed.
	withoutDyn, err := getMaxSwapQuantity(ctx, mgr, common.RuneAsset(), common.BTCAsset, StreamingSwap{
		Interval: 1,
		Deposit:  deposit,
	}, nil, nil)
	c.Assert(err, IsNil)

	// With affiliate context: dynamic 50 bps floor → fewer, larger sub-swaps.
	withDyn, err := getMaxSwapQuantity(ctx, mgr, common.RuneAsset(), common.BTCAsset, StreamingSwap{
		Interval: 1,
		Deposit:  deposit,
	}, affiliates, affBps)
	c.Assert(err, IsNil)

	// 50 bps is 5x the static 10 bps floor, so the dynamic path must allow
	// roughly 1/5 the quantity. Exact ratio: use GT/LT rather than equality
	// to stay robust against future changes to pool math.
	c.Assert(withDyn < withoutDyn, Equals, true, Commentf("dynamic floor should yield fewer sub-swaps than static floor"))
	c.Assert(withDyn*5, Equals, withoutDyn, Commentf("dynamic 50bps is 5x the static 10bps → 1/5 the sub-swaps"))
}

func (s *HelperSuite) TestIsStableToStable(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	busd, _ := common.NewAsset("ETH.BUSD-BD1")
	usdc, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")

	// Set up pools
	for _, asset := range []common.Asset{busd, usdc} {
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Decimals = 8
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}

	// No anchors configured — should return false
	c.Assert(isStableToStable(ctx, k, busd, usdc), Equals, false)

	// Enable both as TOR anchors
	k.SetMimir(ctx, "TorAnchor-ETH-BUSD-BD1", 1)
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)

	// Both are anchors — should return true
	c.Assert(isStableToStable(ctx, k, busd, usdc), Equals, true)

	// One stable, one non-stable — should return false
	c.Assert(isStableToStable(ctx, k, busd, common.BTCAsset), Equals, false)
	c.Assert(isStableToStable(ctx, k, common.ETHAsset, usdc), Equals, false)

	// RUNE to stable — should return false
	c.Assert(isStableToStable(ctx, k, common.RuneAsset(), usdc), Equals, false)

	// Synth stablecoin should also match via GetLayer1Asset
	synthBusd, _ := common.NewAsset("ETH/BUSD-BD1")
	synthUsdc, _ := common.NewAsset("ETH/USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(isStableToStable(ctx, k, synthBusd, synthUsdc), Equals, true)
}

func (s *HelperSuite) TestGetMinSlipBpsStableOverride(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	busd, _ := common.NewAsset("ETH.BUSD-BD1")

	// Set up pool
	pool := NewPool()
	pool.Asset = busd
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Set L1SlipMinBps to 10
	k.SetMimir(ctx, "L1SlipMinBps", 10)

	// Without stable override, should return L1 min bps
	result := getMinSlipBps(ctx, k, common.NoAddress, busd, false, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(10))

	// With stable override but StableSlipMinBps=0, should fall through to L1
	result = getMinSlipBps(ctx, k, common.NoAddress, busd, true, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(10))

	// Set StableSlipMinBps to 3
	k.SetMimir(ctx, "StableSlipMinBps", 3)

	// With stable override and non-zero StableSlipMinBps, should return stable value
	result = getMinSlipBps(ctx, k, common.NoAddress, busd, true, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(3))

	// Without stable override, should still return L1 value
	result = getMinSlipBps(ctx, k, common.NoAddress, busd, false, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(10))
}

func (s *HelperSuite) TestGetMinSlipBpsWasmArbContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	busd, _ := common.NewAsset("ETH.BUSD-BD1")
	k.SetMimir(ctx, "L1SlipMinBps", 25)

	wasmArbAddr, err := common.NewAddress("thor1n5a08r0zvmqca39ka2tgwlkjy9ugalutk7fjpzptfppqcccnat2ska5t4g")
	c.Assert(err, IsNil)

	// Default WasmArbSlipMinBps should be 10
	result := getMinSlipBps(ctx, k, wasmArbAddr, busd, false, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(10))

	// Operational mimir override should take effect
	k.SetMimir(ctx, "WasmArbSlipMinBps", 7)
	result = getMinSlipBps(ctx, k, wasmArbAddr, busd, false, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(7))

	// Non-arb addresses still use the asset-class min slip
	result = getMinSlipBps(ctx, k, common.NoAddress, common.Asset{}, false, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	c.Assert(result.Uint64(), Equals, uint64(25))
}

// TestGetMinSlipBpsDynamicOverridesStable verifies that an active dynamic fee
// for a whitelisted thorname takes precedence over the stable-override.
func (s *HelperSuite) TestGetMinSlipBpsDynamicOverridesStable(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	btc := common.BTCAsset
	pool := NewPool()
	pool.Asset = btc
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)

	k.SetMimir(ctx, "L1SlipMinBps", 10)
	k.SetMimir(ctx, "StableSlipMinBps", 3)
	setDynamicFeeTestMimirs(ctx, k)

	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, ok := dynamicFeePairKey(btc, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "mytn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 25,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	dfm := newDynamicFeeMgr(k)
	affiliates := []string{"mytn"}
	affBps := []cosmos.Uint{cosmos.NewUint(100)}

	// stableOverride=true, dynamic record present → dynamic wins (25) over stable (3).
	result := getMinSlipBps(ctx, k, common.NoAddress, btc, true, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(25))

	// stableOverride=false, dynamic record present → dynamic wins (25) over L1 default (10).
	result = getMinSlipBps(ctx, k, common.NoAddress, btc, false, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(25))

	// Clear dynamic record → dynamic floor still applies for the active thorname.
	c.Assert(k.DeleteDynamicFeeRecord(ctx, "mytn", pairA, pairB), IsNil)
	result = getMinSlipBps(ctx, k, common.NoAddress, btc, true, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(1))

	// No dynamic record, no stable override → dynamic floor (1) for the active thorname.
	result = getMinSlipBps(ctx, k, common.NoAddress, btc, false, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(1))
}

// TestGetMinSlipBpsNonL1IgnoresDynamic verifies that synth, trade and derived
// assets skip the dynamic-fee branch entirely: even when a non-nil
// DynamicFeeManager and non-empty affiliates are supplied, the result must
// come from the asset-type mimir (SynthSlipMinBps / TradeAccountsSlipMinBps /
// DerivedSlipMinBps), not from any DynamicFeeRecord that happens to exist.
func (s *HelperSuite) TestGetMinSlipBpsNonL1IgnoresDynamic(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	// Distinct mimir values so the branch taken is identifiable from the result.
	k.SetMimir(ctx, "L1SlipMinBps", 10)
	k.SetMimir(ctx, "SynthSlipMinBps", 20)
	k.SetMimir(ctx, "TradeAccountsSlipMinBps", 30)
	k.SetMimir(ctx, "DerivedSlipMinBps", 40)
	setDynamicFeeTestMimirs(ctx, k)

	registerThorname(ctx, k, "mytn", 1)

	// Dynamic record for (BTC, RUNE). If any non-L1 branch incorrectly fell
	// through to the default case, this would be returned instead of the
	// asset-type mimir.
	btc := common.BTCAsset
	pairA, pairB, ok := dynamicFeePairKey(btc, common.RuneAsset())
	c.Assert(ok, Equals, true)
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname:   "mytn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 77,
	}), IsNil)

	dfm := newDynamicFeeMgr(k)
	affiliates := []string{"mytn"}
	affBps := []cosmos.Uint{cosmos.NewUint(100)}

	synthBTC, err := common.NewAsset("BTC/BTC")
	c.Assert(err, IsNil)
	tradeBTC, err := common.NewAsset("BTC~BTC")
	c.Assert(err, IsNil)
	derivedBTC, err := common.NewAsset("THOR.BTC")
	c.Assert(err, IsNil)

	// Sanity: the assets classify as the expected types.
	c.Assert(synthBTC.IsSyntheticAsset(), Equals, true)
	c.Assert(tradeBTC.IsTradeAsset(), Equals, true)
	c.Assert(derivedBTC.IsDerivedAsset(), Equals, true)

	// For each non-L1 asset, a non-nil dfm with affiliates must still produce
	// the asset-type mimir value (not the dynamic 77 bps).
	result := getMinSlipBps(ctx, k, common.NoAddress, synthBTC, false, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(20))

	result = getMinSlipBps(ctx, k, common.NoAddress, tradeBTC, false, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(30))

	result = getMinSlipBps(ctx, k, common.NoAddress, derivedBTC, false, affiliates, affBps, btc, common.RuneAsset(), dfm)
	c.Assert(result.Uint64(), Equals, uint64(40))
}

func (s *HelperSuite) TestEmitEndBlockTelemetry(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	// pool with liquidity, so the pool loop actually runs
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// active node
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000 * common.One)
	na.Version = "3.0.0"
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// active asgard holding the pool asset
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// chain observation state + network fee
	c.Assert(k.SetLastChainHeight(ctx, common.BTCChain, 900000), IsNil)
	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain: common.BTCChain, TransactionSize: 1000, TransactionFeeRate: 10,
	}), IsNil)

	// in-flight streaming swap and a secured asset
	k.SetStreamingSwap(ctx, StreamingSwap{
		TxID:     GetRandomTxHash(),
		Quantity: 10,
		Count:    3,
		Deposit:  cosmos.NewUint(5 * common.One),
		In:       cosmos.NewUint(1 * common.One),
		Out:      cosmos.NewUint(1 * common.One),
	})
	k.SetSecuredAsset(ctx, SecuredAsset{Asset: common.BTCAsset, Depth: cosmos.NewUint(common.One)})

	// mimir: a plain value and a block-height-valued halt key
	k.SetMimir(ctx, "MaxSynthPerPoolDepth", 1700)
	k.SetMimir(ctx, "HaltBTCChain", ctx.BlockHeight())

	// emitEndBlockTelemetry recovers panics internally, so also exercise the
	// sub-emitters directly - a nil deref there fails the test loudly.
	c.Assert(emitEndBlockTelemetry(ctx, mgr), IsNil)
	emitChainTelemetry(ctx, mgr)
	emitNetworkStateTelemetry(ctx, mgr)
	emitStreamingSwapTelemetry(ctx, mgr)
	emitSecuredAssetTelemetry(ctx, mgr)
	emitMimirTelemetry(ctx, mgr)
	emitVaultDetailTelemetry(ctx, mgr)
	emitSolvencyTelemetry(ctx, mgr)
	emitChainDetailTelemetry(ctx, mgr)
	emitQueueDetailTelemetry(ctx, mgr)
	emitTCYTelemetry(ctx, mgr)
}

// The mimir gauge is keyed by the store key with its prefix trimmed; if that
// trimming regresses every mimir series silently renames itself.
func (s *HelperSuite) TestEmitMimirTelemetryKeys(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	k.SetMimir(ctx, "MaxSynthPerPoolDepth", 1700)
	k.SetMimir(ctx, "HaltBTCChain", 42)

	got := map[string]int64{}
	iter := k.GetMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		value := types.ProtoInt64{}
		c.Assert(k.Cdc().Unmarshal(iter.Value(), &value), IsNil)
		got[trimKeyPrefix(iter.Key())] = value.GetValue()
	}

	// KVStore.GetKey uppercases, so the gauge label is the upper-cased key. That
	// matches the label on the thornode_mimir_changed counter and on EventSetMimir,
	// so a PromQL join across the three lines up.
	c.Assert(got["MAXSYNTHPERPOOLDEPTH"], Equals, int64(1700))
	c.Assert(got["HALTBTCCHAIN"], Equals, int64(42))
}

// isStableToStable must agree with membership of both layer-1 assets in
// GetAnchors(TOR).
func (s *HelperSuite) TestIsStableToStableMatchesGetAnchors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	dai, err := common.NewAsset("ETH.DAI-0X6B175474E89094C44DA98B954EEDEAC495271D0F")
	c.Assert(err, IsNil)
	for _, asset := range []common.Asset{usdc, usdt, common.BTCAsset} { // dai has a mimir but no pool
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	k.SetMimir(ctx, "TorAnchor-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", 1)
	k.SetMimir(ctx, "TorAnchor-ETH-DAI-0X6B175474E89094C44DA98B954EEDEAC495271D0F", 1)
	k.SetMimir(ctx, "TorAnchor-BTC-BTC", 0)

	anchors := k.GetAnchors(ctx, common.TOR)
	isAnchor := func(a common.Asset) bool {
		for _, anchor := range anchors {
			if anchor.Equals(a.GetLayer1Asset()) {
				return true
			}
		}
		return false
	}
	assets := []common.Asset{usdc, usdt, dai, common.BTCAsset, usdc.GetSyntheticAsset(), usdt.GetTradeAsset(), common.RuneAsset()}
	for _, a := range assets {
		for _, b := range assets {
			c.Check(isStableToStable(ctx, k, a, b), Equals, isAnchor(a) && isAnchor(b), Commentf("%s -> %s", a, b))
		}
	}
	c.Check(isStableToStable(ctx, k, usdc, usdt), Equals, true)
}
