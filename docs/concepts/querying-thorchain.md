# Querying THORChain

## Supported Address Formats

Below are the list of supported Address Formats. Not using this risks loss of funds.

| Chain            | Supported Address Format                                         | Notes                                                                                                                                                                             |
| ---------------- | ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| BTC              | P2WSH /w Bech32 (preferred), P2WPKH /w Bech32, P2PKH, P2SH, P2TR | Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats                                                                                   |
| ETH              | EIP-55 (inbounds support EIP-7702)                               | Do not swap to smart contract addresses (including EIP-7702).                                                                                                                     |
| BSC              | EIP-55 (inbounds support EIP-7702)                               | Do not swap to smart contract addresses (including EIP-7702).                                                                                                                     |
| AVAX             | EIP-55 (inbounds support EIP-7702)                               | Do not swap to smart contract addresses (including EIP-7702).                                                                                                                     |
| BASE             | EIP-55 (inbounds support EIP-7702)                               | Do not swap to smart contract addresses (including EIP-7702).                                                                                                                     |
| TRON             | Base58                                                           | Do not swap to smart contract addresses (including EIP-7702).                                                                                                                     |
| DOGE             | Bech32, P2PKH                                                    | Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats                                                                                   |
| LTC              | Bech32, P2PKH                                                    | Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats. MWEB addresses (ltcmweb1) are not supported                                      |
| BCH              | Bech32, P2PKH                                                    | Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats                                                                                   |
| DASH             | Base58Check P2PKH and P2SH                                       | Use the address format for the current Dash network. SegWit/Bech32 addresses are not supported. Observe the dust threshold returned by `/inbound_addresses`.                      |
| GAIA (cosmoshub) | Bech32 (supports both Secp256k1 and Ed25519)                     | THORChain now supports EDDSA (Ed25519) addresses in addition to the traditional Secp256k1.                                                                                        |
| XRP              | Classic Base58                                                   | Avoid destination tags unless required by receiving wallet. Leave 1 XRP minimum for migration                                                                                     |
| XMR              | Standard (`4…`) and Subaddress (`8…`), Monero Base58             | Integrated addresses and payment IDs are not supported. A refund address is required in the swap memo — Monero hides the sender, so a failed swap without one cannot be refunded. |

All inbound_address support this format.

## Getting the Asgard Vault

Vaults are fetched from the `/inbound_addresses` endpoint:

[https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses)

