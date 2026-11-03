#!/bin/bash

set -o pipefail

wait_for_evm_rpc() {
  local chain="$1"
  local rpc="$2"
  local require_accounts="$3"

  echo "Waiting for $chain RPC: $rpc"
  python3 - "$chain" "$rpc" "$require_accounts" <<'PY'
import sys
import time

import requests

chain, rpc, require_accounts = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
deadline = time.time() + 60
last_err = "rpc not ready"
session = requests.Session()

while time.time() < deadline:
    try:
        response = session.post(
            rpc,
            json={"jsonrpc": "2.0", "method": "net_version", "params": [], "id": 1},
            timeout=3,
        )
        response.raise_for_status()
        data = response.json()
        if "result" not in data:
            raise RuntimeError(f"missing net_version result: {data}")

        if require_accounts:
            response = session.post(
                rpc,
                json={"jsonrpc": "2.0", "method": "eth_accounts", "params": [], "id": 1},
                timeout=3,
            )
            response.raise_for_status()
            data = response.json()
            accounts = data.get("result") or []
            if not accounts:
                raise RuntimeError("eth_accounts is empty")

        print(f"{chain} RPC ready", flush=True)
        sys.exit(0)
    except Exception as err:
        last_err = err
        time.sleep(1)

print(f"{chain} RPC not ready after 60s: {last_err}", file=sys.stderr, flush=True)
sys.exit(1)
PY
}

deploy_evm_contracts() {
  # Deploy each EVM chain's contracts in parallel (each subshell waits for its
  # own RPC then deploys, writing only the router address to a per-chain file).
  # The genesis.json edits are applied serially afterwards because they all
  # read/modify the same file. set -e is NOT in effect, so each background job's
  # exit status is checked explicitly (a bare `wait` would mask failures).
  local pids=() chains=()
  for CHAIN in ETH AVAX BASE POL; do
    RPC_VAR="${CHAIN}_HOST"
    RPC_URL="$(eval echo "\$${RPC_VAR}")"
    REQUIRE_ACCOUNTS=0
    if [[ $CHAIN == "ETH" || $CHAIN == "BASE" || $CHAIN == "BSC" ]]; then
      REQUIRE_ACCOUNTS=1
    fi

    (
      wait_for_evm_rpc "$CHAIN" "$RPC_URL" "$REQUIRE_ACCOUNTS" || exit 1

      # deploy contract and get address from output
      echo "Deploying $CHAIN contracts"
      if ! PYTHONUNBUFFERED=1 python3 scripts/evm/evm-tool.py --chain "$CHAIN" --rpc "$RPC_URL" --action deploy >/tmp/evm-tool-$CHAIN.log 2>&1; then
        cat /tmp/evm-tool-$CHAIN.log
        exit 1
      fi
      cat /tmp/evm-tool-$CHAIN.log
      CONTRACT=$(grep </tmp/evm-tool-$CHAIN.log "Router Contract Address" | awk '{print $NF}')
      if [ -z "$CONTRACT" ]; then
        echo "Failed to determine $CHAIN router contract address"
        exit 1
      fi
      echo "$CONTRACT" >"/tmp/evm-router-$CHAIN.addr"
    ) &
    pids+=("$!")
    chains+=("$CHAIN")
  done

  # wait for every deploy; fail loudly if any did
  local rc=0
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      echo "EVM contract deploy failed for ${chains[$i]}"
      rc=1
    fi
  done
  [ "$rc" -eq 0 ] || return 1

  # add contract addresses to genesis serially (shared genesis.json)
  for CHAIN in ETH AVAX BASE POL; do
    CONTRACT=$(cat "/tmp/evm-router-$CHAIN.addr" 2>/dev/null)
    if [ -z "$CONTRACT" ]; then
      echo "Failed to determine $CHAIN router contract address"
      return 1
    fi
    echo "$CHAIN Contract Address: $CONTRACT"
    jq --arg CHAIN "$CHAIN" --arg CONTRACT "$CONTRACT" \
      '.app_state.thorchain.chain_contracts += [{"chain": $CHAIN, "router": $CONTRACT}]' \
      ~/.thornode/config/genesis.json >/tmp/genesis-$CHAIN.json
    mv /tmp/genesis-$CHAIN.json ~/.thornode/config/genesis.json
  done
}

