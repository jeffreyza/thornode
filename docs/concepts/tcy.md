# THORChain TCY Token Technical Overview

## Introduction

The **TCY** token is a native asset introduced by THORChain to address approximately $210 million debt accumulated by THORFi’s savings and lending services. Approved through community governance via **Proposal 6**, TCY converts defaulted debt into equity, transforming creditors into stakeholders. See more information in this [THORFi Unwind Medium Article](https://medium.com/thorchain/thorfi-unwind-96b46dff72c0).
This page provides wallet developers with technical details to integrate TCY, covering token mechanics, revenue distribution, and pool interaction.

## Token Purpose and Background

TCY restructures approximately $210 million in unserviceable debt accumulated by THORFi, which suspended lending and savings services on **January 23, 2025**. By issuing TCY, THORChain converts debt into equity at a 1:1 ratio ($1 of debt = 1 TCY), providing creditors with a stake in the protocol’s future revenue. This approach aims to stabilise the ecosystem, maintain trust, and align creditor interests with THORChain’s long-term success.

## Key Technical Specifications

- **Token Type:** Native token on THORChain’s blockchain.
- **Total Supply:** Fixed at 210 million TCY tokens, corresponding to $210 million in defaulted debt.
- **Purpose:** Equity-like asset representing a share of THORChain’s revenue.
- **Distribution:** 1 TCY per $1 of defaulted debt, allocated to THORFi creditors.
- **Revenue Share:** TCY stakers receive 10% (set by [`TCYStakeSystemIncomeBps`](../mimir.md)) of system income, allocated per block to the TCY fund module. RUNE is distributed directly to stakers proportional to their staked position. When the TCY Fund’s balance reaches at least 2100 RUNE, set by [`MinRuneForTCYStakeDistribution`](../mimir.md#tcy-management). Stakers must stake at least 0.001 TCY (100000 / 1e8 and set by [`MinTCYForTCYStakeDistribution`](../mimir.md#tcy-management)) to be eligible.
- **Initial Pricing:** TCY starts trading at $0.10 per token in the RUNE/TCY liquidity pool.
- **Claim:** Claims must be greater than zero, with amounts defined in 1e8 precision within [`tcy_claimers_mainnet`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tcyclaimlist/tcy_claimers_mainnet.json). Claims are automatically staked.
- **Security:** TCY is secured by THORChain’s proof-of-stake consensus with node-bonded RUNE.

## Liquidity and Trading

To ensure market accessibility and price stability, THORChain has established a RUNE/TCY liquidity pool with the following details:

- **Initial Liquidity:** $500,000, funded by a $5 million treasury allocation to seed the RUNE/TCY pool.
- **Treasury Support:** $5 million deployed to purchase $500,000 of TCY weekly for 10 weeks.
- **Trading Mechanism:** TCY is tradable in the RUNE/TCY pool, a Continuous Liquidity Pool (CLP) contributing to total pooled liquidity, operating under the [Incentive Pendulum](../concepts/incentive-pendulum.md).
- **Recovery Timeline:** TCY has inherent value based on perpetual RUNE yield however full debt recovery (reaching $1 per TCY) is market dependent. Full debt recovery is not guaranteed.

## Revenue Distribution Mechanism

- **Revenue Source:** Swap fees and block emissions.
- **TCY Allocation:** 10% of system income per block is allocated to the TCY fund.
- **Distribution Frequency:** Each block (~6 seconds), if the TCY fund’s balance is at least 2100 RUNE, RUNE is distributed directly to stakers with at least 0.001 TCY, proportional to their staked TCY, in multiples of 2100 RUNE. The balance accumulates over multiple blocks based on income.

### Distribution Example

For example, if THORChain generates $100,000 in system income daily at $1.50 per RUNE with 10% of the system income being distributed to TCY stakers, and ~ 6 second block time, the following would occur:

- **System Income Split:** 6,666.667 RUNE daily is sent to the TCY Fund, which is ~0.462963 RUNE per block (6,666.667 ÷ 14,400 blocks).
- **Distribution Cycle:** To accumulate 2100 RUNE at ~0.462963 RUNE/block, it takes ~4,536 blocks (2100 ÷ 0.462963). This equals ~7.56 hours or ~0.315 days. Each block thereafter, if the fund’s balance is ≥2100 RUNE, a distribution of 2100 RUNE occurs, with ~3.1746 cycles per day (14,400 ÷ 4,536, approximate).
- **24-Hour Period:** The TCY fund distributes ~6,666.66 RUNE daily to stakers (2100 RUNE/cycle × ~3.1746 cycles/day), assuming consistent income.
- **Staker Distribution:** A user staking 1% of total TCY (2.1 million TCY, given 210 million TCY total supply) would receive 1% of 2100 RUNE = 21 RUNE per cycle, or ~66.6666 RUNE daily (~21 × 3.1746), assuming consistent income and stable RUNE value. Actual distributions vary based on system income and RUNE price.
- **Disclaimer:** The above example is hypothetical and for illustrative purposes only. Actual RUNE distributions are not guaranteed and depend on variable factors, including THORChain’s system income, RUNE market price volatility, TCY staking participation, and network conditions. Stakers should be aware of the risks outlined in the [User Interaction](#user-interaction) section.

## User Interaction

- **Claiming TCY**: Creditors will need to claim TCY using the [claim memo](../concepts/memos.md#claim-tcy). TCY is automatically staked during the claim process. Only valid claims are honored, detailed in [`tcy_claimers_mainnet`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tcyclaimlist/tcy_claimers_mainnet.json).
- **Staking TCY**: TCY is automatically staked when claimed to earn RUNE distributions (requires ≥0.001 TCY), as described in the [Revenue Distribution Mechanism](#revenue-distribution-mechanism) section. TCY can be unstaked and held in supported wallets, but unstaked TCY does not earn RUNE distributions.
- **Trading TCY**: Trade TCY in the RUNE/TCY pool via [THORChain-integrated DEXs](https://docs.thorchain.org/ecosystem#exchanges-only) (e.g., THORSwap, Asgardex).
- **Risks**:
  - **Market Volatility**: TCY’s market price may fluctuate, starting at $0.10 but potentially rising or falling based on RUNE performance and protocol revenue.
  - **Recovery Uncertainty**: Full debt recovery ($1 per TCY) is not guaranteed.
  - **RUNE Dependency**: Revenue is paid in RUNE, exposing TCY stakers to RUNE price volatility.
  - **No Governance Rights**: TCY stakers and holders do not have governance rights, unlike RUNE holders.

## Query TCY

### Claims

The `/thorchain/tcy_claimers` endpoint returns information on all TCY claims for THORFi creditors, including the asset, L1 address, and claim amount. The `/thorchain/tcy_claimer/{address}` endpoint returns all claims for a specific L1 address, which may include multiple claims for chains like EVM.

The `/thorchain/tcy_claimers` endpoint example output:

```json
[
  {
    "asset": "avax.avax",
    "l1_address": "0x00112c24ebee9c96d177a3aa2ff55dcb93a53c80",
    "tcy_claim": 335869573367
  },
  {
    "asset": "eth.eth",
    "l1_address": "0x7e4a8391c728fed9069b2962699ab416628b19fa",
    "tcy_claim": 150000000000
  },
  {
    "asset": "btc.btc",
    "l1_address": "12Fxnarf9wmPnGnFhe9SGk745dd6bSvKdi",
    "tcy_claim": 78780988965
  },
  {
    "asset": "eth.usdc-0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
    "l1_address": "0x453e85ac0f598cfc1cecc2ecbfb663f8c41c3a97",
    "tcy_claim": 1212647033705
  }
]
```

The `/thorchain/tcy_claimer/{address}` endpoint example output, where address = `0x00112c24ebee9c96d177a3aa2ff55dcb93a53c80`:

```json
[
  {
    "asset": "avax.avax",
    "l1_address": "0x00112c24ebee9c96d177a3aa2ff55dcb93a53c80",
    "tcy_claim": 335869573367
  },
  {
    "asset": "bsc.bnb",
    "l1_address": "0x00112c24ebee9c96d177a3aa2ff55dcb93a53c80",
    "tcy_claim": 345729506846
  }
]
```

### TCY Staking

The `/thorchain/tcy_staker/{address}` endpoint returns the staked TCY position where address is a thor address, e.g. `thor1230hd4mtzgxqvrjf73cjzu9mmy5gfr625eezu7`.

```json
{
  "address": "thor1230hd4mtzgxqvrjf73cjzu9mmy5gfr625eezu7",
  "amount": "10000000000000"
}
```

### TCY Balance

TCY is a bank token and can be accessed via the `/bank/balances/{address}` endpoint where address is a thor address, e.g. `thor1230hd4mtzgxqvrjf73cjzu9mmy5gfr625eezu7`. Use the `tcy_staker` endpoint to see the staking balance.

```json
{
  "result": [
    {
      "denom": "rune",
      "amount": "20000"
    },
    {
      "denom": "tcy",
      "amount": "100000000000"
    }
  ]
}
```

## References

- [THORFi Unwind Medium Article](https://medium.com/thorchain/thorfi-unwind-96b46dff72c0)
- [Add TCY Merge Request](https://gitlab.com/thorchain/thornode/-/merge_requests/3988)
- [Proposal 6 - Convert defaulted debt to $TCY](https://gitlab.com/-/snippets/4801556)
