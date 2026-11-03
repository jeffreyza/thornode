# ADR 18: Core Protocol Sustainability

## Changelog

- 26/05/2024: Created
- 15/07/2024: Updated to remove PoL mechanics, added holding address
- 29/07/2024: Updated to include a 3-year expiry "Sunset Clause"

## Status

Proposed

## Context

THORChain is a complex protocol that requires a full-time protocol engineering, security and maintenance team. It is also a "moving" protocol that needs to react to external entropy (changing dominance of L1s, L1 architecture, user feature requests and more), thus requires continual upgrade efforts. This ADR discusses a long-term sustainable incentivisation proposal designed to motivate a full-time engineering team to work towards a single key protocol KPI: fee-revenue.

## Background

Protocol engineering in past was funded and managed by "OG" who handed over operationally to "9R" in the period beginning 2021.
The OG team provided an outlay of capital and vested incentives to NineRealms and THORSec for a 3-year period beginning late June 2021 and concluding June 2024.
It has been extremely important that "OG team" hand over the protocol to a community-led team as part of "Planned Obsolescence" as a proof of decentralisation.

9R+THORSec is an engineering, security and operational collective to manage the business development, maintenance and feature rollout of THORChain. 9R's primary business is as node-operator team, but heavily incentivised to ensure the longevity and sustainability of the protocol. 9R self-funded and emerged from the community in mid-2021.

9R (Core 2.0) currently do not have any additional incentives beyond being normal Node Operators, yet do the bulk of THORChain's maintenance work. This ADR partitions out incentives over 3 years directly to 9R to continue to motivate their efforts.

## Objectives

Long term incentives need to be aligned:

1. World-class talent acquisition for engineering & security
2. Sustainable incentives to pay for core protocol maintenance and upgrades, with Node Operator Oversight
3. Incentive Pendulum immune (not biased to either Nodes or LPs, rather tied to the system in total)

## Proposal

To achieve the above objectives:

### Dev Fund

```text
DevFundSystemIncomeBPS = 500 // 5%
```

5% of System Income (prior to being split to Nodes and LPs) should be paid into a certain address. This address should be nominated in the ADR and can be upgraded by susbsequent ADRs, including changing the fee amount. This achieves "Node Operator Oversight".

The receiving address should be fully-custodied by the "Core Protocol Team" of the day, with full discretion as to how this is spent.
If fiduciary duties are not being met (slowing protocol development), then the NO community can pause funding via Mimir or change the receipt address on subsequent ADR.

Sunset Clause: The ADR will automatically expire and halt payments to the address 3-years after start. A new ADR will need to be proposed to refresh the address. This allows contributors to establish who "Core" is every 3 years - being the primary contributors to the protocol. Any self-organised team should be able to campaign to be nominated as "Core".

At any time Core will be aligned to update, engage and be accountable to the community as to their efforts, else they will fail to be re-nominated, or worse, NO's can rally to pause or move funding in the interim. However, Core do not need to outlay their day-to-day expenses or allocations of the incentives for "approval" from NOs, else this will rapidly remove their ability to move quickly. This is typical of other "DAO Funding mechanisms" that devolve to operational stasis.

## Economics

At current price of RUNE ($4.00), around $150k is made by the system every day (fees + rewards) (up to $300k on high-volume days). At 5%, thus

```text
(150000*0.05)*365 = ~$3m/year (base case)
```

If RUNE prices doubles, and system volume doubles then the accumulated funding would be $12m/year.

This would be enough funding to pay for Core Protocol Maintenance needs, as well as provide ample alignment to grow System Income.

## Decision

Approved via node consensus.

## Consequences

The System Income will start allocating 5% System Income to a Core Protocol Funding wallet. Custody of this wallet will be handed to Core 2.0 for full discretion on spending, but with community oversight.
Separately, and not part of this ADR, Treasury will boost this wallet with 1m RUNE to be a Genesis Incentive Boost to Core 2.0. This will form the first 12-18months of Incentives to Core 2.0.
Nodes have an ability to user mimir to 1) pause, lower, increase the Dev Fund; or use ADR to 2) change the destination wallet.

For reference, the holding address will be this: thor1jw4ujum9a7wxwfuy9j7233aezm3yapc3s379gv
