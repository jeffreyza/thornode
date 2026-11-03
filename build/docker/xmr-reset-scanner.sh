#!/usr/bin/env bash
set -euo pipefail

# Reset the local XMR scanner state for one bifrost/signer pair.
#
# This script is intentionally per-node. A THORNode operator should be able to
# run it on their own machine without cluster-wide assumptions.
#
# Modes:
#   1. Custom vault/height:
#      bash xmr-reset-scanner.sh --vault-pubkey <pubkey> --height <block>
#   2. All local XMR vaults to their recorded birthdays:
#      bash xmr-reset-scanner.sh --all-vaults
#
# The script:
#   1. Resets the local sidecar scanner state for the selected vault(s)
#   2. Recreates the local bifrost with a temporary compose override so XMR
#      restarts from the requested height
#
# Auth: use the signer's explicit XMR_FROST_AUTH_BEARER. If it is not already
# set in the host environment, read it from the running signer and export it
# for the Compose recreation without printing it.
#
# If the host has more than one local signer or bifrost container, pass
# --signer-container / --bifrost-container explicitly.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API="${API:-http://localhost:1317}"
COMPOSE_FILE="${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.xmr-chainnet.yml}"
SIGNER_CONTAINER="${SIGNER_CONTAINER-}"
BIFROST_CONTAINER="${BIFROST_CONTAINER-}"
BIFROST_SERVICE="${BIFROST_SERVICE-}"
VAULT_PUBKEY=""
HEIGHT=""
ALL_VAULTS=false
DRY_RUN=false

usage() {
  cat <<'EOF'
Reset the local XMR scanner state for one thornode operator.

Usage:
  bash xmr-reset-scanner.sh --vault-pubkey <pubkey> [--height <block>] [options]
  bash xmr-reset-scanner.sh --all-vaults [options]

Options:
  --vault-pubkey <pubkey>          Reset one vault
  --height <block>                 XMR height to restart bifrost scanning from
                                   If omitted, defaults to the vault birthday
  --all-vaults                     Reset all local XMR asgard vaults to birthdays
  --signer-container <name>        Local xmr-frost-signer container to use
  --bifrost-container <name>       Local bifrost container to recreate
  --bifrost-service <name>         Compose service name override for bifrost
  --compose-file <path>            Compose file to use for bifrost recreate
  --api <url>                      THORChain API base URL (default: http://localhost:1317)
  --dry-run                        Print actions without changing anything
  -h, --help                       Show this help

Examples:
  bash xmr-reset-scanner.sh --vault-pubkey cthorpub... --height 3647060
  bash xmr-reset-scanner.sh --vault-pubkey cthorpub...
  bash xmr-reset-scanner.sh --all-vaults
  bash xmr-reset-scanner.sh --vault-pubkey cthorpub... --height 3647060 \
    --signer-container docker-xmr-frost-signer-cow-1 \
    --bifrost-container docker-bifrost-cow-1
EOF
}

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

discover_single_container() {
  local pattern="$1"
  mapfile -t matches < <(docker ps --format '{{.Names}}' | grep -E "$pattern" || true)
  if [ "${#matches[@]}" -eq 0 ]; then
    fail "no running containers match pattern: $pattern"
  fi
  if [ "${#matches[@]}" -gt 1 ]; then
    printf 'Multiple matching containers found:\n' >&2
    printf '  %s\n' "${matches[@]}" >&2
    fail "pass an explicit container name"
  fi
  printf '%s\n' "${matches[0]}"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

vault_birthday_from_api() {
  local vault_pubkey="$1"
  local birthday
  birthday="$(
    curl -s "$API/thorchain/vaults/asgard" |
      jq -r --arg pk "$vault_pubkey" '.[] | select(.pub_key_frost == $pk) | (.vault_creation_height_xmr // 0)' |
      head -n1
  )"
  [ -n "$birthday" ] || fail "vault $vault_pubkey not found via $API/thorchain/vaults/asgard"
  [ "$birthday" -gt 0 ] 2>/dev/null || fail "vault $vault_pubkey has no XMR birthday recorded"
  printf '%s\n' "$birthday"
}

reset_vault_on_local_signer() {
  local signer="$1"
  local vault_pubkey="$2"
  local height="$3"

  local payload
  payload=$(printf '{"vault_pub_key":"%s","birthday_height":%s}\n' "$vault_pubkey" "$height")

  echo "Resetting $vault_pubkey on $signer to $height"
  if $DRY_RUN; then
    echo "  [dry-run] payload: $payload"
    return
  fi

  docker exec -i "$signer" sh -lc \
    ': "${XMR_FROST_AUTH_BEARER:?XMR_FROST_AUTH_BEARER is not configured}"; \
      cat >/tmp/xmr-reset-scanner-payload.json && curl -s -X POST \
      -H "Authorization: Bearer $XMR_FROST_AUTH_BEARER" \
      -H "Content-Type: application/json" \
      --data @/tmp/xmr-reset-scanner-payload.json \
      http://localhost:8283/v1/monero/scanner/reset' <<<"$payload"
  echo
}

restart_local_bifrost() {
  local bifrost_container="$1"
  local restart_height="$2"
  local compose_file="$3"
  local service_name="$4"

  [ -f "$compose_file" ] || fail "compose file not found: $compose_file"

  if [ -z "$service_name" ]; then
    service_name="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.service" }}' "$bifrost_container" 2>/dev/null || true)"
  fi
  [ -n "$service_name" ] || fail "unable to determine compose service for $bifrost_container"

  echo "Recreating $bifrost_container ($service_name) from XMR height $restart_height"
  if $DRY_RUN; then
    echo "  [dry-run] docker compose -f $compose_file -f <override> up -d --no-deps --force-recreate $service_name"
    return
  fi

  local override_file
  override_file="$(mktemp "${TMPDIR:-/tmp}/xmr-reset-scanner.override.XXXXXX")"
  trap 'rm -f "$override_file"' EXIT

  cat >"$override_file" <<EOF
