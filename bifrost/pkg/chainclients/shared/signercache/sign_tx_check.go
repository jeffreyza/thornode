package signercache

// LookupResult communicates the outcome of an on-chain verification call.
//
// The asymmetry between LookupAmbiguous and LookupNotFound is critical to the
// safety invariant: an RPC error or unknown response is NOT evidence of
// absence. Treating it as such caused the class of bugs in MR review — a
// transient RPC failure during the mimir-gated bypass produced duplicate
// broadcasts whenever the original was actually live.
type LookupResult int

const (
	// LookupAmbiguous indicates the verification call could not positively
	// confirm presence or absence (RPC error, unparsable response, empty
	// response that does not disambiguate unknown vs pending). Cache is
	// respected — re-signing could double-spend if the tx is on chain.
	LookupAmbiguous LookupResult = iota
	// LookupFound indicates the recorded broadcast is on chain. Cache is
	// respected — re-signing would land alongside the existing tx.
	LookupFound
	// LookupNotFound indicates the recorded broadcast is definitively absent
	// from the chain (positive "not found" signal, e.g. ethereum.NotFound,
	// gRPC codes.NotFound, btcjson code -5, XRP txnNotFound). Cache is
	// cleared and the caller proceeds with re-signing.
	LookupNotFound
	// LookupFailed indicates the recorded broadcast is finalized and execution
	// definitively failed without making the payment. The caller may recover,
	// but must not reuse the failed transaction. Keep its cache entry until a
	// new broadcast replaces it so retries and restarts retain that evidence.
	LookupFailed
)

// VerifyOnChainFunc verifies whether a recorded broadcast tx is on chain.
// Each chain provides its own implementation that distinguishes between the
// LookupResult outcomes. The function should not log; logging is the
// caller's responsibility based on the CacheCheck.Phase returned.
type VerifyOnChainFunc func(sig string) LookupResult

// GateFunc decides whether the operational gate is open for the verification
// path. It is called lazily — only when SignTxCacheCheck has determined the
// cache is expired and needs to consult the gate. Returning false short-
// circuits to PhaseExpiredGateOff (respect cache).
//
// Lazy evaluation matters: the gate typically reads an operator-controlled
// mimir flag via an HTTP round-trip. Calling it on every SignTx (even when
// there's no cache entry) is wasted work and doubles the load on thornode's
// mimir endpoint during incidents.
type GateFunc func() bool

// CacheCheckPhase identifies the decision path taken by SignTxCacheCheck. It
// is returned to the caller for structured logging without coupling the
// helper to any particular logger.
type CacheCheckPhase int

const (
	// PhaseNoEntry means there is no cache entry for this tx out item.
	// SignTx should proceed with signing.
	PhaseNoEntry CacheCheckPhase = iota
	// PhaseFresh means the cache entry exists and has not expired.
	// SignTx must skip signing.
	PhaseFresh
	// PhaseExpiredGateOff means the cache is past expiry but the operational
	// gate (e.g. SkipSignerCache mimir) is not enabled. SignTx must skip.
	PhaseExpiredGateOff
	// PhaseExpiredNoSig means the cache is expired and the gate is enabled,
	// but no broadcast hash is recorded — verification is impossible. SignTx
	// must skip (fail-closed).
	PhaseExpiredNoSig
	// PhaseVerifiedFound means verification confirmed the recorded broadcast
	// is on chain. SignTx must skip to avoid double-spend.
	PhaseVerifiedFound
	// PhaseVerifiedAmbiguous means verification could not confirm absence
	// (RPC error, ambiguous response). SignTx must skip (fail-closed).
	PhaseVerifiedAmbiguous
	// PhaseVerifiedAbsentCleared means verification confirmed the recorded
	// broadcast is absent from chain. The cache entry has been removed and
	// SignTx should proceed with re-signing.
	PhaseVerifiedAbsentCleared
	// PhaseVerifiedFailed means verification confirmed finalized execution
	// failure. SignTx may recover, but must replace the failed transaction.
	// The cache is retained until the replacement broadcast is recorded.
	PhaseVerifiedFailed
)

