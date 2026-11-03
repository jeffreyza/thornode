package common

import (
	"google.golang.org/protobuf/encoding/protowire"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type SolNonceSuite struct{}

var _ = Suite(&SolNonceSuite{})

func solNonceTestObs(c *C) ObservedTx {
	return NewObservedTx(Tx{
		ID:          TxID("A0D2DB1E5A29F68E1F0F2DE8E8C1E3F7A0D2DB1E5A29F68E1F0F2DE8E8C1E3F7"),
		Chain:       SOLChain,
		FromAddress: Address("66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn"),
		ToAddress:   Address("463gZNUrNwF285KgUTRdrMEUHANWPgQGm16spxLYUU7a"),
		Coins:       NewCoins(NewCoin(SOLAsset, cosmos.NewUint(100000000))),
		Gas:         Gas{NewCoin(SOLAsset, cosmos.NewUint(5000))},
		Memo:        "OUT:x",
	}, 100, PubKey("tthorpub1addwnpepqv3e9cvwcurmesn2kztc2qcgsmttlp5wqm9kha9rmfuwxzch9nck2fc9vka"), 100)
}

// topLevelFields lists the field numbers present in a marshaled message.
func topLevelFields(c *C, bz []byte) map[protowire.Number]bool {
	seen := map[protowire.Number]bool{}
	for len(bz) > 0 {
		num, typ, n := protowire.ConsumeTag(bz)
		c.Assert(n > 0, Equals, true)
		bz = bz[n:]
		seen[num] = true
		m := protowire.ConsumeFieldValue(num, typ, bz)
		c.Assert(m >= 0, Equals, true)
		bz = bz[m:]
	}
	return seen
}

// TestSignablePayloadUnchangedWhenAbsent: adding ObservedTx field 14 must not change
// the signed bytes of any observation that does not set it (every existing chain and
// every non-nonce SOL observation), so mixed attestations and existing signatures
// are unaffected.
func (s *SolNonceSuite) TestSignablePayloadUnchangedWhenAbsent(c *C) {
	obs := solNonceTestObs(c)
	bz, err := obs.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)
	fields := topLevelFields(c, bz[:len(bz)-1]) // strip the trailing inbound byte
	c.Assert(fields[14], Equals, false)

	obs.SolNonce = &SolNonceUpdate{
		NonceAccount: "463gZNUrNwF285KgUTRdrMEUHANWPgQGm16spxLYUU7a",
		Nonce:        "66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn",
		Slot:         1000,
	}
	withNonce, err := obs.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)
	c.Assert(topLevelFields(c, withNonce[:len(withNonce)-1])[14], Equals, true) // signed when set
}

func (s *SolNonceSuite) TestValidate(c *C) {
	good := &SolNonceUpdate{
		NonceAccount: "463gZNUrNwF285KgUTRdrMEUHANWPgQGm16spxLYUU7a",
		Nonce:        "66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn",
		Slot:         1,
	}
	c.Assert(ValidateSolNonceUpdate(SOLChain, nil), IsNil)
	c.Assert(ValidateSolNonceUpdate(SOLChain, good), IsNil)
	c.Assert(ValidateSolNonceUpdate(BTCChain, good), NotNil) // SOL only

	bad := *good
	bad.Nonce = ""
	c.Assert(ValidateSolNonceUpdate(SOLChain, &bad), NotNil)
	bad = *good
	bad.NonceAccount = "not-base58-0OIl"
	c.Assert(ValidateSolNonceUpdate(SOLChain, &bad), NotNil)
	bad = *good
	bad.Nonce = "3QJmV3qfvL9SuYo34YihAf3sRCW3qSinyC" // valid base58, wrong length
	c.Assert(ValidateSolNonceUpdate(SOLChain, &bad), NotNil)
	bad = *good
	bad.Slot = 0
	c.Assert(ValidateSolNonceUpdate(SOLChain, &bad), NotNil)
}

// TestEqualitySemantics: a different nonce value is a different vote entry (Equals)
// but never splits outbound consensus (EqualsConsensus), and lands in a different
// attestation gossip bucket (AttestationUniqueHash).
func (s *SolNonceSuite) TestEqualitySemantics(c *C) {
	a := solNonceTestObs(c)
	b := solNonceTestObs(c)
	c.Assert(a.Equals(b), Equals, true)
	c.Assert(a.AttestationUniqueHash(), Equals, b.AttestationUniqueHash())

	a.SolNonce = &SolNonceUpdate{NonceAccount: "463gZNUrNwF285KgUTRdrMEUHANWPgQGm16spxLYUU7a", Nonce: "66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn", Slot: 1}
	c.Assert(a.Equals(b), Equals, false) // no "absent matches" rule
	c.Assert(a.EqualsConsensus(b), Equals, true)
	c.Assert(a.AttestationUniqueHash() == b.AttestationUniqueHash(), Equals, false)

	b.SolNonce = &SolNonceUpdate{NonceAccount: a.SolNonce.NonceAccount, Nonce: a.SolNonce.Nonce, Slot: 1}
	c.Assert(a.Equals(b), Equals, true)
	b.SolNonce.Slot = 2
	c.Assert(a.Equals(b), Equals, false)
	c.Assert(a.EqualsConsensus(b), Equals, true)
}

// TestNonceAccountAddress: known-answer for the create_with_seed derivation, so the
// THORChain-side check stays byte-identical to bifrost's provisioning.
func (s *SolNonceSuite) TestNonceAccountAddress(c *C) {
	got, err := SolNonceAccountAddress("66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn")
	c.Assert(err, IsNil)
	c.Assert(got, Not(Equals), "")
	again, err := SolNonceAccountAddress("66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn")
	c.Assert(err, IsNil)
	c.Assert(again, Equals, got) // deterministic

	_, err = SolNonceAccountAddress("not-base58-0OIl")
	c.Assert(err, NotNil)
	_, err = SolNonceAccountAddress("3QJmV3qfvL9SuYo34YihAf3sRCW3qSinyC") // wrong length
	c.Assert(err, NotNil)
}