services:
  $service_name:
    environment:
      BIFROST_CHAINS_XMR_BLOCK_SCANNER_START_BLOCK_HEIGHT: "$restart_height"
EOF

  docker compose -f "$compose_file" -f "$override_file" up -d --no-deps --force-recreate "$service_name"
  rm -f "$override_file"
  trap - EXIT
}

while [ $# -gt 0 ]; do
  case "$1" in
  --vault-pubkey)
    [ $# -ge 2 ] || fail "--vault-pubkey requires a value"
    VAULT_PUBKEY="$2"
    shift 2
    ;;
  --height)
    [ $# -ge 2 ] || fail "--height requires a value"
    HEIGHT="$2"
    shift 2
    ;;
  --all-vaults)
    ALL_VAULTS=true
    shift
    ;;
  --signer-container)
    [ $# -ge 2 ] || fail "--signer-container requires a value"
    SIGNER_CONTAINER="$2"
    shift 2
    ;;
  --bifrost-container)
    [ $# -ge 2 ] || fail "--bifrost-container requires a value"
    BIFROST_CONTAINER="$2"
    shift 2
    ;;
  --bifrost-service)
    [ $# -ge 2 ] || fail "--bifrost-service requires a value"
    BIFROST_SERVICE="$2"
    shift 2
    ;;
  --compose-file)
    [ $# -ge 2 ] || fail "--compose-file requires a value"
    COMPOSE_FILE="$2"
    shift 2
    ;;
  --api)
    [ $# -ge 2 ] || fail "--api requires a value"
    API="$2"
    shift 2
    ;;
  --dry-run)
    DRY_RUN=true
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    fail "unknown argument: $1"
    ;;
  esac
done

require_cmd docker
require_cmd curl
require_cmd jq

if ! $ALL_VAULTS; then
  [ -n "$VAULT_PUBKEY" ] || fail "pass --vault-pubkey <pubkey> or --all-vaults"
  if [ -z "$HEIGHT" ]; then
    HEIGHT="$(vault_birthday_from_api "$VAULT_PUBKEY")"
    echo "Using vault birthday for $VAULT_PUBKEY: $HEIGHT"
  fi
fi

if [ -z "$SIGNER_CONTAINER" ]; then
  SIGNER_CONTAINER="$(discover_single_container '^docker-xmr-frost-signer')"
fi

signer_auth_bearer="$(docker exec "$SIGNER_CONTAINER" sh -c 'printf "%s" "${XMR_FROST_AUTH_BEARER:-}"')"
[ -n "$signer_auth_bearer" ] || fail "XMR_FROST_AUTH_BEARER is not configured in $SIGNER_CONTAINER"
if [ -n "${XMR_FROST_AUTH_BEARER-}" ] && [ "$XMR_FROST_AUTH_BEARER" != "$signer_auth_bearer" ]; then
  fail "host XMR_FROST_AUTH_BEARER does not match $SIGNER_CONTAINER"
fi
XMR_FROST_AUTH_BEARER="$signer_auth_bearer"
export XMR_FROST_AUTH_BEARER
unset signer_auth_bearer

if [ -z "$BIFROST_CONTAINER" ]; then
  BIFROST_CONTAINER="$(discover_single_container '^docker-bifrost')"
fi

if $ALL_VAULTS; then
  echo "Fetching XMR asgard vaults from $API"
  vaults="$(curl -s "$API/thorchain/vaults/asgard")"
  count="$(echo "$vaults" | jq length)"
  [ "$count" -gt 0 ] || fail "no asgard vaults returned from $API"

  earliest_height=0
  found_any=false
  for i in $(seq 0 $((count - 1))); do
    vault="$(echo "$vaults" | jq ".[$i]")"
    chains="$(echo "$vault" | jq -r '.chains[]?' 2>/dev/null || true)"
    if ! echo "$chains" | grep -q 'XMR'; then
      continue
    fi

    vault_pubkey="$(echo "$vault" | jq -r '.pub_key_frost // empty')"
    if [ -z "$vault_pubkey" ]; then
      echo "Skipping vault without pub_key_frost"
      continue
    fi
    vault_height="$(echo "$vault" | jq -r '.vault_creation_height_xmr // 0')"
    if [ "$vault_height" -le 0 ] 2>/dev/null; then
      echo "Skipping $vault_pubkey: no XMR birthday recorded"
      continue
    fi

    found_any=true
    if [ "$earliest_height" -eq 0 ] || [ "$vault_height" -lt "$earliest_height" ]; then
      earliest_height="$vault_height"
    fi
    reset_vault_on_local_signer "$SIGNER_CONTAINER" "$vault_pubkey" "$vault_height"
  done

  $found_any || fail "no XMR vaults with birthdays found"
  restart_local_bifrost "$BIFROST_CONTAINER" "$earliest_height" "$COMPOSE_FILE" "$BIFROST_SERVICE"
else
  reset_vault_on_local_signer "$SIGNER_CONTAINER" "$VAULT_PUBKEY" "$HEIGHT"
  restart_local_bifrost "$BIFROST_CONTAINER" "$HEIGHT" "$COMPOSE_FILE" "$BIFROST_SERVICE"
fi

echo
echo "Done."
