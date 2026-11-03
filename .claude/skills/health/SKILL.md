---
name: health
description: Point-in-time THORChain mainnet health check from public THORNode endpoints - halted chains and their mimirs, stuck outbounds and churns, vault solvency, bifrost scanner lag, node versions and jails, keysign blame, release adoption. Use whenever someone asks how the network is doing, whether something is halted or stuck, whether a churn is progressing, or wants a status report or incident summary, even without the word "health". For a specific tx, address, or vault use tc-debug.
allowed-tools: WebFetch, Bash, Read, Write, Edit, AskUserQuestion, Grep, Glob
---

# THORChain Network Health Check

Shared conventions (endpoints, scripts, node/vault naming, units, Loki rules, hand-offs) are in `.claude/skills/shared/conventions.md`; read it first. Health-specific rules:

- **Filtering**: current major.minor only (old = dead). Jails only if active. Standby only if bond ≥ active min.
- **Keysign blame**: resolve to `op:addr`, group by operator. Flag >15% share or multi-node operators.
- **Tables**: pipe-delimited markdown. Solvency at full 1e8. Cause/fix columns reference GitLab (`#N`, `!N`).
- **Omit**: loans, fee multiplier, LP deposit pauses, active node IP status.
- **Scope**: current state only.

## Procedure

### 0. Logs

Two sources, in order. Loki is preferred: it has bifrost logs and weeks of history. `/thorchain/logs` is the fallback and only ever has thornode module lines.

**a) Loki**: check for `logcli`. If found, ask user via AskUserQuestion. If yes: verify localhost:3100, pick **one** validator job (avoid duplicates). Query 72h+. Apply the Loki rules from the shared conventions (sanitise identifiers, `(?i)` everywhere, `--limit` is a sample). Skip (b) once Loki is working.

**b) `/thorchain/logs`** (when `logcli` is absent, declined, or Loki is unreachable). Probe it:

```bash
curl -s "$API/thorchain/logs?level=error"
```

Three outcomes:

- JSON array → available. Use `fetch_logs.py` for everything below.
- `400` with `block log capture is disabled` → the node has the endpoint but the operator left `thor.api.logs.retain_blocks: 0`. Ask via AskUserQuestion whether the user has another node URL with it enabled; if not, proceed without logs.
- 404 / unknown query path → the node predates the endpoint. Same AskUserQuestion, same fallback.

If neither source is available, run the whole check without logs and say so in the report — do not silently skip log-dependent steps.

**Using `/thorchain/logs`**:

```bash
export THOR_API='https://gateway.liquify.com/chain/thorchain_api'
python3 .claude/skills/health/fetch_logs.py $((H-2000)) "$H" info > /tmp/tc_logs.jsonl
```

- **Retention is blocks, not time.** `start`/`end` default to the last block capture closed (which can trail the last committed height slightly) and are clamped to the blocks still on disk. `fetch_logs.py` prints the oldest height actually returned to stderr — report coverage as "thornode logs cover blocks X–Y (~N h at 6s blocks)", never "72h".
- **A block can be missing for three reasons**: it fell out of `retain_blocks`, it was dropped to keep the log directory under its 1 GiB ceiling, or capture was cut short for that block. An empty result is _not_ proof the node logged nothing at that level — say "no lines retained", not "no such events".
- **Status codes**: `400` means the request is wrong (bad `start`/`end` — `0` and non-integers are rejected — reversed range, unknown level, or a range too wide). `503` means both read slots are busy: retry, do not treat it as fatal. `500` means the node could not read one of its own block files: retry the identical request, and report it as a node problem if it persists. `fetch_logs.py` splits wide ranges and backs off on 503.
- **No LogQL.** Filter the JSONL with `jq`/`grep`. Entries are `{height, time, level, module, msg, fields}`; `fields` is a flat map of the line's key/value context.
- **Search `fields`, not just `msg`.** Chain names and mimir keys live in `.fields.key`, `.fields.chain`, etc. A `msg`-only grep misses them.
- **No bifrost logs.** Any step below marked bifrost is Loki-only.
- `.time` is RFC3339 per entry, so "Set At"/"Duration" can come straight off the line instead of block arithmetic.
- **Not line-limited.** The endpoint returns every retained line in the range, so the shared `--limit` caveat does not apply — label counts "of retained window" and state the block range. It carries no Kubernetes identifiers either, so there is nothing to redact.

