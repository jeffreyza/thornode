package common

import (
	"strings"

	. "gopkg.in/check.v1"
)

type ZECAddressSuite struct{}

var _ = Suite(&ZECAddressSuite{})

func (s *ZECAddressSuite) TestCanonical(c *C) {
	for _, tc := range []struct {
		input   Address
		want    Address
		mocknet bool
	}{
		{"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU", false},
		{"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", "tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7", true},
	} {
		comment := Commentf("address: %s", tc.input)
		want := tc.want
		if tc.mocknet != (CurrentChainNetwork == MockNet) {
			want = tc.input
		}
		got := tc.input.Canonical()
		c.Check(got, Equals, want, comment)
		c.Check(got.Canonical(), Equals, got, comment)
	}
}

func (s *ZECAddressSuite) TestCanonicalPreservesInvalidTex(c *C) {
	for _, addr := range []Address{
		"tex", "textest", "tex1invalid", "textest1invalid",
		"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7q",
		"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfayq",
	} {
		c.Check(addr.Canonical(), Equals, addr, Commentf("address: %s", addr))
	}
}

func (s *ZECAddressSuite) TestConvertToZECTransparentAddress(c *C) {
	for _, tc := range []struct {
		input   Address
		want    Address
		mocknet bool
	}{
		{"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU", false},
		{"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", "tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7", true},
	} {
		comment := Commentf("address: %s", tc.input)
		got, err := ConvertToZECTransparentAddress(tc.input)
		if tc.mocknet != (CurrentChainNetwork == MockNet) {
			c.Check(err, NotNil, comment)
			c.Check(got, Equals, NoAddress, comment)
			continue
		}
		c.Assert(err, IsNil, comment)
		c.Check(got, Equals, tc.want, comment)
	}
}

func (s *ZECAddressSuite) TestIsCurrentNetwork(c *C) {
	for _, tc := range []struct {
		addr    Address
		mocknet bool
	}{
		{"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", false},
		{"t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU", false},
		{"t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5", false},
		{"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", true},
		{"tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7", true},
		{"t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb", true},
	} {
		comment := Commentf("address: %s, network: %s", tc.addr, CurrentChainNetwork)
		want := tc.mocknet == (CurrentChainNetwork == MockNet)
		c.Check(tc.addr.IsCurrentNetwork(ZECChain), Equals, want, comment)
		// The check must agree with txout normalization, which fails on a
		// wrong-network TEX address.
		if strings.HasPrefix(tc.addr.String(), "tex") {
			_, err := ConvertToZECTransparentAddress(tc.addr)
			c.Check(err == nil, Equals, want, comment)
		}
		// GetNetwork with SoftEquals treats stagenet and mocknet as compatible,
		// so it cannot reject testnet addresses on stagenet.
		if CurrentChainNetwork == StageNet && tc.mocknet {
			c.Check(CurrentChainNetwork.SoftEquals(tc.addr.GetNetwork(ZECChain)), Equals, true, comment)
		}
	}
	for _, addr := range []Address{"", "t1", "tex1invalid", "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a"} {
		c.Check(addr.IsCurrentNetwork(ZECChain), Equals, false, Commentf("address: %s", addr))
	}
}

func (s *ZECAddressSuite) TestConvertToZECTransparentAddressRejectsNonTex(c *C) {
	for _, addr := range []Address{
		"", "t", "t1",
		"t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU",
		"t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5",
		"tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7",
		"t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb",
		"0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
	} {
		got, err := ConvertToZECTransparentAddress(addr)
		c.Check(err, ErrorMatches, ".*is not a TEX address", Commentf("address: %s", addr))
		c.Check(got, Equals, NoAddress)
	}
}

func (s *ZECAddressSuite) TestConvertToZECTransparentAddressRejectsInvalid(c *C) {
	for _, addr := range []Address{
		"tex", "textest", "tex1invalid",
		"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7q",     // bad checksum
		"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfayq", // bad checksum
	} {
		got, err := ConvertToZECTransparentAddress(addr)
		c.Check(err, NotNil, Commentf("address: %s", addr))
		c.Check(got, Equals, NoAddress)
	}
}

