package thorchain

import (
	"fmt"
	"testing"

	"github.com/blang/semver"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemoParserSuite struct{}

var _ = Suite(&MemoParserSuite{})

func TestMemoParserSuite(t *testing.T) {
	TestingT(t)
}

// ============================================================
// TxType method tests
// ============================================================

func (s *MemoParserSuite) TestTxTypeIsInbound(c *C) {
	inboundTypes := []TxType{
		TxAdd, TxWithdraw, TxSwap, TxLimitSwap, TxModifyLimitSwap,
		TxDonate, TxBond, TxUnbond, TxRebond, TxLeave, TxMaint,
		TxReserve, TxNoOp, TxTHORName, TxSwitch,
		TxTradeAccountDeposit, TxTradeAccountWithdrawal,
		TxRunePoolDeposit, TxRunePoolWithdraw,
		TxSecuredAssetDeposit, TxSecuredAssetWithdraw,
		TxStableReserveDeposit, TxStableReserveWithdraw,
		TxReferenceWriteMemo, TxReferenceReadMemo,
		TxTCYClaim, TxTCYStake, TxTCYUnstake, TxOperatorRotate,
	}
	for _, tt := range inboundTypes {
		c.Check(tt.IsInbound(), Equals, true, Commentf("Expected %s to be inbound", tt.String()))
	}
	// Non-inbound types
	c.Check(TxOutbound.IsInbound(), Equals, false)
	c.Check(TxRefund.IsInbound(), Equals, false)
	c.Check(TxRagnarok.IsInbound(), Equals, false)
	c.Check(TxMigrate.IsInbound(), Equals, false)
	c.Check(TxConsolidate.IsInbound(), Equals, false)
	c.Check(TxUnknown.IsInbound(), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeIsOutbound(c *C) {
	c.Check(TxOutbound.IsOutbound(), Equals, true)
	c.Check(TxRefund.IsOutbound(), Equals, true)
	c.Check(TxRagnarok.IsOutbound(), Equals, true)
	c.Check(TxSwap.IsOutbound(), Equals, false)
	c.Check(TxAdd.IsOutbound(), Equals, false)
	c.Check(TxUnknown.IsOutbound(), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeIsOutboundMemoless(c *C) {
	c.Check(TxOutbound.IsOutboundMemoless(), Equals, true)
	c.Check(TxRefund.IsOutboundMemoless(), Equals, true)
	c.Check(TxRagnarok.IsOutboundMemoless(), Equals, false)
	c.Check(TxSwap.IsOutboundMemoless(), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeIsInternal(c *C) {
	c.Check(TxMigrate.IsInternal(), Equals, true)
	c.Check(TxConsolidate.IsInternal(), Equals, true)
	c.Check(TxSwap.IsInternal(), Equals, false)
	c.Check(TxOutbound.IsInternal(), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeHasOutbound(c *C) {
	// Types without outbound
	noOutbound := []TxType{TxAdd, TxBond, TxTradeAccountDeposit, TxSecuredAssetDeposit, TxStableReserveDeposit, TxRunePoolDeposit, TxDonate, TxReserve, TxMigrate, TxMaint, TxBondYield, TxRagnarok}
	for _, tt := range noOutbound {
		c.Check(tt.HasOutbound(), Equals, false, Commentf("%s should not have outbound", tt.String()))
	}
	// Types with outbound
	c.Check(TxSwap.HasOutbound(), Equals, true)
	c.Check(TxWithdraw.HasOutbound(), Equals, true)
	c.Check(TxUnbond.HasOutbound(), Equals, true)
	c.Check(TxRefund.HasOutbound(), Equals, true)
}

func (s *MemoParserSuite) TestTxTypeIsEmpty(c *C) {
	c.Check(TxUnknown.IsEmpty(), Equals, true)
	c.Check(TxSwap.IsEmpty(), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeEquals(c *C) {
	swap := TxSwap
	c.Check(swap.Equals(TxSwap), Equals, true)
	c.Check(swap.Equals(TxAdd), Equals, false)
}

func (s *MemoParserSuite) TestTxTypeString(c *C) {
	c.Check(TxAdd.String(), Equals, "add")
	c.Check(TxSwap.String(), Equals, "swap")
	c.Check(TxOutbound.String(), Equals, "out")
	c.Check(TxRefund.String(), Equals, "refund")
	c.Check(TxConsolidate.String(), Equals, "consolidate")
	c.Check(TxRebond.String(), Equals, "rebond")
}

func (s *MemoParserSuite) TestStringToTxType(c *C) {
	// Valid conversions
	tests := map[string]TxType{
		"add": TxAdd, "+": TxAdd,
		"withdraw": TxWithdraw, "wd": TxWithdraw, "-": TxWithdraw,
		"swap": TxSwap, "s": TxSwap, "=": TxSwap,
		"=<":     TxLimitSwap,
		"m=<":    TxModifyLimitSwap,
		"out":    TxOutbound,
		"donate": TxDonate, "d": TxDonate,
		"bond":        TxBond,
		"unbond":      TxUnbond,
		"rebond":      TxRebond,
		"leave":       TxLeave,
		"reserve":     TxReserve,
		"refund":      TxRefund,
		"migrate":     TxMigrate,
		"ragnarok":    TxRagnarok,
		"noop":        TxNoOp,
		"consolidate": TxConsolidate,
		"name":        TxTHORName, "n": TxTHORName, "~": TxTHORName,
		"reference": TxReferenceWriteMemo,
		"r":         TxReferenceReadMemo,
		"trade+":    TxTradeAccountDeposit,
		"trade-":    TxTradeAccountWithdrawal,
		"secure+":   TxSecuredAssetDeposit,
		"secure-":   TxSecuredAssetWithdraw,
		"pool+":     TxRunePoolDeposit,
		"pool-":     TxRunePoolWithdraw,
		"switch":    TxSwitch,
		"tcy":       TxTCYClaim,
		"tcy+":      TxTCYStake,
		"tcy-":      TxTCYUnstake,
		"maint":     TxMaint,
		"operator":  TxOperatorRotate,
	}
	for s, expected := range tests {
		result, err := StringToTxType(s)
		c.Assert(err, IsNil, Commentf("input: %s", s))
		c.Check(result, Equals, expected, Commentf("input: %s", s))
	}
	// Case insensitive
	result, err := StringToTxType("ADD")
	c.Assert(err, IsNil)
	c.Check(result, Equals, TxAdd)

	// Invalid
	_, err = StringToTxType("invalid_type")
	c.Assert(err, NotNil)
}

// ============================================================
// Parser helper function tests
// ============================================================

func (s *MemoParserSuite) TestParserGet(c *C) {
	p := parser{parts: []string{"a", "b", "c"}, errs: make([]error, 0)}
	c.Check(p.get(0), Equals, "a")
	c.Check(p.get(1), Equals, "b")
	c.Check(p.get(2), Equals, "c")
	c.Check(p.get(3), Equals, "")  // out of bounds
	c.Check(p.get(-1), Equals, "") // negative
}

func (s *MemoParserSuite) TestParserGetSubIndex(c *C) {
	p := parser{parts: []string{"a/b/c", "x/y"}, errs: make([]error, 0)}
	c.Check(p.getSubIndex(0, 0), Equals, "a")
	c.Check(p.getSubIndex(0, 1), Equals, "b")
	c.Check(p.getSubIndex(0, 2), Equals, "c")
	c.Check(p.getSubIndex(0, 3), Equals, "") // sub out of bounds
	c.Check(p.getSubIndex(1, 0), Equals, "x")
	c.Check(p.getSubIndex(1, 1), Equals, "y")
	c.Check(p.getSubIndex(2, 0), Equals, "")  // idx out of bounds
	c.Check(p.getSubIndex(-1, 0), Equals, "") // negative idx
	c.Check(p.getSubIndex(0, -1), Equals, "") // negative subIdx
}

func (s *MemoParserSuite) TestParserGetInt64(c *C) {
	p := parser{parts: []string{"42", "-7", "notanumber", ""}, errs: make([]error, 0)}
	c.Check(p.getInt64(0, false, 0), Equals, int64(42))
	c.Check(p.getInt64(1, false, 0), Equals, int64(-7))
	c.Check(p.getInt64(2, false, 0), Equals, int64(0)) // invalid, returns default
	c.Check(len(p.errs), Equals, 1)                    // error recorded for invalid required=false but non-empty

	p.errs = make([]error, 0)
	c.Check(p.getInt64(3, false, 99), Equals, int64(99)) // empty optional, returns default
	c.Check(len(p.errs), Equals, 0)                      // no error for empty optional

	p.errs = make([]error, 0)
	c.Check(p.getInt64(3, true, 99), Equals, int64(99)) // empty required, returns default
	c.Check(len(p.errs), Equals, 1)                     // error recorded
}

func (s *MemoParserSuite) TestParserGetUint(c *C) {
	p := parser{parts: []string{"100", "notauint", ""}, errs: make([]error, 0)}
	result := p.getUint(0, false, 0)
	c.Check(result.Equal(cosmos.NewUint(100)), Equals, true)

	result = p.getUint(1, true, 50)
	c.Check(result.Equal(cosmos.NewUint(50)), Equals, true)
	c.Check(len(p.errs), Equals, 1)

	p.errs = make([]error, 0)
	result = p.getUint(2, false, 42)
	c.Check(result.Equal(cosmos.NewUint(42)), Equals, true) // empty optional
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoParserSuite) TestParserGetUintWithMaxValue(c *C) {
	p := parser{parts: []string{"5000", "20000", ""}, errs: make([]error, 0)}

	result := p.getUintWithMaxValue(0, false, 0, 10000)
	c.Check(result.Equal(cosmos.NewUint(5000)), Equals, true)

	result = p.getUintWithMaxValue(1, false, 0, 10000)
	c.Check(result.Equal(cosmos.NewUint(10000)), Equals, true) // capped at max
	c.Check(len(p.errs), Equals, 1)                            // error recorded

	p.errs = make([]error, 0)
	result = p.getUintWithMaxValue(2, false, 500, 10000)
	c.Check(result.Equal(cosmos.NewUint(500)), Equals, true) // empty, uses default
}

func (s *MemoParserSuite) TestParserGetString(c *C) {
	p := parser{parts: []string{"hello", ""}, errs: make([]error, 0)}

	c.Check(p.getString(0, false, "def"), Equals, "hello")
	c.Check(p.getString(1, false, "def"), Equals, "def") // empty, returns default
	c.Check(len(p.errs), Equals, 0)

	c.Check(p.getString(1, true, "def"), Equals, "def") // empty required
	c.Check(len(p.errs), Equals, 1)
}

func (s *MemoParserSuite) TestParserGetStringArrayBySeparator(c *C) {
	p := parser{parts: []string{"a/b/c", ""}, errs: make([]error, 0)}

	result := p.getStringArrayBySeparator(0, false, "/")
	c.Check(result, DeepEquals, []string{"a", "b", "c"})

	result = p.getStringArrayBySeparator(1, false, "/")
	c.Check(result, DeepEquals, []string{})
}

func (s *MemoParserSuite) TestParserGetUintArrayBySeparator(c *C) {
	p := parser{parts: []string{"10/20/30", "", "10/bad/30"}, errs: make([]error, 0)}

	result := p.getUintArrayBySeparator(0, false, "/")
	c.Assert(len(result), Equals, 3)
	c.Check(result[0].Equal(cosmos.NewUint(10)), Equals, true)
	c.Check(result[1].Equal(cosmos.NewUint(20)), Equals, true)
	c.Check(result[2].Equal(cosmos.NewUint(30)), Equals, true)

	result = p.getUintArrayBySeparator(1, false, "/")
	c.Check(len(result), Equals, 0)

	// Invalid entry
	p.errs = make([]error, 0)
	result = p.getUintArrayBySeparator(2, true, "/")
	c.Check(len(result), Equals, 0) // returns empty on error
	c.Check(len(p.errs), Equals, 1)
}

func (s *MemoParserSuite) TestParserGetChain(c *C) {
	p := parser{parts: []string{"BTC", ""}, errs: make([]error, 0)}

	chain := p.getChain(0, false, common.EmptyChain)
	c.Check(chain, Equals, common.BTCChain)

	p.errs = make([]error, 0)
	chain = p.getChain(1, false, common.THORChain)
	c.Check(chain, Equals, common.THORChain) // empty, returns default
	c.Check(len(p.errs), Equals, 0)

	// Required empty field
	p.errs = make([]error, 0)
	_ = p.getChain(1, true, common.THORChain)
	c.Check(len(p.errs), Equals, 1)
}

func (s *MemoParserSuite) TestParserGetAsset(c *C) {
	v := semver.MustParse("1.0.0")
	p := parser{parts: []string{"BTC.BTC", ""}, errs: make([]error, 0), version: v}

	asset := p.getAsset(0, false, common.EmptyAsset)
	c.Check(asset, Equals, common.BTCAsset)

	// Empty optional - NewAssetWithShortCodes("") returns err but get(idx)=="" so error not recorded
	// The returned value is whatever NewAssetWithShortCodes returned
	p.errs = make([]error, 0)
	_ = p.getAsset(1, false, common.BTCAsset)
	c.Check(len(p.errs), Equals, 0) // no error recorded for empty optional

	// ETH.ETH asset
	p2 := parser{parts: []string{"ETH.ETH"}, errs: make([]error, 0), version: v}
	asset = p2.getAsset(0, true, common.EmptyAsset)
	c.Check(asset, Equals, common.ETHAsset)
}

func (s *MemoParserSuite) TestParserGetTxID(c *C) {
	txHash := types.GetRandomTxHash()
	p := parser{parts: []string{txHash.String(), "invalid", ""}, errs: make([]error, 0)}

	txID := p.getTxID(0, false, common.BlankTxID)
	c.Check(txID, Equals, txHash)

	p.errs = make([]error, 0)
	txID = p.getTxID(1, true, common.BlankTxID)
	c.Check(txID, Equals, common.BlankTxID)
	c.Check(len(p.errs), Equals, 1)

	p.errs = make([]error, 0)
	txID = p.getTxID(2, false, common.BlankTxID)
	c.Check(txID, Equals, common.BlankTxID) // empty, returns default
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoParserSuite) TestParserGetAccAddress(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomBech32Addr()
	p := parser{parts: []string{addr.String(), "invalid", ""}, errs: make([]error, 0)}

	result := p.getAccAddress(0, false, nil)
	c.Check(result.String(), Equals, addr.String())

	p.errs = make([]error, 0)
	result = p.getAccAddress(1, true, nil)
	c.Check(result, IsNil)
	c.Check(len(p.errs), Equals, 1)

	p.errs = make([]error, 0)
	result = p.getAccAddress(2, false, addr)
	c.Check(result.String(), Equals, addr.String()) // empty, returns default
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoParserSuite) TestParserGetAddress(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()
	p := parser{parts: []string{addr.String(), "invalid", ""}, errs: make([]error, 0)}

	result := p.getAddress(0, false, common.NoAddress)
	c.Check(result, Equals, addr)

	p.errs = make([]error, 0)
	result = p.getAddress(1, true, common.NoAddress)
	c.Check(result, Equals, common.NoAddress)
	c.Check(len(p.errs), Equals, 1)

	p.errs = make([]error, 0)
	result = p.getAddress(2, false, common.NoAddress)
	c.Check(result, Equals, common.NoAddress) // empty, returns default
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoParserSuite) TestParserGetThorAddress(c *C) {
	types.SetupConfigForTest()
	thorAddr := types.GetRandomTHORAddress()
	ethAddr := "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a"
	p := parser{parts: []string{thorAddr.String(), ethAddr, "invalid", ""}, errs: make([]error, 0)}

	// Valid THOR address
	result := p.getThorAddress(0, true, common.NoAddress)
	c.Check(result, Equals, thorAddr)

	// Non-THOR address with required=true should error
	p.errs = make([]error, 0)
	result = p.getThorAddress(1, true, common.NoAddress)
	c.Check(result, Equals, common.NoAddress)
	c.Check(len(p.errs), Equals, 1)

	// Invalid address
	p.errs = make([]error, 0)
	result = p.getThorAddress(2, true, common.NoAddress)
	c.Check(result, Equals, common.NoAddress)
	c.Check(len(p.errs), Equals, 1)

	// Empty optional
	p.errs = make([]error, 0)
	result = p.getThorAddress(3, false, common.NoAddress)
	c.Check(result, Equals, common.NoAddress)
	c.Check(len(p.errs), Equals, 0)
}

func (s *MemoParserSuite) TestParserGetCoin(c *C) {
	types.SetupConfigForTest()
	v := semver.MustParse("1.0.0")

	// Test trade asset coin (contains ~)
	p := parser{parts: []string{"100BTC~BTC", "", "badformat"}, errs: make([]error, 0), version: v}
	coin := p.getCoin(0, false, common.NoCoin)
	c.Check(coin.IsEmpty(), Equals, false)

	// Test empty
	p.errs = make([]error, 0)
	coin = p.getCoin(1, false, common.NoCoin)
	c.Check(coin, Equals, common.NoCoin)

	// Test invalid coin format with no digits
	p = parser{parts: []string{"BTC~BTC"}, errs: make([]error, 0), version: v}
	coin = p.getCoin(0, true, common.NoCoin)
	c.Check(coin, Equals, common.NoCoin) // no leading digits
	c.Check(len(p.errs), Equals, 1)

	// Test only digits
	p = parser{parts: []string{"100"}, errs: make([]error, 0), version: v}
	coin = p.getCoin(0, true, common.NoCoin)
	c.Check(coin, Equals, common.NoCoin) // all digits, no asset
	c.Check(len(p.errs), Equals, 1)
}

func (s *MemoParserSuite) TestParserIncRequired(c *C) {
	p := parser{errs: make([]error, 0)}
	c.Check(p.requiredFields, Equals, 0)
	p.incRequired(true)
	c.Check(p.requiredFields, Equals, 1)
	p.incRequired(false)
	c.Check(p.requiredFields, Equals, 1) // no change
	p.incRequired(true)
	c.Check(p.requiredFields, Equals, 2)
}

func (s *MemoParserSuite) TestParserError(c *C) {
	p := parser{memo: "test:memo", parts: []string{"test", "memo"}, errs: make([]error, 0)}
	// No errors, requiredFields=0, parts=2, hasMinParams(0+1=1) => 2>=1, so no error
	c.Check(p.Error(), IsNil)

	// With errors
	p.addErr(fmt.Errorf("error1"))
	p.addErr(fmt.Errorf("error2"))
	err := p.Error()
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "(?s).*error1.*")
	c.Check(err.Error(), Matches, "(?s).*error2.*")
}

func (s *MemoParserSuite) TestParserHasMinParams(c *C) {
	p := parser{parts: []string{"a", "b"}, errs: make([]error, 0)}
	p.hasMinParams(2) // exactly enough
	c.Check(len(p.errs), Equals, 0)
	p.hasMinParams(3) // not enough
	c.Check(len(p.errs), Equals, 1)
}

// ============================================================
// parseTradeTarget tests
// ============================================================

func (s *MemoParserSuite) TestParseTradeTarget(c *C) {
	result, err := parseTradeTarget("12345")
	c.Assert(err, IsNil)
	c.Check(result.Equal(cosmos.NewUint(12345)), Equals, true)

	result, err = parseTradeTarget("0")
	c.Assert(err, IsNil)
	c.Check(result.IsZero(), Equals, true)

	result, err = parseTradeTarget("123.456")
	c.Assert(err, IsNil)
	c.Check(result.Equal(cosmos.NewUint(123)), Equals, true) // fractional discarded

	_, err = parseTradeTarget("notanumber")
	c.Assert(err, NotNil)

	// boundary: 1e77 is the largest power of ten that still fits in a 256-bit Uint
	result, err = parseTradeTarget("1e77")
	c.Assert(err, IsNil)
	c.Check(result.IsZero(), Equals, false)

	// magnitude guard: values beyond 2^256-1 must error rather than allocate the
	// full integer. 1e80 is just over the boundary; 1e300000000 is the DoS payload
	// that would otherwise materialize a ~118MB big.Int before being rejected.
	_, err = parseTradeTarget("1e80")
	c.Assert(err, NotNil)

	_, err = parseTradeTarget("1e300000000")
	c.Assert(err, NotNil)

	// overflow guard: an exponent past int32 parses to +Inf without error, and
	// big.Float.Int leaves the destination at zero for Inf. That must be an
	// error, not a silent zero limit (zero means "no price protection").
	_, err = parseTradeTarget("1e700000000")
	c.Assert(err, NotNil)

	// sign guard: NewUintFromBigInt panics on negatives; reject them as an error.
	_, err = parseTradeTarget("-1")
	c.Assert(err, NotNil)

	_, err = parseTradeTarget("-1e80")
	c.Assert(err, NotNil)
}

// ============================================================
// newParser tests
// ============================================================

func (s *MemoParserSuite) TestNewParser(c *C) {
	// Empty memo
	_, err := newParser(cosmos.Context{}, nil, semver.MustParse("1.0.0"), "")
	c.Assert(err, NotNil)

	// Invalid tx type
	_, err = newParser(cosmos.Context{}, nil, semver.MustParse("1.0.0"), "invalidtype:arg")
	c.Assert(err, NotNil)

	// Valid
	p, err := newParser(cosmos.Context{}, nil, semver.MustParse("1.0.0"), "swap:BTC.BTC:addr")
	c.Assert(err, IsNil)
	c.Check(p.getType(), Equals, TxSwap)
	c.Check(len(p.parts), Equals, 3)

	// Pipe separator (trailing data after | ignored)
	p, err = newParser(cosmos.Context{}, nil, semver.MustParse("1.0.0"), "swap:BTC.BTC|extradata")
	c.Assert(err, IsNil)
	c.Check(len(p.parts), Equals, 2)
	c.Check(p.parts[1], Equals, "BTC.BTC")
}

// ============================================================
// ParseMemo (without keeper) tests
// ============================================================

func (s *MemoParserSuite) TestParseMemoWithoutKeeper(c *C) {
	types.SetupConfigForTest()
	v := semver.MustParse("1.0.0")

	// Donate
	memo, err := ParseMemo(v, "d:"+common.RuneAsset().String())
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxDonate), Equals, true)

	// Add liquidity
	memo, err = ParseMemo(v, "+:BTC.BTC")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxAdd), Equals, true)

	// Withdraw
	memo, err = ParseMemo(v, "-:BTC.BTC:5000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxWithdraw), Equals, true)

	// Swap
	memo, err = ParseMemo(v, "=:BTC.BTC:bc1qwqdg6squsna38e46795at95yu9atm8azzmyvckulcc7kytlcckxswvvzej:1000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxSwap), Equals, true)

	// Bond
	addr := types.GetRandomBech32Addr()
	memo, err = ParseMemo(v, fmt.Sprintf("bond:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxBond), Equals, true)

	// Unbond
	memo, err = ParseMemo(v, fmt.Sprintf("unbond:%s:100", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxUnbond), Equals, true)

	// Rebond
	addr2 := types.GetRandomBech32Addr()
	memo, err = ParseMemo(v, fmt.Sprintf("rebond:%s:%s:100", addr, addr2))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRebond), Equals, true)
	rebondMemo, ok := memo.(RebondMemo)
	c.Assert(ok, Equals, true)
	c.Check(rebondMemo.GetNodeAddress().String(), Equals, addr.String())
	c.Check(rebondMemo.GetNewProviderAddress().String(), Equals, addr2.String())
	c.Check(rebondMemo.GetAmount().Equal(cosmos.NewUint(100)), Equals, true)

	// Leave
	memo, err = ParseMemo(v, fmt.Sprintf("leave:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxLeave), Equals, true)

	// Migrate
	memo, err = ParseMemo(v, "migrate:100")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxMigrate), Equals, true)
	c.Check(memo.GetBlockHeight(), Equals, int64(100))

	// Ragnarok
	memo, err = ParseMemo(v, "ragnarok:200")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRagnarok), Equals, true)

	// Outbound
	txHash := types.GetRandomTxHash()
	memo, err = ParseMemo(v, "OUT:"+txHash.String())
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxOutbound), Equals, true)

	// Refund
	memo, err = ParseMemo(v, "REFUND:"+txHash.String())
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRefund), Equals, true)

	// Reserve
	memo, err = ParseMemo(v, "reserve")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxReserve), Equals, true)

	// NoOp
	memo, err = ParseMemo(v, "noop:novault")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxNoOp), Equals, true)
	noopMemo, ok := memo.(NoOpMemo)
	c.Assert(ok, Equals, true)
	c.Check(noopMemo.Action, Equals, "novault")

	// Consolidate
	memo, err = ParseMemo(v, "consolidate")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxConsolidate), Equals, true)

	// Pool deposit
	memo, err = ParseMemo(v, "pool+")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRunePoolDeposit), Equals, true)

	// Pool withdraw
	memo, err = ParseMemo(v, "pool-:5000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxRunePoolWithdraw), Equals, true)

	// Secured asset deposit
	memo, err = ParseMemo(v, fmt.Sprintf("secure+:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxSecuredAssetDeposit), Equals, true)

	// Secured asset withdraw
	thorAddr := types.GetRandomTHORAddress()
	memo, err = ParseMemo(v, fmt.Sprintf("secure-:%s", thorAddr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxSecuredAssetWithdraw), Equals, true)

	// Stable reserve deposit
	memo, err = ParseMemo(v, "stable+")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxStableReserveDeposit), Equals, true)

	// Stable reserve withdraw
	memo, err = ParseMemo(v, "stable-:12345678")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxStableReserveWithdraw), Equals, true)

	// Trade deposit
	memo, err = ParseMemo(v, fmt.Sprintf("trade+:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTradeAccountDeposit), Equals, true)

	// Trade withdraw
	ethAddr := types.GetRandomETHAddress()
	memo, err = ParseMemo(v, fmt.Sprintf("trade-:%s", ethAddr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTradeAccountWithdrawal), Equals, true)

	// Switch
	memo, err = ParseMemo(v, fmt.Sprintf("switch:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxSwitch), Equals, true)

	// TCY claim
	memo, err = ParseMemo(v, fmt.Sprintf("tcy:%s", thorAddr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYClaim), Equals, true)

	// TCY stake
	memo, err = ParseMemo(v, "tcy+")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYStake), Equals, true)

	// TCY unstake
	memo, err = ParseMemo(v, "tcy-:5000")
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTCYUnstake), Equals, true)

	// Maint
	memo, err = ParseMemo(v, fmt.Sprintf("maint:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxMaint), Equals, true)

	// Operator rotate
	memo, err = ParseMemo(v, fmt.Sprintf("operator:%s", addr))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxOperatorRotate), Equals, true)
	opMemo, ok := memo.(OperatorRotateMemo)
	c.Assert(ok, Equals, true)
	c.Check(opMemo.OperatorAddress.String(), Equals, addr.String())

	// THORName
	thorAddr2 := types.GetRandomTHORAddress()
	memo, err = ParseMemo(v, fmt.Sprintf("name:myname:BTC:%s", thorAddr2))
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxTHORName), Equals, true)

	// Error: empty memo
	_, err = ParseMemo(v, "")
	c.Assert(err, NotNil)

	// Error: unknown type
	_, err = ParseMemo(v, "unknown:blah")
	c.Assert(err, NotNil)
}

