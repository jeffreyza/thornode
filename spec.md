# XMR Output Liquidity Management Spec

Status: proposal

## Summary

THORNode should treat Monero outputs as spendable liquidity inventory, not only as
an output-count problem. Monero locks newly created outputs for 10 blocks, so every
selected input temporarily removes its full value from the mature spendable pool
until the transaction's change output matures.

That means a consolidation transaction can make an otherwise solvent XMR vault
temporarily illiquid if it spends too much mature value at once. The first priority
is to make the current outbound and consolidation paths impossible to misuse. More
advanced output-shaping, such as splitting and rebucketting, should come later only
if metrics show it is needed.

## Goals

- Keep XMR available for user outbounds despite the 10-block lock.
- Ensure user outbounds only spend mature, unreserved outputs.
- Ensure final transaction construction enforces maturity and reservation rules
  locally, even if an upstream caller supplies bad inputs.
- Keep selected input count within THORChain consensus spent-ref limits.
- Make signing retries deterministic by reusing checkpointed inputs.
- Make reservation, broadcast, spent-ref, and restart behavior durable enough to
  avoid accidental input reuse.
- Make XMR consolidation conservative: management transactions must never consume
  hot liquidity needed for user outbounds.

## Non-Goals

- Eliminate Monero's 10-block output lock.
- Optimize fees ahead of outbound availability.
- Depend on hidden wallet behavior for correctness.
- Implement demand-derived splitting or rebucketting in the first pass.
- Replace the current sidecar output-state enum unless the existing model cannot
  express the needed safety invariants.
- Add a large configuration surface before operational metrics justify it.

## Current Risk

The current consolidation path is primarily output-count driven. It can look at a
vault with many spendable outputs and self-send most of the spendable balance while
leaving only a small reserve. That reduces output count, but the selected input
value becomes locked change for 10 blocks.

For XMR, the important quantity is not just:

```text
unlocked_balance
```

It is:

```text
mature_unreserved_balance_that_remains_after_selected_inputs
```

Any management transaction must be budgeted by selected input value, not by nominal
self-send amount or total vault balance.

## Effective Output State

The implementation does not need to store every state below as a separate enum.
It may keep a compact stored state such as `Unspent | Reserved | Spent` and derive
the rest from height, collision data, reservation data, and reconciliation state.

The system must still be able to distinguish these effective states:

- `immature`: known output with fewer than the required spendable confirmations.
- `mature_unreserved`: mature output available for user outbounds.
- `reserved`: output selected for one request and unavailable to other requests.
- `broadcast_pending`: output used by a built or broadcast transaction but not yet
  fully reconciled.
- `spent`: output conclusively consumed.
- `quarantined`: output excluded because collision, key-image, or reconciliation
  evidence is ambiguous.

Reduced liquidity is safer than ambiguous input reuse. If state is unclear, keep
the output reserved or quarantined until reconciliation proves it is safe.

## Maturity Invariant

An XMR output is spendable only after the configured Monero spendable-confirmation
threshold, currently 10 confirmations.

Every final candidate set must pass an invariant equivalent to:

```rust
is_available_for_spend_at(output, tip_height) =
  output.state == Unspent &&
  confirmations_at(output.height, tip_height) >= MIN_SPENDABLE_CONFIRMATIONS
```

This must be enforced inside the final transaction builder, not only at the scanner
API boundary. Checkpointed-input retries must also pass this builder-level check
unless the input is already bound to a previously built transaction for the same
request.

## Input Count Invariant

XMR outbounds publish spent-output refs to THORChain. Therefore selected input
count must never exceed the maximum number of XMR spent refs that THORChain
consensus can carry.

```text
max_inputs <= common.MaxXMRSpentOutputRefs
```

Operational knobs such as `MaxUTXOsToSpend` may lower the limit, but they must not
raise it above the consensus cap.

If a payment cannot be satisfied within the capped input count, signing should fail
with a clear insufficient-spendable-liquidity error rather than falling back to
immature, reserved, or unreportable inputs.

## Reservation Model

Before signing begins, selected inputs must be atomically reserved.

Minimum reservation record:

