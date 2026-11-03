---
order: 1
parent:
  order: false
---

# Architecture Decision Records (ADR)

This is a location to record all high-level architecture decisions in the THORChain project.

You can read more about the ADR concept in this [blog post](https://product.reverb.com/documenting-architecture-decisions-the-reverb-way-a3563bb24bd0#.78xhdix6t).

For contributors, please see the [PROCESS](PROCESS.md) page for instructions on managing an ADR's lifecycles.

An ADR should provide:

- Context on the relevant goals and the current state
- Proposed changes to achieve the goals
- Summary of pros and cons
- References
- Changelog

Note the distinction between an ADR and a spec. The ADR provides the context, intuition, reasoning, and
justification for a change in architecture, or for the architecture of something
new. The spec is much more compressed and streamlined summary of everything as
it stands today.

If recorded decisions turned out to be lacking, convene a discussion, record the new decisions here, and then modify the code to match.

Note the context/background should be written in the present tense.

## Table of Contents

### Implemented

- [002 - Remove Yggdrasil Vaults](./adr-002-removeyggvaults.md)
- [003 - Floored Outbound Fee](./adr-003-flooredoutboundfee.md)
- [004 - Keyshare Backups](./adr-004-keyshare-backups.md)
- [005 - Deprecate Impermanent Loss Protection](./adr-005-deprecate-ilp.md)
- [006 - Enable POL](./adr-006-enable-pol.md)
- [008 - Implement a Dynamic Outbound Fee Multiplier (DOFM)](./adr-008-implement-dynamic-outbound-fee-multiplier.md)
- [009 - Reserve Income and Fee Overhaul](./adr-009-reserve-income-fee-overhaul.md)
- [010 - Introduction of Streaming Swaps](./adr-010-streaming-swaps.md)
- [011 - THORFi Lending Feature](./adr-011-lending.md)
- [012 - Scale Lending](./adr-012-scale-lending.md)
- [013 - Synth Backstop](./adr-013-synth-backstop.md)
- [014 - Reduce Saver Yield Synth Target to Match POL Target](./adr-014-reduce-saver-yield-target.md)
- [015 - Refund Synth Positions After Ragnarok](./adr-015-refund-synth-positions-after-ragnarok.md)

### Accepted

- [002 - Remove Yggdrasil Vaults](./adr-002-removeyggvaults.md)
- [003 - Floored Outbound Fee](./adr-003-flooredoutboundfee.md)
- [004 - Keyshare Backups](./adr-004-keyshare-backups.md)
- [005 - Deprecate Impermanent Loss Protection](./adr-005-deprecate-ilp.md)
- [006 - Enable POL](./adr-006-enable-pol.md)
- [008 - Implement a Dynamic Outbound Fee Multiplier (DOFM)](./adr-008-implement-dynamic-outbound-fee-multiplier.md)
- [010 - Introduction of Streaming Swaps](./adr-010-streaming-swaps.md)
- [011 - THORFi Lending Feature](./adr-011-lending.md)
- [012 - Scale Lending](./adr-012-scale-lending.md)
- [013 - Synth Backstop](./adr-013-synth-backstop.md)
- [014 - Reduce Saver Yield Synth Target to Match POL Target](./adr-014-reduce-saver-yield-target.md)
- [015 - Refund Synth Positions After Ragnarok](./adr-015-refund-synth-positions-after-ragnarok.md)
- [016 - Affiliate Rev Share](./adr-016-aff-rev-share.md)
- [017 - Burn System Income Lever](./adr-017-burn-system-income-lever.md)
- [018 - Core Protocol Sustainability](./adr-018-core-protocol-sustainability.md)

### Deprecated

None

### Rejected

- [007 - Increase Fund Migration and Churn Interval](./adr-007-increase-fund-migration-and-churn-interval.md)

### Proposed

- [009 - Reserve Income and Fee Overhaul](./adr-009-reserve-income-fee-overhaul.md)
- [019 - AutoBond](./adr-019-auto-bond.md)
- [025 - Migrate Dev Fund Address](./adr-025-migrate-dev-fund-address.md)
- [027 - Affiliate Revenue Share (REVSHARE)](./adr-027-affiliate-revshare.md)
- [029 - Bond Yield Reward-Time Payouts](./adr-029-bond-yield-payouts.md)

### On Pause

- [001 - ThorChat](./adr-001-thorchat.md) _by request of author_
