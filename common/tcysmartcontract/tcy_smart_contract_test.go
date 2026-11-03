//go:build mocknet
// +build mocknet

package tcysmartcontract

import (
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TCYSmartContractSuite struct{}

var _ = Suite(&TCYSmartContractSuite{})

func (s *TCYSmartContractSuite) TestIsTCYSmartContractAddress(c *C) {
	tests := []struct {
		name    string
		address string
		want    bool
	}{
		{
			name:    "valid tcy smart contract address",
			address: TCYSmartContractAddresses[0],
			want:    true,
		},
		{
			name:    "invalid address - not in list",
			address: "tthor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq5y7un4",
			want:    false,
		},
		{
			name:    "empty address",
			address: "",
			want:    false,
		},
	}
	for _, tt := range tests {
		c.Logf("case: %s", tt.name)
		addr, _ := common.NewAddress(tt.address)
		got := IsTCYSmartContractAddress(addr)
		c.Assert(got, Equals, tt.want)
	}
}

func (s *TCYSmartContractSuite) TestGetTCYSmartContractAddresses(c *C) {
	addrs, err := GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, len(TCYSmartContractAddresses))
	for i, addr := range addrs {
		c.Assert(addr.String(), Equals, TCYSmartContractAddresses[i])
	}
}
