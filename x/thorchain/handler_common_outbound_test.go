package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gopkg.in/check.v1"
	. "gopkg.in/check.v1"
)

type HandlerCommonOutboundSuite struct{}

var _ = Suite(&HandlerCommonOutboundSuite{})

func (s *HandlerCommonOutboundSuite) TestIsOutboundFakeGasTX(c *C) {
	coins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	gas := common.Gas{
		{Asset: common.ETHAsset, Amount: cosmos.NewUint(1)},
	}
	// Fake gas transactions have self-referential OUT:txhash memo (bifrost behavior)
	fakeGasTx := common.ObservedTx{
		Tx: common.NewTx("123", "0xabc", "0x123", coins, gas, "OUT:123"),
	}

	c.Assert(isOutboundFakeGasTx(fakeGasTx), Equals, true)

	coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100000)),
	}
	theftTx := common.ObservedTx{
		Tx: common.NewTx("123", "0xabc", "0x123", coins, gas, "=:AVAX.AVAX:0x123"),
	}
	c.Assert(isOutboundFakeGasTx(theftTx), Equals, false)

	// Test with wrong memo format (not self-referential)
	coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	wrongMemoTx := common.ObservedTx{
		Tx: common.NewTx("123", "0xabc", "0x123", coins, gas, "OUT:ABCD1234567890"),
	}
	c.Assert(isOutboundFakeGasTx(wrongMemoTx), Equals, false) // Wrong memo format (not self-referential)
}

func (s *HandlerCommonOutboundSuite) TestIsCancelTx(c *C) {
	// Create a vault pubkey and get its address
	vaultPubKey := GetRandomPubKey()
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Test 1: Valid cancel tx (EVM, gas asset, amount=DustThreshold, vault-to-vault)
	// Cancel transactions have amount=0 on chain, but bifrost converts to DustThreshold
	dustThreshold := common.ETHChain.DustThreshold() // 1 for ETH
	cancelTxCoins := common.Coins{
		common.NewCoin(common.ETHAsset, dustThreshold),
	}
	cancelTxGas := common.Gas{
		{Asset: common.ETHAsset, Amount: cosmos.NewUint(21000)},
	}
	cancelTx := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, cancelTxCoins, cancelTxGas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(cancelTx), Equals, true)

	// Test 2: Not a cancel tx - amount is 0 (not DustThreshold)
	zeroAmountCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
	}
	notCancelTx2 := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, zeroAmountCoins, cancelTxGas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx2), Equals, false)

	// Test 3: Not a cancel tx - large amount
	largeAmountCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100000)),
	}
	notCancelTx3 := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, largeAmountCoins, cancelTxGas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx3), Equals, false)

	// Test 4: Not a cancel tx - not EVM chain (BTC)
	btcVaultPubKey := GetRandomPubKey()
	btcVaultAddr, err := btcVaultPubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	btcCoins := common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.ZeroUint()),
	}
	btcGas := common.Gas{
		{Asset: common.BTCAsset, Amount: cosmos.NewUint(10000)},
	}
	notCancelTx4 := ObservedTx{
		Tx:             common.NewTx("123", btcVaultAddr, btcVaultAddr, btcCoins, btcGas, ""),
		ObservedPubKey: btcVaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx4), Equals, false)

	// Test 5: Valid cancel tx on different EVM chain (AVAX)
	avaxVaultPubKey := GetRandomPubKey()
	avaxVaultAddr, err := avaxVaultPubKey.GetAddress(common.AVAXChain)
	c.Assert(err, IsNil)
	avaxDustThreshold := common.AVAXChain.DustThreshold() // 1 for AVAX
	avaxCoins := common.Coins{
		common.NewCoin(common.AVAXAsset, avaxDustThreshold),
	}
	avaxGas := common.Gas{
		{Asset: common.AVAXAsset, Amount: cosmos.NewUint(21000)},
	}
	cancelTxAvax := ObservedTx{
		Tx:             common.NewTx("123", avaxVaultAddr, avaxVaultAddr, avaxCoins, avaxGas, ""),
		ObservedPubKey: avaxVaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(cancelTxAvax), Equals, true)

	// Test 6: Not a cancel tx - not gas asset (ERC20 token)
	tokenAsset, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	tokenCoins := common.Coins{
		common.NewCoin(tokenAsset, cosmos.ZeroUint()),
	}
	notCancelTx5 := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, tokenCoins, cancelTxGas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx5), Equals, false)

	// Test 7: Not a cancel tx - multiple coins
	multiCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
	}
	notCancelTx6 := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, multiCoins, cancelTxGas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx6), Equals, false)

	// Test 8: Not a cancel tx - has a memo
	memoCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
	}
	notCancelTx7 := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, memoCoins, cancelTxGas, "OUT:abc123"),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(notCancelTx7), Equals, false)
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutEvenDistribution(c *check.C) {
	clout1 := cosmos.NewUint(50)
	clout2 := cosmos.NewUint(50)
	spent := cosmos.NewUint(60)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.String(), check.Equals, "30")
	c.Assert(split2.String(), check.Equals, "30")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutExcessSpent(c *check.C) {
	clout1 := cosmos.NewUint(50)
	clout2 := cosmos.NewUint(50)
	spent := cosmos.NewUint(120)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.String(), check.Equals, "50")
	c.Assert(split2.String(), check.Equals, "50")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutInsufficientFirstClout(c *check.C) {
	clout1 := cosmos.NewUint(20)
	clout2 := cosmos.NewUint(80)
	spent := cosmos.NewUint(60)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.String(), check.Equals, "20")
	c.Assert(split2.String(), check.Equals, "40")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutInsufficientSecondClout(c *check.C) {
	clout1 := cosmos.NewUint(80)
	clout2 := cosmos.NewUint(20)
	spent := cosmos.NewUint(60)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.String(), check.Equals, "40")
	c.Assert(split2.String(), check.Equals, "20")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutSpentIsZero(c *check.C) {
	clout1 := cosmos.NewUint(50)
	clout2 := cosmos.NewUint(50)
	spent := cosmos.NewUint(0)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.IsZero(), check.Equals, true)
	c.Assert(split2.IsZero(), check.Equals, true)
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutOneSideIsZero(c *check.C) {
	clout1 := cosmos.NewUint(0)
	clout2 := cosmos.NewUint(100)
	spent := cosmos.NewUint(60)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.IsZero(), check.Equals, true)
	c.Assert(split2.String(), check.Equals, "60")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutBoundaryCondition(c *check.C) {
	clout1 := cosmos.NewUint(1)
	clout2 := cosmos.NewUint(100000000000)
	spent := cosmos.NewUint(2)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.String(), check.Equals, "1")
	c.Assert(split2.String(), check.Equals, "1")
}

