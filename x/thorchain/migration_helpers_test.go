package thorchain

import (
	"fmt"

	math "cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	tcyclaimlist "gitlab.com/thorchain/thornode/v3/common/tcyclaimlist"
)

type MigrationHelpersTestSuite struct{}

var _ = Suite(&MigrationHelpersTestSuite{})

// TestUnsafeAddRefundOutbound ensures migration refunds use the same
// compromised-vault exclusion as regular outbound scheduling, even though this
// helper selects an Active Asgard directly.
func (MigrationHelpersTestSuite) TestUnsafeAddRefundOutbound(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// add a compromised vault
	vault := GetRandomVault()
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	vault.Coins = common.Coins{
		// common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// add a safe vault for the helper to select
	safeVault := GetRandomVault()
	safeVault.PubKeyFrost = GetRandomEd25519PubKey()
	safeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, safeVault), IsNil)

	// add node
	acc1 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)

	// Create inbound
	inTxID := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	vaultAddr, err := vault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	coin := common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))
	height := w.ctx.BlockHeight()

	tx := common.Tx{
		ID:          inTxID,
		Chain:       common.ETHChain,
		FromAddress: ethAddr,
		ToAddress:   vaultAddr,
		Coins:       common.Coins{coin},
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "bad memo",
	}

	voter := NewObservedTxVoter(inTxID, ObservedTxs{
		ObservedTx{
			Tx:             tx,
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	mgr, ok := w.mgr.(*Mgrs)
	c.Assert(ok, Equals, true)
	w.keeper.SetMimir(w.ctx, compromisedVaultMimirKeyPrefix+vault.PubKey.String(), 1)

	// add outbound using migration helper
	err = unsafeAddRefundOutbound(w.ctx, mgr, inTxID.String(), ethAddr.String(), coin, height)
	c.Assert(err, IsNil)

	items, err := w.mgr.TxOutStore().GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain, Equals, common.ETHChain)
	c.Assert(items[0].InHash.Equals(inTxID), Equals, true)
	c.Assert(items[0].ToAddress.Equals(ethAddr), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(vault.PubKey), Equals, false)
	c.Assert(items[0].VaultPubKey.Equals(safeVault.PubKey), Equals, true)
	c.Assert(items[0].VaultPubKeyEddsa.Equals(safeVault.PubKeyEddsa), Equals, true)
	c.Assert(items[0].VaultPubKeyFrost.Equals(safeVault.PubKeyFrost), Equals, true)
	c.Assert(items[0].Coin.Equals(coin), Equals, true)
}

// TestRequeueDanglingSettlementOutboundERC20 covers the common stuck ETH token
// case: reuse the existing voter action, move it from an old vault to a safe
// active vault, refresh gas fields, and leave the net outbound amount unchanged.
func (MigrationHelpersTestSuite) TestRequeueDanglingSettlementOutboundERC20(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	ctx := w.ctx.WithBlockHeight(1234)
	mgr, ok := w.mgr.(*Mgrs)
	c.Assert(ok, Equals, true)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	maxGas := saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)

	compromisedVault := GetRandomVault()
	compromisedVault.PubKeyFrost = GetRandomEd25519PubKey()
	compromisedVault.Coins = common.Coins{
		common.NewCoin(ethUSDC, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(common.ETHAsset, maxGas.Amount.Add(cosmos.NewUint(100))),
	}
	c.Assert(w.keeper.SetVault(ctx, compromisedVault), IsNil)
	w.keeper.SetMimir(ctx, compromisedVaultMimirKeyPrefix+compromisedVault.PubKey.String(), 1)

	safeVault := GetRandomVault()
	safeVault.PubKeyFrost = GetRandomEd25519PubKey()
	safeVault.Coins = common.Coins{
		common.NewCoin(ethUSDC, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, maxGas.Amount.Add(cosmos.NewUint(100))),
	}
	c.Assert(w.keeper.SetVault(ctx, safeVault), IsNil)

	oldVault := GetRandomVault()
	oldVault.Status = InactiveVault
	oldVault.PubKeyFrost = GetRandomEd25519PubKey()
	txID := GetRandomTxHash()
	amount := cosmos.NewUint(50 * common.One)
	action := TxOutItem{
		Chain:            common.ETHChain,
		InHash:           txID,
		ToAddress:        GetRandomETHAddress(),
		VaultPubKey:      oldVault.PubKey,
		VaultPubKeyEddsa: txOutVaultPubKeyEddsa(common.ETHChain, oldVault.PubKeyEddsa),
		VaultPubKeyFrost: oldVault.PubKeyFrost,
		Coin:             common.NewCoin(ethUSDC, amount),
		MaxGas:           common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		GasRate:          1,
		Memo:             "SECURE-:ETH.USDC",
	}
	voter := NewObservedTxVoter(txID, nil)
	voter.Actions = []TxOutItem{action}
	voter.FinalisedHeight = 99
	w.keeper.SetObservedTxInVoter(ctx, voter)

	spec := danglingSettlementSpec{
		TxID:   txID,
		Chain:  common.ETHChain,
		Asset:  ethUSDC,
		Amount: amount,
	}
	c.Assert(requeueDanglingSettlementOutbound(ctx, mgr, spec, ctx.BlockHeight()), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].InHash.Equals(txID), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(safeVault.PubKey), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(compromisedVault.PubKey), Equals, false)
	c.Assert(items[0].VaultPubKeyEddsa.Equals(safeVault.PubKeyEddsa), Equals, true)
	c.Assert(items[0].VaultPubKeyFrost.Equals(safeVault.PubKeyFrost), Equals, true)
	c.Assert(items[0].Coin.Equals(action.Coin), Equals, true)
	c.Assert(items[0].MaxGas.Equals(common.Gas{maxGas}), Equals, true)
	c.Assert(items[0].GasRate, Equals, int64(mgr.GasMgr().GetGasRate(ctx, common.ETHChain).Uint64()))

	updated, err := w.keeper.GetObservedTxInVoter(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(updated.FinalisedHeight, Equals, ctx.BlockHeight())
	c.Assert(updated.Actions, HasLen, 1)
	c.Assert(updated.Actions[0].VaultPubKey.Equals(safeVault.PubKey), Equals, true)
	c.Assert(updated.Actions[0].VaultPubKeyEddsa.Equals(safeVault.PubKeyEddsa), Equals, true)
	c.Assert(updated.Actions[0].VaultPubKeyFrost.Equals(safeVault.PubKeyFrost), Equals, true)
	c.Assert(updated.Actions[0].Coin.Equals(action.Coin), Equals, true)
	c.Assert(updated.Actions[0].MaxGas.Equals(common.Gas{maxGas}), Equals, true)
	c.Assert(updated.Actions[0].GasRate, Equals, items[0].GasRate)
}

// TestRequeueDanglingSettlementGasAssetRequiresAmountPlusGas protects BTC,
// LTC, and GAIA-style gas-asset outbounds, where the chosen vault must cover
// both the user amount and max gas from the same asset balance.
func (MigrationHelpersTestSuite) TestRequeueDanglingSettlementGasAssetRequiresAmountPlusGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	ctx := w.ctx.WithBlockHeight(2345)
	mgr, ok := w.mgr.(*Mgrs)
	c.Assert(ok, Equals, true)

	maxGas := saveMigrationNetworkFee(c, ctx, mgr, common.BTCChain)
	amount := cosmos.NewUint(10_000)

	underfundedVault := GetRandomVault()
	underfundedVault.PubKeyFrost = GetRandomEd25519PubKey()
	underfundedVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, amount.Add(maxGas.Amount).Sub(cosmos.OneUint())),
	}
	c.Assert(w.keeper.SetVault(ctx, underfundedVault), IsNil)

	oldVault := GetRandomVault()
	oldVault.Status = RetiringVault
	oldVault.PubKeyFrost = GetRandomEd25519PubKey()
	txID := GetRandomTxHash()
	action := TxOutItem{
		Chain:            common.BTCChain,
		InHash:           txID,
		ToAddress:        GetRandomBTCAddress(),
		VaultPubKey:      oldVault.PubKey,
		VaultPubKeyEddsa: txOutVaultPubKeyEddsa(common.BTCChain, oldVault.PubKeyEddsa),
		VaultPubKeyFrost: oldVault.PubKeyFrost,
		Coin:             common.NewCoin(common.BTCAsset, amount),
		MaxGas:           common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		GasRate:          1,
		Memo:             "TRADE-:BTC.BTC",
	}
	voter := NewObservedTxVoter(txID, nil)
	voter.Actions = []TxOutItem{action}
	w.keeper.SetObservedTxInVoter(ctx, voter)

	spec := danglingSettlementSpec{
		TxID:   txID,
		Chain:  common.BTCChain,
		Asset:  common.BTCAsset,
		Amount: amount,
	}
	c.Assert(requeueDanglingSettlementOutbound(ctx, mgr, spec, ctx.BlockHeight()), NotNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)

	fundedVault := GetRandomVault()
	fundedVault.PubKeyFrost = GetRandomEd25519PubKey()
	fundedVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, amount.Add(maxGas.Amount)),
	}
	c.Assert(w.keeper.SetVault(ctx, fundedVault), IsNil)

	c.Assert(requeueDanglingSettlementOutbound(ctx, mgr, spec, ctx.BlockHeight()), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(fundedVault.PubKey), Equals, true)
	c.Assert(items[0].Coin.Amount.Equal(amount), Equals, true)
	c.Assert(items[0].MaxGas.Equals(common.Gas{maxGas}), Equals, true)
}

