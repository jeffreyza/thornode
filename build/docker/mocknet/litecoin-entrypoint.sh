#!/bin/sh

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${BTC_MASTER_ADDR:=rltc1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawf4nr3r}"
BLOCK_TIME=${BLOCK_TIME:=1}
RPC_PORT=${RPC_PORT:=18443}

litecoind -regtest -rpcport="$RPC_PORT" -txindex -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" -rpcallowip=0.0.0.0/0 -rpcbind=127.0.0.1 -rpcbind="$(hostname)" -vbparams=mweb:9999999999:9999999999 &

# give time to litecoind to start
# Allow more time for the initial 100 blocks than for normal single-block mining.
while true; do
  litecoin-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" -rpcclienttimeout=60 generatetoaddress 100 "$MASTER_ADDR" && break
  sleep 5
done

# wait a bit while mocknet starts
sleep 15

# mine a new block every BLOCK_TIME
# The CLI defaults to a 900s HTTP timeout, longer than simulation actor deadlines.
# Bound each attempt so a stalled response does not block the mining loop. A
# timeout does not cancel server-side work; keep client attempts paced and sequential.
while true; do
  if ! litecoin-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" -rpcclienttimeout=10 generatetoaddress 1 "$MASTER_ADDR"; then
    echo "Litecoin mocknet mining RPC failed; retrying in ${BLOCK_TIME}s" >&2
  fi
  sleep "$BLOCK_TIME"
done
