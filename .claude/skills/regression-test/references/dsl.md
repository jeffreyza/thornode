# Regression DSL reference

Read this when you need the exact fields of an op, a template function name, or the list of templates. The source of truth is `test/regression/cmd/operations.go` (`NewOperation` switch and the `Op*` structs); grep a real suite for the op before writing one.

## Ops

Ops decode with mapstructure (`TagName: json`, `WeaklyTypedInput`, `ErrorUnused`): unknown fields are errors, numbers may be quoted. Every `tx-*` op except `tx-price-feed` takes optional `sequence` and `gas`; wasm ops also take `fees`. Fields shown below are the ones suites use; message ops embed the proto message, so its JSON field names apply.

| Op                                                                                                                                   | Fields                                                                                                                                |
| ------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| `env`                                                                                                                                | `key` (env var: SNAKE_CASE constant or mimir default), `value`. Must precede `state`                                                  |
| `state`                                                                                                                              | `genesis` (deep-merged into the node genesis). Must precede all non-`env` ops                                                         |
| `check`                                                                                                                              | `endpoint`, `params`, `status` (default 200), `asserts` (jq `-e`). Host must be `localhost`                                           |
| `create-blocks`                                                                                                                      | `count`, `skip_invariants`, `exit` (expected exit code), `luvi_drop` (pools allowed to lose value)                                    |
| `tx-deposit`                                                                                                                         | `signer`, `coins`, `memo`                                                                                                             |
| `tx-send` / `tx-bank-send`                                                                                                           | `from_address`, `to_address`, `amount`, optional `memo`                                                                               |
| `tx-mimir`                                                                                                                           | `signer`, `key`, `value`                                                                                                              |
| `tx-observed-in` / `tx-observed-out`                                                                                                 | `signer`, `txs[]` of `{tx: {id, chain, from_address, to_address, coins, gas, memo}, block_height, finalise_height, observed_pub_key}` |
| `tx-network-fee`                                                                                                                     | `signer`, `block_height`, `chain`, `transaction_size`, `transaction_rate`                                                             |
| `tx-price-feed`                                                                                                                      | `prices` (symbol -> decimal string), `node` (default `dog`)                                                                           |
| `tx-solvency`                                                                                                                        | `signer`, `chain`, `pub_key`, `coins`, `height`                                                                                       |
| `tx-errata-tx`                                                                                                                       | `signer`, `tx_id`, `chain`                                                                                                            |
| `tx-tss-pool`                                                                                                                        | `signer`, `pub_keys`, `pool_pub_key`, `keygen_type`, `height`, `blame`, `chains`, ... (see struct)                                    |
| `tx-tss-keysign`                                                                                                                     | `signer`, `pub_key`, `id`, `memo`, `coins`, `blame`                                                                                   |
| `tx-ban`                                                                                                                             | `signer`, `node_address`                                                                                                              |
| `tx-node-pause-chain`                                                                                                                | `signer`, `value`                                                                                                                     |
| `tx-set-ip-address` / `tx-set-node-keys` / `tx-version`                                                                              | `signer` plus `ip_address` / `pub_key_set`, `validator_cons_pub_key` / `version`                                                      |
| `tx-propose-upgrade` / `tx-approve-upgrade` / `tx-reject-upgrade`                                                                    | `signer`, `name`, (`upgrade: {height, info}` on propose)                                                                              |
| `tx-store-code`                                                                                                                      | `wasm_file`, plus the store-code message fields                                                                                       |
| `tx-instantiate-contract[-2]`, `tx-execute-contract`, `tx-migrate-contract`, `tx-sudo-contract`, `tx-update-admin`, `tx-clear-admin` | CosmWasm message fields; see `suites/wasm/`                                                                                           |
| `fail-export-invariants`                                                                                                             | No fields. Last op only; the export invariant check is expected to fail                                                               |

`check` details: the port in `endpoint` is rewritten per routine, so always write `localhost:1317` / `localhost:26657`. A 404 is retried up to 10 times. An op with no `endpoint` is a breakpoint. Asserts are prefetched in parallel with the previous op except under `AUTO_UPDATE`.

## Template functions

Defined in `test/regression/cmd/template.go` and `config.go`.

| Function                                                                       | Value                                                                                                                                                |
| ------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `{{ observe_txid N }}`                                                         | `N` as 64-char zero-padded hex; use for L1 tx ids                                                                                                    |
| `{{ native_txid N }}` / `{{ native_txid -1 }}`                                 | Hash of the Nth (or most recent) native tx; resolved at run time                                                                                     |
| `{{ version }}`                                                                | `constants.Version` (`9.999.0` in the regtest image)                                                                                                 |
| `{{ addr_module_<name> }}`                                                     | `thorchain asgard bond transfer reserve fee_collector lending affiliate_collector treasury rune_pool tcy_claim tcy_stake pol_reserve stable_reserve` |
| `{{ addr_<chain>_<name> }}`                                                    | Address for every chain in `common.AllChains`, e.g. `addr_btc_fox`, `addr_thor_cat`                                                                  |
| `{{ addr_zec_<name>_tex }}`                                                    | ZEC TEX address                                                                                                                                      |
| `{{ pubkey_<name> }}`, `{{ pubkey_<name>_eddsa }}`, `{{ cons_pubkey_<name> }}` | secp256k1, ed25519, and consensus pubkeys                                                                                                            |
| `{{ template "<file>.yaml" }}`                                                 | Inline a file from `test/regression/templates/`                                                                                                      |

Names (mnemonics in `config.go`): `dog cat fox pig bird deer duck fish frog goat hawk lion mouse mule rabbit wolf`. `dog` is the default validator and vault; `pig` is the dev, marketing, and over-solvency fund address in mocknet constants.

Variables: `${NAME=value}` sets, `${NAME}` expands; the latest set value wins. Pool seeding convention (README): BTC = 1000 RUNE, ETH = 100 RUNE, others = 1 RUNE.

## Templates

`default-state` (accounts, balances, the `dog` node account; no vaults or pools) is the base for the pool templates, which add pools, LPs, and the asgard vault: `btc-pool-state`, `btc-pool-state-multiple-vaults`, `btc-eth-pool-state`, `btc-eth-usdc-pool-state`, `btc-eth-usdc-usdt-pool-state`, `btc-eth-3usd-pool-state`, `btc-eth-3usdx-pool-state`, `btc-eth-gaia-pool-state`, `btc-sol-pool-state`, `btc-xmr-pool-state`, `btc-xmr-quote-pool-state`, `btc-xmr-split-vault-pool-state`, `eth-usdc-gusd-mainnet-state`. `5-validators-btc-eth-pool-state` and `6-validators-btc-eth-pool-state` are standalone (own node accounts); use them for churn, upgrade, and consensus suites.

## Harness env vars outside `make`

Used when running `test/regression/cmd` directly: `REGTEST_MNT_DIR`, `REGTEST_BIN_DIR`, `REGTEST_HOME_BASE`, `REGTEST_CREATE_BLOCK_PORT_BASE`, `REGTEST_BLOCK_TIMES`, `REGTEST_CPUPROFILE_DIR`. The `make` target does not forward them.