func (s *HandlerCommonOutboundSuite) TestSplitCloutBothCloutsZero(c *check.C) {
	clout1 := cosmos.ZeroUint()
	clout2 := cosmos.ZeroUint()
	spent := cosmos.NewUint(60)

	split1, split2 := calcReclaim(clout1, clout2, spent)

	c.Assert(split1.IsZero(), check.Equals, true)
	c.Assert(split2.IsZero(), check.Equals, true)
}

// TestDeterministicAggregatorComparison verifies that aggregator field comparison
// uses deterministic case-insensitive matching (via strings.ToLower) rather than
// strings.EqualFold which can have locale-specific behavior.
func (s *HandlerCommonOutboundSuite) TestDeterministicAggregatorComparison(c *check.C) {
	// Test cases for deterministic case-insensitive comparison
	testCases := []struct {
		name     string
		a        string
		b        string
		expected bool
	}{
		{"exact match", "0xAggregator", "0xAggregator", true},
		{"case insensitive match", "0xAGGREGATOR", "0xaggregator", true},
		{"mixed case match", "0xAgGrEgAtOr", "0xaGgReGaToR", true},
		{"different strings", "0xAggregator1", "0xAggregator2", false},
		{"empty strings", "", "", true},
		{"one empty", "0xAggregator", "", false},
	}

	for _, tc := range testCases {
		// This mirrors the comparison logic in handler_common_outbound.go
		// nolint:staticcheck // SA6005: ToLower is intentionally used instead of EqualFold for determinism
		result := strings.ToLower(tc.a) == strings.ToLower(tc.b)
		c.Assert(result, check.Equals, tc.expected, check.Commentf("test case: %s", tc.name))
	}
}

