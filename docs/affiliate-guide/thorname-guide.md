# THORName Guide

## Summary

[THORNames](https://docs.thorchain.org/technical-documentation/technical-deep-dive/thorchain-name-service) are THORChain's vanity address system that allows affiliates to collect fees and track their user's transactions. THORNames exist on the THORChain L1, so you will need a THORChain address and $RUNE to create and manage a THORName.

THORNames have the following properties:

- **Name:** The THORName's string. New THORNames must have 1-30 alphanumeric characters or `-_+` special characters. Existing registrations keep their THORNames under earlier naming rules.
- **Owner**: This is the THORChain address that owns the THORName
- **Aliases**: THORNames can have an alias address for any external chain supported by THORChain, and can have an alias for the THORChain L1 that is different than the owner.
- **Expiry:** THORChain Block-height at which the THORName expires.
- **Preferred Asset:** The asset to pay out affiliate fees in. This can be any asset supported by THORChain.

## Create a THORName

THORNames are created by posting a `MsgDeposit` to the THORChain network with the appropriate [memo](../concepts/memos.md) and enough $RUNE to cover the registration fee and to pay for the amount of blocks the THORName should be registered for.

- **Registration fee**: `tns_register_fee_rune` on the [Network endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/network). This value is in 1e8, so `100000000 = 1 $RUNE`
- **Per block fee**: `tns_fee_per_block_rune` on the same endpoint, also in 1e8.

For example, for a new THORName to be registered for 10 years the amount paid would be:

`amt = tns_register_fee_rune + tns_fee_per_block_rune * 10 * 5256000`

`5256000 = avg # of blocks per year`

The expiration of the THORName will automatically be set to the number of blocks in the future that was paid for minus the registration fee.

**Memo Format:**

Memo template is: `~:name:chain:address:?owner:?preferredAsset:?expiry:?preferredAssetOutboundFeeMultiplier`

- **name**: Your THORName. New THORNames must be unique, between 1-30 characters, alphanumeric and `-_+` special characters. Existing renewal and re-registration rules are unchanged: historical spellings that fail current validation cannot be topped up or re-registered after expiry.
- **chain:** The chain of the alias to set.
- **address**: The alias address. Must be an address of chain.
- **owner**: THORChain address of owner (optional). New registrations and ownership transfers cannot assign a THORChain module account as owner.
- **preferredAsset:** Asset to receive fees in. Must be supported by an active pool on THORChain. Value should be `asset` property from the [Pools endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/pools).
- **preferredAssetOutboundFeeMultiplier:** Optional parameter. Custom multiplier for the outbound fee threshold for affiliate fee payouts. This enables affiliate fees to get paid out at a custom threshold. If not specified, uses the global default of 100. Must be between 0 and 10000 (0 resets to global default). This determines how much RUNE must accumulate before triggering a payout in your preferred asset.
  - Example: Assuming default of 100, if your preferred asset is ETH.USDC and the ETH.USDC outbound fee is $1, the RUNE gets swapped to ETH.USDC and sent to your address when it hits $100 (100 \* $1).

```admonish warning
Changing the owner of a THORName resets the preferred asset and all chain aliases. The new owner must reconfigure them after taking ownership.

The available collector balance is paid to the previous owner in native RUNE before a transfer or re-registration after expiry. If that payment fails, the ownership change fails too. Pending conversions and failed-payout refunds remain attached to the THORName and can arrive after the ownership change.
```

```admonish info
Example: `~:ODIN:BTC:bc1Address:thorAddress:BTC.BTC`
Example with custom multiplier: `~:ODIN:BTC:bc1Address:thorAddress:BTC.BTC::500`
```

This will register a new THORName called `ODIN` with a Bitcoin alias of `bc1Address` owner of `thorAddress` and preferred asset of BTC.BTC.

```admonish info
You can use [Asgardex](https://github.com/asgardex/asgardex-desktop) to post a MsgDeposit with a custom memo. Load your wallet, then open your THORChain wallet page > Deposit > Custom.
```

```admonish info
View your THORName's configuration at the THORName endpoint:

e.g. [https://gateway.liquify.com/chain/thorchain_api/thorchain/thorname/](https://gateway.liquify.com/chain/thorchain_api/thorchain/thorname/ac-test){name}
```

## Renewing your THORName

All THORNames have an expiration represented by the THORChain block height. After expiry, another THORChain address can register the THORName if it meets current naming rules. Its available collector balance is paid to the previous owner first; pending conversions and failed-payout refunds continue to follow the THORName. Monitor the expiry height and renew your THORName as needed.

To keep your THORName registered you can extend the registration period (move back the expiration block height), by posting a `MsgDeposit` with the correct THORName memo and $RUNE amount.

**Memo:**

`~:ODIN:THOR:<thor-alias-address>`

_(Chain and alias address are required, so just use current values to keep alias unchanged)._

**$RUNE Amount:**

`rune_amt = num_blocks_to_extend * tns_fee_per_block`

_(Remember this value will be in 1e8, so adjust accordingly for your transaction)._

## Preferred Asset for Affiliate Fees

Affiliates can collect their fees in the asset of their choice (choosing from the assets that have a pool on THORChain). In order to collect fees in a preferred asset, affiliates must use a [THORName](https://docs.thorchain.org/technical-documentation/technical-deep-dive/thorchain-name-service) in their swap memos.

```admonish warning
If a preferred asset is set but the THORName has no chain alias for that asset's chain, payouts will silently fail and RUNE will remain stuck in the `AffiliateCollector` module. Set the missing alias to unblock: `~:<thorname>:<CHAIN>:<address>`
```

### Configuring a Preferred Asset for a THORName

1. [**Register a THORName**](../affiliate-guide/thorname-guide.md#create-a-thorname) if not done already. This is done with a `MsgDeposit` posted to the THORChain network.
2. Set your preferred asset's chain alias (the address you'll be paid out to), and your preferred asset. _Note: your preferred asset must be currently supported by THORChain._

For example, if you wanted to be paid out in USDC you would:

1. Grab the full USDC name from the [Pools](https://gateway.liquify.com/chain/thorchain_api/thorchain/pools) endpoint: `ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48`
2. Post a `MsgDeposit` to the THORChain network with the appropriate memo to register your THORName, set your preferred asset as USDC, and set your Ethereum network address alias. Assuming the following info:

   1. THORChain address: `<your-thor-address>`
   2. THORName: `ac-test`
   3. ETH payout address: `0x6621d872f17109d6601c49edba526ebcfd332d5d`

   The full memo would look like:

   > `~:ac-test:ETH:0x6621d872f17109d6601c49edba526ebcfd332d5d:<your-thor-address>:ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48`

   To set a custom payout threshold (e.g., 50x the outbound fee instead of the default), add the multiplier:

   > `~:ac-test:ETH:0x6621d872f17109d6601c49edba526ebcfd332d5d:<your-thor-address>:ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48::50`

```admonish info
You can use [Asgardex](https://github.com/asgardex/asgardex-desktop) to post a MsgDeposit with a custom memo. Load your wallet, then open your THORChain wallet page > Deposit > Custom.
```

```admonish info
You will also need a THOR alias set to collect affiliate fees. Use another MsgDeposit with memo: `~:<thorname>:THOR:<thorchain-address>` to set your THOR alias. Your THOR alias address can be the same as your owner address, but won't be used for anything if a preferred asset is set.
```

Once you successfully post your MsgDeposit you can verify that your THORName is configured properly. View your THORName info from THORNode at the following endpoint:\
[https://gateway.liquify.com/chain/thorchain_api/thorchain/thorname/ac-test](https://gateway.liquify.com/chain/thorchain_api/thorchain/thorname/ac-test)

The response should look like:

```json
{
  "name": "ac-test",
  "expire_block_height": 28061405,
  "owner": "<your-thor-address>",
  "preferred_asset": "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
  "affiliate_collector_rune": "0",
  "aliases": [
    {
      "chain": "ETH",
      "address": "0x6621d872f17109d6601c49edba526ebcfd332d5d"
    },
    {
      "chain": "THOR",
      "address": "<your-thor-address>"
    }
  ],
  "preferred_asset_swap_threshold_rune": "0"
}
```

Your THORName is now properly configured and any affiliate fees will begin accruing in the AffiliateCollector module. You can verify that fees are being collected by checking the `affiliate_collector_rune` value of the above endpoint.

Each THORName has its own collector, even when multiple THORNames share an owner. The endpoint reports only that THORName's balance. Preferred-asset payouts use that THORName's alias for the payout chain.

## Upgrade from shared owner collectors

Module migration 18 to 19 switches all collectors automatically; there is no migration memo or opt-in mode. For an owner with exactly one stored THORName whose spelling meets current registration rules, the existing collector balance moves to that THORName's collector without a wallet payment or fee. Expired and historical THORNames count too: an owner with one active THORName and one expired THORName has two THORNames. Owners with multiple stored THORNames, no remaining THORNames, or a sole THORName whose spelling fails current registration rules receive their nonzero shared balance directly at their THORChain address in one combined native RUNE payment, without an outbound fee; their new per-THORName collectors start with zero. Legacy balances owned by module accounts go to Reserve instead.

Historical THORName records remain unchanged. Paying their legacy balance avoids assigning it to a THORName that cannot be renewed or re-registered. Later conversions or refunds still follow the THORName and may leave an inaccessible balance after expiry; this migration does not add a withdrawal mechanism or change registration eligibility.

Before distributing legacy balances, migration reconciles the collector module's actual RUNE balance with existing legacy and THORName claims. Reserve covers any shortfall or receives any surplus in a one-time transfer, recorded in an `affiliate_collector_migration_reconcile` event with `from_module`, `to_module`, and `rune_amount`. This adjustment is separate from the full transfer of module-owned legacy balances to Reserve. The adjustment and migration roll back together if any transfer fails.

Existing THORName owners, paid registration periods, aliases, and preferred-asset settings are preserved. The migration emits `affiliate_collector_migration` events with the owner and RUNE amount, plus a `thorname` attribute when the balance moves to a THORName collector or a `destination` address when it moves to Reserve.

Migration uses ownership at the upgrade block, not a reconstruction of historical earnings. The combined owner payment does not identify which THORName historically earned the money. Reconciliation and distribution between partners require a separate off-chain analysis of transaction history. These owner payments use RUNE rather than any THORName's preferred asset; balances kept in a single THORName's collector retain its normal payout settings. Funds already committed to pending swaps are not included in the migrated balance and continue through their existing queues.

The upgrade operates on the existing chain database. New genesis exports contain THORName collectors. Existing THORName genesis validation is unchanged: exported historical THORNames that fail current spelling or length rules still fail validation before import. Collector validation accepts historical spellings but rejects empty THORNames, duplicate collector keys and invalid amounts. Historical genesis files with owner-based collectors require separate conversion before import. Malformed records or any failed transfer abort the migration without committing any THORName credits, payments, or legacy-record deletions.

The collector protobuf retains `owner_address` as a deprecated compatibility field. New collectors leave it empty; accounting uses the THORName.

Queued conversions continue after the upgrade and credit their THORName collector on delivery. The old implementation executes affiliate conversions in one shot and removes them from the queue in the same block, including on delivery failure, so migration does not scan or settle conversions.
