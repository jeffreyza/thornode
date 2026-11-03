package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TxOutTestSuite struct{}

var _ = Suite(&TxOutTestSuite{})

func (TxOutTestSuite) TestTxOut(c *C) {
	pk := GetRandomPubKey()
	toAddr := GetRandomETHAddress()
	txOut := NewTxOut(1)
	c.Assert(txOut, NotNil)
	c.Assert(txOut.TxArray, HasLen, 0)
	c.Assert(txOut.IsEmpty(), Equals, true)
	c.Assert(txOut.Valid(), IsNil)
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		VaultPubKey: pk,
		ToAddress:   toAddr,
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		GasRate:     1,
	}
	txOut.TxArray = append(txOut.TxArray, txOutItem)
	c.Assert(txOut.TxArray, NotNil)
	c.Check(len(txOut.TxArray), Equals, 1)
	c.Assert(txOut.IsEmpty(), Equals, false)
	c.Assert(txOut.Valid(), IsNil)
	strTxOutItem := txOutItem.String()
	c.Check(len(strTxOutItem) > 0, Equals, true)

	txOut1 := NewTxOut(2)
	txOut1.TxArray = append(txOut1.TxArray, txOutItem)
	txOut1.TxArray = append(txOut1.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      GetRandomTxHash(),
		ToAddress:   toAddr,
		VaultPubKey: pk,
		Coin:        common.NoCoin,
		GasRate:     1,
	})
	c.Assert(txOut1.Valid(), NotNil)

	txOut2 := NewTxOut(3)
	txOut2.TxArray = append(txOut2.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      GetRandomTxHash(),
		ToAddress:   "",
		VaultPubKey: pk,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		GasRate:     1,
	})
	c.Assert(txOut2.Valid(), NotNil)
	txOut3 := NewTxOut(4)
	txOut3.TxArray = append(txOut3.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      GetRandomTxHash(),
		ToAddress:   toAddr,
		VaultPubKey: common.EmptyPubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		GasRate:     1,
	})
	c.Assert(txOut3.Valid(), NotNil)
}

func (TxOutTestSuite) TestTxOutItem(c *C) {
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin: common.Coin{
			Asset:  common.ETHAsset,
			Amount: cosmos.NewUint(100),
		},
		Memo: "something memo",
		MaxGas: common.Gas{
			common.NewCoin(common.ETHAsset, ethSingleTxFee),
		},
		GasRate: 1,
		InHash:  GetRandomTxHash(),
	}
	hash := GetRandomTxHash()
	c.Check(len(hash) > 0, Equals, true)
	inputs := []struct {
		name        string
		chain       common.Chain
		toAddr      common.Address
		vaultPubKey common.PubKey
		coin        common.Coin
		maxGas      common.Gas
		inHash      common.TxID
		memo        string
	}{
		{
			name:        "empty chain should return an error",
			chain:       common.EmptyChain,
			toAddr:      txOutItem.ToAddress,
			vaultPubKey: txOutItem.VaultPubKey,
			coin:        txOutItem.Coin,
			maxGas:      txOutItem.MaxGas,
			inHash:      txOutItem.InHash,
		},
		{
			name:        "empty in hash should return an error",
			chain:       txOutItem.Chain,
			toAddr:      txOutItem.ToAddress,
			vaultPubKey: txOutItem.VaultPubKey,
			coin:        txOutItem.Coin,
			maxGas:      txOutItem.MaxGas,
			inHash:      "",
		},
		{
			name:        "empty to address should return an error",
			chain:       txOutItem.Chain,
			toAddr:      common.NoAddress,
			vaultPubKey: txOutItem.VaultPubKey,
			coin:        txOutItem.Coin,
			maxGas:      txOutItem.MaxGas,
			inHash:      txOutItem.InHash,
		},
		{
			name:        "empty vault pub key should return an error",
			chain:       txOutItem.Chain,
			toAddr:      txOutItem.ToAddress,
			vaultPubKey: "",
			coin:        txOutItem.Coin,
			maxGas:      txOutItem.MaxGas,
			inHash:      txOutItem.InHash,
		},
		{
			name:        "empty coin should return an error",
			chain:       txOutItem.Chain,
			toAddr:      txOutItem.ToAddress,
			vaultPubKey: txOutItem.VaultPubKey,
			coin:        common.NoCoin,
			maxGas:      txOutItem.MaxGas,
			inHash:      txOutItem.InHash,
		},
		{
			name:        "invalid MaxGas should return an error",
			chain:       txOutItem.Chain,
			toAddr:      txOutItem.ToAddress,
			vaultPubKey: txOutItem.VaultPubKey,
			coin:        txOutItem.Coin,
			maxGas: common.Gas{
				common.NoCoin,
			},
			inHash: txOutItem.InHash,
		},
	}
	for _, tc := range inputs {
		item := TxOutItem{
			Chain:       tc.chain,
			ToAddress:   tc.toAddr,
			VaultPubKey: tc.vaultPubKey,
			Coin:        tc.coin,
			Memo:        "something memo",
			MaxGas:      tc.maxGas,
			InHash:      tc.inHash,
			GasRate:     1,
		}
		c.Check(item.Valid(), NotNil, Commentf(tc.name))
		if item.MaxGas.Valid() == nil {
			c.Check(txOutItem.Equals(item), Equals, false, Commentf(tc.name))
		}
	}
	txOutItem1 := txOutItem
	c.Check(txOutItem1.Equals(txOutItem), Equals, true)
	txOutItem1.Memo = "123456"
	c.Check(txOutItem.Equals(txOutItem1), Equals, false)
}

func (TxOutTestSuite) TestSigningTransactionPeriodClasses(c *C) {
	const signingPeriod int64 = 10
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	tests := []struct {
		name     string
		item     TxOutItem
		expected int64
	}{
		{name: "ordinary gas outbound", item: TxOutItem{Chain: common.ETHChain, Coin: common.NewCoin(common.ETHAsset, cosmos.OneUint())}, expected: signingPeriod},
		{name: "evm aggregator outbound", item: TxOutItem{Chain: common.ETHChain, Coin: common.NewCoin(common.ETHAsset, cosmos.OneUint()), Aggregator: "0xaggregator"}, expected: signingPeriod * 3},
		{name: "evm token outbound", item: TxOutItem{Chain: common.ETHChain, Coin: common.NewCoin(usdc, cosmos.OneUint())}, expected: signingPeriod * 3},
		{name: "xmr outbound", item: TxOutItem{Chain: common.XMRChain, Coin: common.NewCoin(common.XMRAsset, cosmos.OneUint())}, expected: signingPeriod * 3},
	}
	for _, tc := range tests {
		c.Assert(tc.item.SigningTransactionPeriod(signingPeriod), Equals, tc.expected, Commentf(tc.name))
	}
	c.Assert(MaxTxOutSigningTransactionPeriod(signingPeriod), Equals, signingPeriod*3)

	chainTests := []struct {
		name     string
		chain    common.Chain
		expected int64
	}{
		{name: "standard chain", chain: common.BTCChain, expected: signingPeriod},
		{name: "XMR chain", chain: common.XMRChain, expected: signingPeriod * 3},
		{name: "EVM chain", chain: common.ETHChain, expected: signingPeriod * 3},
	}
	for _, tc := range chainTests {
		c.Assert(MaxTxOutSigningTransactionPeriodForChain(tc.chain, signingPeriod), Equals, tc.expected, Commentf(tc.name))
	}
}
