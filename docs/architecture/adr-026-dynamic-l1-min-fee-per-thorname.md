# ADR 026: Dynamic L1 Min Swap Fee Per Thorname & Pair

## Changelog

- 2026-07-22: Status moved to Implemented — the feature is live on mainnet
  (`L1DynamicFeeEnabled = 1`).
- 2026-04-27: Switch the gradient signal from `fees_rune` to `fees_tor`
  (TOR-denominated, 1e8-precision USD value). Volume is also tracked in
  TOR. RUNE-denominated values are converted at the swap-completion
  hook via `DollarsPerRune`; when no TOR anchor pool is available
  (`DollarsPerRune == 0`), the credit is skipped rather than recorded
  as zero. Cancels the macro-noise term that nudged floors during
  RUNE/USD swings. Adds a query endpoint
  `GET /thorchain/dynamic_l1_fees_current` returning live (in-progress,
  not yet sealed) accumulators per `(thorname, pair)` for the current
  epoch.
- 2026-04-17: Expose the gradient window as the operational Mimir
  `L1DynamicFeeWindowEpochs` (default 3, clamped to `[1, 30]` at read
  time). Previously hardcoded. Larger windows trade response speed for
  noise rejection (√K).
- 2026-04-17: **Algorithm redesign.** Replace the share-based open-loop
  controller with a closed-loop gradient controller on fee revenue. The
  per-`(thorname, pair)` floor now moves each epoch based on whether the
  previous floor change increased or decreased the windowed-mean fee
  signal for that record. Drops the pair accumulator, pair history,
  and the fee/volume score weights; simplifies state schema and mimir
  surface. See the "Detailed Design" section for the new rule. Feature
  unshipped (`L1DynamicFeeEnabled` default still `0`) so the change is
  made in place without a v2 section.
- 2026-04-15: Initial draft.
- 2026-04-15: Post-review revisions: specify integer math via `common.GetSafeShare`;
  streaming sub-swaps credited per sub-swap (not aggregated); clarify multi-affiliate
  floor-selection uses the thorname with the largest affiliate-bps split; document
  record lifecycle and the worse-than-default ceiling implication.
- 2026-04-15: Tighten document: compress Context, drop the User Requirements section
  (redundant with Summary / Key definitions), collapse repeated "10 bps on mainnet"
  mentions to a single authoritative spot, and replace Disabled-state restatements
  with cross-references to the canonical subsection.
- 2026-04-15: Lower default `L1DynamicFeeMinFeesRune` from 100,000 to 10,000 RUNE.
  At current RUNE price (~$0.41) 100k implied ~$10M/day per `(thorname, pair)` which
  no affiliate clears today; 10k implies ~$1M/day and is achievable for the top
  affiliate+pair combinations while still filtering out small integrators.
- 2026-04-15: Replace the `L1DynamicFeeMinFeesRune` activation threshold with a
  governance-curated per-thorname Mimir whitelist (`DYNAMICFEE-WHITELIST-{thorname}`)
  with three states: `0` / absent (not enrolled), `1` (active), `2` (monitor —
  collect and compute but do not apply at swap time). Removal from whitelist
  immediately deletes all state for that thorname. No per-pair activity floor;
  any activity moves the floor. Simulation-test line removed per operator
  guidance.
- 2026-04-15: Initial `dynamic_bps` on record creation changes from `L1SlipMinBPS`
  to `L1DynamicFeeFloorBPS` (default 1 bps). Rationale: the share-based feedback
  rule's downward gradient from 10 → 9 → 8 bps may fall inside the 1% deadband
  every epoch and never fire, leaving the floor pinned at the default. Starting
  at the floor inverts the problem into an upward ratchet, which is the more
  reliable direction for this algorithm.

## Status

Implemented

## TL;DR

Replace the single network-wide `L1SlipMinBPS` with a per-`(thorname, pair)`
dynamic floor tuned by an on-chain **closed-loop revenue gradient**. Each
24h epoch, the controller asks a single question for each tracked record:
**did the previous floor change increase or decrease `fees_tor` for this
pair?** Fees up → continue in the same direction. Fees down → reverse.
Within `[1, 100]` bps bounds and a configurable percentage deadband.

Tracking is denominated in **TOR** (THORChain Oracle Rune — the on-chain
USD-pegged unit, 1e8-precision). The swap-completion hook converts
RUNE-denominated volume and fees to TOR via `DollarsPerRune` so the
controller signal is independent of RUNE/USD price movements.

