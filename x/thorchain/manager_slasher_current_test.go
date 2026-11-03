package thorchain

import (
	"errors"
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type SlashingSuite struct{}

var _ = Suite(&SlashingSuite{})

func (s *SlashingSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *SlashingSuite) TestNodeSignSlashErrors(c *C) {
	testCases := []struct {
		name        string
		condition   func(keeper *TestSlashingLackKeeper)
		shouldError bool
	}{
		{
			name: "fail to get tx out should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetTxOut = true
			},
			shouldError: true,
		},
		{
			name: "fail to get vault should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetVault = true
			},
			shouldError: true,
		},
		{
			name: "fail to get node account by pub key should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetNodeAccountByPubKey = true
			},
			shouldError: false,
		},
		{
			name: "fail to get asgard vault by status should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetAsgardByStatus = true
			},
			shouldError: true,
		},
		{
			name: "fail to get observed tx voter should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetObservedTxVoter = true
			},
			shouldError: true,
		},
		{
			name: "fail to set tx out should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failSetTxOut = true
			},
			shouldError: true,
		},
	}
	for _, item := range testCases {
		c.Logf("name:%s", item.name)
		ctx, _ := setupKeeperForTest(c)
		ctx = ctx.WithBlockHeight(201) // set blockheight
		ver := GetCurrentVersion()
		constAccessor := constants.GetConstantValues(ver)
		na := GetRandomValidatorNode(NodeActive)
		inTx := common.NewTx(
			GetRandomTxHash(),
			GetRandomETHAddress(),
			GetRandomETHAddress(),
			common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(320000000)),
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(420000000)),
			},
			nil,
			"SWAP:ETH.ETH",
		)

		txOutItem := TxOutItem{
			Chain:       common.ETHChain,
			InHash:      inTx.ID,
			VaultPubKey: na.PubKeySet.Secp256k1,
			ToAddress:   GetRandomETHAddress(),
			Coin: common.NewCoin(
				common.ETHAsset, cosmos.NewUint(3980500*common.One),
			),
		}
		txOut := NewTxOut(3)
		txOut.TxArray = append(txOut.TxArray, txOutItem)

		vault := GetRandomVault()
		vault.Type = AsgardVault
		keeper := &TestSlashingLackKeeper{
			txOut:  txOut,
			na:     na,
			vaults: Vaults{vault},
			voter: ObservedTxVoter{
				Actions: []TxOutItem{txOutItem},
			},
			slashPts: make(map[string]int64),
		}
		signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
		ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
		slasher := newSlasher(keeper, NewDummyEventMgr())
		item.condition(keeper)
		if item.shouldError {
			c.Assert(slasher.LackSigning(ctx, NewDummyMgr()), NotNil)
		} else {
			c.Assert(slasher.LackSigning(ctx, NewDummyMgr()), IsNil)
		}
	}
}

func (s *SlashingSuite) TestNotSigningSlash(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(201) // set blockheight
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	na := GetRandomValidatorNode(NodeActive)
	inTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(320000000)),
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(420000000)),
		},
		nil,
		"SWAP:ETH.ETH",
	)

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTx.ID,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomETHAddress(),
		Coin: common.NewCoin(
			common.ETHAsset, cosmos.NewUint(3980500*common.One),
		),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(5000000*common.One)),
	}
	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{vault},
		voter: ObservedTxVoter{
			Actions: []TxOutItem{txOutItem},
		},
		slashPts: make(map[string]int64),
	}
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].VaultPubKey.Equals(keeper.vaults[0].PubKey), Equals, true)
	c.Assert(outItems[0].Memo, Equals, "")
	c.Assert(keeper.voter.Actions, HasLen, 1)
	// ensure we've updated our action item
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(outItems[0].VaultPubKey), Equals, true)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
}

func (s *SlashingSuite) TestLackSigningPaperRestoreDoesNotSettleDeficit(c *C) {
	ctx, _ := setupKeeperForTest(c)
	txOutStore := NewTxStoreDummy()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)

	na := GetRandomValidatorNode(NodeActive)
	inHash := GetRandomTxHash()
	maxGas := common.NewCoin(common.ETHAsset, cosmos.NewUint(37_500))
	outbound := common.NewCoin(common.ETHAsset, cosmos.NewUint(100_000))
	required := outbound.Amount.Add(maxGas.Amount)
	deficit := cosmos.NewUint(10_000)

	assignedVault := GetRandomVault()
	assignedVault.Type = AsgardVault
	assignedVault.Status = ActiveVault
	assignedVault.PubKey = na.PubKeySet.Secp256k1
	assignedVault.Chains = common.Chains{common.THORChain, common.ETHChain}.Strings()
	assignedVault.Coins = common.Coins{}
	assignedVault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, deficit))

	otherVault := GetRandomVault()
	otherVault.Type = AsgardVault
	otherVault.Status = ActiveVault
	otherVault.Chains = common.Chains{common.THORChain, common.ETHChain}.Strings()
	otherVault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, required.Sub(cosmos.OneUint())))
	otherVault.Deficit = common.Coins{}

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inHash,
		VaultPubKey: assignedVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        outbound,
		MaxGas:      common.Gas{maxGas},
		Memo:        fmt.Sprintf("OUT:%s", inHash),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{assignedVault, otherVault},
		voter: ObservedTxVoter{
			Actions: []TxOutItem{txOutItem},
		},
		slashPts: make(map[string]int64),
	}

	mgr := NewDummyMgr()
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].VaultPubKey.Equals(assignedVault.PubKey), Equals, true)
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(assignedVault.PubKey), Equals, true)
	c.Assert(keeper.vaults[0].GetCoin(common.ETHAsset).Amount.IsZero(), Equals, true)
	c.Assert(keeper.vaults[0].GetDeficit(common.ETHAsset).Amount.Equal(deficit), Equals, true)
}

func (s *SlashingSuite) TestEVMAggregatorLackSigningUsesExtendedPeriod(c *C) {
	ctx, _ := setupKeeperForTest(c)
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)

	inTxID := GetRandomTxHash()
	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(5000000*common.One)),
	}
	limit := cosmos.NewUint(1000000)
	txOutItem := TxOutItem{
		Chain:                 common.ETHChain,
		InHash:                inTxID,
		VaultPubKey:           activeVault.PubKey,
		ToAddress:             GetRandomETHAddress(),
		Coin:                  common.NewCoin(common.ETHAsset, cosmos.NewUint(3980500*common.One)),
		MaxGas:                common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(3600000))},
		Memo:                  "OUT:" + inTxID.String(),
		OriginalMemo:          "OUT:" + inTxID.String(),
		Aggregator:            GetRandomETHAddress().String(),
		AggregatorTargetAsset: GetRandomETHAddress().String(),
		AggregatorTargetLimit: &limit,
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	zeroLimit := cosmos.ZeroUint()
	standardEVMOutbound := txOutItem
	standardEVMOutbound.Aggregator = ""
	standardEVMOutbound.AggregatorTargetAsset = ""
	standardEVMOutbound.AggregatorTargetLimit = &zeroLimit
	standardEVMOutbound.Coin = common.NewCoin(common.ETHAsset, cosmos.NewUint(3980500*common.One))
	c.Assert(isEVMAggregatorTxOut(standardEVMOutbound), Equals, false)
	c.Assert(txOutSigningPeriod(standardEVMOutbound, signingTransactionPeriod), Equals, signingTransactionPeriod)

	keeper := &TestSlashingLackKeeper{
		txOut:       txOut,
		txOutHeight: 3,
		vaults:      Vaults{activeVault},
		voter: ObservedTxVoter{
			Actions:         []TxOutItem{txOutItem},
			FinalisedHeight: 3,
		},
		slashPts: make(map[string]int64),
	}
	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())

	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 0)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, true)
	c.Assert(keeper.voter.Actions[0].Aggregator, Equals, txOutItem.Aggregator)

	ctx = ctx.WithBlockHeight(3 + evmAggregatorSigningPeriodMultiplier*signingTransactionPeriod)
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
	outItems, err = txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
	c.Assert(keeper.voter.Actions[0].Aggregator, Equals, "")
	c.Assert(outItems[0].Aggregator, Equals, "")
}

func (s *SlashingSuite) TestEVMTokenLackSigningUsesExtendedPeriod(c *C) {
	ctx, _ := setupKeeperForTest(c)
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)

	tokenAsset, err := common.NewAsset("ETH.USDT-0XA3910454BF2CB59B8B3A401589A3BACC5CA42306")
	c.Assert(err, IsNil)
	inTxID := GetRandomTxHash()
	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(5000000*common.One)),
		common.NewCoin(tokenAsset, cosmos.NewUint(5000000*common.One)),
	}
	txOutItem := TxOutItem{
		Chain:        common.ETHChain,
		InHash:       inTxID,
		VaultPubKey:  activeVault.PubKey,
		ToAddress:    GetRandomETHAddress(),
		Coin:         common.NewCoin(tokenAsset, cosmos.NewUint(6313528)),
		MaxGas:       common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(3600000))},
		Memo:         "OUT:" + inTxID.String(),
		OriginalMemo: "OUT:" + inTxID.String(),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	keeper := &TestSlashingLackKeeper{
		txOut:       txOut,
		txOutHeight: 3,
		vaults:      Vaults{activeVault},
		voter: ObservedTxVoter{
			Actions:         []TxOutItem{txOutItem},
			FinalisedHeight: 3,
		},
		slashPts: make(map[string]int64),
	}
	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())

	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 0)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, true)

	ctx = ctx.WithBlockHeight(3 + evmTokenSigningPeriodMultiplier*signingTransactionPeriod)
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
	outItems, err = txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
	c.Assert(outItems[0].Coin.Asset.Equals(tokenAsset), Equals, true)
}

