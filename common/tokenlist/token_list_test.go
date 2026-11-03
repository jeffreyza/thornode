package tokenlist

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TokenListSuite struct{}

var _ = Suite(&TokenListSuite{})

func (s *TokenListSuite) TestERC20TokenAsset(c *C) {
	token := ERC20Token{
		Address:  "0x1234567890abcdef",
		Symbol:   "TKN",
		Name:     "TestToken",
		Decimals: 18,
	}
	asset := token.Asset(common.ETHChain)
	c.Check(asset.Chain, Equals, common.ETHChain)
	c.Check(asset.Ticker, Equals, common.Ticker("TKN"))
	c.Check(asset.Symbol, Equals, common.Symbol("TKN-0X1234567890ABCDEF"))
}

func (s *TokenListSuite) TestGetEVMTokenListETH(c *C) {
	list := GetEVMTokenList(common.ETHChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListAVAX(c *C) {
	list := GetEVMTokenList(common.AVAXChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListBSC(c *C) {
	list := GetEVMTokenList(common.BSCChain)
	// BSC mocknet has empty name but tokens exist
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListBASE(c *C) {
	list := GetEVMTokenList(common.BASEChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListTRON(c *C) {
	list := GetEVMTokenList(common.TRONChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListPOL(c *C) {
	list := GetEVMTokenList(common.POLChain)
	c.Check(list.Name, Not(Equals), "")
}

func (s *TokenListSuite) TestGetEVMTokenListDefault(c *C) {
	list := GetEVMTokenList(common.BTCChain)
	c.Check(list.Name, Equals, "")
	c.Check(len(list.Tokens), Equals, 0)
}

func (s *TokenListSuite) TestGetAVAXTokenList(c *C) {
	list := GetAVAXTokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetBASETokenList(c *C) {
	list := GetBASETokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetBSCTokenList(c *C) {
	list := GetBSCTokenList()
	// BSC mocknet has empty name but tokens exist
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetPOLTokenList(c *C) {
	list := GetPOLTokenList()
	c.Check(list.Name, Not(Equals), "")
}

func (s *TokenListSuite) TestGetTRONTokenList(c *C) {
	list := GetTRONTokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}
