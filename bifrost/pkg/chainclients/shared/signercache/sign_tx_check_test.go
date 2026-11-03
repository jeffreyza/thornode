package signercache

import (
	. "gopkg.in/check.v1"
)

type SignTxCheckSuite struct{}

var _ = Suite(&SignTxCheckSuite{})

const (
	testCacheHash = "cache-hash-abc"
	testSig       = "broadcast-sig-xyz"
)

// constVerifyFn returns a VerifyOnChainFunc that always reports `result`.
// The wrapper records the sig it was called with so tests can assert call
// arguments and counts.
type recordingVerify struct {
	result LookupResult
	calls  []string
}

func (r *recordingVerify) fn(sig string) LookupResult {
	r.calls = append(r.calls, sig)
	return r.result
}

// failingVerify returns a VerifyOnChainFunc that fails the test if invoked.
// Used by branches where verification must NOT be reached (no entry, fresh,
// expired+gateOff, expired+gateOn+noSig).
func failingVerify(c *C) VerifyOnChainFunc {
	return func(sig string) LookupResult {
		c.Fatalf("verifyFn must not be called, was called with sig=%q", sig)
		return LookupAmbiguous
	}
}

// gateOn / gateOff are GateFunc helpers. gateOn also records call count so
// tests can assert lazy evaluation (the gate must NOT be called when there's
// no cache entry or the cache is fresh).
func gateOn() bool  { return true }
func gateOff() bool { return false }

// recordingGate counts invocations so a test can pin lazy evaluation.
type recordingGate struct {
	enabled bool
	calls   int
}

func (g *recordingGate) fn() bool {
	g.calls++
	return g.enabled
}

// makeManager constructs a CacheManager wired to a mock StorageAccessor with
// the given HasSignedWithExpiry / GetSignedTxHash configuration.
func makeManager(signed, expired bool, sig string, sigOk bool) (*CacheManager, *mockStorageAccessor) {
	mock := &mockStorageAccessor{
		hasSignedResult:        signed,
		hasSignedExpiredResult: expired,
		getSignedTxHashResult:  sig,
		getSignedTxHashOk:      sigOk,
	}
	cm := &CacheManager{storageAccessor: mock}
	return cm, mock
}

// PhaseNoEntry: HasSignedWithExpiry returns (false, _) → caller proceeds with
// signing; verifyFn must not be invoked; cache must not be touched.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseNoEntry(c *C) {
	cm, mock := makeManager(false, false, "", false)

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, failingVerify(c))

	c.Assert(got.SkipSign, Equals, false)
	c.Assert(got.Phase, Equals, PhaseNoEntry)
	c.Assert(got.Sig, Equals, "")
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseFresh: HasSignedWithExpiry returns (true, false) → cache valid, skip
// signing without consulting the chain.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseFresh(c *C) {
	cm, mock := makeManager(true, false, testSig, true)

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, failingVerify(c))

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseFresh)
	c.Assert(got.Sig, Equals, "")
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseExpiredGateOff: HasSignedWithExpiry returns (true, true) but the
// operational gate is off → respect cache without consulting the chain.
// This is the default safety posture for chains that haven't enabled the
// SkipSignerCache mimir.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseExpiredGateOff(c *C) {
	cm, mock := makeManager(true, true, testSig, true)

	got := cm.SignTxCacheCheck(testCacheHash, gateOff, failingVerify(c))

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseExpiredGateOff)
	c.Assert(got.Sig, Equals, "")
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseExpiredNoSig: cache expired, gate enabled, but no recorded broadcast
// hash → cannot verify on chain, must respect cache (fail-closed).
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseExpiredNoSig(c *C) {
	cm, mock := makeManager(true, true, "", false)

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, failingVerify(c))

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseExpiredNoSig)
	c.Assert(got.Sig, Equals, "")
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseVerifiedFound: chain reports the recorded broadcast is on chain →
// respect cache (re-signing would double-spend).
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseVerifiedFound(c *C) {
	cm, mock := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupFound}

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseVerifiedFound)
	c.Assert(got.Sig, Equals, testSig)
	c.Assert(verify.calls, DeepEquals, []string{testSig})
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseVerifiedAmbiguous: verification could not confirm absence (RPC error,
// ambiguous response) → respect cache (fail-closed). This is the bug class
// the safety invariant is designed to prevent: a transient RPC error must
// never be treated as evidence of absence.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseVerifiedAmbiguous(c *C) {
	cm, mock := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupAmbiguous}

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseVerifiedAmbiguous)
	c.Assert(got.Sig, Equals, testSig)
	c.Assert(verify.calls, DeepEquals, []string{testSig})
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseVerifiedAbsentCleared: verification confirms the recorded broadcast is
// definitively absent → cache cleared with the broadcast SIG (NOT the cache
// hash) and caller proceeds with re-signing.
//
// This test pins the safety invariant flagged in MR review: RemoveSigned
// must be called with the broadcast hash so the storage layer can map back
// to the cache key correctly.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseVerifiedAbsentCleared(c *C) {
	cm, mock := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupNotFound}

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

	c.Assert(got.SkipSign, Equals, false)
	c.Assert(got.Phase, Equals, PhaseVerifiedAbsentCleared)
	c.Assert(got.Sig, Equals, testSig)
	c.Assert(verify.calls, DeepEquals, []string{testSig})
	// Critical: RemoveSigned was called exactly once with the SIG, not the
	// cache hash. Storage maps sig→cacheHash internally.
	c.Assert(mock.removeSignedCalls, DeepEquals, []string{testSig})
}