func (s *SlashingSuite) TestInactiveVaultDrainedReroute(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(201)
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	na := GetRandomValidatorNode(NodeActive)
	inTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(3200000000)),
		},
		nil,
		"REFUND:"+GetRandomTxHash().String(),
	)

	// Create the inactive vault (drained — 0 ETH)
	inactiveVault := GetRandomVault()
	inactiveVault.Type = AsgardVault
	inactiveVault.Status = InactiveVault
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
	}

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTx.ID,
		VaultPubKey: inactiveVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin: common.NewCoin(
			common.ETHAsset, cosmos.NewUint(31239*common.One),
		),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(150000)),
		},
		Memo: "REFUND:" + inTx.ID.String(),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	// Create an active vault with sufficient ETH
	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500000*common.One)),
	}

	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{inactiveVault, activeVault},
		voter: ObservedTxVoter{
			Actions:         []TxOutItem{txOutItem},
			FinalisedHeight: 3,
		},
		slashPts: make(map[string]int64),
	}

	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	mgr := NewDummyMgr()
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	// The outbound should have been rerouted to the active vault, NOT the inactive one
	c.Assert(outItems[0].VaultPubKey.Equals(inactiveVault.PubKey), Equals, false)
	c.Assert(outItems[0].VaultPubKey.Equals(activeVault.PubKey), Equals, true)
	// voter action should also be updated
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(activeVault.PubKey), Equals, true)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
}

// TestLackSigningReassignsAwayFromCompromisedVault covers the retry loop called
// out in review: bifrost refuses the original compromised vault, then LackSigning
// must reassign to a safe Active vault instead of picking the flagged one again.
func (s *SlashingSuite) TestLackSigningReassignsAwayFromCompromisedVault(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(201)
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	na := GetRandomValidatorNode(NodeActive)
	inTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(3200000000)),
		},
		nil,
		"REFUND:"+GetRandomTxHash().String(),
	)

	compromisedVault := GetRandomVault()
	compromisedVault.Type = AsgardVault
	compromisedVault.Status = ActiveVault
	compromisedVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500000*common.One)),
	}

	safeVault := GetRandomVault()
	safeVault.Type = AsgardVault
	safeVault.Status = ActiveVault
	safeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500000*common.One)),
	}

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTx.ID,
		VaultPubKey: compromisedVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin: common.NewCoin(
			common.ETHAsset, cosmos.NewUint(31239*common.One),
		),
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(150000)),
		},
		Memo: "REFUND:" + inTx.ID.String(),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{compromisedVault, safeVault},
		voter: ObservedTxVoter{
			Actions:         []TxOutItem{txOutItem},
			FinalisedHeight: 3,
		},
		slashPts: make(map[string]int64),
	}
	keeper.SetMimir(ctx, compromisedVaultMimirKeyPrefix+compromisedVault.PubKey.String(), 1)

	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	mgr := NewDummyMgr()
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].VaultPubKey.Equals(compromisedVault.PubKey), Equals, false)
	c.Assert(outItems[0].VaultPubKey.Equals(safeVault.PubKey), Equals, true)
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(safeVault.PubKey), Equals, true)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
}

func (s *SlashingSuite) TestInactiveXMRVaultDrainedRerouteUsesMonovault(c *C) {
	ctx, _ := setupKeeperForTest(c)
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(2 * signingTransactionPeriod)

	na := GetRandomValidatorNode(NodeActive)
	inTxID := common.BlankTxID
	xmrVaultAddr, err := common.NewAddress(common.XMRChecksummedDummyAddress('4'))
	c.Assert(err, IsNil)

	inactiveVault := GetRandomVault()
	inactiveVault.Type = AsgardVault
	inactiveVault.Status = InactiveVault
	inactiveVault.Chains = common.Chains{common.THORChain, common.XMRChain}.Strings()
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.ZeroUint()),
	}

	regularVault := GetRandomVault()
	regularVault.Type = AsgardVault
	regularVault.Status = ActiveVault
	regularVault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	regularVault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(50*common.One)),
	}

	xmrVault := GetRandomVault()
	xmrVault.Type = AsgardVault
	xmrVault.Status = ActiveVault
	xmrVault.Chains = common.Chains{common.THORChain, common.XMRChain}.Strings()
	xmrVault.PubKeyFrost = GetRandomEd25519PubKey()
	xmrVault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(50*common.One)),
	}

	txOutItem := TxOutItem{
		Chain:            common.XMRChain,
		InHash:           inTxID,
		VaultPubKey:      inactiveVault.PubKey,
		ToAddress:        xmrVaultAddr,
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
		MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		Memo:             "REFUND:" + GetRandomTxHash().String(),
		VaultPubKeyFrost: inactiveVault.PubKeyFrost,
	}

	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{inactiveVault, regularVault, xmrVault},
		voter: ObservedTxVoter{
			Actions:         []TxOutItem{txOutItem},
			FinalisedHeight: 1,
		},
		slashPts: make(map[string]int64),
	}

	mgr := NewDummyMgr()
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].VaultPubKey.Equals(xmrVault.PubKey), Equals, true)
	c.Assert(outItems[0].VaultPubKeyFrost.Equals(xmrVault.PubKeyFrost), Equals, true)
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(xmrVault.PubKey), Equals, true)
	c.Assert(keeper.voter.Actions[0].VaultPubKeyFrost.Equals(xmrVault.PubKeyFrost), Equals, true)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
}

func (s *SlashingSuite) TestNewSlasher(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	keeper := &TestSlashObservingKeeper{
		nas:      nas,
		addrs:    []cosmos.AccAddress{nas[0].NodeAddress},
		slashPts: make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher, NotNil)
}

func (s *SlashingSuite) TestHandleSuccessfulSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	testCases := []struct {
		name                  string
		setupNodeAccount      func() (NodeAccount, error)
		validatorAddr         string
		expectedMissingBefore uint64
		expectedMissingAfter  uint64
		expectedErr           error
	}{
		{
			name: "normal node account with missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 10
				return na, nil
			},
			expectedMissingBefore: 10,
			expectedMissingAfter:  9,
			expectedErr:           nil,
		},
		{
			name: "node account with zero missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 0
				return na, nil
			},
			expectedMissingBefore: 0,
			expectedMissingAfter:  0,
			expectedErr:           nil,
		},
		{
			name: "node account with max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 100
				return na, nil
			},
			expectedMissingBefore: 100,
			expectedMissingAfter:  99,
			expectedErr:           nil,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		na, err := tc.setupNodeAccount()
		c.Assert(err, IsNil)

		keeper := &TestDoubleSlashKeeper{
			na:          na,
			network:     NewNetwork(),
			slashPoints: make(map[string]int64),
			constants:   make(map[string]int64),
		}
		slasher := newSlasher(keeper, NewDummyEventMgr())

		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		c.Assert(err, IsNil)

		var pair nodeAddressValidatorAddressPair
		pair.nodeAddress = na.NodeAddress
		pair.validatorAddress = pk.Address()

		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingBefore)
		err = slasher.HandleSuccessfulSign(ctx, pk.Address(), constAccessor, []nodeAddressValidatorAddressPair{pair})
		c.Assert(err, IsNil)
		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingAfter)
	}
}

func (s *SlashingSuite) TestHandleSuccessfulSignErrors(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	// Test case: validator address not found
	na := GetRandomValidatorNode(NodeActive)
	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	randomAddr := GetRandomBech32Addr().String()
	err := slasher.HandleSuccessfulSign(ctx, []byte(randomAddr), constAccessor, []nodeAddressValidatorAddressPair{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "could not find active node account with validator address: .*")
}

func (s *SlashingSuite) TestHandleMissingSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	testCases := []struct {
		name                  string
		setupNodeAccount      func() (NodeAccount, error)
		maxTrack              int64
		missBlockSignSlashPts int64
		expectedMissingBefore uint64
		expectedMissingAfter  uint64
		expectedSlashPoints   int64
		expectedErr           error
	}{
		{
			name: "normal node account with no missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 0
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 0,
			expectedMissingAfter:  1,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with some missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 5
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 5,
			expectedMissingAfter:  6,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account at max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 10
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 10,
			expectedMissingAfter:  10, // Should not increase beyond maxTrack
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account exceeding max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 11 // This is already above maxTrack, but should be capped
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 11,
			expectedMissingAfter:  10, // Should be capped at maxTrack
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with low maxTrack value",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 1
				return na, nil
			},
			maxTrack:              3,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 1,
			expectedMissingAfter:  2,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with high slash points",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 5
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 20,
			expectedMissingBefore: 5,
			expectedMissingAfter:  6,
			expectedSlashPoints:   20,
			expectedErr:           nil,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		na, err := tc.setupNodeAccount()
		c.Assert(err, IsNil)

		keeper := &TestDoubleSlashKeeper{
			na:          na,
			network:     NewNetwork(),
			slashPoints: make(map[string]int64),
			constants:   make(map[string]int64),
		}
		keeper.constants["MissBlockSignSlashPoints"] = tc.missBlockSignSlashPts
		keeper.constants["MaxTrackMissingBlock"] = tc.maxTrack
		slasher := newSlasher(keeper, NewDummyEventMgr())

		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		c.Assert(err, IsNil)

		var pair nodeAddressValidatorAddressPair
		pair.nodeAddress = na.NodeAddress
		pair.validatorAddress = pk.Address()

		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingBefore)
		err = slasher.HandleMissingSign(ctx, pk.Address(), constAccessor, []nodeAddressValidatorAddressPair{pair})
		c.Assert(err, IsNil)
		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingAfter)
		c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, tc.expectedSlashPoints)
	}
}

