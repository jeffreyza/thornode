# Quickstart Guide

## Introduction

THORChain allows native L1 Swaps. On-chain [Memos](../concepts/memos.md) are used instruct THORChain how to swap, with the option to add [price limits](quickstart-guide.md#price-limits) and [affiliate fees](quickstart-guide.md#affiliate-fees). THORChain nodes observe the inbound transactions and when the majority have observed the transactions, the transaction is processed by threshold-signature transactions from THORChain vaults.

Let's demonstrate decentralised, non-custodial cross-chain swaps. In this example, we will build a transaction that instructs THORChain to swap native Bitcoin to native Ethereum in one transaction.

```admonish info
For chains with limited memo space (like Bitcoin's 80-byte OP_RETURN), see the [Memoless Swaps Guide](./memoless-swaps.md#memoless-swap-checklist) for reference memo registration and amount encoding.
```

```admonish info
The following examples use free, public endpoints. See [connecting-to-thorchain.md](../concepts/connecting-to-thorchain.md) for a full list of providers or to run your own full node.
```

### 1. Determine the correct asset name

THORChain uses a specific [asset notation](../concepts/asset-notation.md#layer-1-assets). Available assets are at: [Pools Endpoint.](https://gateway.liquify.com/chain/thorchain_api/thorchain/pools)

BTC => `BTC.BTC`\
ETH => `ETH.ETH`

```admonish info
Only available pools can be used. (`where 'status' == Available)`
```

### 2. Query for a swap quote

```admonish info
All amounts are 1e8. Multiply native asset amounts by 100000000 when dealing with amounts in THORChain. 1 BTC = 100,000,000.
```

**Request**: _Swap 1 BTC to ETH and send the ETH to_ `0x86d526d6624AbC0178cF7296cD538Ecc080A95F1` using [Streaming Swaps](./streaming-swaps.md), swapping every block (`streaming_interval=1`) and allowing THORNode to work out the optimal amount of blocks (`streaming_quantity=0`).

[https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&streaming_quantity=0](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&streaming_quantity=0)

**Response**:

```json
{
  "inbound_address": "bc1qt9723ak9t7lu7a97lt9kelq4gnrlmyvk4yhzwr",
  "inbound_confirmation_blocks": 1,
  "inbound_confirmation_seconds": 600,
  "outbound_delay_blocks": 179,
  "outbound_delay_seconds": 1074,
  "fees": {
    "asset": "ETH.ETH",
    "affiliate": "0",
    "outbound": "54840",
    "liquidity": "2037232",
    "total": "2092072",
    "slippage_bps": 9,
    "total_bps": 10
  },
  "slippage_bps": 41,
  "streaming_slippage_bps": 9,
  "expiry": 1722575316,
  "warning": "Do not cache this response. Do not send funds after the expiry.",
  "notes": "First output should be to inbound_address, second output should be change back to self, third output should be OP_RETURN, limited to 80 bytes. Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats (P2WSH with Bech32 address format preferred).",
  "dust_threshold": "10000",
  "recommended_min_amount_in": "10760",
  "recommended_gas_rate": "4",
  "gas_rate_units": "satsperbyte",
  "memo": "=:ETH.ETH:0x86d526d6624AbC0178cF7296cD538Ecc080A95F1:0/1/0",
  "expected_amount_out": "2035299208",
  "expected_amount_out_streaming": "2035299208",
  "max_streaming_quantity": 8,
  "streaming_swap_blocks": 7,
  "streaming_swap_seconds": 42,
  "total_swap_seconds": 1674
```

_If you send 1 BTC to `bc1qt9723ak9t7lu7a97lt9kelq4gnrlmyvk4yhzwr` with the memo `=:ETH.ETH:0x86d526d6624AbC0178cF7296cD538Ecc080A95F1:0/1/0`, you can expect to receive `20.35299208` ETH._

_For security reasons, your inbound transaction will be delayed by 600 seconds (1 BTC Block) and 1074 seconds (or 179 native THORChain blocks) for the outbound transaction,_ 2640 seconds all up*. You will pay an outbound gas fee of 0.0054 ETH and will incur 9 basis points (0.09%) of slippage due to streaming swaps, would be 41 bps without StreamingSwaps.*
The swap will be conduced over 7 blocks taking 42 seconds for the streaming swap to complete.

```admonish info
Streaming swaps are the default behavior when no `streaming_interval`/`streaming_quantity` is specified. See [Streaming Swaps](./streaming-swaps.md#default-behavior-change) to opt out or tune this.
```

```admonish info
Full quote swap endpoint specification can be found here: [https://gateway.liquify.com/chain/thorchain_api/thorchain/doc/](https://gateway.liquify.com/chain/thorchain_api/thorchain/doc/).
```

If you'd prefer to calculate the swap yourself, see the [Fees](fees-and-wait-times.md) section to understand what fees need to be accounted for in the output amount. Also, review the [Transaction Memos](../concepts/memos.md#swap) section to understand how to create the swap memos.

### 3. Sign and send transactions on the from_asset chain

Construct, sign and broadcast a transaction on the BTC network with the following parameters:

- Amount => `1.0`
- Recipient => `bc1qt9723ak9t7lu7a97lt9kelq4gnrlmyvk4yhzwr`
- Memo => `=:ETH.ETH:0x86d526d6624AbC0178cF7296cD538Ecc080A95F1:0/1/0`

```admonish error
Never cache inbound addresses! Quotes should only be considered valid for 10 minutes. Sending funds to an old inbound address will result in loss of funds.
```

```admonish info
Learn more about how to construct inbound transactions for each chain type here: [Sending Transactions](../concepts/sending-transactions.md)
```

### 4. Receive tokens

Once a majority of nodes have observed your inbound BTC transaction, they will sign the Ethereum funds out of the network and send them to the address specified in your transaction. You have just completed a non-custodial, cross-chain swap by simply sending a native L1 transaction.

## Additional Considerations

```admonish warning
There is a rate limit of 1 request per second per IP address on /quote endpoints. It is advised to put a timeout on frontend components input fields, so that a request for quote only fires at most once per second. If not implemented correctly, you will receive 503 errors.
```

```admonish success
For best results, request a new quote right before the user submits a transaction. This will tell you whether the _expected_amount_out_ has changed or if the _inbound_address_ has changed. Ensuring that the _expected_amount_out_ is still valid will lead to better user experience and less frequent failed transactions.
```

### Price Limits

Specify _liquidity_tolerance_bps_ to give users control over the maximum slip they are willing to experience before canceling the trade. If not specified, users can pay an unbounded amount of slip. The limit is calculated based on the value of _expected_amount_out_.

[https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000\&from_asset=BTC.BTC\&to_asset=ETH.ETH\&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430\&liquidity_tolerance_bps=500](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000&from_asset=BTC.BTC&to_asset=ETH.ETH&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430&liquidity_tolerance_bps=500)

`https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000&from_asset=BTC.BTC&to_asset=ETH.ETH&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430&liquidity_tolerance_bps=500`

Notice how a minimum amount (1342846539 / \~13.42 ETH) has been appended to the end of the memo. This tells THORChain to revert the transaction if the transacted amount is more than 500 basis points less than what the _expected_amount_out_ returns.

Note that the parameter _tolerance_bps_ can be used instead, but this field does not take swap or outbound fees into account when calculating the limit, which will result in more failed swaps. Instead, it calculates the price limit solely based on the value of the input asset.

### [Affiliate Fees](../affiliate-guide/affiliate-fee-guide.md)

Specify `affiliate` and `affiliate_bps` to skim a percentage of the swap as an affiliate fee. When a valid affiliate address and affiliate basis points are present in the memo, the protocol will skim affiliate_bps from the inbound swap amount and swap this to $RUNE with the affiliate address as the destination address. Affiliates may either be a RUNE address or a registered & un-expired THORName with a THORChain alias defined. If the THORName has a preferred asset set it must also have an alias for the preferred asset's chain. If an invalid, improperly configured, or expired THORName, or an invalid RUNE address is provided as an affiliate, the affiliate fee will be skipped.

Params:

- **affiliate**: Can be a THORName or valid THORChain address
- **affiliate_bps**: 0-1000 basis points

Memo format:
`=:BTC.BTC:<destination_addr>:<limit>:<affiliate>:<affiliate_bps>`

Quote example:

[https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000\&from_asset=BTC.BTC\&to_asset=ETH.ETH\&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430\&affiliate=dx\&affiliate_bps=10](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000&from_asset=BTC.BTC&to_asset=ETH.ETH&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430&affiliate=dx&affiliate_bps=10)

```json
{
  "inbound_address": "bc1qt9723ak9t7lu7a97lt9kelq4gnrlmyvk4yhzwr",
  "inbound_confirmation_blocks": 1,
  "inbound_confirmation_seconds": 600,
  "outbound_delay_blocks": 177,
  "outbound_delay_seconds": 1062,
  "fees": {
    "asset": "ETH.ETH",
    "affiliate": "2036420",
    "outbound": "54840",
    "liquidity": "8303006",
    "total": "10394266",
    "slippage_bps": 40,
    "total_bps": 51
  },
  "slippage_bps": 40,
  "streaming_slippage_bps": 40,
  "expiry": 1722575770,
  "warning": "Do not cache this response. Do not send funds after the expiry.",
  "notes": "First output should be to inbound_address, second output should be change back to self, third output should be OP_RETURN, limited to 80 bytes. Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats (P2WSH with Bech32 address format preferred).",
  "dust_threshold": "10000",
  "recommended_min_amount_in": "242563",
  "recommended_gas_rate": "4",
  "gas_rate_units": "satsperbyte",
  "memo": "=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::dx:10",
  "expected_amount_out": "2017703535",
  "expected_amount_out_streaming": "",
  "max_streaming_quantity": 0,
  "streaming_swap_blocks": 0,
  "total_swap_seconds": 1662
}
```

Notice how `dx:10` has been appended to the end of the memo. This instructs THORChain to skim 10 basis points from the swap. The user should still expect to receive the _expected_amount_out,_ meaning the affiliate fee has already been subtracted from this number.

For more information on affiliate fees: [Affiliate Fee Guide](../affiliate-guide/affiliate-fee-guide.md).

### Multiple Affiliates

Interfaces can define up to 5 valid affiliate and affiliate basis points pairs in a swap memo and the network will attempt to skim an affiliate fee for each. Alternatively, up to 5 valid affiliates and exactly one valid basis points can be defined, and the network will attempt to skim the same basis points fee for each affiliate.

**Note:** When using the `/quote/swap` endpoint with multiple affiliates, **the order of `affiliate` and `affiliate_bps` fields in the query string does not determine their order in the final memo**. The API will **automatically sort affiliate names alphabetically** when constructing the memo.

This means:

- The affiliate names in the memo will appear in alphabetical order.
- The corresponding `bps` values are still correctly aligned with their respective `affiliate` values from the query.

⚠️ If your interface logic assumes that the memo preserves the order of affiliates as supplied in the query, you **must account for this sorting behavior** to avoid mismatches.

Valid memo examples:

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5:10` (Will skim 10 basis points for each of the affiliates)
- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/thor1t2hav42urasnsvwa6x6fyezaex9f953plh72pq/t3:10/20/30` (Will skim 10 basis points for `t1`, 20 basis points for `thor1t2hav42urasnsvwa6x6fyezaex9f953plh72pq`, and 30 basis points for `t3`)

Invalid memo examples:

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5:10/20` (5 affiliates defined, but only 2 affiliate basis points)
- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5/t6:10` (Too many affiliates defined)

### Streaming Swaps

[_Streaming Swaps_](streaming-swaps.md) _is the recommended default which breaks up the trade to reduce slip fees._

Params:

- **streaming_interval**: # of THORChain blocks between each subswap. Larger # of blocks gives arb bots more time to rebalance pools. For deeper/more active pools a value of `1` is most likely okay. For shallower/less active pools a larger value should be considered.
- **streaming_quantity**: # of subswaps to execute. If this value is omitted or set to `0` the protocol will calculate the # of subswaps such that each subswap has a slippage of 5 bps.

Memo format:
`=:BTC.BTC:<destination_addr>:<limit>/<streaming_interval>/<streaming_quantity>`

Quote example:

`https://<stagenet-thornode>/thorchain/quote/swap?amount=100000000&from_asset=BTC.BTC&to_asset=ETH.ETH&destination=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430&streaming_interval=10`

_Note: Stagenet endpoints are being transitioned to the Maya team. See [Connecting to THORChain](../concepts/connecting-to-thorchain.md) for current URLs._

```json
{
  "inbound_address": "bc1qjqrzsszkr6g0autrveuv7vjryytk0pkqdwhuz2",
  "inbound_confirmation_blocks": 1,
  "inbound_confirmation_seconds": 600,
  "outbound_delay_blocks": 720,
  "outbound_delay_seconds": 4320,
  "fees": {
    "asset": "ETH.ETH",
    "affiliate": "0",
    "outbound": "285660",
    "liquidity": "3228480",
    "total": "3514140",
    "slippage_bps": 21,
    "total_bps": 22
  },
  "expiry": 1722575809,
  "warning": "Do not cache this response. Do not send funds after the expiry.",
  "notes": "First output should be to inbound_address, second output should be change back to self, third output should be OP_RETURN, limited to 80 bytes. Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats (P2WSH with Bech32 address format preferred).",
  "dust_threshold": "10000",
  "recommended_min_amount_in": "74260",
  "recommended_gas_rate": "4",
  "gas_rate_units": "satsperbyte",
  "memo": "=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430:0/10/0",
  "expected_amount_out": "1531908900",
  "max_streaming_quantity": 1440,
  "streaming_swap_blocks": 14390,
  "streaming_swap_seconds": 86340,
  "total_swap_seconds": 86940
}
```

Notice how `slippage_bps` shows the savings by using streaming swaps. `streaming_swap_seconds` also shows the amount of time the swap will take.

### Custom Refund Address

By default, in the case of a refund the protocol will return the inbound swap to the original sender. However, in the case of protocol <> protocol interactions, many times the original sender is a smart contract, and not the user's EOA. In these cases, a custom refund address can be defined in the memo, which will ensure the user will receive the refund and not the smart contract.

Params:

- **refund_address**: User's refund address. Needs to be a valid address for the inbound asset, otherwise refunds will be returned to the sender

Memo format:
`=:BTC.BTC:<destination>/<refund_address>`

Quote example:
[https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000&from_asset=ETH.ETH&to_asset=BTC.BTC&destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q&refund_address=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?amount=100000000&from_asset=ETH.ETH&to_asset=BTC.BTC&destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q&refund_address=0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430)

```json
{
  ...
  "memo": "=:BTC.BTC:bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q/0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430",
  ...
}
```

### Error Handling

The quote swap endpoint simulates all of the logic of an actual swap transaction and includes comprehensive error handling. Below are specific examples of errors that can occur:

#### Price Tolerance Error

Description: This error means the swap cannot be completed given your price tolerance. This error can usually be avoided by using the preferred slippage parameter _liquidity_tolerance_bps_. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&streaming_quantity=0&tolerance_bps=1).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=ETH.ETH \
amount=100000000 \
destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1 \
streaming_interval=1 \
streaming_quantity=0 \
tolerance_bps=1
```

Response:
`{"code":3,"message":"failed to simulate swap: failed to simulate handler: emit asset 2651686248 less than price limit 2719908600: invalid request","details":[]}`

#### Destination Address Error

Description: This error ensures the destination address is for the chain specified by `to_asset`. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q&streaming_interval=1).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=ETH.ETH \
amount=100000000 \
destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q \
streaming_interval=1
```

