package common

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type ObservedTxSuite struct{}

var _ = Suite(&ObservedTxSuite{})

const (
	testSpendTxHash  = "abcd000000000000000000000000000000000000000000000000000000000001"
	testSpendTxHash2 = "abcd000000000000000000000000000000000000000000000000000000000002"
	testRefTxHashA   = "ef01000000000000000000000000000000000000000000000000000000000001"
	testRefTxHashB   = "ef01000000000000000000000000000000000000000000000000000000000002"
	testKeyImageA    = "ki01000000000000000000000000000000000000000000000000000000000001"
	testKeyImageB    = "ki02000000000000000000000000000000000000000000000000000000000001"
	testXMRTxKey     = "aa01000000000000000000000000000000000000000000000000000000000001"
	testXMRTxKey2    = "aa02000000000000000000000000000000000000000000000000000000000001"
	testPubKey       = PubKey("thorpub1addwnpepqfshsq2y6ejy2ysxmq4gj8n8mzuzyulk9wh4n946jv5w2vpwdn2yuyp6sp4")
)

func makeRef(txHash string, idx uint32, keyImage, spendTxHash string) OutputRef {
	return OutputRef{
		TxHash:      txHash,
		OutputIndex: idx,
		KeyImage:    keyImage,
		SpendTxHash: spendTxHash,
	}
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_Empty(c *C) {
	c.Assert(ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), nil), IsNil)
	c.Assert(ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), []OutputRef{}), IsNil)
	c.Assert(ValidateSpentOutputRefs(BTCChain, TxID(testSpendTxHash), nil), IsNil)
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_NonXMRChain(c *C) {
	refs := []OutputRef{makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)}
	err := ValidateSpentOutputRefs(BTCChain, TxID(testSpendTxHash), refs)
	c.Assert(err, ErrorMatches, "spent output refs are only valid for XMR observed txs")
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_OverCap(c *C) {
	refs := make([]OutputRef, MaxXMRSpentOutputRefs+1)
	for i := range refs {
		refs[i] = makeRef(testRefTxHashA, uint32(i), testKeyImageA, testSpendTxHash)
	}
	err := ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), refs)
	c.Assert(err, ErrorMatches, "spent output refs exceeds max 32")
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_EmptyFields(c *C) {
	tests := []struct {
		ref    OutputRef
		errSub string
	}{
		{makeRef("", 0, testKeyImageA, testSpendTxHash), "tx hash cannot be empty"},
		{makeRef("   ", 0, testKeyImageA, testSpendTxHash), "tx hash cannot be empty"},
		{makeRef(testRefTxHashA, 0, "", testSpendTxHash), "key image cannot be empty"},
		{makeRef(testRefTxHashA, 0, testKeyImageA, ""), "spend tx hash cannot be empty"},
	}
	for _, tc := range tests {
		err := ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), []OutputRef{tc.ref})
		c.Assert(err, NotNil)
		c.Assert(strings.Contains(err.Error(), tc.errSub), Equals, true,
			Commentf("expected %q in %v", tc.errSub, err))
	}
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_MismatchedSpendTxHash(c *C) {
	refs := []OutputRef{makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash2)}
	err := ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), refs)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "does not match observed tx"), Equals, true)
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_DuplicateSameCase(c *C) {
	ref := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	err := ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), []OutputRef{ref, ref})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "duplicate spent output ref"), Equals, true)
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_DuplicateCaseInsensitive(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(strings.ToUpper(testRefTxHashA), 0, strings.ToUpper(testKeyImageA), testSpendTxHash)
	err := ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), []OutputRef{a, b})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "duplicate spent output ref"), Equals, true)
}

func (s ObservedTxSuite) TestValidateSpentOutputRefs_HappyPath(c *C) {
	refs := []OutputRef{
		makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash),
		makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash),
	}
	c.Assert(ValidateSpentOutputRefs(XMRChain, TxID(testSpendTxHash), refs), IsNil)
}