func (s *SlashingSuite) TestHandleMissingSignErrors(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	// Test case: validator address not found
	na := GetRandomValidatorNode(NodeActive)
	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	randomAddr := GetRandomBech32Addr().String()
	err := slasher.HandleMissingSign(ctx, []byte(randomAddr), constAccessor, []nodeAddressValidatorAddressPair{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "could not find active node account with validator address: .*")
}

func (s *SlashingSuite) TestDoubleSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	na := GetRandomValidatorNode(NodeActive)

	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
	c.Assert(err, IsNil)

	var pair nodeAddressValidatorAddressPair
	pair.nodeAddress = na.NodeAddress
	pair.validatorAddress = pk.Address()

	c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, int64(0))
	keeper.constants["DoubleBlockSignSlashPoints"] = 1
	err = slasher.HandleDoubleSign(ctx, pk.Address(), 0, constAccessor, []nodeAddressValidatorAddressPair{pair})
	c.Assert(err, IsNil)
	c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, int64(1))
}

func (s *SlashingSuite) TestIncreaseDecreaseSlashPoints(c *C) {
	ctx, _ := setupKeeperForTest(c)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)

	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())
	addr := GetRandomBech32Addr()
	slasher.IncSlashPoints(ctx, 1, addr)
	slasher.DecSlashPoints(ctx, 1, addr)
	c.Assert(keeper.slashPoints[addr.String()], Equals, int64(0))
}

func (s *SlashingSuite) TestSlashVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	// when coins are empty , it should return nil
	c.Assert(slasher.SlashVault(ctx, GetRandomPubKey(), common.NewCoins(), mgr), IsNil)

	// when vault is not available , it should return an error
	err := slasher.SlashVault(ctx, GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, types.ErrVaultNotFound), Equals, true)

	// create a node
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node.Bond.Uint64())
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable

	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	poolBeforeSlash := pool.BalanceRune

	err = slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)
	nodeTemp, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	expectedBond := cosmos.NewUint(99850000000)
	c.Assert(nodeTemp.Bond.Equal(expectedBond), Equals, true, Commentf("%d", nodeTemp.Bond.Uint64()))

	asgardAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	poolAfterSlash := pool.BalanceRune

	// ensure pool's change is in sync with asgard's change
	c.Assert(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, poolAfterSlash.Sub(poolBeforeSlash).Uint64(), Commentf("%d", "pool/asgard rune mismatch"))

	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(100000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(100000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(bondBeforeSlash.Sub(bondAfterSlash).Uint64(), Equals, uint64(150000000), Commentf("%d", bondBeforeSlash.Sub(bondAfterSlash).Uint64()))
	c.Check(reserveAfterSlash.Sub(reserveBeforeSlash).Uint64(), Equals, uint64(50000000), Commentf("%d", reserveAfterSlash.Sub(reserveBeforeSlash).Uint64()))

	// add one more node , slash asgard
	node1 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node1), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node1.Bond.Uint64())

	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = types2.VaultStatus_ActiveVault
	vault1.PubKey = GetRandomPubKey()
	vault1.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
		node1.PubKeySet.Secp256k1.String(),
	}
	vault1.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)
	nodeBeforeSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondBeforeSlash := nodeBeforeSlash.Bond
	node1BondBeforeSlash := node1.Bond
	mgr.Keeper().SetMimir(ctx, "PauseOnSlashThreshold", 1)
	err = slasher.SlashVault(ctx, vault1.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)

	nodeAfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	node1AfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node1.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondAfterSlash := nodeAfterSlash.Bond
	node1BondAfterSlash := node1AfterSlash.Bond

	c.Check(nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64(), Equals, uint64(76457722), Commentf("%d", nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64()))
	c.Check(node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64(), Equals, uint64(76572581), Commentf("%d", node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64()))

	// Check that signing halt mimir was set
	val, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(18), Commentf("%d", val))

	// Check that trading halt mimir was set
	val2, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltTrading, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val2, Equals, int64(18), Commentf("%d", val2))
}

func (s *SlashingSuite) TestUpdatePoolFromSlash(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	deductAsset := cosmos.NewUint(250 * common.One)
	creditRune := cosmos.NewUint(500 * common.One)
	stolenAsset := common.NewCoin(common.BTCAsset, deductAsset)
	slasher.updatePoolFromSlash(ctx, pool, stolenAsset, creditRune, mgr)

	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.Uint64(), Equals, uint64(750*common.One))
	c.Assert(pool.BalanceRune.Uint64(), Equals, uint64(1500*common.One))
}

func (s *SlashingSuite) TestNetworkShouldNotSlashMorethanVaultAmount(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	// create a node
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node.Bond.Uint64())
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One/2)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable

	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	poolBeforeSlash := pool.BalanceRune

	// vault only has 0.5 BTC , however the outbound is 1 BTC , make sure we don't over slash the vault
	err := slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)
	nodeTemp, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	expectedBond := cosmos.NewUint(99925000000)
	c.Assert(nodeTemp.Bond.Equal(expectedBond), Equals, true, Commentf("%d", nodeTemp.Bond.Uint64()))

	asgardAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	poolAfterSlash := pool.BalanceRune

	// ensure pool's change is in sync with asgard's change
	c.Assert(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, poolAfterSlash.Sub(poolBeforeSlash).Uint64(), Commentf("%d", "pool/asgard rune mismatch"))

	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(50000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(bondBeforeSlash.Sub(bondAfterSlash).Uint64(), Equals, uint64(75000000), Commentf("%d", bondBeforeSlash.Sub(bondAfterSlash).Uint64()))
	c.Check(reserveAfterSlash.Sub(reserveBeforeSlash).Uint64(), Equals, uint64(25000000), Commentf("%d", reserveAfterSlash.Sub(reserveBeforeSlash).Uint64()))

	// add one more node , slash asgard
	node1 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node1), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node1.Bond.Uint64())

	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = types2.VaultStatus_ActiveVault
	vault1.PubKey = GetRandomPubKey()
	vault1.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
		node1.PubKeySet.Secp256k1.String(),
	}
	vault1.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One/2)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)
	nodeBeforeSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondBeforeSlash := nodeBeforeSlash.Bond
	node1BondBeforeSlash := node1.Bond
	mgr.Keeper().SetMimir(ctx, "PauseOnSlashThreshold", 1)
	err = slasher.SlashVault(ctx, vault1.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)

	nodeAfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	node1AfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node1.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondAfterSlash := nodeAfterSlash.Bond
	node1BondAfterSlash := node1AfterSlash.Bond

	c.Check(nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64(), Equals, uint64(37862676), Commentf("%d", nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64()))
	c.Check(node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64(), Equals, uint64(37891094), Commentf("%d", node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64()))

	// Check that signing halt mimir was set
	val, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(18), Commentf("%d", val))

	// Check that trading halt mimir was set
	val2, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltTrading, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val2, Equals, int64(18), Commentf("%d", val2))

	// Attempt to slash more than node has, pool should only be deducted what was successfully slashed
	pool.BalanceRune = cosmos.NewUint(4000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(4000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	node2 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node2), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node2.Bond.Uint64())

	vault = GetRandomVault()
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node2.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(4000*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	err = slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2000*common.One))), mgr)
	c.Assert(err, IsNil)
	updatedPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	// Even though the total rune value to slash is 3000, the node only has 1000 RUNE bonded, so only slash and credit that much to the pool's rune side
	c.Assert(updatedPool.BalanceRune.Uint64(), Equals, cosmos.NewUint(5000*common.One).Uint64())
	// But deduct full stolen amount from asset side
	c.Assert(updatedPool.BalanceAsset.Uint64(), Equals, cosmos.NewUint(2000*common.One).Uint64())
}

