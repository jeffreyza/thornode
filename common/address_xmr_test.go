package common

import (
	"encoding/hex"
	"strings"

	eth "github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"
)

type XMRAddressSuite struct{}

var _ = Suite(&XMRAddressSuite{})

const (
	featuredNonGuaranteedSubaddressVector = "CjWdTpuDaZ69nTGxzm9YarR82YDYFECi1WaaREZTMy5yDsjaRX5bC3cbC3JpcrBPd7YYpjoWKuBMidgGaKBK5Jye2v3wfMHCy"
	featuredGuaranteedSubaddressVector    = "CjWdTpuDaZ69nTGxzm9YarR82YDYFECi1WaaREZTMy5yDsjaRX5bC3cbC3JpcrBPd7YYpjoWKuBMidgGaKBK5Jye2v4Pyz8bD"
	xmrFixtureSpendHex                    = "b862409fb5c4c4123df2abf7462b88f041ad36dd6864ce872fd5472be363c5b1"
	xmrFixtureMockAddress                 = "48cNbRvGyrb43yGQYLWKxshBnnq8nRkFfPcUwfqpihwJWiSwHb7gy6p6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGbm9KqB"
	xmrFixturePublicAddress               = "48cNbRvGyrb43yGQYLWKxshBnnq8nRkFfPcUwfqpihwJWiSwHb7gy6p6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGbm9KqB"
)

func expectedStandardXMRNetwork() ChainNetwork {
	if CurrentChainNetwork == MockNet {
		return MockNet
	}
	return CurrentChainNetwork
}

func makeTestXMRAddr(networkByte byte, spend, view [32]byte) string {
	payload := make([]byte, 0, 69)
	payload = append(payload, networkByte)
	payload = append(payload, spend[:]...)
	payload = append(payload, view[:]...)
	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)
	addr, _ := xmrBase58Encode(payload)
	return addr
}

func makeTestXMRKey(label string) [32]byte {
	return xmrDeterministicPubKey([]byte(label))
}

func makeTestXMRIntegratedAddr(spend, view [32]byte, paymentID [8]byte) string {
	payload := make([]byte, 0, 77)
	payload = append(payload, xmrIntegratedNetworkByte)
	payload = append(payload, spend[:]...)
	payload = append(payload, view[:]...)
	payload = append(payload, paymentID[:]...)
	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)
	addr, _ := xmrBase58Encode(payload)
	return addr
}

func makeTestXMRFeaturedAddr(networkByte byte, spend, view [32]byte, features uint64, paymentID *[8]byte) string {
	varint := xmrAppendVarint(nil, features)
	return makeTestXMRFeaturedAddrWithVarint(networkByte, spend, view, varint, paymentID)
}

func makeTestXMRFeaturedAddrWithVarint(networkByte byte, spend, view [32]byte, varint []byte, paymentID *[8]byte) string {
	payload := make([]byte, 0, 1+32+32+len(varint)+8+4)
	payload = append(payload, networkByte)
	payload = append(payload, spend[:]...)
	payload = append(payload, view[:]...)
	payload = append(payload, varint...)
	if paymentID != nil {
		payload = append(payload, paymentID[:]...)
	}
	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)
	addr, _ := xmrBase58Encode(payload)
	return addr
}

func (s *XMRAddressSuite) TestXMRAddressHandling(c *C) {
	primary, err := NewAddress("45v8VYPut8S9wY9fTvn1gKiGN5AMMzsCK9kLNcEpEESygy4vU3Jf3uE4Zyqv2bDLwTToH8ArrsZ3W28kmKUKJkjY8fN9anA")
	c.Assert(err, IsNil)
	c.Check(primary.IsChain(XMRChain), Equals, true)
	c.Check(primary.GetChain(), Equals, XMRChain)
	c.Check(primary.GetNetwork(XMRChain), Equals, expectedStandardXMRNetwork())

	subAddr := XMRChecksummedDummyAddress('8')
	addr8, err := NewAddress(subAddr)
	c.Assert(err, IsNil)
	c.Check(addr8.IsChain(XMRChain), Equals, true)
	c.Check(addr8.GetChain(), Equals, XMRChain)
	c.Check(addr8.GetNetwork(XMRChain), Equals, expectedStandardXMRNetwork())

	featuredAddr := XMRChecksummedDummyAddress('C')
	addrC, err := NewAddress(featuredAddr)
	c.Assert(err, IsNil)
	c.Check(addrC.IsChain(XMRChain), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(featuredAddr), Equals, true)

	_, err = NewAddress("4" + strings.Repeat("0", 94))
	c.Check(err, NotNil)
}