func (s ObservedTxSuite) TestValidateXMRTxKey(c *C) {
	c.Assert(ValidateXMRTxKey(XMRChain, ""), IsNil)
	c.Assert(ValidateXMRTxKey(XMRChain, testXMRTxKey), IsNil)
	c.Assert(ValidateXMRTxKey(XMRChain, strings.ToUpper(testXMRTxKey)), IsNil)

	err := ValidateXMRTxKey(BTCChain, testXMRTxKey)
	c.Assert(err, ErrorMatches, "xmr tx key is only valid for XMR observed txs")

	err = ValidateXMRTxKey(XMRChain, "abc")
	c.Assert(err, ErrorMatches, "xmr tx key must be 64 hex characters")

	err = ValidateXMRTxKey(XMRChain, strings.Repeat("z", 64))
	c.Assert(err, ErrorMatches, "xmr tx key must be hex")
}

func (s ObservedTxSuite) TestGetSignablePayloadXMRTxKeyMetadata(c *C) {
	txKeyUpper := strings.ToUpper(testXMRTxKey)
	txKeyLower := strings.ToLower(testXMRTxKey)

	withUpperKey := makeXMRObservedTx(c, nil)
	withUpperKey.XmrTxKey = txKeyUpper
	upperPayload, err := withUpperKey.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	withLowerKey := makeXMRObservedTx(c, nil)
	withLowerKey.XmrTxKey = txKeyLower
	lowerPayload, err := withLowerKey.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	emptyTx := makeXMRObservedTx(c, nil)
	emptyPayload, err := emptyTx.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	c.Assert(string(upperPayload), Equals, string(lowerPayload))
	c.Assert(string(lowerPayload) == string(emptyPayload), Equals, false)
}

func (s ObservedTxSuite) TestGetSignablePayloadXMRMetadataDeterministicAcrossRefOrder(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)

	tx1 := makeXMRObservedTx(c, []OutputRef{a, b})
	tx1.XmrTxKey = testXMRTxKey
	payload1, err := tx1.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	tx2 := makeXMRObservedTx(c, []OutputRef{b, a})
	tx2.XmrTxKey = strings.ToUpper(testXMRTxKey)
	payload2, err := tx2.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	txNoRefs := makeXMRObservedTx(c, nil)
	txNoRefs.XmrTxKey = testXMRTxKey
	payloadNoRefs, err := txNoRefs.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	c.Assert(string(payload1), Equals, string(payload2))
	c.Assert(string(payload1) == string(payloadNoRefs), Equals, false)
}

// helpers for consensus-equality tests

func makeXMRObservedTx(c *C, refs []OutputRef) ObservedTx {
	xmrAddr := XMRSeededDummyAddress(0x12, []byte("from-seed"))
	to := XMRSeededDummyAddress(0x12, []byte("to-seed"))
	id, err := NewTxID(testSpendTxHash)
	c.Assert(err, IsNil)
	tx := NewTx(
		id,
		Address(xmrAddr),
		Address(to),
		Coins{NewCoin(XMRAsset, cosmos.NewUint(One))},
		Gas{NewCoin(XMRAsset, cosmos.NewUint(1000))},
		"=:THOR.RUNE",
	)
	tx.Chain = XMRChain
	otx := NewObservedTx(tx, 100, testPubKey, 100)
	otx.SpentOutputRefs = refs
	return otx
}

func (s ObservedTxSuite) TestEqualsConsensus_SameRefsDifferentOrder(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)
	tx1 := makeXMRObservedTx(c, []OutputRef{a, b})
	tx2 := makeXMRObservedTx(c, []OutputRef{b, a})
	c.Assert(tx1.EqualsConsensus(tx2), Equals, true)
	c.Assert(tx1.EqualsConsensusIgnoreGas(tx2), Equals, true)
	c.Assert(tx1.HasEquivalentSpentOutputRefs(tx2), Equals, true)
}

func (s ObservedTxSuite) TestEqualsConsensus_XMRAddressCaseVariantsDoNotCoalesce(c *C) {
	exact := makeXMRObservedTx(c, nil)
	fromVariant := makeXMRObservedTx(c, nil)
	fromVariant.Tx.FromAddress = Address(strings.ToUpper(fromVariant.Tx.FromAddress.String()))
	toVariant := makeXMRObservedTx(c, nil)
	toVariant.Tx.ToAddress = Address(strings.ToUpper(toVariant.Tx.ToAddress.String()))

	c.Assert(exact.EqualsConsensus(fromVariant), Equals, false)
	c.Assert(exact.EqualsConsensusIgnoreGas(fromVariant), Equals, false)
	c.Assert(exact.EqualsConsensus(toVariant), Equals, false)
	c.Assert(exact.EqualsConsensusIgnoreGas(toVariant), Equals, false)
}