### 1. Height + Version

`/thorchain/lastblock` → `CURRENT_HEIGHT`. `/thorchain/version` (WebFetch) → `MAJOR.MINOR`.

### 2. Halts

`/thorchain/mimir` + `/thorchain/inbound_addresses` (WebFetch). List ALL mimirs per halted chain.

**Mimir change log (MANDATORY for every halted chain)**:

1. Fetch `/thorchain/mimir/nodes_all` (WebFetch) and search for ALL keys mentioning each halted chain (not just `ADD-CHAIN-*`). Look for `MimirUpgradeContract*`, `Halt*`, `StopSolvencyCheck*`, and any chain-scoped keys. The `nodes_all` response is large — search thoroughly, as keys may appear deep in the results.
2. **Per-key lookup (MANDATORY)**: For each halted chain, fetch these individual mimir key endpoints to see votes, values, and whether consensus is accumulating:
   - `/thorchain/mimir/key/Halt{CHAIN}Trading`
   - `/thorchain/mimir/key/HaltSigning{CHAIN}`
   - `/thorchain/mimir/key/StopSolvencyCheck{CHAIN}`
   - `/thorchain/mimir/key/MimirUpgradeContract{CHAIN}` (key format from `router_upgrade_controller.go`)
     This is essential — `nodes_all` alone can miss keys due to response size.
3. If Loki is available, run **two** queries before classifying the halt:

   **a) Broad mimir change sweep (MANDATORY — run before any chain-specific analysis)**:
   Query all mimir changes over the past 72h+ without chain filtering first. This catches router upgrade keys, contract address mimirs, and other related changes that narrow queries miss:

   ```logql
   {job="<thornode_job>"} |~ "(?i)mimir" | json | line_format "{{.message}} {{.module}}" |~ "(?i)(set|vote|update|upgrade|contract|router)"
   ```

   Or as a simpler fallback:

   ```logql
   {job="<thornode_job>"} |~ "(?i)(MimirUpgrade|RouterUpgrade|HaltTrading|HaltSigning|StopSolvency)" --since=72h --limit=500
   ```

   Review ALL results before writing the chain classification — router upgrade mimirs may appear with slightly different key names or log phrasing.

   **b) Chain-specific query** (after reviewing the broad sweep):

   ```logql
   {job="<thornode_job>"} |~ "(?i)(mimir|halt|upgrade|router)" |~ "(?i)<CHAIN>" --since=72h --limit=200
   ```

   **Always use `(?i)` (case-insensitive) for chain names and mimir keywords** — log output can mix upper/lowercase depending on the code path that emitted the event. A case-sensitive query will silently miss events.

   **API equivalent** (when using `/thorchain/logs` instead). Mimir votes are logged as `handleMsgMimir request` with the key in `.fields.key`, not in `msg` — grep the whole entry, not the message:

   ```bash
   # broad sweep: anything mimir/router/upgrade shaped, key and value included
   jq -c 'select((.msg + " " + (.fields|tostring)) | test("mimir|router|upgrade|halt|solvency";"i"))' /tmp/tc_logs.jsonl

   # chain-specific, after reviewing the sweep
   jq -c 'select((.msg + " " + (.fields|tostring)) | test("<CHAIN>";"i"))' /tmp/tc_logs.jsonl

   # who voted what, and when
   jq -r 'select(.msg=="handleMsgMimir request") | [.time, .height, .fields.node, .fields.key, .fields.value] | @tsv' /tmp/tc_logs.jsonl
   ```

   Router upgrade activity also shows up as `new router already set` / `fail to upgrade contract` with `.fields.chain`. Use `.time` for the "Set At" column.

