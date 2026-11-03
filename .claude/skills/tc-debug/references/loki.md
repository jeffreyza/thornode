# Loki query reference for tc-debug

Rules that apply to every query (sanitisation, `(?i)`, `--limit` is a sample) are in `../../shared/conventions.md`.

## Job selection

Always discover jobs dynamically (step 0b). Pick **one** thornode job to avoid duplicate log lines. Use all available bifrost jobs unless filtering to specific vault members. If no bifrost jobs were discovered, skip all bifrost log queries.

## logcli flags

Always use these flags for clean output:

- `--quiet` — suppress progress/stats output
- `--output=raw` — for human-readable log lines (default for most queries)
- `--limit=N` — cap results (default 500, use 2000+ for counting/blame)
- `--since=Nh` — time range (24h for recent tx, 72h for vault/general issues)

Example full command:

```bash
logcli --addr=http://localhost:3100 query --quiet --output=raw --limit=500 --since=24h \
  '{job="<job>"} |~ "(?i)<pattern>"'
```

## Standard queries

**Transaction lookup (all hashes)**:

```logql
{<job_selector>} |~ "(?i)<HASH_or_first16>"
```

**Chain-specific errors**:

```logql
{<bifrost_job_selector>} |~ "(?i)<CHAIN>" |~ "(?i)(error|fail|timeout|panic|reject)"
```

**Signing events**:

```logql
{<bifrost_job_selector>} |~ "(?i)(keysign|sign.*tx|broadcast)" |~ "(?i)<CHAIN>"
```

**Observer events**:

```logql
{<bifrost_job_selector>} |~ "(?i)observe" |~ "(?i)<CHAIN>"
```

**Solvency alerts**:

```logql
{<thornode_job_selector>} |~ "(?i)solvency"
```

**Expanding search (when txid lookups yield nothing)**:
When direct txid lookups don't find relevant logs, expand to time-based chain client logs:

1. Determine the approximate THORChain block time of the event.
2. Convert to wall-clock time: `(CURRENT_HEIGHT - event_height) * 6` seconds ago.
3. Search chain client logs in that time window:

```bash
logcli --addr=http://localhost:3100 query --quiet --output=raw --limit=1000 \
  --from="<rfc3339_start>" --to="<rfc3339_end>" \
  '{<bifrost_job_selector>} |~ "(?i)<CHAIN>"'
```

## Time range guidance

- Transaction debugging: Start with `--since=24h`, expand to `--since=72h` if not found.
- Vault issues: `--since=72h` or wider — insolvency can build up over days.
- Description-based: Match the user's reported timeframe, default to `--since=72h`.