// TestRequeueDanglingSettlementSkipsObservedOrMismatchedActions ensures the
// migration does not create duplicates or requeue a stale snapshot row that no
// longer exactly matches the stored voter action.
func (MigrationHelpersTestSuite) TestRequeueDanglingSettlementSkipsObservedOrMismatchedActions(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	ctx := w.ctx.WithBlockHeight(3456)
	mgr, ok := w.mgr.(*Mgrs)
	c.Assert(ok, Equals, true)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	maxGas := saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)

	activeVault := GetRandomVault()
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	activeVault.Coins = common.Coins{
		common.NewCoin(ethUSDC, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, maxGas.Amount.Add(cosmos.NewUint(100))),
	}
	c.Assert(w.keeper.SetVault(ctx, activeVault), IsNil)

	txID := GetRandomTxHash()
	amount := cosmos.NewUint(10 * common.One)
	action := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      txID,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(ethUSDC, amount),
		MaxGas:      common.Gas{maxGas},
		GasRate:     int64(mgr.GasMgr().GetGasRate(ctx, common.ETHChain).Uint64()),
		Memo:        "SECURE-:ETH.USDC",
	}
	spec := danglingSettlementSpec{
		TxID:   txID,
		Chain:  common.ETHChain,
		Asset:  ethUSDC,
		Amount: amount,
	}

	observedVoter := NewObservedTxVoter(txID, nil)
	observedVoter.Actions = []TxOutItem{action}
	observedVoter.OutTxs = []common.Tx{{
		ID:        GetRandomTxHash(),
		Chain:     common.ETHChain,
		ToAddress: action.ToAddress,
		Coins:     common.Coins{action.Coin},
		Gas:       action.MaxGas,
		Memo:      action.Memo,
	}}
	w.keeper.SetObservedTxInVoter(ctx, observedVoter)
	c.Assert(requeueDanglingSettlementOutbound(ctx, mgr, spec, ctx.BlockHeight()), NotNil)

	mismatchedVoter := NewObservedTxVoter(txID, nil)
	mismatchedAction := action
	mismatchedAction.Coin.Amount = amount.Add(cosmos.OneUint())
	mismatchedVoter.Actions = []TxOutItem{mismatchedAction}
	w.keeper.SetObservedTxInVoter(ctx, mismatchedVoter)
	c.Assert(requeueDanglingSettlementOutbound(ctx, mgr, spec, ctx.BlockHeight()), NotNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

// mustMigrationAsset keeps migration tests readable while still failing fast if
// a hard-coded asset string is invalid.
func mustMigrationAsset(c *C, raw string) common.Asset {
	asset, err := common.NewAsset(raw)
	c.Assert(err, IsNil)
	return asset
}

// saveMigrationNetworkFee seeds deterministic gas data for helper tests that
// call GasMgr.GetMaxGas.
func saveMigrationNetworkFee(c *C, ctx cosmos.Context, mgr *Mgrs, chain common.Chain) common.Coin {
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NewNetworkFee(chain, 1000, 10)), IsNil)
	maxGas, err := mgr.GasMgr().GetMaxGas(ctx, chain)
	c.Assert(err, IsNil)
	return maxGas
}

