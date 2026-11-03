#!/bin/bash

set -o pipefail

. "$(dirname "$0")/core.sh"

if [ "$NET" = "mocknet" ]; then
  echo "Loading unsafe init for mocknet..."
  . "$(dirname "$0")/core-unsafe.sh"
fi

resolve_signer_seed_phrase

########################################################################################
# Genesis Init
########################################################################################

genesis_init() {
  init_chain
  create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

  VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
  NODE_ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain -a --keyring-backend file)
  NODE_PUB_KEY=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain -p --keyring-backend file | thornode pubkey)
  NODE_PUB_KEY_ED25519=$(printf "%s\n" "$SIGNER_SEED_PHRASE" | thornode ed25519)
  VERSION=$(fetch_version)

  NODE_IP_ADDRESS=${EXTERNAL_IP:=$(curl -s http://whatismyip.akamai.com)}
  add_node_account "$NODE_ADDRESS" "$VALIDATOR" "$NODE_PUB_KEY" "$VERSION" "$NODE_ADDRESS" "$NODE_PUB_KEY_ED25519" "$NODE_IP_ADDRESS"

  # disable default bank transfer, and opt to use our own custom one
  disable_bank_send

  # for mocknet, add initial balances
  echo "Using NET $NET"
  if [ "$NET" = "mocknet" ]; then
    echo "Setting up accounts"

    # smoke test accounts
    add_account tthor1z63f3mzwv3g75az80xwmhrawdqcjpaekk0kd54 rune 5000000000000
    add_account tthor1wz78qmrkplrdhy37tw0tnvn0tkm5pqd6zdp257 rune 25000000000100
    add_account tthor18f55frcvknxvcpx2vvpfedvw4l8eutuhku0uj6 rune 25000000000100
    add_account tthor1xwusttz86hqfuk5z7amcgqsg7vp6g8zhsp5lu2 rune 5090000000000

    # local cluster accounts (2M RUNE)
    add_account tthor1uuds8pd92qnnq0udw0rpg0szpgcslc9p8lluej rune 200000000000000 # cat
    add_account tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m rune 200000000000000 # dog
    add_account tthor13wrmhnh2qe98rjse30pl7u6jxszjjwl4f6yycr rune 200000000000000 # fox
    add_account tthor1qk8c8sfrmfm0tkncs0zxeutc8v5mx3pjj07k4u rune 200000000000000 # pig
    add_account tthor1necf5u8h6eeazm4449pxwhrr5h46eg7hc6zuq5 rune 200000000000000 # frog

    # simulation master
    add_account tthor1f4l5dlqhaujgkxxqmug4stfvmvt58vx2tspx4g rune 100000000000000 # master

    # When the bootstrap validator uses a prefunded mnemonic such as "dog", avoid
    # appending a second bank/auth entry for the same address.
    add_account_if_missing "$NODE_ADDRESS" rune 100000000000

    # mint to reserve for mocknet
    reserve 22000000000000000

    set_bond_module # set bond module balance for invariant

    # deploy evm contracts (skip if EVM chains are disabled)
    if [ "${SKIP_EVM_DEPLOY:-false}" != "true" ]; then
      deploy_evm_contracts || exit 1
    fi
  fi

  if [ "$NET" = "stagenet" ]; then
    if [ -z ${FAUCET+x} ]; then
      echo "env variable 'FAUCET' is not defined: should be a sthor address"
      exit 1
    fi
    add_account "$FAUCET" rune 40000000000000000

    for i in 1 2 3 4 5; do
      eval "EXTRA=\$EXTRA_FUND_ACCOUNT_$i"
      if [ -n "$EXTRA" ]; then
        echo "Funding extra account: $EXTRA"
        add_account "$EXTRA" rune 200000000000000
      fi
    done

    reserve 5000000000000000

    set_bond_module # set bond module balance for invariant
  fi

  if [ "$NET" = "chainnet" ]; then
    if [ -z ${FAUCET+x} ]; then
      echo "env variable 'FAUCET' is not defined: should be a cthor address"
      exit 1
    fi
    # 400M RUNE to faucet + 50M to reserve = 450M of 500M MaxRuneSupply
    add_account "$FAUCET" rune 40000000000000000

    for i in 1 2 3 4 5; do
      eval "EXTRA=\$EXTRA_FUND_ACCOUNT_$i"
      if [ -n "$EXTRA" ]; then
        echo "Funding extra account: $EXTRA"
        add_account "$EXTRA" rune 200000000000000
      fi
    done

    reserve 5000000000000000

    set_bond_module # set bond module balance for invariant
  fi

  if [ "$NET" != "mocknet" ]; then
    add_account_if_missing "$NODE_ADDRESS" rune 100000000000
  fi

  if [ -n "${ETH_CONTRACT+x}" ]; then
    echo "ETH Contract Address: $ETH_CONTRACT"
    set_eth_contract "$ETH_CONTRACT"
  fi
  if [ -n "${AVAX_CONTRACT+x}" ]; then
    echo "AVAX Contract Address: $AVAX_CONTRACT"
    set_avax_contract "$AVAX_CONTRACT"
  fi
  if [ -n "${BSC_CONTRACT+x}" ]; then
    echo "BSC Contract Address: $BSC_CONTRACT"
    set_bsc_contract "$BSC_CONTRACT"
  fi
  if [ -n "${BASE_CONTRACT+x}" ]; then
    echo "BASE Contract Address: $BASE_CONTRACT"
    set_base_contract "$BASE_CONTRACT"
  fi

  echo "Genesis content"
  cat ~/.thornode/config/genesis.json
  thornode genesis validate --trace
}

########################################################################################
# Main
########################################################################################

# A failed deployment may leave both genesis.json and contracts on other chains.
# Persist the incomplete state across container restarts; neither starting from
# that genesis nor blindly deploying again is safe.
MOCKNET_GENESIS_INIT_MARKER="$HOME/.thornode/.mocknet-genesis-in-progress"
if [ "$NET" = "mocknet" ] && [ -e "$MOCKNET_GENESIS_INIT_MARKER" ]; then
  echo "Incomplete mocknet genesis initialization: $MOCKNET_GENESIS_INIT_MARKER" >&2
  echo "Refusing to start or redeploy. Inspect the startup logs and recreate the disposable mocknet volumes together." >&2
  exit 1
fi

# genesis on first init if we are the genesis node
if [ ! -f ~/.thornode/config/genesis.json ]; then
  if [ "$NET" = "mocknet" ]; then
    mkdir -p "$HOME/.thornode" || exit 1
    mkdir "$MOCKNET_GENESIS_INIT_MARKER" || exit 1
  fi
  genesis_init || exit 1
  if [ "$NET" = "mocknet" ]; then
    rmdir "$MOCKNET_GENESIS_INIT_MARKER" || exit 1
  fi
fi

# render tendermint and cosmos configuration files
thornode render-config

export SIGNER_NAME
export SIGNER_PASSWD
exec thornode start
