# ADR 27: Affiliate Revenue Share (REVSHARE)

## Changelog

- 2026-04-20: Design approved.
- 2026-04-27: ADR drafted.

## Status

Proposed

## Context

THORChain's existing affiliate system takes a cut of the **user's input
amount** at swap time via the memo `affiliate` / `affiliate_bps` fields.
There is no mechanism to share the **protocol's own income** (the RUNE
liquidity fees collected on swaps) with the affiliate that drove the
swap. This limits the business models affiliates can build on top of
THORChain — they can only earn by taxing their users more heavily,
even when they are demonstrably driving the protocol's swap revenue.

ADR 16 went the other direction: it added `ProtocolAffiliateFeeBasisPoints`
so the protocol could take a cut of the affiliate's fee. This ADR is the
complementary lever: the protocol gives back a configurable share of the
liquidity fees it earned from swaps an affiliate routed.

The dial must be:

- Per-thorname (different affiliates earn different shares).
- Operationally controlled (low-quorum mimir, churned out like other
  operational keys), so node operators can iterate without an upgrade.
- Capped (the protocol cannot give away more than half its own income on
  a per-affiliate basis under any setting).
- Paid out of protocol income, not user funds, so the user-side affiliate
  cut continues to behave exactly as it does today.

## Alternative Approaches

1. **Direct reserve → external address transfer per swap.** Rejected:
   bypasses `AffiliateCollector`, breaks preferred-asset conversion,
   and pays out at swap-record time rather than block-end (worse for
   accounting and event ordering).
2. **Single global REVSHARE bps for all affiliates.** Rejected: cannot
   express different commercial relationships, and a single dial is the
   wrong abstraction for what is fundamentally a per-affiliate setting.
3. **Pay out of `totalLiquidityFees` directly (mint / divert before
   reserve roll-up).** Rejected: existing accounting funnels all
   liquidity fees through the reserve as `systemIncome`. Diverting
   pre-roll-up complicates invariants; deducting "off the top" of
   `systemIncome` after roll-up keeps the reserve balanced and reuses
   the existing module-to-module transfer surface.
4. **Apply REVSHARE to add/withdraw, savers, lending, trade accounts,
   secured assets, and rune pool fees.** Rejected for v1: the goal is
   to share _swap_ fee revenue with the affiliate that brought the
   swap. Other surfaces have different fee semantics and can be added
   later without re-doing this design.
5. **Fall through the affiliate list (if A has no REVSHARE, try B).**
   Rejected: encourages spam-filling the affiliate list to harvest
   revenue, and conflicts with the intent that a swap has exactly one
   affiliate-of-record.

## Decision

Add a per-thorname revenue-share mechanism gated by an operational
mimir `REVSHARE-<thorname>` (basis points, capped at 5000). When set,
the protocol pays the named thorname `bps × attributed_liquidity_fee /
10_000` from the reserve to `AffiliateCollector` at end of block, and
reduces `systemIncome` by that total before any other split runs (dev
fund, burn, TCY, marketing, POL, bond/LP pendulum). Attribution is
strict-first-memo-affiliate, accumulates across streaming sub-swaps,
and resolves only to active thornames; raw addresses and expired
thornames are ignored.

A `rev_share` event is emitted at end of block for every thorname with
non-zero per-block accrual — including when REVSHARE is unset (bps=0,
payout=0) — so off-chain indexers can track gross attribution
independently of payout configuration.

## Detailed Design

The full design lives at
[`docs/superpowers/specs/2026-04-20-affiliate-revshare-design.md`](../superpowers/specs/2026-04-20-affiliate-revshare-design.md).
Summary follows.

### Constants

- `constants.RevShareMaxBps = 5000` — hard cap (50%) per thorname.
- `constants.MimirTemplateRevShareThorName = "REVSHARE-%s"`.

### Keeper state

New keyspace `AffiliateLiquidityFee/<thorname> → sdk.Uint` (RUNE, 1e8).
Per-block accumulator only; cleared at end of block after payouts.
Distinct from `AffiliateCollector` (which holds paid-out, cross-block
balances awaiting preferred-asset conversion for each THORName).