// Final failure is distinct from absence: retaining the failed broadcast lets
// the caller reject its checkpoint again if construction fails or it restarts.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_PhaseVerifiedFailed(c *C) {
	cm, mock := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupFailed}

	for i := 0; i < 2; i++ {
		got := cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

		c.Assert(got.SkipSign, Equals, false)
		c.Assert(got.Phase, Equals, PhaseVerifiedFailed)
		c.Assert(got.Sig, Equals, testSig)
		c.Assert(got.Noteworthy(), Equals, true)
		c.Assert(mock.removeSignedCalls, IsNil)
	}
	c.Assert(verify.calls, DeepEquals, []string{testSig, testSig})
}

func (s *SignTxCheckSuite) TestSignTxCacheCheck_UnknownLookupRetainsCache(c *C) {
	cm, mock := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupResult(999)}

	got := cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseVerifiedAmbiguous)
	c.Assert(got.Sig, Equals, testSig)
	c.Assert(verify.calls, DeepEquals, []string{testSig})
	c.Assert(mock.removeSignedCalls, IsNil)
}

// VerifyFn is invoked at most once per call. Tests above already enforce
// "at least once" via DeepEquals; this guards against a future refactor
// adding redundant lookups (e.g. retry loops).
func (s *SignTxCheckSuite) TestSignTxCacheCheck_VerifyCalledExactlyOnce(c *C) {
	cm, _ := makeManager(true, true, testSig, true)
	verify := &recordingVerify{result: LookupFound}

	_ = cm.SignTxCacheCheck(testCacheHash, gateOn, verify.fn)

	c.Assert(len(verify.calls), Equals, 1)
}

// Lazy evaluation: gateFn must NOT be called when there is no cache entry
// or the cache is fresh. This pins the perf/correctness invariant — chain
// clients pass closures that hit thornode's mimir endpoint on every call,
// so an eager invocation would double thornode's mimir load on every SignTx.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_GateFnLazyOnNoEntry(c *C) {
	cm, _ := makeManager(false, false, "", false)
	gate := &recordingGate{enabled: true}

	_ = cm.SignTxCacheCheck(testCacheHash, gate.fn, failingVerify(c))

	c.Assert(gate.calls, Equals, 0) // never called when no entry
}

func (s *SignTxCheckSuite) TestSignTxCacheCheck_GateFnLazyOnFresh(c *C) {
	cm, _ := makeManager(true, false, testSig, true)
	gate := &recordingGate{enabled: true}

	_ = cm.SignTxCacheCheck(testCacheHash, gate.fn, failingVerify(c))

	c.Assert(gate.calls, Equals, 0) // never called when cache fresh
}

// gateFn IS called when the cache is expired and we need a decision.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_GateFnCalledOnExpired(c *C) {
	cm, _ := makeManager(true, true, testSig, true)
	gate := &recordingGate{enabled: false} // gate-off, but still must be consulted

	_ = cm.SignTxCacheCheck(testCacheHash, gate.fn, failingVerify(c))

	c.Assert(gate.calls, Equals, 1)
}

// nil gateFn is treated as gate-off (defensive). Avoids panic if a chain
// client forgets to pass a closure.
func (s *SignTxCheckSuite) TestSignTxCacheCheck_NilGateFnTreatedAsOff(c *C) {
	cm, mock := makeManager(true, true, testSig, true)

	got := cm.SignTxCacheCheck(testCacheHash, nil, failingVerify(c))

	c.Assert(got.SkipSign, Equals, true)
	c.Assert(got.Phase, Equals, PhaseExpiredGateOff)
	c.Assert(mock.removeSignedCalls, IsNil)
}

// PhaseString returns a non-empty, distinct human-readable label for each
// phase. The strings are part of the public log surface; this asserts they
// remain stable and unique.
func (s *SignTxCheckSuite) TestCacheCheckPhaseStrings(c *C) {
	phases := []CacheCheckPhase{
		PhaseNoEntry,
		PhaseFresh,
		PhaseExpiredGateOff,
		PhaseExpiredNoSig,
		PhaseVerifiedFound,
		PhaseVerifiedAmbiguous,
		PhaseVerifiedAbsentCleared,
		PhaseVerifiedFailed,
	}
	seen := map[string]bool{}
	for _, p := range phases {
		s := p.String()
		c.Assert(s, Not(Equals), "")
		c.Assert(s, Not(Equals), "unknown phase")
		c.Assert(seen[s], Equals, false)
		seen[s] = true
	}
}

// Unrecognized phase value falls through to "unknown phase".
func (s *SignTxCheckSuite) TestCacheCheckPhaseStringUnknown(c *C) {
	c.Assert(CacheCheckPhase(999).String(), Equals, "unknown phase")
}

func (s *SignTxCheckSuite) TestCacheCheckNoteworthy(c *C) {
	for _, phase := range []CacheCheckPhase{
		PhaseNoEntry, PhaseFresh, PhaseExpiredGateOff, PhaseExpiredNoSig, PhaseVerifiedFound,
	} {
		c.Assert((CacheCheck{Phase: phase}).Noteworthy(), Equals, false)
	}
	for _, phase := range []CacheCheckPhase{
		PhaseVerifiedAmbiguous, PhaseVerifiedAbsentCleared, PhaseVerifiedFailed,
	} {
		c.Assert((CacheCheck{Phase: phase}).Noteworthy(), Equals, true)
	}
}
