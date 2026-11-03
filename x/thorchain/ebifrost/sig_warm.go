package ebifrost

import (
	"runtime"

	common "gitlab.com/thorchain/thornode/v3/common"
)

// sigWarmSlots bounds the goroutines verifying attestation signatures ahead of
// the block that includes them. When every slot is busy, warming is skipped:
// the quorum handler then verifies those signatures inline, as without warming.
var sigWarmSlots = make(chan struct{}, max(runtime.GOMAXPROCS(0)/4, 1))

// sigWarmVerify verifies and caches one signature; tests replace it.
var sigWarmVerify = common.VerifyAttestationSignature

// sigWarmJob is one attestation set and the payload its signatures cover.
type sigWarmJob struct {
	payload      []byte
	attestations []*common.Attestation
}

// warmAttestationSigs verifies the jobs' signatures in the background so the
// quorum handlers find them in common.VerifyAttestationSignature's cache. It
// never blocks the gRPC caller, and its outcome only affects handler wall
// time: invalid signatures are not cached and are rejected again on delivery.
func warmAttestationSigs(jobs ...sigWarmJob) {
	if len(jobs) == 0 {
		return
	}
	select {
	case sigWarmSlots <- struct{}{}:
	default:
		return
	}
	verify := sigWarmVerify
	go func() {
		defer func() { <-sigWarmSlots }()
		for _, job := range jobs {
			for _, att := range job.attestations {
				if att == nil || len(att.PubKey) == 0 || len(att.Signature) == 0 {
					continue
				}
				warmAttestationSig(verify, att, job.payload)
			}
		}
	}()
}

// warmAttestationSig verifies one signature, ignoring a panic on malformed
// input.
func warmAttestationSig(verify func(pubKey, sig, payload []byte) bool, att *common.Attestation, payload []byte) {
	defer func() { _ = recover() }()
	verify(att.PubKey, att.Signature, payload)
}