// ============================================================
// Memo type String() and getter tests
// ============================================================

func (s *MemoParserSuite) TestMemoBaseGetters(c *C) {
	base := MemoBase{TxType: TxSwap, Asset: common.BTCAsset}
	c.Check(base.GetType(), Equals, TxSwap)
	c.Check(base.IsType(TxSwap), Equals, true)
	c.Check(base.IsType(TxAdd), Equals, false)
	c.Check(base.GetAsset(), Equals, common.BTCAsset)
	c.Check(base.GetAmount().IsZero(), Equals, true)
	c.Check(base.GetDestination(), Equals, common.NoAddress)
	c.Check(base.GetSlipLimit().IsZero(), Equals, true)
	c.Check(base.GetTxID(), Equals, common.TxID(""))
	c.Check(base.GetAccAddress().Empty(), Equals, true)
	c.Check(base.GetBlockHeight(), Equals, int64(0))
	c.Check(base.IsOutbound(), Equals, false)
	c.Check(base.IsInbound(), Equals, true)
	c.Check(base.IsInternal(), Equals, false)
	c.Check(base.IsEmpty(), Equals, false)
	c.Check(base.GetDexAggregator(), Equals, "")
	c.Check(base.GetDexTargetAddress(), Equals, "")
	c.Check(base.GetDexTargetLimit(), IsNil)
	c.Check(base.GetAffiliateTHORName(), IsNil)
	c.Check(base.GetRefundAddress(), Equals, common.NoAddress)
	c.Check(base.GetAffiliates(), IsNil)
	c.Check(base.GetAffiliatesBasisPoints(), IsNil)
	c.Check(base.GetAddress(), Equals, common.NoAddress)
	c.Check(base.String(), Equals, "")
}

