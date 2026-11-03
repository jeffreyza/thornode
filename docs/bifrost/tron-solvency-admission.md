# TRON solvency voter admission

TRON solvency reports can disagree because balance reads are not pinned to the
reported height. Ordinary observation slash points are disabled for TRON, while
the legacy exact-duplicate penalty remains enabled. To limit the creation of
distinct persistent voters without penalizing that disagreement, both the legacy
and quorum handlers share a per-validator admission budget.

## Policy

- Each validator can fund **32 new TRON solvency voters per fixed window of
  10 THORChain blocks**. The window is `ctx.BlockHeight() / 10`: heights 100–109
  share an allowance, which resets at 110.
- The allowance spans all reported TRON heights, vaults, balances and both message
  paths. The report's height and the relayer's identity cannot reset it.
- An existing voter's additional signatures do not consume allowance, even when
  the signer has exhausted their creation budget. Agreement thresholds and the
  existing duplicate-handling rules are preserved.
- For a new quorum voter, attestations are first verified against the active set.
  The first verified signer with remaining allowance, in ascending address-byte
  order, funds creation. Only one allowance is charged. Invalid attestations
  cannot spend someone else's allowance, and no voter is written if all fail
  verification.
- A structurally valid batch with no verified active attestations succeeds as a
  no-op, allowing the ebifrost post-handler to discard those attestations from its
  queue. This covers signatures from validators that have churned out. Existing
  voters and quota records are unchanged.
- A payload that cannot be serialized for signature verification is logged and
  acknowledged without changing voters or quotas, so ebifrost can discard it
  instead of retrying the same unusable payload.
- Exhaustion returns an error without creating a voter or adding slash points.
  Failed quorum transactions are not confirmed by the ebifrost post-handler;
  they remain in the proposal cache for retry, subject to its existing TTL.
- The charge is written only after successful processing, alongside the voter in
  the transaction cache. Transaction rollback discards both. One 16-byte record
  per chain and participating validator stores the window and used count; it is
  overwritten rather than adding a key every window. Ordinary node restarts
  preserve it.

## Limit choice and scope

The initial limit of 32 provides burst headroom for reporting across active and
retiring vaults. As a sizing example, three active and three retiring vaults, each reporting
three times in a window, can create 18 distinct records from one validator.
Thirty-two allows that case even when none of its payloads match another node's.
This is a capacity example, not a guarantee about the live vault count or workload;
larger honest bursts can exhaust the allowance and delay admission.

Quota applicability, window length and creation limit are defined by
`Chain.SolvencyVoterQuota()` in `common`; zero values disable the quota.
The admission handler uses that policy without branching on a specific chain.
The keeper accepts a chain and validator address, with independent counters under
the generic `solvency_voter_quota/` prefix. Chain-name casing shares the same key.
These values are consensus rules, not local Bifrost settings or Mimir overrides.
Changing them requires a coordinated release. Monitor active/retiring vault counts
and reporting bursts when reviewing this limit for deployment.

This bounds **record creation rate**, not lifetime database size, record size, or
network/signature-verification cost. Sustained senders can still create records
at the allowed rate, and a coalition has the sum of its members' allowances.
Fixed-window boundaries permit two windows' allowances in a short burst. There
is no voter pruning or additional report-height restriction in this change.

## Verification

The admission tests exercise shared-budget and field-changing attempts, independent
validators, existing-voter consensus and duplicates, invalid/inactive signatures,
deterministic quorum funding, transaction rollback, quota-exhaustion retries, and
discarding unusable quorum batches without changing voters or quotas.
Keeper tests cover committed-store reload, malformed records, independent chains
and validators, chain-name casing and constant key count across windows. Existing
non-TRON solvency tests retain their accounting.
