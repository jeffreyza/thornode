#!/usr/bin/env bash

# NOTE: This script should only be entered by the Makefile target.

set -euo pipefail

# Check if the chain argument is provided
if [ -z "${1-}" ]; then
  echo "Usage: $0 <chain>"
  exit 1
fi

# set chain rpc to local hardhat
UPPER_CHAIN=$(echo "$1" | tr '[:lower:]' '[:upper:]')

# get chain rpc
case $1 in
"avax")
  CHAIN_RPC="https://rpc.ankr.com/avalanche"
  export "${UPPER_CHAIN}_HOST"=http://host.docker.internal:5458/ext/bc/C/rpc
  ;;
"base")
  CHAIN_RPC="https://rpc.ankr.com/base"
  export "${UPPER_CHAIN}_HOST"=http://host.docker.internal:5458
  ;;
"bsc")
  CHAIN_RPC="https://rpc.ankr.com/bsc"
  export "${UPPER_CHAIN}_HOST"=http://host.docker.internal:5458
  ;;
"eth")
  CHAIN_RPC="https://rpc.ankr.com/eth"
  export "${UPPER_CHAIN}_HOST"=http://host.docker.internal:5458
  ;;
*)
  echo "Unsupported chain: $1"
  exit 1
  ;;
esac

# start mocknet
docker compose -f build/docker/docker-compose.yml --profile mocknet --profile midgard up -d

set -m

# start hardhat in background
cd tools/evm || {
  echo "Directory tools/evm not found."
  exit 1
}
npm install || {
  echo "Failed to install npm dependencies."
  exit 1
}

npx hardhat node --fork "$CHAIN_RPC" --hostname 0.0.0.0 --port 5458 &
HARDHAT_PID=$!

# log the PID
echo "Hardhat PID: $HARDHAT_PID"

# Wait for the Hardhat process
wait $HARDHAT_PID

# Bootstrap USDC balance
node init.js || {
  echo "Failed to run init.js."
  kill $HARDHAT_PID
  exit 1
}