func (s *MemoParserSuite) TestDonateMemoString(c *C) {
	m := DonateMemo{MemoBase: MemoBase{TxType: TxDonate, Asset: common.BTCAsset}}
	c.Check(m.String(), Equals, "DONATE:BTC.BTC")
}

func (s *MemoParserSuite) TestMigrateMemoStringAndGetter(c *C) {
	m := NewMigrateMemo(500)
	c.Check(m.String(), Equals, "MIGRATE:500")
	c.Check(m.GetBlockHeight(), Equals, int64(500))
}

func (s *MemoParserSuite) TestRagnarokMemoStringAndGetter(c *C) {
	m := NewRagnarokMemo(1024)
	c.Check(m.String(), Equals, "RAGNAROK:1024")
	c.Check(m.GetBlockHeight(), Equals, int64(1024))
}

func (s *MemoParserSuite) TestOutboundMemoStringAndGetter(c *C) {
	txID := types.GetRandomTxHash()
	m := NewOutboundMemo(txID)
	c.Check(m.String(), Equals, "OUT:"+txID.String())
	c.Check(m.GetTxID(), Equals, txID)
}

func (s *MemoParserSuite) TestRefundMemoStringAndGetter(c *C) {
	txID := types.GetRandomTxHash()
	m := NewRefundMemo(txID)
	c.Check(m.String(), Equals, "REFUND:"+txID.String())
	c.Check(m.GetTxID(), Equals, txID)
}