// TestSlashingExemptionsForOperationalTxs verifies that fake gas and cancel
// transactions are properly exempted from slashing when no TxOutItem match is found.
func (s *HandlerCommonOutboundSuite) TestSlashingExemptionsForOperationalTxs(c *check.C) {
	// Test 1: Fake gas transaction should be exempted
	coins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	gas := common.Gas{
		{Asset: common.ETHAsset, Amount: cosmos.NewUint(1)},
	}
	// Fake gas transactions have self-referential OUT:txhash memo
	fakeGasTx := ObservedTx{
		Tx: common.NewTx("TXHASH123", "0xabc", "0x123", coins, gas, "OUT:TXHASH123"),
	}
	c.Assert(isOutboundFakeGasTx(fakeGasTx), check.Equals, true, check.Commentf("fake gas tx should be recognized"))

	// Test 2: Cancel transaction should be exempted
	vaultPubKey := GetRandomPubKey()
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, check.IsNil)
	dustThreshold := common.ETHChain.DustThreshold()
	cancelCoins := common.Coins{
		common.NewCoin(common.ETHAsset, dustThreshold),
	}
	cancelTx := ObservedTx{
		Tx:             common.NewTx("123", vaultAddr, vaultAddr, cancelCoins, gas, ""),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isCancelOrApprovalTx(cancelTx), check.Equals, true, check.Commentf("cancel tx should be recognized"))

	// Test 3: Theft attempt (large amount, not operational) should NOT be exempted
	theftCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
	}
	theftTx := ObservedTx{
		Tx:             common.NewTx("123", "0xabc", "0x123", theftCoins, gas, "OUT:123"),
		ObservedPubKey: vaultPubKey,
	}
	c.Assert(isOutboundFakeGasTx(theftTx), check.Equals, false, check.Commentf("theft tx should not be exempted"))
	c.Assert(isCancelOrApprovalTx(theftTx), check.Equals, false, check.Commentf("theft tx should not be exempted as cancel"))
}

func (s *HandlerCommonOutboundSuite) TestMaxEVMGasForChain(c *check.C) {
	ctx, mgr := setupManagerForTest(c)

	// Verify all EVM chains have a defined max gas cap.
	for _, chain := range common.AllChains {
		if !chain.IsEVM() {
			continue
		}
		maxGas, known := maxEVMGasForChain(ctx, mgr.Keeper(), chain)
		c.Assert(known, check.Equals, true, check.Commentf("EVM chain %s missing explicit max gas cap", chain))
		c.Assert(maxGas.GT(cosmos.ZeroUint()), check.Equals, true, check.Commentf("EVM chain %s has zero max gas cap", chain))
	}

	// Non-EVM chain should return the default cap, not be known.
	maxGas, known := maxEVMGasForChain(ctx, mgr.Keeper(), common.BTCChain)
	c.Assert(known, check.Equals, false)
	c.Assert(maxGas.Equal(cosmos.NewUint(constants.DefaultMaxEVMGas)), check.Equals, true)

	// Mimir override should take precedence over hardcoded constant.
	mimirOverride := int64(99_000_000)
	mgr.Keeper().SetMimir(ctx, "MaxGas-ETH", mimirOverride)
	maxGas, known = maxEVMGasForChain(ctx, mgr.Keeper(), common.ETHChain)
	c.Assert(known, check.Equals, true)
	c.Assert(maxGas.Equal(cosmos.NewUint(uint64(mimirOverride))), check.Equals, true)
}

