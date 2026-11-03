# Known Chain Client Bugs and Prevention Rules

This document catalogs historical bugs from 80 merged MRs that caused mainnet outages, double spends, stuck funds, panics, or other critical issues. Use these patterns as a checklist when building or reviewing chain clients.

## Table of Contents

1. [Double Spend Prevention](#1-double-spend-prevention) — re-signing, scanner health, error matching, auto-observe, RBF, TSS pairing, pending liabilities, signer cache pre-check
2. [Signer Cache Management](#2-signer-cache-management) — write on success only, per-chain, observation removal, correct cache, version bumps, key uniqueness
3. [Nonce / Sequence Management](#3-nonce--sequence-number-management) — increment after broadcast, never overwrite higher local, cap pending gap, per-key tracking
4. [Block Scanner and Reorg Handling](#4-block-scanner-and-reorg-handling) — time-based health, invalidate+rescan, non-instant chains, resume from max, guard errata
5. [Confirmation Count](#5-confirmation-count) — actual finality, not assumed
6. [Gas and Fee Calculation](#6-gas-and-fee-calculation) — L1 fees for L2, gas rate units
7. [EVM-Specific Issues](#7-evm-specific-issues) — receipt logs, unsupported tx types, BroadcastTx errors
8. [UTXO-Specific Issues](#8-utxo-specific-issues) — vault validation, solvency, signable addresses
9. [Observation and Attestation](#9-observation-and-attestation) — immediate reporting, dedup, direction keys, instant observe, ledger confirmation
10. [Response Parsing](#10-response-parsing-and-validation) — non-zero validation, parse response bodies
11. [Nil Safety](#11-nil-safety) — pointer field initialization
12. [Attestation Security](#12-attestation-security) — peer auth, lock ordering
13. [Non-Deterministic Tx Construction](#13-non-deterministic-transaction-construction) — no time.Now(), reference block drift, dup-tx errors
14. [Race Conditions](#14-race-conditions-and-concurrency) — UTXO selection locks, sequence mutex, solvency state, approval races, shared state, cache atomicity
15. [Nil Pointer Safety](#15-nil-pointer-safety) — map init, EIP-1559, no panic on timeout, RPC nil-check
16. [Integer and Decimal Safety](#16-integer-and-decimal-safety) — no float math, multiply first, validate casts, guard subtraction
17. [Scanner Amount Derivation](#17-scanner-amount-derivation) — instruction data not balance deltas
18. [Stuck Tx Recovery](#18-stuck-transaction-recovery) — background recovery process
19. [Chain-Specific Requirements](#19-chain-specific-requirements) — balance reserves, MaxGas constants, null addresses
20. [Observation Robustness](#20-observation-and-broadcast-robustness) — skip invalid txs, deterministic fees, retryable errors, failed outbound observation, write-once cache, mempool finality
21. [Address Validation](#21-address-validation) — roundtrip verify, network match, dynamic chain IDs
22. [Keysign Safety](#22-keysign-and-signing-safety) — hard timeouts, break on failure, restart blocks, gas rate
23. [Halt Height Semantics](#23-halt-height-semantics) — height-gated vs boolean

---

## 1. Double Spend Prevention

These bugs allowed the same funds to be sent twice.

### 1a. Never re-sign a transaction — retry the same signed bytes

**MRs**: 2900, 537
**Root cause**: When broadcast failed, the signer re-signed the transaction (new nonce, new UTXOs). If the original had actually reached the mempool, both could confirm.
**Rule**: On broadcast failure, persist the signed transaction bytes. On retry, broadcast the same bytes — never re-sign.

### 1b. Block scanner health gates signing

**MR**: 1544
**Root cause**: When the block scanner was down but signing still worked, the network couldn't observe its own outbounds. The protocol assumed they were never sent and rescheduled them infinitely.
**Rule**: The signer must refuse to sign/broadcast when the block scanner is unhealthy.

### 1c. Error string matching must handle chain forks

**MR**: 2629
**Root cause**: AVAX used exact string matching against Ethereum's `ErrNonceTooLow`, but Avalanche's coreth library formats the error differently. The mismatch caused nonce-too-low errors to trigger re-signing.
**Rule**: Use prefix/contains matching for chain RPC errors, not exact string comparison. Forked chains have subtly different error formats.

### 1d. Auto-observe only on successful broadcast

**MR**: 2990
**Root cause**: Auto-observe submitted an observation regardless of broadcast success. If broadcast failed, the invalid observation blocked rescheduling to a new vault.
**Rule**: Never auto-observe a transaction that was not successfully broadcast.

### 1e. RBF/transaction replacement awareness

**MR**: 3162
**Root cause**: Replace-by-fee transactions on BTC caused observations of transactions that were later replaced, leading to incorrect processing.
**Rule**: Chain clients for chains supporting RBF must track replaced/dropped transactions and only finalize observations for confirmed transactions.

### 1f. TSS signature pairing must be content-based

**MR**: 4344
**Root cause**: UTXO batch TSS signing used array-index-based pairing. Non-deterministic goroutine ordering caused signatures to be paired with wrong messages.
**Rule**: Never rely on array index to pair TSS signatures with messages. Sort inputs deterministically before TSS and verify each signature matches its intended message content. Fail-fast on any signature verification failure.

### 1g. Track local pending liabilities, don't trust RPC pending balance

**MR**: 4386
**Root cause**: EVM client relied on `PendingBalanceAt` RPC which is inconsistent and can be manipulated via mempool flooding ("masking attack"). During high-volume signing, the node overspent.
**Rule**: Track in-memory liabilities for all signed-but-unconfirmed txs. Compute EffectiveBalance = ConfirmedBalance - PendingLiabilities. Reconstruct liabilities on startup before enabling signing.

### 1h. Always check signer cache before signing

**MR**: 4704
**Root cause**: Solana chain client didn't check signer cache before signing. Rescheduled outbounds were signed and broadcast twice.
**Rule**: Every chain client must check the signer cache before attempting to sign. Also set the cache entry if tx is confirmed on-chain even when broadcast returns an error.

---

## 2. Signer Cache Management

These bugs caused outbounds to get permanently stuck because the signer cache prevented retries.

### 2a. Only write to signer cache on confirmed broadcast success

**MRs**: 537, 1986, 2195
**Root cause**: Cache was written after signing (before broadcast), or on non-zero broadcast response codes ("tx in mempool" treated as success). Failed txs were permanently marked as signed.
**Rule**: Only add to signer cache when broadcast response code is 0 (explicit success). Failed broadcasts must clear the cache entry to allow retries.

### 2b. Per-chain-client signer cache, not global

**MR**: 1986
**Root cause**: Global signer cache at the signer level couldn't distinguish chain-specific failures. ETH out-of-gas left the tx stuck.
**Rule**: Signer cache must be managed per chain client, with OnObservedTxIn updating the cache when outbound txs are observed on-chain.

### 2c. Remove from signer cache on observation, not background cleanup

**MR**: 3323
**Root cause**: Observed outbounds weren't removed from the signed tx cache, relying on unreliable background cleanup.
**Rule**: Remove transactions from signed/pending caches immediately upon on-chain observation.

### 2d. Remove from correct cache on failure

**MR**: 2003
**Root cause**: Failed on-chain txs were removed from blockMeta store but not from the signer cache (CacheManager). Wrong cache was cleared.
**Rule**: Failed on-chain transactions must be removed from the signer cache (CacheManager), not just block meta storage.

### 2e. Bump cache key versions on format changes

**MR**: 2407
**Root cause**: Stale or invalid cache entries from a previous format caused incorrect deduplication.
**Rule**: When changing cache/storage format, always bump the key version prefix to force a clean slate.

### 2f. Cache keys must include all uniqueness fields

**MR**: 537
**Root cause**: Signer cache key didn't include InHash, causing key collisions between different outbounds.
**Rule**: Signer cache keys must include all fields that make a txout unique (including InHash).

---

## 3. Nonce / Sequence Number Management

These bugs caused all subsequent transactions to fail after a single failure.

### 3a. Increment nonce only after successful broadcast

**MR**: 684
**Root cause**: Sequence number was incremented in SignTx, before broadcast. If broadcast failed, all subsequent txs had wrong sequence.
**Rule**: Nonce/sequence increment must live in BroadcastTx, not SignTx. Only increment after confirmed broadcast.

### 3b. Never overwrite a higher local nonce with on-chain state

**MR**: 2195
**Root cause**: On-chain sequence fetch overwrote local sequence even when local was higher (due to in-flight mempool txs).
**Rule**: When syncing nonce from chain state, only update if on-chain value is greater than local. In-flight mempool txs may not be reflected on-chain.

### 3c. Cap pending nonce gap

**MR**: 3330
**Root cause**: EVM clients used pending nonce without limits. Many stuck mempool txs caused nonces far in the future.
**Rule**: Cap the gap between pending and finalized nonces to a configurable maximum (e.g., 3). Refuse to sign when too many txs are unconfirmed.

### 3d. Track nonce/sequence per signing key, not globally

**MR**: 682
**Root cause**: Single global block height for metadata caused stale sequences when multiple vaults signed in one block.
**Rule**: Track chain metadata (nonce/sequence, block height) per signing key/vault, not as a single global value.

---

## 4. Block Scanner and Reorg Handling

### 4a. Health threshold must be time-based, not block-count

**MR**: 3260
**Root cause**: 50-block lag threshold was too generous. A scanner 30+ minutes behind was considered healthy, allowing signing with stale state.
**Rule**: Use configurable time-based lag threshold (e.g., 30 minutes) for scanner health, not just block count. Health flag must be thread-safe (atomic.Bool).

### 4b. Reorgs must invalidate AND rescan

**MRs**: 1511, 1523
**Root cause**: Reorg handling sent errata for invalidated txs but didn't rescan replacement blocks, missing new transactions.
**Rule**: Reorg handling must both submit errata for dropped transactions AND rescan affected block heights for new transactions.

### 4c. Every non-instant-finality chain needs reorg handling

**MR**: 3619
**Root cause**: BSC (frequent short reorgs) had no reorg detection. Observed transactions that disappeared.
**Rule**: Every chain client without instant finality must implement reorg detection (parent hash comparison), block metadata storage, errata submission, and block rescanning.

### 4d. Resume scanning from max(config, stored position)

**MR**: 1379
**Root cause**: Scanner used StartBlockHeight even when it had already progressed past it, rescanning old blocks.
**Rule**: On startup, resume from max(configured_start_height, stored_scan_position).

### 4e. Guard errata submissions

**MR**: 1523
**Root cause**: Empty errata blocks were being enqueued unnecessarily.
**Rule**: Only submit errata blocks when there are actual errata transactions. Log rescan activity for operational visibility.

---

## 5. Confirmation Count

### 5a. Set confirmations based on actual chain finality

**MR**: 3643
**Root cause**: BSC was grouped with AVAX as "instant finality" (0 confirmations), but BSC actually needs ~3 blocks.
**Rule**: Set confirmation requirements based on the chain's actual consensus mechanism. Never assume instant finality without verification.

Current values for reference:

- **0 (instant)**: Solana, Cosmos/GAIA, XRP, AVAX
- **2**: ETH
- **3**: BSC
- **12**: BASE
- **15**: POL (Polygon)
- **1+19**: TRON

---

## 6. Gas and Fee Calculation

### 6a. Include L1 fees for L2 chains

**MR**: 3882
**Root cause**: BASE gas calculation only included L2 execution gas, ignoring L1 data posting fees. Caused insolvency.
**Rule**: For L2 EVM chains, always include receipt.L1Fee in gas calculations. Gas must reflect total cost, not just L2 execution.

### 6b. Gas rate unit consistency

**MR**: 3925
**Root cause**: Gas rate was expressed in inconsistent units across different code paths.
**Rule**: Ensure gas rate is consistently expressed in `gas_rate_units` throughout signing, observation, and fee reporting.

---

## 7. EVM-Specific Issues

### 7a. Detect smart contracts via receipt logs, not tx.To()

**MR**: 1692
**Root cause**: Router interactions via intermediary contracts had a different `to` field. Transactions were missed.
**Rule**: Detect smart contract interactions by matching event log addresses in receipts, not the transaction's `to` field.

### 7b. Filter unsupported transaction types

**MR**: 3839
**Root cause**: Blob transactions (EIP-4844) weren't filtered, causing parse errors.
**Rule**: Explicitly filter or skip unsupported transaction types (blob txs, etc.) as chains evolve.

### 7c. BroadcastTx must return errors on failure

**MR**: 3316
**Root cause**: BroadcastTx returned `("", nil)` on SendTransaction failure, so the signer treated failure as success.
**Rule**: BroadcastTx must always return a non-nil error when the transaction fails to send.

---

## 8. UTXO-Specific Issues

### 8a. Validate vault address in observed outputs

**MR**: 3128
**Root cause**: getOutput could select outputs where neither sender nor receiver was a vault, observing unrelated payments.
**Rule**: Validate that at least one party (sender or receiver) is a known THORChain vault before processing any UTXO output.

### 8b. Include all asgard outbounds in solvency calculations

**MR**: 3514
**Root cause**: Only "self" transactions were counted as vault balance reductions, missing outbounds from other asgard vaults.
**Rule**: Solvency calculations must account for all unconfirmed outbounds from any active asgard vault.

### 8c. Accepted address formats must be signable

**MR**: 3820
**Root cause**: NewAddress accepted AddressPubKey format, but SignTx couldn't sign for it. Outbounds stuck permanently.
**Rule**: Every address format accepted by NewAddress must be round-trippable through SignTx.

---

## 9. Observation and Attestation

### 9a. Report observations immediately, not in batches

**MR**: 3367
**Root cause**: Batched observations caused race conditions with failed-transaction cleanup (RemoveSigned before SetSigned).
**Rule**: Submit outbound observations immediately after successful broadcast, not deferred/batched.

### 9b. Deduplicate deck observations

**MRs**: 4008, 4059
**Root cause**: Scanner height resets caused duplicate TxInItems in the observer deck.
**Rule**: Deduplicate incoming transactions against already-queued deck items using proper equality checks.

### 9c. Cache keys must include observation direction

**MR**: 4010
**Root cause**: Same cache key for inbound and outbound observations of the same tx (vault migrations). One overwrote the other.
**Rule**: Attestation cache keys must include directionality (inbound vs outbound) for migrations and consolidations.

### 9d. All chain clients must implement instant observe

**MR**: 4137
**Root cause**: XRP chain client didn't return a TxInItem from SignTx, risking double spends from stuck outbounds.
**Rule**: All chain clients must return a `*TxInItem` from SignTx for instant observation.

### 9e. Broadcast retry must check actual ledger confirmation

**MR**: 4137
**Root cause**: txNeedsBroadcast returned false when tx was found in lookup, even though it wasn't yet validated/confirmed.
**Rule**: Broadcast retry logic must check for actual ledger confirmation/validation, not just transaction lookup success.

---

## 10. Response Parsing and Validation

### 10a. Validate critical fields are non-zero after deserialization

**MR**: 526
**Root cause**: Generic SDK unmarshal silently returned zero values for account number and sequence.
**Rule**: Always validate that deserialized RPC responses contain non-default/non-zero values for critical fields.

### 10b. Parse broadcast response bodies, not just HTTP status

**MR**: 682
**Root cause**: Only HTTP status was checked, missing application-level errors in the response body.
**Rule**: Always parse broadcast response bodies for application-level errors (bad sequence, failed txs).

---

## 11. Nil Safety

### 11a. Initialize pointer fields on outbound queue items

**MR**: 3921
**Root cause**: TxOutItems with nil pointer fields (CloutSpent, AggregatorTargetLimit) caused panics during serialization.
**Rule**: All pointer fields on TxOutItem must be initialized to non-nil zero values before being persisted to the outbound queue.

---

## 12. Attestation Security

### 12a. Authenticate attestation state sync peers

**MR**: 4059
**Root cause**: Any peer could send unsolicited attestation state batches.
**Rule**: Only accept attestation state from peers explicitly requested during sync. Verify peer identity.

### 12b. Consistent lock ordering

**MR**: 4059
**Root cause**: Lock ordering issue between global and per-state mutexes risked deadlocks.
**Rule**: Acquire inner (per-item) locks before releasing outer (global) locks. Document lock ordering.

---

## 13. Non-Deterministic Transaction Construction

### 13a. Never use time.Now() in transaction construction

**MR**: 4493
**Root cause**: TRON used `time.Now()` for timestamps. Nodes near second boundaries disagreed on tx bytes, breaking TSS consensus.
**Rule**: All fields in signed transactions must derive from deterministic shared state (e.g., thornode block time). Never use wall clock time, random values, or other non-deterministic inputs.

### 13b. Minimize reference block interval drift

**MR**: 4493
**Root cause**: Large reference block interval (65536) amplified scanner/daemon height drift, causing tx construction disagreement.
**Rule**: Use small reference block intervals to minimize the chance of nodes having different reference block heights.

### 13c. Treat duplicate-transaction broadcast errors as success

**MR**: 4493
**Root cause**: `DUP_TRANSACTION_ERROR` on broadcast was treated as failure, causing nodes to retry already-completed outbounds.
**Rule**: Add chain-specific duplicate/already-known errors to the "acceptable error" list. A duplicate error means the tx was already submitted.

---

## 14. Race Conditions and Concurrency

### 14a. UTXO selection must lock the entire sign+broadcast flow per vault

**MRs**: 3927, 3968
**Root cause**: Concurrent UTXO selection from signing and consolidation chose the same UTXOs. Initial fix locked only at selection; revised fix locks the entire sign+broadcast lifecycle.
**Rule**: Use per-vault locks covering the entire sign-and-broadcast lifecycle, not just UTXO selection. Different vaults can sign in parallel but the same vault must be serialized.

### 14b. Sequence numbers must increment under mutex after broadcast success

**MR**: 4228
**Root cause**: `atomic.AddUint64` allowed concurrent broadcasts to race on sequence increment. One would fail with wrong sequence.
**Rule**: Increment sequence numbers only on successful broadcast, under the same mutex that protects the broadcast call. Never use lock-free atomics for sequence management.

### 14c. Solvency check state must match observation state

**MR**: 4224
**Root cause**: TRON solvency queried finalized state (15 blocks behind) while scanner observed at head. Solvency didn't reflect recently observed txs.
**Rule**: Ensure solvency balance queries reference the same state as scanner observations. Add confirmation buffer if the RPC doesn't support querying at specific heights.

### 14d. EVM approval-then-execute must wait for confirmation

**MR**: 4656
**Root cause**: EVM Router V6 approval tx wasn't confirmed before the actual outbound was estimated. Gas estimate failed because approval wasn't mined yet.
**Rule**: When multi-step transaction sequences require prerequisites (approve → execute), wait for each step to confirm before proceeding.

### 14e. All shared state access needs proper locking

**MR**: 1574
**Root cause**: PubKeyManager methods read shared state without RWMutex. Nested calls caused potential deadlocks.
**Rule**: Every access to shared state needs locks. Create `NoLock`/`Internal` method variants for use within already-locked contexts. Run tests with `-race` flag.

### 14f. Cache check-then-modify must be atomic

**MR**: 4393
**Root cause**: `MarkAttestationsConfirmed` and `AddToBlock` were separate operations releasing the lock between them. Race allowed re-addition of confirmed items.
**Rule**: When multiple cache operations must be atomic (check-then-modify), hold the lock across all of them — never release between logically coupled operations.

---

## 15. Nil Pointer Safety

### 15a. Initialize all map fields in constructors

**MR**: 4253
**Root cause**: Solana scanner's `vaultScanStatuses` map was not initialized, causing nil map write panic.
**Rule**: Every struct with a map field must initialize it at construction time. A nil map in Go panics on write.

### 15b. EIP-1559 transactions have nil GasPrice

**MR**: 4285
**Root cause**: `tx.GasPrice()` returns nil for EIP-1559 (type 2) transactions. Unconditional access caused nil pointer dereference.
**Rule**: Use `receipt.EffectiveGasPrice` instead of `tx.GasPrice()` for actual gas cost. Add nil checks for all receipt and transaction fields that vary by transaction type.

### 15c. Never panic on expected operational errors

**MR**: 3750
**Root cause**: Keysign timeout called `panic()`, crashing the entire Bifrost process.
**Rule**: Never panic on timeouts, RPC errors, or other expected operational conditions. Use error returns and logging. Reserve panic for programmer errors only.

### 15d. All RPC responses must be nil-checked

**MR**: 4647
**Root cause**: Degraded Gaia RPC returned nil for block response, nil for BlockResults, and out-of-bounds TxsResults.
**Rule**: Never assume an RPC returns valid data. Nil-check all responses and validate slice bounds before indexing.

---

## 16. Integer and Decimal Safety

### 16a. Never use float math for chain amounts

**MRs**: 940, 3348
**Root cause**: BTC amounts as float64 had precision loss from floating-point arithmetic. Accumulating floats before converting to int caused incorrect totals.
**Rule**: Convert each external float to integer denomination individually before any arithmetic. Never accumulate floats — convert first, then sum.

### 16b. Multiply before converting decimals

**MR**: 4185
**Root cause**: Converting per-unit gas rate to 1e8 first truncated it to zero for high-precision chains. Multiplying the zero rate by size gave zero max gas.
**Rule**: Always multiply before dividing/converting decimals. Apply decimal conversion to the final aggregate, not intermediate per-unit values.

### 16c. Validate integer casts to prevent overflow

**MRs**: 4440, 4518
**Root cause**: uint64→int64 cast silently wrapped to negative, bypassing fee requirements. `Uint64()` on cosmos.Uint silently truncates.
**Rule**: Always validate bounds before narrowing or sign-changing integer casts. Use safe conversion helpers (`BigInt().IsUint64()`, `SafeUintFromInt64`). Propagate errors rather than silently capping.

### 16d. Guard unsigned subtraction

**MR**: 4378
**Root cause**: `preBalance - postBalance` panicked when postBalance > preBalance (account received funds in same tx).
**Rule**: Always check that minuend >= subtrahend before unsigned subtraction.

---

## 17. Scanner Amount Derivation

### 17a. Parse transfer instruction data, not balance deltas

**MR**: 4523
**Root cause**: Solana scanner computed amounts from PreBalances - PostBalances delta. Attacker inflated the delta by adding extra instructions.
**Rule**: Never derive transfer amounts from balance deltas on chains where a single tx can contain multiple instructions. Parse the actual transfer instruction data.

---

## 18. Stuck Transaction Recovery

### 18a. Every chain client needs a stuck tx recovery mechanism

**MR**: 1667
**Root cause**: ETH transactions dropped from mempool remained stuck forever with no recovery. The nonce stayed occupied, blocking all subsequent outbounds.
**Rule**: Track all broadcast transactions with their block height. Implement a background process that detects txs not confirmed within a timeout and re-signs/rebroadcasts. Critical for account-model chains where a stuck nonce blocks everything.

---

## 19. Chain-Specific Requirements

### 19a. Minimum balance reserves

**MR**: 4136
**Root cause**: XRP requires 1 XRP minimum reserve. Outbounds draining below the threshold made the vault unusable.
**Rule**: For chains with minimum balance requirements (XRP, XLM, SOL rent, etc.), subtract the reserve from available vault balance in outbound selection.

### 19b. Per-chain MaxGas constants for EVM chains

**MR**: 4614
**Root cause**: Only ETH had a MaxGas cap. Other EVM chains allowed unbounded gas on outbounds.
**Rule**: When adding a new EVM chain, define a MaxGas constant and ensure the generalized gas cap check covers it.

### 19c. Block null/burn addresses

**MR**: 3587
**Root cause**: EVM outbounds to the zero address (0x000...000) burned funds irretrievably.
**Rule**: Validate destination addresses against known burn/null/invalid addresses per chain. Maintain a blocklist.

---

## 20. Observation and Broadcast Robustness

### 20a. Skip invalid transactions, don't fail the batch

**MR**: 2950
**Root cause**: One invalid sender address caused the entire block of transactions to fail observation.
**Rule**: Invalid individual transactions must be skipped (logged and ignored), never cause the entire batch to fail.

### 20b. Deterministic gas/fee observations

**MRs**: 2104, 2107
**Root cause**: Runtime RPC gas price estimates produced non-deterministic values across nodes, causing slashing.
**Rule**: Derive fees from confirmed block data at explicit heights, not runtime RPC estimates or "latest" queries. All observations requiring consensus must be deterministic.

### 20c. Expand retryable RPC error list

**MR**: 3457
**Root cause**: UTXO RPC only retried "work queue depth exceeded". Other transient errors (connection refused, EOF, daemon startup) caused missed observations.
**Rule**: Cover all known transient error patterns. Use exponential backoff with caps. Treat the retryable error list as a living document.

### 20d. Observe failed vault outbounds for gas accounting

**MR**: 4697
**Root cause**: Failed Cosmos outbounds weren't observed, so gas cost was never deducted from vault accounting, causing progressive insolvency.
**Rule**: Always observe failed outbound transactions from vaults to account for consumed gas.

### 20e. Write-once observation cache

**MR**: 4663
**Root cause**: Duplicate detection path called `UntrackObservedTx()`, creating a toggle effect where txs were alternately tracked and untracked.
**Rule**: Once a tx is tracked in the observation cache, keep it there permanently. Never remove entries during duplicate detection.

### 20f. Mempool vs finality distinction

**MR**: 4389
**Root cause**: ConfirmationCountReady returned true for mempool txs, causing them to be marked as finalized.
**Rule**: Mempool transactions must always be marked `final=false`. Observation readiness and finality are separate concerns: `final = ConfirmationCountReady && !MemPool`.

---

## 21. Address Validation

### 21a. Roundtrip-verify addresses

**MR**: 2200
**Root cause**: UTXO addresses that decoded successfully produced different strings when re-encoded, sending funds to wrong addresses.
**Rule**: After decoding an address, re-encode and compare to original. Reject if they differ. Also reject uncommon/unsafe address types (raw public keys).

### 21b. Validate address network matches active network

**MR**: 2889
**Root cause**: Handlers accepted testnet addresses on mainnet, creating unusable LP positions.
**Rule**: All handler validation must verify that user-provided addresses match the active network (mainnet/stagenet/testnet).

### 21c. Never hardcode chain identifiers in shared code

**MR**: 3331
**Root cause**: EVM `GetBalances()` hardcoded 'EVM' prefix instead of using the actual chain name, breaking token solvency for non-ETH EVM chains.
**Rule**: Always derive chain name dynamically from the client's configured chain. Multiple EVM chains share the same code.

---

## 22. Keysign and Signing Safety

### 22a. Hard timeout on all signing operations

**MR**: 1553
**Root cause**: TSS keysign hung indefinitely with no timeout, blocking the entire signer goroutine.
**Rule**: All keysign operations must have a hard timeout (e.g., 5 minutes). Use `context.WithTimeout`.

### 22b. Break (not continue) on ordered keysign failures

**MR**: 3150
**Root cause**: Unstuck loop used `continue` on keysign error, causing TSS members to attempt different cancel txs and never converge.
**Rule**: When processing ordered keysign lists, break on failure so all TSS participants resync to the same item.

### 22c. Account for blocks during Bifrost restart

**MR**: 4169
**Root cause**: On restart, scanner used current THORNode height. Outbounds created right after upgrade were missed.
**Rule**: Use consensus-observed heights (lastblock), not current chain tip, as anchor for scan start. Test restart scenarios around upgrade boundaries.

### 22d. Never lower gas rate to fit max gas budget

**MR**: 3333
**Root cause**: Bifrost reduced gas rate to fit within max gas, creating under-priced transactions that got stuck.
**Rule**: Never lower the gas rate. If max gas is insufficient, allow bounded overshoot or abort and let THORNode reschedule.

---

## 23. Halt Height Semantics

### 23a. Halt heights are height-gated, not boolean flags

**MR**: 4625
**Root cause**: Solvency runner treated any halt value > 1 as "halted now", but values > 1 are height-based thresholds.
**Rule**: Value of 1 = admin halt (immediate). Value > 1 = height-gated (only active when `thorHeight >= haltHeight`). Always compare against current THORChain height.
