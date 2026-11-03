package common

import (
	"crypto/sha256"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	. "gopkg.in/check.v1"
)

type AttestationSigCacheSuite struct{}

var _ = Suite(&AttestationSigCacheSuite{})

func verifiedAttestationSigCached(pubKey, sig, payload []byte) bool {
	key := attestationSigCacheKey(pubKey, sig, payload)
	verifiedAttestationSigs.Lock()
	defer verifiedAttestationSigs.Unlock()
	_, cur := verifiedAttestationSigs.cur[key]
	_, prev := verifiedAttestationSigs.prev[key]
	return cur || prev
}

func (s *AttestationSigCacheSuite) TestVerifyAttestationSignature(c *C) {
	ResetVerifiedAttestationSigs()
	defer ResetVerifiedAttestationSigs()

	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().Bytes()
	payload := []byte("observed tx payload")
	sig, err := priv.Sign(payload)
	c.Assert(err, IsNil)

	c.Check(VerifyAttestationSignature(pub, sig, payload), Equals, true)
	c.Check(verifiedAttestationSigCached(pub, sig, payload), Equals, true)
	// a hit gives the same answer
	c.Check(VerifyAttestationSignature(pub, sig, payload), Equals, true)

	// a valid signature does not vouch for other bytes or another key
	other := []byte("observed tx payloaD")
	c.Check(VerifyAttestationSignature(pub, sig, other), Equals, false)
	c.Check(verifiedAttestationSigCached(pub, sig, other), Equals, false)
	otherPub := secp256k1.GenPrivKey().PubKey().Bytes()
	c.Check(VerifyAttestationSignature(otherPub, sig, payload), Equals, false)
	c.Check(verifiedAttestationSigCached(otherPub, sig, payload), Equals, false)

	// field boundaries are length-prefixed, so shifting bytes between the
	// signature and the payload cannot reuse a cached entry
	shifted := append(append([]byte{}, sig[len(sig)-1]), payload...)
	c.Check(VerifyAttestationSignature(pub, sig[:len(sig)-1], shifted), Equals, false)

	// malformed keys are rejected, not cached
	c.Check(VerifyAttestationSignature([]byte{1, 2, 3}, sig, payload), Equals, false)
	c.Check(verifiedAttestationSigCached([]byte{1, 2, 3}, sig, payload), Equals, false)
}

func (s *AttestationSigCacheSuite) TestVerifiedAttestationSigsBounded(c *C) {
	ResetVerifiedAttestationSigs()
	defer ResetVerifiedAttestationSigs()

	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().Bytes()
	first := []byte("first")
	sig, err := priv.Sign(first)
	c.Assert(err, IsNil)
	c.Assert(VerifyAttestationSignature(pub, sig, first), Equals, true)

	// fill the current generation with stand-in entries, then verify again:
	// the full generation rotates to prev, keeping the first entry for one more
	// generation
	verifiedAttestationSigs.Lock()
	for i := 0; len(verifiedAttestationSigs.cur) < verifiedAttestationSigsGeneration; i++ {
		verifiedAttestationSigs.cur[sha256.Sum256([]byte{byte(i), byte(i >> 8), byte(i >> 16)})] = struct{}{}
	}
	verifiedAttestationSigs.Unlock()

	second := []byte("second")
	sig2, err := priv.Sign(second)
	c.Assert(err, IsNil)
	c.Assert(VerifyAttestationSignature(pub, sig2, second), Equals, true)

	verifiedAttestationSigs.Lock()
	c.Check(verifiedAttestationSigs.cur, HasLen, 1)
	c.Check(verifiedAttestationSigs.prev, HasLen, verifiedAttestationSigsGeneration)
	verifiedAttestationSigs.Unlock()
	c.Check(verifiedAttestationSigCached(pub, sig, first), Equals, true)
	c.Check(verifiedAttestationSigCached(pub, sig2, second), Equals, true)
}