func (s ObservedTxSuite) TestEqualsConsensus_OneRefDiffers(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)
	other := makeRef(testRefTxHashB, 1, "ki03000000000000000000000000000000000000000000000000000000000001", testSpendTxHash)
	tx1 := makeXMRObservedTx(c, []OutputRef{a, b})
	tx2 := makeXMRObservedTx(c, []OutputRef{a, other})
	c.Assert(tx1.EqualsConsensus(tx2), Equals, false)
	c.Assert(tx1.HasEquivalentSpentOutputRefs(tx2), Equals, false)
}

func (s ObservedTxSuite) TestEqualsConsensus_MissingRefsDoesNotSplitConsensus(c *C) {
	ref := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	txWithRefs := makeXMRObservedTx(c, []OutputRef{ref})
	txWithoutRefs := makeXMRObservedTx(c, nil)
	txWithRefs.XmrTxKey = testXMRTxKey
	txWithoutRefs.XmrTxKey = testXMRTxKey

	c.Assert(txWithRefs.Equals(txWithoutRefs), Equals, false)
	c.Assert(txWithRefs.EqualsConsensus(txWithoutRefs), Equals, true)
	c.Assert(txWithRefs.EqualsConsensusIgnoreGas(txWithoutRefs), Equals, true)
	c.Assert(txWithRefs.HasEquivalentSpentOutputRefs(txWithoutRefs), Equals, true)
}

func (s ObservedTxSuite) TestEqualsConsensus_OneXMRTxKeyDiffers(c *C) {
	tx1 := makeXMRObservedTx(c, nil)
	tx2 := makeXMRObservedTx(c, nil)
	tx1.XmrTxKey = testXMRTxKey
	tx2.XmrTxKey = testXMRTxKey2
	c.Assert(tx1.EqualsConsensus(tx2), Equals, false)
	c.Assert(tx1.EqualsConsensusIgnoreGas(tx2), Equals, false)
	c.Assert(tx1.HasEquivalentXMRTxKey(tx2), Equals, false)
}

func (s ObservedTxSuite) TestEqualsConsensus_BothNonXMREmpty(c *C) {
	id, err := NewTxID(testSpendTxHash)
	c.Assert(err, IsNil)
	tx := NewTx(
		id,
		Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a"),
		Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635b"),
		Coins{NewCoin(ETHAsset, cosmos.NewUint(5*One))},
		Gas{NewCoin(ETHAsset, cosmos.NewUint(10000))},
		"=:THOR.RUNE",
	)
	pk := testPubKey
	a := NewObservedTx(tx, 100, pk, 100)
	b := NewObservedTx(tx, 100, pk, 100)
	c.Assert(a.EqualsConsensus(b), Equals, true)
	c.Assert(a.HasEquivalentSpentOutputRefs(b), Equals, true)
}

func (s ObservedTxSuite) TestAttestationUniqueHash_NonXMREqualsBase(c *C) {
	id, err := NewTxID(testSpendTxHash)
	c.Assert(err, IsNil)
	tx := NewTx(
		id,
		Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a"),
		Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635b"),
		Coins{NewCoin(ETHAsset, cosmos.NewUint(5*One))},
		Gas{NewCoin(ETHAsset, cosmos.NewUint(10000))},
		"=:THOR.RUNE",
	)
	otx := NewObservedTx(tx, 100, testPubKey, 100)
	c.Assert(otx.AttestationUniqueHash(), Equals, otx.Tx.Hash(otx.BlockHeight))
}

func (s ObservedTxSuite) TestAttestationUniqueHash_XMREmptyRefsEqualsBase(c *C) {
	otx := makeXMRObservedTx(c, nil)
	c.Assert(otx.AttestationUniqueHash(), Equals, otx.Tx.Hash(otx.BlockHeight))
}

func (s ObservedTxSuite) TestAttestationUniqueHash_XMRWithRefsDiffersFromBase(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	otx := makeXMRObservedTx(c, []OutputRef{a})
	c.Assert(otx.AttestationUniqueHash() != otx.Tx.Hash(otx.BlockHeight), Equals, true)
}

