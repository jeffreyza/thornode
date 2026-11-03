---
name: tc-debug
description: Investigate a specific THORChain problem - a transaction hash, THORChain or L1 address, vault pubkey, block-explorer URL, or a described symptom like "my swap is stuck" or "funds never arrived". Traces inbound observation, swap queue, outbound scheduling, signing, and broadcast using the THORNode API, Midgard, Loki logs, and L1 data, and reads the code at the version that was live. Use whenever the user pastes a hash or address or describes a stuck, missing, refunded, or failed transaction, even if they do not say "debug". For overall network state use health.
allowed-tools: WebFetch, Bash, Read, Write, Edit, AskUserQuestion, Grep, Glob
---

# THORChain Network Debugger

Shared conventions (endpoints and fallbacks, scripts, node/vault naming, units, Loki rules, hand-offs) are in `.claude/skills/shared/conventions.md`; read it first.

## Invocation

```text
/tc-debug <transaction_hash | vault_pubkey | address | description>
```

`classify_input.py` decides which path below to follow.

## Conventions specific to this skill

- **Network fallback**: default to mainnet. If a tx or address is not found, try stagenet (see shared conventions) or ask the user.

## Procedure

### 0. Prerequisites

**Step 0a — Classify input**:

```bash
python3 .claude/skills/tc-debug/classify_input.py "<user_input>"
```

Read the output JSON to determine `type` and `chain_hint`. This determines which debugging path to follow.

**Step 0b — Check logcli + Loki**:

1. Check `which logcli`. If not found, inform the user: "logcli is required for log analysis. Install it from https://grafana.com/docs/loki/latest/tools/logcli/ and ensure it's in your PATH."
2. Check Loki connectivity: `logcli --addr=http://localhost:3100 labels --since=1h --quiet 2>&1`. If connection refused, ask user via AskUserQuestion: "Loki is not reachable at localhost:3100. Do you have a port-forward to your cluster's Loki? If it's on a different port, please specify."
3. Discover job labels:

```bash
python3 .claude/skills/tc-debug/discover_loki.py
```

Review the output. If no thornode or bifrost jobs found, inform user and proceed with API-only debugging.

**Step 0c — Network version + code reference**:

1. Fetch `/thorchain/version` (WebFetch) → record `current` as `CURRENT_VERSION` and note `next_since_height`.
2. Fetch `/thorchain/lastblock` (WebFetch) → record `CURRENT_HEIGHT` (thorchain height).
3. **Determine `ISSUE_VERSION`** — the version active when the issue occurred, which may differ from `CURRENT_VERSION` if the network has upgraded since:
   - For transaction debugging: compare the tx's `consensus_height` or `finalised_height` against `next_since_height`. If the tx height predates it, the issue occurred under a previous version.
   - To find the previous version: `git tag -l "v*" --sort=-version:refname` and correlate release tags chronologically.
   - For non-tx debugging or when the tx height matches `CURRENT_VERSION`, set `ISSUE_VERSION = CURRENT_VERSION`.
4. Set up code reference at the `ISSUE_VERSION` tag:

```bash
TAG=$(git tag -l "v*" | grep "<ISSUE_VERSION>" | head -1)
```

Use `git show $TAG:<filepath>` to read files at that version without modifying the working tree.

**Last resort**: If the tag doesn't exist, use current branch code and note the version mismatch in the report.

**Step 0d — Node + vault context** (run if investigating vaults or need vault membership):

```bash
curl -s 'https://gateway.liquify.com/chain/thorchain_api/thorchain/nodes' \
  | python3 .claude/skills/health/parse_nodes.py "$MAJOR_MINOR" "$HEIGHT"

curl -s 'https://gateway.liquify.com/chain/thorchain_api/thorchain/vaults/asgard' > /tmp/tc_vaults.json
python3 .claude/skills/health/parse_vaults.py "$HEIGHT" < /tmp/tc_vaults.json
```

### 1. Transaction Debugging

Use when input type is `tx_hash`.

#### 1a. Fetch transaction status

```text
WebFetch: https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/status/<TXID>
```

