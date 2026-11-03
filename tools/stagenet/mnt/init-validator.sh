#!/bin/sh

# set version
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-version --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes

# set node keys
NODE_PUB_KEY=$(echo "password" | thornode keys show thorchain --pubkey --keyring-backend file | thornode pubkey)
NODE_PUB_KEY_ED25519=$(echo "password" | thornode ed25519)
VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes

# set node ip
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-ip-address "$(hostname -i)" --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes
