package types

import (
	"math/rand"
	"strings"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TypeObservedTxSuite struct{}

var _ = Suite(&TypeObservedTxSuite{})

func (s TypeObservedTxSuite) TestVoter(c *C) {
	txID := GetRandomTxHash()
	eth := GetRandomETHAddress()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	accConsPub1 := GetRandomBech32ConsensusPubKey()
	accConsPub2 := GetRandomBech32ConsensusPubKey()
	accConsPub3 := GetRandomBech32ConsensusPubKey()
	accConsPub4 := GetRandomBech32ConsensusPubKey()

	accPubKeySet1 := GetRandomPubKeySet()
	accPubKeySet2 := GetRandomPubKeySet()
	accPubKeySet3 := GetRandomPubKeySet()
	accPubKeySet4 := GetRandomPubKeySet()

	tx1 := GetRandomTx()
	tx1.Memo = "hello"
	tx2 := GetRandomTx()
	observePoolAddr := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	obTx1 := common.NewObservedTx(tx1, 0, observePoolAddr, 1)
	obTx2 := common.NewObservedTx(tx2, 0, observePoolAddr, 1)

	c.Check(len(obTx1.String()) > 0, Equals, true)

	voter.Add(obTx1, acc1)
	c.Assert(voter.Txs, HasLen, 1)

	voter.Add(obTx1, acc1) // check THORNode don't duplicate the same signer
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Txs[0].Signers, HasLen, 1)

	voter.Add(obTx1, acc2) // append a signature
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)

	voter.Add(obTx2, acc1) // same validator seeing a different version of tx
	c.Assert(voter.Txs, HasLen, 2)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)

	voter.Add(obTx2, acc3) // second version
	c.Assert(voter.Txs, HasLen, 2)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)
	c.Assert(voter.Txs[1].Signers, HasLen, 2)

	obTx1Finalised := common.NewObservedTx(tx1, 1, observePoolAddr, 1)
	obTx2Finalised := common.NewObservedTx(tx2, 1, observePoolAddr, 1)

	voter.Add(obTx1Finalised, acc1)
	c.Assert(voter.Txs, HasLen, 3)

	voter.Add(obTx1Finalised, acc1) // check THORNode don't duplicate the same signer
	c.Assert(voter.Txs, HasLen, 3)
	c.Assert(voter.Txs[2].Signers, HasLen, 1)

	voter.Add(obTx1Finalised, acc2) // append a signature
	c.Assert(voter.Txs, HasLen, 3)
	c.Assert(voter.Txs[2].Signers, HasLen, 2)

	voter.Add(obTx2Finalised, acc1) // same validator seeing a different version of tx
	c.Assert(voter.Txs, HasLen, 4)
	c.Assert(voter.Txs[3].Signers, HasLen, 1)

	voter.Add(obTx2Finalised, acc3) // second version
	c.Assert(voter.Txs, HasLen, 4)
	c.Assert(voter.Txs[2].Signers, HasLen, 2)
	c.Assert(voter.Txs[3].Signers, HasLen, 2)

	trusts3 := NodeAccounts{
		NodeAccount{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		NodeAccount{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		NodeAccount{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
	}
	trusts4 := NodeAccounts{
		NodeAccount{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		NodeAccount{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		NodeAccount{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
		NodeAccount{
			NodeAddress:         acc4,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet4,
			ValidatorConsPubKey: accConsPub4,
		},
	}

	tx := voter.GetTx(trusts3)
	c.Check(tx.Tx.Memo, Equals, "hello")
	txFinalised := voter.GetTx(trusts3)
	c.Check(txFinalised.Tx.Memo, Equals, "hello")
	voter.Tx = common.ObservedTx{} // reset the final observed tx
	tx = voter.GetTx(trusts4)
	c.Check(tx.IsEmpty(), Equals, true)
	c.Assert(voter.HasConsensus(trusts3), Equals, true)
	voter.Tx = common.ObservedTx{} // reset the final observed tx
	c.Check(voter.HasConsensus(trusts4), Equals, false)
	c.Check(voter.Key().Equals(txID), Equals, true)
	c.Check(voter.String() == txID.String(), Equals, true)

	voter.Tx = common.ObservedTx{}
	txFinalised = voter.GetTx(trusts4)
	c.Check(txFinalised.IsEmpty(), Equals, true)
	c.Check(voter.HasFinalised(trusts3), Equals, true)
	voter.Tx = common.ObservedTx{}
	c.Check(voter.HasFinalised(trusts4), Equals, false)
	c.Check(voter.Key().Equals(txID), Equals, true)
	c.Check(voter.String() == txID.String(), Equals, true)

	thorchainCoins := common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100)),
	}
	inputs := []struct {
		coins           common.Coins
		memo            string
		sender          common.Address
		observePoolAddr common.PubKey
		blockHeight     int64
	}{
		{
			coins:           nil,
			memo:            "test",
			sender:          eth,
			observePoolAddr: observePoolAddr,
			blockHeight:     1024,
		},
		{
			coins:           common.Coins{},
			memo:            "test",
			sender:          eth,
			observePoolAddr: observePoolAddr,
			blockHeight:     1024,
		},
		{
			coins:           thorchainCoins,
			memo:            "test",
			sender:          common.NoAddress,
			observePoolAddr: observePoolAddr,
			blockHeight:     1024,
		},
		{
			coins:           thorchainCoins,
			memo:            "test",
			sender:          eth,
			observePoolAddr: common.EmptyPubKey,
			blockHeight:     1024,
		},
		{
			coins:           thorchainCoins,
			memo:            "test",
			sender:          eth,
			observePoolAddr: observePoolAddr,
			blockHeight:     0,
		},
	}

	for _, item := range inputs {
		tx := common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: item.sender,
			ToAddress:   GetRandomETHAddress(),
			Coins:       item.coins,
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
			Memo:        item.memo,
		}
		txIn := common.NewObservedTx(tx, item.blockHeight, item.observePoolAddr, item.blockHeight)
		c.Assert(txIn.Valid(), NotNil)
	}
}

func (TypeObservedTxSuite) TestSetTxToComplete(c *C) {
	activeNodes := NodeAccounts{
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
	}
	tx1 := GetRandomTx()
	tx1.Memo = "whatever"
	voter := NewObservedTxVoter(tx1.ID, nil)
	observePoolAddr := GetRandomPubKey()
	observedTx := common.NewObservedTx(tx1, 1024, observePoolAddr, 1028)
	voter.Add(observedTx, activeNodes[0].NodeAddress)
	voter.Add(observedTx, activeNodes[1].NodeAddress)
	voter.Add(observedTx, activeNodes[2].NodeAddress)
	c.Assert(voter.HasConsensus(activeNodes), Equals, true)

	consensusTx := voter.GetTx(activeNodes)
	c.Assert(consensusTx.IsEmpty(), Equals, false)
	c.Assert(voter.Tx.IsEmpty(), Equals, true)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	voter.Tx = *consensusTx

	observedTx = common.NewObservedTx(tx1, 1024, observePoolAddr, 1024)
	voter.Add(observedTx, activeNodes[0].NodeAddress)
	voter.Add(observedTx, activeNodes[1].NodeAddress)
	voter.Add(observedTx, activeNodes[2].NodeAddress)
	c.Assert(voter.HasFinalised(activeNodes), Equals, true)

	finalisedTx := voter.GetTx(activeNodes)
	c.Assert(finalisedTx.IsEmpty(), Equals, false)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	voter.Tx = *finalisedTx

	tx := GetRandomTx()
	addr, err := observePoolAddr.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx.FromAddress = addr
	toi := TxOutItem{
		Chain:       tx.Chain,
		ToAddress:   tx.ToAddress,
		VaultPubKey: observePoolAddr,
		Coin:        tx.Coins[0],
		Memo:        "",
		GasRate:     1,
	}
	voter.Actions = append(voter.Actions, toi)
	c.Assert(voter.AddOutTx(tx), Equals, true)
	// add it again should return true, but without any real action
	c.Assert(voter.AddOutTx(tx), Equals, true)
	c.Assert(voter.AddOutTx(GetRandomTx()), Equals, false)
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	c.Assert(voter.Tx.OutHashes[0], Equals, tx.ID.String())

	c.Assert(voter.IsDone(), Equals, true)
	voter.Tx = *voter.GetTx(activeNodes)
	c.Assert(voter.GetTx(activeNodes).Equals(voter.Tx), Equals, true)
}

func (TypeObservedTxSuite) TestAddOutTx(c *C) {
	activeNodes := NodeAccounts{
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
	}
	tx1 := GetRandomTx()
	tx1.Memo = "whatever"
	voter := NewObservedTxVoter(tx1.ID, nil)
	observePoolAddr := GetRandomPubKey()

	observedTx := common.NewObservedTx(tx1, 1024, observePoolAddr, 1024)
	voter.Add(observedTx, activeNodes[0].NodeAddress)
	voter.Add(observedTx, activeNodes[1].NodeAddress)
	voter.Add(observedTx, activeNodes[2].NodeAddress)
	c.Assert(voter.HasFinalised(activeNodes), Equals, true)

	finalisedTx := voter.GetTx(activeNodes)
	c.Assert(finalisedTx.IsEmpty(), Equals, false)
	c.Assert(voter.Tx.IsEmpty(), Equals, true)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	voter.Tx = *finalisedTx

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(100010000)),
		},
		common.Gas{
			{Asset: common.BTCAsset, Amount: cosmos.NewUint(27500)},
		},
		"",
	)
	addr, err := observePoolAddr.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx.FromAddress = addr

	toi := TxOutItem{
		Chain:       tx.Chain,
		ToAddress:   tx.ToAddress,
		VaultPubKey: observePoolAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		Memo:        "",
		GasRate:     1,
		MaxGas: common.Gas{
			{Asset: common.BTCAsset, Amount: cosmos.NewUint(37500)},
		},
	}
	voter.Actions = append(voter.Actions, toi)
	c.Assert(voter.AddOutTx(tx), Equals, true)
	// add it again should return true, but without any real action
	c.Assert(voter.AddOutTx(tx), Equals, true)
	c.Assert(voter.AddOutTx(GetRandomTx()), Equals, false)
	if !voter.Tx.IsEmpty() {
		c.Assert(voter.Tx.Status, Equals, common.Status_done)
		c.Assert(voter.Tx.OutHashes[0], Equals, tx.ID.String())
	}

	c.Assert(voter.IsDone(), Equals, true)
	voter.Tx = *voter.GetTx(activeNodes)
	c.Assert(voter.GetTx(activeNodes).Equals(voter.Tx), Equals, true)
}

