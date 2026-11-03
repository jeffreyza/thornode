#!/bin/sh

RPC_PORT=${RPC_PORT:-19898}
SIGNER_NAME=${SIGNER_NAME:-thorchain}
SIGNER_PASSWD=${SIGNER_PASSWD:-password}

BLOCK_COUNT=$(
  dash-cli \
    -regtest \
    -rpcport="$RPC_PORT" \
    -rpcuser="$SIGNER_NAME" \
    -rpcpassword="$SIGNER_PASSWD" \
    getblockcount 2>/dev/null
)

if [ -n "$BLOCK_COUNT" ] && [ "$BLOCK_COUNT" -ge 101 ]; then
  exit 0
fi

exit 1
