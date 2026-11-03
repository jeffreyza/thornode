#!/usr/bin/env bash
#
# Validates factual claims in .claude/skills/chain-client/ against the live
# codebase. Catches drift when the skill's reference material falls out of
# sync with the actual code (e.g. interface method counts, referenced file
# paths). Runs as part of `make lint`.
#
# The checks here are intentionally narrow: we verify only load-bearing
# claims that, if stale, would cause the skill to generate wrong code.

set -euo pipefail

SKILL_DIR=".claude/skills/chain-client"
SKILL_MD="${SKILL_DIR}/SKILL.md"
EVALS_JSON="${SKILL_DIR}/evals/evals.json"

if [[ ! -f ${SKILL_MD} ]]; then
  # Skill not present on this branch — nothing to validate.
  exit 0
fi

fail() {
  echo "ERR: chain-client skill lint: $*" >&2
  exit 1
}

# Count methods inside a named Go interface. A method declaration is
# identified by a line whose first non-whitespace token is an identifier
# followed by `(` — this correctly handles multi-line signatures (the
# continuation lines start with a different token) and skips comments and
# blank lines.
count_interface_methods() {
  local file="$1" name="$2"
  awk -v name="${name}" '
    $0 ~ "type[ \t]+" name "[ \t]+interface[ \t]*{" { inside = 1; next }
    inside && /^[[:space:]]*}/ { inside = 0; exit }
    inside && /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(/ { count++ }
    END { print count + 0 }
  ' "${file}"
}

check_method_count() {
  local label="$1" file="$2" iface="$3" claim_pattern="$4"

  [[ -f ${file} ]] || fail "${label}: file not found: ${file}"

  local actual
  actual=$(count_interface_methods "${file}" "${iface}")
  [[ ${actual} -gt 0 ]] || fail "${label}: could not find interface ${iface} in ${file}"

  if ! grep -qE "${claim_pattern//N/${actual}}" "${SKILL_MD}"; then
    local documented
    documented=$(grep -oE "${claim_pattern//N/[0-9]+}" "${SKILL_MD}" | head -1 || true)
    fail "${label}: SKILL.md claims '${documented:-<not found>}' but ${iface} in ${file} has ${actual} methods"
  fi
  echo "OK: ${label} — ${iface} has ${actual} methods (matches SKILL.md)"
}

check_method_count "ChainClient" \
  "bifrost/pkg/chainclients/shared/types/types.go" \
  "ChainClient" \
  'ChainClient.{0,10}\(N methods\)'

check_method_count "BlockScannerFetcher" \
  "bifrost/blockscanner/blockscanner.go" \
  "BlockScannerFetcher" \
  'BlockScannerFetcher.{0,10}\(N methods\)'

check_method_count "SolvencyCheckProvider" \
  "bifrost/pkg/chainclients/shared/runners/solvency.go" \
  "SolvencyCheckProvider" \
  'SolvencyCheckProvider.{0,10}\(N methods\)'

# Validate evals.json mentions the same method count. The evals file is a
# secondary source of truth for Claude's expected output and must stay aligned
# with SKILL.md.
if [[ -f ${EVALS_JSON} ]]; then
  chain_client_methods=$(count_interface_methods \
    "bifrost/pkg/chainclients/shared/types/types.go" "ChainClient")
  if grep -qE "[0-9]+ ChainClient methods" "${EVALS_JSON}"; then
    if ! grep -qE "${chain_client_methods} ChainClient methods" "${EVALS_JSON}"; then
      documented=$(grep -oE "[0-9]+ ChainClient methods" "${EVALS_JSON}" | head -1)
      fail "evals.json claims '${documented}' but ChainClient has ${chain_client_methods} methods"
    fi
    echo "OK: evals.json ChainClient method count (${chain_client_methods}) matches interface"
  fi
fi

# Validate that every source file path referenced by the skill actually exists
# -- these are the paths Claude will open when scaffolding a chain. Extract
# every backtick-quoted, directory-qualified .go path and assert it resolves,
# either from the repo root or relative to bifrost/pkg/chainclients/ (the skill
# writes chain-client paths in short form, e.g. `xrp/client.go`).
#
# Extracting from the docs beats a hand-maintained list: a stale entry in such
# a list used to be skipped because the check only ran for paths the docs
# happened to mention.
#
# ponytail: .go files only, and only paths containing a `/` -- bare basenames
# like `client.go` are ambiguous and suffix conventions like `_mocknet.go`
# aren't paths at all. Placeholders are written `<chain>.go` so they fall
# outside the character class and are never extracted.
missing=()
# shellcheck disable=SC2016  # backticks here are literal markdown, not expansion
while read -r path; do
  [[ -z ${path} ]] && continue
  [[ -f ${path} || -f bifrost/pkg/chainclients/${path} ]] || missing+=("${path}")
done < <(grep -rhoE '`[A-Za-z0-9_/.-]+/[A-Za-z0-9_.-]+\.go`' \
  "${SKILL_MD}" "${SKILL_DIR}/references/" | tr -d '`' | sort -u)

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "ERR: chain-client skill references non-existent paths:" >&2
  printf "  - %s\n" "${missing[@]}" >&2
  exit 1
fi

echo "OK: all referenced chain client paths exist"
echo "chain-client skill lint passed"