# Mocknet registration resumes from committed chain state after a restart. Each
# pass reads the signer account and node records at one height, skips a step
# that is already recorded, and otherwise signs with the account sequence from
# that height. A sequence can be used by only one transaction, so an earlier
# attempt that is still pending, or that committed before this client saw the
# result, can never execute alongside the new one.
MOCKNET_ATTEMPTS=10

# Store a peer API response in MOCKNET_RESPONSE and the height it was served at
# in MOCKNET_RESPONSE_HEIGHT. Temporary failures are retried a bounded number
# of times. A requested height must match the served height.
mocknet_api_get() {
  local path="$1" height="$2" url="http://${PEER_API:-$PEER}:1317$1" attempt response
  [ -n "$height" ] && url="$url?height=$height"
  for ((attempt = 1; attempt <= MOCKNET_ATTEMPTS; attempt++)); do
    if response=$(curl -sf --max-time 5 -w '\n%header{grpc-metadata-x-cosmos-block-height}' "$url"); then
      MOCKNET_RESPONSE=${response%$'\n'*}
      MOCKNET_RESPONSE_HEIGHT=${response##*$'\n'}
      if [[ $MOCKNET_RESPONSE_HEIGHT =~ ^[1-9][0-9]*$ ]] && [ "${height:-$MOCKNET_RESPONSE_HEIGHT}" = "$MOCKNET_RESPONSE_HEIGHT" ] &&
        jq -e . <<<"$MOCKNET_RESPONSE" >/dev/null 2>&1; then
        return 0
      fi
    fi
    echo "Mocknet query $path failed (attempt $attempt/$MOCKNET_ATTEMPTS)" >&2
    sleep 2
  done
  echo "Mocknet query $path failed after $MOCKNET_ATTEMPTS attempts" >&2
  return 1
}

# Read the signer account and the node records at the account's height. Only
# the keys step needs every node, to find keys registered to another node.
mocknet_registration_snapshot() {
  mocknet_api_get "/cosmos/auth/v1beta1/accounts/$NODE_ADDRESS" || return 1
  MOCKNET_HEIGHT=$MOCKNET_RESPONSE_HEIGHT
  local account
  if ! account=$(jq -er '.account | [.account_number, .sequence] |
    select(all(type == "string" and test("^[0-9]+$"))) | join(" ")' <<<"$MOCKNET_RESPONSE"); then
    echo "Mocknet account $NODE_ADDRESS has no valid account number and sequence" >&2
    return 1
  fi
  # init_chain leaves IFS set to a comma.
  IFS=" " read -r MOCKNET_ACCOUNT_NUMBER MOCKNET_SEQUENCE <<<"$account"
  mocknet_api_get "/thorchain/node/$NODE_ADDRESS" "$MOCKNET_HEIGHT" || return 1
  MOCKNET_NODE=$MOCKNET_RESPONSE
  MOCKNET_NODES="[]"
  if [ "$1" = keys ]; then
    mocknet_api_get /thorchain/nodes "$MOCKNET_HEIGHT" || return 1
    MOCKNET_NODES=$MOCKNET_RESPONSE
  fi
}

# Print "done" or "missing" for a registration step in the snapshot, or
# "conflict: <reason>" when committed state belongs to another setup.
mocknet_registration_status() {
  jq -nr --arg step "$1" --arg address "$NODE_ADDRESS" --arg secp256k1 "$NODE_PUB_KEY" \
    --arg ed25519 "$NODE_PUB_KEY_ED25519" --arg validator "$VALIDATOR" --arg ip "$NODE_IP_ADDRESS" \
    --arg version "$NODE_VERSION" --argjson node "$MOCKNET_NODE" --argjson nodes "$MOCKNET_NODES" '
    def node_keys: [.pub_key_set.secp256k1 // "", .pub_key_set.ed25519 // "", .validator_cons_pub_key // ""];
    def semver: split(".") | map(tonumber);
    ($node.node_operator_address // "") as $operator
    | if $operator != "" and $operator != $address then
        "conflict: node \($address) is operated by \($operator)"
      elif $step == "bond" then
        if $operator == $address and ($node.total_bond // "0") != "0" then "done" else "missing" end
      elif $step == "keys" then
        ($node | node_keys) as $current
        | if $current == [$secp256k1, $ed25519, $validator] then "done"
          elif $current != ["", "", ""] then
            "conflict: node \($address) already has keys \($current | join(" ")), expected \($secp256k1) \($ed25519) \($validator)"
          else
            [$nodes[] | select(.node_address != $address)
              | select(node_keys as $k | $k[2] == $validator or ($k[0:2] | any(. == $secp256k1 or . == $ed25519)))
              | .node_address] as $owners
            | if $owners == [] then "missing"
              else "conflict: node keys are already registered to \($owners | join(", "))" end
          end
      elif $step == "ip" then
        if ($node.ip_address // "") == $ip then "done" else "missing" end
      elif $step == "version" then
        if (($node.version // "0.0.0") | semver) >= ($version | semver) then "done" else "missing" end
      else error("unknown mocknet registration step \($step)") end'
}

# Sign with the snapshot sequence and wait for execution. Returns 0 after a
# successful commit, 1 after a permanent rejection, and 2 when the outcome is
# unknown or the sequence was already used, so committed state must be read
# again before deciding whether to retry.
mocknet_tx() {
  local node="tcp://${PEER}:${PORT_RPC:-26657}"
  local response code txhash poll
  if ! response=$(printf "%s\n" "$SIGNER_PASSWD" | timeout 30s thornode tx thorchain "$@" \
    --node "$node" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" \
    --offline --account-number "$MOCKNET_ACCOUNT_NUMBER" --sequence "$MOCKNET_SEQUENCE" \
    --yes --broadcast-mode sync --output json); then
    echo "Mocknet $1 broadcast failed" >&2
    return 2
  fi
  if ! code=$(jq -er '.code | numbers' <<<"$response"); then
    echo "Mocknet $1 returned an invalid broadcast response" >&2
    return 2
  fi

  if [ "$code" = "0" ]; then
    if ! txhash=$(jq -er '.txhash | strings | select(test("^[[:xdigit:]]{64}$"))' <<<"$response"); then
      echo "Mocknet $1 broadcast returned no valid transaction hash" >&2
      return 2
    fi
    for ((poll = 1; poll <= 30; poll++)); do
      if response=$(timeout 5s thornode query tx "$txhash" --node "$node" --output json 2>/dev/null) &&
        jq -e --arg hash "$txhash" '.txhash == $hash and ((.height | tonumber) > 0) and (.code | type == "number")' <<<"$response" >/dev/null 2>&1; then
        code=$(jq -r '.code' <<<"$response")
        MOCKNET_TX_HEIGHT=$(jq -r '.height' <<<"$response")
        break
      fi
      if [ "$poll" = 30 ]; then
        echo "Mocknet $1 transaction $txhash was not confirmed after 30 queries" >&2
        return 2
      fi
      sleep 2
    done
    if [ "$code" = "0" ]; then
      echo "Mocknet $1 transaction $txhash committed successfully"
      return 0
    fi
  fi

  if jq -e '.codespace == "sdk"' <<<"$response" >/dev/null; then
    local reason=""
    case "$code" in
    19) reason="this transaction is already in the mempool" ;;
    20) reason="the mempool is full" ;;
    32) reason="another transaction already used sequence $MOCKNET_SEQUENCE" ;;
    esac
    if [ -n "$reason" ]; then
      echo "Mocknet $1 was not executed: $reason (code $code)" >&2
      return 2
    fi
  fi
  echo "Mocknet $1 transaction rejected: $(jq -r '.raw_log' <<<"$response") (code $code)" >&2
  return 1
}

# Complete one registration step unless committed state already records it.
# Every transaction attempt is followed by another read of committed state.
mocknet_register() {
  local step="$1" attempt status committed_height=0
  shift
  for ((attempt = 1; ; attempt++)); do
    mocknet_registration_snapshot "$step" || return 1
    status=$(mocknet_registration_status "$step") || return 1
    case "$status" in
    done)
      echo "Mocknet $step registration is recorded at height $MOCKNET_HEIGHT"
      return 0
      ;;
    missing)
      if ((committed_height > 0 && MOCKNET_HEIGHT >= committed_height)); then
        echo "Mocknet $step transaction committed at height $committed_height, but the node record at height $MOCKNET_HEIGHT does not match the intended setup" >&2
        return 1
      fi
      ;;
    *)
      echo "Mocknet $step registration conflicts with committed state: ${status#conflict: }" >&2
      return 1
      ;;
    esac
    if ((attempt > MOCKNET_ATTEMPTS)); then
      echo "Mocknet $step registration was not recorded after $MOCKNET_ATTEMPTS attempts" >&2
      return 1
    fi
    if ((committed_height > 0)); then
      # The API has not caught up with the committed transaction yet.
      sleep 2
      continue
    fi
    mocknet_tx "$@"
    case $? in
    0) committed_height=$MOCKNET_TX_HEIGHT ;;
    1) return 1 ;;
    *) sleep 2 ;;
    esac
  done
}

