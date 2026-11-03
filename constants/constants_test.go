//go:build mocknet
// +build mocknet

package constants

import (
	"encoding/json"
	"sort"
	"strings"

	. "gopkg.in/check.v1"
)

// TestConstantValsGetBoolValue tests GetBoolValue on ConstantVals
func (s *ConstantsSuite) TestConstantValsGetBoolValue(c *C) {
	consts := NewConstantValue()

	// StrictBondLiquidityRatio has a base value of true, but mocknet overrides it to false
	c.Check(consts.GetBoolValue(StrictBondLiquidityRatio), Equals, false)

	// Non-existent bool key should return false
	c.Check(consts.GetBoolValue(ConstantName(9999)), Equals, false)
}

// TestConstantValsGetStringValue tests GetStringValue on ConstantVals
func (s *ConstantsSuite) TestConstantValsGetStringValue(c *C) {
	consts := NewConstantValue()

	// DefaultPoolStatus has a base value "Staged" but mocknet overrides to "Available"
	c.Check(consts.GetStringValue(DefaultPoolStatus), Equals, "Available")

	// RequiredPriceFeeds has a base value set but no mocknet override, so return base value
	c.Check(consts.GetStringValue(RequiredPriceFeeds), Not(Equals), "")

	// Non-existent string key should return ""
	c.Check(consts.GetStringValue(ConstantName(9999)), Equals, "")
}

// TestConstantValsGetInt64ValueNonExistent tests GetInt64Value with a non-existent key
func (s *ConstantsSuite) TestConstantValsGetInt64ValueNonExistent(c *C) {
	consts := NewConstantValue()

	// Non-existent key should return 0
	c.Check(consts.GetInt64Value(ConstantName(9999)), Equals, int64(0))
}

// TestConstantValsGetInt64ValueOverride tests that overrides take precedence
func (s *ConstantsSuite) TestConstantValsGetInt64ValueOverride(c *C) {
	consts := NewConstantValue()

	// DesiredValidatorSet has base value 100 but mocknet overrides to 12
	c.Check(consts.GetInt64Value(DesiredValidatorSet), Equals, int64(12))

	// ChurnInterval has base value 43200 but mocknet overrides to 60
	c.Check(consts.GetInt64Value(ChurnInterval), Equals, int64(60))
}

// TestConstantValsString tests the String() method on ConstantVals
func (s *ConstantsSuite) TestConstantValsString(c *C) {
	consts := NewConstantValue()
	result := consts.String()
	lines := strings.Split(strings.TrimSuffix(result, "\n"), "\n")
	c.Assert(lines, HasLen, len(consts.int64values)+len(consts.boolValues))

	names := make([]string, len(lines))
	for i, line := range lines {
		name, _, ok := strings.Cut(line, ":")
		c.Assert(ok, Equals, true)
		names[i] = name
	}
	c.Check(sort.StringsAreSorted(names[:len(consts.int64values)]), Equals, true)
	c.Check(sort.StringsAreSorted(names[len(consts.int64values):]), Equals, true)
}

// TestConstantValsGetConstantValsByKeyname tests GetConstantValsByKeyname
func (s *ConstantsSuite) TestConstantValsGetConstantValsByKeyname(c *C) {
	consts := NewConstantValue()
	result := consts.GetConstantValsByKeyname()

	// Should have int64 values
	c.Assert(len(result.Int64Values) > 0, Equals, true)

	// Should have bool values
	c.Assert(len(result.BoolValues) > 0, Equals, true)

	// Should have string values
	c.Assert(len(result.StringValues) > 0, Equals, true)

	// Check that overrides are reflected
	c.Check(result.Int64Values["DesiredValidatorSet"], Equals, int64(12))
	c.Check(result.BoolValues["StrictBondLiquidityRatio"], Equals, false)
	c.Check(result.StringValues["DefaultPoolStatus"], Equals, "Available")
}

// TestConstantValsMarshalJSON tests MarshalJSON
func (s *ConstantsSuite) TestConstantValsMarshalJSON(c *C) {
	consts := NewConstantValue()
	data, err := consts.MarshalJSON()
	c.Assert(err, IsNil)
	c.Assert(len(data) > 0, Equals, true)

	// Should be valid JSON
	var result ConstantValsByKeyname
	err = json.Unmarshal(data, &result)
	c.Assert(err, IsNil)
	c.Assert(len(result.Int64Values) > 0, Equals, true)
}

// TestDummyConstants tests all DummyConstants methods
func (s *ConstantsSuite) TestDummyConstants(c *C) {
	int64Vals := map[ConstantName]int64{
		EmissionCurve: 42,
		BlocksPerYear: 100,
	}
	boolVals := map[ConstantName]bool{
		StrictBondLiquidityRatio: true,
	}
	stringVals := map[ConstantName]string{
		DefaultPoolStatus: "TestStatus",
	}
	dc := NewDummyConstants(int64Vals, boolVals, stringVals)

	// Test GetInt64Value
	c.Check(dc.GetInt64Value(EmissionCurve), Equals, int64(42))
	c.Check(dc.GetInt64Value(BlocksPerYear), Equals, int64(100))
	c.Check(dc.GetInt64Value(PoolCycle), Equals, int64(0)) // not set

	// Test GetBoolValue
	c.Check(dc.GetBoolValue(StrictBondLiquidityRatio), Equals, true)
	c.Check(dc.GetBoolValue(ConstantName(9999)), Equals, false) // not set

	// Test GetStringValue
	c.Check(dc.GetStringValue(DefaultPoolStatus), Equals, "TestStatus")
	c.Check(dc.GetStringValue(ConstantName(9999)), Equals, "") // not set

	// Test String
	c.Check(dc.String(), Equals, "")

	// Test GetConstantValsByKeyname
	result := dc.GetConstantValsByKeyname()
	c.Check(result, DeepEquals, ConstantValsByKeyname{})
}

// TestCamelToSnakeUpperAdditional tests additional CamelToSnakeUpper cases
func (s *ConstantsSuite) TestCamelToSnakeUpperAdditional(c *C) {
	c.Check(CamelToSnakeUpper("EmissionCurve"), Equals, "EMISSION_CURVE")
	c.Check(CamelToSnakeUpper("MaxRuneSupply"), Equals, "MAX_RUNE_SUPPLY")
	c.Check(CamelToSnakeUpper("POLBuffer"), Equals, "POL_BUFFER")
	c.Check(CamelToSnakeUpper("MinCR"), Equals, "MIN_CR")
	c.Check(CamelToSnakeUpper("MaxCR"), Equals, "MAX_CR")
	c.Check(CamelToSnakeUpper("RUNEPoolEnabled"), Equals, "RUNE_POOL_ENABLED")
	c.Check(CamelToSnakeUpper("TCYStakingHalt"), Equals, "TCY_STAKING_HALT")
	c.Check(CamelToSnakeUpper("EVMDisableContractWhitelist"), Equals, "EVM_DISABLE_CONTRACT_WHITELIST")
}