Participation is governance-curated: a thorname is enrolled via the
operational Mimir `DYNAMICFEE-WHITELIST-{thorname}` with two on-states —
active and monitor. Scope: L1↔L1 swaps only; registered thornames only.
A master Mimir (`L1DynamicFeeEnabled`) cleanly disables the entire
mechanism. Goal: charge more where demand is inelastic, less where it is
competitive — directly optimizing the protocol's fee-revenue objective.

## Context

Today, the minimum L1 swap slip floor is governed by a single network-wide
Mimir, `L1SlipMinBPS`. A single value cannot optimize fee revenue across
the full matrix of affiliates and trading pairs, because different flow
has fundamentally different price sensitivity.

THORChain's own front-end (swap.thorchain.org), currently the protocol's
second-largest affiliate by volume, attracts users who transact on
THORChain largely _regardless_ of price execution — demand is sticky.
Aggregator integrations such as SwapKit route price-sensitive flow
constantly compared against other venues; a floor that is even slightly
too high on a competitive pair loses the flow entirely. The same split
exists across pairs: THORChain is the only meaningful venue for some
asset pairs and one of many for others.

With one network-wide floor, the protocol must pick a single point on
this landscape. Lowering it to compete on SwapKit's price-sensitive
pairs cedes revenue from sticky flow; raising it to capture more sticky
flow loses competitive pairs. The protocol cannot do both.

A per-`(thorname, pair)` floor, tuned by an on-chain feedback loop, lets
the protocol charge high fees where THORChain is effectively the only
venue and low fees where it is competing. The earlier revision of this
ADR tuned that floor by watching each thorname's **share** of its pair's
weighted `(fees, volume)` score — an open-loop proxy for the underlying
objective. That approach had three structural weaknesses:

1. **Open-loop attribution.** The controller reads a signal (share) and
   acts on a lever (floor) without ever asking whether the lever moved
   the signal. Share can shift because a competitor disappeared, a
   promo ran, a new chain listed, or macro moved — none of which the
   floor caused, but all of which the algorithm attributes to pricing.
2. **Solo-on-pair dead zone.** A thorname that owns a pair has
   `share ≈ 1.0` permanently; the algorithm never fires.
3. **Scalar collapse and phantom signals.** A fees-plus-volume score
   requires operators to pick weights without a principled basis, and
   multi-affiliate co-listing changes move B's share without B doing
   anything.

This revision replaces the share signal with a **closed-loop gradient on
`fees_tor`** (TOR-denominated, USD-equivalent), the metric the protocol
actually cares about. The controller measures its own effect each epoch
and adjusts — so all three weaknesses above go away. Tracking in TOR
also cancels RUNE/USD price noise that a `fees_rune` signal would
carry. The attribution, whitelist, bounds, and governance surface are
unchanged; only the signal and the state it requires change.

This ADR applies only to L1↔L1 swaps (no trade assets, no secured
assets, no synths) and only to registered thornames — raw affiliate
addresses continue to use the default floor.

## Decision

Introduce a per-`(thorname, pair)` dynamic minimum L1 slip fee tuned by
a closed-loop gradient controller on `fees_tor` (TOR-denominated,
1e8-precision USD value). Each epoch, for every tracked record, the
controller looks at the most recent bps change in the record's history,
compares the mean `fees_tor` before and after that change (windowed
across `L1DynamicFeeWindowEpochs` epochs on each side — default 3),
and moves the floor in whichever direction fees responded positively —
or reverses when fees responded negatively.

### Summary of the decision

- **Scope:** L1↔L1 swaps only (RUNE counts as a valid endpoint).
- **Pair identity:** non-RUNE endpoints, direction-agnostic, alphabetically
  normalized. Chain-distinct (`BTC.BTC` and `BSC.BTCB` are different pairs).
- **Attribution unit:** registered thornames that are explicitly
  whitelisted via Mimir (`DYNAMICFEE-WHITELIST-{thorname}`). Raw-address
  affiliates, unregistered thornames, and registered-but-unwhitelisted
  thornames receive `L1SlipMinBPS` and are not tracked.
- **Whitelist states:** `1` = active (dynamic floor applied at swap time),
  `2` = monitor (state computed but not applied — swaps use
  `L1SlipMinBPS`), `0` or absent = not enrolled.
