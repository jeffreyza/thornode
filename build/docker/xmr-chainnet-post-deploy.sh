#!/usr/bin/env bash
set -euo pipefail

CHAIN_ID="thorchain"
KEYRING="file"
PASSWORD="password"
DOG_RPC="tcp://localhost:27147"
CAT_RPC="tcp://thornode-cat:27147"
FOX_RPC="tcp://thornode-fox:27147"

DOG_NODE_ADDR="cthor1zf3gsk7edzwl9syyefvfhle37cjtql35layy8z"
CAT_SEED="cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat crawl"
FOX_SEED="fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox filter"

tx() {
  local container="$1"
  shift
  docker exec "$container" bash -c "printf '${PASSWORD}\n${PASSWORD}\n' | thornode tx thorchain $* --from thorchain --keyring-backend $KEYRING --chain-id $CHAIN_ID --yes" 2>&1
}

mimir() {
  local key="$1"
  local val="$2"
  echo "  mimir $key = $val"
  tx docker-thornode-1 "mimir $key $val --node $DOG_RPC" | grep -E "code:|txhash:" || true
  sleep 7
}

wait_for_health() {
  local container="$1"
  local max_wait="${2:-120}"
  echo -n "  waiting for $container"
  for i in $(seq 1 "$max_wait"); do
    local status
    status=$(docker inspect --format='{{.State.Health.Status}}' "$container" 2>/dev/null)
    if [ "$status" = "healthy" ]; then
      echo " OK (${i}s)"
      return 0
    fi
    echo -n "."
    sleep 1
  done
  echo " TIMEOUT after ${max_wait}s"
  return 1
}

set_node_keys() {
  local container="$1"
  local seed="$2"
  local rpc="$3"

  echo "  set-node-keys on $container"
  docker exec -e SIGNER_SEED_PHRASE="$seed" "$container" bash -c '
    NODE_PUB_KEY=$(echo "'"$PASSWORD"'" | thornode keys show thorchain --pubkey --keyring-backend '"$KEYRING"' | thornode pubkey)
    NODE_PUB_KEY_ED25519=$(thornode ed25519)
    VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
    printf "'"$PASSWORD"'\n'"$PASSWORD"'\n" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend '"$KEYRING"' --chain-id '"$CHAIN_ID"' --node '"$rpc"' --yes
  ' 2>&1 | grep -E "code:|txhash:" || true
}

set_ip() {
  local container="$1"
  local rpc="$2"

  local ip
  ip=$(docker inspect "$container" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
  echo "  set-ip-address $ip on $container"
  tx "$container" "set-ip-address $ip --node $rpc" | grep -E "code:|txhash:" || true
}

set_version() {
  local container="$1"
  local rpc="$2"

  echo "  set-version on $container"
  tx "$container" "set-version --node $rpc" | grep -E "code:|txhash:" || true
}

echo "============================================"
echo "XMR Chainnet Post-Deploy"
echo "============================================"

echo ""
echo "[1/6] Waiting for thornode health..."
wait_for_health docker-thornode-1 120

echo ""
echo "[2/6] Setting mimirs..."
mimir P2PGateDisabled 1
mimir MinimumNodesForBFT 2
mimir DesiredValidatorSet 3
mimir NumberOfNewNodesPerChurn 3
mimir ChurnInterval 10
mimir ChurnRetryInterval 10
mimir MinimumBondInRune 1
mimir HaltScannerXMR 0

echo ""
echo "[3/8] Waiting for dog bifrost to report XMR fee + chain tip (sole active node = 1 attestation quorum)..."
for i in $(seq 1 60); do
  fee_rate=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="XMR") | .observed_fee_rate' 2>/dev/null)
  last_observed=$(curl -s localhost:1317/thorchain/lastblock 2>/dev/null | jq -r '.[] | select(.chain=="XMR") | .last_observed_in' 2>/dev/null)
  if [ -n "$fee_rate" ] && [ "$fee_rate" != "0" ] && [ "$fee_rate" != "null" ] && [ -n "$last_observed" ] && [ "$last_observed" != "0" ] && [ "$last_observed" != "null" ]; then
    echo "  XMR fee=$fee_rate chain_tip=$last_observed OK (${i}x10s)"
    break
  fi
  echo -n "  [$i] fee=$fee_rate tip=$last_observed"
  echo " waiting..."
  sleep 10
done

echo ""
echo "[4/8] Waiting for cat/fox thornodes..."
wait_for_health docker-thornode-cat-1 120
wait_for_health docker-thornode-fox-1 120

echo ""
echo "[5/8] Setting node keys, IPs, versions for cat and fox..."
set_node_keys docker-thornode-cat-1 "$CAT_SEED" "$CAT_RPC"
set_ip docker-thornode-cat-1 "$CAT_RPC"
set_version docker-thornode-cat-1 "$CAT_RPC"
sleep 7
set_node_keys docker-thornode-fox-1 "$FOX_SEED" "$FOX_RPC"
set_ip docker-thornode-fox-1 "$FOX_RPC"
sleep 7
set_version docker-thornode-fox-1 "$FOX_RPC"

echo ""
echo "[6/8] Equalizing dog bond..."
echo "  bonding 70T RUNE to dog"
tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$DOG_NODE_ADDR --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true

echo ""
echo "[7/8] Monitoring for churn + keygen..."
echo "  Nodes should move: Whitelisted -> Standby -> Ready -> Active"
echo "  Keygen triggers once all 3 are Active (check every 30s, timeout 10min)"

for i in $(seq 1 20); do
  sleep 30
  statuses=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || echo "error")
  vault_members=$(curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq '.[0].membership | length' 2>/dev/null || echo 0)

  echo "  [${i}/20] nodes: $statuses | vault members: $vault_members"

  if [ "$vault_members" = "3" ]; then
    echo ""
    echo "============================================"
    echo "SUCCESS: 3-member vault created!"
    echo "============================================"

    echo ""
    echo "Halting churning..."
    mimir HaltChurning 1

    echo ""
    echo "Vault info:"
    curl -s localhost:1317/thorchain/vaults/asgard | jq '.[0] | {status, block_height, chains, membership_count: (.membership | length)}'

    echo ""
    echo "XMR inbound address:"
    curl -s localhost:1317/thorchain/inbound_addresses | jq '.[] | select(.chain=="XMR")'

    echo ""
    echo "REMINDER: Export keyshares now!"
    # shellcheck disable=SC2016
    echo '  mkdir -p ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)'
    echo "  ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-1:/home/serai/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/\$(date +%Y%m%d)/dog.tar.gz"
    echo "  ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-cat-1:/home/serai/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/\$(date +%Y%m%d)/cat.tar.gz"
    echo "  ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-fox-1:/home/serai/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/\$(date +%Y%m%d)/fox.tar.gz"
    exit 0
  fi
done

echo ""
echo "TIMEOUT: 3-member vault not created after 10 minutes."
echo "Check logs:"
echo "  docker logs docker-thornode-1 2>&1 | grep -i 'keygen\|churn\|rotation'"
echo "  docker logs docker-bifrost-1 2>&1 | grep -i 'keygen\|dkg'"
echo "  docker logs docker-xmr-frost-signer-1 2>&1 | grep -i 'dkg\|group_key'"
exit 1