Pipe through the parser:

```bash
curl -s 'https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/status/<TXID>' \
  | python3 .claude/skills/tc-debug/parse_tx_status.py "$HEIGHT"
```

#### 1b. Fetch transaction details

```text
WebFetch: https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/details/<TXID>
```

Key fields:

- `observed_tx.status`: `"done"`, `"reverted"`, or empty (incomplete)
- `observed_tx.out_hashes`: Hashes of outbound txs (for cross-referencing)
- `observed_tx.signers`: Which nodes signed (compare to expected set)
- `outbound_height`: **True** scheduled height (NOT the retry height from queue)
- `keysign_metric`: TSS signing performance data

#### 1c. Check for refund and fetch reason

If the tx/status `planned_out_txs` contains a `"refund": true` entry, or the tx/details `actions` memo starts with `"REFUND:"`, the transaction was refunded rather than processed as intended.

**Always fetch the refund reason from Midgard**:

```text
WebFetch: https://gateway.liquify.com/chain/thorchain_midgard/v2/actions?txid=<TXID>
```

Look for `metadata.refund.reason` in the response.

**Always include the refund reason prominently in the output** — both in the investigation narrative and the structured summary:

```markdown
> **Refund Reason**: "<reason from Midgard>"
> Original intent: <memo action type> (parsed from memo)
```

The refund reason often directly identifies the root cause:

- `"observed inbound tx to an inactive vault"` → vault was inactive at observation time, check vault status timeline
- `"emit asset 0 less than price limit"` → limit price not met, check pool price vs limit
- `"fail swap, not enough fee"` → dust amount, too small to cover fees
- `"trading is halted"` / `"pool is suspended"` → check mimir halts

#### 1c'. LP value loss (luvi_loss events)

THORNode checks every handler run and EndBlock step for a drop in a pool's hedged LP equity G (`x/thorchain/luvi_guard.go`). A `luvi_loss` event (always beyond the allowance) names the pool, the `scope` (the handler's message type URL, `endblock/<step>` or `migration/...`), the inbound `tx_id`, the normalized drop `normalized_delta_g` = dG/sqrt(A\*R), and the `allowed` drop for gas, slashes, LP-flow rounding and synth mints:

- `excess=true rejected=true`: the guard reverted a user flow (swap, add/withdraw liquidity, donate, ...). The user was refunded; the refund reason contains `luvi guard: pool LP value dropped`.
- `excess=true rejected=false`: a protocol step lowered LP value beyond the allowance and nothing stopped it. **Treat as a possible exploit or accounting bug**: read the handler or manager named in `scope` at `ISSUE_VERSION`.

```bash
# events in a block range (max 500 blocks), optionally for one pool or inbound tx
python3 .claude/skills/tc-debug/luvi_forensics.py https://gateway.liquify.com/chain/thorchain_rpc <from> [to] [--pool BTC.BTC] [--tx <TXID>]
```

Use it when a refund reason mentions the luvi guard, when LPs report value missing from a pool, or when a pool's `luvi_last_normalized_delta_g` on `/thorchain/pool/<asset>` is negative (the per-block accumulator: `luvi_g`, `luvi_c`, `luvi_star`, `luvi_height`). `luvi_height` dates the last step; a single strongly negative step points at that block. G cannot see L1 vault drains or phantom inbound credits, so pair a clean result with the vault solvency check (2b).

#### 1d. Diagnose by phase

Based on the `parse_tx_status.py` output phase:

**Phase: `inbound_observation` (Unobserved Inbound)**

The transaction hasn't been observed by THORChain. Investigate:

1. **Verify the tx exists on the source chain**: Use appropriate block explorer to confirm the L1 tx is confirmed.
2. **Check inbound_addresses**: `WebFetch: .../thorchain/inbound_addresses` — verify the destination address matches an active vault and the chain isn't halted.
3. **Check the memo format**: Bad memos can cause observation drops.
4. **Search bifrost observer logs**:

```logql
{<bifrost_job_selector>} |~ "(?i)<TXID_or_prefix>"
{<bifrost_job_selector>} |~ "(?i)observe" |~ "(?i)<CHAIN>" | json | line_format "{{.message}}" |~ "(?i)(drop|skip|ignore|reject|err|fail)"
```

5. **Check if chain scanner is lagging**: Compare `/thorchain/lastblock/<CHAIN>` external height to actual chain tip.
6. **Check for halted chain**: `WebFetch: .../thorchain/mimir` — look for `Halt<CHAIN>*` or `SolvencyHalt<CHAIN>*` mimirs.

**Phase: `confirmation_counting` (Waiting for Confirmations)**

Normal state — external chain needs more confirmations. Report the remaining time and move on unless it's been stuck for unusually long (compare expected chain block time to remaining seconds).

**Phase: `swap_pending` (Stuck in Swap Queue)**

The swap is queued but not executing:

1. **Check swap queue**: `WebFetch: .../thorchain/queue/swap/details/<TXID>` — get queue_type and position.
2. **Check for pool halts**: `WebFetch: .../thorchain/mimir` — look for `HaltTrading` or pool-specific halts.
3. **Search thornode logs**:

```logql
{<thornode_job_selector>} |~ "(?i)<TXID_prefix>" |~ "(?i)(swap|queue|limit|stream)"
{<thornode_job_selector>} |~ "(?i)swap" |~ "(?i)(fail|err|skip|reject|refund)"
```

4. **For streaming swaps**: Check if the interval/quantity makes sense. A streaming swap with many remaining swaps at a wide interval can take hours — this may be normal.

**Phase: `outbound_delay` (In Delay Period)**

Normal state — THORChain security delay. Report remaining time. Only investigate if the delay seems abnormally long (check `OutboundTransactionFee` and tx value).

**Phase: `outbound_signing` (Outbound Not Yet Signed)**

**Important**: An outbound that was scheduled only a few blocks ago (<50 blocks / ~5 min) is NOT stuck — signing and broadcasting takes time. Only escalate if it's been >50 blocks since `scheduled_outbound_height`. Also check if the vault is retiring (churning) — outbounds from retiring vaults can be slower due to TSS coordination.

This is the most common truly "stuck" state when age is significant. The outbound was scheduled but never signed/broadcast:

1. **Get outbound details**: From tx/details, note `outbound_height` (true schedule time), `observed_vault_pubkey` (signing vault).
2. **Check signing vault status**: `WebFetch: .../thorchain/vault/<PUBKEY>` — check `status`. If `InactiveVault` or `RetiringVault`, run the **Inactive/Retiring Vault Balance Check** (section 2e) immediately. This is a critical diagnostic — outbounds assigned to non-active vaults are a common cause of stuck transactions.
3. **Check scheduled queue**: `WebFetch: .../thorchain/queue/scheduled` — is the outbound present?
4. **Check keysign queue**: `WebFetch: .../thorchain/keysign/<outbound_height>` — is there a keysign record?
5. **Check for chain signing halt**: `WebFetch: .../thorchain/mimir` — look for `HaltSigning<CHAIN>=1` or `HaltChainGlobal`.
6. **Search thornode logs for signing**:

```logql
{<thornode_job_selector>} |~ "(?i)<TXID_prefix>"
{<thornode_job_selector>} |~ "(?i)sign" |~ "(?i)<CHAIN>" |~ "(?i)(fail|err|timeout|blame)"
```

7. **Search ALL bifrost logs for signing/broadcast**:

```logql
{<bifrost_job_selector>} |~ "(?i)<TXID_prefix>"
{<bifrost_job_selector>} |~ "(?i)(sign|broadcast)" |~ "(?i)<CHAIN>" |~ "(?i)(fail|err|timeout|reject)"
```

Search all bifrost instances first. If too noisy, narrow to bifrost instances that are members of the signing vault (cross-reference vault membership from step 0d with `/tmp/tc_pubkey_map.json`).

8. **Check keysign metrics/blame**: `WebFetch: .../thorchain/metrics` — look for blame entries on the signing vault.
9. **Expand search if stuck**: Look at chain client logs around the time of the scheduled outbound:

