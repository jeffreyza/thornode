#!/bin/sh

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${DASH_MASTER_ADDR:=yTPDMMXarsNWKC9NDTfHREkTGNqSWE555Y}"
BLOCK_TIME=${BLOCK_TIME:=1}
RPC_PORT=${RPC_PORT:=19898}

dashd -regtest -rpcport="$RPC_PORT" -txindex -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" -rpcallowip=0.0.0.0/0 -rpcbind=127.0.0.1 -rpcbind="$(hostname)" &

# give time to dashd to start
while true; do
  output=$(dash-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" createwallet "" 2>&1)
  case "$output" in
  *'"name"'* | *'already exists'*)
    break
    ;;
  esac
  sleep 2
done

# Import the master address as a watch-only address so generatetoaddress.
while true; do
  dash-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" importaddress "$MASTER_ADDR" "" false >/dev/null 2>&1 && break
  sleep 2
done

# Mine 200 blocks up front in a single call. Dash requires 100 confirmations
# for coinbase outputs to become spendable:
#   src/consensus/consensus.h COINBASE_MATURITY = 100
# 200 blocks yields ~100 matured UTXOs immediately for simtest master to spend.
dash-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" generatetoaddress 200 "$MASTER_ADDR" >/dev/null

sleep 15

# Mine one block every BLOCK_TIME seconds so confirmations progress.
while true; do
  dash-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" generatetoaddress 1 "$MASTER_ADDR"
  sleep "$BLOCK_TIME"
done
