# ADR 025: Migrate Dev Fund Address

## Changelog

- 31/03/2026: Created

## Status

Proposed

## Context

[ADR 018](./adr-018-core-protocol-sustainability.md) established the Dev Fund mechanism, allocating 5% of System Income (`DevFundSystemIncomeBps = 500`) to a designated address controlled by the Core Protocol Team. This funding supports protocol engineering, security, maintenance, and feature development.

ADR 018 includes a 3-year Sunset Clause requiring a new ADR to refresh the custodian address and team. The current Dev Fund address (`thor1d8c0wv4y72kmlytegjmgx825xwumt9qt5xe07k`) is controlled by 9R. As part of the periodic refresh, this ADR proposes migrating custody to a new team via a new address.

## Decision

Migrate the Dev Fund address from the current 9R-controlled address to a new 2-of-3 multisig address controlled by:

- **sonOfOdin** — Core protocol contributor
- **Marcel** (THORWallet) — Ecosystem contributor
- **Aaluxx** (Maya Protocol) — Ecosystem contributor

The new address is: `thor1sgnuptp32y2fht3258vlx68mq8wsk2uz4wrshu`

Funds will continue to be used to pay developers and cover other operational costs related to core protocol development and maintenance.

The 3-year Sunset Clause resets from the date this ADR is accepted. A new ADR will be required to refresh the address and custodian team after the 3-year period.

The 5% System Income allocation rate (`DevFundSystemIncomeBps = 500`) remains unchanged.

## Detailed Design

The implementation is a single constant update in `constants/constants_v1.go`:

```go
// Old address (9R):
DevFundAddress: "thor1d8c0wv4y72kmlytegjmgx825xwumt9qt5xe07k",

// New address (2-of-3 multisig):
DevFundAddress: "thor1sgnuptp32y2fht3258vlx68mq8wsk2uz4wrshu",
```

No other code changes are required. The existing Dev Fund distribution logic in `manager_network_current.go` reads the `DevFundAddress` constant and sends the calculated RUNE amount from the Reserve module to that address. This logic is unaffected by the address change.

### Multisig Governance

The new address is a 2-of-3 multisig, requiring signatures from any 2 of the 3 keyholders to authorize transactions. This provides:

- Redundancy in case one keyholder is unavailable
- Protection against unilateral fund movement
- Distributed trust across independent ecosystem participants

### Node Operator Oversight

As established in ADR 018, Node Operators retain the ability to:

1. Pause, lower, or increase the Dev Fund allocation via Mimir (`DevFundSystemIncomeBps`)
2. Change the destination address via a subsequent ADR

## Consequences

### Positive

- Refreshes the custodian team to reflect current active contributors
- Introduces multisig governance (2-of-3) for improved security and accountability over the previous single-party custody
- Distributes trust across three independent ecosystem participants
- Resets the Sunset Clause, ensuring periodic review of fund custody

### Negative

- Remaining funds in the old address need to be managed separately by 9R
- Introduces coordination overhead for the 3 multisig keyholders when moving funds

### Neutral

- No change to the funding rate or distribution mechanism
- Node Operator oversight mechanisms remain unchanged

## References

- [ADR 018 — Core Protocol Sustainability](./adr-018-core-protocol-sustainability.md)
- [MR 4698 — Update dev fund address](https://gitlab.com/thorchain/thornode/-/merge_requests/4698)