4. **Classify the halt**:

   - **Planned router upgrade**: `Halt{CHAIN}Trading=1` + `StopSolvencyCheck{CHAIN}=1` + `MimirUpgradeContract{CHAIN}` votes present → standard router upgrade playbook. Do not misclassify as an incident.
   - **Incident**: Halt values are block heights (set by admin/automated), or no UpgradeContract mimir votes exist anywhere.
   - If still unclear, check `/thorchain/mimir/key/{MIMIR_KEY}` to see who voted and when.

   **IMPORTANT — API vs logs for upgrade detection**: The `/thorchain/mimir/key/MimirUpgradeContract{CHAIN}` endpoint only reflects the **consensus** value (returns `-1` until enough nodes vote). Individual node votes appear in the Loki logs BEFORE consensus is reached. Always check the logs (step 3b above) for `MIMIRUPGRADECONTRACTBASE` / `MIMIRUPGRADECONTRACTSOL` etc. — if votes are in progress but below threshold, it is still a planned router upgrade, not an incident. The API alone will mislead you into thinking no upgrade mimir exists.

Search GitLab: `glab issue list -s opened -P 30 | grep -i <CHAIN>`, `glab mr list --merged -P 15 | grep -i <CHAIN>`.

### 3. Outbound Queue

`/thorchain/queue` (WebFetch) + `/thorchain/queue/outbound` (WebFetch).

**Exclude MIGRATE outbounds**: Items with a `memo` starting with `MIGRATE:` are normal churn migration transactions, NOT stuck outbounds. Do not include them in the stuck outbounds section. If migration is blocked (e.g. by a signing halt), note it in the **Stuck Churn** section instead.

**True age lookup (MANDATORY for EVERY non-MIGRATE queued item)**: The `height` field on queued outbound items is the **latest retry height**, NOT when the outbound was originally scheduled. For EVERY non-MIGRATE outbound item, look up `/thorchain/tx/details/{in_hash}` and read the `outbound_height` field to determine the true age. Compare `outbound_height` to `CURRENT_HEIGHT` — if the difference is more than ~100 blocks, the outbound is stuck. Never dismiss an outbound as "in-flight" or "recent" based solely on the queue `height` field.

**Bifrost log investigation (MANDATORY for stuck outbounds, Loki only)**: `/thorchain/logs` has no bifrost lines. Without Loki, write "bifrost logs unavailable (no Loki)" in the Likely Cause column rather than omitting the row or guessing. With Loki, search ALL available bifrost jobs for errors related to the chain and/or the specific transaction:

```logql
{job="<bifrost_job>"} |~ "(?i)<CHAIN>.*(error|fail|timeout|reject)"
{job="<bifrost_job>"} |~ "<in_hash_prefix>"
```

Search multiple bifrost jobs if available to cross-reference. Include findings in the report.

### 4. Chain Sync

`last_signed_out` (THORChain height) vs `CURRENT_HEIGHT`. `last_observed_in` is external chain height — not comparable.

### 5. Nodes

```bash
curl -s '.../thorchain/nodes' | python3 .claude/skills/health/parse_nodes.py "$M_m" "$H"
```

### 6. Bifrost Scanner

```bash
python3 .claude/skills/health/check_bifrost.py 100 10
```

Most nodes expose `/status/scanner` on port 6040 (typically 60-80%+). The script skips the `/p2pid` pre-check to avoid rate limiting and goes straight to `/status/scanner`. Correlate outliers with slash points and blame.

**Nginx cache caveat**: Each bifrost's `/status/scanner` response is cached independently for ~1 minute by its local nginx. This means scanner "lag" of up to ~1 minute worth of blocks per chain is normal and expected (e.g. ~10 blocks for ETH, ~1 block for BTC). Only flag lag that significantly exceeds this per-chain threshold. When reporting, note the cache effect so readers don't mistake normal cache staleness for real lag.

