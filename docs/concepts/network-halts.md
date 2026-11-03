# Network Halts

```admonish warning
If the network is halted, do not send funds. The easiest check to do is if `halted = true` on the inbound addresses endpoint.
```

```admonish info
In most cases funds won't be lost if they are sent when halted, but they may be significantly delayed.
```

```admonish danger
In the worse case if THORChain suffers a consensus halt the `inbound_addresses` endpoint will freeze with `halted = false` but the network is actually hard-halted. In this case running a fullnode is beneficial, because the last block will become stale after 6 seconds and interfaces can detect this.
```

Interfaces should provide more feedback to the user what specifically is paused.

There are levels of granularity the network has to control itself and chains in the event of issues. Interfaces need to monitor these settings and apply appropriate controls in their interfaces, inform users and prevent unsupported actions.

All activity is controlled within [Mimir](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir) and needs to be observed by interfaces and acted upon. Also, see a description of [Constants and Mimir](../mimir.md).

Halt flags are Boolean. For clarity `0` = false, no issues and `> 0` = true (usually 1), halt in effect.

## Halt/ Pause Management

Each chain has granular control allowing each chain to be halted or resumed on a specific chain as required. Network-level halting is also possible.

### Chain Halt Types

The three primary chain-level halts node operators use:

| Halt Type        | Mimir Key            | Example          | Effect                                                                             |
| ---------------- | -------------------- | ---------------- | ---------------------------------------------------------------------------------- |
| **Trading Halt** | `HALT<CHAIN>TRADING` | `HALTETHTRADING` | Stops swaps; inbound observations continue; outbounds processed; Bifrost online    |
| **Signing Halt** | `HALTSIGNING<CHAIN>` | `HALTSIGNINGETH` | Stops outbound signing; outbounds queued; swaps/quotes still active                |
| **Chain Halt**   | `HALT<CHAIN>CHAIN`   | `HALTETHCHAIN`   | Full halt; chain not observed; Bifrost offline; requires majority resync to resume |

```admonish info
When issues arise, halt **both trading and signing**. Halting only signing keeps quoting active, potentially allowing integrations to send funds that won't process.
```

```admonish warning
A chain halt (`HALT<CHAIN>CHAIN`) blocks ALL swap activity involving that chain - including L1, Trade Account, and Secured Asset swaps. Halting the chain also stops all scanning, so should only be used in emergencies (lest the chain scanner fall behind and require significant time to catch up).
```

### XMR Scanner Activation

`HaltScannerXMR` is a Bifrost-only activation switch for the XMR block scanner and spent-reference catch-up. Set it to `0` to explicitly enable scanning. Any positive value pauses scanning. A missing or negative value, or a Mimir lookup error, fails closed and keeps the scanner paused.

This switch does not control keygen admission, enter THORNode's XMR chain-halt path, set the inbound-address `halted` field, or directly pause churn. A paused scanner also blocks normal XMR signing through the scanner-health check. Funded XMR vault migrations can therefore stall, and a retiring vault with funds above dust subsequently blocks churn. An unseeded pool or trading halt does not guarantee that the vault is unfunded; trading halts still permit observations.

Only Bifrost builds containing this scanner gate honor the key. Keep `HaltXMRChain` in effect during deployment until every intended participant runs the same reviewed Bifrost and signer build. Keep `HaltScannerXMR` positive until economic activation is approved, then explicitly vote it to `0`; deleting the key does not enable scanning. Production activation runbooks and node-launcher notes must include this step. Before funding the pool or enabling deposits, confirm that the effective value is `0`, the existing pause checks permit scanning, and the scanner is healthy and progressing.

`HaltScannerXMR` is an operational Mimir. `OperationalVotesMin` defaults to three active-node votes, but the chosen value must also have strictly more votes than every competing value. Three votes are not necessarily sufficient to replace an existing halt; a tie leaves the effective value unchanged.

Operators should monitor the XMR `healthy` field from Bifrost's `/status/scanner` endpoint. A paused scanner reports `healthy: false`, but that health value does not distinguish this switch from other scanner failures. An unset switch is logged only at Debug level. Lookup errors are logged at Error level.

### Chain-Specific Halts (Detailed)

