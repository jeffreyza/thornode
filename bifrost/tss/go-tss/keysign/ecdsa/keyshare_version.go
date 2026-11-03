package ecdsa

import (
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
)

// KeyshareVersion records the on-disk shape of a deserialised LocalPartySaveData.
//
// The v3 fork of tss-lib added two fields to the Paillier private key (P, Q)
// so that keygen can emit a ModProof. The v0.1.6 binance-chain/tss-lib never
// stored them. Detecting which set of fields is populated tells us which
// generation of tss-lib produced the keyshare.
type KeyshareVersion int

const (
	// KeyshareVersionLegacy is the pre-v3 shape: PaillierSK has only
	// {N, LambdaN, PhiN}. Common in production on nodes that have not
	// re-keygenned since the v3 upgrade.
	KeyshareVersionLegacy KeyshareVersion = iota
	// KeyshareVersionV3 is the v3 shape: PaillierSK additionally carries
	// the safe primes {P, Q} required by ModProof during keygen.
	KeyshareVersionV3
)

// DetectKeyshareVersion classifies a deserialised keyshare by the presence of
// the v3-only Paillier safe primes. Callers must have unmarshalled the bytes
// into a v3 LocalPartySaveData before calling.
func DetectKeyshareVersion(d keygen.LocalPartySaveData) KeyshareVersion {
	if d.PaillierSK != nil && d.PaillierSK.P != nil && d.PaillierSK.Q != nil {
		return KeyshareVersionV3
	}
	return KeyshareVersionLegacy
}

// ValidateForKeysign returns true when the keyshare carries every field the
// v3 ECDSA signing protocol actually reads.
//
// This is strictly weaker than keygen.LocalPreParams.ValidateWithProof — that
// function additionally requires PaillierSK.P, PaillierSK.Q, the top-level
// Alpha, Beta, P, Q used by ModProof and FacProof at keygen time. None of
// those are referenced from vendor/.../v3/ecdsa/signing/. Using
// ValidateWithProof at the keysign entry point rejects legacy keyshares for
// no protocol reason.
//
// If a future v3 signing change starts consuming a new field, add it here.
func ValidateForKeysign(d keygen.LocalPartySaveData) bool {
	if d.PaillierSK == nil ||
		d.PaillierSK.N == nil ||
		d.PaillierSK.LambdaN == nil ||
		d.PaillierSK.PhiN == nil {
		return false
	}
	if d.NTildei == nil || d.H1i == nil || d.H2i == nil {
		return false
	}
	if d.Xi == nil || d.ShareID == nil {
		return false
	}
	if d.ECDSAPub == nil {
		return false
	}
	n := len(d.Ks)
	if n == 0 ||
		len(d.NTildej) != n ||
		len(d.H1j) != n ||
		len(d.H2j) != n ||
		len(d.BigXj) != n ||
		len(d.PaillierPKs) != n {
		return false
	}
	for i := 0; i < n; i++ {
		if d.Ks[i] == nil ||
			d.NTildej[i] == nil ||
			d.H1j[i] == nil ||
			d.H2j[i] == nil ||
			d.BigXj[i] == nil ||
			d.PaillierPKs[i] == nil ||
			d.PaillierPKs[i].N == nil {
			return false
		}
	}
	return true
}