func (s *XMRAddressSuite) TestXMRChecksumValidation(c *C) {
	spend := makeTestXMRKey("checksum-spend")
	view := makeTestXMRKey("checksum-view")

	primaryAddr := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	c.Check(len(primaryAddr), Equals, 95)
	c.Check(IsValidXMRAddress(primaryAddr), Equals, true)

	subAddr := makeTestXMRAddr(xmrSubaddressNetworkByte, spend, view)
	c.Check(len(subAddr), Equals, 95)
	c.Check(IsValidXMRAddress(subAddr), Equals, true)

	var paymentID [8]byte
	paymentID[0] = 0xFF
	intAddr := makeTestXMRIntegratedAddr(spend, view, paymentID)
	c.Check(len(intAddr), Equals, 106)
	c.Check(IsValidXMRAddress(intAddr), Equals, true)

	featured := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	c.Check(len(featured), Equals, 97)
	c.Check(IsValidXMRAddress(featured), Equals, true)

	featuredIntegrated := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedIntegratedBit|xmrFeaturedGuaranteedBit,
		&paymentID,
	)
	c.Check(len(featuredIntegrated), Equals, 108)
	c.Check(IsValidXMRAddress(featuredIntegrated), Equals, true)

	corrupt := featured[:len(featured)-1]
	if featured[len(featured)-1] == '1' {
		corrupt += "2"
	} else {
		corrupt += "1"
	}
	c.Check(IsValidXMRAddress(corrupt), Equals, false)
}

func (s *XMRAddressSuite) TestXMRRejectsInvalidPublicKeys(c *C) {
	var zeroSpend, zeroView [32]byte
	c.Check(IsValidXMRAddress(makeTestXMRAddr(xmrPrimaryNetworkByte, zeroSpend, zeroView)), Equals, false)
	c.Check(IsValidXMRAddress(makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		zeroSpend,
		zeroView,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)), Equals, false)

	validSpend := makeTestXMRKey("valid-spend")
	c.Check(IsValidXMRAddress(makeTestXMRAddr(xmrPrimaryNetworkByte, validSpend, zeroView)), Equals, false)
	_, err := deriveXMRAddressFromSpendPubKey(zeroSpend[:], MockNet)
	c.Check(err, NotNil)
}

func (s *XMRAddressSuite) TestXMRAddressLengthRejects(c *C) {
	c.Check(IsValidXMRAddress(strings.Repeat("4", 94)), Equals, false)
	c.Check(IsValidXMRAddress(strings.Repeat("4", 96)), Equals, false)
	c.Check(IsValidXMRAddress(strings.Repeat("4", 100)), Equals, false)
	c.Check(IsValidXMRAddress(strings.Repeat("C", 107)), Equals, false)
	c.Check(IsValidXMRAddress(""), Equals, false)
}

func (s *XMRAddressSuite) TestXMRInvalidNetworkByte(c *C) {
	spend := makeTestXMRKey("invalid-network-spend")
	view := makeTestXMRKey("invalid-network-view")
	badStandard := makeTestXMRAddr(0xFF, spend, view)
	c.Check(IsValidXMRAddress(badStandard), Equals, false)

	badFeatured := makeTestXMRFeaturedAddr(0xFF, spend, view, xmrFeaturedGuaranteedBit, nil)
	c.Check(IsValidXMRAddress(badFeatured), Equals, false)
}

func (s *XMRAddressSuite) TestXMRFeaturedFeatureValidation(c *C) {
	spend := makeTestXMRKey("feature-validation-spend")
	view := makeTestXMRKey("feature-validation-view")
	var paymentID [8]byte

	unknownBit := makeTestXMRFeaturedAddr(xmrFeaturedMainnetNetworkByte, spend, view, 1<<3, nil)
	c.Check(IsValidXMRAddress(unknownBit), Equals, false)

	nonCanonical := makeTestXMRFeaturedAddrWithVarint(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		[]byte{0x81, 0x00},
		nil,
	)
	c.Check(IsValidXMRAddress(nonCanonical), Equals, false)

	truncatedVarint := makeTestXMRFeaturedAddrWithVarint(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		[]byte{0x80},
		nil,
	)
	c.Check(IsValidXMRAddress(truncatedVarint), Equals, false)

	missingIntegratedID := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedIntegratedBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	c.Check(IsValidXMRAddress(missingIntegratedID), Equals, false)

	unexpectedIntegratedID := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedGuaranteedBit,
		&paymentID,
	)
	c.Check(IsValidXMRAddress(unexpectedIntegratedID), Equals, false)
}

