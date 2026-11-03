# Overview

The Incentive Pendulum splits rewards between node operators and liquidity providers based on how much security the nodes provide to the network (in terms of bond) and how much liquidity is at risk in pools. Adjustments are made based on the actual vaults and assets that need to be secured and the amount of bond provided, leading to a fair allocation of rewards depending on the security/liquidity balance. The Incentive Pendulum controls the split of rewards up to the Pendulum Reward Cap, but it does not control the admission of liquidity to the network—that is enforced by the TVL Cap.

## Terms and Variables

### Key terms used to determine the Incentive Pendulum are

- **Bond Hard Cap**: The highest bond value among the bottom 2/3 of [active nodes](https://thorchain.net/nodes), including bond from both node operators and bond providers, to ensure no single node has excessive influence on the **Total Effective Bond**.
- **Effective Security Bond**: The sum of the total bond of the bottom 2/3 active nodes (including both node operators and bond providers).
- **Total Effective Bond**: The sum of all active nodes bond up to the **Bond Hard Cap**. For each node, the effective bond amount that receives rewards and is acknowledged by the Incentive Pendulum is capped by the Bond Hard Cap. This maintains a balanced and secure network by encouraging additional bond increase only of the 2/3 of nodes with the lowest bond.
- **Vault Liquidity**: Sum value of L1 assets within all Asgard Vaults valued in RUNE. Includes Pooled Assets, Trade Assets, Secure Assets, streaming swap Assets, and Oversolvencies.
- [**Total Pooled**](https://runescan.io/address/thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0): Sum of RUNE liquidity in all available [pools](https://runescan.io/pools) by liquidity providers. Total Pooled is within Vault Liquidity.
- **Total Rewards**: Block rewards as per the [block emission schedule](./economic-model.md#emission-schedule) plus liquidity fees within one block, after `DevFundSystemIncomeBps`, `SystemIncomeBurnRateBps` and `TCYStakeSystemIncomeBps` have been deducted.
- **TVL Cap Basis Points**: Basis points defining the maximum allowed liquidity relative to total bonded RUNE before deposits are blocked.

## Pendulum Reward Cap and TVL Cap

While the Incentive Pendulum dynamically adjusts rewards to maintain a healthy balance between bonded RUNE and pooled liquidity, there are also independent mechanisms that enforce upper limits on rewards and liquidity admitted into the protocol.

- **Pendulum Reward Cap**: As described above, if the `securing` amount of bonded RUNE falls below the secured amount of liquidity, liquidity providers receive no rewards. This creates a strong disincentive to over-supply liquidity without sufficient bond coverage. This enforcement is referred to as the Pendulum Reward Cap.

- **TVL Cap (Independent Safety Limit)**: Separately from the Incentive Pendulum, the protocol enforces a configurable Total Value Locked Cap (TVL Cap) to prevent the aggregate Total Pooled value from exceeding a safe proportion of total node bond. The TVL Cap is calculated as `security = sum(all node bonds) × TVLCapBasisPoints / 10,000`. When the total pooled liquidity exceeds this threshold, further liquidity additions are rejected until either more bond is added or the cap is adjusted. These mechanisms work alongside the Incentive Pendulum, which determines the dynamic reward allocation described below.

## Incentive Pendulum

Within the Incentive Pendulum, `secured` and `securing` define the amount of L1 assets to be secured and the amount of RUNE Bond that secures them. Both values can change depending on the below Network Variables [Mimir](../mimir.md#economics).

### Key [Network Variables](../mimir.md#economics) that influence the Incentive Pendulum are

- **PendulumUseEffectiveSecurity**: If = 1 (True), `securing` will be the **Effective Security Bond**. If = 0 (False) `securing` will be **Total Effective Bond**.
- **PendulumUseVaultAssets**: If = 1 (True), `secured` will be **Vault Liquidity**, e.g., all L1 Assets. If = 0 (False) `secured` will be **Total Pooled**, a subset of Vaulted Liquidty.
- **PendulumAssetsBasisPoints**: Used to scale the Incentive Pendulum perception of the `secured` L1 asset size. > 100% overestimates and < 100% underestimates the amount of secured assets.

The current network Incentive Pendulum can be seen in [RUNETools](https://rune.tools/pendulum).

## Algorithm

The algorithm that controls the Incentive Pendulum is as follows:

### Apply Network Variables

1. Identify Security Bond and Assets to be Secured:

$$
securing =
\begin{cases}
effectiveSecurityBond & if  PendulumUseEffectiveSecurity = 1 \\
totalEffectiveBond & otherwise
\end{cases}
$$

$$
secured =
\begin{cases}
vaultLiquidity & if  PendulumUseVaultAssets = 1 \\
pooledRune & otherwise
\end{cases}
$$

2. Adjust `secured` liquidity size up or down based on **PendulumAssetBasisPoints**:

$$
secured = \frac{PendulumAssetBasisPoints}{10,000} \times secured
$$

3. Check the securing bond is not less than the secured liquidity. No reward payments to liquidity providers when the `securing` is less than or equal to the `secured`; example: `if effectiveSecurityBond.LTE(vaultLiquidity)`. This known as the Pendulum Reward Cap.

$$
securing \leq secured \implies finalPoolShare = 0
$$

### Work out Node and LP Shares

4. Determine the Initial Share of rewards for node operators based on the `securing` and `secured`:

$$
baseNodeShare= \frac{secured}{securing} \times totalRewards
$$

5. Calculate Base Pool Share after allocating the `baseNodeShare` to node operators:

$$
basePoolShare = totalRewards - baseNodeShare
$$

### Adjust and Aggregate Rewards

6. **Adjust Node and Pool Shares:**

- **Adjust the Node Share** if `totalEffectiveBond` exceeds the `effectiveSecurityBond` so Nodes are rewarded up to the Bond Hard Cap:

$$
adjustmentNodeShare = \frac{totalEffectiveBond}{effectiveSecurityBond} \times baseNodeShare
$$

- **Adjust Pool Share** based on the ratio of `pooledRUNE` to `vaultLiquidity` as non-pooled liquidity is not yield-bearing:

$$
adjustmentPoolShare = \frac{pooledRUNE}{vaultLiquidity} \times basePoolShare
$$

7. **Readjust rewards depending on the network settings:**

$$
adjustmentNodeShare =
\begin{cases}
adjustmentNodeShare, & if  PendulumUseEffectiveSecurity = 1 \\
baseNodeShare, & otherwise
\end{cases}
$$

$$
adjustmentPoolShare =
\begin{cases}
adjustmentPoolShare, & if  PendulumUseVaultAssets = 1 \\
basePoolShare, & otherwise
\end{cases}
$$

8. **Aggregate the adjusted shares** for both node operators and liquidity providers:

$$
adjustmentRewards = adjustmentPoolShare + adjustmentNodeShare
$$

### Work out the Split of Total Rewards

9. **Calculate the final amount** of rewards allocated to liquidity providers and node operators, ensuring it does not exceed the `totalRewards`:

$$
finalPoolShare = \frac{adjustmentPoolShare}{adjustmentRewards} \times totalRewards
$$

$$
finalNodeShare = \frac{adjustmentNodeShare}{adjustmentRewards} \times totalRewards
$$

Liquidity Providers are paid the `finalPoolShare` and node operators are paid the remainder.

10. **Yield Calculation** based on Liquidity Provided

Ensure the yield (rewards per unit of liquidity) for liquidity providers and node operators is balanced, taking into account the total effective bond and the vault liquidity:

- **Yield for Node Operators:**

$$
nodeYield = \frac{finalNodeShare}{totalEffectiveBond}
$$

- **Yield for Liquidity Providers:**

$$
poolYield = \frac{finalPoolShare}{2 \times pooledRUNE}
$$

## Impact of Pendulum Parameters: Effective Security and Vault Assets Parameters

Below are examples using the same values to demonstrate how the PendulumUseEffectiveSecurity and PendulumUseVaultAssets network variables affect the outcome of the Incentive Pendulum.

In all examples the values are:

| Parameters                | Value           |
| ------------------------- | --------------- |
| totalEffectiveBond        | 99,000,000 RUNE |
| effectiveSecurityBond     | 66,000,000 RUNE |
| vaultLiquidity            | 33,000,000 RUNE |
| pooledRUNE                | 22,000,000 RUNE |
| totalRewards              | 1,000 RUNE      |
| PendulumAssetsBasisPoints | 100%            |

### Example 1: Both parameters are False, the default setting

PendulumUseEffectiveSecurity = 0
PendulumUseVaultAssets = 0

- Node Share = 22.22%
- Pool Share = 77.78%

In this scenario, both parameters are set to false, meaning that the Incentive Pendulum calculation uses totalEffectiveBond for securing and pooledRUNE (i.e., total pooled) as the secured value. This increases the securing (bond) and reduces the secured (pool) share. Consequently, the network is considered over bonded, with too much bond compared to pooled liquidity. To incentivise more liquidity, a greater share of final rewards is allocated to pools, while nodes receive a lower share.

### Example 2: PendulumUseEffectiveSecurity is True, PendulumUseVaultAssets is False

PendulumUseEffectiveSecurity = 1
PendulumUseVaultAssets = 0

Node Share = 42.86%
Pool Share = 57.14%

In this scenario, with PendulumUseEffectiveSecurity set to true, the Incentive Pendulum calculation uses effectiveSecurityBond for the securing value. Since effectiveSecurityBond is lower than totalEffectiveBond, the securing amount is decreased however the secured amount remains the same. Consequently, the securing (bond) is reduced, and the network is not considered over bonded compared to the previous example. As a result, the final rewards are split slightly in favor of the pools.

### Example 3: PendulumUseEffectiveSecurity is False, PendulumUseVaultAssets is True

PendulumUseEffectiveSecurity = 0
PendulumUseVaultAssets = 1

Node Share = 42.86%
Pool Share = 57.14%

In this scenario, with PendulumUseEffectiveSecurity set to false, the totalEffectiveBond becomes the securing, and with PendulumUseVaultAssets set to true, the secured becomes vaultLiquidity instead of pooledRUNE. Compared to the previous example, this increases both the securing (bond) and the secured (vaultLiquidity). As a result, and due to the example numbers used, the income distribution is similar to the previous example slightly in favor of the pools.

### Example 4: Both parameters are True

PendulumUseEffectiveSecurity = 1
PendulumUseVaultAssets = 1

Node Share = 69.23%
Pool Share = 30.77%

In this scenario, with PendulumUseEffectiveSecurity set to true, the effectiveSecurityBond is used as the securing, and with PendulumUseVaultAssets set to true, vaultLiquidity is used as the secured in the Incentive Pendulum calculation. This results in a reduced securing amount (bond) and an increased secured amount (vaulted Liquidity) compared to the first example resulting in the network being under bonded. To incentivise more bond, the nodes have a higher share of the final rewards, and the pools have a lower share.

## Simplified examples

Below are two examples that show partial calculations with different considerations based on the PendulumUseEffectiveSecurity and PendulumUseVaultAssets network values.

In all examples the values are:

| Parameters            | Value           |
| --------------------- | --------------- |
| totalEffectiveBond    | 99,000,000 RUNE |
| effectiveSecurityBond | 66,000,000 RUNE |
| vaultLiquidity        | 33,000,000 RUNE |
| pooledRUNE            | 22,000,000 RUNE |
| totalRewards          | 1,000 RUNE      |

### Parameters Set to False

These are the Constant defaults.

| Parameters                   | Value |
| ---------------------------- | ----- |
| PendulumUseEffectiveSecurity | 0     |
| PendulumUseVaultAssets       | 0     |
| PendulumAssetsBasisPoints    | 100%  |

Starting Point:

- Secured = 22000000 \* 1
- Securing = 99000000

Base LP Shares:

- baseNodeShare = 222.22
- basePoolShare = 777.778

As PendulumUseEffectiveSecurity and PendulumUseVaultAssets are both false, there are no adjustments required.

- adjustmentRewards = 1000
- finalNodeShare = adjustmentNodeShare = baseNodeShare
- finalPoolShare = adjustmentPoolShare = basePoolShare

Final Shares are:

- Node Share = 22.22%
- Pool Share = 77.78%

RUNE per RUNE Provided Reward:

- Node Yield: 2.24467x10^-6
- Pool Yield: 17.6777x10^-6

### Parameters Set to True

| Parameters                   | Value |
| ---------------------------- | ----- |
| PendulumUseEffectiveSecurity | 1     |
| PendulumUseVaultAssets       | 1     |
| PendulumAssetsBasisPoints    | 100%  |

Starting Point:

- Secured = 33000000 \* 1
- Securing = 66000000

Base LP Shares:

- baseNodeShare = 500
- basePoolShare = 500

As PendulumUseEffectiveSecurity and PendulumUseVaultAssets are both true, an adjustment is required:

- adjustmentNodeShare = 750
- adjustmentPoolShare = 333.333
- adjustmentRewards = 1083.333

Re-adjusted based on `adjustmentRewards` and `totalRewards`.

- finalNodeShare = 692.31
- finalPoolShare = 307.69

Final Shares are:

- Node Share = 69.23%
- Pool Share = 30.77%

RUNE per RUNE Provided Reward:

- Node Yield: 6.993x10^-6
- Pool Yield: 6.993x10^-6

## Driving Capital Allocation

As a by-product of the Incentive Pendulum's aggressive re-targeting of the yield split between node:LP yield, the system aims to maintain an equilibrium where the value of BONDED RUNE is proportionally aligned with the value of Vaulted RUNE.

If there is any disruption to this balance, the Incentive Pendulum will reallocate rewards to correct the imbalance by incentivising node operators to bond more RUNE or liquidity providers to pool more assets. With the use of [RUNEPool](../concepts/rune-pool.md) and [Pooled Nodes](https://docs.thorchain.org/thornodes/pooled-thornodes) users can play both sides of the Incentive Pendulum in order to maximise their return and help return the network back into equilibrium.
