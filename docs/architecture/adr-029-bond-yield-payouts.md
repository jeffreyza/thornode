# ADR 029: Bond Yield Reward-Time Payouts

## Changelog

- 2026-05-16: Initial draft.
- 2026-05-16: Clarified Bond Yield event accounting semantics.

## Status

Proposed

## TL;DR

Bond Yield lets a bond provider route a configured basis-point share of future
validator bond rewards to a payout destination at reward distribution time. The
remaining provider reward is added to slashable bond in the same distribution
cycle.

A provider with no Bond Yield configuration, or with `bps = 0`, receives the
standard full-rebond treatment. Bond Yield stores payout configuration only; it
does not introduce a separate yield balance or settlement transaction.

## Context

Validator rewards allocated to a node are shared between the node operator and
the node's bond providers. Provider rewards normally increase slashable bond,
which compounds future reward weight and strengthens the security backing the
node.

Some bond providers want predictable realized income from rewards while keeping
their existing bonded security intact. Bond Yield supports that requirement by
splitting only the incoming provider reward stream: the configured share is paid
before it is added to bond, and the remaining share is immediately rebonded.

This keeps provider payout preferences separate from bond accounting. Existing
slashable bond is not reduced by a payout, and the bond module does not need a
separate ledger for unsettled yield.

## Decision

Introduce `MsgBondYield` as the configuration message for automatic reward-time
payouts. The message has one `bps` field and an optional `payout_target`.

Reward distribution applies the provider configuration as follows:

- When Bond Yield is disabled, when the provider is not configured, or when
  `bps = 0`, the provider reward is fully rebonded.
- When `bps > 0`, that basis-point share of the provider reward is paid out
  immediately through the configured payout route.
- The remaining provider reward is rebonded immediately.

Bond Yield settlement occurs only during reward distribution. Feature state
contains payout configuration, not provider yield amounts awaiting settlement by
a separate transaction.

## Detailed Design

### Mimir Controls

- `BondYieldEnabled`: Economic Mimir that enables automatic reward-time
  bond-yield payouts. When disabled, provider rewards are fully rebonded.

### Provider State

`BondProvider` stores:

- `bond_address`: the provider account.
- `bond`: slashable provider bond.
- `bps`: the share of future provider rewards to pay out automatically. `0`
  means no reward split.
- `payout_target`: either empty, a THORChain address, or a THORName.

### Reward Distribution

Validator bond rewards are processed before churn status changes. The node
operator fee is taken from the reward first. The remaining provider reward is
allocated by provider bond share.

When Bond Yield is active for a provider, its reward share is split:

- `payout_amount = provider_reward * bps / constants.MaxBasisPts`
- `rebonded_amount = provider_reward - payout_amount`

Only `rebonded_amount` increases `NodeAccount.Bond`. `payout_amount` leaves the
bond module through the configured payout route. If payout cannot be completed,
the payout amount is rebonded in the same distribution cycle. In that fallback
case, the emitted `bond_yield` event reports the final settled amounts:
`payout_amount = 0` and `rebonded_amount` includes the fallback amount.

When Bond Yield is inactive for a provider, the full provider reward is
rebonded and no Bond Yield payout event is emitted.

### Churn and Bond Provider Operations

Bond Yield is settled only during reward distribution, not during churn. It does
not change existing churn, bonding, unbonding, or provider removal processing.

### Message and Memo Behavior

`MsgBondYield` configures future reward-time payouts:

- `bps`: the future reward share to pay out automatically, from `0` to
  `constants.MaxBasisPts`.
- `payout_target`: the optional payout route. Empty means native RUNE to the bond
  provider.

`bps = 0` disables reward-time payouts for that provider and clears the stored
`payout_target`. A target supplied with `bps = 0` is not stored.

Memo form:

- `bondyield:<node_address>:<bps>[:<payout_target>]`

### Payouts

An empty payout target pays RUNE to the provider. A THORChain address pays RUNE
to that address. Any other payout target is interpreted as a THORName.

The THORName must exist and be owned by the bond provider. Ownership is checked
when the payout target is configured and again when a payout is attempted.

If the THORName has no preferred asset, the payout is sent as RUNE to its
THORChain alias when one is set, or to the owner otherwise.

If the THORName has a preferred asset, Bond Yield credits the affiliate
collector for that THORName. Preferred-asset swap execution then follows
the existing affiliate collector rules and may be skipped when the preferred
asset route is unavailable.

### Events

Each successful Bond Yield payout emits the existing `bond` event with
`bond_returned` for the RUNE leaving the bond module. Reward distribution also
emits a `bond_yield` event with the provider reward share, final payout amount,
rebonded amount, configured basis points, and payout target. Indexers should use
`bond_yield` for yield-specific accounting and treat the paired `bond` event as
the underlying transfer event to avoid double-counting the same payout.

### Invariants

Bond Yield does not change the existing bond invariant:

```text
bond module RUNE = node bond + pending bond rewards
```

Payout configuration is not part of the invariant.

## Consequences

### Positive

- Unconfigured providers keep standard full-rebond behavior.
- Providers can realize part of each reward without reducing existing slashable
  bond.
- The non-paid reward share compounds immediately into bond and contributes to
  future reward weight, subject to the normal bond hard cap.
- If a payout cannot be completed, that reward share is rebonded instead.
- THORName payouts can use either direct RUNE aliases or preferred-asset
  affiliate collector behavior.

### Negative

- Yield can be received only at reward distribution time.
- Non-RUNE payout depends on pool availability and payout configuration.

### Neutral

- Governance controls feature enablement.