**Diagnosing lag cause**: The scanner status reports both `chain_height` (what the node's external daemon thinks is the tip) and `scanner_height` (what bifrost has processed). When lag is concerning, compare these across nodes to distinguish two different problems:

- **Scanner lag** (scanner_height behind chain_height on the same node): bifrost is falling behind processing blocks from its own daemon. This is a bifrost performance issue.
- **Daemon lag** (chain_height itself is behind other nodes' chain_height): the node's external chain daemon (e.g. geth, solana-validator) is not synced. This is an infrastructure/node-operator issue, not a bifrost bug.
  Report which type of lag is occurring so the cause is clear.

### 7. Churn

```bash
curl -s '.../thorchain/vaults/asgard' > /tmp/tc_vaults.json
python3 .claude/skills/health/parse_vaults.py "$H" < /tmp/tc_vaults.json
```

Retiring >1000 blocks = stuck. If signing halted for chain with remaining assets → churn blocked.

### 8. Solvency

When stuck churn or outbounds. Full 1e8. Re-fetch the vaults in the same command: a vault snapshot even a few minutes old, compared against a live L1 balance, straddles any outbound sent in between and reports that outbound as a deficit.

```bash
curl -s '.../thorchain/vaults/asgard' \
  | python3 .claude/skills/health/extract_vault_balances.py CHAIN STATUS \
  | python3 .claude/skills/health/check_solvency.py CHAIN
```

A deficit on one busy vault that other vaults on the same chain do not show is usually this skew, not insolvency; rerun before reporting it.

### 9. Economics

`/thorchain/network` (WebFetch). Security ratio from `parse_nodes.py` bond total (more accurate).

**RUNE price**: `rune_price_in_tor` from `/thorchain/network` is in **1e8 base units** (like all THORChain amounts). Divide by 1e8 to get the USD price. Example: `rune_price_in_tor: 41001823` → $0.41, NOT $4.10.

### 10. Keysign Blame

**Loki**: single thornode job, 72h+, `--limit=2000` or more, resolve via `/tmp/tc_pubkey_map.json`. Group by node and operator. Report counts as "of sample" with the sample size.

**API**: the same lines are in `/thorchain/logs`. `.fields.blame` is `Blame.String()` — a multi-line string with `Node: <pubkey>` per blamed node, so pull pubkeys with a regex, not a field access:

```bash
jq -r 'select(.msg=="handle MsgTssKeysignFail request") | .fields.blame' /tmp/tc_logs.jsonl \
  | grep -oE 'thorpub[0-9a-z]+' | sort | uniq -c | sort -rn
```

Resolve each pubkey via `/tmp/tc_pubkey_map.json` to `op:addr`. Percentages here are "of retained window" — state the block range covered, not a 72h claim.

### 11. Releases

```bash
glab release list -P 5
glab release view <tag>
git log <tag>..develop --oneline
```

Compare latest release vs node adoption. Flag if a release fixes current issues but lacks supermajority.

## Output

Only include sections with findings. Healthy = say so briefly.

```markdown
## THORChain Network Health

**Block:** {H} | **Status:** 🟢/🟡/🔴

### Halted Chains

| Chain | Type | Mimir Keys | Set At | Duration | Likely Cause | Likely Fix |

### Stuck Churn

| Vault | Since | Duration | Remaining | Blocker | Likely Fix |

### Stuck Outbounds

| Chain | Items | First Scheduled | Age | Likely Cause | Likely Fix |

### Vault Solvency

| Vault | THORChain (1e8) | L1 (native) | L1→1e8 | Diff (1e8) | Status |

### Bifrost Scanner Lag (N reporting)

| Chain | Nodes | Min | Med | Max | Worst Node |

### Keysign Blame (72h)

| Node (op:addr) | Blames | % Total | Primary Vault |

### Nodes

| Metric | Value |

### Releases

| Release | Released | Content | Adoption |

### Summary

| Metric | Value |

### Related GitLab

| ID | Title | Status | Relevance |

### Logs

| Source | Finding |

State which source was used and its coverage, e.g. "thornode logs (node-local, blocks 1234000–1236000)" or "thornode logs (Loki, 72h)". If no log source was available, say so instead of dropping the section.
```