Response:
`{"code":2,"message":"failed to simulate swap: failed to validate message: swap destination address is not the same chain as the target asset: unknown request [cosmossdk.io/errors@v1.0.2/errors.go:151]: unknown request","details":[]}`

#### Affiliate Address Length Error

Description: This error is due to the fact the affiliate address is too long given the source chain's memo length requirements. Try registering a [THORName](../affiliate-guide/thorname-guide.md) to shorten the memo. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&liquidity_tolerance_bps=100&affiliate=thor1rr6rahhd4sy76a7rdxkjaen2q4k4pw2g06w7qp&affiliate_bps=10).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=ETH.ETH \
amount=100000000 \
destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1 \
streaming_interval=1 \
liquidity_tolerance_bps=100 \
affiliate=thor1rr6rahhd4sy76a7rdxkjaen2q4k4pw2g06w7qp \
affiliate_bps=10
```

Response:
`{"code":3,"message":"generated memo too long for source chain: invalid request","details":[]}`

#### Asset Not Found Error

This error means the requested asset does not exist. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=<20bf>&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&liquidity_tolerance_bps=100&affiliate=thor1rr6rahhd4sy76a7rdxkjaen2q4k4pw2g06w7qp&affiliate_bps=10).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=<20bf> \
amount=100000000 \
destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1 \
streaming_interval=1 \
liquidity_tolerance_bps=100 \
affiliate=thor1rr6rahhd4sy76a7rdxkjaen2q4k4pw2g06w7qp \
affiliate_bps=10
```

