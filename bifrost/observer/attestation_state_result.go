package observer

import "fmt"

type attestationStateTransferResult struct {
	items     int // wire items with accepted evidence, including already retained duplicates
	completed bool
	err       error
}

// A definitive rejection leaves both fields empty. A temporary failure before
// storage makes the transfer incomplete; later delivery failures do not.
type attestationStateImportResult struct {
	items      int
	incomplete error
}

func (r *attestationStateImportResult) record(outcome attestationImportOutcome, itemAccepted *bool) {
	if outcome.accepted && !*itemAccepted {
		r.items++
		*itemAccepted = true
	}
	// Keep one cause rather than retaining an error for every rejected share.
	if r.incomplete == nil && outcome.err != nil {
		r.incomplete = fmt.Errorf("attestation state import incomplete: %w", outcome.err)
	}
}

type attestationStateGroupResult struct {
	empty, nonEmpty, failed, partial int
}

func (r *attestationStateGroupResult) add(other attestationStateGroupResult) {
	r.empty += other.empty
	r.nonEmpty += other.nonEmpty
	r.failed += other.failed
	r.partial += other.partial
}

func (r attestationStateGroupResult) outcome() string {
	switch {
	case r.nonEmpty > 0:
		return "non_empty_snapshot_completed"
	case r.empty > 0 && r.failed > 0:
		return "empty_snapshots_with_failures"
	case r.empty > 0:
		return "empty_snapshots_only"
	case r.partial > 0 && r.partial == r.failed:
		return "partial_transfers_only"
	default:
		return "no_completed_snapshot"
	}
}

func (r attestationStateGroupResult) completionReason() string {
	if r.nonEmpty > 0 {
		return "non_empty_completion"
	}
	return ""
}

func (s *AttestationGossip) logAttestationStateCollection(c *attestationStateCollection, reason string) {
	event := s.logger.Info()
	if c.result.empty+c.result.nonEmpty == 0 {
		event = s.logger.Warn()
	}
	event.Str("reason", reason).Str("outcome", c.result.outcome()).
		Int("requests", c.requests).Int("request_limit", c.requestLimit).
		Int("completed_empty", c.result.empty).Int("completed_non_empty", c.result.nonEmpty).
		Int("failed", c.result.failed).Int("partial", c.result.partial).
		Msg("startup attestation state collection ended; snapshots do not prove complete evidence")
}