1. **Signing Halt** - Allows inbound transactions but stops the signing of outbound transactions. Outbound transactions are [queued](https://gateway.liquify.com/chain/thorchain_api/thorchain/queue). This is the least impactful halt.
   - Mimir setting: `HALTSIGNING<CHAIN>`, e.g., `HALTSIGNINGETH`
2. **Liquidity Provider Pause** - Addition and withdrawal of liquidity are suspended but swaps and other transactions are processed.
   - Mimir setting: `PAUSELP<CHAIN>`, e.g., `PAUSELPBCH` for BCH
3. **Trading Halt** - Transactions on external chains are observed but not processed, only [refunds](memos.md#refunds) are given. THORNode's Bifrost is running, nodes are synced to the tip therefore trading resumption can happen very quickly.
   - Mimir setting: `HALT<CHAIN>TRADING`, e.g., `HALTBCHTRADING` for BCH
4. **Chain Halt** - Serious halt where transactions on that chain are no longer observed and THORNodes will not be synced to the chain tip, usually with their Bifrost offline. Resumption will require a majority of nodes syncing to the tip before trading can commence.
   - Mimir setting: `HALT<CHAIN>CHAIN`, e.g., `HALTBCHCHAIN` for BCH
5. **Pool Deposit Pause** - Suspends deposits into a specific Liquidity Pool.
   - Mimir setting: `PAUSELPDEPOSIT-<ASSET>`, e.g., `PAUSELPDEPOSIT-BTC-BTC` for BTC pool
6. **Streaming Swap Pause** - Pauses streaming swaps, affecting user slip protection.
   - Mimir setting: `StreamingSwapPause`

```admonish warning
Chain specific halts do occur and need to be monitored and reacted to when they occur. Users should not be able to send transactions via an interface when a halt is in effect.
```

### **Network Level Halts**

- **Network Pause LP** `PAUSELP = 1` Addition and withdrawal of liquidity are suspended for all pools but swaps and other transactions are processed.
- **Network Trading Halt** `HALTTRADING = 1` Will stop all trading for every connected chain. The THORChain blockchain will continue and native RUNE transactions will be processed.
- **Global Chain Halt** `HaltChainGlobal = 1` Halts all external chains simultaneously. Single key that causes `IsChainHalted()` to return true for any chain. Does not set individual chain halt keys.

A chain halt is possible in which case Mimir or Midgard will not return data. This can happen if the chain suffers consensus failure or more than 1/3 of nodes are switched off. If this occurs the Dev Discord Server `#interface-alerts` will issue alerts.

```admonish warning
While very rare, a network level halt is possible and should be monitored for.
```

### Secured Asset Halt Management

1. **Global Secured Asset Halt** - Disables deposits and withdrawals of all secured assets across base and App Layers.
   1. Mimir setting is `HaltSecuredGlobal`, set to `1` to disable all operations.
2. **Specific Secured Asset Deposit Halt** - Disables deposits of secured assets in base and App Layer for the specified chain.
   1. Mimir setting is `HaltSecuredDeposit-<CHAIN>`, e.g., `HaltSecuredDeposit-ETH = 1` disabled deposits for ETH-ETH and all ERC20 secured assets.
3. **Specific Secured Asset Withdrawal Halt** - Same as `HaltSecuredDeposit-<CHAIN>` except for Secured Asset Withdrawal.
   1. Mimir setting is `HaltSecuredWithdraw-<CHAIN>`, e.g., `HaltSecuredWithdraw-ETH = 1` disables withdrawals for ETH-ETH and all ERC20 secured assets.

### Smart Contract Halt Management

Smart contract halts control CosmWasm contract execution in the App Layer. These halts pause specific or all contract activities during vulnerabilities. Ordered by scope:

1. **Global Smart Contract Halt** - Disables all CosmWasm contract executions.
   - Mimir setting: `HaltWasmGlobal`, set to `1` to disable; `0` to enable.
2. **Deployer Halt** - Disables all contracts deployed by a specific address.
   - Mimir setting: `HaltWasmDeployer-<ADDRESS>`, e.g., `HaltWasmDeployer-tthor1abc...xyz`
3. **Contract Code Halt** - Disables all instances of a contract by its code checksum.
   - Mimir setting: `HaltWasmCs-<CHECKSUM>`, e.g., `HaltWasmCs-4UMPB3...`
4. **Specific Contract Halt** - Disables a single contract instance by address suffix (last 6 characters).
   - Mimir setting: `HaltWasmContract-<SUFFIX>`, e.g., `HaltWasmContract-w58u9f`

### Oracle

- `HaltOracle`: Disables oracle price feeds used by smart contracts for lending and perps.

### TCY Management

Claiming and Staking of $TCY can be enabled and disabled using flags.

- `HaltTCYTrading`: Halts all TCY trading/swaps.
- `TCYClaimingSwapHalt`: Disables RUNE-to-TCY swaps in the claiming module (default: 1, halted).
- `TCYStakeDistributionHalt`: Disables distribution of RUNE revenue to TCY stakers (default: 1, halted).
- `TCYStakingHalt`: Disables staking of TCY tokens (default: 1, halted).
- `TCYUnstakingHalt`: Disables unstaking of TCY tokens (default: 1, halted).
- `TCYClaimingHalt`: Disables claiming of TCY tokens for THORFi deposits (default: 1, halted).

### Trade Accounts

**Trade Accounts Pause** `TradeAccountsEnabled = 1` - Adding to and withdrawing from the Trade Account is enabled.

### RUNEPool Halts

- `RUNEPoolHaltDeposit`: Disables RUNEPool deposits.
- `RUNEPoolHaltWithdraw`: Disables RUNEPool withdrawals.

### Memoless Transactions

**Memoless Halt** `HaltMemoless = 1` - Emergency halt that disables all memoless transaction functionality, including both registration of new reference memos and usage of existing ones. When enabled, transactions using reference memos will be rejected.

### Node Operator Halts

These halts affect node operations rather than trading:

- `PauseBond`: Disables bonding.
- `PauseUnbond`: Disables unbonding.
- `HaltRebond`: Halts rebond operations.
- `HaltOperatorRotate`: Halts operator rotation during churn.

## Monitoring Mimir Keys

```bash
curl https://thornode.thorchain.org/thorchain/mimir
```

- **Integration**: App Layer interfaces must poll Mimir settings to detect halts and adjust functionality
- **Alerts**: Subscribe to Dev Discord `#interface-alerts` channel for updates

## Quick Reference Table

### Chain Halts

| Key                      | Example                  | Effect                                            |
| ------------------------ | ------------------------ | ------------------------------------------------- |
| `HALTSIGNING<CHAIN>`     | `HALTSIGNINGETH`         | Outbound txs queued, no broadcast                 |
| `PAUSELP<CHAIN>`         | `PAUSELPBCH`             | LPs cannot add/remove liquidity                   |
| `HALT<CHAIN>TRADING`     | `HALTBCHTRADING`         | Refunds only, no swaps (includes TA/SA swaps)     |
| `HALT<CHAIN>CHAIN`       | `HALTBCHCHAIN`           | Full halt, Bifrost offline (includes TA/SA swaps) |
| `PAUSELPDEPOSIT-<ASSET>` | `PAUSELPDEPOSIT-BTC-BTC` | LP deposits disabled for specific pool            |
| `StreamingSwapPause`     | -                        | Streaming swaps paused, affects user slip         |

### Global Halts

| Key               | Effect                           |
| ----------------- | -------------------------------- |
| `PAUSELP`         | All pools pause LP adds/removals |
| `HALTTRADING`     | No swaps across any chain        |
| `HaltChainGlobal` | All external chains halted       |

### Secured Assets

| Key                           | Example                   | Effect                                 |
| ----------------------------- | ------------------------- | -------------------------------------- |
| `HaltSecuredGlobal`           | -                         | All secured asset operations disabled  |
| `HaltSecuredDeposit-<CHAIN>`  | `HaltSecuredDeposit-ETH`  | Secured deposits disabled for chain    |
| `HaltSecuredWithdraw-<CHAIN>` | `HaltSecuredWithdraw-ETH` | Secured withdrawals disabled for chain |

### Smart Contracts (CosmWasm)

| Key                          | Example                            | Effect                                  |
| ---------------------------- | ---------------------------------- | --------------------------------------- |
| `HaltWasmGlobal`             | -                                  | All contract execution disabled         |
| `HaltWasmDeployer-<ADDRESS>` | `HaltWasmDeployer-tthor1abc...xyz` | All contracts by deployer disabled      |
| `HaltWasmCs-<CHECKSUM>`      | `HaltWasmCs-4UMPB3...`             | All instances of contract code disabled |
| `HaltWasmContract-<SUFFIX>`  | `HaltWasmContract-w58u9f`          | Specific contract instance disabled     |
| `HaltOracle`                 | -                                  | Oracle price feeds disabled             |

### TCY

| Key                        | Effect                                        |
| -------------------------- | --------------------------------------------- |
| `HaltTCYTrading`           | TCY trading/swaps disabled                    |
| `TCYClaimingSwapHalt`      | RUNE→TCY swaps in claiming module disabled    |
| `TCYStakeDistributionHalt` | RUNE revenue distribution to stakers disabled |
| `TCYStakingHalt`           | TCY staking disabled                          |
| `TCYUnstakingHalt`         | TCY unstaking disabled                        |
| `TCYClaimingHalt`          | TCY claims from THORFi deposits disabled      |

### RUNEPool

| Key                    | Effect                        |
| ---------------------- | ----------------------------- |
| `RUNEPoolHaltDeposit`  | RUNEPool deposits disabled    |
| `RUNEPoolHaltWithdraw` | RUNEPool withdrawals disabled |

### Trade Accounts & Memoless

| Key                    | Effect                                      |
| ---------------------- | ------------------------------------------- |
| `TradeAccountsEnabled` | `0` = disabled, `1` = enabled               |
| `HaltMemoless`         | Memoless tx registration and usage disabled |

### Node Operator

| Key                  | Effect                   |
| -------------------- | ------------------------ |
| `PauseBond`          | Bonding disabled         |
| `PauseUnbond`        | Unbonding disabled       |
| `HaltRebond`         | Rebond operations halted |
| `HaltOperatorRotate` | Operator rotation halted |