func (s ObservedTxSuite) TestAttestationUniqueHash_XMRWithTxKeyDiffersFromBase(c *C) {
	otx := makeXMRObservedTx(c, nil)
	otx.XmrTxKey = testXMRTxKey
	c.Assert(otx.AttestationUniqueHash() != otx.Tx.Hash(otx.BlockHeight), Equals, true)
}

func (s ObservedTxSuite) TestAttestationUniqueHash_DeterministicAcrossOrder(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)
	tx1 := makeXMRObservedTx(c, []OutputRef{a, b})
	tx2 := makeXMRObservedTx(c, []OutputRef{b, a})
	c.Assert(tx1.AttestationUniqueHash(), Equals, tx2.AttestationUniqueHash())
}

func (s ObservedTxSuite) TestAttestationUniqueHash_DiffersOnDifferentRefs(c *C) {
	a := makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)
	b := makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)
	tx1 := makeXMRObservedTx(c, []OutputRef{a})
	tx2 := makeXMRObservedTx(c, []OutputRef{b})
	c.Assert(tx1.AttestationUniqueHash() != tx2.AttestationUniqueHash(), Equals, true)
}

func (s ObservedTxSuite) TestSignerAddressCache(c *C) {
	addr := cosmos.AccAddress([]byte("perf-signer-cache-01"))
	signer := addr.String()

	// cached and uncached decodes agree
	first, err := signerAddress(signer)
	c.Assert(err, IsNil)
	second, err := signerAddress(signer)
	c.Assert(err, IsNil)
	c.Check(first.Equals(addr), Equals, true)
	c.Check(second.Equals(addr), Equals, true)

	// callers get their own copy
	second[0] ^= 0xff
	third, err := signerAddress(signer)
	c.Assert(err, IsNil)
	c.Check(third.Equals(addr), Equals, true)

	// failures are not cached and still fail
	_, err = signerAddress("bogus")
	c.Check(err, NotNil)
	_, err = signerAddress("bogus")
	c.Check(err, NotNil)

	tx := ObservedTx{Signers: []string{signer, "bogus", signer}}
	c.Check(tx.GetSigners(), HasLen, 2)
	c.Check(tx.HasSigned(addr), Equals, true)

	// SignerAddressString agrees with signerAddress, memoized or not, and is
	// not affected by callers modifying their copies
	fresh := cosmos.AccAddress([]byte("perf-signer-cache-02")).String()
	for _, s := range []string{fresh, fresh, signer, strings.ToUpper(signer)} {
		got, ok := SignerAddressString(s)
		c.Assert(ok, Equals, true)
		want, err := signerAddress(s)
		c.Assert(err, IsNil)
		c.Check(got, Equals, string(want))
		want[0] ^= 0xff
		again, _ := SignerAddressString(s)
		c.Check(again, Equals, got)
	}
	for _, s := range []string{"bogus", "", strings.ToUpper(signer[:1]) + signer[1:]} {
		got, ok := SignerAddressString(s)
		c.Check(ok, Equals, false)
		c.Check(got, Equals, "")
		_, err := signerAddress(s)
		c.Check(err, NotNil)
	}
}

func (s ObservedTxSuite) TestHasSignedMatchesDecodedSigners(c *C) {
	addr := cosmos.AccAddress([]byte("perf-has-signed-0001"))
	other := cosmos.AccAddress([]byte("perf-has-signed-0002"))
	upper := strings.ToUpper(addr.String()) // bech32 decodes all-uppercase too

	for _, signers := range [][]string{
		{addr.String()},
		{upper},
		{"bogus", other.String(), upper},
		{other.String()},
		{"bogus", ""},
		nil,
	} {
		tx := ObservedTx{Signers: signers}
		want := false
		for _, signer := range tx.GetSigners() {
			if signer.Equals(addr) {
				want = true
			}
		}
		c.Check(tx.HasSigned(addr), Equals, want, Commentf("signers %v", signers))
	}
	c.Check((&ObservedTx{Signers: []string{upper}}).HasSigned(addr), Equals, true)
	c.Check((&ObservedTx{Signers: []string{""}}).HasSigned(cosmos.AccAddress{}), Equals, false)
}
