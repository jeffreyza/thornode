package common

import (
	"bytes"

	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	. "gopkg.in/check.v1"
)

func (s *AddressSuite) TestDashIsCurrentNetwork(c *C) {
	for _, params := range []*chaincfg.Params{&chaincfg.MainNetParams, &chaincfg.RegressionNetParams, &chaincfg.TestNet3Params} {
		p2pkh, err := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), params)
		c.Assert(err, IsNil)
		p2sh, err := btcutil.NewAddressScriptHashFromHash(bytes.Repeat([]byte{2}, 20), params)
		c.Assert(err, IsNil)
		for _, addr := range []btcutil.Address{p2pkh, p2sh} {
			want := (params != &chaincfg.MainNetParams) == (CurrentChainNetwork == MockNet)
			c.Check(Address(addr.String()).IsCurrentNetwork(DASHChain), Equals, want,
				Commentf("address: %s, build network: %s", addr, CurrentChainNetwork))
		}
	}
	for _, addr := range []Address{
		"", "invalid", "1BoatSLRHtKNngkdXEeobR76b53LETtpyT",
		"0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
	} {
		c.Check(addr.IsCurrentNetwork(DASHChain), Equals, false, Commentf("address: %s", addr))
	}
}