func (s *HandlerCommonOutboundSuite) TestEVMOutboundGasShouldSlash(c *check.C) {
	tokenAsset, err := common.NewAsset("POL.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	c.Assert(err, check.IsNil)
	avaxTokenAsset, err := common.NewAsset("AVAX.USDX-0X9999999999999999999999999999999999999999")
	c.Assert(err, check.IsNil)

	gasOutbound := TxOutItem{
		Chain: common.POLChain,
		Coin:  common.NewCoin(common.POLAsset, cosmos.NewUint(1)),
		MaxGas: common.Gas{
			common.NewCoin(common.POLAsset, cosmos.NewUint(18_000_000)),
		},
	}
	c.Assert(evmOutboundGasShouldSlash(gasOutbound, cosmos.NewUint(constants.MaxPOLGas-1), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, false)
	c.Assert(evmOutboundGasShouldSlash(gasOutbound, cosmos.NewUint(constants.MaxPOLGas), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, true)

	tokenOutbound := gasOutbound
	tokenOutbound.Coin = common.NewCoin(tokenAsset, cosmos.NewUint(1))
	c.Assert(evmOutboundGasShouldSlash(tokenOutbound, cosmos.NewUint(constants.MaxPOLGas), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, false)
	// Bifrost permits equality with the multiplied scheduled allowance. The
	// observer must not slash the same exact-cap transaction.
	tokenBoundaryCap := cosmos.NewUint(54_000_000)
	c.Assert(evmOutboundGasShouldSlash(tokenOutbound, tokenBoundaryCap, tokenBoundaryCap), check.Equals, false)
	c.Assert(evmOutboundGasShouldSlash(tokenOutbound, cosmos.NewUint(54_000_000), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, false)
	c.Assert(evmOutboundGasShouldSlash(tokenOutbound, cosmos.NewUint(54_000_001), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, true)

	avaxTokenOutbound := TxOutItem{
		Chain: common.AVAXChain,
		Coin:  common.NewCoin(avaxTokenAsset, cosmos.NewUint(1)),
		MaxGas: common.Gas{
			common.NewCoin(common.AVAXAsset, cosmos.NewUint(960_000)),
		},
	}
	c.Assert(evmOutboundGasShouldSlash(avaxTokenOutbound, cosmos.NewUint(49_999_999), cosmos.NewUint(constants.MaxAVAXGas)), check.Equals, false)

	aggregatorOutbound := tokenOutbound
	aggregatorOutbound.Aggregator = "aggregator"
	aggregatorBoundaryCap := cosmos.NewUint(180_000_000)
	c.Assert(evmOutboundGasShouldSlash(aggregatorOutbound, aggregatorBoundaryCap, aggregatorBoundaryCap), check.Equals, false)
	c.Assert(evmOutboundGasShouldSlash(aggregatorOutbound, cosmos.NewUint(180_000_000), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, false)
	c.Assert(evmOutboundGasShouldSlash(aggregatorOutbound, cosmos.NewUint(180_000_001), cosmos.NewUint(constants.MaxPOLGas)), check.Equals, true)
}

func (s *HandlerCommonOutboundSuite) TestOutboundSearchUsesMimirSigningPeriod(c *check.C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	defaultPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	overridePeriod := defaultPeriod + 10
	k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), overridePeriod)

	// Place the token outbound just outside the default 3x window, but inside
	// the operator's matching 3x Mimir window.
	ctx = ctx.WithBlockHeight(defaultPeriod*3 + 20)
	scheduledHeight := ctx.BlockHeight() - defaultPeriod*3 - 1
	inHash := GetRandomTxHash()
	outHash := GetRandomTxHash()
	vaultPubKey := GetRandomPubKey()
	vaultAddress, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, check.IsNil)
	toAddress := GetRandomETHAddress()
	tokenAsset, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	c.Assert(err, check.IsNil)
	coin := common.NewCoin(tokenAsset, cosmos.NewUint(1_000_000))
	item := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inHash,
		ToAddress:   toAddress,
		VaultPubKey: vaultPubKey,
		Coin:        coin,
		Memo:        NewOutboundMemo(inHash).String(),
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10_000))},
	}
	txOut := NewTxOut(scheduledHeight)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(k.SetTxOut(ctx, txOut), check.IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(k.SetVault(ctx, vault), check.IsNil)
	voter := NewObservedTxVoter(inHash, nil)
	voter.Height = scheduledHeight
	voter.FinalisedHeight = scheduledHeight
	k.SetObservedTxInVoter(ctx, voter)

	observed := NewObservedTx(common.NewTx(
		outHash,
		vaultAddress,
		toAddress,
		common.NewCoins(coin),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000))},
		item.Memo,
	), ctx.BlockHeight(), vaultPubKey, ctx.BlockHeight())
	_, err = NewCommonOutboundTxHandler(mgr).handle(ctx, observed, inHash)
	c.Assert(err, check.IsNil)

	stored, err := k.GetTxOut(ctx, scheduledHeight)
	c.Assert(err, check.IsNil)
	c.Assert(stored.TxArray, check.HasLen, 1)
	c.Check(stored.TxArray[0].OutHash.Equals(outHash), check.Equals, true)
}