func (s *SlashingSuite) TestNeedsNewVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()
	sig1, _ := pk1.GetThorAddress()
	sig2, _ := pk2.GetThorAddress()
	sig3, _ := pk3.GetThorAddress()
	pk := GetRandomPubKey()
	tx := GetRandomTx()
	tx.ID = outhash
	obs := NewObservedTx(tx, 0, pk, 0)
	obs.ObservedPubKey = pk
	obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

	vault := GetRandomVault()
	vault.Membership = []string{pk1.String(), pk2.String(), pk3.String()}

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	c.Assert(len(tx.Coins), Equals, 1)
	toi := TxOutItem{
		InHash:      inhash,
		Coin:        tx.Coins[0],
		ToAddress:   tx.ToAddress,
		VaultPubKey: pk,
	}

	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)

	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)

	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
	ctx = ctx.WithBlockHeight(600)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
	ctx = ctx.WithBlockHeight(900)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)

	// A non-finalised observation cannot pin reassignment forever.
	ctx = ctx.WithBlockHeight(902)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)
}

func (s *SlashingSuite) TestNeedsNewVaultEdDSA(c *C) {
	ctx, mgr := setupManagerForTest(c)

	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()
	sig1, _ := pk1.GetThorAddress()
	sig2, _ := pk2.GetThorAddress()
	sig3, _ := pk3.GetThorAddress()

	// Use separate ECDSA and EdDSA keys for the vault, as would be the case for SOL.
	ecdsaPK := GetRandomPubKey()
	eddsaPK := GetRandomPubKey()

	tx := GetRandomTx()
	tx.ID = outhash
	obs := NewObservedTx(tx, 0, eddsaPK, 0)
	obs.ObservedPubKey = eddsaPK // observation uses the EdDSA key (as SOL bifrost would)
	obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

	vault := GetRandomVault()
	vault.Membership = []string{pk1.String(), pk2.String(), pk3.String()}

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	c.Assert(len(tx.Coins), Equals, 1)
	toi := TxOutItem{
		InHash:           inhash,
		Coin:             tx.Coins[0],
		ToAddress:        tx.ToAddress,
		VaultPubKey:      ecdsaPK, // ECDSA key on the toi
		VaultPubKeyEddsa: eddsaPK, // EdDSA key on the toi
	}

	// No observation yet, should need new vault.
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)

	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	// Observation exists with EdDSA key matching toi.VaultPubKeyEddsa,
	// even though it doesn't match toi.VaultPubKey (ECDSA). Should NOT need new vault.
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)

	// The EdDSA observation pins while fresh, then expires like other non-final observations.
	ctx = ctx.WithBlockHeight(902)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)
}

func (s *SlashingSuite) TestNeedsNewVaultFrost(c *C) {
	ctx, mgr := setupManagerForTest(c)

	signingTransPeriod := int64(300)
	startHeight := int64(1)
	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()
	sig1, _ := pk1.GetThorAddress()
	sig2, _ := pk2.GetThorAddress()
	sig3, _ := pk3.GetThorAddress()

	secpPK := GetRandomPubKey()
	frostPK := GetRandomPubKey()

	tx := GetRandomTx()
	tx.ID = outhash
	tx.Chain = common.XMRChain
	tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)))
	obs := NewObservedTx(tx, 0, frostPK, 0)
	obs.ObservedPubKey = frostPK
	obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

	vault := GetRandomVault()
	vault.Membership = []string{pk1.String(), pk2.String(), pk3.String()}

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	c.Assert(len(tx.Coins), Equals, 1)
	toi := TxOutItem{
		Chain:            common.XMRChain,
		InHash:           inhash,
		Coin:             tx.Coins[0],
		ToAddress:        tx.ToAddress,
		VaultPubKey:      secpPK,
		VaultPubKeyFrost: frostPK,
	}
	xmrSigningTransPeriod := txOutSigningPeriod(toi, signingTransPeriod)

	c.Check(slasher.needsNewVault(ctx, mgr, vault, xmrSigningTransPeriod, startHeight, toi), Equals, true)

	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	c.Check(slasher.needsNewVault(ctx, mgr, vault, xmrSigningTransPeriod, startHeight, toi), Equals, false)
	ctx = ctx.WithBlockHeight(startHeight + staleOutboundObservationSigningPeriods*signingTransPeriod + 1)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, xmrSigningTransPeriod, startHeight, toi), Equals, false)
	ctx = ctx.WithBlockHeight(startHeight + staleOutboundObservationSigningPeriods*xmrSigningTransPeriod + 1)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, xmrSigningTransPeriod, startHeight, toi), Equals, true)
}

func (s *SlashingSuite) TestActiveOutboundObservationExpiresStaleNonFinalized(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	signingTransPeriod := int64(300)
	startHeight := int64(100)
	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = outhash
	obs := NewObservedTx(tx, 0, GetRandomPubKey(), 0)
	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	expiryHeight := startHeight + staleOutboundObservationSigningPeriods*signingTransPeriod
	firstStaleHeight := expiryHeight + 1

	ctx = ctx.WithBlockHeight(expiryHeight)
	status := slasher.staleNonFinalOutboundObservation(ctx, voter, signingTransPeriod, startHeight)
	c.Check(status.stale, Equals, false)
	c.Check(status.firstStaleWindow, Equals, false)
	c.Check(status.observationHeight, Equals, startHeight)
	c.Check(status.staleSinceHeight, Equals, firstStaleHeight)
	c.Check(slasher.hasActiveOutboundObservation(ctx, mgr, signingTransPeriod, startHeight, inhash), Equals, true)

	ctx = ctx.WithBlockHeight(firstStaleHeight)
	status = slasher.staleNonFinalOutboundObservation(ctx, voter, signingTransPeriod, startHeight)
	c.Check(status.stale, Equals, true)
	c.Check(status.firstStaleWindow, Equals, true)
	c.Check(status.staleSinceHeight, Equals, firstStaleHeight)
	c.Check(slasher.hasActiveOutboundObservation(ctx, mgr, signingTransPeriod, startHeight, inhash), Equals, false)

	ctx = ctx.WithBlockHeight(firstStaleHeight + signingTransPeriod)
	status = slasher.staleNonFinalOutboundObservation(ctx, voter, signingTransPeriod, startHeight)
	c.Check(status.stale, Equals, true)
	c.Check(status.firstStaleWindow, Equals, false)
	c.Check(slasher.hasActiveOutboundObservation(ctx, mgr, signingTransPeriod, startHeight, inhash), Equals, false)

	voter.UnfinalizedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	c.Check(slasher.hasActiveOutboundObservation(ctx, mgr, signingTransPeriod, startHeight, inhash), Equals, true)

	voter.UnfinalizedHeight = 0
	voter.FinalisedHeight = startHeight
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	c.Check(slasher.hasActiveOutboundObservation(ctx, mgr, signingTransPeriod, startHeight, inhash), Equals, true)
}

func (s *SlashingSuite) TestTreasuryRecoveryForMaxAttempts(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create tx out item
	inHash := GetRandomTxHash()
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	// Parse the memo
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)
	c.Assert(memo.IsOutbound(), Equals, true, Commentf("Memo should be outbound"))

	// Directly call sendFailedOutboundToTreasury
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error"))

	// Verify a swap was queued (check both standard and advanced swap queues)
	hasSwap := false

	// Check standard swap queue
	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		hasSwap = true
		break
	}
	swapQueueItems.Close()

	// If not in standard queue, check advanced swap queue
	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			hasSwap = true
			break
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected a swap to be queued for treasury recovery"))
}

func (s *SlashingSuite) TestQueueRecoverySwapUsesFrostVaultAddressForXMR(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = GetRandomPubKey()
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	xmrAddr, err := vault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	txID := GetRandomTxHash()
	toi := TxOutItem{
		Chain:            common.XMRChain,
		InHash:           txID,
		VaultPubKey:      vault.PubKey,
		VaultPubKeyFrost: vault.PubKeyFrost,
		ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
		Memo:             fmt.Sprintf("OUT:%s", txID),
	}

	err = slasher.queueRecoverySwap(
		ctx,
		mgr,
		toi,
		vault,
		txID,
		common.RuneAsset(),
		common.TreasuryAddress,
		fmt.Sprintf("=:RUNE:%s", common.TreasuryAddress),
		"treasury",
	)
	c.Assert(err, IsNil)

	var queuedSwap *MsgSwap
	iter := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var msg MsgSwap
		if unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg); unmarshalErr == nil {
			queuedSwap = &msg
			break
		}
	}
	iter.Close()

	// Fallback to advanced queue if standard queue has no item.
	if queuedSwap == nil {
		advIter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advIter.Valid(); advIter.Next() {
			var msg MsgSwap
			if unmarshalErr := mgr.Keeper().Cdc().Unmarshal(advIter.Value(), &msg); unmarshalErr == nil {
				queuedSwap = &msg
				break
			}
		}
		advIter.Close()
	}

	c.Assert(queuedSwap, NotNil)
	c.Assert(queuedSwap.Tx.FromAddress.Equals(xmrAddr), Equals, true)
	c.Assert(queuedSwap.Tx.ToAddress.Equals(xmrAddr), Equals, true)
}