Collectors are keyed by THORName, so THORNames sharing an owner keep separate
balances. [Module migration 18 to 19](../affiliate-guide/thorname-guide.md#upgrade-from-shared-owner-collectors)
moves each legacy balance to the owner's THORName collector if they have exactly
one stored THORName whose spelling meets current registration rules, even if expired.
Otherwise, it pays the owner in RUNE. Module-owned legacy balances go to Reserve.
This changes collector storage and settlement; REVSHARE attribution and
rate calculation remain the same.

```go
AddAffiliateLiquidityFee(ctx, thorname, fee) error
GetAffiliateLiquidityFees(ctx) []AffiliateLiquidityFee
DeleteAffiliateLiquidityFees(ctx)
```

`GetAffiliateLiquidityFees` returns a slice of `(Thorname, Fee)` entries
sorted lexicographically by upper-cased `Thorname`. The lex order is
consensus-critical: end-of-block deduction iterates the slice and emits
events in slice order, so any non-deterministic order across nodes
would diverge state. The slice form (rather than `map[string]Uint` +
iterator) was chosen so the determinism contract is in the type itself
and callers cannot accidentally introduce `for ... range map`
iteration.

The function returns no `error`. Per-entry decode failures are logged
and skipped inside the iteration: end-of-block drain unconditionally
deletes the accumulator, so an aborted iteration would silently
discard every sibling entry's attribution for the block. Skip-and-
continue limits the blast radius to the single corrupted entry, and
removes the only failure mode the function could surface; bubbling an
error to `processRevShare` would be dead code.

Files: `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go` and
keeper interface addition in `x/thorchain/keeper/keeper.go`
(`AffiliateLiquidityFee` struct + `KeeperAffiliateLiquidityFees`
interface).

### Swap-path attribution

In `Swapper.Swap`, immediately after `AddToLiquidityFees`, attribute
the same fee to the swap memo's first affiliate when it resolves to an
active thorname (`ExpireBlockHeight > ctx.BlockHeight()`). Strict
first-affiliate, no fall-through. Applies to every fee-recording path
including streaming sub-swap iterations (the swap handler re-parses
the parent memo each tick, so each sub-swap reuses the parent's first
affiliate).

The REVSHARE mimir is **not** read at attribution time; that lookup
happens at payout, so attribution is resilient to operators
enabling/disabling REVSHARE mid-block.

### End-of-block deduction (`UpdateNetwork`)

In `x/thorchain/manager_network_current.go`, immediately after
`systemIncome := blockReward.Add(totalLiquidityFees)` and **before**
any other split:

1. Iterate the slice returned by
   `keeper.GetAffiliateLiquidityFees(ctx)` (lex-ordered by Thorname).
   For each entry:
   - Look up the thorname owner. If the thorname no longer exists,
     emit a `rev_share` event with empty owner and `payout = 0` (the
     event is the canonical record of attribution; payout is skipped
     and RUNE remains in the reserve).
   - Read `bps = mimir("REVSHARE-" + thorname)`. The
     `GetMimirWithRef` "unset" sentinel (`-1`) is normalised to `0` by
     the `NewEventRevShare` constructor so indexers never see a
     negative `bps` attribute.
   - If `bps > 0`: `payout = bps × accrued / 10_000`, transfer from
     reserve to `AffiliateCollector` for that THORName's collector entry, and
     accumulate into `totalRevShare`.
   - Always emit a `rev_share` event with `(thorname, owner, accrued,
bps, payout)`, even when `bps == 0` or the thorname is missing.
2. `keeper.DeleteAffiliateLiquidityFees(ctx)`.
3. `systemIncome = SafeSub(systemIncome, totalRevShare)`.
4. Existing splits run unchanged off the reduced `systemIncome`.

**Bookkeeping invariant.** The reserve has just received
`totalLiquidityFees` as part of the system-income roll-up. Rev-share
moves a subset of that out to `AffiliateCollector`; no RUNE is minted.
`Σ payouts ≤ 0.5 × Σ accrued ≤ 0.5 × totalLiquidityFees`, so
`systemIncome` cannot go negative.

### Mimir: validation and operational classification

Set-time validation (in the mimir handler and node-mimir vote
validation) rejects `REVSHARE-<X>` when `<X>` is not an existing
thorname or the value is outside `[0, 5000]`.

`REVSHARE` is added to `IsOperationalMimir`'s prefix list so the key
is clearable via operational vote and is purged from node-mimir votes
at end-of-block by `PurgeOperationalNodeMimirs`, matching the
HALT/PAUSE/etc. pattern.

**THORName character-set limitation.** `IsValidTHORName` accepts
`[a-zA-Z0-9+_-]`, but `MimirKeyRegex` (which gates every mimir key set)
accepts only `[a-zA-Z0-9-]`. The intersection is the only set of names
for which `REVSHARE-<thorname>` can be set: a thorname containing `_`
or `+` registers cleanly but its operator can never enable REVSHARE
because the targeted mimir key fails `mimirValidKey` validation.
Operators who want REVSHARE eligibility must register a thorname using
only letters, digits, and `-`.

### Event

```text
type: rev_share
attributes:
  thorname:    <name>
  owner:       <thor1...>
  accrued_fee: <RUNE liquidity fee attributed this block, 1e8>
  bps:         <REVSHARE mimir value applied; 0 if unset>
  payout:      <RUNE moved reserve → AffiliateCollector, 1e8; 0 if no payout>
```

Emitted for every accumulator entry, including `bps == 0 && payout ==
0`, so indexers can answer "gross liquidity fees attributable to
affiliate X" independently of REVSHARE configuration.

### Testing

- Unit tests for the keeper (round-trip), swap-path attribution
  (strict first affiliate, raw address skip, expired thorname skip,
  streaming roll-up), `UpdateNetwork` (systemIncome reduction, reserve
  → AffiliateCollector delta, missing-thorname skip, event shape), and
  mimir validation (cap, negative, unknown thorname).
- Regression suite `test/regression/suites/affiliate-revshare.yaml`:
  registers a thorname, sets REVSHARE, performs a swap with that
  thorname as first memo affiliate, asserts the `rev_share` event,
  the affiliate-collector balance delta, and the reserve balance
  reduction relative to baseline.

### Version gating

Both `UpdateNetwork` deduction and swap-handler attribution sit behind
a version bump via the standard `GetVersion` switch. Mimir validation
is additive and ships immediately. No state migration — the
accumulator starts empty each block.

## Consequences

### Positive

- Affiliates can earn a residual share of the protocol revenue they
  drive without raising user-side fees.
- Per-thorname dial enables differentiated commercial relationships.
- Off-chain indexers (Midgard, etc.) get a single `rev_share` event
  that captures both gross attribution and net payouts in one feed.
- No new module accounts, no migration, low blast radius.

### Negative

- Reduces the share of swap fees flowing into existing splits (dev
  fund, burn, TCY stake, marketing, POL, bond/LP pendulum) by exactly
  `Σ payouts` per block. Operationally controlled but worth
  monitoring.
- One additional iterator pass and N module-to-module transfers per
  block (one per thorname with attributed fees). Bounded by the number
  of distinct first-affiliates seen per block; expected to be small.
- An additional event emitted per thorname per block — minor indexer
  work. Volume is bounded by the count of distinct first-affiliates
  with non-zero accrual in the block (O(10s) in practice). The
  emission is unconditional (it fires even when bps=0 or the payout
  rounds to zero, so indexers never undercount); the bound is set by
  organic swap volume rather than by REVSHARE configuration, since
  the accumulator is populated by the swap path regardless of mimir
  state. A future revision could suppress the bps=0/payout=0 event
  after a grace period if the indexer cost proves material.

### Neutral

- The user-side affiliate cut path (`skimAffiliateFees`) is untouched.
- Add/withdraw, savers, lending, trade accounts, secured assets, and
  rune pool fees remain out of scope for v1.

## References

- Design spec: [`docs/superpowers/specs/2026-04-20-affiliate-revshare-design.md`](../superpowers/specs/2026-04-20-affiliate-revshare-design.md)
- Implementation plan: [`docs/superpowers/plans/2026-04-20-affiliate-revshare.md`](../superpowers/plans/2026-04-20-affiliate-revshare.md)
- ADR 16 (complementary protocol-take lever): [`adr-016-aff-rev-share.md`](./adr-016-aff-rev-share.md)
- MR: [thornode!4779](https://gitlab.com/thorchain/thornode/-/merge_requests/4779)