func (MigrationHelpersTestSuite) TestGetTCYClaimsFromData(c *C) {
	rawClaims := tcyclaimlist.GetTCYClaimsList()
	claimer, err := getTCYClaimsFromData()
	c.Assert(err, IsNil)
	c.Assert(len(claimer), Equals, len(rawClaims))
	if len(rawClaims) == 0 {
		return
	}

	sampleIndexes := []int{0, len(rawClaims) / 2, len(rawClaims) - 1}
	for _, idx := range sampleIndexes {
		rawClaim := rawClaims[idx]
		expectedAsset, err := common.NewAsset(rawClaim.Asset)
		c.Assert(err, IsNil)

		c.Assert(claimer[idx].Amount.Equal(math.NewUint(rawClaim.TCYClaim)), Equals, true)
		c.Assert(claimer[idx].Asset.Equals(expectedAsset), Equals, true)
		c.Assert(claimer[idx].L1Address.Equals(common.Address(rawClaim.Address)), Equals, true)
	}
}

func (MigrationHelpersTestSuite) TestSetTCYClaims(c *C) {
	ctx, mgr := setupManagerForTest(c)
	type claimExpectation struct {
		address common.Address
		asset   common.Asset
		amount  cosmos.Uint
	}

	rawClaims := tcyclaimlist.GetTCYClaimsList()
	expectations := make(map[string]claimExpectation)
	sampleKeys := make([]string, 0, 3)
	for _, rawClaim := range rawClaims {
		asset, err := common.NewAsset(rawClaim.Asset)
		c.Assert(err, IsNil)

		address := common.Address(rawClaim.Address)
		key := fmt.Sprintf("%s|%s", address.String(), asset.String())
		expectation, exists := expectations[key]
		if !exists {
			expectation = claimExpectation{
				address: address,
				asset:   asset,
				amount:  cosmos.ZeroUint(),
			}
			if len(sampleKeys) < 3 {
				sampleKeys = append(sampleKeys, key)
			}
		}
		expectation.amount = expectation.amount.Add(cosmos.NewUint(rawClaim.TCYClaim))
		expectations[key] = expectation
	}

	for _, key := range sampleKeys {
		expectation := expectations[key]
		_, err := mgr.Keeper().GetTCYClaimer(ctx, expectation.address, expectation.asset)
		c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", expectation.address.String()))
	}

	c.Assert(setTCYClaims(ctx, mgr), IsNil)

	for _, key := range sampleKeys {
		expectation := expectations[key]
		claimer, err := mgr.Keeper().GetTCYClaimer(ctx, expectation.address, expectation.asset)
		c.Assert(err, IsNil)
		c.Assert(claimer.Amount.Equal(expectation.amount), Equals, true)
		c.Assert(claimer.Asset.Equals(expectation.asset), Equals, true)
		c.Assert(claimer.L1Address.Equals(expectation.address), Equals, true)
	}
}