func (s *SlashingSuite) TestTreasuryRecoveryWithDifferentMemoTypes(c *C) {
	ctx, mgr := setupManagerForTest(c)

	testCases := []struct {
		name          string
		memo          string
		shouldRecover bool
	}{
		{
			name:          "outbound memo should recover",
			memo:          fmt.Sprintf("OUT:%s", GetRandomTxHash()),
			shouldRecover: true,
		},
		{
			name:          "refund memo should recover",
			memo:          fmt.Sprintf("REFUND:%s", GetRandomTxHash()),
			shouldRecover: true,
		},
		{
			name:          "ragnarok memo should recover",
			memo:          fmt.Sprintf("RAGNAROK:%d", 12345),
			shouldRecover: true,
		},
		{
			name:          "migrate memo should not recover (internal)",
			memo:          fmt.Sprintf("MIGRATE:%d", 12345),
			shouldRecover: false,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)

		// Setup vault
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

		vault := GetRandomVault()
		vault.Type = AsgardVault
		vault.Status = types2.VaultStatus_ActiveVault
		vault.PubKey = na.PubKeySet.Secp256k1
		vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
		vault.Coins = common.NewCoins(
			common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		)
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

		// Setup pool for non-RUNE assets
		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		// Create tx out item
		inHash := GetRandomTxHash()
		txOutItem := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      inHash,
			VaultPubKey: na.PubKeySet.Secp256k1,
			ToAddress:   GetRandomBTCAddress(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
			Memo:        tc.memo,
		}

		// Parse memo
		parsedMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
		c.Assert(err, IsNil)

		// Check if memo matches expected outbound type
		c.Assert(parsedMemo.IsOutbound(), Equals, tc.shouldRecover, Commentf("Memo type mismatch for %s", tc.name))

		// If this should recover, test the sendFailedOutboundToTreasury function
		if tc.shouldRecover {
			slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
			err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, parsedMemo)
			c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error for %s", tc.name))

			// Verify a swap was queued (check both standard and advanced swap queues)
			hasSwap := false

			// Check standard swap queue
			swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
			for ; swapQueueItems.Valid(); swapQueueItems.Next() {
				hasSwap = true
				break
			}
			swapQueueItems.Close()

			// If not in standard queue, check advanced swap queue
			if !hasSwap {
				advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
				for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
					hasSwap = true
					break
				}
				advSwapQueueItems.Close()
			}

			c.Assert(hasSwap, Equals, true, Commentf("Expected swap to be queued for %s", tc.name))
		}
	}
}

func (s *SlashingSuite) TestReverseSwapForFailedSwapOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pools for both assets
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create original swap transaction: ETH -> BTC
	inHash := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	swapMemo := fmt.Sprintf("=:BTC.BTC:%s", btcAddr.String())

	originalTx := common.Tx{
		ID:          inHash,
		Chain:       common.ETHChain,
		FromAddress: ethAddr,
		ToAddress:   GetRandomETHAddress(), // vault address
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		Memo:        swapMemo,
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
	}

	observedTx := ObservedTx{
		Tx:             originalTx,
		Status:         common.Status_done,
		OutHashes:      nil,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
		ObservedPubKey: vault.PubKey,
		FinaliseHeight: ctx.BlockHeight(),
	}

	// Save the observed tx voter
	voter := NewObservedTxVoter(inHash, []ObservedTx{observedTx})
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Create the failed outbound (BTC output that couldn't be sent)
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   btcAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	// Parse the outbound memo
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)
	c.Assert(memo.IsOutbound(), Equals, true)

	// Call sendFailedOutboundToTreasury - it should detect this is a swap and do a reverse swap
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error"))

	// Verify a swap was queued for reverse swap (BTC -> ETH)
	hasSwap := false
	var queuedSwap MsgSwap

	// Check standard swap queue
	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	// If not in standard queue, check advanced swap queue
	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected reverse swap to be queued"))

	// Verify the swap is reversing correctly: BTC -> ETH back to original sender
	c.Assert(queuedSwap.Tx.Coins[0].Asset.Equals(common.BTCAsset), Equals, true,
		Commentf("Swap should be from BTC (failed asset)"))
	c.Assert(queuedSwap.TargetAsset.Equals(common.ETHAsset), Equals, true,
		Commentf("Swap should be to ETH (original asset)"))
	c.Assert(queuedSwap.Destination.Equals(ethAddr), Equals, true,
		Commentf("Swap should send back to original sender"))
	c.Assert(queuedSwap.TradeTarget.IsZero(), Equals, true,
		Commentf("Reverse swap should have no slip limit"))
}

func (s *SlashingSuite) TestReverseSwapWithCustomRefundAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pools
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create original swap transaction with custom refund address: ETH -> BTC
	inHash := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	customRefundAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	// Format: =:ASSET:DESTINATION/REFUND:LIMIT
	swapMemo := fmt.Sprintf("=:BTC.BTC:%s/%s", btcAddr.String(), customRefundAddr.String())

	originalTx := common.Tx{
		ID:          inHash,
		Chain:       common.ETHChain,
		FromAddress: ethAddr,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		Memo:        swapMemo,
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
	}

	observedTx := ObservedTx{
		Tx:             originalTx,
		Status:         common.Status_done,
		OutHashes:      nil,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
		ObservedPubKey: vault.PubKey,
		FinaliseHeight: ctx.BlockHeight(),
	}

	voter := NewObservedTxVoter(inHash, []ObservedTx{observedTx})
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Create the failed outbound
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   btcAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)

	// Call sendFailedOutboundToTreasury
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil)

	// Verify the reverse swap uses the custom refund address
	hasSwap := false
	var queuedSwap MsgSwap

	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected reverse swap to be queued"))
	c.Assert(queuedSwap.Destination.Equals(customRefundAddr), Equals, true,
		Commentf("Reverse swap should use custom refund address"))
}

func (s *SlashingSuite) TestReverseSwapFallbackToTreasury(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup only BTC pool (no ETH pool for reverse swap)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create failed outbound WITHOUT an original observed tx
	// This simulates a case where we can't retrieve the original transaction
	inHash := GetRandomTxHash()
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)

	// Call sendFailedOutboundToTreasury - should fall back to treasury since no original tx
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil)

	// Verify a treasury recovery swap was queued (BTC -> RUNE to treasury)
	hasSwap := false
	var queuedSwap MsgSwap

	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected treasury recovery swap to be queued"))
	c.Assert(queuedSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true,
		Commentf("Treasury recovery should swap to RUNE"))
	c.Assert(queuedSwap.Destination.Equals(common.TreasuryAddress), Equals, true,
		Commentf("Treasury recovery should send to treasury address"))
}

func (s *SlashingSuite) TestReverseSwapXMRUnknownSenderFallsBackToTreasury(c *C) {
	ctx, mgr := setupManagerForTest(c)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	inHash := GetRandomTxHash()
	xmrVaultAddr, err := common.NewAddress(common.XMRChecksummedDummyAddress('4'))
	c.Assert(err, IsNil)
	xmrUnknownSender, err := common.NewAddress(common.XMRUnknownSenderAddress(inHash.String(), xmrVaultAddr.String()))
	c.Assert(err, IsNil)
	btcAddr := GetRandomBTCAddress()
	swapMemo := fmt.Sprintf("=:BTC.BTC:%s", btcAddr.String())

	originalTx := common.Tx{
		ID:          inHash,
		Chain:       common.XMRChain,
		FromAddress: xmrUnknownSender,
		ToAddress:   xmrVaultAddr,
		Coins:       common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(10*common.One))},
		Memo:        swapMemo,
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
	}

	observedTx := ObservedTx{
		Tx:             originalTx,
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
		ObservedPubKey: vault.PubKey,
		FinaliseHeight: ctx.BlockHeight(),
	}

	voter := NewObservedTxVoter(inHash, []ObservedTx{observedTx})
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   btcAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil)

	hasSwap := false
	var queuedSwap MsgSwap

	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected treasury recovery swap to be queued"))
	c.Assert(queuedSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true,
		Commentf("XMR unknown sender should skip reverse swap and fall back to treasury recovery"))
	c.Assert(queuedSwap.Destination.Equals(common.TreasuryAddress), Equals, true,
		Commentf("Treasury recovery should send to treasury address when XMR sender is unknown"))
}

