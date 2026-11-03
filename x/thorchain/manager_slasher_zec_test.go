package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type zecRescheduleGasManager struct {
	*DummyGasManager
	amount, rate uint64
}

func (m *zecRescheduleGasManager) GetMaxGas(cosmos.Context, common.Chain) (common.Coin, error) {
	return common.NewCoin(common.ZECAsset, cosmos.NewUint(m.amount)), nil
}

func (m *zecRescheduleGasManager) GetGasRate(cosmos.Context, common.Chain) cosmos.Uint {
	return cosmos.NewUint(m.rate)
}

func (s *SlashingSuite) TestLegacyZECTexReschedulesAndCompletes(c *C) {
	for _, refund := range []bool{false, true} {
		ctx, k := setupKeeperForTest(c)
		ctx = ctx.WithBlockHeight(1)
		mgr := NewDummyMgrWithKeeper(k)
		gas := &zecRescheduleGasManager{DummyGasManager: NewDummyGasManager(), amount: 1_000, rate: 1}
		mgr.gasMgr = gas
		mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
		k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)
		k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 1)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
		k.SetMimir(ctx, constants.MaxOutboundAttempts.String(), 0)

		pool := NewPool()
		pool.Asset = common.ZECAsset
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vaults := make(Vaults, 2)
		for i := range vaults {
			vaults[i] = NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ZECChain}.Strings(), nil)
			vaults[i].AddFunds(common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(100*common.One))))
			c.Assert(k.SetVault(ctx, vaults[i]), IsNil)
		}
		p2pkh, tex, _, _ := zecTxOutAddresses()
		inHash := GetRandomTxHash()
		memo := NewOutboundMemo(inHash).String()
		if refund {
			memo = NewRefundMemo(inHash).String()
		}
		item := TxOutItem{
			Chain: common.ZECChain, InHash: inHash, ToAddress: tex,
			VaultPubKey: vaults[0].PubKey,
			Coin:        common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One)),
			MaxGas:      common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(gas.amount))},
			GasRate:     int64(gas.rate), Memo: memo,
		}
		queued := NewTxOut(ctx.BlockHeight())
		queued.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, queued), IsNil)
		from, err := vaults[0].PubKey.GetAddress(common.ZECChain)
		c.Assert(err, IsNil)
		voter := NewObservedTxVoter(inHash, nil)
		voter.Height, voter.FinalisedHeight = ctx.BlockHeight(), ctx.BlockHeight()
		voter.Tx = NewObservedTx(common.NewTx(inHash, p2pkh, from, common.NewCoins(item.Coin), item.MaxGas,
			"=:BTC.BTC:"+GetRandomBTCAddress().String()+"/"+tex.String()), ctx.BlockHeight(), vaults[0].PubKey, ctx.BlockHeight())
		voter.Actions = []TxOutItem{item}
		k.SetObservedTxInVoter(ctx, voter)
		slasher := newSlasher(k, mgr.EventMgr())

		for range 2 {
			previousHeight, previousVault := ctx.BlockHeight(), item.VaultPubKey
			ctx = ctx.WithBlockHeight(previousHeight + 10)
			gas.amount += 1_000
			gas.rate++
			c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
			queued, err = k.GetTxOut(ctx, ctx.BlockHeight())
			c.Assert(err, IsNil)
			c.Assert(queued.TxArray, HasLen, 1)
			item = queued.TxArray[0]
			c.Check(item.ToAddress, Equals, tex)
			c.Check(item.VaultPubKey.Equals(previousVault), Equals, false)
			c.Check(item.GasRate, Equals, int64(gas.rate))
			c.Check(item.MaxGas, DeepEquals, common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(gas.amount))})
			voter, err = k.GetObservedTxInVoter(ctx, inHash)
			c.Assert(err, IsNil)
			c.Assert(voter.Actions, HasLen, 1)
			c.Check(voter.Actions[0].Equals(item), Equals, true)
			c.Check(voter.Actions[0].MaxGas, DeepEquals, item.MaxGas)
			previous, getErr := k.GetTxOut(ctx, previousHeight)
			c.Assert(getErr, IsNil)
			c.Check(previous.TxArray[0].OutHash, Equals, common.BlankTxID)
		}

		from, err = item.VaultPubKey.GetAddress(common.ZECChain)
		c.Assert(err, IsNil)
		outHash := GetRandomTxHash()
		observed := NewObservedTx(common.NewTx(outHash, from, p2pkh, common.NewCoins(item.Coin), item.MaxGas, memo),
			ctx.BlockHeight(), item.VaultPubKey, ctx.BlockHeight())
		if refund {
			_, err = NewRefundHandler(mgr).handle(ctx, MsgRefundTx{Tx: observed, InTxID: inHash})
		} else {
			_, err = NewCommonOutboundTxHandler(mgr).handle(ctx, observed, inHash)
		}
		c.Assert(err, IsNil)
		c.Check(hasMissingTxOutSecurityEvent(ctx), Equals, false)
		queued, err = k.GetTxOut(ctx, ctx.BlockHeight())
		c.Assert(err, IsNil)
		c.Check(queued.TxArray[0].OutHash, Equals, outHash)
		voter, err = k.GetObservedTxInVoter(ctx, inHash)
		c.Assert(err, IsNil)
		c.Check(voter.IsDone(), Equals, true)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 10)
		c.Assert(slasher.LackSigning(ctx, mgr), IsNil)
		queued, err = k.GetTxOut(ctx, ctx.BlockHeight())
		c.Assert(err, IsNil)
		c.Check(queued.TxArray, HasLen, 0)
	}
}
