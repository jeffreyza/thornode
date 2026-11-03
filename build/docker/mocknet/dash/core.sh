#!/bin/bash

NODE_IP=$(hostname -i | awk '{print $1}')
SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${DASH_MASTER_ADDR:=yTPDMMXarsNWKC9NDTfHREkTGNqSWE555Y}"

configPath="/home/dash/.dashcore/dash.conf"

# DIP-0024 is active only after the chain is sufficiently advanced.
# Start past that activation point so InstantSend and ChainLocks are available immediately.
export initialBlocks=1000
export logPath="/home/dash/.dashcore/dashd.log"

json_compact() {
  tr -d '\n\r'
}

json_get_string() {
  key="$1"
  printf '%s' "$2" | json_compact | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

json_get_number() {
  key="$1"
  printf '%s' "$2" | json_compact | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\\([-0-9][0-9]*\\(\\.[0-9][0-9]*\\)\\?\\).*/\\1/p"
}

json_get_bool() {
  key="$1"
  printf '%s' "$2" | json_compact | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\\(true\\|false\\).*/\\1/p"
}

json_get_array_length() {
  key="$1"
  compact=$(printf '%s' "$2" | json_compact)
  content=$(printf '%s' "$compact" | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\\[\\([^]]*\\)\\].*/\\1/p")
  if [[ -z $content ]]; then
    echo 0
    return
  fi
  commas=$(printf '%s' "$content" | tr -cd ',' | wc -c)
  echo $((commas + 1))
}

waitforverificationprogresscomplete() {
  echo "Waiting for node ($1) verification..."
  while true; do
    blockchainInfo=$(dash-cli -rpcconnect="$1" getblockchaininfo 2>/dev/null)
    verificationProgress=$(json_get_number verificationprogress "$blockchainInfo")
    if [[ -n $verificationProgress ]] && awk "BEGIN { exit !($verificationProgress >= 1) }"; then
      break
    fi
    sleep 1
  done
  echo "Dash node ready."
}

waitforpeerconnections() {
  echo "Waiting for node ($1) to establish $2 peer connections..."
  while true; do
    peers=$(dash-cli -rpcconnect="$1" getconnectioncount 2>/dev/null)
    if [[ $peers -ge $2 ]]; then
      break
    fi
    sleep 1
  done
  echo "Node $1 has $2 peers."
}

waitforblock() {
  echo "Waiting for node ($1) to reach block $2..."
  while true; do
    block=$(dash-cli -rpcconnect="$1" getblockcount 2>/dev/null)
    if [[ $block -ge $2 ]]; then
      break
    fi
    sleep 1
  done
  echo "Block $2 reached."
}

waitformasternodestatus() {
  echo "Waiting for masternode ($1) to reach $2 state..."
  while true; do
    sleep 1
    mnStatus=$(dash-cli -rpcconnect="$1" masternode status 2>/dev/null)
    if [[ $mnStatus == *"Method not found"* ]]; then
      continue
    fi
    if [[ $mnStatus == *"This is not a masternode"* ]]; then
      continue
    fi
    mnState=$(json_get_string state "$mnStatus")
    if [[ $mnState == "$2" ]]; then
      break
    fi
  done
  echo "Masternode ready."
}

waitformasternodesync() {
  echo "Waiting for masternode ($1) to reach MASTERNODE_SYNC_FINISHED state..."
  while true; do
    mnSync=$(dash-cli -rpcconnect="$1" mnsync status 2>/dev/null)
    mnSyncStatus=$(json_get_string AssetName "$mnSync")
    if [[ $mnSyncStatus == "MASTERNODE_SYNC_FINISHED" ]]; then
      break
    fi
    sleep 1
  done
  echo "Masternode ready."
}

waitforquorumwithname() {
  echo "Waiting for quorum '$1' to be established..."
  while true; do
    quorums=$(dash-cli quorum list 2>/dev/null)
    count=$(json_get_array_length "$1" "$quorums")
    if [[ $count -ge "2" ]]; then
      break
    fi
    sleep 1
  done
}

killpidandwait() {
  pid="$1"
  echo "Sending SIGTERM to process $pid..."
  kill "$pid"
  while [[ $(
    ps -p "$pid" >/dev/null
    echo "$?"
  ) == "0" ]]; do
    sleep 0.2
  done
  echo "Process $pid terminated."
}

printsignerconfig() {
  echo "
---------------- Signer Configuration ----------------
NODE_IP                  $NODE_IP
SIGNER_NAME              $SIGNER_NAME
SIGNER_PASSWD            $SIGNER_PASSWD
MASTER_ADDR              $MASTER_ADDR
"
}

writedashdconfig() {
  echo "Writing config file to: $configPath"
  tee "$configPath" >/dev/null <<EOF
regtest=1
[regtest]
  discover=0
  printtoconsole=1
  txindex=1
  debug=llmq
  rest=1
  server=1
  logips=1
  printpriority=1
  watchquorums=1
  allowprivatenet=1
  addressindex=1
  spentindex=1
  rpcuser=$SIGNER_NAME
  rpcpassword=$SIGNER_PASSWD
  rpcallowip=0.0.0.0/0
  bind=0.0.0.0:19899
  externalip=$NODE_IP
  rpcbind=0.0.0.0:19898
  rpcconnect=$NODE_IP:19898
  rpcport=19898
  sporkaddr=yUPxpYgEubT11whAthBorhnjiztcSJ35ze
  sporkkey=cUHWarE1SdgyVV5PBBq73sfD1fuXjDeXAAc2qjfUWZk9PHsyhPsQ
  zmqpubhashtx=tcp://0.0.0.0:28332
  zmqpubhashblock=tcp://0.0.0.0:28332
  zmqpubrawblock=tcp://0.0.0.0:28332
  zmqpubrawtx=tcp://0.0.0.0:28332
  zmqpubrawtxlock=tcp://0.0.0.0:28332
  llmqinstantsenddip0024=llmq_test_dip0024
  llmqchainlocks=llmq_test
EOF
}

exitonsigterm() {
  timeToExit=0
  trap "timeToExit=1" SIGINT SIGTERM
  while true; do
    sleep 1
    if [[ ${timeToExit} == 1 ]]; then
      echo "Caught sigint/sigterm, exiting."
      kill -9 0
      exit 0
    fi
  done
}

startTime="$(date +%s)"
printtimetostart() {
  duration="$(($(date +%s) - startTime))"
  echo "Finished setting up the LLMQ in ${duration} seconds"
}