- **Signal:** `fees_tor` (TOR-denominated, 1e8-precision USD value)
  accumulated per `(thorname, pair)` per epoch, compared across the most
  recent bps change via window averaging. RUNE-denominated values are
  converted at the swap-completion hook via `DollarsPerRune`. Skipped
  entirely when `DollarsPerRune == 0` (no TOR anchors).
- **Epoch:** block-count based, default ~14,400 blocks (≈24h at 6s blocks).
- **Movement:** ±1 bps per epoch within `[1, 100]` bps bounds, with a
  configurable percentage deadband on the fee delta (default 10%).
- **Objective:** directly maximize protocol fee revenue per record,
  measured in USD-equivalent terms (TOR). Volume matters only
  instrumentally — a floor change that reduces volume more than it
  increases per-swap fees shows up as a fees drop and the controller
  reverses.
- **Feature master switch:** operational Mimir.

## Detailed Design

### Key definitions

- **Pair**: The normalized tuple of the two non-RUNE assets on a swap,
  sorted alphabetically by their asset string (chain + symbol + optional
  id). If either side of the swap is RUNE, the pair is `ASSET↔RUNE`.
  Pairs are direction-agnostic.
- **Epoch**: A fixed number of consecutive blocks, governed by the
  operational Mimir `L1DynamicFeeEpochBlocks` (default 14,400). Epoch
  boundaries occur at `block_height % epoch_blocks == 0`.
- **Affiliate thorname**: A thorname listed in a swap memo's affiliate
  field(s). Raw-address affiliates are ignored by this mechanism.
- **Whitelist state**: The value of the operational Mimir
  `DYNAMICFEE-WHITELIST-{thorname}`. Values:
  - `1` = **active**: the thorname participates fully. Accumulators
    run, `dynamic_bps` is maintained each epoch, and the value is
    applied at swap-time.
  - `2` = **monitor**: the thorname participates _in data collection
    only_. Accumulators run and `dynamic_bps` is maintained as if
    active, but at swap-time the swap still uses `L1SlipMinBPS`.
    Intended for previewing a thorname's behaviour before promoting
    to active.
  - `0` or absent = **not enrolled**: no tracking, no state, no
    dynamic floor. Swaps use `L1SlipMinBPS`.
- **Dynamic bps**: The per-`(thorname, pair)` minimum slip floor, in
  basis points, maintained by the algorithm. Starts at
  `L1DynamicFeeFloorBPS` (default 1 bps) when a record is created,
  and is bounded by `L1DynamicFeeFloorBPS` and
  `L1DynamicFeeCeilingBPS`. The ceiling is not capped at
  `L1SlipMinBPS` — a whitelisted thorname whose fees degrade can
  experience a _worse_ floor than the default, by design.
- **Anchor**: The most-recent index in a record's history at which
  `bps_at_close` differs from its predecessor. The controller
  evaluates "did my last move help?" by comparing the fees window
  starting at the anchor to the fees window ending at the anchor.
- **Window (K)**: The number of epochs averaged on each side of the
  anchor when estimating the gradient. Governed by the operational
  Mimir `L1DynamicFeeWindowEpochs` (default 3) — short enough to react
  quickly, long enough to cut per-epoch fee variance by roughly √K.
  Clamped into `[1, MaxDynamicFeeHistory]` at read time.

### Record lifecycle

- A per-`(thorname, pair)` record is created lazily on the first
  in-scope swap carrying that thorname _while the thorname's whitelist
  state is `1` (active) or `2` (monitor)_. At creation, the
  accumulator is zero and `dynamic_bps` is initialized to
  `L1DynamicFeeFloorBPS`. Starting at the floor means the algorithm's
  first real move is always **up** — a probe into the pricing
  landscape to generate the first gradient datapoint.
- The first epoch with activity seals a history entry at
  `bps_at_close = floor`. The second consecutive epoch with activity
  probes up to `floor + step`; from that point onward there is a prior
  bps change to evaluate, and subsequent moves follow the gradient
  rule.
- When a thorname's whitelist state is changed to `0` or the key is
  removed, **all** state for that thorname is deleted immediately:
  every `(thorname, pair)` record and every in-progress accumulator.
  Re-adding the thorname to the whitelist is a cold start.
