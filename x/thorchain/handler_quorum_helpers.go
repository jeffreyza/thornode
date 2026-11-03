package thorchain

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// deduplicateAttestations returns attestations deduplicated by pubkey, capped at maxCount.
// This prevents a malicious block proposer from including thousands of duplicate attestations
// to cause excessive signature verification and KV store operations.
func deduplicateAttestations(attestations []*common.Attestation, maxCount int) []*common.Attestation {
	if len(attestations) == 0 {
		return attestations
	}
	seen := make(map[string]struct{})
	result := make([]*common.Attestation, 0, min(len(attestations), maxCount))
	for _, att := range attestations {
		if att == nil {
			continue
		}
		key := string(att.PubKey)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, att)
		if len(result) >= maxCount {
			break
		}
	}
	return result
}

// attestationBech32Cache memoizes the account bech32 encoding of attestation
// secp256k1 pubkeys. Every quorum message re-encodes the same ~100 active
// validator keys (twice: preverifyAttestations and verifyQuorumAttestation),
// and the encoding is a pure function of the key bytes and the account pubkey
// prefix, so the cache cannot change results. Only keys that matched an active
// node are stored, so attestations from arbitrary keys cannot crowd out the
// validators; bounded: once full, keys are encoded without being stored.
var (
	attestationBech32Cache   = make(map[string]attestationBech32Entry)
	attestationBech32CacheMu sync.RWMutex
)

const attestationBech32CacheMax = 4096

// attestationBech32Entry is a key's encoding and the account pubkey prefix it
// was made under. The cache is keyed by the raw key bytes, so lookups do not
// allocate; an entry made under another prefix is never served.
type attestationBech32Entry struct {
	prefix, bech32Pub string
}

// attestationBech32Pub is cosmos.Bech32ifyPubKey(Bech32PubKeyTypeAccPub) of the
// secp256k1 key, served from attestationBech32Cache when present.
func attestationBech32Pub(key []byte) (string, error) {
	attestationBech32CacheMu.RLock()
	e, ok := attestationBech32Cache[string(key)]
	attestationBech32CacheMu.RUnlock()
	if ok && e.prefix == cosmos.GetConfig().GetBech32AccountPubPrefix() {
		return e.bech32Pub, nil
	}
	pk := secp256k1.PubKey{Key: key}
	return cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &pk)
}

// rememberAttestationBech32Pub stores a successful encoding of an active
// node's key, replacing one made under another prefix.
func rememberAttestationBech32Pub(key []byte, bech32Pub string) {
	prefix := cosmos.GetConfig().GetBech32AccountPubPrefix()
	attestationBech32CacheMu.Lock()
	if e, ok := attestationBech32Cache[string(key)]; ok && e.prefix != prefix || !ok && len(attestationBech32Cache) < attestationBech32CacheMax {
		attestationBech32Cache[string(key)] = attestationBech32Entry{prefix: prefix, bech32Pub: bech32Pub}
	}
	attestationBech32CacheMu.Unlock()
}

// attestationCheck is one attestation and the payload it must sign.
type attestationCheck struct {
	att    *common.Attestation
	signBz []byte
}

// attestationChecks pairs each attestation with the shared payload.
func attestationChecks(atts []*common.Attestation, signBz []byte) []attestationCheck {
	checks := make([]attestationCheck, len(atts))
	for i, att := range atts {
		checks[i] = attestationCheck{att: att, signBz: signBz}
	}
	return checks
}

// minParallelAttestations is the smallest batch preverifyAttestations runs.
const minParallelAttestations = 4

// attestationSigs holds secp256k1 results computed ahead of a handler's
// sequential attestation loop, by attestation.
type attestationSigs map[*common.Attestation]bool

// preverifyAttestations verifies the signatures of the attestations signed by
// an active node concurrently. Verification is a pure function of (pubkey,
// payload, signature) and dominates quorum message handling, so this changes
// nothing but wall time: verifyQuorumAttestation still runs every check in
// order and only takes the signature result from here.
func preverifyAttestations(activeNodeAccounts NodeAccounts, checks []attestationCheck) attestationSigs {
	// a handful of signatures is not worth the setup; verify them in order
	if len(checks) < minParallelAttestations {
		return nil
	}
	active := make(map[string]struct{}, len(activeNodeAccounts))
	for _, na := range activeNodeAccounts {
		active[na.PubKeySet.Secp256k1.String()] = struct{}{}
	}
	todo := make([]attestationCheck, 0, len(checks))
	for _, c := range checks {
		if c.att == nil || len(c.att.PubKey) == 0 || len(c.att.Signature) == 0 {
			continue
		}
		bech32Pub, err := attestationBech32Pub(c.att.PubKey)
		if err != nil {
			continue
		}
		if _, ok := active[bech32Pub]; ok {
			rememberAttestationBech32Pub(c.att.PubKey, bech32Pub)
			todo = append(todo, c)
		}
	}

	results := make([]bool, len(todo))
	done := make([]bool, len(todo))
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := min(runtime.GOMAXPROCS(0), len(todo)); w > 0; w-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(todo) {
					return
				}
				results[i], done[i] = verifyAttestationSig(todo[i])
			}
		}()
	}
	wg.Wait()

	sigs := make(attestationSigs, len(todo))
	for i, c := range todo {
		if done[i] {
			sigs[c.att] = results[i]
		}
	}
	return sigs
}

// verifyAttestationSig reports ok=false if verification panics, leaving that
// attestation to verifyQuorumAttestation in the handler goroutine, where a
// panic is recovered by the tx runner exactly as before.
func verifyAttestationSig(c attestationCheck) (verified, ok bool) {
	defer func() {
		if recover() != nil {
			verified, ok = false, false
		}
	}()
	return common.VerifyAttestationSignature(c.att.PubKey, c.att.Signature, c.signBz), true
}

func verifyQuorumAttestation(activeNodeAccounts NodeAccounts, signBz []byte, att *common.Attestation, sigs attestationSigs) (cosmos.AccAddress, error) {
	if att == nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "nil")},
		)
		return nil, fmt.Errorf("attestation is nil")
	}
	if len(att.PubKey) == 0 {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "empty_pubkey")},
		)
		return nil, fmt.Errorf("pubkey is empty")
	}
	if len(att.Signature) == 0 {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "empty_signature")},
		)
		return nil, fmt.Errorf("signature is empty")
	}

	pk := secp256k1.PubKey{Key: att.PubKey}

	bech32Pub, err := attestationBech32Pub(att.PubKey)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "bech32_fail")},
		)
		return nil, fmt.Errorf("fail to get bech32 pub key: %w", err)
	}

	// check if the signer is an active node account
	found := false
	for _, validator := range activeNodeAccounts {
		if bech32Pub == validator.PubKeySet.Secp256k1.String() {
			found = true
			break
		}
	}
	if found {
		rememberAttestationBech32Pub(att.PubKey, bech32Pub)
	} else {
		// can occur if a node account churns out before the tx is processed.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "not_active_node")},
		)
		return nil, fmt.Errorf("signer is not an active node account: %s", pk.String())
	}

	verified, ok := sigs[att]
	if !ok {
		verified = common.VerifyAttestationSignature(att.PubKey, att.Signature, signBz)
	}
	if !verified {
		// A node signing payloads the rest of the network cannot verify is on a
		// mismatched build or malfunctioning; its observations are discarded.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "bad_signature")},
		)
		return nil, fmt.Errorf("failed to verify signature: %s", pk.String())
	}

	return cosmos.AccAddress(pk.Address()), nil
}