func (s *XMRAddressSuite) TestXMRNoChecksumAddress(c *C) {
	c.Check(IsValidXMRAddress("8"+strings.Repeat("1", 94)), Equals, false)
	c.Check(IsValidXMRAddress("4"+strings.Repeat("1", 94)), Equals, false)
	c.Check(IsValidXMRAddress("C"+strings.Repeat("1", 96)), Equals, false)
}

func (s *XMRAddressSuite) TestXMRBase58Roundtrip(c *C) {
	raw := make([]byte, 69)
	raw[0] = xmrPrimaryNetworkByte
	for i := 1; i < 65; i++ {
		raw[i] = byte(i)
	}
	checksum := eth.Keccak256(raw[:65])
	copy(raw[65:], checksum[:4])

	encoded, err := xmrBase58Encode(raw)
	c.Assert(err, IsNil)
	c.Check(len(encoded), Equals, 95)

	decoded, err := xmrBase58Decode(encoded)
	c.Assert(err, IsNil)
	c.Check(len(decoded), Equals, 69)
	for i := range raw {
		c.Check(decoded[i], Equals, raw[i])
	}
}

func (s *XMRAddressSuite) TestXMRChecksummedDummyAddress(c *C) {
	dummy4 := XMRChecksummedDummyAddress('4')
	c.Check(len(dummy4), Equals, 95)
	c.Check(IsValidXMRAddress(dummy4), Equals, true)
	c.Check(dummy4[0], Equals, byte('4'))

	dummy8 := XMRChecksummedDummyAddress('8')
	c.Check(len(dummy8), Equals, 95)
	c.Check(IsValidXMRAddress(dummy8), Equals, true)
	c.Check(dummy8[0], Equals, byte('8'))

	dummyC := XMRChecksummedDummyAddress('C')
	c.Check(len(dummyC), Equals, 97)
	c.Check(IsValidXMRAddress(dummyC), Equals, true)
	c.Check(dummyC[0], Equals, byte('C'))
	c.Check(IsXMRFeaturedGuaranteedAddress(dummyC), Equals, true)

	c.Check(dummy4 == dummy8, Equals, false)
	c.Check(dummy8 == dummyC, Equals, false)
}

func (s *XMRAddressSuite) TestXMRGetNetwork(c *C) {
	spend := makeTestXMRKey("network-spend")
	view := makeTestXMRKey("network-view")
	primaryAddr := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	addr := Address(primaryAddr)
	c.Check(addr.GetNetwork(XMRChain), Equals, expectedStandardXMRNetwork())

	subAddr := makeTestXMRAddr(xmrSubaddressNetworkByte, spend, view)
	addr = Address(subAddr)
	c.Check(addr.GetNetwork(XMRChain), Equals, expectedStandardXMRNetwork())

	featuredMock := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedGuaranteedBit,
		nil,
	)
	addr = Address(featuredMock)
	c.Check(addr.GetNetwork(XMRChain), Equals, MockNet)

	featuredPublic := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	addr = Address(featuredPublic)
	c.Check(addr.GetNetwork(XMRChain) == MockNet, Equals, false)
}

