#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
LINTER="$SCRIPT_DIR/../lint-tokens.bash"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

# Exercise real Git fetches using local fixtures, independent of user Git config.
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null LC_ALL=C
export GIT_AUTHOR_NAME="Token lint test" GIT_AUTHOR_EMAIL="test@example.invalid"
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
RELEASE=mainnet-3.20.4
RELEASE_TOKEN=0x0000000000000000000000000000000000000001
DEVELOP_TOKEN=0x0000000000000000000000000000000000000002
NEW_TARGET_TOKEN=0x0000000000000000000000000000000000000003
LISTS=(
  common/tokenlist/ethtokens/eth_mainnet_latest.json
  common/tokenlist/avaxtokens/avax_mainnet_latest.json
  common/tokenlist/bsctokens/bsc_mainnet_latest.json
  common/tokenlist/basetokens/base_mainnet_latest.json
)

write_lists() {
  local list
  for list in "${LISTS[@]}"; do
    mkdir -p "$(dirname "$list")"
    jq -n --args '{tokens: ($ARGS.positional | map({address: .}))}' "$@" >"$list"
  done
}

run_lint() {
  local name=$1 expected=$2 target=$3 title=${4-} status=0
  env CI_MERGE_REQUEST_TARGET_BRANCH_NAME="$target" CI_MERGE_REQUEST_TITLE="$title" \
    bash "$LINTER" >"$TEST_ROOT/output" 2>&1 || status=$?
  if [[ $status != "$expected" ]]; then
    echo "FAIL: $name (expected exit $expected, got $status)"
    cat "$TEST_ROOT/output"
    exit 1
  fi
  echo "PASS: $name"
}

expect_output() {
  if ! grep -Fq -- "$1" "$TEST_ROOT/output"; then
    echo "FAIL: expected output: $1"
    cat "$TEST_ROOT/output"
    exit 1
  fi
}

git init --quiet --initial-branch="$RELEASE" "$TEST_ROOT/remote"
cd "$TEST_ROOT/remote"
write_lists "$RELEASE_TOKEN"
git add common
git -c core.hooksPath=/dev/null commit --quiet -m 'Release token lists'
git checkout --quiet -b develop
write_lists "$RELEASE_TOKEN" "$DEVELOP_TOKEN"
git add common
git -c core.hooksPath=/dev/null commit --quiet -m 'Develop-only tokens'

git clone --quiet --single-branch --branch "$RELEASE" "$TEST_ROOT/remote" "$TEST_ROOT/checkout"
cd "$TEST_ROOT/checkout"
git update-ref -d "refs/remotes/origin/$RELEASE"
run_lint 'fetch missing MR target ref in a single-branch checkout' 0 "$RELEASE"
expect_output "refs/remotes/origin/$RELEASE"

git fetch --quiet origin refs/heads/develop:refs/remotes/origin/develop
run_lint 'release list passes despite develop-only tokens' 0 "$RELEASE"
run_lint 'non-MR fallback still rejects removals from develop' 1 ''
expect_output 'Tokens removed:'
expect_output "$DEVELOP_TOKEN"
write_lists "$RELEASE_TOKEN" "$DEVELOP_TOKEN"
run_lint 'unchanged develop list passes outside an MR' 0 ''
run_lint 'unchanged develop list passes in an MR' 0 develop
write_lists "$RELEASE_TOKEN"
run_lint 'MRs targeting develop reject a removed token' 1 develop
expect_output "$DEVELOP_TOKEN"

for list in "${LISTS[@]}"; do
  printf '{"tokens":[]}\n' >"$list"
  run_lint "real removal is rejected: $list" 1 "$RELEASE"
  expect_output 'Tokens removed:'
  expect_output "$RELEASE_TOKEN"
  write_lists "$RELEASE_TOKEN"
done

write_lists "$RELEASE_TOKEN" "$DEVELOP_TOKEN"
run_lint 'adding tokens remains allowed' 0 "$RELEASE"
write_lists
run_lint 'explicit warning override still works' 0 "$RELEASE" '#check-lint-warning'
expect_output 'Merge request is marked unsafe.'
write_lists "$RELEASE_TOKEN"

git -C "$TEST_ROOT/remote" checkout --quiet "$RELEASE"
(
  cd "$TEST_ROOT/remote"
  write_lists "$RELEASE_TOKEN" "$NEW_TARGET_TOKEN"
  git add common
  git -c core.hooksPath=/dev/null commit --quiet -m 'Target branch advances'
)
run_lint 'refresh stale MR target ref before comparing' 1 "$RELEASE"
expect_output 'Tokens removed:'
expect_output "$NEW_TARGET_TOKEN"

run_lint 'missing target fails even with the warning override' 128 release/missing '#check-lint-warning'
expect_output "couldn't find remote ref refs/heads/release/missing"

# A usable local copy must not hide a failed fetch of a deleted remote branch.
git update-ref refs/remotes/origin/release/gone HEAD
run_lint 'fetch failure is fatal even with a usable stale ref' 128 release/gone
expect_output "couldn't find remote ref refs/heads/release/gone"
run_lint 'warning override cannot hide a failed fetch with a usable stale ref' 128 release/gone '#check-lint-warning'
expect_output "couldn't find remote ref refs/heads/release/gone"

# Rewind only the disposable fixture's ref to simulate a rewritten MR target.
git -C "$TEST_ROOT/remote" update-ref "refs/heads/$RELEASE" HEAD~1
run_lint 'refresh a rewritten MR target ref' 0 "$RELEASE"

echo 'All token lint tests passed.'
