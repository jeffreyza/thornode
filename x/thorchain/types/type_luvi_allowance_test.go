package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LuviAllowanceSuite struct{}

var _ = Suite(&LuviAllowanceSuite{})

func luviTestState(r, a, l, s uint64) LuviState {
	return NewLuviState(Pool{
		BalanceRune:  cosmos.NewUint(r),
		BalanceAsset: cosmos.NewUint(a),
		LPUnits:      cosmos.NewUint(l),
	}, cosmos.NewUint(s))
}

func (LuviAllowanceSuite) TestAllowanceTerms(c *C) {
	prev := luviTestState(1e12, 1e10, 1e12, 0)

	// nothing happened: nothing allowed
	c.Check(LuviAllowance(prev, prev, cosmos.ZeroUint(), 0).IsZero(), Equals, true)

	// gas: (g/A)^2, capped at g = A
	gas := LuviAllowance(prev, prev, cosmos.NewUint(1e8), 0)
	c.Check(gas.String(), Equals, "0.000100000000000000")
	c.Check(LuviAllowance(prev, prev, cosmos.NewUint(5e10), 0).String(), Equals, "1.000000000000000000")

	// LP-flow rounding: 1/A + 1/R per flow
	c.Check(LuviAllowance(prev, prev, cosmos.ZeroUint(), 2).String(), Equals, "0.000000000202000000")

	// synth mint of y = 1% of A: y^3 * (1/16 + 2y)
	next := luviTestState(1e12, 1e10, 1e12, 1e8)
	c.Check(LuviAllowance(prev, next, cosmos.ZeroUint(), 0).String(), Equals, "0.000000082500000000")
	// symmetric for a burn, and capped at y = 1
	c.Check(LuviAllowance(next, prev, cosmos.ZeroUint(), 0).String(), Equals, "0.000000082500000000")
	big := luviTestState(1e12, 1e10, 1e12, 5e10)
	c.Check(LuviAllowance(prev, big, cosmos.ZeroUint(), 0).String(), Equals, "2.062500000000000000")

	// an empty previous state has no value to protect
	empty := luviTestState(0, 0, 0, 0)
	c.Check(LuviAllowance(empty, prev, cosmos.NewUint(1), 3).IsZero(), Equals, true)
}

func (LuviAllowanceSuite) TestStepEvents(c *C) {
	events := cosmos.Events{
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "10")),
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "5")),
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "bad")),
		cosmos.NewEvent(SlashEventType, cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("BTC.BTC", "-7"), cosmos.NewAttribute("THOR.RUNE", "70")),
		cosmos.NewEvent(SlashEventType, cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("BTC.BTC", "3")),
		cosmos.NewEvent(AddLiquidityEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "100")),
		cosmos.NewEvent(WithdrawEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "40")),
		cosmos.NewEvent(WithdrawEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "x")),
		cosmos.NewEvent(ErrataEventType, cosmos.NewAttribute("asset", "LTC.LTC")),
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "ETH.ETH")),
	}
	ev := NewLuviStepEvents(FlattenLuviEvents(events))
	c.Check(ev.AssetPaidOf("ETH.ETH").String(), Equals, "15")
	c.Check(ev.AssetPaidOf("BTC.BTC").String(), Equals, "7")
	c.Check(ev.AssetPaidOf("DOGE.DOGE").IsZero(), Equals, true)
	c.Check(ev.Flows("ETH.ETH"), Equals, 2)
	c.Check(ev.RoundingOps("ETH.ETH"), Equals, 4)
	c.Check(ev.RoundingOps("DOGE.DOGE"), Equals, 1)
	c.Check(ev.Errata["LTC.LTC"], Equals, true)
	c.Check(ev.Errata["ETH.ETH"], Equals, false)
}

func (LuviAllowanceSuite) TestRestoreTracker(c *C) {
	a := luviTestState(1e12, 1e10, 1e12, 3e9)
	b := luviTestState(1.1e12, 1e10, 1e12, 3e9)
	d := luviTestState(1.2e12, 0.95e10, 1e12, 4e9)

	whole, err := NewLuviTracker(a)
	c.Assert(err, IsNil)
	_, err = whole.Observe(b)
	c.Assert(err, IsNil)
	c.Check(whole.Prev().BalanceRune.String(), Equals, b.BalanceRune.String())

	// resuming from a stored snapshot continues exactly
	resumed, err := RestoreLuviTracker(whole.Prev(), whole.RawC(), whole.RawG())
	c.Assert(err, IsNil)
	_, err = whole.Observe(d)
	c.Assert(err, IsNil)
	_, err = resumed.Observe(d)
	c.Assert(err, IsNil)
	c.Check(resumed.RawG().String(), Equals, whole.RawG().String())
	c.Check(resumed.RawC().String(), Equals, whole.RawC().String())

	// the raw accessors are copies
	g := resumed.RawG()
	g.SetInt64(0)
	c.Check(resumed.RawG().String(), Equals, whole.RawG().String())

	_, err = RestoreLuviTracker(luviTestState(0, 0, 0, 0), g, g)
	c.Check(err, NotNil)
}
