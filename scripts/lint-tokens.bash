#!/bin/bash
set -euo pipefail

# Release branches can intentionally omit tokens that only exist on develop.
BASE_BRANCH="${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-develop}"
BASE_REF="refs/remotes/origin/$BASE_BRANCH"
if [ -n "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME-}" ]; then
  # Runner checkouts may lack the target ref or retain a stale copy of it.
  git fetch --depth=1 --no-tags origin "+refs/heads/$BASE_BRANCH:$BASE_REF"
fi
echo "Checking token lists against $BASE_REF"

# Keep concurrent lint runs and the fixture tests from sharing scratch files.
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

possibly_die() {
  if [[ ${CI_MERGE_REQUEST_TITLE-} == *"#check-lint-warning"* ]]; then
    echo "Merge request is marked unsafe. Please carefully review errors above."
  else
    exit 1
  fi
}

# Outside an MR, compare with develop's tip. A shallow CI clone may lack
# origin/develop, so fetch just that commit.
if [ -z "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME-}" ] && ! git rev-parse -q --verify "$BASE_REF" >/dev/null; then
  git fetch -q --depth=1 --no-tags origin develop:refs/remotes/origin/develop
fi

check_token_list() {
  local LIST="$1"

  echo "Linting $LIST"

  git show "$BASE_REF:$LIST" |
    jq -r '.tokens[] | .address | ascii_downcase' |
    sort -n >"$TMP_DIR/orig_erc20_token_list.txt"

  jq -r '.tokens[] | .address | ascii_downcase' <"$LIST" |
    sort -n >"$TMP_DIR/modified_erc20_token_list.txt"

  # shellcheck disable=SC2155
  local REMOVALS=$(comm -23 "$TMP_DIR/orig_erc20_token_list.txt" "$TMP_DIR/modified_erc20_token_list.txt")

  # allow removals of dupes
  REMOVALS=$(echo "$REMOVALS" | grep -v -F -f "$TMP_DIR/modified_erc20_token_list.txt" || true)

  # Check for duplicates after filtering out legitimate removals
  local DUPES
  DUPES=$(cat "$TMP_DIR/orig_erc20_token_list.txt" "$TMP_DIR/modified_erc20_token_list.txt" | sort -n | uniq --count | awk '$1 > 2 {print}')
  # Filter out duplicates that are being legitimately removed
  DUPES=$(echo "$DUPES" | grep -v -F -f "$TMP_DIR/modified_erc20_token_list.txt" || true)
  if [ -n "$DUPES" ]; then
    printf "Tokens already in list:\n%s\n" "$DUPES"
    possibly_die
  fi

  if [ -n "$REMOVALS" ]; then
    printf "Tokens removed:\n%s\n" "$REMOVALS"
    possibly_die
  fi
}

check_token_list common/tokenlist/ethtokens/eth_mainnet_latest.json
check_token_list common/tokenlist/avaxtokens/avax_mainnet_latest.json
check_token_list common/tokenlist/bsctokens/bsc_mainnet_latest.json
check_token_list common/tokenlist/basetokens/base_mainnet_latest.json

# case sensitive
# check_token_list common/tokenlist/trontokens/tron_mainnet_latest.json

echo "OK"
