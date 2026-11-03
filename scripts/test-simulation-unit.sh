#!/usr/bin/env bash
# Run the nested simulation module's hermetic tests. Pass a path for JSONL output.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
report=${1:-$(mktemp "${TMPDIR:-/tmp}/thornode-simulation-unit.XXXXXXXX.jsonl")}
mkdir -p "$(dirname "$report")"
report=$(cd "$(dirname "$report")" && pwd)/$(basename "$report")

cd "$repo_root/test/simulation"
set +e
flock -w 1200 /tmp/thornode-test-quality-build.lock \
  env -u DOGE_SIM_INTEGRATION_RPC_URL -u ZEC_SIM_INTEGRATION_RPC_URL \
  GOMAXPROCS=4 GOTOOLCHAIN=go1.26.10 \
  go test -p 2 -tags mocknet -count=1 -json ./... >"$report"
test_status=$?
set -e

python3 - "$report" "$test_status" <<'PY'
import json
import re
import sys

report, test_status = sys.argv[1], int(sys.argv[2])
allowed_skips = {
    "TestDogeStalledBroadcastIsNotDuplicated",
    "TestZecIsolatedFunding",
}
passed = set()
skipped = set()
failed = set()
package_passes = set()
gocheck_passes = {}
with open(report, encoding="utf-8") as stream:
    for line in stream:
        event = json.loads(line)
        action = event.get("Action")
        package = event.get("Package")
        test = event.get("Test")
        if action == "pass" and test:
            passed.add((package, test))
        elif action == "skip" and test:
            skipped.add((package, test))
        elif action == "fail":
            failed.add((package, test))
        if action == "pass" and not test:
            package_passes.add(package)
        if action == "output":
            match = re.search(r"OK: (\d+) passed", event.get("Output", ""))
            if match:
                gocheck_passes[package] = int(match.group(1))

unexpected_skips = sorted((package, test) for package, test in skipped
                          if test not in allowed_skips)
missing_expected_skips = sorted(allowed_skips - {test for _, test in skipped})
required_gocheck = {
    "actors/core": 1,
    "pkg/solana": 1,
    "pkg/types": 7,
}
missing_gocheck = [(suffix, minimum, gocheck_passes.get(
    "gitlab.com/thorchain/thornode/v3/test/simulation/" + suffix, 0))
    for suffix, minimum in required_gocheck.items()
    if gocheck_passes.get("gitlab.com/thorchain/thornode/v3/test/simulation/" + suffix, 0) < minimum]
print(f"simulation unit report: {report}")
print(f"{len(package_passes)} packages passed; {len(passed)} tests passed; "
      f"{len(skipped)} integration tests skipped")
if len(package_passes) < 14:
    print(f"EXPECTED AT LEAST 14 TEST PACKAGES, got {len(package_passes)}", file=sys.stderr)
for package, test in sorted(failed, key=lambda item: (item[0], item[1] or "")):
    print(f"FAIL {package} {test or '[package]'}", file=sys.stderr)
for package, test in unexpected_skips:
    print(f"UNEXPECTED SKIP {package} {test}", file=sys.stderr)
for test in missing_expected_skips:
    print(f"MISSING INTEGRATION EXCLUSION {test}", file=sys.stderr)
for suffix, minimum, actual in missing_gocheck:
    print(f"GOCHECK CASES {suffix}: expected at least {minimum}, got {actual}", file=sys.stderr)
if (test_status or len(package_passes) < 14 or failed or unexpected_skips or missing_expected_skips
        or missing_gocheck or not passed):
    sys.exit(1)
PY