// The reassignment guard must read the destination through the same comparator
// the outbound handlers use: an observation to an unrelated address is not this
// outbound and must not pin the item, while a Zcash outbound scheduled to a tex
// address and observed in its P2PKH form is.
func (s *SlashingSuite) TestNeedsNewVaultMatchesDestination(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// A majority of the assigned vault's members pins an item outright, so the
	// members below sign every observation in this test and the destination is
	// then the only thing that can decide the outcome.
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()

	newVoterFor := func(toi TxOutItem, observedTo common.Address, pk common.PubKey) common.TxID {
		inhash := toi.InHash
		outhash := GetRandomTxHash()
		sig1, _ := pk1.GetThorAddress()
		sig2, _ := pk2.GetThorAddress()
		sig3, _ := pk3.GetThorAddress()

		tx := GetRandomTx()
		tx.ID = outhash
		tx.Chain = toi.Chain
		tx.ToAddress = observedTo
		tx.Coins = common.NewCoins(toi.Coin)
		obs := NewObservedTx(tx, 0, pk, 0)
		obs.ObservedPubKey = pk
		obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

		voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
		mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
		mgr.Keeper().SetObservedLink(ctx, inhash, outhash)
		return outhash
	}

	vaultOf := func(members ...common.PubKey) Vault {
		vault := GetRandomVault()
		vault.Membership = nil
		for _, m := range members {
			vault.Membership = append(vault.Membership, m.String())
		}
		return vault
	}

	// An unrelated destination is not this outbound.
	pk := GetRandomPubKey()
	strangerToi := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: pk,
	}
	_ = newVoterFor(strangerToi, GetRandomBTCAddress(), pk)
	c.Check(slasherNeedsNewVault(c, mgr, ctx, vaultOf(pk1, pk2, pk3), strangerToi), Equals, true)

	// The very same observation, reported to the scheduled destination, pins it.
	matchedToi := strangerToi
	matchedToi.InHash = GetRandomTxHash()
	_ = newVoterFor(matchedToi, matchedToi.ToAddress, pk)
	c.Check(slasherNeedsNewVault(c, mgr, ctx, vaultOf(pk1, pk2, pk3), matchedToi), Equals, false)

	// A Zcash tex destination and its P2PKH form are one address.
	p2pkhAddress, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	texAddress, err := p2pkhAddress.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(p2pkhAddress.Equals(texAddress), Equals, false)

	zecToi := TxOutItem{
		Chain:       common.ZECChain,
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One)),
		ToAddress:   texAddress,
		VaultPubKey: pk,
	}
	_ = newVoterFor(zecToi, p2pkhAddress, pk)
	c.Check(slasherNeedsNewVault(c, mgr, ctx, vaultOf(pk1, pk2, pk3), zecToi), Equals, false)
}

// slasherNeedsNewVault runs needsNewVault with the fixture's signing period and
// start height, keeping the destination assertions above readable.
func slasherNeedsNewVault(c *C, mgr Manager, ctx cosmos.Context, vault Vault, toi TxOutItem) bool {
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	return slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi)
}

// A link is written on the first attestation, long before the outbound voter
// reaches consensus, so the links for an inbound hash are an unordered pile of
// claims rather than a record of what was sent. Every one of them must be
// weighed before the outbound is handed to another vault, and only the assigned
// vault's own members can speak the majority that pins it indefinitely.
func (s *SlashingSuite) TestNeedsNewVaultWeighsEveryLink(c *C) {
	const (
		signingTransPeriod = int64(300)
		startHeight        = int64(1)
	)

	// A five member vault: one signer is a sub-majority, three are a majority.
	members := make([]common.PubKey, 5)
	memberSigners := make([]string, len(members))
	for i := range members {
		members[i] = GetRandomPubKey()
		addr, err := members[i].GetThorAddress()
		c.Assert(err, IsNil)
		memberSigners[i] = addr.String()
	}
	outsiderSigners := make([]string, 6)
	for i := range outsiderSigners {
		addr, err := GetRandomPubKey().GetThorAddress()
		c.Assert(err, IsNil)
		outsiderSigners[i] = addr.String()
	}

	// link describes one observed link to record for the item under test.
	type link struct {
		signers        []string
		toAddress      common.Address
		amount         *cosmos.Uint
		finalised      bool
		observedPubKey *common.PubKey
	}

	// setup records the given links against a fresh inbound hash and returns
	// the assigned vault and the item they were recorded for.
	setup := func(ctx cosmos.Context, mgr Manager, links []link) (Vault, TxOutItem) {
		vault := GetRandomVault()
		vault.Membership = nil
		for _, member := range members {
			vault.Membership = append(vault.Membership, member.String())
		}

		inhash := GetRandomTxHash()
		toi := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      inhash,
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: vault.PubKey,
			MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
		}

		for _, l := range links {
			outhash := GetRandomTxHash()
			tx := GetRandomTx()
			tx.ID = outhash
			tx.Chain = toi.Chain
			tx.ToAddress = toi.ToAddress
			if !l.toAddress.IsEmpty() {
				tx.ToAddress = l.toAddress
			}
			amount := toi.Coin.Amount
			if l.amount != nil {
				amount = *l.amount
			}
			tx.Coins = common.NewCoins(common.NewCoin(toi.Coin.Asset, amount))
			tx.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))}

			observedPubKey := toi.VaultPubKey
			if l.observedPubKey != nil {
				observedPubKey = *l.observedPubKey
			}
			obs := NewObservedTx(tx, 0, observedPubKey, 0)
			obs.ObservedPubKey = observedPubKey
			obs.Signers = l.signers

			voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
			if l.finalised {
				voter.FinalisedHeight = startHeight
				voter.Tx = obs
			}
			mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
			mgr.Keeper().SetObservedLink(ctx, inhash, outhash)
		}

		return vault, toi
	}

	// The sub-majority window for a single member closes at
	// startHeight + 2*signingTransPeriod; the stale window closes later still,
	// so at this height a majority observation is live and a lone one is not.
	liveHeight := startHeight + 2*signingTransPeriod + 1
	staleHeight := startHeight + staleOutboundObservationSigningPeriods*signingTransPeriod + 1

	strangerKey := GetRandomPubKey()
	unrelatedAmount := cosmos.NewUint(7 * common.One)

	for _, tc := range []struct {
		name    string
		height  int64
		links   []link
		expects bool
	}{
		{
			// The regression: a stray low-quorum link recorded first must not
			// hide the assigned vault's majority observation under a later one.
			name:    "low quorum link ahead of a majority link",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:1]}, {signers: memberSigners[:3]}},
			expects: false,
		},
		{
			name:    "majority link ahead of a low quorum link",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3]}, {signers: memberSigners[:1]}},
			expects: false,
		},
		{
			// Nothing but expired low-quorum claims: the outbound is not in
			// progress and the item must be reassignable.
			name:    "several expired low quorum links",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:1]}, {signers: memberSigners[1:2]}},
			expects: true,
		},
		{
			// Outbound observations come from any active observer, not just
			// vault members, so an outsider quorum saying funds left this
			// vault's address still earns the waiting window - reassigning
			// over it is what pays the destination twice.
			name:    "outsiders only inside the window",
			height:  liveHeight,
			links:   []link{{signers: outsiderSigners}},
			expects: false,
		},
		{
			// That window is bounded: outsiders are not the vault's own word,
			// and once the observation goes stale the item is reassignable.
			name:    "outsiders only once stale",
			height:  staleHeight,
			links:   []link{{signers: outsiderSigners}},
			expects: true,
		},
		{
			// A lone outsider buys only two signing periods, and no more.
			name:    "expired lone outsider",
			height:  liveHeight,
			links:   []link{{signers: outsiderSigners[:1]}},
			expects: true,
		},
		{
			name:    "majority reporting an unrelated destination",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3], toAddress: GetRandomBTCAddress()}},
			expects: true,
		},
		{
			name:    "majority reporting an unrelated amount",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3], amount: &unrelatedAmount}},
			expects: true,
		},
		{
			name:    "majority naming a vault the item is not assigned to",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3], observedPubKey: &strangerKey}},
			expects: true,
		},
		{
			// A non-finalised observation cannot pin the item forever.
			name:    "stale majority",
			height:  staleHeight,
			links:   []link{{signers: memberSigners[:3]}},
			expects: true,
		},
		{
			// A split outbound finalises the parts that did go out; the part
			// still unfulfilled must stay reassignable.
			name:    "finalised sibling of a split outbound",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3], finalised: true}},
			expects: true,
		},
		{
			name:    "a live majority alongside a finalised sibling",
			height:  liveHeight,
			links:   []link{{signers: memberSigners[:3], finalised: true}, {signers: memberSigners[:3]}},
			expects: false,
		},
	} {
		ctx, mgr := setupManagerForTest(c)
		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
		vault, toi := setup(ctx, mgr, tc.links)
		ctx = ctx.WithBlockHeight(tc.height)
		c.Check(
			slasher.needsNewVault(ctx, mgr, vault, signingTransPeriod, startHeight, toi),
			Equals, tc.expects,
			Commentf("case %q", tc.name),
		)
	}
}

