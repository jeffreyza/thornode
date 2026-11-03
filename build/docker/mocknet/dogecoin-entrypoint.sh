#!/bin/sh

# This script supervises dogecoind and deliberately does not use `set -e`.
# Under CI load a single generatetoaddress call can exceed the daemon's HTTP
# timeout and fail with "EOF reached". Exiting on that error restarted the
# container, which killed dogecoind before it flushed the regtest chain. The
# restarted node lost recent blocks and this script mined a fresh initial batch
# on a new branch, dropping confirmed simulation funding and vault UTXOs.
# Block production failures are now logged and retried. The container exits
# only when dogecoind exits, and stop signals are forwarded to dogecoind.

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${DOGE_MASTER_ADDR:=mtzUk1zTJzTdyC8Pz6PPPyCHTEL5RLVyDJ}"
BLOCK_TIME="${BLOCK_TIME:=1}"
INITIAL_BLOCKS="${DOGE_INITIAL_BLOCKS:=1000}"
STARTUP_RETRY_SECONDS="${DOGE_STARTUP_RETRY_SECONDS:=5}"

# Bound each CLI call so a stalled request is retried instead of blocking block
# production indefinitely (dogecoin-cli defaults to 900 seconds).
RPC_CLIENT_TIMEOUT="${DOGE_RPC_CLIENT_TIMEOUT:=60}"

cli() {
  dogecoin-cli -regtest -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" \
    -rpcclienttimeout="$RPC_CLIENT_TIMEOUT" "$@"
}

log() {
  echo "dogecoin-entrypoint: $*" >&2
}

# DOGE startup creates a burst of wallet RPCs during simulation user creation.
# Raise the daemon work queue above the default so queued importaddress calls do
# not fail with "Work queue depth exceeded" under local mocknet load. Extra RPC
# threads and a longer server timeout keep bifrost, the simulation, and block
# production from timing out behind one slow wallet call.
dogecoind \
  -regtest \
  -txindex \
  -rpcworkqueue=256 \
  -rpcthreads=16 \
  -rpcservertimeout=120 \
  -rpcuser="$SIGNER_NAME" \
  -rpcpassword="$SIGNER_PASSWD" \
  -rpcallowip=0.0.0.0/0 \
  -rpcbind=127.0.0.1 \
  -rpcbind="$(hostname)" &
DOGECOIND_PID=$!
PRODUCER_PID=

# Forward stop signals so dogecoind flushes the chain and wallet before exit.
# shellcheck disable=SC2317 # invoked by the trap below
shutdown() {
  log "stopping dogecoind (pid $DOGECOIND_PID)"
  [ -n "$PRODUCER_PID" ] && kill -TERM "$PRODUCER_PID" 2>/dev/null
  kill -TERM "$DOGECOIND_PID" 2>/dev/null
  wait "$DOGECOIND_PID"
  exit 0
}
trap shutdown TERM INT

# Mine up to INITIAL_BLOCKS once, then one block every BLOCK_TIME. After a
# restart the existing chain is extended rather than replaced by a new branch.
produce_blocks() {
  while true; do
    if height=$(cli getblockcount 2>/dev/null); then
      if [ "$height" -ge "$INITIAL_BLOCKS" ]; then
        log "chain height $height, initial blocks already mined"
        break
      fi
      missing=$((INITIAL_BLOCKS - height))
      log "chain height $height, mining $missing initial blocks"
      cli generatetoaddress "$missing" "$MASTER_ADDR" >/dev/null && continue
    fi
    sleep "$STARTUP_RETRY_SECONDS"
  done

  failures=0
  while true; do
    if cli generatetoaddress 1 "$MASTER_ADDR" >/dev/null; then
      if [ "$failures" -gt 0 ]; then
        log "block generation recovered after $failures failed attempts"
      fi
      failures=0
    else
      failures=$((failures + 1))
      log "block generation failed (attempt $failures), dogecoind left running"
    fi
    sleep "$BLOCK_TIME"
  done
}
produce_blocks &
PRODUCER_PID=$!

# The container lives exactly as long as dogecoind. If the daemon exits on its
# own, exit non-zero so Docker restarts the container.
wait "$DOGECOIND_PID"
status=$?
kill -TERM "$PRODUCER_PID" 2>/dev/null
log "dogecoind exited with status $status"
[ "$status" -eq 0 ] && status=1
exit "$status"