```logql
{<bifrost_job_selector>} |~ "(?i)<CHAIN>" | json | line_format "{{.message}}" |~ "(?i)(gas|nonce|balance|insufficient|reject)"
```

**Phase: `completed` (Outbound Sent)**

If the user reports it as stuck but status shows completed:

1. **Verify on L1**: Check the outbound tx hash on the destination chain's block explorer.
2. **Check for failed L1 tx**: The outbound may have been broadcast but reverted on-chain.
3. **Check tx/details status**: If `status: "reverted"`, the outbound failed and was refunded.

#### 1e. Non-swap transaction types

Parse the memo prefix to learn which stages to expect; `references/memo-types.md` lists them. Add, donate, bond, and trade-account deposits have no outbound, so "completed at inbound_finalised with no planned outbounds" is normal, not stuck.

#### 1f. Cross-reference outbound hashes

For every outbound hash found in `out_txs` or `out_hashes`:

1. Look up on the destination chain's block explorer to verify it landed.
2. If the outbound hash is empty/missing but the phase is past signing → the broadcast may have failed silently.

#### 1g. Not found on mainnet

If `/thorchain/tx/status/<TXID>` returns empty/404 on mainnet:

1. Try Midgard: `WebFetch: .../v2/actions?txid=<TXID>`
2. Try stagenet endpoints.
3. Ask user if they can confirm which network/chain the tx is from.

### 2. Vault Debugging

Use when input type is `vault_pubkey` or when transaction debugging reveals a vault issue.

#### 2a. Fetch vault details

```text
WebFetch: https://gateway.liquify.com/chain/thorchain_api/thorchain/vault/<PUBKEY>
```

Key fields: `type` (asgard/yggdrasil), `status` (active/retiring/inactive), `coins` (THORChain accounting balances), `membership` (node pubkeys), `chains`, `inbound_tx_count`, `pending_tx_block_heights`.

#### 2b. Compare balances to L1

For each chain the vault holds assets on:

```bash
# Fetch and compare in one command so the THORNode snapshot and the L1 read are seconds apart
curl -s 'https://gateway.liquify.com/chain/thorchain_api/thorchain/vaults/asgard' \
  | python3 .claude/skills/health/extract_vault_balances.py <CHAIN> \
  | python3 .claude/skills/health/check_solvency.py <CHAIN>
```

Report discrepancies at full 1e8 precision. Before calling a deficit real, rerun once: a stale vault snapshot straddling an outbound shows up as exactly that outbound's amount missing from L1. Common causes of a persistent gap:

- Gas spent on failed outbounds (not accounted for in THORChain)
- Dust attacks (unsolicited deposits)
- Double-spend or reorg on source chain
- Bug in gas accounting logic

#### 2c. Check vault-related logs

```logql
# Thornode logs mentioning the vault
{<thornode_job_selector>} |~ "(?i)<PUBKEY_last8>"

# Bifrost logs for vault members
{<bifrost_job_selector>} |~ "(?i)<PUBKEY_last8>"

# Look for solvency checker findings
{<thornode_job_selector>} |~ "(?i)solvency" |~ "(?i)<CHAIN>"
```

#### 2d. Check pending transactions

If `pending_tx_block_heights` is non-empty, the vault has pending work:

```text
WebFetch: .../thorchain/keysign/<pending_height>/<PUBKEY>
```

Check if keysigns are stuck or progressing.

#### 2e. Inactive/Retiring vault balance check (MANDATORY)

**This check is MANDATORY any time a vault involved in the investigation has status `InactiveVault` or `RetiringVault`.** It applies both when debugging vaults directly (section 2) and when transaction debugging (section 1d, outbound_signing phase) reveals the signing vault is non-active.

**For `RetiringVault`**:

1. Check how long it's been retiring (compare `status_since` to current height — >1000 blocks is slow, >5000 blocks is stuck).
2. Check remaining `coins` — what assets still need to migrate.
3. Check for signing halts on chains with remaining balances.
4. Check for stuck MIGRATE outbounds in the scheduled queue.

