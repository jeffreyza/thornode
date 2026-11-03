#!/bin/bash

set -o pipefail

CHAIN_API="${CHAIN_API:=127.0.0.1:1317}"

. "$(dirname "$0")/core.sh"

resolve_signer_seed_phrase

"$(dirname "$0")/wait-for-thorchain-api.sh" "$CHAIN_API"

wait_for_tss_bootstrap_peer() {
  local peers="$1"
  local peer=""
  local peer_id=""

  if [ -z "$peers" ]; then
    return 0
  fi

  # Bound the wait: every bifrost blocks here BEFORE exec, so it is not yet
  # listening on :6040 while it waits. With a cyclic peer mesh and a coordinated
  # restart, all nodes wait and none listen -> the wait never resolves and the
  # fleet deadlocks permanently. On timeout, start anyway and let libp2p discover
  # peers normally: a node that never starts is strictly worse than one that
  # starts unbootstrapped.
  local timeout="${BIFROST_PEER_WAIT_TIMEOUT:-300}"
  # Reject non-digits and multi-digit leading-zero values to avoid Bash's
  # octal parsing. Limit length before arithmetic to prevent overflow.
  case "$timeout" in
  '' | *[!0-9]* | 0[0-9]*) timeout=300 ;;
  esac
  [ "${#timeout}" -le 9 ] || timeout=300
  local deadline=$(($(date +%s) + timeout))

  echo "Waiting for bifrost bootstrap peer(s): $peers (timeout ${timeout}s)"
  until [ -n "$peer_id" ]; do
    IFS=',' read -r -a peer_list <<<"$peers"
    for peer in "${peer_list[@]}"; do
      peer_id=$(curl -fsS --max-time 2 "http://$peer:6040/p2pid" 2>/dev/null || true)
      if [ -n "$peer_id" ]; then
        break
      fi
    done
    if [ -z "$peer_id" ] && [ "$(date +%s)" -ge "$deadline" ]; then
      echo "ERROR: timed out after ${timeout}s waiting for bootstrap peer(s): $peers; starting anyway. NOTE: on non-mocknet builds bifrost treats unreachable configured bootstrap peers as fatal (see bifrost/p2p/bootstrap_connectivity_mainnet.go), so startup may still fail and the container may restart until a peer becomes reachable" >&2
      return 0
    fi
    sleep 1
  done

  echo "Bootstrap peer ready: $peer ($peer_id)"
}

wait_for_tss_bootstrap_peer "${PEER-}"

create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

# dynamically set external ip if mocknet and unset
if [ "$NET" = "mocknet" ] && [ -z "$EXTERNAL_IP" ]; then
  EXTERNAL_IP=$(hostname -i)
fi

export SIGNER_NAME SIGNER_PASSWD
exec "$@"