# Publish a complete state with a same-directory rename. A failed write must
# never allow registration to proceed without its restart guard.
write_mocknet_registration_state() {
  local state_file="$1"
  local state="$2"
  local temporary_file
  mkdir -p "$(dirname "$state_file")" || return 1
  temporary_file=$(mktemp "${state_file}.XXXXXX") || return 1
  if ! printf '%s\n' "$state" >"$temporary_file" || ! mv -f "$temporary_file" "$state_file"; then
    rm -f "$temporary_file"
    echo "Failed to persist mocknet registration state: $state" >&2
    return 1
  fi
}

init_mocknet() {
  NODE_ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show "$SIGNER_NAME" -a --keyring-backend file)

  if [ "$PEER" = "none" ]; then
    echo "Missing PEER"
    exit 1
  fi

  # wait for peer
  until curl -s "$PEER:$PORT_RPC" 1>/dev/null 2>&1; do
    echo "Waiting for peer: $PEER:$PORT_RPC"
    sleep 3
  done

  NODE_PUB_KEY=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain --pubkey --keyring-backend=file | thornode pubkey)
  VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
  NODE_IP_ADDRESS=${EXTERNAL_IP:=$(curl -s http://whatismyip.akamai.com)}
  if [ -z "$NODE_PUB_KEY" ] || [ -z "$NODE_PUB_KEY_ED25519" ] || [ -z "$VALIDATOR" ] || [ -z "$NODE_IP_ADDRESS" ]; then
    echo "Failed to determine the node keys or IP address" >&2
    return 1
  fi
  if ! NODE_VERSION=$(thornode query thorchain version --output json | jq -er '.version | strings | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))'); then
    echo "Failed to determine the local THORNode version" >&2
    return 1
  fi

  mocknet_register bond deposit 100000000000000 RUNE "bond:$NODE_ADDRESS" || return 1
  mocknet_register keys set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" || return 1
  mocknet_register ip set-ip-address "$NODE_IP_ADDRESS" || return 1
  mocknet_register version set-version || return 1
}

# set external ip to localhost in mocknet
if [ "$NET" = "mocknet" ]; then
  EXTERNAL_IP="$(hostname -i)"
  export EXTERNAL_IP
fi
