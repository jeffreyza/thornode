# Vault Behaviors

Vaults are THORChain's on-chain multisig accounts controlled by active validator nodes using Threshold Signature Scheme (TSS). They are responsible for signing outbound transactions, receiving inbound funds, and securely holding assets on external chains.

## Vault Types and Lifecycle

THORChain vaults are TSS-based and managed by the validator set. There are two important layers to understand:

### Logical vs. Physical Vaults

- **Logical Vault**: A high-level vault concept representing a bonded set of nodes. There is one logical vault per vault status (e.g. one active, one retiring).
- **Physical Vaults**: Each logical vault is _sharded_ into multiple physical vaults to support scalability. The number of shards is determined by the `asgardsize` [Mimir parameter](../mimir.md#churning) (default: 20), such that:

  ```go
  num_physical_vaults = ceil(num_nodes / ASGARDSIZE)
  ```

For example, with 100 active nodes and ASGARDSIZE = 20, there will be 5 physical vaults that together form one logical vault. Each physical vault has a unique TSS key and its own vault address on each supported chain. These vaults operate in parallel and share the responsibility of outbound transactions and custody.

### Vault Status

Vault status is applied at the level of physical vaults, not logical vaults. This allows each shard to independently track its lifecycle status and be rotated or decommissioned individually as funds are migrated.

THORChain recognizes four vault statuses:

- **InitVault**: A new vault created during a churn after a successful keygen. These vaults are not yet active but are finalized and registered on-chain. Once all expected physical vaults for the logical vault are created, they are collectively promoted to Active.
- **Active**: Participating in signing and responsible for processing new transactions.
- **Retiring**: No longer assigned new transactions, but still holds funds. Used during node churn transitions.
- **Inactive**: No longer bonded or used for signing. These vaults should be empty and are eventually pruned.

Vaults transition between these statuses as follows:

1. When nodes churn in or out, a keygen is triggered, creating one or more vaults with the status of **InitVault**.
2. Once all expected vaults are created and the churn is ready, `InitVaults` are promoted to **Active** status.
3. The previously **Active** vault becomes **Retiring**, and begins migrating its funds.
4. Once a **Retiring** vault’s funds are drained (excluding dust or locked assets like reserve XRP), it is marked **Inactive**.

### Vault Selection for Inbound Transactions

To improve liquidity distribution, reduce fragmentation and minimize security risk, the THORChain protocol selects the vault with the **highest bond-to-asset ratio** (after accounting for pending outbounds) to be the current [**inbound address**](../concepts/querying-thorchain.md#getting-the-asgard-vault).

- This selection is determined using the `GetMostSecure()` method, which ranks vaults by comparing their bond coverage to the total vault value (in RUNE terms).
- The most secure vault is the one with the highest bond-to-value ratio and becomes the primary vault for receiving inbound transactions.
- As vaults change in balance or bond coverage, the selected inbound vault (and thus the address returned by `thorchain/inbound_addresses`) may rotate between physical vaults within the same logical vault.

This dynamic selection prioritizes security, distributes inbound load across vaults, and ensures new liquidity flows to the most well-collateralized vault available.

## TSS Signing

THORChain uses [**Threshold Signature Scheme (TSS)**](tss.md) to enable vault signing without ever reconstructing the private key. This allows a group of validator nodes to jointly sign transactions in a secure, decentralised manner. This mechanism is efficient and assumes that at least 67% of signing nodes act honestly and have access to accurate chain state.

### Outbound Tx Signing Process

When THORChain determines that an outbound transaction needs to be executed, the following steps occur:

> **Note**: This section provides a vault-centric view of the signing process. For the detailed cryptographic keysign implementation (message preparation, party coordination, multi-round protocol, signature assembly), see [TSS Implementation - Keysign Process](tss.md#message-signing-keysign-process).

1. **Vault Selection**: The vault responsible for holding the funds is selected. This is typically an active Asgard vault shared among the current validator set.
2. **Keysign Request**: A `MsgTssKeysignFail` or `MsgTssKeysignRetry` may be involved if retries are required. Otherwise, a signing ceremony begins.
3. **TSS Signing Ceremony**:
   - Transaction data is hashed and prepared for signing
   - Each node loads its encrypted key share (generated during DKG)
   - Nodes execute a multi-round threshold signing protocol
   - Signature shares are combined to produce a valid signature (R, S components)
   - Supermajority (≥67%) threshold required for signature completion
4. **Broadcast and Attestation**:
   - The node that assembles the final signature broadcasts the outbound transaction to the external chain.
   - That node then submits a `MsgObservedTxOut`, attesting that the outbound transaction was seen on-chain.
   - Other signing nodes independently observe the outbound on the external chain and submit their own attestations.
   - Once a **supermajority (≥67%)** of active validators submit matching attestations, THORChain marks the outbound transaction as complete.

**Note**: THORChain does not independently verify transactions on external chains. It relies on attestations from the validator set. This design trades trustless external validation for speed and decentralization.

### Trust Model and Safety

- **No Single Point of Failure**: No individual node can sign on its own or access the full private key.
- **Threshold Safety**: If a minority of nodes are malicious or offline, signing still proceeds.
- **Attestation-Based Finality**: Transaction finality is based on validator consensus, not external verification.
- **Blame Attribution**: TSS fault detection identifies misbehaving nodes during ceremonies. See [TSS Blame and Monitoring](tss.md#blame-process) for detailed diagnostics.
- **Slashing Incentives**: Nodes that submit false outbound attestations are penalized (see [Slashing Mechanics](#slashing-mechanics)).

### Retry and Timeout Logic

- If the signing process fails or times out (e.g., due to offline participants), THORChain may retry the ceremony or reassign the transaction to a different vault.
- Each signing opportunity is time-limited by `SigningTransactionPeriod`.

### Related Internal Messages

- `MsgTssKeysignFail` — reported when a vault fails to complete a signing session.
- `MsgOutboundTx` — submitted after a successful broadcast to report the external transaction.
- `MsgSolvency` — used by nodes to report vault balances, which ensures signing nodes are not misreporting success.

## Vault Migrations

After each churn, funds must be migrated from retiring vaults to newly created ones. This ensures that active vaults hold all user funds and are controlled by the current validator set.

### Migration Process

- Migrations are triggered automatically every `FundMigrationInterval` blocks (typically every 30 minutes).
- The process runs across multiple rounds, controlled by the [`ChurnMigrateRounds`](../mimir.md#churning) constant (default: 5).
- Each outbound migration includes a [memo](../concepts/memos.md#migrate) of the form: `MIGRATE:<blockheight>`, where `<blockheight>` indicates the churn event that triggered the migration.

### Migration Logic

- Migrations happen **per physical vault**. Each retiring vault (shard) migrates its balances independently.
- **Non-gas assets are migrated first** to avoid prematurely funds needed for transaction fees.
- Each round migrates an increasing proportion of remaining funds:

  - Round 1: 1/5 of remaining balance
  - Round 2: 1/4 of remaining balance
  - Round 3: 1/3 of remaining balance
  - … and so on.

- In the **final round**, gas assets are only migrated after all non-gas assets have been cleared from the vault. This avoids getting stuck without gas to pay for remaining transactions.

- **Vaults with pending outbounds** (including pending migrations) are skipped in that round and retried later.
- **Dust amounts** below the minimum send threshold are burned. Equivalent amounts are later restored to the pool from the protocol reserve.
- On XRP, migrations intentionally **leave behind 1 XRP** to maintain the minimum account reserve.

### Target Vault Selection

- Assets are migrated to **Active** vaults.
- For **gas assets**, priority is given to vaults that have not yet received that asset this churn cycle.
- Among candidate vaults, the most secure (by bond-to-value ratio) is selected using `GetMostSecure()`, which accounts for both existing and pending balances.

### Finalization and State Updates

- After all funds are migrated and a vault holds zero assets, it is marked `Inactive` and no longer used.
- Migrations are signed using the vault's TSS key, just like standard outbound transactions.

## UTXO Consolidation

On UTXO-based chains (e.g., BTC, LTC, BCH, DOGE), vaults accumulate many unspent transaction outputs (UTXOs). Excessive UTXOs degrade performance by:

- Increasing the size and cost of outbound transactions (more inputs = more bytes = higher fees),
- Slowing down TSS signing due to more signatures being required per transaction,
- Running into chain-level limits (e.g., BTC limits ~20 unconfirmed “ancestor” UTXOs per transaction chain).

THORChain's TSS engine parallelizes signing, but when too many UTXOs are consumed at once, it can lead to extremely long or stuck signing ceremonies.

### Consolidation Process

To mitigate this, THORChain automatically consolidates UTXOs:

**Trigger**: When a vault exceeds `MaxUTXOsToSpend` UTXOs (Mimir-controlled, default varies by chain).

- **Timing**: Evaluated every block. If no consolidation is already in progress, a new one may be initiated.
- **Transaction**: A **self-transfer** is created from the vault to itself, spending all or most UTXOs.
- **Memo**: The transaction uses the [`consolidate`](../concepts/memos.md#other-internal-memos) memo.
- **Validation**: Once observed, THORChain validates it using the `MsgConsolidate` handler to ensure:
  - The source and destination are the same vault,
  - The transaction matches expectations.
  - The consolidation transaction is initiated and signed by Bifrost, not scheduled by THORChain. Once broadcast, it is observed and validated on-chain using the `MsgConsolidate` handler.

If the transaction is malformed, the vault is **slashed** for the entire consolidation amount. Only one consolidation can run at a time per vault. Consolidation helps keep outbound transactions efficient.

This mechanism ensures outbound transactions remain performant and signing ceremonies do not stall the network.

## Dust Thresholds

THORChain enforces dust thresholds to prevent spam attacks and stuck transactions caused by very small amounts being sent to vaults. If a transaction amount is **equal to or below** the dust threshold, it is ignored and not observed by the network. Dust checks are done at the observation layer (Bifrost) and apply to inbound transactions only.

### Purpose

Dust attacks can freeze vaults or require external intervention (e.g., viabtc acceleration) by clogging them with negligible value UTXOs or underpriced gas transactions. The dust threshold ensures only economically meaningful transactions are processed.

### Validation Rules

- **Thresholds Apply Per Chain**: Each supported chain has a defined minimum transaction amount in **base units** (e.g., sats, wei, uatom, drops).
- **Transactions Must Exceed Threshold**: Inbound transactions below or equal to the threshold will not be observed or executed.
- **Check Before Sending**: Dust thresholds are published via the [Inbound Addresses endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses).
- **Not Gas-Rate Related**: Dust thresholds are different from `gas_rate_units` (e.g., gwei or sats/byte) which are used for gas pricing.

### Unit Conversion Note

Always convert human-readable amounts (e.g., `1 BTC`, `1 ETH`) into base units when comparing to the dust threshold. For example:

- `1 BTC` = `100,000,000 sats`
- `1 ETH` = `1e18 wei`

### Chain-Specific Dust Thresholds

| Chain | Threshold (base units)         |
| ----- | ------------------------------ |
| AVAX  | 1 gwei                         |
| BASE  | 1 gwei                         |
| BCH   | 10,000 sats                    |
| BSC   | 1 gwei                         |
| BTC   | 1,000 sats                     |
| DOGE  | 100,000,000 sats (1 DOGE)      |
| ETH   | 1 gwei                         |
| GAIA  | 1 uatom                        |
| LTC   | 10,000 sats                    |
| SOL   | 1,000,000 lamports (0.001 SOL) |
| XRP   | 1,000,000 drops (1 XRP)        |

These values are currently hardcoded within the code ([chain.go->DustThreshold()](https://gitlab.com/thorchain/thornode/-/blob/develop/common/chain.go)) and not configurable via Mimir.

**Warning**: Transactions at or below the dust threshold will be silently ignored. Always verify current thresholds before broadcasting.

## Slashing Mechanics

THORChain uses two distinct slashing mechanisms to incentivize correct behavior and penalize faults: slash points (which reduce block rewards) and bond slashing (which burns a portion of a node’s bonded RUNE).

### Slash Points (Reward Reduction)

These reduce a node's block rewards without impacting bonded capital. Each slash point reduces rewards by one block.

**Applied for:**

- **Missed block signing**: `MissBlockSignSlashPoints` per missed block
- **Double block signing**: `DoubleBlockSignSlashPoints` for signing two blocks at same height
- **TSS keysign failure**: `FailKeysignSlashPoints` + jailing when blamed for ceremony failure
- **Not observing outbounds**: Adds slash points and may lead to transaction rescheduling

### Bond Slashing (Capital Penalty)

Bond slashing directly burns a portion of a validator’s bonded RUNE and is used to enforce security-critical behavior.

- **Unauthorized outbound transaction**:  
   If a vault signs and broadcasts a transaction that does **not match an approved outbound instruction**, the participating vault members are slashed. Violations include:

  - Sending funds to an incorrect recipient address
  - Sending a larger amount than authorized
  - Broadcasting a transaction that was never scheduled by THORChain

  In such cases:

  - THORChain compares the outbound instruction to the observed transaction
  - If a mismatch is detected, the vault is marked as compromised
  - A penalty of up to **1.5× the misused amount (in RUNE)** is slashed from the signing nodes' bond
  - The slashed RUNE is distributed:
    - ⅔ to the affected liquidity pool
    - ⅓ to the protocol reserve

  **Pause Threshold**:  
   If the cumulative RUNE slashed from unauthorized actions exceeds the `PauseOnSlashThreshold`, THORChain **automatically halts** outbound transactions for the affected chain via a [Chain Halt](../concepts/network-halts.md#halt-pause-management).

### Enforcement

- Slashing is enforced by the [Slasher Manager](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/manager_slasher_current.go).
- Misbehavior is tracked on a per-node basis.
- Validator nodes are expected to honestly observe and attest to outbound transactions.
- Nodes that submit **false outbound attestations** or participate in **unauthorized vault signatures** are penalized accordingly.
