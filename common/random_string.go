package common

import (
	"crypto/rand"
)

const hexBytes = "abcdefABCDEF0123456789"

// randHexLimit is the largest multiple of len(hexBytes) a byte can hold. Bytes at
// or above it are redrawn, so b%len(hexBytes) stays uniform over hexBytes.
const randHexLimit = 256 - 256%len(hexBytes)

// RandHexString generates random hex string used for test purpose
func RandHexString(n int) string {
	ret := make([]byte, 0, n)
	// One read for the whole string rather than a rand.Int (a syscall and a
	// big.Int) per character.
	buf := make([]byte, n)
	for len(ret) < n {
		if _, err := rand.Read(buf[:n-len(ret)]); err != nil {
			return ""
		}
		for _, b := range buf[:n-len(ret)] {
			if int(b) < randHexLimit {
				ret = append(ret, hexBytes[int(b)%len(hexBytes)])
			}
		}
	}

	return string(ret)
}
