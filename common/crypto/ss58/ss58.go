package ss58

import (
	"errors"
	"fmt"

	"github.com/mr-tron/base58"
	"golang.org/x/crypto/blake2b"
)

var ss58Prefix = []byte("SS58PRE")

// Encode encodes a public key into an SS58 address with the given network prefix.
// For simple (single-byte) prefixes (0-63), the format is:
//
//	prefix(1 byte) + pubkey(32 bytes) + checksum(2 bytes) → base58
func Encode(prefix byte, pubkey []byte) (string, error) {
	if len(pubkey) != 32 {
		return "", fmt.Errorf("invalid pubkey length %d, expected 32", len(pubkey))
	}

	// Build payload: SS58PRE + prefix + pubkey
	payload := make([]byte, 0, len(ss58Prefix)+1+len(pubkey))
	payload = append(payload, ss58Prefix...)
	payload = append(payload, prefix)
	payload = append(payload, pubkey...)

	// Blake2b-512 checksum
	hash := blake2b.Sum512(payload)
	checksum := hash[:2]

	// Encode: prefix + pubkey + checksum
	raw := make([]byte, 0, 1+32+2)
	raw = append(raw, prefix)
	raw = append(raw, pubkey...)
	raw = append(raw, checksum...)

	return base58.Encode(raw), nil
}

// Decode decodes an SS58 address into its network prefix and public key.
func Decode(address string) (prefix byte, pubkey []byte, err error) {
	raw, err := base58.Decode(address)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid base58 encoding: %w", err)
	}

	// Simple prefix: 1 byte prefix + 32 bytes pubkey + 2 bytes checksum = 35 bytes
	if len(raw) != 35 {
		return 0, nil, fmt.Errorf("invalid SS58 address length %d, expected 35", len(raw))
	}

	prefix = raw[0]
	pubkey = raw[1:33]
	checksum := raw[33:35]

	// Verify checksum
	payload := make([]byte, 0, len(ss58Prefix)+1+32)
	payload = append(payload, ss58Prefix...)
	payload = append(payload, prefix)
	payload = append(payload, pubkey...)

	hash := blake2b.Sum512(payload)
	if hash[0] != checksum[0] || hash[1] != checksum[1] {
		return 0, nil, errors.New("invalid SS58 checksum")
	}

	return prefix, pubkey, nil
}