- While a thorname is in `2` (monitor), `dynamic_bps` is still
  computed and stored each epoch but is **not** applied at swap
  time. Promoting from `2` to `1` lets the current stored
  `dynamic_bps` take effect on the next swap, without re-learning.
- After 30 consecutive epochs with no activity (while still
  whitelisted), the record is pruned in full (see Epoch-boundary
  algorithm §3).

### State

**Per `(thorname, pair)` record:**

- `dynamic_bps: uint64` — current floor maintained by the algorithm.
  Only consulted at swap-time when the owning thorname is in
  whitelist state `1`.
- `last_active_epoch: uint64` — most recent epoch in which the
  thorname had any in-scope activity on this pair. Used for the
  30-epoch prune rule.
- `history: []EpochRecord` (up to 30 entries, one per epoch):
  - `epoch`, `volume_tor`, `fees_tor`, `bps_at_close`.
  - `volume_tor` is kept for observability and event emission; it
    is not read by the controller.
  - `bps_at_close` records what the floor was set to at the end of
    this epoch; the controller's "find the most recent change" walk
    inspects this field.

**In-progress (current epoch) accumulators:**

- Per `(thorname, pair)`: running `volume_tor`, `fees_tor` for the
  current epoch. Sealed and cleared at each epoch boundary.

Note: there is no per-pair record or per-pair accumulator. The
share-based controller in the previous revision required a pair-wide
score as its denominator; the revenue-gradient controller has no such
need and the state is gone.

### Mimir keys (all operational)

| Key                               | Default | Meaning                                                                                                               |
| --------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------- |
| `L1DynamicFeeEnabled`             | 0       | Master on/off. 0 disables the entire mechanism.                                                                       |
| `L1DynamicFeeEpochBlocks`         | 14400   | Epoch length in blocks (~24h at 6s).                                                                                  |
| `DYNAMICFEE-WHITELIST-{thorname}` | absent  | Per-thorname enrollment: `1` = active, `2` = monitor (collect only), `0`/absent = not enrolled.                       |
| `L1DynamicFeeFloorBPS`            | 1       | Lower bound of dynamic bps.                                                                                           |
| `L1DynamicFeeCeilingBPS`          | 20      | Upper bound of dynamic bps.                                                                                           |
| `L1DynamicFeeStepBPS`             | 1       | Magnitude of movement per epoch.                                                                                      |
| `L1DynamicFeeDeadbandBPS`         | 1000    | Percentage change in windowed `fees_tor` below which the controller holds (in bps: 1000 = 10%).                       |
| `L1DynamicFeeWindowEpochs`        | 3       | Epochs averaged on each side of the last bps change when measuring the gradient. Clamped into `[1, 30]` at read time. |

The whitelist key's `{thorname}` suffix is the registered thorname
name, normalized per thornode's existing Mimir-key conventions
(typically upper-cased). The thorname must be a currently-registered
thorname; an entry for a non-existent thorname has no effect and is
ignored.

**Hardcoded:**

- 30-epoch prune horizon (records for a `(thorname, pair)` with no
  activity for 30 consecutive epochs are deleted). This also bounds
  the `L1DynamicFeeWindowEpochs` clamp at read time.

The window size (`L1DynamicFeeWindowEpochs`, default 3) trades noise
rejection for response speed: a larger window cuts per-epoch variance
by roughly √K but takes more epochs to confirm a trend, and eats into
the fixed `MaxDynamicFeeHistory = 30` budget (the window must leave
room for before + after samples). Values outside `[1, 30]` are
clamped at read time so misconfiguration cannot crash the
controller.

### Attribution rules (per swap)

When an L1↔L1 swap completes (or at the moment slip fees are recorded),
attribute to the in-progress epoch counters as follows:

1. **In-scope check.** Both swap endpoints must be L1 assets (RUNE is
   eligible). Trade assets, secured assets, and synths disqualify the
   swap from tracking. Out-of-scope swaps are ignored.
2. **Pair normalization.** Compute the pair key from the two non-RUNE
   endpoints (or `ASSET↔RUNE` if one side is RUNE), sorted.
3. **TOR conversion.** Convert the swap's `volume_rune` and `fees_rune`
   to TOR via `DollarsPerRune` (`value_tor = value_rune *
DollarsPerRune / 1e8`). When `DollarsPerRune == 0` (no TOR anchor
   pool, or all halted), skip the credit entirely — recording zeros
   would drag the windowed-mean signal toward zero and provoke
   spurious controller reversals.
