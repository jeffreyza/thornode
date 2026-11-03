package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type zecTxOutGasManager struct {
	*DummyGasManager
}

func (m *zecTxOutGasManager) GetGasDetails(_ cosmos.Context, _ common.Chain) (common.Coin, int64, error) {
	return common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000)), 1, nil
}

func (m *zecTxOutGasManager) GetMaxGas(ctx cosmos.Context, chain common.Chain) (common.Coin, error) {
	coin, _, err := m.GetGasDetails(ctx, chain)
	return coin, err
}

func zecTxOutAddresses() (p2pkh, tex, p2sh, wrongNetwork common.Address) {
	if common.CurrentChainNetwork == common.MockNet {
		return "tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7",
			"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays",
			"t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb",
			"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k"
	}
	return "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU",
		"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k",
		"t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5",
		"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays"
}

func (s TxOutStoreSuite) TestZECTexOutboundNormalization(c *C) {
	p2pkh, tex, p2sh, _ := zecTxOutAddresses()
	for _, tc := range []struct {
		input, want common.Address
		refund      bool
	}{
		{tex, p2pkh, false},
		{p2pkh, p2pkh, false},
		{p2sh, p2sh, false},
		{tex, p2pkh, true},
	} {
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		mgr.gasMgr = &zecTxOutGasManager{NewDummyGasManager()}
		txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
		mgr.txOutStore = txOutStore
		k.SetMimir(ctx, constants.MinTxOutVolumeThreshold.String(), 0)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)

		pool := NewPool()
		pool.Asset = common.ZECAsset
		pool.Decimals = 8
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ZECChain}.Strings(), nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(10*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		from, err := vault.PubKey.GetAddress(common.ZECChain)
		c.Assert(err, IsNil)

		item := TxOutItem{
			Chain:       common.ZECChain,
			ToAddress:   tc.input,
			InHash:      GetRandomTxHash(),
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One)),
		}
		voter := NewObservedTxVoter(item.InHash, nil)
		voter.Height = ctx.BlockHeight()
		if tc.refund {
			// Supply TEX through the swap memo's refund field, using a different
			// sender to ensure the explicit refund address is honored.
			memo := "=:BTC.BTC:" + GetRandomBTCAddress().String() + "/" + tc.input.String()
			voter.Tx = NewObservedTx(common.NewTx(item.InHash, p2sh, from,
				common.NewCoins(item.Coin), common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000))}, memo),
				ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
		}
		k.SetObservedTxInVoter(ctx, voter)

		if tc.refund {
			c.Assert(refundTx(ctx, voter.Tx, mgr, CodeSwapFail, "test refund to TEX", ""), IsNil)
		} else {
			var ok bool
			ok, err = txOutStore.TryAddTxOutItem(ctx, mgr, item, cosmos.ZeroUint())
			c.Assert(err, IsNil)
			c.Assert(ok, Equals, true)
		}
		items, err := txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1)
		c.Check(items[0].ToAddress, Equals, tc.want)
		if tc.refund {
			c.Check(items[0].GetMemo(), Equals, NewRefundMemo(item.InHash).String())
		}
		voter, err = k.GetObservedTxInVoter(ctx, item.InHash)
		c.Assert(err, IsNil)
		c.Assert(voter.Actions, HasLen, 1)
		c.Check(voter.Actions[0].ToAddress, Equals, tc.want)

		// The daemon reports the Base58Check destination. It must complete both the
		// queued outbound and the voter action without a missing-tx-out slash.
		outHash := GetRandomTxHash()
		observed := NewObservedTx(common.NewTx(outHash, from, tc.want,
			common.NewCoins(items[0].Coin), items[0].MaxGas, items[0].GetMemo()),
			ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
		var result *cosmos.Result
		if tc.refund {
			result, err = NewRefundHandler(mgr).handle(ctx, MsgRefundTx{Tx: observed, InTxID: item.InHash})
		} else {
			result, err = NewCommonOutboundTxHandler(mgr).handle(ctx, observed, item.InHash)
		}
		c.Assert(err, IsNil)
		c.Check(result.Events, HasLen, 0)
		c.Check(hasMissingTxOutSecurityEvent(ctx), Equals, false)
		items, err = txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1)
		c.Check(items[0].OutHash, Equals, outHash)
		voter, err = k.GetObservedTxInVoter(ctx, item.InHash)
		c.Assert(err, IsNil)
		c.Assert(voter.OutTxs, HasLen, 1)
		c.Check(voter.OutTxs[0].ID, Equals, outHash)
		c.Check(voter.IsDone(), Equals, true)
	}
}

