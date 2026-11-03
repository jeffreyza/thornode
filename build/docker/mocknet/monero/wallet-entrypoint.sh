#!/bin/sh

set -eu

WALLET_DIR="${XMR_WALLET_DIR:-/var/lib/monero-wallet}"
WALLET_NAME="${XMR_WALLET_NAME:-mocknet}"
WALLET_PASSWORD="${XMR_WALLET_PASSWORD:-password}"
WALLET_RPC_BIND_IP="${XMR_WALLET_RPC_BIND_IP:-0.0.0.0}"
WALLET_RPC_BIND_PORT="${XMR_WALLET_RPC_BIND_PORT:-18082}"
DAEMON_HOST="${XMR_DAEMON_HOST:-monero-daemon}"
DAEMON_PORT="${XMR_DAEMON_PORT:-18081}"
BOOTSTRAP_HEIGHT="${XMR_BOOTSTRAP_HEIGHT:-512}"
BLOCKS_PER_TICK="${XMR_BLOCKS_PER_TICK:-1}"
BLOCK_TICK_SECONDS="${XMR_BLOCK_TICK_SECONDS:-2}"
DAEMON_HTTP_URL="http://${DAEMON_HOST}:${DAEMON_PORT}"
RPC_URL="http://127.0.0.1:${WALLET_RPC_BIND_PORT}/json_rpc"
DAEMON_RPC_URL="${DAEMON_HTTP_URL}/json_rpc"

mkdir -p "${WALLET_DIR}"

wait_for_daemon() {
  retries=0
  until daemon_request "get_block_count" '{}' >/dev/null; do
    retries=$((retries + 1))
    if [ "${retries}" -ge 120 ]; then
      echo "monero daemon did not become ready in time" >&2
      return 1
    fi
    sleep 1
  done
}

wait_for_wallet_rpc() {
  retries=0
  until curl -sf \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"0","method":"get_version"}' \
    "${RPC_URL}" >/dev/null; do
    retries=$((retries + 1))
    if [ "${retries}" -ge 120 ]; then
      echo "monero wallet rpc did not become ready in time" >&2
      return 1
    fi
    sleep 1
  done
}

wallet_request() {
  method="$1"
  params="$2"
  payload=$(printf '{"jsonrpc":"2.0","id":"0","method":"%s","params":%s}' "${method}" "${params}")
  curl -sf -H "Content-Type: application/json" -d "${payload}" "${RPC_URL}"
}

daemon_request() {
  method="$1"
  params="$2"
  payload=$(printf '{"jsonrpc":"2.0","id":"0","method":"%s","params":%s}' "${method}" "${params}")
  curl -sf -H "Content-Type: application/json" -d "${payload}" "${DAEMON_RPC_URL}"
}

current_height() {
  height="$(
    curl -sf "${DAEMON_HTTP_URL}/get_height" 2>/dev/null |
      jq -r '.height // 0' 2>/dev/null || echo 0
  )"

  case "${height}" in
  '' | *[!0-9]*)
    echo 0
    ;;
  *)
    echo "${height}"
    ;;
  esac
}

generate_blocks() {
  block_count="$1"
  resp="$(
    daemon_request \
      "generateblocks" \
      "$(printf '{"wallet_address":"%s","amount_of_blocks":%s}' "${wallet_address}" "${block_count}")" || true
  )"
  status="$(printf '%s' "${resp}" | jq -r '.result.status // empty' 2>/dev/null || true)"
  if [ "${status}" = "OK" ]; then
    return 0
  fi

  echo "failed to generate monero blocks: ${resp}" >&2
  return 1
}

ensure_daemon_blocks() {
  while kill -0 "${wallet_pid}" 2>/dev/null; do
    wait_for_daemon || return 1

    height="$(current_height)"
    blocks_to_generate="${BLOCKS_PER_TICK}"
    if [ "${height}" -lt "${BOOTSTRAP_HEIGHT}" ]; then
      blocks_to_generate=$((BOOTSTRAP_HEIGHT - height))
    fi

    if ! generate_blocks "${blocks_to_generate}"; then
      sleep 1
      continue
    fi

    sleep "${BLOCK_TICK_SECONDS}"
  done
}

wait_for_daemon

monero-wallet-rpc \
  --non-interactive \
  --confirm-external-bind \
  --rpc-bind-ip "${WALLET_RPC_BIND_IP}" \
  --rpc-bind-port "${WALLET_RPC_BIND_PORT}" \
  --disable-rpc-login \
  --trusted-daemon \
  --allow-mismatched-daemon-version \
  --daemon-address "${DAEMON_HOST}:${DAEMON_PORT}" \
  --wallet-dir "${WALLET_DIR}" \
  --log-file "${WALLET_DIR}/monero-wallet-rpc.log" &
wallet_pid=$!
block_pid=

shutdown() {
  if [ -n "${block_pid-}" ] && kill -0 "${block_pid}" 2>/dev/null; then
    kill "${block_pid}" 2>/dev/null || true
    wait "${block_pid}" 2>/dev/null || true
  fi
  if kill -0 "${wallet_pid}" 2>/dev/null; then
    kill "${wallet_pid}" 2>/dev/null || true
    wait "${wallet_pid}" 2>/dev/null || true
  fi
}
trap shutdown INT TERM

wait_for_wallet_rpc

wallet_file="${WALLET_DIR}/${WALLET_NAME}"
wallet_keys="${wallet_file}.keys"
if [ -f "${wallet_file}" ] && [ -f "${wallet_keys}" ]; then
  wallet_request "open_wallet" "$(printf '{"filename":"%s","password":"%s"}' "${WALLET_NAME}" "${WALLET_PASSWORD}")" >/dev/null
  wallet_request "rescan_blockchain" '{}' >/dev/null
else
  wallet_request "create_wallet" "$(printf '{"filename":"%s","password":"%s","language":"English"}' "${WALLET_NAME}" "${WALLET_PASSWORD}")" >/dev/null
fi

address_resp="$(wallet_request "get_address" '{}')"
wallet_address="$(printf '%s' "${address_resp}" | jq -r '.result.address // empty')"
if [ -z "${wallet_address}" ]; then
  echo "failed to discover mocknet wallet address" >&2
  exit 1
fi

ensure_daemon_blocks &
block_pid=$!

wait "${wallet_pid}"