func (s *MemoParserSuite) TestConsolidateMemoString(c *C) {
	m := NewConsolidateMemo()
	c.Check(m.String(), Equals, "consolidate")
	c.Check(m.GetType(), Equals, TxConsolidate)
}

func (s *MemoParserSuite) TestNoOpMemoString(c *C) {
	m := NewNoOpMemo("")
	c.Check(m.String(), Equals, "noop")

	m = NewNoOpMemo("novault")
	c.Check(m.String(), Equals, "noop:novault")
}

func (s *MemoParserSuite) TestBondMemoGetter(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomBech32Addr()
	bp := types.GetRandomBech32Addr()
	m := NewBondMemo(addr, bp, 500)
	c.Check(m.GetAccAddress().String(), Equals, addr.String())
	c.Check(m.BondProviderAddress.String(), Equals, bp.String())
	c.Check(m.NodeOperatorFee, Equals, int64(500))
}

func (s *MemoParserSuite) TestUnbondMemoGetters(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomBech32Addr()
	bp := types.GetRandomBech32Addr()
	m := NewUnbondMemo(addr, bp, cosmos.NewUint(300))
	c.Check(m.GetAccAddress().String(), Equals, addr.String())
	c.Check(m.GetAmount().Equal(cosmos.NewUint(300)), Equals, true)
}

func (s *MemoParserSuite) TestLeaveMemoGetter(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomBech32Addr()
	m := NewLeaveMemo(addr)
	c.Check(m.GetAccAddress().String(), Equals, addr.String())
}

func (s *MemoParserSuite) TestAddLiquidityMemoStringVariants(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()

	// No address or affiliate
	m := NewAddLiquidityMemo(common.BTCAsset, "", "", cosmos.ZeroUint())
	c.Check(m.String(), Equals, "+:BTC.BTC")

	// With address
	m = NewAddLiquidityMemo(common.BTCAsset, addr, "", cosmos.ZeroUint())
	c.Check(m.String(), Equals, fmt.Sprintf("+:BTC.BTC:%s", addr))

	// With address and affiliate
	affAddr := types.GetRandomTHORAddress()
	m = NewAddLiquidityMemo(common.BTCAsset, addr, affAddr, cosmos.NewUint(500))
	expected := fmt.Sprintf("+:BTC.BTC:%s:%s:500", addr, affAddr)
	c.Check(m.String(), Equals, expected)

	// GetDestination
	c.Check(m.GetDestination(), Equals, addr)
}

func (s *MemoParserSuite) TestWithdrawLiquidityMemoGetters(c *C) {
	m := NewWithdrawLiquidityMemo(common.BTCAsset, cosmos.NewUint(5000), common.ETHAsset)
	c.Check(m.GetAmount().Equal(cosmos.NewUint(5000)), Equals, true)
	c.Check(m.GetWithdrawalAsset(), Equals, common.ETHAsset)
}