**For all non-active vaults (both `InactiveVault` and `RetiringVault`)**:

1. **Check THORNode accounting balance**: From the vault's `coins` array, identify ALL assets with non-zero balances.
2. **Check L1 on-chain balances**: For each chain where the vault has a non-zero THORNode balance, verify the actual on-chain balance at the vault's chain-specific address:

```bash
# If vault data is in /tmp/tc_vaults.json (from step 0d):
python3 .claude/skills/health/extract_vault_balances.py <CHAIN> inactive < /tmp/tc_vaults.json \
  | python3 .claude/skills/health/check_solvency.py <CHAIN>

# Or fetch the vault's chain address from the vault response and query L1 directly
```

3. **Flag stuck funds prominently**: If either the THORNode accounting balance or the L1 on-chain balance is significant (non-dust) for an inactive vault, flag it in the report:

```markdown
> **STUCK FUNDS ON INACTIVE VAULT**
> Vault `...{last4}` (status: InactiveVault since height {N}) holds significant balance:
>
> - THORNode accounting: {amount} {asset}
> - L1 on-chain ({chain address}): {amount} {asset}
>   These funds are stranded — the vault's TSS committee can no longer sign outbounds.
```

4. **Check recovery status**: Look at the slasher's recovery mechanism:
   - Fetch mimir for `MaxRetiredVaultRecoveryAttempts` (default: 100) and `SigningTransactionPeriod` (default: 300).
   - Calculate: `attempts = (current_height - tx_finalised_height) / signing_period`.
   - If `attempts >= max_recovery_attempts`: recovery has been **exhausted** — the outbound is abandoned. Note this explicitly.
   - If still within recovery window: the slasher should be rescheduling. Check if the outbound is in the scheduled queue.

**Why this matters**: Funds on inactive vaults are effectively locked. The vault's TSS members may have churned out, making signing impossible. Even if the vault has the balance, the outbound can't be fulfilled without either (a) the inactive vault successfully signing, or (b) a code fix to reroute the outbound to an active vault.

### 3. Address Debugging

Use when input type is `thor_address` or `chain_address`.

#### 3a. Determine address role

**For THORChain addresses (`thor1...`)**:

1. Check if it's a vault: `WebFetch: .../thorchain/vaults/asgard` — search for the address in vault `addresses` map.
2. Check if it's a node: `WebFetch: .../thorchain/node/<address>` — if found, it's a node operator address.
3. If neither, it's a user address.

**For chain-specific addresses**:

1. Check if it matches any vault address: search `/thorchain/vaults/asgard` response for matching addresses per chain.
2. If it's a vault address → proceed to Vault Debugging (section 2).
3. If it's a user address → search for transactions involving this address.

#### 3b. Find transactions for user addresses

Use Midgard to find transactions:

```text
WebFetch: https://gateway.liquify.com/chain/thorchain_midgard/v2/actions?address=<ADDRESS>&limit=10
```

Review the returned actions. For any that look problematic (pending, incomplete), extract the tx hash and proceed to Transaction Debugging (section 1).

#### 3c. Find transactions for chain addresses without Midgard

If Midgard doesn't return results, search Loki:

```logql
{<bifrost_job_selector>} |~ "(?i)<ADDRESS_or_last12>"
{<thornode_job_selector>} |~ "(?i)<ADDRESS_or_last12>"
```

### 4. Description Debugging

Use when input type is `description`. The user has described a problem rather than providing a specific identifier.

#### 4a. Parse the description

Extract key information:

- **Chain mentioned**: Which blockchain is involved?
- **Action type**: Swap, add liquidity, withdraw, send, etc.
- **Symptom**: Stuck, failed, missing, wrong amount, etc.
- **Time frame**: When did this happen?

#### 4b. Search relevant code

Using the `ISSUE_VERSION` tag (from step 0c), read relevant handler/manager code:

