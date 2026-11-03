#!/bin/bash
# Runs govulncheck and fails only on reachable (symbol-level) vulnerabilities whose
# OSV ID is not listed in .govulncheck-ignore. govulncheck has no native ignore flag,
# so this wraps its JSON output. Allowlisted IDs that no longer appear are reported so
# the list can be pruned.
#
# Exit status: 0 no unaccepted findings, 1 unaccepted findings, 2 scanner, parser or
# allowlist error. Every error path fails closed.
#
# Usage: scripts/govulncheck.sh [packages...]   (default: ./...)
# Requires: go, jq
# Tests: build/scripts/tests/test_govulncheck.py
set -euo pipefail
export LC_ALL=C

GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.1.4}"
IGNORE_FILE="${IGNORE_FILE:-.govulncheck-ignore}"
# traces printed per unaccepted ID; the rest are counted
MAX_TRACES="${MAX_TRACES:-5}"

cd "$(dirname "$0")/.."

die() {
  echo "govulncheck: ERROR: $*" >&2
  exit 2
}

if [ $# -eq 0 ]; then
  set -- ./...
fi

command -v jq >/dev/null || die "jq is required"
[ -r "$IGNORE_FILE" ] || die "cannot read allowlist $IGNORE_FILE"

OUT=$(mktemp)
trap 'rm -f "$OUT"' EXIT

# -format json always exits 0 on findings; a non-zero exit is a real error (build
# failure, DB fetch failure) and must fail the job.
if ! go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
  -tags mocknet -format json "$@" >"$OUT"; then
  die "scanner failed"
fi

# Parse everything up front with checked exit status. Process substitution would
# hide a jq failure and turn malformed output into a silent pass. A real run always
# starts with a config message, so its absence means empty or foreign output.
if ! jq -e -n '[inputs] | any(.config.scanner_name == "govulncheck")' "$OUT" >/dev/null 2>&1; then
  die "scanner output is not valid govulncheck JSON"
fi

# a finding is reachable when its innermost trace frame names a function
REACHABLE_FILTER='select(.finding) | .finding | select(.trace[0].function != null)'

found_ids=$(jq -r "$REACHABLE_FILTER | .osv | strings" "$OUT") || die "failed to parse findings"
malformed=$(jq -n "[inputs | $REACHABLE_FILTER | select(.osv | type != \"string\")] | length" "$OUT") ||
  die "failed to parse findings"
[ "$malformed" -eq 0 ] || die "$malformed reachable findings have no OSV ID"

ignored_ids=$(sed -e 's/#.*//' "$IGNORE_FILE" | awk 'NF {print $1}')
bad=$(grep -Ev '^GO-[0-9]{4}-[0-9]+$' <<<"$ignored_ids" | grep -v '^$' || true)
[ -z "$bad" ] || die "malformed entries in $IGNORE_FILE: $(echo "$bad" | tr '\n' ' ')"

mapfile -t found < <(grep -v '^$' <<<"$found_ids" | sort -u)
mapfile -t ignored < <(grep -v '^$' <<<"$ignored_ids" | sort -u)
mapfile -t new < <(comm -23 <(printf '%s\n' "${found[@]}" | grep -v '^$') <(printf '%s\n' "${ignored[@]}" | grep -v '^$'))
mapfile -t stale < <(comm -13 <(printf '%s\n' "${found[@]}" | grep -v '^$') <(printf '%s\n' "${ignored[@]}" | grep -v '^$'))

echo "govulncheck: ${#found[@]} reachable vulnerabilities, ${#ignored[@]} allowlisted in $IGNORE_FILE"

for id in "${stale[@]}"; do
  echo "WARNING: $id is allowlisted in $IGNORE_FILE but no longer reported; remove it"
done

if [ ${#new[@]} -eq 0 ]; then
  echo "govulncheck: no reachable vulnerabilities outside the allowlist"
  exit 0
fi

echo
echo "govulncheck: ${#new[@]} reachable vulnerabilities not in $IGNORE_FILE:"
for id in "${new[@]}"; do
  # Limit output inside jq: piping to head would SIGPIPE jq and, under pipefail,
  # abort before the remaining IDs are reported.
  summary=$(jq -r -n --arg id "$id" 'first(inputs | select(.osv.id? == $id) | .osv.summary) // "(no summary)"' "$OUT")
  echo
  echo "$id: $summary"
  echo "  https://pkg.go.dev/vuln/$id"
  jq -r -n --arg id "$id" --argjson max "$MAX_TRACES" "[inputs | $REACHABLE_FILTER | select(.osv == \$id)]"'
    | (.[:$max][]
      | "  \(.trace[0].module)@\(.trace[0].version) (fixed in \(.fixed_version // "none"))\n    " +
        ([.trace[] | "\(.package).\(if .receiver then .receiver + "." else "" end)\(.function)"] | reverse | join("\n      -> "))),
      (select(length > $max) | "  ... and \(length - $max) more traces")' \
    "$OUT"
done
echo
echo "Upgrade the affected dependency, or add the ID to $IGNORE_FILE with a reason."
exit 1