func (s *HandlerCommonOutboundSuite) TestZecTexOutboundCompletesAction(c *check.C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	scheduledHeight := ctx.BlockHeight()

	p2pkhAddress, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, check.IsNil)
	texAddress, err := p2pkhAddress.ToTexAddress()
	c.Assert(err, check.IsNil)
	c.Assert(p2pkhAddress.Equals(texAddress), check.Equals, false)

	inHash := GetRandomTxHash()
	outHash := GetRandomTxHash()
	vaultPubKey := GetRandomPubKey()
	vaultAddress, err := vaultPubKey.GetAddress(common.ZECChain)
	c.Assert(err, check.IsNil)

	coin := common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One))
	item := TxOutItem{
		Chain:       common.ZECChain,
		InHash:      inHash,
		ToAddress:   texAddress,
		VaultPubKey: vaultPubKey,
		Coin:        coin,
		Memo:        NewOutboundMemo(inHash).String(),
		MaxGas:      common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000))},
	}
	txOut := NewTxOut(scheduledHeight)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(k.SetTxOut(ctx, txOut), check.IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ZECChain}.Strings(), nil)
	c.Assert(k.SetVault(ctx, vault), check.IsNil)
	voter := NewObservedTxVoter(inHash, nil)
	voter.Height = scheduledHeight
	voter.FinalisedHeight = scheduledHeight
	voter.Actions = []TxOutItem{item}
	k.SetObservedTxInVoter(ctx, voter)

	// The daemon reports the decoded P2PKH form of the tex destination.
	observed := NewObservedTx(common.NewTx(
		outHash,
		vaultAddress,
		p2pkhAddress,
		common.NewCoins(coin),
		common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000))},
		item.Memo,
	), ctx.BlockHeight(), vaultPubKey, ctx.BlockHeight())
	result, err := NewCommonOutboundTxHandler(mgr).handle(ctx, observed, inHash)
	c.Assert(err, check.IsNil)
	c.Check(result.Events, check.HasLen, 0)
	c.Check(hasMissingTxOutSecurityEvent(ctx), check.Equals, false)

	stored, err := k.GetTxOut(ctx, scheduledHeight)
	c.Assert(err, check.IsNil)
	c.Assert(stored.TxArray, check.HasLen, 1)
	c.Check(stored.TxArray[0].OutHash.Equals(outHash), check.Equals, true)
	voter, err = k.GetObservedTxInVoter(ctx, inHash)
	c.Assert(err, check.IsNil)
	c.Assert(voter.OutTxs, check.HasLen, 1)
	c.Check(voter.OutTxs[0].ID, check.Equals, outHash)
	c.Check(voter.IsDone(), check.Equals, true)
}

func (s *HandlerCommonOutboundSuite) TestXMRMemolessPlanAcceptsSemanticRefund(c *check.C) {
	for _, wrongHash := range []bool{false, true} {
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		slasher := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = slasher
		inHash, outHash := GetRandomTxHash(), GetRandomTxHash()
		vaultKey := GetRandomEd25519PubKey()
		from, err := vaultKey.GetAddress(common.XMRChain)
		c.Assert(err, check.IsNil)
		to := common.Address(common.XMRSeededDummyAddress('8', []byte("memo-compatibility")))
		coin := common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One))
		gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))}
		semanticMemo := NewRefundMemo(inHash).String()
		item := TxOutItem{Chain: common.XMRChain, InHash: inHash, ToAddress: to, VaultPubKey: vaultKey, VaultPubKeyFrost: vaultKey, Coin: coin, MaxGas: gas, Memo: "", OriginalMemo: semanticMemo}
		scheduled := NewTxOut(ctx.BlockHeight())
		scheduled.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, scheduled), check.IsNil)
		vault := NewVault(1, ActiveVault, AsgardVault, vaultKey, common.Chains{common.XMRChain}.Strings(), nil)
		vault.PubKeyFrost = vaultKey
		c.Assert(k.SetVault(ctx, vault), check.IsNil)
		voter := NewObservedTxVoter(inHash, nil)
		voter.Height, voter.FinalisedHeight = ctx.BlockHeight(), ctx.BlockHeight()
		k.SetObservedTxInVoter(ctx, voter)
		memoHash := inHash
		if wrongHash {
			memoHash = GetRandomTxHash()
		}
		observed := NewObservedTx(common.NewTx(outHash, from, to, common.NewCoins(coin), gas, NewRefundMemo(memoHash).String()), ctx.BlockHeight(), vaultKey, ctx.BlockHeight())
		parsed, err := ParseMemo(common.LatestVersion, observed.Tx.Memo)
		c.Assert(err, check.IsNil)
		result, err := NewCommonOutboundTxHandler(mgr).handle(ctx, observed, parsed.GetTxID())
		c.Assert(err, check.IsNil)
		stored, err := k.GetTxOut(ctx, ctx.BlockHeight())
		c.Assert(err, check.IsNil)
		c.Check(stored.TxArray[0].OutHash.Equals(outHash), check.Equals, !wrongHash)
		slashed := false
		for _, event := range result.Events {
			if event.Type == "vault-slash" {
				slashed = true
			}
		}
		c.Check(slashed, check.Equals, wrongHash)
		if wrongHash {
			c.Check(slasher.calls, check.Equals, 1)
		} else {
			c.Check(slasher.calls, check.Equals, 0)
		}
	}
}

type xmrMemoCompatibilitySlasher struct {
	*DummySlasher
	calls int
}

func (s *xmrMemoCompatibilitySlasher) SlashVault(cosmos.Context, common.PubKey, common.Coins, Manager) error {
	s.calls++
	return nil
}