func (s *SlashingSuite) TestOutboundObservationAmountMatches(c *C) {
	maxUint, err := cosmos.ParseUint("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	c.Assert(err, IsNil)
	token, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	for _, tc := range []struct {
		name                          string
		asset                         common.Asset
		amount, observed, maxGas, gas cosmos.Uint
		decimals                      int64
		matches                       bool
	}{
		{"exact amount", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(10000), cosmos.NewUint(1000), cosmos.NewUint(500), 8, true},
		{"pool decimal rounding", token, cosmos.NewUint(123456789), cosmos.NewUint(123456700), cosmos.NewUint(1000), cosmos.NewUint(500), 6, true},
		{"wrong token amount", token, cosmos.NewUint(123456789), cosmos.NewUint(123456600), cosmos.NewUint(1000), cosmos.NewUint(500), 6, false},
		{"unused gas paid to recipient", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(10500), cosmos.NewUint(1000), cosmos.NewUint(500), 8, true},
		{"gas overspend with same total", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(9500), cosmos.NewUint(1000), cosmos.NewUint(1500), 8, true},
		{"wrong total spend", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(9499), cosmos.NewUint(1000), cosmos.NewUint(1500), 8, false},
		{"amount exceeds intended spend", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(11001), cosmos.NewUint(1000), cosmos.ZeroUint(), 8, false},
		{"zero gas at intended spend", common.BTCAsset, cosmos.NewUint(10000), cosmos.NewUint(11000), cosmos.NewUint(1000), cosmos.ZeroUint(), 8, true},
		{"largest valid total", common.BTCAsset, maxUint.SubUint64(1000), maxUint.SubUint64(500), cosmos.NewUint(1000), cosmos.NewUint(500), 8, true},
		{"maximum amount plus one gas", common.BTCAsset, cosmos.NewUint(10000), maxUint, cosmos.NewUint(1000), cosmos.OneUint(), 8, false},
		{"one amount plus maximum gas", common.BTCAsset, cosmos.NewUint(10000), cosmos.OneUint(), cosmos.NewUint(1000), maxUint, 8, false},
		{"maximum amount and gas", common.BTCAsset, cosmos.NewUint(10000), maxUint, cosmos.NewUint(1000), maxUint, 8, false},
	} {
		ctx, mgr := setupManagerForTest(c)
		pool := NewPool()
		pool.Asset = tc.asset
		pool.Decimals = tc.decimals
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		gasAsset := tc.asset.GetChain().GetGasAsset()
		toi := TxOutItem{
			Chain:  tc.asset.GetChain(),
			Coin:   common.NewCoin(tc.asset, tc.amount),
			MaxGas: common.Gas{common.NewCoin(gasAsset, tc.maxGas)},
		}
		tx := common.Tx{
			Coins: common.NewCoins(common.NewCoin(tc.asset, tc.observed)),
			Gas:   common.Gas{common.NewCoin(gasAsset, tc.gas)},
		}
		c.Check(outboundObservationAmountMatches(ctx, mgr, tx, toi), Equals, tc.matches, Commentf("case: %s", tc.name))
	}
}

func (s *SlashingSuite) TestLackSigningSurvivesOversizedObservation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(500)

	nodes := make(NodeAccounts, 4)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Decimals = 8
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	signingTransPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	c.Assert(signingTransPeriod > 0, Equals, true)
	const scheduledHeight = int64(302)
	inHash := GetRandomTxHash()
	toi := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		Memo:        "OUT:" + inHash.String(),
	}
	txOut := NewTxOut(scheduledHeight)
	txOut.TxArray = append(txOut.TxArray, toi)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	inVoter := NewObservedTxVoter(inHash, nil)
	inVoter.FinalisedHeight = 1
	inVoter.Actions = []TxOutItem{toi}
	mgr.Keeper().SetObservedTxInVoter(ctx, inVoter)

	// One active node can store a matching report before consensus. Both values
	// are valid Uint amounts, but adding the reported amount and gas overflows.
	maxUint, err := cosmos.ParseUint("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	c.Assert(err, IsNil)
	from, err := vault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(GetRandomTxHash(), from, toi.ToAddress,
		common.NewCoins(common.NewCoin(common.BTCAsset, maxUint)),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())}, toi.Memo)
	msg := NewMsgObservedTxOut(ObservedTxs{NewObservedTx(tx, 100, vault.PubKey, 101)}, nodes[0].NodeAddress)
	c.Assert(msg.ValidateBasic(), IsNil)
	_, err = NewObservedTxOutHandler(mgr).Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().GetObservedLink(ctx, inHash), HasLen, 1)
	outVoter, err := mgr.Keeper().GetObservedTxOutVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(outVoter.Txs, HasLen, 1)
	c.Assert(outVoter.Txs[0].Signers, HasLen, 1)
	c.Assert(outVoter.Height, Equals, int64(0))
	c.Assert(outVoter.UnfinalizedHeight, Equals, int64(0))
	c.Assert(outVoter.FinalisedHeight, Equals, int64(0))

	// Re-read the persisted report at the next signing boundary. It cannot
	// match this outbound or panic the check called from EndBlock.
	ctx = ctx.WithBlockHeight(scheduledHeight + signingTransPeriod)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, signingTransPeriod, inVoter.FinalisedHeight, toi), Equals, true)
	after, err := mgr.Keeper().GetTxOut(ctx, scheduledHeight)
	c.Assert(err, IsNil)
	c.Assert(after.TxArray, HasLen, 1)
	// The replaced batch item proves LackSigning reached and rescheduled it.
	c.Check(after.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

func (s *SlashingSuite) TestRecoverySwapsForSiblingOutboundsDoNotCollide(c *C) {
	for _, advQueue := range []int64{0, 1} {
		c.Logf("EnableAdvSwapQueue=%d", advQueue)
		ctx, mgr := setupManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advQueue)
		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		vault1 := GetRandomVault()
		vault2 := GetRandomVault()

		// A split outbound: one inbound, same memo and destination, two vaults.
		inHash := GetRandomTxHash()
		dest := GetRandomBTCAddress()
		split := []TxOutItem{
			{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault1.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
				Memo:        fmt.Sprintf("OUT:%s", inHash),
			},
			{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault2.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(3*common.One)),
				Memo:        fmt.Sprintf("OUT:%s", inHash),
			},
		}
		// Ragnarok payouts from one iteration share the blank InHash and memo.
		ragnarok := []TxOutItem{
			{
				Chain:       common.BTCChain,
				InHash:      common.BlankTxID,
				VaultPubKey: vault1.PubKey,
				ToAddress:   GetRandomBTCAddress(),
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One)),
				Memo:        NewRagnarokMemo(100).String(),
			},
			{
				Chain:       common.BTCChain,
				InHash:      common.BlankTxID,
				VaultPubKey: vault1.PubKey,
				ToAddress:   GetRandomBTCAddress(),
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(7*common.One)),
				Memo:        NewRagnarokMemo(100).String(),
			},
		}
		// Byte-identical siblings hash to the same TxOutItem hash; the queue
		// key check must still keep both recoveries.
		items := append(append([]TxOutItem{}, split...), ragnarok...)
		items = append(items, ragnarok[1])

		vaults := []Vault{vault1, vault2, vault1, vault1, vault1}
		expected := cosmos.ZeroUint()
		for i, toi := range items {
			memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), toi.Memo)
			c.Assert(err, IsNil)
			c.Assert(memo.IsOutbound(), Equals, true)
			c.Assert(slasher.sendFailedOutboundToTreasury(ctx, mgr, toi, vaults[i], memo), IsNil)
			expected = expected.Add(toi.Coin.Amount)
		}

		swaps := affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper())
		c.Assert(swaps, HasLen, len(items))
		total := cosmos.ZeroUint()
		for _, swap := range swaps {
			total = total.Add(swap.Tx.Coins[0].Amount)
		}
		c.Check(total.String(), Equals, expected.String())
	}
}

// TestLackSigningRecoversEverySplitOutbound drives recovery through LackSigning:
// the legs of one split outbound exhaust MaxOutboundAttempts in the same block,
// and every leg marked handled must leave its own recovery swap behind.
func (s *SlashingSuite) TestLackSigningRecoversEverySplitOutbound(c *C) {
	for _, advQueue := range []int64{0, 1} {
		c.Logf("EnableAdvSwapQueue=%d", advQueue)
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(500)
		mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), 1)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advQueue)

		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.Decimals = 8
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		signingTransPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
		c.Assert(signingTransPeriod > 0, Equals, true)
		scheduledHeight := ctx.BlockHeight() - signingTransPeriod

		inHash := GetRandomTxHash()
		dest := GetRandomBTCAddress()
		txOut := NewTxOut(scheduledHeight)
		expected := cosmos.ZeroUint()
		for _, amt := range []uint64{2, 3} {
			vault := GetRandomVault()
			vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
			toi := TxOutItem{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(amt*common.One)),
				MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
				Memo:        "OUT:" + inHash.String(),
			}
			txOut.TxArray = append(txOut.TxArray, toi)
			expected = expected.Add(toi.Coin.Amount)
		}
		c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
		voter := NewObservedTxVoter(inHash, nil)
		voter.FinalisedHeight = 1
		voter.Actions = txOut.TxArray
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)

		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
		c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

		after, err := mgr.Keeper().GetTxOut(ctx, scheduledHeight)
		c.Assert(err, IsNil)
		c.Assert(after.TxArray, HasLen, 2)
		for _, toi := range after.TxArray {
			c.Check(toi.OutHash.Equals(common.BlankTxID), Equals, true)
		}

		swaps := affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper())
		c.Assert(swaps, HasLen, 2)
		total := cosmos.ZeroUint()
		for _, swap := range swaps {
			total = total.Add(swap.Tx.Coins[0].Amount)
		}
		c.Check(total.String(), Equals, expected.String())
	}
}