func (s *MemoParserSuite) TestSwapMemoGetters(c *C) {
	types.SetupConfigForTest()
	dest := types.GetRandomTHORAddress()
	affAddr := types.GetRandomTHORAddress()
	tn := types.NewTHORName("test", 50, []types.THORNameAlias{{Chain: common.THORChain, Address: dest}})
	thorAccAddr, _ := dest.AccAddress()
	tn.Owner = thorAccAddr
	dexLimit := cosmos.NewUint(1000)
	affiliates := []string{"aff1"}
	affBps := []cosmos.Uint{cosmos.NewUint(100)}

	m := NewSwapMemo(common.BTCAsset, dest, cosmos.NewUint(500), affAddr, cosmos.NewUint(100),
		"dexagg", "dextarget", dexLimit, types.SwapType_limit, 10, 5, tn, common.NoAddress, affiliates, affBps)

	c.Check(m.GetDestination(), Equals, dest)
	c.Check(m.GetSlipLimit().Equal(cosmos.NewUint(500)), Equals, true)
	c.Check(m.GetAffiliateAddress(), Equals, affAddr)
	c.Check(m.GetAffiliateBasisPoints().Equal(cosmos.NewUint(100)), Equals, true)
	c.Check(m.GetDexAggregator(), Equals, "dexagg")
	c.Check(m.GetDexTargetAddress(), Equals, "dextarget")
	c.Check(m.GetDexTargetLimit().Equal(cosmos.NewUint(1000)), Equals, true)
	c.Check(m.GetSwapType(), Equals, types.SwapType_limit)
	c.Check(m.GetStreamQuantity(), Equals, uint64(10))
	c.Check(m.GetStreamInterval(), Equals, uint64(5))
	c.Check(m.GetAffiliateTHORName(), NotNil)
	c.Check(m.GetRefundAddress(), Equals, common.NoAddress)
	c.Check(m.GetAffiliates(), DeepEquals, affiliates)
	c.Check(len(m.GetAffiliatesBasisPoints()), Equals, 1)
}

func (s *MemoParserSuite) TestSwapMemoString(c *C) {
	types.SetupConfigForTest()
	dest := types.GetRandomTHORAddress()

	// Basic swap
	m := NewSwapMemo(common.BTCAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", cosmos.ZeroUint(), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil)
	result := m.String()
	c.Check(result, Matches, "=:BTC.BTC:.*")

	// With slip limit
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.NewUint(1000), common.NoAddress, cosmos.ZeroUint(),
		"", "", cosmos.ZeroUint(), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil)
	result = m.String()
	c.Check(result, Matches, "=:BTC.BTC:.*:1000")

	// With streaming params
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.NewUint(500), common.NoAddress, cosmos.ZeroUint(),
		"", "", cosmos.ZeroUint(), types.SwapType_market, 20, 10, types.NewTHORName("", 0, nil), "", nil, nil)
	result = m.String()
	c.Check(result, Matches, "=:BTC.BTC:.*:500/10/20")

	// With dex agg
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"dex", "target", cosmos.ZeroUint(), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil)
	result = m.String()
	c.Check(result, Matches, "=:BTC.BTC:.*:::dex:target")

	// With dex target limit
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"dex", "target", cosmos.NewUint(999), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil)
	result = m.String()
	c.Check(result, Matches, "=:BTC.BTC:.*:::dex:target:999")

	// With refund address
	refAddr := types.GetRandomTHORAddress()
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", cosmos.ZeroUint(), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), refAddr, nil, nil)
	result = m.String()
	c.Check(result, Matches, fmt.Sprintf("=:BTC.BTC:%s/%s", dest, refAddr))

	// ShortString
	m = NewSwapMemo(common.BTCAsset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", cosmos.ZeroUint(), types.SwapType_market, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil)
	short := m.ShortString()
	c.Check(len(short) > 0, Equals, true)
}

