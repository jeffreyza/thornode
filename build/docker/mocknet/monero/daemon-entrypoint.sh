#!/bin/sh

set -eu

DATA_DIR="${XMR_DATA_DIR:-/var/lib/monero}"
RPC_BIND_IP="${XMR_RPC_BIND_IP:-0.0.0.0}"
RPC_BIND_PORT="${XMR_RPC_BIND_PORT:-18081}"
P2P_BIND_IP="${XMR_P2P_BIND_IP:-0.0.0.0}"
P2P_BIND_PORT="${XMR_P2P_BIND_PORT:-18080}"

mkdir -p "${DATA_DIR}"

exec monerod \
  --regtest \
  --offline \
  --non-interactive \
  --confirm-external-bind \
  --rpc-bind-ip "${RPC_BIND_IP}" \
  --rpc-bind-port "${RPC_BIND_PORT}" \
  --p2p-bind-ip "${P2P_BIND_IP}" \
  --p2p-bind-port "${P2P_BIND_PORT}" \
  --fixed-difficulty 1 \
  --data-dir "${DATA_DIR}" \
  --log-file "${DATA_DIR}/monerod.log"
