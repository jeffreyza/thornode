# Shared conventions for the THORChain operations skills

Referenced by `health` and `tc-debug`. Read once per session; it is short.

## Improvement policy

When the user corrects behaviour or gives feedback about one of these skills, update that skill's SKILL.md (or this file, if the correction is shared) rather than saving a memory. The skills are shared artefacts; a fix in a private memory helps one person.

## Endpoints

| Service           | URL                                                   | Fallback                             |
| ----------------- | ----------------------------------------------------- | ------------------------------------ |
| THORNode API      | `https://gateway.liquify.com/chain/thorchain_api`     | `https://thornode.thorchain.network` |
| CometBFT RPC      | `https://gateway.liquify.com/chain/thorchain_rpc`     | `https://rpc.thorchain.network`      |
| Midgard           | `https://gateway.liquify.com/chain/thorchain_midgard` | `https://midgard.thorchain.network`  |
| Stagenet THORNode | `https://stagenet-thornode.ninerealms.com`            |                                      |
| Stagenet Midgard  | `https://stagenet-midgard.ninerealms.com`             |                                      |

Never WebFetch `/thorchain/nodes` or `/thorchain/vaults/asgard`; they are too large. `curl` them and pipe into the scripts in `health/`. For solvency, fetch the vaults in the same pipeline as the L1 check: the two reads must be seconds apart, or any outbound between them looks like a deficit.

## Scripts

All live in `.claude/skills/health/` and `.claude/skills/tc-debug/`, paths relative to the repo root. Every script is stdlib-only Python 3.

| Script                                              | Purpose                                                                      |
| --------------------------------------------------- | ---------------------------------------------------------------------------- |
| `health/parse_nodes.py <M.m> <height>`              | Node summary; writes `/tmp/tc_pubkey_map.json` and `/tmp/tc_active_ips.json` |
| `health/parse_vaults.py <height>`                   | Vault status, balances, retirement duration                                  |
| `health/extract_vault_balances.py <chain> [status]` | Per-chain vault balances as JSON for the solvency check                      |
| `health/check_solvency.py <chain>`                  | Vault accounting vs L1 balance at 1e8                                        |
| `health/check_bifrost.py [count] [timeout]`         | Bifrost scanner lag from `/status/scanner`; reads `/tmp/tc_active_ips.json`  |
| `health/fetch_logs.py <start> <end> [level] [mod]`  | `/thorchain/logs` over a block range, as JSONL on stdout                     |
| `tc-debug/classify_input.py <input>`                | tx hash / vault / address / description, with chain hint                     |
| `tc-debug/discover_loki.py [addr]`                  | Loki job labels, cached to `/tmp/tc_loki_jobs.json`                          |
| `tc-debug/parse_tx_status.py [height]`              | `/thorchain/tx/status` to phase + stuck verdict (stdin)                      |
| `tc-debug/luvi_forensics.py <rpc> <from> [to]`      | `luvi_loss` events per tx / EndBlock step (`--pool`, `--tx`, `--parse`)      |

All read stdin from `curl` except `classify_input.py`, `discover_loki.py`, `fetch_logs.py`, and `luvi_forensics.py`, which take their arguments on the command line.

## Naming and units

- **Nodes**: `<operator_last4>:<address_last4>`, never bare pubkeys. Resolve via `/tmp/tc_pubkey_map.json`.
- **Vaults**: `...{last 4 of pub_key}`.
- **Amounts**: THORChain-native values are 1e8 base units, including `rune_price_in_tor` (divide by 1e8 for USD; `41001823` is $0.41, not $4.10). Report solvency at full 1e8.
- **Mimirs**: a value that is a block height means "set at that block"; compute the duration. Value `1` from `nodes_all` is a node mimir. Do not say "admin mimir".
- **GitLab references**: issues as `#N`, MRs as `!N`.

## Loki

Loki is optional. Check for `logcli`; if present, verify `localhost:3100` and ask before querying. Pick **one** thornode job to avoid duplicate lines; use all bifrost jobs unless narrowing to a vault's members.

- **Sanitise output.** Never expose namespace, pod, container, or job labels, or any other Kubernetes identifier, anywhere in output. Refer to sources as "thornode logs" or "bifrost logs" and strip identifiers from quoted lines. These reports are shared outside the cluster operator.
- **Match case-insensitively.** Chain names, mimir keys, and event words appear in mixed case depending on the code path. Always use `(?i)` in LogQL patterns, or matches will silently miss events.
- **`--limit` is a sample, not a population.** It caps returned lines, not events in the range. Never use it as a denominator. Label derived counts and percentages "of sample" with the sample size, and use `--limit=2000` or more when counting or apportioning blame.
- Standard flags: `--quiet --output=raw`. Time ranges: 24h for a recent tx, 72h or more for vaults, blame, and general issues.

## Handing off between skills

| You have                                                  | Use            |
| --------------------------------------------------------- | -------------- |
| Nothing specific, want the current state                  | `health`       |
| A tx hash, address, vault, or described problem           | `tc-debug`     |
| Writing, changing, or reviewing bifrost chain-client code | `chain-client` |
