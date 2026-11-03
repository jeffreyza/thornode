#!/bin/bash

set -o pipefail

. "$(dirname "$0")/core.sh"

if [ "$NET" = "mocknet" ]; then
  echo "Loading unsafe init for mocknet..."
  . "$(dirname "$0")/core-unsafe.sh"
fi

resolve_signer_seed_phrase

PEER="${PEER:=none}"          # the hostname of a seed node set as tendermint persistent peer
PEER_API="${PEER_API:=$PEER}" # the hostname of a seed node API if different

# Inspect registration before init_chain can create a temporary genesis file.
# Existing initialized volumes without a marker retain their normal startup. An
# incomplete registration resumes from the state already committed on chain.
registration_state=""
registration_state_file="$HOME/.thornode/mocknet-registration.state"
if [ "$NET" = "mocknet" ] && [ -e "$registration_state_file" ]; then
  registration_state=$(cat "$registration_state_file") || exit 1
  case "$registration_state" in
  registered) ;;
  registering)
    echo "Resuming incomplete mocknet registration from committed chain state"
    ;;
  *)
    echo "Invalid mocknet registration state in $registration_state_file; inspect it before recovery." >&2
    exit 1
    ;;
  esac
fi

if { [ ! -f ~/.thornode/config/genesis.json ] && [ "$registration_state" != "registered" ]; } ||
  [ "$registration_state" = "registering" ]; then
  if [ "$NET" = "mocknet" ]; then
    write_mocknet_registration_state "$registration_state_file" registering || exit 1
    # A restart can leave the temporary genesis file that init_chain refuses to overwrite.
    rm -f ~/.thornode/config/genesis.json
  fi
  echo "Setting THORNode as Validator node"

  create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

  init_chain || exit 1
  rm -rf ~/.thornode/config/genesis.json # set in thornode render-config

  if [ "$NET" = "mocknet" ]; then
    init_mocknet || exit 1
    write_mocknet_registration_state "$registration_state_file" registered || exit 1
  else
    NODE_ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show "$SIGNER_NAME" -a --keyring-backend file)
    echo "Your THORNode address: $NODE_ADDRESS"

    if [ "${AUTO_BOND:-false}" = "true" ]; then
      until curl -s "$PEER:$PORT_RPC" 1>/dev/null 2>&1; do
        echo "Waiting for peer: $PEER:$PORT_RPC"
        sleep 3
      done

      printf "%s\n" "$SIGNER_PASSWD" | thornode tx thorchain deposit 100000000000000 RUNE "bond:$NODE_ADDRESS" \
        --node "tcp://$PEER:${PORT_RPC:-26657}" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" --yes
      sleep 8

      NODE_PUB_KEY=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain --pubkey --keyring-backend=file | thornode pubkey)
      NODE_PUB_KEY_ED25519=$(printf "%s\n" "$SIGNER_SEED_PHRASE" | thornode ed25519)
      VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)

      until printf "%s\n%s\n" "$SIGNER_PASSWD" "$SIGNER_PASSWD" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" \
        --node "tcp://$PEER:${PORT_RPC:-26657}" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" --yes; do
        sleep 5
      done
      sleep 4

      NODE_IP_ADDRESS=${EXTERNAL_IP:=$(hostname -i)}
      until printf "%s\n%s\n" "$SIGNER_PASSWD" "$SIGNER_PASSWD" | thornode tx thorchain set-ip-address "$NODE_IP_ADDRESS" \
        --node "tcp://$PEER:${PORT_RPC:-26657}" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" --yes; do
        sleep 5
      done
      sleep 4

      printf "%s\n%s\n" "$SIGNER_PASSWD" "$SIGNER_PASSWD" | thornode tx thorchain set-version \
        --node "tcp://$PEER:${PORT_RPC:-26657}" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" --yes
    else
      echo "Send your bond to that address"
    fi
  fi
fi

# render tendermint and cosmos configuration files
thornode render-config || exit 1

export SIGNER_NAME
export SIGNER_PASSWD
exec thornode start
