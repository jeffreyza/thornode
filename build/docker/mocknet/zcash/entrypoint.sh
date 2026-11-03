#!/bin/sh

set -e

BLOCK_TIME=${BLOCK_TIME:-1}
INIT_BLOCKS=400 # Keep aligned with waitForZecRPCReadinessWithClock.
BOOTSTRAP_TIMEOUT=${BOOTSTRAP_TIMEOUT:-480}
# Let a sent request finish even when the host stalls for tens of seconds.
MINING_RPC_TIMEOUT=${MINING_RPC_TIMEOUT:-600}
RPC_CONNECT_TIMEOUT=${RPC_CONNECT_TIMEOUT:-10}
RPC_URL="http://127.0.0.1:18232"

zebrad --config /etc/zebrad.toml &
ZEBRAD_PID=$!
response_file=""
cleanup() {
  kill "$ZEBRAD_PID" 2>/dev/null || true
  wait "$ZEBRAD_PID" 2>/dev/null || true
  if [ -n "$response_file" ]; then
    rm -f "$response_file"
  fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM

echo "waiting for zebrad RPC to be ready..."
deadline=$(($(date +%s) + BOOTSTRAP_TIMEOUT))
while kill -0 "$ZEBRAD_PID" 2>/dev/null; do
  if count=$(curl --max-time 5 -sf -X POST "$RPC_URL" \
    -H "Content-Type: application/json" \
    -d '{"method":"getblockcount","params":[],"id":1}' 2>/dev/null); then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "timed out waiting for zebrad RPC" >&2
    exit 1
  fi
  sleep 1
done
kill -0 "$ZEBRAD_PID"

# One miner and one block per request, including warm-up. A long initial batch
# can keep resetting Zebra's mempool while clients begin funding. Mine up to
# INIT_BLOCKS without a delay, then use the regular block interval. The simulator
# waits past this count before funding and never calls generate.
# 100 blocks for coinbase maturity + 20 inputs per user * 12 users = 340;
# 400 leaves headroom for funding all simulation users.
# Resume from the restored height without another warm-up burst.
blocks_mined=$(printf '%s' "$count" | sed -n 's/.*"result"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
blocks_mined=${blocks_mined:-0}
response_file=$(mktemp)
while kill -0 "$ZEBRAD_PID" 2>/dev/null; do
  # Zebra can keep mining after curl gives up. Retry only when curl positively
  # reports that no request bytes were sent; missing statistics are ambiguous.
  # Otherwise stop mining and keep Zebra alive so a failed RPC cannot restart
  # the chain or overlap another mining request with unfinished server work.
  rc=0
  : >"$response_file"
  sent=$(curl --connect-timeout "$RPC_CONNECT_TIMEOUT" --max-time "$MINING_RPC_TIMEOUT" \
    -sS -o "$response_file" -w '%{size_request}' -X POST "$RPC_URL" \
    -H "Content-Type: application/json" \
    -d '{"method":"generate","params":[1],"id":1}') || rc=$?
  if [ "$rc" -ne 0 ] && [ "$sent" = "0" ]; then
    echo "single-block generation request was not sent (curl exit $rc); retrying" >&2
    sleep 1
    continue
  fi
  if [ "$rc" -ne 0 ] || ! grep -Eq '"result"[[:space:]]*:[[:space:]]*\[[[:space:]]*"[[:xdigit:]]{64}"[[:space:]]*\]' "$response_file"; then
    response=$(cat "$response_file")
    echo "single-block generation failed (curl exit $rc): $response; stopping mining, zebrad keeps running" >&2
    break
  fi
  blocks_mined=$((blocks_mined + 1))
  if [ "$blocks_mined" -ge "$INIT_BLOCKS" ]; then
    sleep "$BLOCK_TIME"
  fi
done

wait "$ZEBRAD_PID"
