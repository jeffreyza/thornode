# Transaction Memos

## Overview

Transactions to THORChain pass user intent with the `MEMO` field on their respective chains. THORChain inspects the transaction object and the `MEMO` in order to process the transaction, so care must be taken to ensure the `MEMO` and the transaction are both valid. If not, THORChain will automatically refund the assets. Memos are set in inbound transactions unless specified.

THORChain uses specific [asset notation](asset-notation.md) for all assets. Assets and functions can be abbreviated, and affiliate addresses and asset amounts can be shortened to [reduce memo length](memo-length-reduction.md), including through use of [scientific notation](memo-length-reduction.md#scientific-notation). Some parameters can also refer to a [THORName](../affiliate-guide/thorname-guide.md) instead of an address.

A guide has been created for [Swap](../swap-guide/quickstart-guide.md) to enable quoting and the automatic construction of memos for simplicity.

All memos are listed in the [relevant THORChain source code](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/memo/memo.go) variable `stringToTxTypeMap`.

### Memo Size Limits

THORChain has a [memo size limit of 250 bytes](https://gitlab.com/thorchain/thornode/-/blob/develop/constants/constants.go?ref_type=heads#L32). Any inbound tx sent with a larger memo will be ignored. Additionally, memos on UTXO chains are further constrained by the `OP_RETURN` size limit, which is [80 bytes](https://developer.bitcoin.org/devguide/transactions.html#null-data), which can be extended by using the approach described [here](sending-transactions.md#memo-longer-than-80-bytes).

### Dust Thresholds

THORChain has various dust thresholds (dust limits), defined on a per-chain basis. These are minimum amounts required for transactions to be processed by THORChain. Transactions below these thresholds will be ignored to prevent spam and ensure economic viability.

For example, sending 1 satoshi of BTC or 1 wei of ETH would be below the dust threshold and ignored. Each chain has different thresholds based on their native transaction costs and economic considerations. Refer to the [Dust Thresholds](../bifrost/vault-behaviors.md#dust-thresholds) for current threshold values for each supported chain.

## Format

All memos follow the format: `FUNCTION:PARAM1:PARAM2:PARAM3:PARAM4`

The function is invoked by a string, which in turn calls a particular handler in the state machine. The state machine parses the memo looking for the parameters which it simply decodes from human-readable strings.

Some parameters are optional. Simply leave them blank but retain the `:` separator, e.g., `FUNCTION:PARAM1:::PARAM4`.

## Permitted Functions

The following functions can be put into a memo:

1. [**SWAP**](memos.md#swap)
1. [**ADD Liquidity**](memos.md#add-liquidity)
1. [**WITHDRAW Liquidity**](memos.md#withdraw-liquidity)
1. [**CLAIM TCY**](memos.md#claim-tcy)
1. [**STAKE TCY**](memos.md#stake-tcy)
1. [**UNSTAKE TCY**](memos.md#unstake-tcy)
1. [**ADD Trade Account**](memos.md#add-trade-account)
1. [**WITHDRAW Trade Account**](memos.md#withdraw-trade-account)
1. [**ADD Secured Asset**](memos.md#add-secured-asset)
1. [**WITHDRAW Secured Asset**](memos.md#withdraw-secured-asset)
1. [**SWITCH Asset**](memos.md#switch-asset)
1. [**DEPOSIT** **Savers**](memos.md#deposit-savers)
1. [**WITHDRAW Savers**](memos.md#withdraw-savers)
1. [**DEPOSIT RUNEPool**](memos.md#deposit-runepool)
1. [**WITHDRAW RUNEPool**](memos.md#withdraw-runepool)
1. [**BOND**, **UNBOND**, **REBOND** & **LEAVE**](memos.md#bond-unbond-rebond-and-leave)
1. [**BONDYIELD**](memos.md#bond-yield)
1. [**MAINT** (Maintenance Mode)](memos.md#maintenance-mode)
1. [**OPERATOR Rotate**](memos.md#operator-rotate)
1. [**DONATE** & **RESERVE**](memos.md#donate--reserve)
1. [**REFERENCE MEMO** - Memoless Transactions](memos.md#reference-memo---memoless-transactions)
1. [**MIGRATE**](memos.md#migrate)
1. [**NOOP**](memos.md#noop)
1. [**Other Internal Memos**](memos.md#other-internal-memos)

### Swap

Perform an asset swap. THORChain supports different swap types using specific prefixes:

- **Market Swaps**: Use `=` prefix for immediate execution
- **Limit Swaps**: Use `=<` prefix for conditional execution based on price limits

**`SWAP:ASSET:DESTADDR:LIM/INTERVAL/QUANTITY:AFFILIATE:FEE`**

```admonish info
For the DEX aggregator-oriented variation of the `SWAP` memo, see [Aggregators Memos](../aggregators/memos.md).
```

| Parameter     | Notes                                                                                 | Conditions                                                                                                                                                                                               |
| ------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Payload       | Send the asset to swap.                                                               | Must be an active pool on THORChain.                                                                                                                                                                     |
| `SWAP`        | The swap handler.                                                                     | Also `s`, `=` (market swap), or `=<` (limit swap)                                                                                                                                                        |
| `:ASSET`      | The [asset identifier](asset-notation.md).                                            | Can be shortened.                                                                                                                                                                                        |
| `:DESTADDR`   | The destination address to send to.                                                   | Can use THORName.                                                                                                                                                                                        |
| `/REFUNDADDR` | The destination address for a refund to be sent to.                                   | Optional. If provided, the refund will be sent to this address; otherwise, it will be sent to the originator's address.                                                                                  |
| `:LIM`        | The trade limit, i.e., set 100000000 to get a minimum of 1 full asset, else a refund. | Optional. 1e8 or scientific notation. For limit swaps (`=<`), this determines execution price.                                                                                                           |
| `/INTERVAL`   | Swap interval in blocks.                                                              | Optional. With advanced queue: 0 = rapid streaming (multiple sub-swaps per block), ≥1 = traditional streaming (one sub-swap per X blocks). For limit swaps, can also specify custom TTL (≤43200 blocks). |
| `/QUANTITY`   | Swap quantity. The interval value determines the frequency of swaps in blocks.        | Optional. If 0 or omitted with advanced queue, network determines optimal streaming parameters. Legacy queue: 0 = single swap.                                                                           |
| `:AFFILIATE`  | The affiliate addresses.                                                              | Optional. Define up to [MultipleAffiliatesMaxCount](../mimir.md#swapping) (currently 5) THORNames or THOR Addresses, separated by `/`                                                                    |
| `:FEE`        | The [affiliate fees](fees.md#affiliate-fee). RUNE is sent to affiliate.               | Optional. Ranges from 0 to 1000 Basis Points. Specify one fee for all affiliates, or individual fees matching the number of affiliates defined.                                                          |

**Syntactic Examples:**

- `=:ASSET:DESTADDR` &mdash; market swap (with advanced queue: auto-streaming for optimal execution)
- `=:ASSET:DESTADDR:/1/1` &mdash; market swap (force single swap, disable streaming)
- `=<:ASSET:DESTADDR:LIM` &mdash; limit swap (executes only when price target is met)
- `SWAP:ASSET:DESTADDR/REFUNDADDR:LIM/1/0:AFFILIATE:FEE` &mdash; swap with refund address specified
- `=:ASSET:DESTADDR:LIM/0/5` &mdash; rapid streaming: 5 sub-swaps as fast as possible (multiple per block)
- `=:ASSET:DESTADDR:LIM/3/5` &mdash; traditional streaming: 5 sub-swaps, one every 3 blocks
- `=<:ASSET:DESTADDR:LIM/0/0` &mdash; limit swap, rapid execution when conditions met
- `=<:ASSET:DESTADDR:LIM/43200/0` &mdash; limit swap with custom 43200 block TTL (max allowed)
- `=:ASSET:DESTADDR:LIM/1/0:AFFILIATE:FEE` &mdash; market swap with affiliate fee
- `=<:ASSET:DESTADDR:LIM/1/0:AFFILIATE1/AFFILIATE2/AFFILIATE3:FEE` &mdash; limit swap with multiple affiliates
- `=:ASSET:DESTADDR:LIM/1/0:AFFILIATE1/AFFILIATE2/AFFILIATE3:FEE1/FEE2/FEE3` &mdash; market swap with individual affiliate fees

**Real-world Examples:**

- `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0` &mdash; swap to Ether, send output to the specified address
- `=:BTC.BTC:bc1q6527vxxqjpq80la2l0sw7hay3lj6dz07zs6gzl/0x7a093cebfa77403672d68e1c22d0681400a36682` &mdash; swap to Bitcoin, send output to the specified address. If a refund, send to the other specific address. Source and refund address should be the same chain.
- `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000` &mdash; same as above except the ETH output should be more than 0.1 ETH else refund
- `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/1/1` &mdash; same as above except do not stream the swap
- `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/3/0` &mdash; same as above except streaming the swap, every 3 blocks, and THORChain to calculate the number of swaps required to achieve optimal price efficiency
- `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/3/0:t:10` &mdash; same as above except sends 10 basis points from the input to affiliate `t` (THORSwap)
- `s:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:1e6/3/0:t:10` &mdash; same as above except with a reduced memo and scientific notation trade limit
- `=:r:thor1el4ufmhll3yw7zxzszvfakrk66j7fx0tvcslym:19779138111` &mdash; swap to at least 197.79 RUNE
- `=:BSC.BNB:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:544e6/2/6` &mdash; swap to at least 5.4 BNB, using streaming swaps, 6 swaps, every 2 blocks
- `=:BTC~BTC:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz:1e6/1/0:dx:10` &mdash; Swap to Bitcoin Trade Asset, using a Limit, Streaming Swaps and a 10 basis point fee to the affiliate `dx` (Asgardex)
- `=:BTC-BTC:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz:1e6/1/0:dx:10` &mdash; Swap to Bitcoin Secured Asset, using a Limit, Streaming Swaps and a 10 basis point fee to the affiliate `dx` (Asgardex)
- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5:10` &mdash; Swap to Ether, will skim 10 basis points for each of the affiliates
- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/dx/ss:10/20/30` &mdash; Swap to Ether, Will skim 10 basis points for `t1`, 20 basis points for `dx`, and 30 basis points for `ss`

#### Advanced Swap Queue

THORChain uses an advanced swap queue system with different operational modes controlled by the `EnableAdvSwapQueue` mimir setting:

- **Disabled (0)**: Uses legacy swap queue
- **Enabled (1)**: Full advanced queue with both market and limit swaps
- **Market-only (2)**: Advanced queue for market swaps only, limit swaps are skipped

#### Swap Boundaries and Limits

**Limit Swap Expiration:**

- **Default TTL**: 43,200 blocks (~3 days)
- **Custom TTL**: Specify via `/INTERVAL` parameter (must be ≤ 43,200 blocks)
- **Automatic Cleanup**: Expired limit swaps are automatically removed and remaining funds refunded

**Rapid Swap Processing:**

- The advanced queue can process multiple swap iterations per block
- Controlled by `AdvSwapQueueRapidSwapMax` mimir setting (default: 1)
- Enables higher throughput during peak activity

**Price Discovery:**

- Market swaps execute immediately at current pool prices
- Limit swaps only execute when the fee-less swap ratio meets the specified limit
- Advanced indexing system for efficient limit order matching

```admonish info
For detailed information about the advanced swap queue, see [Advanced Swap Queue Guide](../swap-guide/advanced-swap-queue.md).
```

### Add Liquidity

Add liquidity to a pool.

**`ADD:POOL:PAIREDADDR:AFFILIATE:FEE`**

There are rules for adding liquidity, see [the rules here](https://docs.thorchain.org/thornodes/frequently-asked-questions/liquidity-providers#entering-and-leaving-a-pool).

| Parameter     | Notes                                                                                                                                                                                                                                  | Conditions                                                                  |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Payload       | The asset to add liquidity with.                                                                                                                                                                                                       | Must be supported by THORChain.                                             |
| `ADD`         | The add liquidity handler.                                                                                                                                                                                                             | Also `+`                                                                    |
| `:POOL`       | The pool to add liquidity to.                                                                                                                                                                                                          | Can be shortened.                                                           |
| `:PAIREDADDR` | The other address to link with. If on external chain, link to THOR address. If on THORChain, link to external address. If a paired address is found, the LP is matched and added. If none is found, the liquidity is put into pending. | Optional. If not specified, a single-sided add-liquidity action is created. |
| `:AFFILIATE`  | The affiliate address. The affiliate is added to the pool as an LP.                                                                                                                                                                    | Optional. Must be a THORName or THOR Address.                               |
| `:FEE`        | The [affiliate fee](../affiliate-guide/affiliate-fee-guide.md#how-it-works).                                                                                                                                                           | Optional. Ranges from 0 to 1000 Basis Points.                               |

**Examples:**

- `ADD:BTC.BTC` &mdash; add liquidity single-sided. If this is a position's first add, liquidity can only be withdrawn to the same address
- `a:POOL:PAIREDADDR` &mdash; add on both sides (dual-sided)
- `+:POOL:PAIREDADDR:AFFILIATE:FEE` &mdash; add dual-sided with affiliate
- `+:ETH.ETH:` &mdash; add liquidity with position pending

### Withdraw Liquidity

Withdraw liquidity from a pool.

**`WITHDRAW:POOL:BASISPOINTS:ASSET`**

A withdrawal can be either dual-sided (withdrawn based on pool's price) or entirely single-sided (converted to one side and sent out).

| Parameter      | Notes                                                                                       | Conditions                                                    |
| -------------- | ------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| Payload        | Send the dust threshold of the asset to cause the transaction to be picked up by THORChain. | [Dust thresholds](#dust-thresholds) must be met.              |
| `WITHDRAW`     | The withdraw liquidity handler.                                                             | Also `-` or `wd`                                              |
| `:POOL`        | The pool to withdraw liquidity from.                                                        | Can be shortened.                                             |
| `:BASISPOINTS` | Basis points.                                                                               | Range 0-10000, where 10000 = 100%.                            |
| `:ASSET`       | Single-sided withdraw to one side.                                                          | Optional. Can be shortened. Must be either RUNE or the ASSET. |

**Examples:**

- `WITHDRAW:POOL:10000` &mdash; dual-sided 100% withdraw liquidity. If a single-address position, this withdraws single-sidedly instead
- `-:POOL:1000` &mdash; dual-sided 10% withdraw liquidity
- `wd:POOL:5000:ASSET` &mdash; withdraw 50% liquidity as the asset specified while the rest stays in the pool, e.g., `wd:BTC.BTC:5000:BTC.BTC`

### Claim TCY

Claim TCY tokens for THORFi debt. Claims must be sent from an address within [`tcy_claimers_mainnet`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tcyclaimlist/tcy_claimers_mainnet.json). Claimed TCY is assigned to the specified thor address and automatically staked.

**`TCY:ADDR`**

| Parameter | Notes                  | Conditions                                       |
| --------- | ---------------------- | ------------------------------------------------ |
| Payload   | From a claim address   | [Dust thresholds](#dust-thresholds) must be met. |
| `TCY`     | The TCY Claim handler. |                                                  |
| `ADDR`    | Must be a thor address | Becomes the owner of the TCY                     |

Example:

- `tcy:thor1tj237x6zdpk3l7x8g4qhutqnnjq5v6syglprz7` &mdash; claim TCY, sent to the originator's address.

### Stake TCY

**`TCY+`**

| Parameter | Notes                  | Conditions        |
| --------- | ---------------------- | ----------------- |
| Payload   | TCY to be staked       | Use `MsgDeposit`. |
| `TCY+`    | The TCY Stake handler. |                   |

Example:

- `TCY+` &mdash; Stakes TCY included in the payload.

### Unstake TCY

**`TCY-:BASISPOINTS`**

| Parameter      | Notes                    | Conditions                         |
| -------------- | ------------------------ | ---------------------------------- |
| Payload        | Unstake TCY              | Use `MsgDeposit`.                  |
| `TCY-`         | The TCY Unstake handler. |                                    |
| `:BASISPOINTS` | Basis points.            | Range 0-10000, where 10000 = 100%. |

Example:

- `TCY-:5000` &mdash; remove 50% of the staked TCY.

### Add Trade Account

**`TRADE+:ADDR`**

Adds an L1 asset to the [Trade Account](../concepts/trade-accounts.md).

| Parameter | Notes                                 | Conditions                                     |
| --------- | ------------------------------------- | ---------------------------------------------- |
| Payload   | The asset to add to the Trade Account | Must be a L1 asset and supported by THORChain. |
| `TRADE+`  | The trade account handler.            |                                                |
| `ADDR`    | Must be a thor address                | Specifies the owner                            |

**Example:** `TRADE+:thor1x2whgc2nt665y0kc44uywhynazvp0l8tp0vtu6` - Add the sent asset and amount to the Trade Account.

### Withdraw Trade Account

Withdraws an L1 asset from the Trade Account.

**`TRADE-:ADDR`**

| Parameter | Notes                                                                          | Conditions               |
| --------- | ------------------------------------------------------------------------------ | ------------------------ |
| Payload   | The [Trade Asset](./asset-notation.md#trade-assets) to be withdrawn and amount | Use `MsgDeposit`.        |
| `TRADE-`  | The trade account handler.                                                     |                          |
| `ADDR`    | L1 address to which the withdrawal will be sent                                | Cannot be a thor address |

Note: Trade Asset and Amount are determined by the `coins` within the `MsgDeposit`. Transaction fee in `RUNE` does apply.

**Example:**

- `TRADE-:bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw` - Withdraw 0.1 BTC from the Trade Account and send to `bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw`

  ```text
  {"body":{"messages":[{"":"/types.MsgDeposit","coins":[{"asset":"BTC~BTC","amount":"10000000","decimals":"0"}],"memo":"trade-:bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw","signer":"thor19phfqh3ce3nnjhh0cssn433nydq9shx7wfmk7k"}],"memo":"","timeout_height":"0","extension_options":[],"non_critical_extension_options":[]},"auth_info":{"signer_infos":[],"fee":{"amount":[],"gas_limit":"200000","payer":"","granter":""}},"signatures":[]}
  ```

### Add Secured Asset

**`SECURE+:ADDR`**

Converts a L1 asset to a [Secured Asset](../concepts/secured-assets.md).

| Parameter | Notes                               | Conditions                                     |
| --------- | ----------------------------------- | ---------------------------------------------- |
| Payload   | The asset to become a Secured Asset | Must be a L1 asset and supported by THORChain. |
| `SECURE+` | The Secured Asset handler.          |                                                |
| `ADDR`    | Must be a thor address              | Specifies the owner and destination            |

**Example:** `SECURE+:thor1x2whgc2nt665y0kc44uywhynazvp0l8tp0vtu6` - Converts the sent asset and amount to a Secured Asset.

### Withdraw Secured Asset

Converts a Secured Asset to a L1 Asset.

**`SECURE-:ADDR`**

| Parameter | Notes                                                                             | Conditions               |
| --------- | --------------------------------------------------------------------------------- | ------------------------ |
| Payload   | The [Secured Asset](./asset-notation.md#secured-assets) to be redeemed and amount | Use `MsgDeposit`.        |
| `SECURE-` | The Secured Asset handler.                                                        |                          |
| `ADDR`    | L1 address to which the L1 asset will be sent                                     | Cannot be a thor address |

Note: Secured Assets and amount are determined by the `coins` within the `MsgDeposit`. Transaction fee in `RUNE` does apply.

**Example:** `SECURE-:bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw` - Convert 0.1 BTC from a Secured Asset to a L1 and send to `bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw`

### Switch Asset

One way switch for external tokens to be a native THORChain asset. Supported Assets are listed in a switchMap within the `SwitchManager`.

**`SWITCH:ADDR`**

| Parameter | Notes                              | Conditions                           |
| --------- | ---------------------------------- | ------------------------------------ |
| Payload   | The asset to become a Native Asset | Must be a L1 supported on THORChain. |
| `switch`  | The Switch handler.                |                                      |
| `ADDR`    | The thor address for native asset  | Must be a thor address               |

### Limit Swap Modification

**`m=<:SOURCE:TARGET:MODIFIEDTARGETAMOUNT`**

A trader can modify a limit swap by changing the target amount. Setting this
value to 0 will cancel the limit swap.

| Parameter               | Description                               | Notes                                        |
| ----------------------- | ----------------------------------------- | -------------------------------------------- |
| `Payload`               | None required                             | Use `MsgDeposit` or send from external chain |
| `m=<`                   | The limit swap modifier handler           |                                              |
| `:SOURCE`               | The source coin (e.g., `1234BTC.BTC`)     |                                              |
| `:TARGET`               | The target coin (e.g., `1234ETH.ETH`)     |                                              |
| `:MODIFIEDTARGETAMOUNT` | The modified target amount (e.g., `1235`) |                                              |

### **Deposit Savers**

Deposit an asset into THORChain Savers.

**`ADD:POOL::AFFILIATE:FEE`**

| Parameter    | Notes                                                                                                   | Conditions                                      |
| ------------ | ------------------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| Payload      | The asset to add liquidity with.                                                                        | Must be supported by THORChain.                 |
| `ADD`        | The deposit handler.                                                                                    | Also `+`                                        |
| `:POOL`      | The pool to add liquidity to.                                                                           | Gas and stablecoin pools only.                  |
| `:`          | Must be empty.                                                                                          | Optional. Required if adding affiliate and fee. |
| `:AFFILIATE` | The affiliate address.                                                                                  | Optional. Must be a THORName or THOR Address.   |
| `:FEE`       | The [affiliate fee](../affiliate-guide/affiliate-fee-guide.md#how-it-works). RUNE is sent to affiliate. | Optional. Ranges from 0 to 1000 Basis Points.   |

**Examples:**

- `ADD:ETH/ETH` &mdash; deposit into the ETH Savings Vault
- `+:BTC/BTC::t:10` &mdash; deposit into the BTC Savings Vault, with 10 basis points being sent to affiliate `t` (THORSwap)
- `+:DOGE/DOGE` &mdash; deposit into the DOGE Savings Vault

```admonish info
Depositing into Savers can also work without a memo, however memos are recommended to be explicit about the transaction intent.
```

### Withdraw Savers

Withdraw an asset from THORChain Savers.

**`WITHDRAW:POOL:BASISPOINTS`**

| Parameter      | Notes                                                                                       | Conditions                                       |
| -------------- | ------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| Payload        | Send the dust threshold of the asset to cause the transaction to be picked up by THORChain. | [Dust thresholds](#dust-thresholds) must be met. |
| `WITHDRAW`     | The withdraw handler.                                                                       | Also `-` or `wd`                                 |
| `:POOL`        | The pool to withdraw liquidity from.                                                        | Gas and stablecoin pools only.                   |
| `:BASISPOINTS` | Basis points.                                                                               | Optional. Range 0-10000, where 10000 = 100%.     |

**Examples:**

- `WITHDRAW:BTC/BTC:10000` &mdash; withdraw 100% from BTC Savers
- `-:ETH/ETH:5000` &mdash; withdraw 50% from ETH Savers
- `wd:BTC/BTC:1000` &mdash; withdraw 10% from BTC Savers

```admonish info
Withdrawing from Savers can be also be done [without a memo](../archived/saving-guide/quickstart-guide.md#basic-mechanics).
```

### Deposit RUNEPool

Deposit RUNE to the RUNEPool

**`POOL+`**

| Parameter | Notes                 | Conditions                                                               |
| --------- | --------------------- | ------------------------------------------------------------------------ |
| Payload   | THOR.RUNE             | Use `MsgDeposit`. The amount of RUNE in the tx will be added to RunePool |
| `POOL+`   | The RUNEPool handler. |                                                                          |

### Withdraw RUNEPool

**`POOL-:BASISPOINTS:AFFILIATE:FEE`**

| Parameter      | Notes                                                                        | Conditions                                    |
| -------------- | ---------------------------------------------------------------------------- | --------------------------------------------- |
| Payload        | None required.                                                               | Use `MsgDeposit`.                             |
| `POOL-`        | The The RUNEPool handler.                                                    |                                               |
| `:BASISPOINTS` | Basis points.                                                                | Required. Range 0-10000, where 10000 = 100%.  |
| `:AFFILIATE`   | The affiliate address.                                                       | Optional. Must be a THORName or THOR Address. |
| `:FEE`         | The [affiliate fee](../affiliate-guide/affiliate-fee-guide.md#how-it-works). | Optional. Ranges from 0 to 1000 Basis Points. |

Example: `POOL-:10000:dx:10` - 100% Withdraw from RUNEPool with a 10 basis point affiliate fee. Affiliates receive the corresponding basis points of positive PnL. If user is withdrawing with a loss, the affiliate will receive no fee.

### DONATE & RESERVE

Donate to a pool.

**`DONATE:POOL`**

| Parameter | Notes                                    | Conditions                                            |
| --------- | ---------------------------------------- | ----------------------------------------------------- |
| Payload   | The asset to donate to a THORChain pool. | Must be supported by THORChain. Can be RUNE or ASSET. |
| `DONATE`  | The donate handler.                      | Also `d`                                              |
| `:POOL`   | The pool to withdraw liquidity from.     | Can be shortened.                                     |

**Examples:**

- `DONATE:ETH.ETH` &mdash; donate to the ETH pool

**`RESERVE`**

Donate to the THORChain Reserve.

| Parameter | Notes                | Conditions                                   |
| --------- | -------------------- | -------------------------------------------- |
| Payload   | THOR.RUNE            | The RUNE to credit to the THORChain Reserve. |
| `RESERVE` | The reserve handler. |                                              |

### REFERENCE MEMO - Memoless Transactions

Memoless transactions allow users to register a memo on THORChain and then reference it in future transactions using a short reference number encoded in the transaction amount. This enables transactions on chains with limited memo space (like Bitcoin's 80-byte OP_RETURN limit) by eliminating the need to include lengthy memos.

For detailed information, see [Memoless Transactions](../swap-guide/memoless-swaps.md).

#### Registering a Reference Memo

Register a memo for future memoless transactions.

**`REFERENCE:ASSET:MEMO`**

| Parameter   | Notes                                                   | Conditions                                           |
| ----------- | ------------------------------------------------------- | ---------------------------------------------------- |
| Payload     | THOR.RUNE                                               | Use `MsgDeposit`.                                    |
| `REFERENCE` | The reference memo registration handler.                | Also `reference`                                     |
| `:ASSET`    | The [asset identifier](asset-notation.md) for the memo. | Must be an active asset on THORChain.                |
| `:MEMO`     | The memo to register for memoless transactions.         | Any valid THORChain memo (swap, add liquidity, etc.) |

#### Use Reference Memo

Use a previously registered reference memo explicitly in a transaction.

**`R:REFERENCE`**

| Parameter    | Notes                                              | Conditions                                                                                    |
| ------------ | -------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| Payload      | The asset for the memoless transaction.            | Must match the asset registered in the memo. [Dust thresholds](#dust-thresholds) must be met. |
| `R`          | The reference memo usage handler.                  | Also `r`                                                                                      |
| `:REFERENCE` | The reference number assigned during registration. | Must be a valid, non-expired reference number.                                                |

**Important**: Before broadcasting, **always** call `GET /thorchain/memo/check/{asset}/{amount}` to validate:

- Reference exists and is not expired
- Usage limit not exceeded
- Memoless functionality not halted

**Transaction Flow:**

1. User sends RUNE with `REFERENCE:BTC.BTC:=:ETH.ETH:0xADDRESS` memo
2. THORChain assigns a reference number (e.g., 20002)
3. User can now use this reference for future BTC transactions. Reference expires post [`MemolessTxnTTL`](../mimir.md#memoless-transactions), approx. 6 hours

See [Memoless Swaps](../swap-guide/memoless-swaps.md) for detailed information.

#### Encoding Examples

**Bitcoin Example (8 decimals):**

- Reference: 20002
- Want to send: 0.05 BTC = 5000000
- Amount: 5000000 + 20002 = 5020002 satoshis
- Verification: 5020002 % 100000 = 20002 ✓

**Lower Decimal Asset Example - USDC (6 decimals):**

- Reference: 20002
- Want to send: 1500 units = 1500000000
- Amount: 1,500,000,000 + 20002 = 1,500,020,002 native units
- Verification: 1,500,020,002 % 100000 = 20002 ✓

**Configuration:**

- **TTL (Time To Live)**: `MemolessTxnTTL` - Reference memos expire after a configurable number of blocks (default: 3600 blocks ≈ 6 hours)
- **Reference Range**: `MemolessTxnRefCount` - Reference numbers range from 1 to this value (default: 99999). **CAUTION:** Changing this value changes the zero-padding normalization of reference keys. Existing non-expired references become inaccessible. Only change after all references have expired or with a coordinated migration.
- **Cost**: `MemolessTxnCost` - Additional protocol-level fee, currently set to 0. Scaled up by `MemolessTxnCostSlopeBPS` as the asset's reference slots fill, so registering into a congested ring costs more (see [Mimir](../mimir.md))
- **Max Usage**: `MemolessTxnMaxUse` - Maximum number of times a reference can be used (default: 1)
- **HaltMemoless**: Operational mimir to emergency halt all memoless functionality

**Examples:**

Registration:

- `REFERENCE:BTC.BTC:=:ETH.ETH:0x1c7b17362c84287bd1184447e6dfeaf920c31bbe`
  - Registers a swap from BTC to ETH memo for the specified address
  - Returns reference number (e.g., 20002)
  - Cost is only deducted if registration succeeds

Usage (Explicit):

- `R:20002` - Uses the registered memo with reference number 20002
- The actual swap will be executed using the originally registered memo

Usage (Amount-Encoded):

- Send `0.00020002 BTC` with empty memo
- THORChain extracts reference `20002` and resolves the registered swap memo
- Swap executes to ETH address `0x1c7b17362c84287bd1184447e6dfeaf920c31bbe`

**API Endpoints:**

- `GET /thorchain/memo/{asset}/{reference}` - Retrieve memo by asset and reference number
- `GET /thorchain/memo/{registration_hash}` - Retrieve memo by registration transaction hash
- `GET /thorchain/memo/check/{asset}/{amount}` - Pre-flight check: preview reference from amount and check availability

#### Reference Memo Lifecycle

Reference memos progress through distinct states from creation to expiration:

| **State**     | **Condition**                                    | **API Response**           | **User Action**  |
| ------------- | ------------------------------------------------ | -------------------------- | ---------------- |
| **CREATED**   | `Height > 0`                                     | Returns reference details  | Can start using  |
| **ACTIVE**    | `Height + TTL > current`<br>AND<br>`usage < max` | Returns valid memo         | Normal usage     |
| **USED**      | `UsedByTxs` contains TxIDs                       | Returns memo + usage count | Monitor limits   |
| **EXHAUSTED** | `usage >= MemolessTxnMaxUse`                     | Returns empty → refund     | Must re-register |
| **EXPIRED**   | `Height + TTL < current`                         | Returns 404/empty → refund | Must re-register |
| **HALTED**    | `HaltMemoless` mimir > 0                         | Returns empty → refund     | Wait for unhalt  |

#### Emergency Halt System

**HaltMemoless Mimir**:

- **Purpose**: Emergency brake for security issues or bugs
- **Effect**: ALL memoless transactions rejected instantly
- **Monitoring**: Check [mimir endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir)
- **Procedures**: See [Emergency Procedures](https://docs.thorchain.org/thornodes/emergency-procedures) for network halt operations

**Pre-Flight Checking:**

Before sending a memoless transaction, wallets should use the pre-flight API to verify:

- What reference will be extracted from the transaction amount
- Whether the reference is available or already registered
- When an existing registration expires
- Usage count vs. max usage limit
- Whether a new registration would succeed

### BOND, UNBOND, REBOND and LEAVE

Perform node maintenance features. Also see [Pooled Nodes](https://docs.thorchain.org/thornodes/pooled-thornodes).

**`BOND:NODEADDR:PROVIDER:FEE`**

| Parameter   | Notes                                    | Conditions                                                                             |
| ----------- | ---------------------------------------- | -------------------------------------------------------------------------------------- |
| Payload     | THOR.RUNE                                | The asset to bond to a Node.                                                           |
| `BOND`      | The bond handler.                        |                                                                                        |
| `:NODEADDR` | The node to bond with.                   |                                                                                        |
| `:PROVIDER` | Whitelist in a provider.                 | Optional. Add a provider.                                                              |
| `:FEE`      | Specify an Operator Fee in Basis Points. | Optional. Default will be the mimir value (2000 Basis Points). Can be changed anytime. |

**`UNBOND:NODEADDR:AMOUNT:PROVIDER`**

| Parameter   | Notes                    | Conditions                                                                           |
| ----------- | ------------------------ | ------------------------------------------------------------------------------------ |
| Payload     | None required.           | Use `MsgDeposit`.                                                                    |
| `UNBOND`    | The unbond handler.      |                                                                                      |
| `:NODEADDR` | The node to unbond from. | Must be in standby only and not churning out.                                        |
| `:AMOUNT`   | The amount to unbond.    | In 1e8 format. If setting more than actual bond, then capped at bond.                |
| `:PROVIDER` | Remove a bond provider.  | Optional. Node operator only. Removes the provider from the bond provider whitelist. |

**`REBOND:NODEADDR:NEWADDR:AMOUNT`**

Migrate bonded RUNE to a different whitelisted address on the same node

| Parameter   | Notes                                   | Conditions                                                                                            |
| ----------- | --------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Payload     | None required                           | Use `MsgDeposit`                                                                                      |
| `REBOND`    | The rebond handler.                     |                                                                                                       |
| `:NODEADDR` | The node to rebond from.                | Cannot be in `Unknown` state.                                                                         |
| `:NEWADDR`  | The address to receive the bonded RUNE. | Must be whitelisted and on the same node.                                                             |
| `:AMOUNT`   | The amount to migrate.                  | Optional. Default is 0. In 1e8 format. If setting is 0 or more than actual bond, then capped at bond. |

**`LEAVE:NODEADDR`**

| Parameter   | Notes                       | Conditions                                                                                               |
| ----------- | --------------------------- | -------------------------------------------------------------------------------------------------------- |
| Payload     | None required.              | Use `MsgDeposit`.                                                                                        |
| `LEAVE`     | The leave handler.          |                                                                                                          |
| `:NODEADDR` | The node to force to leave. | If in Active, request a churn out to Standby for 1 churn cycle. If in Standby, forces a permanent leave. |

**Examples:**

- `BOND:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a`
- `UNBOND:thor1x2whgc2nt665y0kc44uywhynazvp0l8tp0vtu6:750000000000`
- `LEAVE:thor1hlhdm0ngr2j4lt8tt8wuvqxz6aus58j57nxnps`

### Bond Yield

Configure automatic payouts from a bond provider's future reward share. Bond Yield does not withdraw existing bond or pay accrued yield when the memo is submitted. At reward distribution, the configured share is paid out and the remainder is added to the provider's bond. The feature must be enabled by the `BondYieldEnabled` Mimir.

**`BONDYIELD:NODEADDR:BPS:PAYOUTTARGET`**

| Parameter       | Notes                                                                  | Conditions                                                                                                                                           |
| --------------- | ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Payload         | None required.                                                         | Use `MsgDeposit`; a non-zero coin amount is rejected.                                                                                                |
| `BONDYIELD`     | The Bond Yield configuration handler.                                  |                                                                                                                                                      |
| `:NODEADDR`     | The node whose provider payout configuration is being changed.         | The node must be `Active`, `Standby`, or `Ready`.                                                                                                    |
| `:BPS`          | Basis-point share of future provider rewards to pay out automatically. | Required integer from `0` to `10000`. For example, `4000` pays out 40% and rebonds 60%. `0` disables Bond Yield and clears the stored payout target. |
| `:PAYOUTTARGET` | A THORChain address or THORName used for the automatic payout.         | Optional. The transaction must be sent by an existing bond provider for the node. THORNames must exist and be owned by that provider.                |

The node operator fee is deducted before rewards are allocated among bond providers. Bond Yield then applies to the submitting provider's allocated reward:

- `payout amount = provider reward share × BPS / 10000`
- `rebonded amount = provider reward share - payout amount`

If a payout fails, its amount is rebonded during the same reward distribution cycle.

Payout targets behave as follows:

- No stored target pays native RUNE directly to the bond provider.
- A THORChain address pays native RUNE to that address. Network module addresses are not allowed.
- A THORName without a preferred asset pays native RUNE to its THORChain alias, or to its owner when no THORChain alias is set.
- A THORName with a preferred asset credits that THORName's affiliate collector; conversion and delivery then follow the preferred-asset collector rules.
- THORName ownership is checked both when configuring the target and when making a payout.

For a non-zero BPS, omitting `PAYOUTTARGET` or leaving it empty preserves the currently stored target. If no target was previously stored, the payout goes directly to the provider. To clear an existing target while keeping Bond Yield enabled, send two transactions: first `BONDYIELD:NODEADDR:0`, then `BONDYIELD:NODEADDR:BPS` with the desired non-zero BPS. A target supplied with BPS `0` is ignored and cleared.

**Examples:**

- `BONDYIELD:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a:4000` &mdash; pay out 40% while preserving the current target; use the provider address when no target is stored.
- `BONDYIELD:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a:4000:thor1x2whgc2nt665y0kc44uywhynazvp0l8tp0vtu6` &mdash; pay 40% as native RUNE to a THORChain address.
- `BONDYIELD:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a:4000:yieldname` &mdash; pay 40% through a provider-owned THORName.
- `BONDYIELD:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a:0` &mdash; disable automatic payouts and clear the stored target.

### Maintenance Mode

Toggle a node's maintenance flag. When in maintenance mode, the node is excluded from churn cycles and will not be slashed for not signing. The node operator sends this as a `MsgDeposit`.

**`MAINT:NODEADDR`**

| Parameter   | Notes                                          | Conditions        |
| ----------- | ---------------------------------------------- | ----------------- |
| Payload     | None required.                                 | Use `MsgDeposit`. |
| `MAINT`     | The maintenance mode toggle handler.           |                   |
| `:NODEADDR` | The node address to toggle maintenance on/off. |                   |

**Example:**

- `MAINT:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a` &mdash; toggle maintenance mode for the specified node

### Operator Rotate

**`OPERATOR:NEWOPADDR`**

The operator address can send a `OPERATOR` memo to change the operator address for all their nodes. No nodes for the old or new operator can be active at the time of operator rotation. Operator rotation can only occur in the first half of churn cycle.

Operator rotation changes the controlling address but preserves the existing bond-provider record, including any Bond Yield BPS and payout target. Review and update those settings from the new operator address before the node becomes active again when the payout destination should also change.

| Parameter    | Notes                        | Extra             |
| ------------ | ---------------------------- | ----------------- |
| Payload      | None required.               | Use `MsgDeposit`. |
| `OPERATOR`   | The operator rotate handler. |                   |
| `:NEWOPADDR` | The new operator address.    |                   |

### MIGRATE

Internal memo type used to mark migration transactions between a retiring vault and a new Asgard vault during churn. Special THORChain triggered outbound tx without a related inbound tx.

**`MIGRATE:BLOCKHEIGHT`**

| Parameter      | Notes                              | Conditions                    |
| -------------- | ---------------------------------- | ----------------------------- |
| Payload        | Assets migrating.                  |                               |
| `MIGRATE`      | The migrate handler.               |                               |
| `:BLOCKHEIGHT` | THORChain block height to migrate. | Must be a valid block height. |

**Example:**

- `MIGRATE:3494355` &mdash; migrate at height 3494355. See a [real-world example on RuneScan](https://runescan.io/tx/8330CAC064370F86352D247DE3046C9AA8C3E53C78760E5D35CFC7CAA3068DC6)

### NOOP

Dev-centric functions used to fix THORChain state.

```admonish danger
May cause loss of funds if not performed correctly and at the right time.
```

**`NOOP:NOVAULT`**

| Parameter  | Notes                           | Conditions                                               |
| ---------- | ------------------------------- | -------------------------------------------------------- |
| Payload    | The asset to credit to a vault. | Must be ASSET or RUNE.                                   |
| `NOOP`     | The no-op handler.              | Adds to the vault balance, but does not add to the pool. |
| `:NOVAULT` | Do not credit the vault.        | Optional. Just fix the insolvency issue.                 |

The `:NOVAULT` debit only applies while the operational mimir `AllowNoVault-<CHAIN>` is `1` (or a block height greater than or equal to the current height). Prefer the block-height form: it is the only variant that closes itself. When the gate is closed the vault is credited as with a plain `NOOP`.

## Refunds

The following are the conditions for refunds:

| Condition                | Conditions                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------ |
| Invalid `MEMO`           | If the `MEMO` is incorrect the user will be refunded.                                                        |
| Invalid Assets           | If the asset for the transaction is incorrect (adding an asset into a wrong pool) the user will be refunded. |
| Invalid Transaction Type | If the user is performing a multi-send vs a send for a particular transaction, they are refunded.            |
| Exceeding Price Limit    | If the final value achieved in a trade differs to expected, they are refunded.                               |

Refunds cost fees to prevent DoS (denial-of-service) attacks. The user will pay the correct outbound fee for that chain. Refund memo is sent within a outbound transaction.

## **Other Internal Memos**

- `consolidate` &mdash; consolidate UTXO transactions
- `name` or `n` or `~` &mdash; THORName operations; see [THORName Guide](../affiliate-guide/thorname-guide.md)
- `out` &mdash; for outbound transaction, set within a outbound transaction
- `ragnarok` &mdash; used to delist pools, set within a outbound transaction
- `switch` &mdash; convert external tokens to native THORChain assets; see [Switch Asset](#switch-asset)
- `yggdrasil+` and `yggdrasil-` &mdash; Yggdrasil vault operations (deprecated; see [ADR002](../architecture/adr-002-removeyggvaults.md))
