# L1 lookups without API keys

Same hosts `health/check_solvency.py` uses for balances. All return JSON; none need a key. Etherscan-style v1 APIs and Solscan's public API are dead, so do not reach for them.

| Chain | Transaction                                                                                                                              | Balance                                                                 |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| BTC   | `GET https://mempool.space/api/tx/<hash>`                                                                                                | `GET https://mempool.space/api/address/<addr>`                          |
| LTC   | `GET https://litecoinspace.org/api/tx/<hash>`                                                                                            | `GET https://litecoinspace.org/api/address/<addr>`                      |
| DOGE  | `GET https://api.blockchair.com/dogecoin/dashboards/transaction/<hash>`                                                                  | `GET https://dogechain.info/api/v1/address/balance/<addr>`              |
| BCH   | `GET https://api.blockchair.com/bitcoin-cash/dashboards/transaction/<hash>`                                                              | `GET https://api.blockchair.com/bitcoin-cash/dashboards/address/<addr>` |
| ETH   | JSON-RPC `eth_getTransactionReceipt` at `https://ethereum-rpc.publicnode.com`                                                            | `eth_getBalance`                                                        |
| AVAX  | same, at `https://avalanche-c-chain-rpc.publicnode.com`                                                                                  | `eth_getBalance`                                                        |
| BSC   | same, at `https://bsc-rpc.publicnode.com`                                                                                                | `eth_getBalance`                                                        |
| BASE  | same, at `https://base-rpc.publicnode.com`                                                                                               | `eth_getBalance`                                                        |
| GAIA  | `GET https://cosmos-rest.publicnode.com/cosmos/tx/v1beta1/txs/<hash>`                                                                    | `GET .../cosmos/bank/v1beta1/balances/<addr>`                           |
| SOL   | JSON-RPC `getTransaction [<sig>, {"encoding":"jsonParsed","maxSupportedTransactionVersion":0}]` at `https://api.mainnet-beta.solana.com` | `getBalance`                                                            |
| XRP   | JSON-RPC `tx [{"transaction":"<hash>"}]` at `https://xrplcluster.com`                                                                    | `account_info`                                                          |
| TRON  | `POST https://api.trongrid.io/wallet/gettransactioninfobyid` body `{"value":"<hash>"}`                                                   | `GET https://api.trongrid.io/v1/accounts/<addr>`                        |

EVM receipt `status` of `0x0` means the outbound landed but reverted; THORChain will not know until it is observed as a failed outbound. Blockchair is rate-limited to a few requests a minute without a key.