Response:
`{"code":3,"message":"bad to asset: invalid symbol: invalid request","details":[]}`

#### Bound Checks Error

Bound checks are made on both `affiliate_bps` and `liquidity_tolerance_bps`. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100000000&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1&liquidity_tolerance_bps=10015&affiliate=dx&affiliate_bps=1).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=ETH.ETH \
amount=100000000 \
destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1 \
streaming_interval=1 \
liquidity_tolerance_bps=10015 \
affiliate=dx \
affiliate_bps=1
```

Response:
`{"code":3,"message":"liquidity tolerance basis points must be less than 10000: invalid request","details":[]}`

#### Minimum Swap Amount Error

Description: This error occurs when the swap amount is less than the minimum required amount to cover fees. The error message now includes the recommended minimum amount. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=ETH.ETH&to_asset=BTC.BTC&amount=1000&destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q&streaming_interval=1).

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=ETH.ETH \
to_asset=BTC.BTC \
amount=1000 \
destination=bc1qyl7wjm2ldfezgnjk2c78adqlk7dvtm8sd7gn0q \
streaming_interval=1
```

Response:
`{"code":3,"message":"amount less than min swap amount (recommended_min_amount_in: 17895): invalid request","details":[]}`

#### Below Dust Threshold Error

Description: This error occurs when the swap amount is less than the dust threshold of the inbound chain. THORChain will not observe inbound transactions less than the dust threshold. Refer to the [dust threshold section](../bifrost/vault-behaviors.md#dust-thresholds) for more information. [Click here to view request URL](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap?from_asset=BTC.BTC&to_asset=ETH.ETH&amount=100&destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1&streaming_interval=1)

Request URL:

```json
https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/swap \
from_asset=BTC.BTC \
to_asset=ETH.ETH \
amount=100 \
destination=0x86d526d6624AbC0178cF7296cD538Ecc080A95F1 \
streaming_interval=1
```

Response:
`{"code":3,"message":"amount less than dust threshold: invalid request","details":[]}`

### Support

Developers experiencing issues with these APIs can go to the [THORChain Dev Discord](https://discord.gg/7RRmc35UEG) for assistance. Interface developers should subscribe to the #interface-alerts channel for information pertinent to the endpoints and functionality discussed here.
