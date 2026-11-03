package common

import (
	"crypto/sha256"
	"math/big"
	"sort"
	"strconv"

	"github.com/blang/semver"
)

// Session nonces bind tss-lib's SSID to a single ceremony so that a
// transcript (ZK proofs, commitments) from one session cannot be replayed
// into another. tss-lib mixes the nonce into
// ssid = SHA512_256i(curve, partyIDs, roundNum, sessionNonce), which every
// party computes locally and uses as the Fiat-Shamir context when verifying
// its peers' proofs. The nonce is therefore NOT a secret and NOT locally
// random: it must be derived deterministically from values every party in
// the ceremony already agrees on, and must be byte-identical across all
// parties — a divergent nonce makes peer proof verification fail and aborts
// the ceremony (fail-safe, but a liveness failure).

// SessionNonceActivationVersion is the THORChain network version from which
// bifrost sets the session nonce. The pinned tss-lib already mixes the nonce
// into the SSID that each party uses to verify its peers' proofs, so a
// committee where only some members set it diverges and aborts every
// ceremony. Gating on the consensus network version — which only advances
// once all active nodes run the new release — flips the whole network in the
// same block, so no mixed committee can occur.
// TODO(tss owners): confirm the activation version before release.
const SessionNonceActivationVersion = "3.20.0"

var sessionNonceActivation = semver.MustParse(SessionNonceActivationVersion)

// SessionNonceEnabled reports whether the session nonce should be set for a
// ceremony at the given network version. The version comes from the ceremony
// request and is the consensus-agreed THORChain version, identical on every
// participant. An unparseable or empty version leaves the nonce off,
// matching pre-activation peers.
func SessionNonceEnabled(networkVersion string) bool {
	v, err := semver.Make(networkVersion)
	if err != nil {
		return false
	}
	return v.GTE(sessionNonceActivation)
}

// sessionNonce hashes a domain tag and the given parts into a positive
// big.Int. Parts are NUL-separated to prevent ambiguity between adjacent
// fields. A 0x01 byte is prepended to the digest so the result is always
// positive, as tss-lib rejects a nil or non-positive nonce.
func sessionNonce(domain string, parts ...string) *big.Int {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return new(big.Int).SetBytes(append([]byte{1}, h.Sum(nil)...))
}

// KeygenSessionNonce derives the session nonce for a keygen ceremony from
// the participant pubkey set, the keygen block height (the churn
// identifier), and the signing algorithm. All three come from the keygen
// request, which every participant receives identically from THORChain
// consensus. Keys are sorted on a copy so ordering differences between
// nodes cannot diverge the nonce.
func KeygenSessionNonce(keys []string, blockHeight int64, algo string) *big.Int {
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	parts := append([]string{strconv.FormatInt(blockHeight, 10), algo}, sorted...)
	return sessionNonce("thorchain/tss/keygen/v1", parts...)
}

// KeysignSessionNonce derives the session nonce for one message of a
// keysign ceremony from the pool pubkey, the message digest, the message's
// index in the batch, and the participant pubkey set. The batch is sorted
// by digest before sub-parties are built (tss/keysign.go), so (digest,
// index) pairs are identical on every signer; a per-message nonce cleanly
// separates the batched sub-parties, which already key off the
// digest:index moniker. Parties are sorted on a copy so ordering
// differences between nodes cannot diverge the nonce.
func KeysignSessionNonce(poolPubKey, msgDigest string, index int, parties []string) *big.Int {
	sorted := make([]string, len(parties))
	copy(sorted, parties)
	sort.Strings(sorted)
	parts := append([]string{poolPubKey, msgDigest, strconv.Itoa(index)}, sorted...)
	return sessionNonce("thorchain/tss/keysign/v1", parts...)
}