4. **Thorname credit.** For each affiliate entry in the memo:
   - If the entry is a registered thorname whose whitelist state is
     `1` (active) or `2` (monitor), add the swap's full `volume_tor`
     and `fees_tor` to the `(thorname, pair)` current-epoch
     accumulator. (Multi-affiliate memos credit each eligible listed
     thorname in full — no proportional split.)
   - Otherwise (raw address, unregistered thorname, or a registered
     thorname with whitelist state `0` / absent), no thorname-level
     credit is recorded.
5. **Streaming swaps.** Each sub-swap is credited independently as it
   completes, into the epoch in which its completion block lands. A
   streaming swap that spans an epoch boundary is therefore split
   naturally: sub-swaps before the boundary land in epoch N, sub-swaps
   after land in epoch N+1. This increases the weight of high-sub-swap
   streams but keeps epoch-boundary accounting clean.

### Fee floor selection (at slip fee calculation)

For an in-scope L1 swap, at the point slip fees are computed:

1. If `L1DynamicFeeEnabled != 1` → use `L1SlipMinBPS`. See
   [Disabled state](#disabled-state) for the full contract.
2. If the swap has no affiliate, or the affiliate is a raw address, or
   the affiliate thorname is not registered → use `L1SlipMinBPS`.
3. Look up the affiliate thorname's whitelist state:
   - State `1` (active): if a `(thorname, pair)` record exists →
     use `record.dynamic_bps`. Otherwise → use `L1SlipMinBPS` (the
     first in-scope swap for this pair creates the record but the
     dynamic floor is not applied until after the first completed
     epoch, per Record lifecycle).
   - State `2` (monitor): use `L1SlipMinBPS`. Attribution still
     runs (step 3 of Attribution rules) so the thorname's
     `dynamic_bps` continues to be computed each epoch, but it is
     **not** applied to the swap.
   - State `0` or absent: use `L1SlipMinBPS`.
4. For multi-affiliate memos, the thorname with the **largest
   affiliate-bps split** is the one whose record applies (ties broken
   by order of appearance). This prevents a free-rider exploit where
   an integrator co-lists an active thorname as a token secondary to
   inherit its discount — to benefit from a thorname's dynamic bps,
   you must give that thorname the largest share of the affiliate
   revenue. If the largest-split thorname is in state `2` or
   unenrolled, step 3 falls through to `L1SlipMinBPS` for this
   swap — secondary listed thornames do not "rescue" the floor.
   Attribution credit to all eligible listed thornames is unchanged
   (see Attribution rules §3).

### Epoch-boundary algorithm

At block heights where `block_height % L1DynamicFeeEpochBlocks == 0`:

1. **Cleanup removed thornames.** For every thorname with records
   whose `DYNAMICFEE-WHITELIST-{thorname}` is now `0` / absent, delete
   all dynamic-fee state for that thorname.

2. **Seal accumulators.** Iterate all `(thorname, pair)` accumulators
   from this epoch. For each:

   - `fees_now = accumulator.fees_tor`.
   - Load the record; append a new history entry
     `{epoch, volume_tor=accumulator.volume_tor,
fees_tor=fees_now, bps_at_close=TBD}`.
   - Compute the next `bps` via the gradient rule below.
   - Write `bps_at_close = new_bps` into the just-appended entry.
   - Set `record.dynamic_bps = new_bps`,
     `record.last_active_epoch = current_epoch`.
   - If `new_bps != old_bps`, emit `dynamic_l1_fee_update`.
   - Delete the accumulator.

3. **Prune.** For every record where
   `current_epoch - last_active_epoch >= 30`, delete the record in
   full.

**Gradient rule** (concrete pseudo-code for step 2):

```text
changeIdx, hasChange = last index j in history[:-1] where
                       history[j].bps_at_close != history[j-1].bps_at_close

if not hasChange:
    # Cold start — no prior move to evaluate. Probe upward by one step.
    return "cold_start_probe", clamp(old_bps + step, floor, ceiling)

dir       = sign(history[changeIdx].bps_at_close - history[changeIdx-1].bps_at_close)
feesBefore = mean(history[changeIdx-K : changeIdx].fees_tor)    # up to K epochs before change
feesAfter  = mean(history[changeIdx : ].fees_tor)               # up to K epochs since change, incl. this one

if feesBefore == 0:
    # No baseline to compare against; hold.
    return "hold", old_bps

deltaPctBps = abs(feesAfter - feesBefore) * 10000 / feesBefore  # via GetSafeShare

if deltaPctBps < deadband:
    return "hold", old_bps

feesIncreased = feesAfter > feesBefore
if dir == +1 and feesIncreased:  return "continue_up",   clamp(old_bps + step, floor, ceiling)
if dir == +1 and not feesIncreased: return "reverse_down", clamp(old_bps - step, floor, ceiling)
if dir == -1 and feesIncreased:  return "continue_down", clamp(old_bps - step, floor, ceiling)
if dir == -1 and not feesIncreased: return "reverse_up",   clamp(old_bps + step, floor, ceiling)
```

The window averaging handles the case where the most recent bps change
is several epochs behind the current epoch: `feesBefore` draws up to 3
epochs ending just before the change, `feesAfter` draws up to 3 of the
most recent epochs starting at the change (including the one just
appended). If the record has fewer than K epochs on one side, the
window shortens rather than dropping the comparison — the controller
works with as little as one sample on each side, and noise is
controlled by the deadband.

<a id="disabled-state"></a>

### Disabled state (`L1DynamicFeeEnabled != 1`)

When the master Mimir is not `1`, the feature is completely inert:
every L1 swap uses `L1SlipMinBPS`, no attribution runs, no epoch
processing runs, and no events are emitted. Existing records
remain in state but are frozen — not consulted, updated, or pruned.

Re-enabling resumes attribution and epoch processing from that
point. Prior records persist but their history is stale; the
algorithm's cold-start rule (no prior bps change → probe up) handles
this naturally, so the first movement after re-enabling requires two
consecutive epochs of activity under the re-enabled feature.

### Events

`dynamic_l1_fee_update` — emitted per `(thorname, pair)` on each bps
change at an epoch boundary:

```json
{
  "thorname":      "...",
  "pair":          "BTC.BTC|ETH.ETH",
  "epoch":         12345,
  "old_bps":       47,
  "new_bps":       48,
  "fees_before":   "1000000000",
  "fees_after":    "1250000000",
  "delta_pct_bps": "2500",
  "reason":        "cold_start_probe" | "continue_up" | "continue_down" |
                   "reverse_up" | "reverse_down"
}
```

### REST endpoints

- `GET /thorchain/dynamic_l1_fees`
  Returns all currently-tracked `(thorname, pair)` records with their
  current `dynamic_bps`, whitelist state (`1` or `2`),
  `last_active_epoch`, and the latest epoch's `fees_tor`
  (1e8-precision USD value).

- `GET /thorchain/dynamic_l1_fees/{thorname}`
  Returns the thorname's whitelist state and all pairs tracked for
  it, each with up to 30 epochs of history
  (`volume_tor`, `fees_tor`, `bps_at_close`).

- `GET /thorchain/dynamic_l1_fees_current`
  Returns the in-progress (current epoch, not yet sealed) accumulators
  for every `(thorname, pair)` plus the current epoch number. Each
  entry has `volume_tor` and `fees_tor` running totals so far this
  epoch, before they are sealed into the next history record at the
  next epoch boundary.

### Integer math and determinism

All arithmetic for the gradient, window mean, and deadband evaluation
uses `cosmos.Uint` (big.Int-backed, consensus-safe). No floating-point
is used anywhere in the algorithm.

- **Window mean**: straight integer sum divided by the window size
  (an integer between 1 and 3 in practice).
- **|delta %|**: `deltaPctBps = common.GetSafeShare(|feesAfter - feesBefore|, feesBefore, NewUint(10000))`.
  `GetSafeShare` returns `0` if either argument is zero, which
  correctly handles the "no baseline" case (treated as hold).
- **Sign of delta**: tracked separately by comparing `feesAfter` and
  `feesBefore` with `GT/LT` on `cosmos.Uint`.
- **Deadband check**: `deltaPctBps.LT(L1DynamicFeeDeadbandBPS)` → hold.
  A mimir value of `0` is a valid, supported configuration: it disables
  the deadband so that every non-zero fee delta moves the floor.

### Efficiency considerations

- State per `(thorname, pair)`: up to 30 epoch records (~60 bytes each)
  plus a small header. A few kilobytes per tracked pair.
- Epoch-boundary work is O(tracked records), bounded by the
  whitelist size + 30-epoch prune. In practice, tens to low
  hundreds of records.
- Gradient calculation per record walks at most 30 history entries
  backwards plus two small means — O(history) ≈ 60 integer
  additions.
- Attribution on each swap is O(number of affiliates in memo), which
  is small (typically 1, occasionally 2–3).

### Observability / monitoring

- Per-change events (above) let indexers reconstruct history and build
  dashboards.
- REST endpoints support operational monitoring and affiliate
  introspection.
- Recommended external metrics: number of whitelisted thornames (in
  each state), number of active `(thorname, pair)` records,
  distribution of `dynamic_bps` values, count of records pinned at
  floor/ceiling, reason-code distribution over the last 30 epochs
  (a healthy system reverses occasionally rather than drifting
  monotonically to a bound).

### Security considerations

- **Sybil resistance** relies on thorname registration cost. Raw
  addresses cannot earn dynamic bps.
- **Wash trading.** The closed-loop controller is harder to game than
  the share-based design because the only lever the affiliate can
  pull — paying artificial fees — directly counts toward the objective
  the controller optimizes. A wash-trading affiliate who continues to
  generate fees at a higher floor is genuinely profitable for the
  protocol; if they stop the moment the floor gets too high, the
  controller sees fees drop and reverses. Additional mitigations:
  (a) **governance-curated whitelisting** — a thorname cannot
  participate at all without being explicitly added via Mimir,
  (b) **monitor mode** (`2`) which lets operators observe a thorname's
  behaviour before promoting to active, (c) slow ±1 bps step,
  (d) the percentage deadband, and (e) the configurable window
  averaging (`L1DynamicFeeWindowEpochs`) which smooths single-epoch
  spikes.
- **Floor and ceiling** guarantee the mechanism cannot push fees
  outside `[1, 100]` bps regardless of signal behavior.
- **Master switch** (`L1DynamicFeeEnabled = 0`) is an immediate,
  total kill. See [Disabled state](#disabled-state).
- **Determinism:** all arithmetic uses integer math on RUNE-denominated
  values and bps. No floating point.

### Testing

- Unit tests on the gradient-decision helper (`decideNextBps`)
  covering every reason branch: cold-start probe, continue_up,
  continue_down, reverse_up, reverse_down, hold (in deadband), and
  the "no baseline" edge where `fees_before` is zero.
- Unit tests on attribution: L1-only filtering, pair normalization,
  RUNE-endpoint handling, multi-affiliate credit, streaming sub-swap
  crediting, raw-address / unregistered-thorname exclusion,
  whitelist state gating (state `0`/absent, `1`, `2`).
- Unit tests on epoch rollover: sealing into history, floor/ceiling
  clamping at either bound, pruning at 30 epochs, cold start,
  whitelist removal deleting state.
- Unit test confirming the solo-on-pair regression: a thorname that
  is the only affiliate active on its pair still sees its floor
  move, unlike the previous share-based design.
- Unit tests on fee-floor selection: whitelist state `1` applies
  `dynamic_bps`, state `2` uses `L1SlipMinBPS` but still collects,
  largest-bps-split selection in multi-affiliate memos.
- Regression tests covering a multi-epoch scenario where a thorname
  is added to the whitelist as `2` (monitor), observed, promoted to
  `1` (active), earns a bps reduction, is demoted back to `2`, and
  finally removed.

### Rollout

1. Deploy with `L1DynamicFeeEnabled = 0` and no whitelist entries.
   No behavior change.
2. Tune operational Mimir defaults if needed in stagenet.
3. Flip `L1DynamicFeeEnabled = 1`. Still no effect: no thornames
   are whitelisted.
4. Add a small set of thornames to the whitelist in state `2`
   (monitor). Observe their computed `dynamic_bps` via events and
   REST over a few epochs to validate the algorithm against real
   flow.
5. Promote selected thornames to state `1` (active). The
   monitor-mode history is preserved, so the first floor decision
   under active mode can draw on epochs already observed.
6. Adjust bounds / step / deadband as needed based on observed
   behavior.

## Consequences

### Positive

- **Directly optimizes the stated objective.** The controller measures
  `fees_tor` (USD-equivalent fees) and moves the lever that affects
  `fees_tor` — there is no proxy between the signal and the goal.
  "Higher volume" shows up automatically: when a floor is too high on
  a price-sensitive pair, volume drops, fees drop, and the controller
  reverses.
- **Closed-loop self-correction.** The algorithm asks "did my last
  move help?" and reverses when it didn't. Exogenous noise that
  would have misled the open-loop share design — a competitor
  appearing, a promo ending, a macro move — still causes one wrong
  step, but the next epoch's evaluation catches it and reverses.
- **Macro-noise-resistant signal.** Tracking in TOR (USD-equivalent
  via `DollarsPerRune`) cancels the dominant macro noise term that a
  pure `fees_rune` signal would carry. RUNE/USD price moves no longer
  push the floor in spurious directions.
- **Solo-on-pair works.** A thorname that owns a pair is no longer in
  a dead zone; their `fees_tor` is a live signal regardless of who
  else trades the pair.
- **No scalar collapse.** The signal is `fees_tor`; there are no
  fee/volume weights to tune by taste.
- **No phantom multi-affiliate signal.** Each thorname's signal is
  its own `fees_tor`, not a share that shifts when co-listed
  thornames change behavior.
- **Simpler state.** No per-pair record, no per-pair accumulator,
  fewer mimirs, smaller proto surface.
- **Monitor mode** (whitelist value `2`) remains a cheap, reversible
  validation step.
- **Failure mode is graceful**: the master Mimir is an immediate,
  total kill switch (see [Disabled state](#disabled-state)).

### Negative

- **Tor-denominated signal still moves with total network activity.**
  Tracking in `fees_tor` cancels the dominant macro noise term —
  RUNE/USD price moves no longer contaminate the gradient. What
  remains is genuine activity-driven variance (a quiet trading day
  shows up as lower `fees_tor` in absolute terms). Mitigated by (a)
  the configurable window averaging (`L1DynamicFeeWindowEpochs`,
  default 3) and (b) the 10%-default deadband. ±1 bps/epoch × clamp
  box means any mis-step cost is bounded regardless.
- **TOR oracle dependency.** When the TOR anchor pools are unavailable
  (no whitelisted USD anchors, or all halted), `DollarsPerRune`
  returns zero and dynamic-fee crediting is skipped for affected
  swaps. Existing dynamic_bps values are preserved until the next
  epoch boundary, but no new gradient signal accrues during the
  outage. Re-enabling anchors resumes accrual on the next swap.
- **Adds state and per-epoch computation.** Bounded by the whitelist
  size and the 30-epoch prune horizon, but non-zero.
- **Adds attribution logic to the swap hot path** (whitelist state
  lookup, accumulator updates, affiliate parsing). Small cost, but
  not free.
- **Curation burden.** Participation requires explicit governance
  action; new affiliates that grow organically don't automatically
  benefit — they must be added. This is by design (operators have
  wanted this control) but it is work.
- **Streaming swaps crediting each sub-swap independently** means a
  stream with many sub-swaps carries proportionally more weight than
  a single one-shot swap of equal total size. Predictable and splits
  naturally across epoch boundaries, but it does amplify a single
  user's impact on a thorname's signal.
- **Ceiling can exceed the network default.** The dynamic ceiling
  (`L1DynamicFeeCeilingBPS`, default 20) is not capped at
  `L1SlipMinBPS`. A whitelisted thorname whose fees degrade can
  drift _above_ `L1SlipMinBPS` — meaning swaps attributed to them
  incur a stricter floor than non-affiliated swaps. This is by
  design: it keeps the feedback signal symmetric. A thorname can
  escape the penalty by being demoted to monitor mode, removed from
  the whitelist, or by the controller finding a better local
  optimum in subsequent epochs.

### Neutral

- Raw-address affiliates keep getting `L1SlipMinBPS`. This is by
  design but means integrators who never adopted thornames see no
  change either positive or negative.
- Newly-whitelisted thornames start with `dynamic_bps =
L1DynamicFeeFloorBPS` on record creation — that is, at the most
  discounted possible floor. The first real move is always a probe
  upward, and the controller takes over from the second evaluated
  move onward. Changes to `L1SlipMinBPS` do not affect the dynamic
  value for any whitelisted thorname.

## References

- [Mimir reference documentation](../mimir.md)
- [ADR 016: Affiliate revenue share](./adr-016-aff-rev-share.md)
- [ADR 010: Streaming swaps](./adr-010-streaming-swaps.md)
