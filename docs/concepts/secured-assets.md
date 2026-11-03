# Secured Assets

CosmWasm-compatible assets, backed by native L1 deposits

Secured Assets allow L1 tokens to be deposited to THORChain, creating a new native asset, which can be transferred between accounts and integrated with CosmWasm smart contracts using standard Cosmos SDK messages. IBC is not enabled on THORChain. They also replace [Trade Accounts](../concepts/trade-accounts.md) in providing professional traders (mostly arbitrage bots) a method to execute instant trades on THORChain without involving Layer1 transactions on external blockchains.

Arbitrage bots can arbitrage the pools faster and with more capital efficiency than Synthetics can. This is because Synthetics adds or removes from one side of the pool depth but not the other, causing the pool to move only half the distance in terms of price. For example, a $100 RUNE --> BTC swap requires $200 of Synthetic BTC to be burned to correct the price. Secured Assets have twice the efficiency, so a $100 RUNE --> BTC swap would require $100 from Secured Assets to correct the price. This allows arbitrageurs to quickly restore big deviations using less capital.

## How it Works

1. Token holders deposit Layer1 assets into the network, minting a [Secured Asset](./asset-notation.md#secured-assets).
1. The balance of the Secured Asset represents shares of the deposit asset pool depth.
1. Holders can swap Secured Assets <> RUNE (or other secured asset). The swapping process is the same however the minimum swap fee is governed by [`SecuredAssetSlipMinBps`](../mimir.md#swapping)
1. Holders can execute Smart Contracts on the app-layer with Secured Assets, eg placing limit orders or depositing to a lending pool.
1. Holders can withdraw some or all of their balance by redeeming the share token for a withdrawal of the respresentative share. [Outbound delay](./delays.md) applies when they withdraw.

RUNE, Synthetics and Trade Assets cannot be converted to a Secured Asset.

## Security

As Secured Assets are not held in the pools, the combined pool and secured asset value (combined Layer1 asset value) could exceed the total bonded. To ensure this does not occur:

1. The calculation of the [Incentive Pendulum](../concepts/incentive-pendulum.md) now operates based on Layer1 assets versus bonds, rather than solely on pool depths versus bonds. This ensures there is always "space" for arbitrageurs to exist in the network and be able to arbitrage pools effectively (versus synths hitting caps).
1. Before Secured Assets are created, a check is done to ensure there is sufficient security budget to secure them.

## Using Secured Assets

Secured Assets can be used by creating transaction memos for [adding](./memos.md#add-secured-asset), [swapping](./memos.md#swap) and [withdrawing](./memos.md#withdraw-secured-asset).

### Minting Secured Assets

Send Layer1 Asset to the [Inbound Address](./querying-thorchain.md#getting-the-asgard-vault) with the memo:
**`SECURE+:THORADD`**.

**Example:**

`SECURE+:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz` - Add the shares for the sent asset and amount to the account's Secured Asset balance.

The Layer1 asset is converted at the current share ratio to a secured asset, updating the account's balance.

```admonish info
The owner's THORChain Address must be specified.
```

### Swapping Secured Assets

The [swap memo](./memos.md#swap) is used when swapping to and from Secured Assets.

**Examples:**

- `=:ETH-ETH:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz` &mdash; Swap (from RUNE) to Ether Secured Asset
- `=:BTC-BTC:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz` &mdash; Swap (from ETH-ETH) to Bitcoin Secured Asset
- `=:BTC-BTC:thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz:1e6/1/0:dx:10` &mdash; - Swap to Bitcoin Secured Asset, using a Limit, Streaming Swaps and a 10 basis point fee to the affiliate `dx` (Asgardex)

```admonish info
The destination/receiving address of the Secured Assets MUST be a THORChain Address!
```

### Withdrawing Secured Assets

Send a THORChain MsgDeposit with the memo **`SECURE-:ADDR`**.

**Example:**

`SECURE-:bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw` - Redeem 0.1 BTC Secured Asset shares and withdraw them to `bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw`. The amount in the coins array defines the redeemed share amount. The withdrawal amount will be `redeem shares * current pool ratio`.

```json
{
  "body": {
    "messages": [
      {
        "": "/types.MsgDeposit",
        "coins": [
          {
            "asset": "BTC-BTC",
            "amount": "10000000",
            "decimals": "0"
          }
        ],
        "memo": "secure-:bc1qp8278yutn09r2wu3jrc8xg2a7hgdgwv2gvsdyw",
        "signer": "thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz"
      }
    ],
    "memo": "",
    "timeout_height": "0",
    "extension_options": [],
    "non_critical_extension_options": []
  },
  "auth_info": {
    "signer_infos": [],
    "fee": {
      "amount": [],
      "gas_limit": "200000",
      "payer": "",
      "granter": ""
    }
  },
  "signatures": []
}
```

### Verify Secured Account Balances

Balances can be verified using the owner's THORChain Address via the `cosmos/bank/v1beta1/balances` [endpoint](./connecting-to-thorchain.md#thornode).

**Example:**

<https://gateway.liquify.com/chain/thorchain_api/cosmos/bank/v1beta1/balances/thor1g6pnmnyeg48yc3lg796plt0uw50qpp7humfggz>:

```json
{
  "balances": [
    {
      "asset": "BTC-BTC",
      "amount": "49853"
    },
    {
      "asset": "ETH-ETH",
      "amount": "49853"
    }
  ]
}
```