func (s TxOutStoreSuite) TestZECRefundWrongNetworkAddressUsesSender(c *C) {
	p2pkh, _, sender, wrongNetworkTex := zecTxOutAddresses()
	wrongNetworkP2PKH := common.Address("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkP2PKH = "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU"
	}
	for _, tc := range []struct{ refundAddr, want common.Address }{
		{wrongNetworkTex, sender},
		{wrongNetworkP2PKH, sender},
		{p2pkh, p2pkh},
	} {
		comment := Commentf("refund address: %s", tc.refundAddr)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		mgr.gasMgr = &zecTxOutGasManager{NewDummyGasManager()}
		txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
		mgr.txOutStore = txOutStore
		k.SetMimir(ctx, constants.MinTxOutVolumeThreshold.String(), 0)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)

		pool := NewPool()
		pool.Asset = common.ZECAsset
		pool.Decimals = 8
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ZECChain}.Strings(), nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(10*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		vaultAddr, err := vault.PubKey.GetAddress(common.ZECChain)
		c.Assert(err, IsNil)

		coin := common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One))
		memo := "=:BTC.BTC:" + GetRandomBTCAddress().String() + "/" + tc.refundAddr.String()
		tx := NewObservedTx(common.NewTx(GetRandomTxHash(), sender, vaultAddr,
			common.NewCoins(coin), common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000))}, memo),
			ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
		voter := NewObservedTxVoter(tx.Tx.ID, nil)
		voter.Height = ctx.BlockHeight()
		voter.Tx = tx
		k.SetObservedTxInVoter(ctx, voter)

		c.Assert(refundTx(ctx, tx, mgr, CodeSwapFail, "test refund", ""), IsNil, comment)
		items, err := txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1, comment)
		c.Check(items[0].ToAddress, Equals, tc.want, comment)
		c.Check(items[0].Coin.Asset, Equals, common.ZECAsset, comment)
		pool, err = k.GetPool(ctx, common.ZECAsset)
		c.Assert(err, IsNil)
		c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(100*common.One), comment)
	}
}

func (s TxOutStoreSuite) TestUnsafeAddZECOutboundPreservesAddress(c *C) {
	p2pkh, tex, p2sh, _ := zecTxOutAddresses()
	for _, tc := range []struct{ input, want common.Address }{
		{tex, tex}, {p2pkh, p2pkh}, {p2sh, p2sh},
	} {
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
		item := TxOutItem{
			Chain:       common.ZECChain,
			ToAddress:   tc.input,
			InHash:      GetRandomTxHash(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One)),
		}
		c.Assert(txOutStore.UnSafeAddTxOutItem(ctx, mgr, item, ctx.BlockHeight()), IsNil)
		items, err := txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1)
		c.Check(items[0].ToAddress, Equals, tc.want)
	}
}

func (s TxOutStoreSuite) TestZECOutboundRejectsInvalidDestination(c *C) {
	_, _, _, wrongNetwork := zecTxOutAddresses()
	for _, addr := range []common.Address{"tex1invalid", wrongNetwork} {
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		mgr.gasMgr = &zecTxOutGasManager{NewDummyGasManager()}
		txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
		item := TxOutItem{
			Chain:       common.ZECChain,
			ToAddress:   addr,
			InHash:      GetRandomTxHash(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One)),
		}
		ok, err := txOutStore.TryAddTxOutItem(ctx, mgr, item, cosmos.ZeroUint())
		c.Check(err, NotNil)
		c.Check(ok, Equals, false)
		items, err := txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Check(items, HasLen, 0)
		voter, err := k.GetObservedTxInVoter(ctx, item.InHash)
		c.Assert(err, IsNil)
		c.Check(voter.Actions, HasLen, 0)
	}
}

func (s TxOutStoreSuite) TestZECTexCloutReducesTransparentOutbound(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	mgr := NewDummyMgrWithKeeper(k)
	txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
	k.SetMimir(ctx, constants.CloutLimit.String(), 1_000)
	p2pkh, tex, _, _ := zecTxOutAddresses()

	// A swap credits clout to the TEX destination from its memo.
	clout := NewSwapperClout(tex)
	clout.Score = cosmos.NewUint(500)
	c.Assert(k.SetSwapperClout(ctx, clout), IsNil)

	inHash := GetRandomTxHash()
	voter := NewObservedTxVoter(inHash, nil)
	voter.Tx.Tx.FromAddress = GetRandomBTCAddress()
	k.SetObservedTxInVoter(ctx, voter)

	// The outbound is scheduled to the normalized transparent address.
	toi := TxOutItem{Chain: common.ZECChain, ToAddress: p2pkh, InHash: inHash}
	value, applied := txOutStore.calcClout(ctx, cosmos.NewUint(800), toi)
	c.Check(applied.String(), Equals, "500")
	c.Check(value.String(), Equals, "300")
	clout, err := k.GetSwapperClout(ctx, tex)
	c.Assert(err, IsNil)
	c.Check(clout.Spent.String(), Equals, "500")
	c.Check(clout.LastSpentHeight, Equals, int64(100))
}
