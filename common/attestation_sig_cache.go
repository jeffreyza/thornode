package common

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

// verifiedAttestationSigs memoizes secp256k1 attestation signatures this
// process has already verified. Every quorum message carries ~100 signatures
// and checking them dominates the handler's CPU, yet the local enshrined
// bifrost has usually received the same attestations before the block that
// includes them, so they can be checked off the block path ahead of time.
//
// Only successful verifications are stored, keyed by a hash binding the
// pubkey, the signature and the exact signed bytes, so a hit returns what
// verifying again would return. A miss verifies inline. The cache contents
// can therefore change wall time but never a result.
//
// Bounded by two generations: when the current one fills, it becomes the
// previous one and the oldest generation is dropped.
var verifiedAttestationSigs = struct {
	sync.Mutex
	cur, prev map[[sha256.Size]byte]struct{}
}{cur: make(map[[sha256.Size]byte]struct{})}

const verifiedAttestationSigsGeneration = 1 << 15

func attestationSigCacheKey(pubKey, sig, payload []byte) [sha256.Size]byte {
	h := sha256.New()
	var n [8]byte
	for _, b := range [][]byte{pubKey, sig, payload} {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

// VerifyAttestationSignature reports whether sig is pubKey's secp256k1
// signature over payload, as secp256k1.PubKey.VerifySignature does, consulting
// and filling the verified cache.
func VerifyAttestationSignature(pubKey, sig, payload []byte) bool {
	key := attestationSigCacheKey(pubKey, sig, payload)

	c := &verifiedAttestationSigs
	c.Lock()
	_, hit := c.cur[key]
	if !hit {
		_, hit = c.prev[key]
	}
	c.Unlock()
	if hit {
		return true
	}

	pk := secp256k1.PubKey{Key: pubKey}
	if !pk.VerifySignature(payload, sig) {
		return false
	}

	c.Lock()
	if len(c.cur) >= verifiedAttestationSigsGeneration {
		c.prev = c.cur
		c.cur = make(map[[sha256.Size]byte]struct{}, verifiedAttestationSigsGeneration)
	}
	c.cur[key] = struct{}{}
	c.Unlock()
	return true
}

// ResetVerifiedAttestationSigs empties the verified signature cache, so
// benchmarks can measure handlers that verify every signature inline.
func ResetVerifiedAttestationSigs() {
	c := &verifiedAttestationSigs
	c.Lock()
	c.cur = make(map[[sha256.Size]byte]struct{})
	c.prev = nil
	c.Unlock()
}