func (TypeObservedTxSuite) TestMatchActionItemZecTex(c *C) {
	p2pkhAddr, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	otherAddr, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	texAddr, err := p2pkhAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(p2pkhAddr.Equals(texAddr), Equals, false)

	coin := common.NewCoin(common.ZECAsset, cosmos.NewUint(common.One))
	outboundTx := common.NewTx(
		GetRandomTxHash(),
		otherAddr,
		p2pkhAddr,
		common.Coins{coin},
		common.Gas{{Asset: common.ZECAsset, Amount: cosmos.NewUint(1000)}},
		"OUT:"+GetRandomTxHash().String(),
	)

	voter := NewObservedTxVoter(GetRandomTxHash(), nil)
	voter.Actions = []TxOutItem{{
		Chain:     common.ZECChain,
		ToAddress: texAddr,
		Coin:      coin,
		Memo:      outboundTx.Memo,
	}}
	c.Check(voter.matchActionItem(outboundTx), Equals, true)

	// An unrelated Zcash destination still fails to match.
	strangerTx := outboundTx
	strangerTx.ToAddress = otherAddr
	c.Check(voter.matchActionItem(strangerTx), Equals, false)
}

func (TypeObservedTxSuite) TestObservedTxEquals(c *C) {
	coins1 := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
	}
	coins2 := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	coins3 := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
	}
	coins4 := common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
	}
	eth, err := common.NewAddress("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Assert(err, IsNil)
	eth1, err := common.NewAddress("0xd58610f89265a2fb637ac40edf59141ff873b266")
	c.Assert(err, IsNil)
	observePoolAddr := GetRandomPubKey()
	observePoolAddr1 := GetRandomPubKey()
	inputs := []struct {
		tx    common.ObservedTx
		tx1   common.ObservedTx
		equal bool
	}{
		{
			tx:    common.NewObservedTx(common.Tx{FromAddress: eth, ToAddress: GetRandomETHAddress(), Coins: coins1, Memo: "memo", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{FromAddress: eth, ToAddress: GetRandomETHAddress(), Coins: coins1, Memo: "memo1", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{FromAddress: eth, ToAddress: GetRandomETHAddress(), Coins: coins1, Memo: "memo", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{FromAddress: eth1, ToAddress: GetRandomETHAddress(), Coins: coins1, Memo: "memo", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{Coins: coins2, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{Coins: coins3, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{Coins: coins4, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr1, 0),
			equal: false,
		},
		{
			tx:    common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			tx1:   common.NewObservedTx(common.Tx{Coins: coins1, Memo: "memo", FromAddress: eth, ToAddress: GetRandomETHAddress(), Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0),
			equal: false,
		},
	}
	for _, item := range inputs {
		c.Assert(item.tx.Equals(item.tx1), Equals, item.equal)
	}

	// test aggregator scenarios
	addr := GetRandomETHAddress()
	tx1 := common.NewObservedTx(common.Tx{FromAddress: eth, ToAddress: addr, Coins: coins1, Memo: "memo", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0)
	tx2 := common.NewObservedTx(common.Tx{FromAddress: eth, ToAddress: addr, Coins: coins1, Memo: "memo", Gas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}}, 0, observePoolAddr, 0)
	c.Assert(tx1.Equals(tx2), Equals, true)

	tx1.Aggregator = GetRandomETHAddress().String()
	c.Assert(tx1.Equals(tx2), Equals, false)
	tx2.Aggregator = GetRandomETHAddress().String()
	c.Assert(tx1.Equals(tx2), Equals, false)
	tx2.Aggregator = tx1.Aggregator
	c.Assert(tx1.Equals(tx2), Equals, true)

	tx1.AggregatorTarget = GetRandomETHAddress().String()
	c.Assert(tx1.Equals(tx2), Equals, false)
	tx2.AggregatorTarget = GetRandomETHAddress().String()
	c.Assert(tx1.Equals(tx2), Equals, false)
	tx2.AggregatorTarget = tx1.AggregatorTarget
	c.Assert(tx1.Equals(tx2), Equals, true)

	targetLimit := cosmos.NewUint(100)
	tx1.AggregatorTargetLimit = &targetLimit
	c.Assert(tx1.Equals(tx2), Equals, false)
	targetLimit = cosmos.NewUint(200)
	tx1.AggregatorTargetLimit = &targetLimit
	c.Assert(tx1.Equals(tx2), Equals, false)

	targetLimit = cosmos.NewUint(100)
	tx2.AggregatorTargetLimit = &targetLimit
	c.Assert(tx1.Equals(tx2), Equals, true)
}

func (TypeObservedTxSuite) TestObservedTxVote(c *C) {
	tx := GetRandomTx()
	voter := NewObservedTxVoter("", []common.ObservedTx{common.NewObservedTx(tx, 1, GetRandomPubKey(), 1)})
	c.Check(voter.Valid(), NotNil)

	voter1 := NewObservedTxVoter(GetRandomTxHash(), []common.ObservedTx{common.NewObservedTx(tx, 0, "", 0)})
	c.Check(voter1.Valid(), NotNil)

	voter2 := NewObservedTxVoter(GetRandomTxHash(), []common.ObservedTx{common.NewObservedTx(tx, 1024, GetRandomPubKey(), 1024)})
	c.Check(voter2.Valid(), IsNil)

	observedTx := common.NewObservedTx(GetRandomTx(), 1024, GetRandomPubKey(), 1024)
	addr := GetRandomBech32Addr()
	c.Check(observedTx.Sign(addr), Equals, true)
	c.Check(observedTx.Sign(addr), Equals, false)

	observedTx1 := common.NewObservedTx(observedTx.Tx, 1024, GetRandomPubKey(), 1024)
	c.Assert(observedTx.Equals(observedTx1), Equals, false)
	txID := GetRandomTxHash()
	observedTx1.SetDone(txID, 2)
	observedTx1.SetDone(txID, 2)
	c.Check(observedTx1.IsDone(2), Equals, false)
}

func (TypeObservedTxSuite) TestObservedTxGetConsensus(c *C) {
	txID := GetRandomTxHash()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	accConsPub1 := GetRandomBech32ConsensusPubKey()
	accConsPub2 := GetRandomBech32ConsensusPubKey()
	accConsPub3 := GetRandomBech32ConsensusPubKey()
	accConsPub4 := GetRandomBech32ConsensusPubKey()

	accPubKeySet1 := GetRandomPubKeySet()
	accPubKeySet2 := GetRandomPubKeySet()
	accPubKeySet3 := GetRandomPubKeySet()
	accPubKeySet4 := GetRandomPubKeySet()

	tx1 := GetRandomTx()
	tx1.Memo = "hello"
	observePoolAddr := GetRandomPubKey()

	voter := NewObservedTxVoter(txID, nil)
	obTx1 := common.NewObservedTx(tx1, 1, observePoolAddr, 1)
	obTx2 := common.NewObservedTx(tx1, 1, observePoolAddr, 2)

	c.Check(len(obTx1.String()) > 0, Equals, true)

	voter.Add(obTx1, acc1)
	c.Assert(voter.Txs, HasLen, 1)

	voter.Add(obTx1, acc1) // check THORNode don't duplicate the same signer
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Txs[0].Signers, HasLen, 1)

	voter.Add(obTx1, acc2) // append a signature
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)

	voter.Add(obTx2, acc1) // same validator seeing a different version of tx
	c.Assert(voter.Txs, HasLen, 2)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)

	voter.Add(obTx2, acc3) // second version
	c.Assert(voter.Txs, HasLen, 2)
	c.Assert(voter.Txs[0].Signers, HasLen, 2)
	c.Assert(voter.Txs[1].Signers, HasLen, 2)

	obTx1Finalised := common.NewObservedTx(tx1, 2, observePoolAddr, 2)

	voter.Add(obTx1Finalised, acc2)
	c.Assert(voter.Txs, HasLen, 3)

	voter.Add(obTx1Finalised, acc1) // check THORNode don't duplicate the same signer
	c.Assert(voter.Txs, HasLen, 3)
	c.Assert(voter.Txs[2].Signers, HasLen, 2)

	voter.Add(obTx1Finalised, acc2) // append a signature
	c.Assert(voter.Txs, HasLen, 3)
	c.Assert(voter.Txs[2].Signers, HasLen, 2)

	trusts4 := NodeAccounts{
		NodeAccount{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		NodeAccount{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		NodeAccount{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
		NodeAccount{
			NodeAddress:         acc4,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet4,
			ValidatorConsPubKey: accConsPub4,
		},
	}

	tx := voter.GetTx(trusts4)
	c.Assert(tx.IsEmpty(), Equals, true)

	voter.Add(obTx1Finalised, acc4) // append a signature
	c.Assert(voter.Txs, HasLen, 3)
	c.Assert(voter.Txs[2].Signers, HasLen, 3)

	tx = voter.GetTx(trusts4)
	c.Assert(tx.IsEmpty(), Equals, false)
	c.Assert(tx.Equals(obTx1), Equals, true)
}

func (TypeObservedTxSuite) TestNewGetConsensusTx(c *C) {
	txID := GetRandomTxHash()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	accConsPub1 := GetRandomBech32ConsensusPubKey()
	accConsPub2 := GetRandomBech32ConsensusPubKey()
	accConsPub3 := GetRandomBech32ConsensusPubKey()
	accConsPub4 := GetRandomBech32ConsensusPubKey()

	accPubKeySet1 := GetRandomPubKeySet()
	accPubKeySet2 := GetRandomPubKeySet()
	accPubKeySet3 := GetRandomPubKeySet()
	accPubKeySet4 := GetRandomPubKeySet()

	trusts4 := NodeAccounts{
		NodeAccount{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		NodeAccount{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		NodeAccount{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
		NodeAccount{
			NodeAddress:         acc4,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet4,
			ValidatorConsPubKey: accConsPub4,
		},
	}
	tx1 := GetRandomTx()
	tx1.Memo = "hello"
	tx1.ID = txID
	observePoolAddr := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	txForged := GetRandomTx()
	txForged.ID = txID
	obTx1 := common.NewObservedTx(txForged, 1, observePoolAddr, 1)
	obTx2 := common.NewObservedTx(tx1, 1, observePoolAddr, 2)

	obTx3 := common.NewObservedTx(tx1, 2, observePoolAddr, 2)

	c.Assert(voter.Add(obTx1, acc1), Equals, true)
	c.Assert(voter.Add(obTx2, acc2), Equals, true)
	c.Assert(voter.Add(obTx2, acc3), Equals, true)
	c.Assert(voter.Add(obTx2, acc4), Equals, true)
	c.Assert(voter.HasFinalised(trusts4), Equals, false)
	c.Assert(voter.HasConsensus(trusts4), Equals, true)
	tx := voter.GetTx(trusts4)

	c.Assert(tx.Tx.EqualsEx(tx1), Equals, true)
	c.Assert(voter.Add(obTx3, acc2), Equals, true)
	c.Assert(voter.Add(obTx3, acc3), Equals, true)
	c.Assert(voter.Add(obTx3, acc4), Equals, true)

	c.Assert(voter.HasFinalised(trusts4), Equals, true)
	txGood := voter.GetTx(trusts4)
	c.Assert(txGood.IsEmpty(), Equals, false)
	c.Assert(txGood.Equals(obTx3), Equals, true)
}

func (TypeObservedTxSuite) TestGetConsensusTxDoesNotMixObservedVaults(c *C) {
	txID := GetRandomTxHash()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	accConsPub1 := GetRandomBech32ConsensusPubKey()
	accConsPub2 := GetRandomBech32ConsensusPubKey()
	accConsPub3 := GetRandomBech32ConsensusPubKey()
	accConsPub4 := GetRandomBech32ConsensusPubKey()

	accPubKeySet1 := GetRandomPubKeySet()
	accPubKeySet2 := GetRandomPubKeySet()
	accPubKeySet3 := GetRandomPubKeySet()
	accPubKeySet4 := GetRandomPubKeySet()

	trusts4 := NodeAccounts{
		{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
		{
			NodeAddress:         acc4,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet4,
			ValidatorConsPubKey: accConsPub4,
		},
	}

	tx := GetRandomTx()
	tx.ID = txID
	tx.Memo = "MIGRATE:2750"

	observePoolAddr1 := GetRandomPubKey()
	observePoolAddr2 := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	obTx1 := common.NewObservedTx(tx, 1, observePoolAddr1, 2)
	obTx2 := common.NewObservedTx(tx, 1, observePoolAddr2, 2)

	c.Assert(voter.Add(obTx1, acc1), Equals, true)
	c.Assert(voter.Add(obTx1, acc2), Equals, true)
	c.Assert(voter.Add(obTx2, acc3), Equals, true)
	c.Assert(voter.Add(obTx2, acc4), Equals, true)

	c.Assert(voter.HasConsensus(trusts4), Equals, false)
	c.Assert(voter.GetTx(trusts4).IsEmpty(), Equals, true)
}

func (TypeObservedTxSuite) TestGetConsensusTxDoesNotMixXMRSpentRefs(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
	}

	txID := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = txID
	tx.Chain = common.XMRChain
	tx.Memo = "OUT:spent-refs"

	observePoolAddr := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	obTxA := common.NewObservedTx(tx, 1, observePoolAddr, 1)
	obTxA.SpentOutputRefs = []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}
	obTxB := common.NewObservedTx(tx, 1, observePoolAddr, 1)
	obTxB.SpentOutputRefs = []common.OutputRef{
		{TxHash: "cccc", OutputIndex: 0, KeyImage: "ki_cccc", SpendTxHash: txID.String()},
		{TxHash: "dddd", OutputIndex: 1, KeyImage: "ki_dddd", SpendTxHash: txID.String()},
	}

	c.Assert(voter.Add(obTxA, nas[0].NodeAddress), Equals, true)
	c.Assert(voter.Add(obTxA, nas[1].NodeAddress), Equals, true)
	c.Assert(voter.Add(obTxB, nas[2].NodeAddress), Equals, true)
	c.Assert(voter.Add(obTxB, nas[3].NodeAddress), Equals, true)

	c.Assert(voter.Txs, HasLen, 2)
	c.Assert(voter.HasConsensus(nas), Equals, false)
	c.Assert(voter.GetTx(nas).IsEmpty(), Equals, true)
}

func (TypeObservedTxSuite) TestVoterMergesEquivalentXMRSpentRefsRegardlessOfOrder(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
		GetRandomValidatorNode(NodeStatus_Active),
	}

	txID := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = txID
	tx.Chain = common.XMRChain
	tx.Memo = "OUT:spent-refs-order"

	observePoolAddr := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	obTxA := common.NewObservedTx(tx, 1, observePoolAddr, 1)
	obTxA.SpentOutputRefs = []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}
	obTxB := common.NewObservedTx(tx, 1, observePoolAddr, 1)
	obTxB.SpentOutputRefs = []common.OutputRef{
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	c.Assert(voter.Add(obTxA, nas[0].NodeAddress), Equals, true)
	c.Assert(voter.Add(obTxB, nas[1].NodeAddress), Equals, true)
	c.Assert(voter.Add(obTxA, nas[2].NodeAddress), Equals, true)

	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.HasConsensus(nas), Equals, true)
	c.Assert(voter.GetTx(nas).HasEquivalentSpentOutputRefs(obTxA), Equals, true)
}

func (TypeObservedTxSuite) TestShouldRefundRedundantObservationXMRSpentRefJoker(c *C) {
	txID := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = txID
	tx.Chain = common.XMRChain
	tx.Memo = "OUT:xmr-ref-joker"

	signer := GetRandomBech32Addr()
	assertRefund := func(voter *ObservedTxVoter, observation common.ObservedTx, want bool) {
		c.Assert(voter.ShouldRefundRedundantInboundObservation(observation, signer), Equals, want, Commentf("inbound"))
		c.Assert(voter.ShouldRefundRedundantOutboundObservation(observation, signer), Equals, want, Commentf("outbound"))
	}
	observedPubKey := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	// The first observation has no spent-output refs, so it is a consensus
	// wildcard (the "joker") for any later ref list.
	joker := common.NewObservedTx(tx, 1, observedPubKey, 1)
	c.Assert(voter.Add(joker, signer), Equals, true)

	variantA := common.NewObservedTx(tx, 1, observedPubKey, 1)
	variantA.SpentOutputRefs = []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}
	// The optional-ref transition remains in the same cap class, but it does not
	// receive an immediate refund; consensus/flex accounting credits it normally.
	assertRefund(&voter, variantA, false)
	c.Assert(voter.Add(variantA, signer), Equals, true)

	variantB := common.NewObservedTx(tx, 1, observedPubKey, 1)
	variantB.SpentOutputRefs = []common.OutputRef{
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}
	// Although variant A and B conflict with each other, both belong to the
	// same refund class. The joker must not keep the count at 1.
	assertRefund(&voter, variantB, false)

	// Even a gas-only variant with variant A's exact refs is no longer eligible:
	// both the joker and variant A occupy the transitive refund class, so the
	// one-redundant-observation allowance has already been consumed.
	sameRefsDifferentGas := variantA
	sameRefsDifferentGas.Tx.Gas = common.Gas{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(1234)),
	}
	assertRefund(&voter, sameRefsDifferentGas, false)

	// Without the joker, one conflicting non-empty ref set is in the same
	// transitive refund class but is not consensus-compatible. It must not make
	// the next variant eligible for an immediate refund, because a late matching
	// observation can also receive the flex-window refund.
	conflictingOnly := NewObservedTxVoter(txID, nil)
	c.Assert(conflictingOnly.Add(variantA, signer), Equals, true)
	assertRefund(&conflictingOnly, variantB, false)

	// A normal gas-only variant with the same canonical refs remains eligible for
	// the intended one-time redundant-observation refund.
	assertRefund(&conflictingOnly, sameRefsDifferentGas, true)
}

func (TypeObservedTxSuite) TestGetConsensusSignersStayScopedToObservedVault(c *C) {
	txID := GetRandomTxHash()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	accConsPub1 := GetRandomBech32ConsensusPubKey()
	accConsPub2 := GetRandomBech32ConsensusPubKey()
	accConsPub3 := GetRandomBech32ConsensusPubKey()
	accConsPub4 := GetRandomBech32ConsensusPubKey()

	accPubKeySet1 := GetRandomPubKeySet()
	accPubKeySet2 := GetRandomPubKeySet()
	accPubKeySet3 := GetRandomPubKeySet()
	accPubKeySet4 := GetRandomPubKeySet()

	trusts4 := NodeAccounts{
		{
			NodeAddress:         acc1,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet1,
			ValidatorConsPubKey: accConsPub1,
		},
		{
			NodeAddress:         acc2,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet2,
			ValidatorConsPubKey: accConsPub2,
		},
		{
			NodeAddress:         acc3,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet3,
			ValidatorConsPubKey: accConsPub3,
		},
		{
			NodeAddress:         acc4,
			Status:              NodeStatus_Active,
			PubKeySet:           accPubKeySet4,
			ValidatorConsPubKey: accConsPub4,
		},
	}

	tx := GetRandomTx()
	tx.ID = txID
	tx.Memo = "MIGRATE:2750"

	observePoolAddr1 := GetRandomPubKey()
	observePoolAddr2 := GetRandomPubKey()
	voter := NewObservedTxVoter(txID, nil)

	obTx1 := common.NewObservedTx(tx, 1, observePoolAddr1, 2)
	obTx2 := common.NewObservedTx(tx, 1, observePoolAddr2, 2)

	c.Assert(voter.Add(obTx1, acc1), Equals, true)
	c.Assert(voter.Add(obTx1, acc2), Equals, true)
	c.Assert(voter.Add(obTx1, acc3), Equals, true)
	c.Assert(voter.Add(obTx2, acc4), Equals, true)

	txGood := voter.GetTx(trusts4)
	c.Assert(txGood.IsEmpty(), Equals, false)
	c.Assert(txGood.ObservedPubKey.Equals(observePoolAddr1), Equals, true)

	voter.Tx = *txGood
	signers := voter.GetConsensusSigners()
	c.Assert(signers, HasLen, 3)

	signerSet := make(map[string]bool, len(signers))
	for _, signer := range signers {
		signerSet[signer.String()] = true
	}
	c.Assert(signerSet[acc1.String()], Equals, true)
	c.Assert(signerSet[acc2.String()], Equals, true)
	c.Assert(signerSet[acc3.String()], Equals, true)
	c.Assert(signerSet[acc4.String()], Equals, false)
}

// TestGetObserveSignersCreditsDifferentGasObserver reproduces the EVM
// instant-observation slash (thornode#3081): a signer's pre-broadcast
// gas-limit observation of an outbound differs only in gas from the scanner's
// gas-used observation that wins consensus. GetConsensusSigners (gas-sensitive)
// excludes it; GetObserveSigners (gas-agnostic) must credit it so its observe
// slash point is refunded.
func (TypeObservedTxSuite) TestGetObserveSignersCreditsDifferentGasObserver(c *C) {
	txID := GetRandomTxHash()
	acc1 := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	acc4 := GetRandomBech32Addr()

	trusts4 := NodeAccounts{
		{NodeAddress: acc1, Status: NodeStatus_Active, PubKeySet: GetRandomPubKeySet(), ValidatorConsPubKey: GetRandomBech32ConsensusPubKey()},
		{NodeAddress: acc2, Status: NodeStatus_Active, PubKeySet: GetRandomPubKeySet(), ValidatorConsPubKey: GetRandomBech32ConsensusPubKey()},
		{NodeAddress: acc3, Status: NodeStatus_Active, PubKeySet: GetRandomPubKeySet(), ValidatorConsPubKey: GetRandomBech32ConsensusPubKey()},
		{NodeAddress: acc4, Status: NodeStatus_Active, PubKeySet: GetRandomPubKeySet(), ValidatorConsPubKey: GetRandomBech32ConsensusPubKey()},
	}

	observePoolAddr := GetRandomPubKey()

	// Same outbound tx observed two ways, differing ONLY in gas.
	gasUsedTx := GetRandomTx()
	gasUsedTx.ID = txID
	gasUsedTx.Memo = "OUT:" + txID.String()
	gasUsedTx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(21000))}

	gasLimitTx := gasUsedTx
	gasLimitTx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(50000))}

	obGasUsed := common.NewObservedTx(gasUsedTx, 1, observePoolAddr, 2)
	obGasLimit := common.NewObservedTx(gasLimitTx, 1, observePoolAddr, 2)

	voter := NewObservedTxVoter(txID, nil)
	// 3-of-4 observe the gas-used version (reaches consensus); acc4 is the lone
	// signer's gas-limit "instant" observation.
	c.Assert(voter.Add(obGasUsed, acc1), Equals, true)
	c.Assert(voter.Add(obGasUsed, acc2), Equals, true)
	c.Assert(voter.Add(obGasUsed, acc3), Equals, true)
	c.Assert(voter.Add(obGasLimit, acc4), Equals, true)

	txGood := voter.GetTx(trusts4)
	c.Assert(txGood.IsEmpty(), Equals, false)
	// Consensus stays gas-sensitive: the gas-used value wins.
	c.Assert(txGood.Tx.Gas.Equals(gasUsedTx.Gas), Equals, true)
	voter.Tx = *txGood

	// Gas-sensitive set excludes the gas-limit observer.
	consensusSigners := voter.GetConsensusSigners()
	c.Assert(consensusSigners, HasLen, 3)

	// Gas-agnostic set credits every honest observer, including acc4.
	observeSigners := voter.GetObserveSigners()
	c.Assert(observeSigners, HasLen, 4)
	observeSet := make(map[string]bool, len(observeSigners))
	for _, s := range observeSigners {
		observeSet[s.String()] = true
	}
	c.Assert(observeSet[acc1.String()], Equals, true)
	c.Assert(observeSet[acc2.String()], Equals, true)
	c.Assert(observeSet[acc3.String()], Equals, true)
	c.Assert(observeSet[acc4.String()], Equals, true)
}

// TestRecordObserveSignersNoAliasing is a regression guard for the backing-array
// aliasing bug: m.Tx is a shallow struct copy of a Txs entry (getConsensusTx
// returns a range-copy), so m.Tx.Signers shares its backing array with the
// originating m.Txs[i].Signers. Before the clone-on-write, RecordObserveSigners
// appended in place into that shared array, so a later append to the sibling
// slice silently overwrote the consensus Tx's freshly-credited signer.
func (s TypeObservedTxSuite) TestRecordObserveSignersNoAliasing(c *C) {
	accA := GetRandomBech32Addr()
	accB := GetRandomBech32Addr()
	accC := GetRandomBech32Addr()

	// Signers slice with spare capacity so an in-place append reuses the backing
	// array — exactly the state getConsensusTx hands back.
	shared := make([]string, 1, 8)
	shared[0] = accA.String()

	obTx := common.NewObservedTx(GetRandomTx(), 0, GetRandomPubKey(), 1)
	obTx.Signers = shared

	voter := NewObservedTxVoter(GetRandomTxHash(), []common.ObservedTx{obTx})
	voter.Tx = voter.Txs[0] // shallow copy: voter.Tx.Signers aliases voter.Txs[0].Signers

	// Credit the wider observe-signer set; accB is new, so it is appended.
	voter.RecordObserveSigners([]cosmos.AccAddress{accB})
	c.Assert(voter.Tx.Signers, HasLen, 2)
	c.Assert(voter.Tx.Signers[1], Equals, accB.String())

	// An independent, later append to the sibling Txs entry must NOT reach back
	// through a shared backing array and overwrite voter.Tx.Signers[1]. Without
	// clone-on-write this rewrites the consensus signer accB to accC.
	voter.Txs[0].Signers = append(voter.Txs[0].Signers, accC.String())
	c.Assert(voter.Tx.Signers[1], Equals, accB.String())
}

// getConsensusTx counts each active validator signer once, ignoring non-active
// accounts, exactly like the linear NodeAccounts.IsNodeKeys scan it replaced.
func (s *TypeObservedTxSuite) TestGetConsensusTxCountsActiveSignersOnce(c *C) {
	active := NodeAccounts{}
	for i := 0; i < 4; i++ {
		active = append(active, GetRandomValidatorNode(NodeStatus_Active))
	}
	standby := GetRandomValidatorNode(NodeStatus_Standby)
	accounts := append(NodeAccounts{standby}, active...)

	tx := GetRandomObservedTx()
	tx.BlockHeight, tx.FinaliseHeight = 10, 10

	vote := func(voters ...NodeAccount) ObservedTxVoter {
		voter := NewObservedTxVoter(tx.Tx.ID, nil)
		for _, na := range voters {
			voter.Add(tx, na.NodeAddress)
		}
		return voter
	}

	// 3 distinct active of 5 accounts is short of 2/3; counting the standby
	// would reach it
	voter := vote(active[0], active[1], active[2], standby)
	c.Check(voter.HasFinalised(accounts), Equals, false)
	// Add rejects a repeat vote via HasSigned, so plant the duplicate signer
	// directly; counting it would reach 2/3
	voter.Txs[0].Signers = append(voter.Txs[0].Signers, active[1].NodeAddress.String())
	c.Check(voter.HasFinalised(accounts), Equals, false)
	// 4 distinct active of 5 accounts reaches it
	voter = vote(active[0], active[1], active[2], active[3], standby)
	c.Check(voter.HasFinalised(accounts), Equals, true)
	for _, signer := range voter.Txs[0].GetSigners() {
		c.Check(accounts.IsNodeKeys(signer), Equals, !signer.Equals(standby.NodeAddress))
	}
}

// oldGetTx is GetTx before the active-node index was shared between passes;
// it is the oracle for TestGetTxMatchesOracle.
func oldGetTx(m *ObservedTxVoter, nodeAccounts NodeAccounts) *common.ObservedTx {
	if !m.Tx.IsEmpty() && m.Tx.IsFinal() {
		return &m.Tx
	}
	finalTx := oldGetConsensusTx(m, nodeAccounts, true)
	if !finalTx.IsEmpty() {
		return &finalTx
	}
	discoverTx := oldGetConsensusTx(m, nodeAccounts, false)
	if !discoverTx.IsEmpty() {
		return &discoverTx
	}
	return &m.Tx
}

func oldGetConsensusTx(m *ObservedTxVoter, accounts NodeAccounts, final bool) common.ObservedTx {
	var activeNodes map[string]struct{}
	for _, txFinal := range m.Txs {
		voters := make(map[string]bool)
		if txFinal.IsFinal() != final {
			continue
		}
		if activeNodes == nil {
			activeNodes = make(map[string]struct{}, len(accounts))
			for _, na := range accounts {
				if na.Status == NodeStatus_Active {
					activeNodes[string(na.NodeAddress)] = struct{}{}
				}
			}
		}
		for _, txIn := range m.Txs {
			if txIn.IsFinal() != final {
				continue
			}
			if !sameConsensusObservation(txFinal, txIn) {
				continue
			}
			for _, signer := range txIn.GetSigners() {
				key := string(signer)
				if _, exist := voters[key]; exist {
					continue
				}
				if _, active := activeNodes[key]; active {
					voters[key] = true
				}
			}
		}
		if HasSuperMajority(len(voters), len(accounts)) {
			return txFinal
		}
	}
	return common.ObservedTx{}
}

// GetTx, HasConsensus and HasFinalised agree with the oracle on randomized
// voters: mixed finality, consensus-equal and -unequal observations, repeated,
// non-active, unknown, upper-case and undecodable signers, and repeated or
// non-active accounts.
func (s *TypeObservedTxSuite) TestGetTxMatchesOracle(c *C) {
	r := rand.New(rand.NewSource(42)) // #nosec G404 test only
	pool := make([]cosmos.AccAddress, 7)
	for i := range pool {
		pool[i] = GetRandomBech32Addr()
	}
	statuses := []NodeStatus{NodeStatus_Active, NodeStatus_Active, NodeStatus_Active, NodeStatus_Standby, NodeStatus_Disabled}
	otherPrefix := make([]string, len(pool))
	for i := range pool {
		var err error
		otherPrefix[i], err = bech32.ConvertAndEncode("cosmos", pool[i])
		c.Assert(err, IsNil)
	}

	base := GetRandomObservedTx()
	base.BlockHeight, base.FinaliseHeight = 10, 10
	var primary common.ObservedTx // most observations per voter
	variant := func() common.ObservedTx {
		tx := primary
		tx.Signers = nil
		switch r.Intn(8) {
		case 1: // other finality
			if tx.IsFinal() {
				tx.FinaliseHeight = 20
			} else {
				tx.FinaliseHeight = tx.BlockHeight
			}
		case 2: // consensus-equal, different block height
			tx.BlockHeight, tx.FinaliseHeight = 11, tx.FinaliseHeight-tx.BlockHeight+11
		case 3: // different gas: not consensus-equal
			tx.Tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(1+r.Intn(3))))}
		case 4: // different memo, maybe not final
			tx.Tx.Memo = "memo"
			if r.Intn(2) == 0 {
				tx.FinaliseHeight = 20
			}
		}
		return tx
	}

	selected := map[bool]int{} // consensus selections by finality
	for iter := 0; iter < 8000; iter++ {
		primary = base
		if r.Intn(2) == 0 {
			primary.FinaliseHeight = 20
		}
		var accounts NodeAccounts
		for i, n := 0, r.Intn(7); i < n; i++ {
			accounts = append(accounts, NodeAccount{
				NodeAddress: pool[r.Intn(len(pool))],
				Status:      statuses[r.Intn(len(statuses))],
			})
		}
		voter := NewObservedTxVoter(base.Tx.ID, nil)
		for i, n := 0, r.Intn(5); i < n; i++ {
			tx := variant()
			for j, k := 0, r.Intn(10); j < k; j++ {
				signer := pool[r.Intn(len(pool))].String()
				switch r.Intn(10) {
				case 0:
					signer = strings.ToUpper(signer)
				case 1:
					signer = "garbage"
				case 2: // mixed case: not valid bech32
					signer = strings.ToUpper(signer[:1]) + signer[1:]
				case 3: // valid bech32 under another prefix
					signer = otherPrefix[r.Intn(len(otherPrefix))]
				}
				tx.Signers = append(tx.Signers, signer)
			}
			voter.Txs = append(voter.Txs, tx)
		}
		switch r.Intn(4) {
		case 1:
			voter.Tx = variant()
		case 2:
			if len(voter.Txs) > 0 {
				voter.Tx = voter.Txs[r.Intn(len(voter.Txs))]
			}
		}

		want := oldGetTx(&voter, accounts)
		got := voter.GetTx(accounts)
		c.Assert(got, DeepEquals, want, Commentf("iteration %d", iter))
		c.Assert(got == &voter.Tx, Equals, want == &voter.Tx, Commentf("iteration %d", iter))
		c.Assert(voter.HasConsensus(accounts), Equals, !want.IsEmpty())
		c.Assert(voter.HasFinalised(accounts), Equals, !want.IsEmpty() && want.IsFinal())
		if !want.IsEmpty() && want != &voter.Tx {
			selected[want.IsFinal()]++
		}
	}
	// the generator must reach both kinds of consensus often enough to matter
	c.Assert(selected[true] > 150 && selected[false] > 150, Equals, true, Commentf("selections: %v", selected))
}
