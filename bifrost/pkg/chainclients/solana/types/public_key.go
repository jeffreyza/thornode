package types

import (
	"crypto/sha256"
	"fmt"

	"github.com/mr-tron/base58"
)

const (
	PublicKeyLength = 32
	MaxSeedLength   = 32
	MaxSeed         = 16
)

type PublicKey [PublicKeyLength]byte

func (p PublicKey) String() string {
	return p.ToBase58()
}

func (p PublicKey) Bytes() []byte {
	return p[:]
}

func (p PublicKey) EqualsString(s string) bool {
	return p.ToBase58() == s
}

func MustPublicKeyFromString(s string) PublicKey {
	d, _ := base58.Decode(s)
	return PublicKeyFromBytes(d)
}

func PublicKeyFromString(s string) (PublicKey, error) {
	d, err := base58.Decode(s)
	if err != nil {
		return PublicKey{}, err
	}
	return PublicKeyFromBytes(d), nil
}

func (p PublicKey) ToBase58() string {
	return base58.Encode(p[:])
}

func PublicKeyFromBytes(b []byte) PublicKey {
	var pubkey PublicKey
	if len(b) > PublicKeyLength {
		b = b[:PublicKeyLength]
	}
	copy(pubkey[PublicKeyLength-len(b):], b)
	return pubkey
}

// pdaMarker is the suffix Solana reserves for program-derived addresses. An owner
// ending in this marker is rejected by create_with_seed to keep the seed-derived
// and PDA address spaces disjoint.
var pdaMarker = []byte("ProgramDerivedAddress")

// CreateWithSeed deterministically derives an account address from a base key, an
// ASCII seed and the owning program, matching Solana's Pubkey::create_with_seed
// (sha256(base || seed || owner)). It is the derivation used to give a vault's
// durable-nonce account a fixed address every keysign party can compute from the
// vault pubkey alone — no shared state, no coordination.
//
// The seed must be at most MaxSeedLength bytes and the owner must not end in the
// PDA marker, mirroring the runtime's constraints; both are satisfied by the fixed
// nonce seed + SystemProgram owner this package uses, but are validated so a future
// caller cannot silently derive an address the runtime would reject.
func CreateWithSeed(base PublicKey, seed string, owner PublicKey) (PublicKey, error) {
	if len(seed) > MaxSeedLength {
		return PublicKey{}, fmt.Errorf("seed length %d exceeds max %d", len(seed), MaxSeedLength)
	}
	if len(owner) >= len(pdaMarker) {
		if string(owner[len(owner)-len(pdaMarker):]) == string(pdaMarker) {
			return PublicKey{}, fmt.Errorf("owner ends with PDA marker, illegal for create_with_seed")
		}
	}
	h := sha256.New()
	h.Write(base[:])
	h.Write([]byte(seed))
	h.Write(owner[:])
	return PublicKeyFromBytes(h.Sum(nil)), nil
}