func (s *ZECAddressSuite) TestZECAddressValidation(c *C) {
	// Sapling address (z), not supported
	_, err := NewAddress("zs1z7rejlpsa98s2rrrfkwmaxu53e4ue0ulcrw0h4x5g8jl04tak0d3mm47vdtahatqrlkngh9sly")
	c.Assert(err, NotNil)

	// Unified address (u), not supported
	_, err = NewAddress("u1yvwppp7ann6n3pgkysdu0spvr50w4jf4jwgme3c8x8fp4av59rupgvdd3fddc3f2cwrk3ghs5lxt87ggj8cvjuzcrf4jkejwlu9pc83gk2vtx03ucqcc3ed0furcuypqs6d6swu3nws")
	c.Assert(err, NotNil)

	// Wrong checksum
	_, err = NewAddress("t2DcaUhoxrJDCj1xJmzWRdkHofRUM1erfih")
	c.Assert(err, NotNil)
}

func (s *ZECAddressSuite) TestZECNotChain(c *C) {
	// Test that non-ZEC addresses don't match ZECChain
	ethAddr, _ := NewAddress("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	c.Assert(ethAddr.IsChain(ZECChain), Equals, false)

	btcAddr, _ := NewAddress("1MirQ9bwyQcGVJPwKUgapu5ouK2E2Ey4gX")
	c.Assert(btcAddr.IsChain(ZECChain), Equals, false)

	thorAddr, _ := NewAddress("thor1kljxxccrheghavaw97u78le6yy3sdj7h696nl4")
	c.Assert(thorAddr.IsChain(ZECChain), Equals, false)
}

func (s *ZECAddressSuite) TestEquivalentAddressesZecTex(c *C) {
	p2pkhAddr, err := getTestPubKey(c).GetAddress(ZECChain)
	c.Assert(err, IsNil)
	otherAddr, err := getTestPubKey2(c).GetAddress(ZECChain)
	c.Assert(err, IsNil)

	texAddr, err := p2pkhAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(p2pkhAddr.Equals(texAddr), Equals, false)

	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, texAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, texAddr, p2pkhAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, p2pkhAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, texAddr, texAddr), Equals, true)

	// Distinct destinations stay distinct in either spelling.
	otherTexAddr, err := otherAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, otherAddr), Equals, false)
	c.Check(EquivalentAddresses(ZECChain, texAddr, otherTexAddr), Equals, false)
	c.Check(EquivalentAddresses(ZECChain, texAddr, otherAddr), Equals, false)
}

func (s *ZECAddressSuite) TestEquivalentAddressesZecWithoutTexForm(c *C) {
	p2sh := "t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5"
	if CurrentChainNetwork == MockNet {
		p2sh = "t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb"
	}
	p2shAddr, err := NewAddress(p2sh)
	c.Assert(err, IsNil)
	_, err = p2shAddr.ToTexAddress()
	c.Assert(err, NotNil)

	c.Check(EquivalentAddresses(ZECChain, p2shAddr, p2shAddr), Equals, true)

	// Unparsable input reaches this comparison from observed transactions, so
	// it must be handled rather than trusted.
	for _, malformed := range []Address{"", "t", "tm", "not-an-address", "t1"} {
		c.Check(EquivalentAddresses(ZECChain, malformed, malformed), Equals, true)
		c.Check(EquivalentAddresses(ZECChain, malformed, p2shAddr), Equals, false)
		c.Check(EquivalentAddresses(ZECChain, p2shAddr, malformed), Equals, false)
	}

	// A P2PKH destination is not equivalent to a P2SH one that shares no bytes.
	p2pkhAddr, err := getTestPubKey(c).GetAddress(ZECChain)
	c.Assert(err, IsNil)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, p2shAddr), Equals, false)
}
