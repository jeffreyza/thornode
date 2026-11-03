#!/bin/bash
# Regtest pivxd for mocknet. The first 250 blocks are proof-of-work.
# UPGRADE_POS activates at height 251; after that generatetoaddress stakes
# coins the wallet mined. Coinbase maturity on regtest is 100.
set -euo pipefail

DATADIR="${DATADIR:-/data}"
RPC_USER="${RPC_USER:-thorchain}"
RPC_PASSWORD="${RPC_PASSWORD:-password}"
BLOCK_TIME="${BLOCK_TIME:-60}"

mkdir -p "${DATADIR}" "${HOME}/.pivx-params"
if compgen -G "/opt/pivx/share/pivx/*.params" > /dev/null; then
  cp -n /opt/pivx/share/pivx/*.params "${HOME}/.pivx-params/" || true
fi

cat > "${DATADIR}/pivx.conf" <<EOF
regtest=1
server=1
listen=0
txindex=1
rpcuser=${RPC_USER}
rpcpassword=${RPC_PASSWORD}
rpcbind=0.0.0.0
rpcallowip=0.0.0.0/0
rpcport=51473
port=51476
EOF

pivxd -datadir="${DATADIR}" -regtest -printtoconsole &
pid="$!"
trap 'kill "${pid}" 2>/dev/null || true' EXIT

cli() {
  pivx-cli -datadir="${DATADIR}" -regtest "$@"
}

for _ in $(seq 1 90); do
  if cli getblockcount >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
cli getblockcount >/dev/null

address="$(cli getnewaddress)"
echo "PIVX regtest mining address ${address}"
# 110 blocks leaves the earliest coinbase mature (maturity 100) and stays
# inside the proof-of-work window (PoS starts at height 251).
cli generatetoaddress 110 "${address}"

while true; do
  if ! cli generatetoaddress 1 "${address}"; then
    echo "generatetoaddress failed; trying wallet staking generate" >&2
    cli generate 1 || echo "block generation failed" >&2
  fi
  sleep "${BLOCK_TIME}"
done
