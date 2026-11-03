#!/usr/bin/env bash
set -euo pipefail

# FUZZ_TARGET selects one named target or "all". Every target gets its own go invocation.
target_filter=${FUZZ_TARGET:-all}
budget=${FUZZ_TIME:-5s}
corpus_dir=${FUZZ_CORPUS_DIR:-/tmp/thornode-security-fuzz-corpus}
mkdir -p "$corpus_dir"
corpus_dir=$(cd "$corpus_dir" && pwd)

targets=(
  'FuzzParseMemo|./x/thorchain/memo'
  'FuzzNewAddress|./common'
  'FuzzRPCVersion|./bifrost/pkg/chainclients/solana/rpc'
  'FuzzFrostEvidenceDecode|./bifrost/p2p/messages'
  'FuzzFrostSignedPayload|./bifrost/p2p/messages'
)

selected=0
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  if [[ $target_filter == all || $target_filter == "$name" ]]; then
    selected=$((selected + 1))
  fi
done
if ((selected == 0)); then
  printf 'No fuzz targets selected for %q\n' "$target_filter" >&2
  exit 2
fi

# Keep the build cache (CI's /go-build-cache volume, or the local default) and
# persist only the generated corpus. Go reads and writes the corpus under
# $GOCACHE/fuzz/<import path>, so mirror this module's subtree in before
# fuzzing and back out on any exit. Clear it first: the build cache volume is
# shared by every branch, and only the corpus from $corpus_dir may be replayed.
# The lock lives on that volume, so jobs in other containers sharing it wait
# instead of swapping the subtree out from under each other.
gocache=$(GOTOOLCHAIN=go1.26.10 go env GOCACHE)
mkdir -p "$gocache"
exec 9>"$gocache/.thornode-security-fuzz.lock"
flock -w 1200 9 || {
  echo 'Timed out waiting for the shared fuzz cache lock' >&2
  exit 1
}
fuzz_cache=$gocache/fuzz/gitlab.com/thorchain/thornode/v3
rm -rf "$fuzz_cache"
mkdir -p "$fuzz_cache"
cp -a "$corpus_dir/." "$fuzz_cache/"
trap 'cp -a "$fuzz_cache/." "$corpus_dir/"' EXIT
printf 'Fuzzing %d target(s), each for %s; corpus cache: %s\n' "$selected" "$budget" "$corpus_dir"
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  package=${spec#*|}
  if [[ $target_filter != all && $target_filter != "$name" ]]; then
    continue
  fi
  # Go replays each cached corpus entry once before mutating and counts those
  # runs toward an Nx budget, so add them to keep N mutated executions.
  fuzztime=$budget
  cached=$fuzz_cache/${package#./}/$name
  if [[ $budget =~ ^([0-9]+)x$ && -d $cached ]]; then
    fuzztime="$((BASH_REMATCH[1] + $(find "$cached" -type f | wc -l)))x"
  fi
  printf '\nRunning %s in %s for %s\n' "$name" "$package" "$fuzztime"
  GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 go test -p 2 -parallel 4 -run '^$' -fuzz "^${name}$" -fuzztime "$fuzztime" "$package"
done
echo 'Completed targets:'
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  if [[ $target_filter == all || $target_filter == "$name" ]]; then
    printf '  %s\n' "$name"
  fi
done