func (s *XMRAddressSuite) TestXMRFeaturedNetworkByteGating(c *C) {
	spend := makeTestXMRKey("network-byte-spend")
	view := makeTestXMRKey("network-byte-view")

	featuredStagenet := makeTestXMRFeaturedAddr(
		xmrFeaturedStagenetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	addr := Address(featuredStagenet)
	c.Check(IsValidXMRAddress(featuredStagenet), Equals, true)
	c.Check(addr.GetNetwork(XMRChain), Equals, MockNet)
	c.Check(XMRChain.IsValidAddress(addr), Equals, false)

	featuredTestnet := makeTestXMRFeaturedAddr(
		xmrFeaturedTestnetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	addr = Address(featuredTestnet)
	c.Check(IsValidXMRAddress(featuredTestnet), Equals, true)
	c.Check(addr.GetNetwork(XMRChain), Equals, MockNet)
	c.Check(XMRChain.IsValidAddress(addr), Equals, false)

	featuredPublic := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	addr = Address(featuredPublic)
	c.Check(addr.GetNetwork(XMRChain) == MockNet, Equals, false)
	c.Check(XMRChain.IsValidAddress(addr), Equals, CurrentChainNetwork != MockNet)

	featuredMock := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedGuaranteedBit,
		nil,
	)
	addr = Address(featuredMock)
	c.Check(addr.GetNetwork(XMRChain), Equals, MockNet)
	c.Check(XMRChain.IsValidAddress(addr), Equals, CurrentChainNetwork == MockNet)
}

func (s *XMRAddressSuite) TestXMRIsChainExclusive(c *C) {
	spend := makeTestXMRKey("chain-exclusive-spend")
	view := makeTestXMRKey("chain-exclusive-view")
	primaryAddr := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	addr, err := NewAddress(primaryAddr)
	c.Assert(err, IsNil)
	c.Check(addr.IsChain(XMRChain), Equals, true)
	c.Check(addr.IsChain(BTCChain), Equals, false)
	c.Check(addr.IsChain(ETHChain), Equals, false)
	c.Check(addr.IsChain(THORChain), Equals, false)
	c.Check(addr.IsChain(LTCChain), Equals, false)
	c.Check(addr.IsChain(BCHChain), Equals, false)
	c.Check(addr.IsChain(DOGEChain), Equals, false)
	c.Check(addr.IsChain(XRPChain), Equals, false)
	c.Check(addr.IsChain(GAIAChain), Equals, false)
	c.Check(addr.IsChain(SOLChain), Equals, false)
}

func (s *XMRAddressSuite) TestXMRMappedAccAddress(c *C) {
	spend := makeTestXMRKey("mapped-spend")
	view := makeTestXMRKey("mapped-view")
	primaryAddr := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	addr := Address(primaryAddr)
	accAddr, err := addr.MappedAccAddress()
	c.Assert(err, IsNil)
	c.Check(len(accAddr), Equals, 20)

	accAddr2, err := addr.MappedAccAddress()
	c.Assert(err, IsNil)
	c.Check(accAddr.Equals(accAddr2), Equals, true)

	spend = makeTestXMRKey("mapped-other-spend")
	otherAddr := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	accAddr3, err := Address(otherAddr).MappedAccAddress()
	c.Assert(err, IsNil)
	c.Check(accAddr.Equals(accAddr3), Equals, false)
}

func (s *XMRAddressSuite) TestXMRSeededDummyAddress(c *C) {
	seed1 := []byte("txid-aaaa")
	seed2 := []byte("txid-bbbb")

	addr1 := XMRSeededDummyAddress('C', seed1)
	addr2 := XMRSeededDummyAddress('C', seed2)
	c.Check(IsValidXMRAddress(addr1), Equals, true)
	c.Check(IsValidXMRAddress(addr2), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(addr1), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(addr2), Equals, true)

	c.Check(addr1 == addr2, Equals, false)
	c.Check(XMRSeededDummyAddress('C', seed1), Equals, addr1)

	addr4 := XMRSeededDummyAddress('4', seed1)
	c.Check(IsValidXMRAddress(addr4), Equals, true)
	c.Check(addr4[0], Equals, byte('4'))
	c.Check(addr1 == addr4, Equals, false)
}

func (s *XMRAddressSuite) TestXMRFeaturedGuaranteedHelper(c *C) {
	c.Check(IsXMRFeaturedGuaranteedAddress(featuredGuaranteedSubaddressVector), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(featuredNonGuaranteedSubaddressVector), Equals, false)
	c.Check(IsXMRFeaturedGuaranteedAddress(XMRChecksummedDummyAddress('8')), Equals, false)
}

func (s *XMRAddressSuite) TestXMRSupportedExternalRecipientPolicy(c *C) {
	spend := makeTestXMRKey("recipient-policy-spend")
	view := makeTestXMRKey("recipient-policy-view")
	var paymentID [8]byte
	paymentID[0] = 0xA5

	primary := makeTestXMRAddr(xmrPrimaryNetworkByte, spend, view)
	subaddress := makeTestXMRAddr(xmrSubaddressNetworkByte, spend, view)
	integrated := makeTestXMRIntegratedAddr(spend, view, paymentID)
	featured := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedSubaddressBit|xmrFeaturedGuaranteedBit,
		nil,
	)
	featuredIntegrated := makeTestXMRFeaturedAddr(
		xmrFeaturedMainnetNetworkByte,
		spend,
		view,
		xmrFeaturedIntegratedBit|xmrFeaturedGuaranteedBit,
		&paymentID,
	)

	c.Check(IsValidXMRAddress(primary), Equals, true)
	c.Check(IsValidXMRAddress(subaddress), Equals, true)
	c.Check(IsValidXMRAddress(integrated), Equals, true)
	c.Check(IsValidXMRAddress(featured), Equals, true)
	c.Check(IsValidXMRAddress(featuredIntegrated), Equals, true)
	c.Check(IsSupportedXMRAddress(primary), Equals, true)
	c.Check(IsSupportedXMRAddress(subaddress), Equals, true)
	c.Check(IsSupportedXMRAddress(integrated), Equals, false)
	c.Check(IsSupportedXMRAddress(featured), Equals, false)
	c.Check(IsSupportedXMRAddress(featuredIntegrated), Equals, false)
}

