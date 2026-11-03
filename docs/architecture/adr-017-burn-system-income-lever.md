# ADR 17: Burn System Income Lever

## Changelog

- 19/05/2024: Created

## Status

Proposed

## Context

The original ADR for lending [ADR-11-Lending](https://gitlab.com/thorchain/thornode/-/blob/develop/docs/architecture/adr-011-lending.md?ref_type%253Dheads#proactive-dynamic-burn) discussed the requirement for a burn of system income once Circuit Breakers were hit to pay down the liability of the system.

This has not yet been implemented. A possible imperative to accelerate this feature is two-fold:

1. Post-CB features should be implemented before hitting CB, as a method of calming the myopia "what-if" and reduce the uncertainty.
2. Nodes have a tool up their sleeve to temporarily increase Burn Rates to a non-zero amount, to play more into narratives affecting the market.

### Post CB Safety Lever

Adding the feature now will give more certainty to users wondering about the Post-CB state. This certainty thus reduces the myopia about those users entertaining the idea of the Post-CB state.

Public Posts such as [this](https://x.com/sunnya97/status/1803137054563307540) reinforce the misunderstanding of such a state, and adding the Lever now will partly address (or give a tool for supporters to address) the issue.

Additionally, it immediately gives nodes a tool to address supply issues if the CB is being approached, before trigging it. Currently they have no such ability.

### Market Narratives

With [EIP-1559](https://ultrasound.money/); the market converges around the Fee-Burn narrative much more strongly, leading to short-medium term influence on market sentiments. By building such a lever now, the Network can respond to the need for such a narrative much more rapidly.

## Economics

Burning the fees does not directly benefit LPs-Nodes, since it temporarily reduces their income. However it does theoretically enrich all holders equally. The stronger aspect at play here; is that LP-Nodes are part of a long-term capital-allocating cohort, so a short-term drop in income doesn't measurably affect their positions. Instead, this mechanism should influence short-term allocators and give Nodes an ability to calm markets; similar to Overnight Bank Funding Rates which directly influences the market, but does typically does not affet 5-10year bond participation rates.

Lifetime System Income is 16bn RUNE, so this proposal would have burnt 1.6m RUNE in the last 3 years.

## Proposal

New Mimir

```text
SystemIncomeBurnRateBP = 0.0001 //1BPS
```

This immediately starts burning 1% of system income (prior to be split to Nodes-LPs). It can be increased or set to 0 to turn off completely.

## Decision

Approved via node consensus.

## Consequences

The System Income will start burning 5%.