```text
reservation {
  request_id,
  vault_pubkey,
  selected_input_refs,
  selected_input_total,
  created_at_or_height,
  expires_at_or_height,
  tx_hash_candidate,
  status
}
```

Rules:

- No output may be reserved by two active request ids.
- A retry for the same request must reuse the same selected inputs.
- A checkpoint must include the selected input refs.
- Broadcast must bind the reservation to the tx hash.
- Observed spent refs must mark the selected outputs spent and complete the
  reservation.
- Expired reservations may be released only after daemon or mempool reconciliation
  says no known transaction spends those inputs.

The reservation store must be durable. In-memory cooldowns are acceptable for rate
limits, but not for correctness.

## Restart And Reconciliation

On sidecar startup and periodically while running:

1. Load tracked outputs and reservations.
2. Query daemon and mempool state for transactions created by this signer.
3. Apply observed spent refs from THORNode.
4. Mark confirmed consumed outputs as spent.
5. Release expired reservations only when no known transaction spends those inputs.
6. Quarantine outputs with conflicting key-image or collision evidence.
7. Recompute mature spendable inventory from canonical state.

If reconciliation cannot determine whether a reserved input was spent, keep it
reserved or quarantine it.

## Conservative Consolidation

Consolidation should be kept conservative until the sidecar supports explicit
selected-input management requests.

### Preconditions

A vault may attempt XMR consolidation only when all conditions hold:

```text
management_enabled == true
scanner_healthy == true
node_is_active == true
vault_is_active_asgard == true
no_outbound_signing_for_vault == true
no_mempool_outgoing_for_vault == true
pending_xmr_outbound_pressure_is_low == true
hot_liquidity > hot_target
```

Near churn, management should stop by default:

```text
churn_eta_blocks < MIN_SPENDABLE_CONFIRMATIONS + churn_safety_blocks
```

The exception is an explicit migration transaction.

### Hot Liquidity

Hot liquidity is mature, unreserved, non-quarantined XMR value intended for user
outbounds.

```text
hot_liquidity = sum(mature_unreserved_non_quarantined_outputs)
```

The first implementation should keep `hot_target` simple:

```text
hot_target =
  pending_xmr_outbound_value
  + configured_min_hot_reserve
  + fee_buffer
```

Demand projection and output bucket policy can be added later after metrics exist.

### Management Budget

Management must budget selected input value, not self-send amount.

```text
available_management_value = hot_liquidity - hot_target

rolling_locked_management_value =
  sum(selected_input_value of management txs still inside the 10-block lock window)

management_budget = min(
  available_management_value,
  max_management_lock_bps_per_window * hot_liquidity / 10000
    - rolling_locked_management_value
)
```

Skip management if `management_budget <= 0`.

The final builder or selected-input management path must reject management if:

```text
selected_input_total > management_budget
```

or if applying the transaction would leave:

```text
remaining_hot_liquidity < hot_target
```

### Current-Path Consolidation

If consolidation continues to use the normal signing path, it should remain
strictly conservative:

- Use a high reserve floor.
- Cap `max_inputs` tightly.
- Skip during outbound pressure.
- Skip near churn.
- Skip if another XMR outbound or consolidation is in progress.
- Record selected input value from the signing response and count it toward the
  rolling management lock window.

This is safer than trying to optimize output count while the builder still chooses
inputs internally.

### Future Explicit Management Builder

Long term, management should use an explicit sidecar request so THORNode or the
sidecar can choose exact excess inputs before building:

```text
BuildManagementTx {
  vault_pubkey,
  request_id,
  purpose: Consolidate,
  selected_input_refs,
  max_fee_atomic,
  tip_height,
}
```

The sidecar must validate:

- every selected input exists.
- every selected input is mature at `tip_height`.
- every selected input is unreserved or reserved by the same request.
- selected input total fits the management budget.
- the transaction is a self-send to the vault's XMR address.
- output count and fee stay inside configured limits.

## Splitting And Rebucketting

Splitting and rebucketting are useful but should not be first-pass work. They add
fee cost, extra self-send activity, and more tuning risk than conservative
consolidation.

Do not implement automatic splitting until metrics show a real problem such as:

- enough total mature value but too few usable mature chunks.
- repeated user outbounds spending oversized outputs and locking large change.
- output concentration causing outbound latency under normal demand.

When implemented, splitting should be behind a separate feature flag and should use
the same hot-liquidity and management-budget rules as consolidation.

## Balance And Metrics

The sidecar should report enough balance detail for THORNode and operators to
distinguish solvency from spendability:

```text
total_balance_atomic
spendable_balance_atomic
reserved_balance_atomic
immature_balance_atomic
management_locked_balance_atomic
hot_target_atomic
excess_liquidity_atomic
mature_output_count
reserved_output_count
management_tx_count
management_skipped_reason_count
outbound_waiting_for_maturity_count
selected_input_count
selected_input_value_atomic
selected_change_atomic
```

THORNode should surface insufficient spendable balance differently from insufficient
total balance, ideally including the expected maturity wait when known.

## Minimal Configuration

Start with a small set of controls:

```yaml
xmr:
  output_management:
    enabled: true
    min_spendable_confirmations: 10
    min_hot_reserve_atomic: 0
    max_management_lock_bps_per_window: 1000
    management_window_blocks: 10
    max_management_inputs: 8
    outbound_pressure_count: 8
    outbound_pressure_value_bps: 5000
    churn_safety_blocks: 5
```

Only add demand buckets, split targets, or p95 demand projections after the metrics
above prove they are needed.

## Rollout Plan

### Phase 1: Immediate Hardening

- Enforce builder-level maturity filtering with `is_available_for_spend_at`.
- Cap XMR `MaxInputs` to `common.MaxXMRSpentOutputRefs`.
- Ensure checkpointed retries cannot substitute different inputs.
- Ensure insufficient spendable balance errors distinguish locked liquidity from
  true insolvency.

### Phase 2: Reservation And Reconciliation Audit

- Verify reservation records survive restart.
- Verify broadcast binds reservations to tx hashes.
- Verify observed spent refs complete reservations.
- Verify expired reservations are released only after daemon or mempool
  reconciliation.
- Add tests for ambiguous restart state retaining or quarantining inputs.

### Phase 3: Conservative Consolidation

- Add hot-target checks.
- Add selected-input-value management budget.
- Add rolling 10-block management exposure tracking.
- Skip consolidation during outbound pressure and near churn.
- Keep consolidation disabled or conservative until these checks are in place.

### Phase 4: Observability

- Add balance breakdowns.
- Add output inventory metrics.
- Add selected-input and selected-change metrics.
- Log every management skip or execution with vault pubkey, height, hot liquidity,
  hot target, selected input count/value, and skip reason.

### Phase 5: Optional Output Shaping

- Consider smallest-sufficient outbound selection if oldest-first causes repeated
  oversized change locks.
- Consider splitting only if metrics show mature value is too concentrated in large
  outputs.
- Keep splitting behind a separate feature flag and the same management budget.

## Required Tests

- Final builder rejects or filters immature outputs even when provided directly.
- XMR `MaxInputs` cannot exceed `common.MaxXMRSpentOutputRefs`.
- Reserved outputs cannot be selected by a different request.
- Retry uses checkpointed inputs and does not substitute a different set.
- Broadcast binds reserved inputs to the tx hash.
- Observed spent refs mark selected outputs spent.
- Restart does not release ambiguous reservations.
- Consolidation skips when hot liquidity equals hot target.
- Consolidation selected input total cannot exceed management budget.
- Consolidation skips during outbound pressure.
- Consolidation skips near churn unless it is an explicit migration.

## Acceptance Criteria

The first production-ready version is acceptable when:

- User outbounds cannot spend immature, reserved-by-other-request, quarantined, or
  unreportable inputs.
- Selected input count cannot exceed THORChain's XMR spent-ref consensus cap.
- Retried signing requests reuse the same selected inputs.
- Restarting the signer cannot cause selected inputs to be forgotten and reused by a
  different request.
- Consolidation cannot lock more mature value than the configured management budget.
- Consolidation cannot reduce mature unreserved liquidity below the hot target.
- Operators can distinguish total balance from currently spendable balance.