You need to select the address of the Chain the inbound transaction. See [supported address formats](./querying-thorchain.md#supported-address-formats).

The address will be the current active Asgard Address that accepts inbounds. Do not cache these address as they change regularly. Do not delay inbound transactions (e.g. do not use future timeLocks).

Example Output, each connected chain will be displayed.

```json
{
  "chain": "BTC",
  "pub_key": "thorpub1addwnpepqf33c0dn8wqey2xcj0fe34kn3hhpz3z3vsjvke3f5mhdq5xumajmc7k89fv",
  "address": "bc1qjrj4cw7x46m2rxa78y32kv6nhzzssmvmpyfpcz",
  "halted": false,
  "global_trading_paused": false,
  "chain_trading_paused": false,
  "chain_lp_actions_paused": false,
  "observed_fee_rate": "2",
  "gas_rate": "3",
  "gas_rate_units": "satsperbyte",
  "outbound_tx_size": "1000",
  "outbound_fee": "903",
  "dust_threshold": "10000"
}
```

```admonish danger
Never cache vault addresses, they [churn](../bifrost/vault-behaviors.md#vault-migrations) regularly!
```

```admonish danger
Inbound transactions should not be delayed for any reason else there is risk funds will be sent to an unreachable address. Use standard transactions, check the `inbound address` before sending and use the recommended [`gas rate`](querying-thorchain.md#getting-the-asgard-vault) to ensure transactions are confirmed in the next block to the latest `Inbound_Address`.
```

```admonish danger
Check for the `halted` parameter and never send funds if it is set to true
```

````admonish warning
If a chain has a `router` on the inbound address endpoint, then everything must be deposited via the router. The router is a contract that the user first approves, and the deposit call transfers the asset into the network and emits an event to THORChain.

\
This is done because "tokens" on protocols don't support memos on-chain, thus need to be wrapped by a router which can force a memo.

Note: you can transfer the base asset, eg ETH, directly to the address and skip the router, but it is recommended to deposit everything via the router.

```json
{
  "address": "0x500b62a37c1afe79d59b373639512d03e3c4f5e8",
  "chain": "ETH",
  "gas_rate": "70",
  "halted": false,
  "pub_key": "thorpub1addwnpepq05w4xwaswph29ksls25ymjkypav30t8ktyu2dqzkxqk3pkf2l5zklvfzef",
  "router": "0xD37BbE5744D730a1d98d8DC97c42F0Ca46aD7146"
}
```

````

```admonish warning
If you connect to a public Midgard, you must be conscious of the fact that you can be phished and could send money to the WRONG vault. You should do safety checks, i.e. comparing with other nodes, or even inspecting the vault itself for the presence of funds. You should also consider running your own '[fullnode](https://docs.thorchain.org/thornodes/overview)' instance to query for trusted data.
```

- `Chain`: Chain Name
- `Address`: Asgard Vault inbound address for that chain.,
- `Halted`: Boolean, if the chain is halted. This should be monitored.
- `gas_rate`: rate to be used, e.g. in Stats or GWei. This represents the current gas price for transactions on that chain. For EVM chains, this is in Gwei. For UTXO chains, this is in sats/byte. Use this value when constructing transactions to ensure proper fee estimation. See [Fees and Wait Times](../swap-guide/fees-and-wait-times.md) for more details.

### Displaying available pairs

Use the `/pools` [endpoint](https://gateway.liquify.com/chain/thorchain_midgard/v2/pools) of Midgard to retrieve all swappable assets on THORChain. The response will be an array of objects like this:

```json
{
  "annualPercentageRate": "0.013025968330830473",
  "asset": "AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E",
  "assetDepth": "44984823069671",
  "assetPrice": "0.7628247087053169",
  "assetPriceUSD": "1",
  "earnings": "17038463728",
  "earningsAnnualAsPercentOfDepth": "0.012945072714771326",
  "liquidityUnits": "92700911990602",
  "lpLuvi": "-0.9995183044925366",
  "nativeDecimal": "6",
  "poolAPY": "0.013025968330830473",
  "runeDepth": "34315534554282",
  "saversAPR": "0",
  "saversDepth": "64514721140337",
  "saversUnits": "57164578167010",
  "saversYieldShare": "NaN",
  "status": "available",
  "synthSupply": "65229115178334",
  "synthUnits": "244408597168005",
  "totalCollateral": "0",
  "totalDebtTor": "0",
  "units": "337109509158607",
  "volume24h": "5772328862793"
}
```

```admonish info
Only pools with `"status": "available"` are available to trade
```

```admonish info
Make sure to manually add Native $RUNE as a swappable asset.
```

```admonish info
`"assetPrice" tells you the asset's price in RUNE (RUNE Depth/AssetDepth ). In the above example;

`1 ETH.USDC = 0.76282 RUNE`
```

### Decimals and Base Units

All values on THORChain (thornode and Midgard) are given in 1e8 eg, 100000000 base units (like Bitcoin), and unless postpended by "USD", they are in units of RUNE. Even 1e18 assets, such as ETH.ETH, are shortened to 1e8. 1e6 Assets like ETH.USDC, are padded to 1e8. THORNode will tell you the decimals for each asset, giving you the opportunity to convert back to native units in your interface.

See code examples using the THORChain xchain package here [https://github.com/xchainjs/xchainjs-lib/tree/master/packages/xchain-thorchain](https://github.com/xchainjs/xchainjs-lib/tree/master/packages/xchain-thorchain)

### Finding Chain Status

There are two ways to see if a Chain is halted.

1. Looking at the `/inbound_addresses` [endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) and inspecting the halted flag.
2. Looking at Mimir and inspecting the HALT\[Chain]TRADING setting. See [network-halts.md](network-halts.md#halt-pause-management) for more details.
