# Fees and Wait Times

## Fee Types

Users pay up to four kinds of fees when conducting a swap.

1. **Layer1 Network Fees** (gas): paid by the user when sending the asset to THORChain to be swapped. This is controlled by the user's wallet.
2. **Slip Fee**: protects the pool from being manipulated by large swaps. Calculated as a function of transaction size and current pool depth. The slip fee formula is explained [here](https://docs.thorchain.org/technical-documentation/thorchain-finance/continuous-liquidity-pools#clp-derivation), and a [minimum applies](../concepts/fees.md#minimum-swap-fee) when it falls below the floor for the asset type.
3. **Affiliate Fee** - (optional) a percentage skimmed from the inbound amount that can be paid to exchanges or wallet providers. See the [Affiliate Fee Guide](../affiliate-guide/affiliate-fee-guide.md) and the [Fees](../concepts/fees.md#affiliate-fee) section for more details and setup information.
4. **Outbound Fee** - the fee the Network pays on behalf of the user to send the outbound transaction. See [Outbound Fee](../concepts/fees.md#outbound-fee).

```admonish info
The Swap Quote endpoint will calculate and show all fees.
```

See the [fees](../concepts/fees.md) section for full details.

### Refunds and Minimum Swap Amount

If a transaction fails, it is refunded, thus it will pay the `outboundFee` for the **SourceChain** not the DestinationChain. Thus devs should always swap an amount that is a maximum of the following, multiplied by at least a 4x buffer to allow for gas spikes:

- The Destination Chain outboundFee, or
- The Source Chain outboundFee, or
- The minimum outbound fee, set by the `MinimumL1OutboundFeeUSD` Mimir (currently $0.25 on mainnet).

```admonish info
This value is set by node operators via Mimir and may change. See [Constants and Mimirs](../mimir.md) for current values.
```

For convenience, a `recommended_min_amount_in` is included on the [Swap Quote](../swap-guide/quickstart-guide.md#2-query-for-a-swap-quote) endpoint, which is the value described above. This value is priced in the inbound asset of the quote request (in 1e8). This should be the minimum-allowed swap amount for the requested quote. If a swap is attempted and fails due to insufficient funds to pay for fees, the endpoint will return an error message that includes the recommended minimum amount.

## Wait Times

There are five phases of a transaction sent to THORChain each taking time to complete.

1. **Layer1 Inbound Confirmation** - assuming the inboundTx will be confirmed in the next block, it is the source blockchain block time.
2. **Observation Counting** - time for 67% THORChain Nodes to observe and agree on the inboundTx.
3. **Confirmation Counting** - for non-instant finality blockchains, the amount of time THORChain will wait before processing to protect against double spends and re-org attacks.
4. **Outbound Delay** - dependent on size and network traffic. Large outbounds will be delayed.
5. **Layer1 Outbound Confirmation** - Outbound blockchain block time.

Wait times can be between a few seconds up to an hour. The assets being swapped, the size of the swap and the current network traffic within THORChain will determine the wait time.

```admonish info
The Swap Quote endpoint will calculate points 3 and 4.
```

See the [delays.md](../concepts/delays.md "mention") section for full details.