// String returns a human-readable label for the phase, suitable for log
// messages.
func (p CacheCheckPhase) String() string {
	switch p {
	case PhaseNoEntry:
		return "no cache entry, proceeding with sign"
	case PhaseFresh:
		return "transaction signed before, ignore"
	case PhaseExpiredGateOff:
		return "signer cache expired but mimir not set, respecting cache"
	case PhaseExpiredNoSig:
		return "signer cache expired but no recorded tx hash, respecting cache"
	case PhaseVerifiedFound:
		return "cached broadcast confirmed on chain, respecting signer cache"
	case PhaseVerifiedAmbiguous:
		return "could not confirm absence on chain, respecting signer cache"
	case PhaseVerifiedAbsentCleared:
		return "expired cached broadcast not found on chain, clearing signer cache for re-sign"
	case PhaseVerifiedFailed:
		return "cached broadcast finalized with failed execution, retaining signer cache for recovery"
	}
	return "unknown phase"
}

// CacheCheck describes the outcome of SignTxCacheCheck.
type CacheCheck struct {
	// SkipSign is true when SignTx should return nil immediately (cache says
	// don't sign). It is false on PhaseNoEntry, PhaseVerifiedAbsentCleared,
	// and PhaseVerifiedFailed.
	SkipSign bool
	// Phase identifies which branch was taken; useful for logging.
	Phase CacheCheckPhase
	// Sig is the recorded broadcast hash, when one was retrieved (empty in
	// PhaseNoEntry, PhaseFresh, PhaseExpiredGateOff, PhaseExpiredNoSig).
	Sig string
}

// Noteworthy reports whether the outcome is operationally significant enough to
// log above Info: verification could not confirm absence (PhaseVerifiedAmbiguous)
// or recovery is allowed (PhaseVerifiedAbsentCleared / PhaseVerifiedFailed).
// Routine cache hits (fresh / found / gate-off) stay at Info.
func (c CacheCheck) Noteworthy() bool {
	return c.Phase == PhaseVerifiedAmbiguous || c.Phase == PhaseVerifiedAbsentCleared || c.Phase == PhaseVerifiedFailed
}

// SignTxCacheCheck implements the standard signer-cache check performed at the
// top of a chain client's SignTx. It mirrors the Solana reference pattern
// (sol_client.go) so all chain clients enforce the same safety invariant by
// construction:
//
//  1. If no cache entry exists -> proceed with signing.
//  2. If the cache entry is fresh -> skip signing.
//  3. If the cache entry has expired:
//     a. If gateFn is nil or returns false -> skip signing (respect cache).
//     b. If no recorded broadcast hash -> skip signing (cannot verify).
//     c. Otherwise call verifyFn on the recorded sig:
//     - LookupFound or LookupAmbiguous -> skip signing.
//     - LookupNotFound -> clear the cache and proceed with re-signing.
//     - LookupFailed -> retain the cache and recover with a new transaction.
//
// The gateFn parameter is the operational guard for the verification path. It
// is invoked lazily — only when the cache is expired AND the gate decision is
// actually needed. Solana passes a closure that always returns true (no
// mimir gate). The other chain clients pass a closure that reads
// SkipSignerCache<Chain> mimir, so operators retain a kill-switch over the
// recovery path. Lazy evaluation avoids an HTTP round-trip on every SignTx
// call when the cache is fresh or absent.
func (cm *CacheManager) SignTxCacheCheck(
	cacheHash string,
	gateFn GateFunc,
	verifyFn VerifyOnChainFunc,
) CacheCheck {
	signed, expired := cm.HasSignedWithExpiry(cacheHash)
	if !signed {
		return CacheCheck{SkipSign: false, Phase: PhaseNoEntry}
	}
	if !expired {
		return CacheCheck{SkipSign: true, Phase: PhaseFresh}
	}
	if gateFn == nil || !gateFn() {
		return CacheCheck{SkipSign: true, Phase: PhaseExpiredGateOff}
	}
	sig, ok := cm.GetSignedTxHash(cacheHash)
	if !ok {
		return CacheCheck{SkipSign: true, Phase: PhaseExpiredNoSig}
	}
	switch verifyFn(sig) {
	case LookupFound:
		return CacheCheck{SkipSign: true, Phase: PhaseVerifiedFound, Sig: sig}
	case LookupNotFound:
		cm.RemoveSigned(sig)
		return CacheCheck{SkipSign: false, Phase: PhaseVerifiedAbsentCleared, Sig: sig}
	case LookupFailed:
		return CacheCheck{SkipSign: false, Phase: PhaseVerifiedFailed, Sig: sig}
	default: // LookupAmbiguous
		return CacheCheck{SkipSign: true, Phase: PhaseVerifiedAmbiguous, Sig: sig}
	}
}