- Swap issues: `x/thorchain/handler_swap*.go`, `x/thorchain/manager_swap*.go`
- Observation issues: `bifrost/observer/observe.go`, `bifrost/observer/attestation*.go`
- Signing issues: `bifrost/signer/sign.go`
- Outbound issues: `x/thorchain/manager_txout*.go`
- Gas issues: `x/thorchain/manager_gas*.go`
- LP value missing from a pool: `x/thorchain/luvi_guard.go`, then step 1c' (`luvi_forensics.py`)
- Vault issues: `x/thorchain/manager_vault*.go`

Identify what log messages would be emitted for the described scenario.

Read `references/tx-pipeline.md` when you need the function-level path between stages (observer deck, attestation quorum, `MsgObservedTxQuorum`, swap queue, txout scheduling, signer, reschedule) and the condition that gates each one.

#### 4c. Search logs based on code analysis

Build targeted LogQL queries from the log messages found in code:

```logql
# Example: searching for specific error messages found in code
{<thornode_job_selector>} |~ "(?i)<error_message_pattern>"
{<bifrost_job_selector>} |~ "(?i)<error_message_pattern>"
```

#### 4d. Check network state

Depending on the description:

- **Halted chains**: `/thorchain/mimir` + `/thorchain/inbound_addresses`
- **Queue issues**: `/thorchain/queue`, `/thorchain/queue/outbound`, `/thorchain/queue/scheduled`
- **Version issues**: `/thorchain/version` + node version spread
- **Churn issues**: Vault analysis from step 0d

### 5. GitLab Correlation

Once you've identified a likely root cause:

#### 5a. Check if already fixed on develop

```bash
# Compare ISSUE_VERSION tag to develop
git log <ISSUE_VERSION_tag>..develop --oneline | grep -i "<keywords>"
git log <ISSUE_VERSION_tag>..develop --oneline | grep -i "<chain>"
```

If a fix commit exists, check whether it's also in `CURRENT_VERSION` (already deployed) or only on `develop` (ships with next release).

#### 5b. Check open issues and MRs

```bash
# Search open issues
glab issue list -s opened -P 30 | grep -i "<keywords>"

# Search open MRs
glab mr list -s opened -P 30 | grep -i "<keywords>"

# Search recently merged MRs
glab mr list --merged -P 15 | grep -i "<keywords>"
```

Report any related issues/MRs with their IDs (use `#N` for issues, `!N` for MRs).

### 6. Loki Query Reference

See `references/loki.md` for job selection, logcli flags, the standard query set, and the time-window expansion technique.

## Output Format

Produce a **hybrid investigation report**: narrative investigation flow showing what was checked and found, with a structured summary at the end.

### Investigation narrative

Walk through the debugging steps taken, showing key findings at each step. Use code blocks for important API responses and log excerpts. Redact all Kubernetes identifiers.

### Structured Summary

```markdown
## Debug Summary

**Input**: `<user_input>` | **Type**: <tx/vault/address/description>
**Issue Version**: <ISSUE_VERSION> | **Current Version**: <CURRENT_VERSION> | **Block Height**: <height>

### Diagnosis

| Finding           | Details                                                           |
| ----------------- | ----------------------------------------------------------------- |
| Phase             | <current phase of the tx/issue>                                   |
| Refund Reason     | <reason from Midgard, if applicable — ALWAYS include for refunds> |
| Root Cause        | <identified or suspected cause>                                   |
| Affected Chain(s) | <chain(s)>                                                        |
| Stuck Duration    | <how long, if applicable>                                         |

### Key Evidence

| Source          | Finding                   |
| --------------- | ------------------------- |
| THORNode API    | <what the API showed>     |
| Loki (thornode) | <relevant log findings>   |
| Loki (bifrost)  | <relevant log findings>   |
| On-chain        | <L1 verification results> |

### Recommendation

<What should be done — is there a fix on develop? An open issue? Does a mimir need to be set? Does a node need to restart?>

### Related GitLab

| ID  | Title | Status | Relevance |
| --- | ----- | ------ | --------- |
```

## L1 Verification

When a tx or balance must be confirmed on the external chain, use the public JSON endpoints in `references/l1-endpoints.md`. For vault balances prefer the solvency pipeline from the health scripts.