func (s *MemoParserSuite) TestManageTHORNameMemoGetters(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()
	owner := types.GetRandomBech32Addr()
	m := NewManageTHORNameMemo("test", common.BTCChain, addr, 100, common.BTCAsset, owner, 0)
	c.Check(m.GetName(), Equals, "test")
	c.Check(m.GetChain(), Equals, common.BTCChain)
	c.Check(m.GetAddress(), Equals, addr)
	c.Check(m.GetBlockExpire(), Equals, int64(100))
}

func (s *MemoParserSuite) TestSwitchMemoGetter(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomBech32Addr()
	m := NewSwitchMemo(addr)
	c.Check(m.GetAccAddress().String(), Equals, addr.String())
}

func (s *MemoParserSuite) TestTradeAccountMemoGetters(c *C) {
	types.SetupConfigForTest()
	// Deposit
	accAddr := types.GetRandomBech32Addr()
	dep := NewTradeAccountDepositMemo(accAddr)
	c.Check(dep.GetAccAddress().String(), Equals, accAddr.String())

	// Withdrawal
	ethAddr := types.GetRandomETHAddress()
	wd := NewTradeAccountWithdrawalMemo(ethAddr)
	c.Check(wd.GetAddress(), Equals, ethAddr)
}

func (s *MemoParserSuite) TestSecuredAssetMemoGetters(c *C) {
	types.SetupConfigForTest()
	// Deposit
	accAddr := types.GetRandomBech32Addr()
	dep := NewSecuredAssetDepositMemo(accAddr)
	c.Check(dep.GetAccAddress().String(), Equals, accAddr.String())

	// Withdraw
	thorAddr := types.GetRandomTHORAddress()
	wd := NewSecuredAssetWithdrawMemo(thorAddr)
	c.Check(wd.GetAddress(), Equals, thorAddr)
}

func (s *MemoParserSuite) TestStableReserveMemoGetters(c *C) {
	dep := NewStableReserveDepositMemo()
	c.Check(dep.IsType(TxStableReserveDeposit), Equals, true)

	amount := cosmos.NewUint(12345678)
	wd := NewStableReserveWithdrawMemo(amount)
	c.Check(wd.IsType(TxStableReserveWithdraw), Equals, true)
	c.Check(wd.GetAmount().Equal(amount), Equals, true)
}

func (s *MemoParserSuite) TestRunePoolDepositMemoString(c *C) {
	m := NewRunePoolDepositMemo()
	c.Check(m.String(), Equals, "pool+")
	c.Check(m.ShortString(), Equals, "pool+")
}

func (s *MemoParserSuite) TestRunePoolWithdrawMemoGetters(c *C) {
	types.SetupConfigForTest()
	affAddr := types.GetRandomTHORAddress()
	tn := types.NewTHORName("test", 50, []types.THORNameAlias{{Chain: common.THORChain, Address: affAddr}})
	thorAccAddr, _ := affAddr.AccAddress()
	tn.Owner = thorAccAddr

	m := NewRunePoolWithdrawMemo(cosmos.NewUint(5000), affAddr, cosmos.NewUint(100), tn)
	c.Check(m.GetBasisPts().Equal(cosmos.NewUint(5000)), Equals, true)
	c.Check(m.GetAffiliateAddress(), Equals, affAddr)
	c.Check(m.GetAffiliateBasisPoints().Equal(cosmos.NewUint(100)), Equals, true)
	c.Check(m.GetAffiliateTHORName(), NotNil)
	// String includes the type prefix from txToStringMap
	str := m.String()
	c.Check(str, Matches, ".*:5000:.*:100")

	// Without thorname
	m2 := NewRunePoolWithdrawMemo(cosmos.NewUint(5000), common.NoAddress, cosmos.ZeroUint(), types.NewTHORName("", 0, nil))
	c.Check(m2.GetAffiliateTHORName(), IsNil)
}

func (s *MemoParserSuite) TestTCYClaimMemoGetter(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()
	m := NewTCYClaimMemo(addr)
	c.Check(m.GetAddress(), Equals, addr)
}

func (s *MemoParserSuite) TestTCYUnstakeMemo(c *C) {
	m := NewTCYUnstakeMemo(cosmos.NewUint(5000))
	c.Check(m.BasisPoints.Equal(cosmos.NewUint(5000)), Equals, true)
}

func (s *MemoParserSuite) TestModifyLimitSwapMemoGetters(c *C) {
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(200))
	m := NewModifyLimitSwapMemo(source, target, cosmos.NewUint(300))
	c.Check(m.GetSource().Equals(source), Equals, true)
	c.Check(m.GetTarget().Equals(target), Equals, true)
	c.Check(m.GetModifiedTargetAmount().Equal(cosmos.NewUint(300)), Equals, true)
}

func (s *MemoParserSuite) TestRebondMemoGetters(c *C) {
	types.SetupConfigForTest()
	node := types.GetRandomBech32Addr()
	provider := types.GetRandomBech32Addr()
	m := NewRebondMemo(node, provider, cosmos.NewUint(500))
	c.Check(m.GetNodeAddress().String(), Equals, node.String())
	c.Check(m.GetNewProviderAddress().String(), Equals, provider.String())
	c.Check(m.GetAmount().Equal(cosmos.NewUint(500)), Equals, true)
}

// ============================================================
// ParseMemo panic recovery test
// ============================================================

func (s *MemoParserSuite) TestParseMemoEmptyMemo(c *C) {
	// EmptyMemo
	c.Check(EmptyMemo.IsEmpty(), Equals, true)
	c.Check(EmptyMemo.GetType(), Equals, TxUnknown)
}

