package tcyclaimlist

import (
	"testing"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TCYClaimListSuite struct{}

var _ = Suite(&TCYClaimListSuite{})

func (s *TCYClaimListSuite) TestGetTCYClaimsList(c *C) {
	claims := GetTCYClaimsList()
	c.Assert(len(claims) > 0, Equals, true)

	// Verify each claim has required string fields populated
	for i, claim := range claims {
		c.Logf("claim[%d]", i)
		c.Assert(claim.Asset == "", Equals, false)
		c.Assert(claim.Address == "", Equals, false)
	}
}

func (s *TCYClaimListSuite) TestTCYClaimsJSONStructure(c *C) {
	claims := GetTCYClaimsList()
	c.Assert(len(claims) > 0, Equals, true)

	// Verify the first claim has expected structure
	first := claims[0]
	c.Assert(first.Asset == "", Equals, false)
	c.Assert(first.Address == "", Equals, false)
}

func (s *TCYClaimListSuite) TestTCYClaimsListRawValid(c *C) {
	// Verify the embedded JSON was parsed correctly by init()
	// If init() panicked, we wouldn't reach this test
	c.Assert(len(TCYClaimsListRaw) > 0, Equals, true)
}