func (s *XMRAddressSuite) TestXMRCrossStackDeterminismFixture(c *C) {
	spend, err := hex.DecodeString(xmrFixtureSpendHex)
	c.Assert(err, IsNil)
	c.Assert(len(spend), Equals, 32)

	mock, err := deriveXMRAddressFromSpendPubKey(spend, MockNet)
	c.Assert(err, IsNil)
	c.Check(mock, Equals, xmrFixtureMockAddress)

	public, err := deriveXMRAddressFromSpendPubKey(spend, MainNet)
	c.Assert(err, IsNil)
	c.Check(public, Equals, xmrFixturePublicAddress)
}

func (s *XMRAddressSuite) TestXMRUsesWalletCompatibleStandardAddress(c *C) {
	spend, err := hex.DecodeString(xmrFixtureSpendHex)
	c.Assert(err, IsNil)
	c.Assert(len(spend), Equals, 32)

	mock, err := deriveXMRAddressFromSpendPubKey(spend, MockNet)
	c.Assert(err, IsNil)
	c.Check(len(mock), Equals, 95)
	c.Check(strings.HasPrefix(mock, "4"), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(mock), Equals, false)

	public, err := deriveXMRAddressFromSpendPubKey(spend, MainNet)
	c.Assert(err, IsNil)
	c.Check(len(public), Equals, 95)
	c.Check(strings.HasPrefix(public, "4"), Equals, true)
	c.Check(IsXMRFeaturedGuaranteedAddress(public), Equals, false)
	c.Check(public, Equals, mock)
}

func (s *XMRAddressSuite) TestXMRUnknownSenderAddress(c *C) {
	addr := XMRUnknownSenderAddress("tx-hash-a", "8abcd")
	parsed, err := NewAddress(addr)
	c.Assert(err, IsNil)
	c.Check(IsXMRUnknownSenderAddress(parsed), Equals, true)
	c.Check(parsed.IsChain(XMRChain), Equals, false)

	c.Check(XMRUnknownSenderAddress("tx-hash-a", "8abcd"), Equals, addr)
	c.Check(XMRUnknownSenderAddress("tx-hash-b", "8abcd") == addr, Equals, false)

	info := ResolveObservedSender(XMRChain, parsed)
	c.Check(info.Known, Equals, false)
	c.Check(info.Address, Equals, NoAddress)
	c.Check(info.RefundAddressRequired, Equals, true)
}

func (s *XMRAddressSuite) TestXMRIntegratedAddressNetwork(c *C) {
	spend := makeTestXMRKey("integrated-spend")
	view := makeTestXMRKey("integrated-view")
	var paymentID [8]byte
	paymentID[0] = 0xDE
	intAddr := makeTestXMRIntegratedAddr(spend, view, paymentID)
	c.Check(len(intAddr), Equals, 106)
	c.Check(intAddr[0], Equals, byte('4'))
	addr := Address(intAddr)
	c.Check(addr.GetNetwork(XMRChain), Equals, expectedStandardXMRNetwork())
}