// ============================================================
// Reference memo type getters
// ============================================================

func (s *MemoParserSuite) TestReferenceWriteMemoGetters(c *C) {
	m := NewReferenceWriteMemo(common.BTCAsset, "=:ETH.ETH:addr")
	c.Check(m.GetAsset(), DeepEquals, common.BTCAsset)
	c.Check(m.GetMemo(), Equals, "=:ETH.ETH:addr")
}

func (s *MemoParserSuite) TestReferenceReadMemoGetters(c *C) {
	m := NewReferenceReadMemo("12345")
	c.Check(m.GetReference(), Equals, "12345")
	c.Check(m.CreateMemo(), Equals, "r:12345")
}

// ============================================================
// Limit swap type tests
// ============================================================

func (s *MemoParserSuite) TestLimitSwapParsing(c *C) {
	types.SetupConfigForTest()
	v := semver.MustParse("1.0.0")

	// Limit swap without keeper parses through ParseSwapMemoV1 which creates a SwapMemo with TxSwap type
	memo, err := ParseMemo(v, "=<:BTC.BTC:bc1qwqdg6squsna38e46795at95yu9atm8azzmyvckulcc7kytlcckxswvvzej:1000")
	c.Assert(err, IsNil)
	swapMemo, ok := memo.(SwapMemo)
	c.Assert(ok, Equals, true)
	// ParseSwapMemoV1 hardcodes TxSwap in the MemoBase
	c.Check(swapMemo.GetType(), Equals, TxSwap)
	c.Check(swapMemo.GetSlipLimit().Equal(cosmos.NewUint(1000)), Equals, true)
}

// ============================================================
// Parser getAddressWithKeeper without keeper (nil)
// ============================================================

func (s *MemoParserSuite) TestGetAddressWithKeeperNilKeeper(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()
	p := parser{parts: []string{addr.String()}, errs: make([]error, 0), keeper: nil}

	result := p.getAddressWithKeeper(0, false, common.NoAddress, common.THORChain)
	c.Check(result, Equals, addr)
}

func (s *MemoParserSuite) TestGetAddressAndRefundAddressWithoutKeeper(c *C) {
	types.SetupConfigForTest()
	addr := types.GetRandomTHORAddress()
	refAddr := types.GetRandomTHORAddress()

	// With refund address (no keeper)
	p := parser{parts: []string{fmt.Sprintf("%s/%s", addr, refAddr)}, errs: make([]error, 0), keeper: nil}
	dest, refund := p.getAddressAndRefundAddressWithKeeper(0, false, common.NoAddress, common.THORChain)
	c.Check(dest, Equals, addr)
	c.Check(refund, Equals, refAddr)

	// Without refund address (no keeper)
	p = parser{parts: []string{addr.String()}, errs: make([]error, 0), keeper: nil}
	dest, refund = p.getAddressAndRefundAddressWithKeeper(0, false, common.NoAddress, common.THORChain)
	c.Check(dest, Equals, addr)
	c.Check(refund, Equals, common.NoAddress)

	// Empty destination with refund address should error
	p = parser{parts: []string{fmt.Sprintf("/%s", refAddr)}, errs: make([]error, 0), keeper: nil}
	dest, refund = p.getAddressAndRefundAddressWithKeeper(0, false, common.NoAddress, common.THORChain)
	c.Check(dest.IsEmpty(), Equals, true)
	c.Check(refund, Equals, refAddr)
	c.Check(len(p.errs) > 0, Equals, true) // refund set but no destination
}

func (s *MemoParserSuite) TestParserGetTHORNameWithoutKeeper(c *C) {
	p := parser{parts: []string{"thorname"}, errs: make([]error, 0), keeper: nil}
	tn := p.getTHORName(0, false, types.NewTHORName("", 0, nil), -1)
	c.Check(tn.Name, Equals, "")
}

func (s *MemoParserSuite) TestParseAddLiquidityMemoWithExplicitAssetAddress(c *C) {
	types.SetupConfigForTest()
	v := semver.MustParse("1.0.0")

	thorAddr := types.GetRandomTHORAddress()
	xmrAddr := "45v8VYPut8S9wY9fTvn1gKiGN5AMMzsCK9kLNcEpEESygy4vU3Jf3uE4Zyqv2bDLwTToH8ArrsZ3W28kmKUKJkjY8fN9anA"

	// pair address plus explicit asset-chain address (memoless XMR adds)
	memo, err := ParseMemo(v, "+:XMR.XMR:"+thorAddr.String()+"/"+xmrAddr)
	c.Assert(err, IsNil)
	c.Check(memo.IsType(TxAdd), Equals, true)
	addMemo, ok := memo.(AddLiquidityMemo)
	c.Assert(ok, Equals, true)
	c.Check(addMemo.GetDestination().String(), Equals, thorAddr.String())
	c.Check(addMemo.GetAssetAddress().String(), Equals, xmrAddr)
	c.Check(addMemo.GetRefundAddress().String(), Equals, xmrAddr)

	// String() round-trips the composite address field
	reparsed, err := ParseMemo(v, addMemo.String())
	c.Assert(err, IsNil)
	reparsedAdd, ok := reparsed.(AddLiquidityMemo)
	c.Assert(ok, Equals, true)
	c.Check(reparsedAdd.GetDestination().String(), Equals, thorAddr.String())
	c.Check(reparsedAdd.GetRefundAddress().String(), Equals, xmrAddr)

	// plain pair address still parses with no refund address
	memo, err = ParseMemo(v, "+:XMR.XMR:"+thorAddr.String())
	c.Assert(err, IsNil)
	addMemo, ok = memo.(AddLiquidityMemo)
	c.Assert(ok, Equals, true)
	c.Check(addMemo.GetRefundAddress().IsEmpty(), Equals, true)

	// Other L1 adds retain the existing single-address grammar so an explicitly
	// supplied asset address is never parsed and then silently ignored.
	ethAddr := types.GetRandomETHAddress()
	ethPairAddr := types.GetRandomTHORAddress()
	_, err = ParseMemo(v, "+:ETH.ETH:"+ethPairAddr.String()+"/"+ethAddr.String())
	c.Assert(err, NotNil)
}