// TestReverseSwapRecoveriesForSiblingsDoNotCollide covers the reverse_swap
// recovery type: the split legs of a failed swap outbound each queue their own
// reverse swap, and a failed refund of that swap, which would reverse-swap
// into its own asset, falls back to treasury recovery.
func (s *SlashingSuite) TestReverseSwapRecoveriesForSiblingsDoNotCollide(c *C) {
	for _, advQueue := range []int64{0, 1} {
		c.Logf("EnableAdvSwapQueue=%d", advQueue)
		ctx, mgr := setupManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advQueue)
		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

		for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
			pool := NewPool()
			pool.Asset = asset
			pool.BalanceRune = cosmos.NewUint(1000 * common.One)
			pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
			pool.Status = PoolAvailable
			c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		}

		// Original swap: ETH -> BTC.
		inHash := GetRandomTxHash()
		sender := GetRandomETHAddress()
		dest := GetRandomBTCAddress()
		observed := ObservedTx{
			Tx: common.Tx{
				ID:          inHash,
				Chain:       common.ETHChain,
				FromAddress: sender,
				ToAddress:   GetRandomETHAddress(),
				Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
				Memo:        fmt.Sprintf("=:BTC.BTC:%s", dest),
				Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			FinaliseHeight: ctx.BlockHeight(),
		}
		voter := NewObservedTxVoter(inHash, []ObservedTx{observed})
		voter.FinalisedHeight = ctx.BlockHeight()
		voter.Tx = observed
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)

		vault1 := GetRandomVault()
		vault2 := GetRandomVault()
		items := []TxOutItem{
			{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault1.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
				Memo:        fmt.Sprintf("OUT:%s", inHash),
			},
			{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault2.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(3*common.One)),
				Memo:        fmt.Sprintf("OUT:%s", inHash),
			},
			{
				Chain:       common.ETHChain,
				InHash:      inHash,
				VaultPubKey: vault1.PubKey,
				ToAddress:   sender,
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(4*common.One)),
				Memo:        fmt.Sprintf("REFUND:%s", inHash),
			},
		}
		vaults := []Vault{vault1, vault2, vault1}
		for i, toi := range items {
			memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), toi.Memo)
			c.Assert(err, IsNil)
			c.Assert(slasher.sendFailedOutboundToTreasury(ctx, mgr, toi, vaults[i], memo), IsNil)
		}

		swaps := affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper())
		c.Assert(swaps, HasLen, len(items))
		reversed := cosmos.ZeroUint()
		for _, swap := range swaps {
			c.Check(swap.Tx.Coins[0].Asset.Equals(swap.TargetAsset), Equals, false)
			switch {
			case swap.TargetAsset.Equals(common.ETHAsset):
				c.Check(swap.Destination.Equals(sender), Equals, true)
				reversed = reversed.Add(swap.Tx.Coins[0].Amount)
			case swap.Tx.Coins[0].Asset.Equals(common.ETHAsset):
				c.Check(swap.TargetAsset.IsRune(), Equals, true)
				c.Check(swap.Destination.Equals(common.TreasuryAddress), Equals, true)
			default:
				c.Errorf("unexpected recovery swap %s -> %s", swap.Tx.Coins[0].Asset, swap.TargetAsset)
			}
		}
		c.Check(reversed.String(), Equals, cosmos.NewUint(5*common.One).String())
	}
}

// TestRecoverySwapIDExhaustionDoesNotOverwrite fills every nonce slot for one
// byte-identical TxOutItem: the next recovery must fail rather than overwrite a
// queued swap, leaving LackSigning to reschedule the item for a later retry.
func (s *SlashingSuite) TestRecoverySwapIDExhaustionDoesNotOverwrite(c *C) {
	for _, advQueue := range []int64{0, 1} {
		c.Logf("EnableAdvSwapQueue=%d", advQueue)
		ctx, mgr := setupManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advQueue)
		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		vault := GetRandomVault()
		toi := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      common.BlankTxID,
			VaultPubKey: vault.PubKey,
			ToAddress:   GetRandomBTCAddress(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			Memo:        NewRagnarokMemo(100).String(),
		}
		memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), toi.Memo)
		c.Assert(err, IsNil)

		for i := 0; i < maxRecoveryTxIDNonce; i++ {
			c.Assert(slasher.sendFailedOutboundToTreasury(ctx, mgr, toi, vault, memo), IsNil)
		}
		err = slasher.sendFailedOutboundToTreasury(ctx, mgr, toi, vault, memo)
		c.Assert(err, NotNil)
		c.Check(err, ErrorMatches, "no free treasury recovery swap id.*")

		swaps := affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper())
		c.Check(swaps, HasLen, maxRecoveryTxIDNonce)
	}
}

// TestLackSigningRetriesRecoveryAfterIDExhaustion queues more byte-identical
// items than there are recovery IDs per height: the overflow item must be
// rescheduled rather than stranded, and recovered at the next signing period.
// For a failed swap outbound the overflow must still refund the original
// sender, not fall back to the treasury while reverse_swap IDs are exhausted.
func (s *SlashingSuite) TestLackSigningRetriesRecoveryAfterIDExhaustion(c *C) {
	for _, reverseSwap := range []bool{false, true} {
		for _, advQueue := range []int64{0, 1} {
			c.Logf("reverseSwap=%t EnableAdvSwapQueue=%d", reverseSwap, advQueue)
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(500)
			mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), 1)
			mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advQueue)

			for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
				pool := NewPool()
				pool.Asset = asset
				pool.Decimals = 8
				pool.BalanceRune = cosmos.NewUint(1000 * common.One)
				pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
				pool.Status = PoolAvailable
				c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
			}

			signingTransPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
			c.Assert(signingTransPeriod > 0, Equals, true)
			scheduledHeight := ctx.BlockHeight() - signingTransPeriod

			vault := GetRandomVault()
			vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000*common.One))}
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

			inHash := GetRandomTxHash()
			sender := GetRandomETHAddress()
			dest := GetRandomBTCAddress()
			toi := TxOutItem{
				Chain:       common.BTCChain,
				InHash:      inHash,
				VaultPubKey: vault.PubKey,
				ToAddress:   dest,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
				MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
				Memo:        "OUT:" + inHash.String(),
			}
			txOut := NewTxOut(scheduledHeight)
			for i := 0; i <= maxRecoveryTxIDNonce; i++ {
				txOut.TxArray = append(txOut.TxArray, toi)
			}
			c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
			voter := NewObservedTxVoter(inHash, nil)
			if reverseSwap {
				// Original swap: ETH -> BTC.
				observed := ObservedTx{
					Tx: common.Tx{
						ID:          inHash,
						Chain:       common.ETHChain,
						FromAddress: sender,
						ToAddress:   GetRandomETHAddress(),
						Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One))},
						Memo:        fmt.Sprintf("=:BTC.BTC:%s", dest),
						Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
					},
					Status:         common.Status_done,
					BlockHeight:    1,
					FinaliseHeight: 1,
				}
				voter = NewObservedTxVoter(inHash, []ObservedTx{observed})
				voter.Tx = observed
			}
			voter.FinalisedHeight = 1
			voter.Actions = txOut.TxArray
			mgr.Keeper().SetObservedTxInVoter(ctx, voter)

			slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
			c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

			// Every item is handled: maxRecoveryTxIDNonce recovered, one rescheduled.
			after, err := mgr.Keeper().GetTxOut(ctx, scheduledHeight)
			c.Assert(err, IsNil)
			c.Assert(after.TxArray, HasLen, maxRecoveryTxIDNonce+1)
			for _, item := range after.TxArray {
				c.Check(item.OutHash.Equals(common.BlankTxID), Equals, true)
			}
			c.Check(affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper()), HasLen, maxRecoveryTxIDNonce)

			retryHeight := ctx.BlockHeight()
			retry, err := mgr.Keeper().GetTxOut(ctx, retryHeight)
			c.Assert(err, IsNil)
			c.Assert(retry.TxArray, HasLen, 1)
			c.Check(retry.TxArray[0].OutHash.IsEmpty(), Equals, true)
			c.Check(retry.TxArray[0].Coin.Equals(toi.Coin), Equals, true)

			// Next signing period: the retry recovers with fresh, height-scoped IDs.
			ctx = ctx.WithBlockHeight(retryHeight + signingTransPeriod)
			c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

			retry, err = mgr.Keeper().GetTxOut(ctx, retryHeight)
			c.Assert(err, IsNil)
			c.Assert(retry.TxArray, HasLen, 1)
			c.Check(retry.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
			swaps := affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper())
			c.Assert(swaps, HasLen, maxRecoveryTxIDNonce+1)
			total := cosmos.ZeroUint()
			for _, swap := range swaps {
				total = total.Add(swap.Tx.Coins[0].Amount)
				if reverseSwap {
					c.Check(swap.TargetAsset.Equals(common.ETHAsset), Equals, true)
					c.Check(swap.Destination.Equals(sender), Equals, true)
				} else {
					c.Check(swap.TargetAsset.IsRune(), Equals, true)
					c.Check(swap.Destination.Equals(common.TreasuryAddress), Equals, true)
				}
			}
			c.Check(total.String(), Equals, cosmos.NewUint(uint64(maxRecoveryTxIDNonce+1)*common.One).String())

			// Nothing is left to retry at the next height.
			next, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
			c.Assert(err, IsNil)
			c.Check(next.TxArray, HasLen, 0)
		}
	}
}
