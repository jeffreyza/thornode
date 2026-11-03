#!/bin/bash

BLOCK_TIME=${BLOCK_TIME:=12}
# BOOTSTRAP_BLOCK_TIME controls how fast we mine during initial startup (sync + masternode
# registration + quorum formation). Once Dash is ready and the master account is funded,
# we switch the background miner loop to BLOCK_TIME for the actual simulation run.
BOOTSTRAP_BLOCK_TIME=${BOOTSTRAP_BLOCK_TIME:=1}
MASTER_ADDR="${DASH_MASTER_ADDR:=yTPDMMXarsNWKC9NDTfHREkTGNqSWE555Y}"

# Load shared Dash mocknet helpers from the mounted `/mocknet` volume so these entrypoint scripts
# and `core.sh` stay in sync and can be edited in this repo.
# Older versions sourced `/scripts/core.sh` (baked into the node-launcher image). That worked,
# but it was harder to change and could drift from the `/mocknet/*` scripts we mount at runtime.
. /mocknet/core.sh

initialBlocks="${initialBlocks:-1000}"
logPath="${logPath:-/home/dash/.dashcore/dashd.log}"
genesisAddress=""
minerPID=""

startminer() {
  interval="$1"
  echo "Starting background miner with block interval=${interval}s"
  while true; do
    dash-cli generatetoaddress 1 "$genesisAddress" &>/dev/null
    sleep "$interval"
  done &
  minerPID="$!"
}

stopminer() {
  if [[ -z ${minerPID} ]]; then
    return
  fi
  kill "${minerPID}" 2>/dev/null || true
  wait "${minerPID}" 2>/dev/null || true
  minerPID=""
}

verifyinstantsendandchainlocks() {
  interval="$1"
  while true; do
    echo "Verifying InstantSend and ChainLocks..."
    hash=$(dash-cli sendtoaddress "$genesisAddress" 10)
    sleep "$interval"
    rawTx=$(dash-cli getrawtransaction "$hash" true)
    instantLock=$(json_get_bool instantlock "$rawTx")
    chainLock=$(json_get_bool chainlock "$rawTx")

    if [[ $instantLock == "true" && $chainLock == "true" ]]; then
      echo "InstantSend OK"
      echo "ChainLock OK"
      break
    fi
  done
}

genesis() {
  printsignerconfig
  echo "BOOTSTRAP_BLOCK_TIME     $BOOTSTRAP_BLOCK_TIME"
  echo "BLOCK_TIME               $BLOCK_TIME"

  writedashdconfig

  dashd 1>"$logPath" &

  waitforverificationprogresscomplete "$(hostname)"

  echo "Creating default wallet"
  dash-cli createwallet "" >/dev/null

  # Import master address for monitoring.
  echo "Importing master address: $MASTER_ADDR"
  dash-cli importaddress "$MASTER_ADDR" "" false

  echo "Creating genesis address for quorum setup"
  genesisAddress=$(dash-cli getnewaddress "")
  echo "--> $genesisAddress"

  # Mine initial blocks to genesis address so we can spend from it for
  # masternode setup and subsequent test funding.
  echo "Generating $initialBlocks initial blocks to genesis address..."
  dash-cli generatetoaddress "$initialBlocks" "$genesisAddress" >/dev/null

  waitforblock "$(hostname)" "$initialBlocks"
  # Mine continuous blocks to genesis address, fast during bootstrap and then
  # later switch to the steady-state BLOCK_TIME interval.
  startminer "$BOOTSTRAP_BLOCK_TIME"

  waitforblock dash2 "$initialBlocks"
  waitformasternodestatus dash2 READY
  waitformasternodesync dash2

  waitforblock dash3 "$initialBlocks"
  waitformasternodestatus dash3 READY
  waitformasternodesync dash3

  waitforblock dash4 "$initialBlocks"
  waitformasternodestatus dash4 READY
  waitformasternodesync dash4

  waitforblock dash5 "$initialBlocks"
  waitformasternodestatus dash5 READY
  waitformasternodesync dash5

  waitforpeerconnections "$(hostname)" 4

  echo "Activating sporks..."
  dash-cli sporkupdate SPORK_2_INSTANTSEND_ENABLED 0
  dash-cli sporkupdate SPORK_3_INSTANTSEND_BLOCK_FILTERING 0
  dash-cli sporkupdate SPORK_9_SUPERBLOCKS_ENABLED 0
  dash-cli sporkupdate SPORK_17_QUORUM_DKG_ENABLED 0
  dash-cli sporkupdate SPORK_19_CHAINLOCKS_ENABLED 0

  while true; do
    dash-cli generatetoaddress 1 "$genesisAddress" &>/dev/null
    quorums=$(dash-cli quorum list 2>/dev/null)
    count=$(json_get_array_length llmq_test "$quorums")
    if [[ $count -ge "2" ]]; then
      break
    fi
    sleep 0.1
  done &

  waitforquorumwithname llmq_test
  printtimetostart

  waitBlocks=8
  waitTime=$((BOOTSTRAP_BLOCK_TIME * waitBlocks))
  echo "Waiting for $waitTime seconds ($waitBlocks blocks)"
  sleep "$waitTime"

  verifyinstantsendandchainlocks "$BOOTSTRAP_BLOCK_TIME"
  sleep 8

  # From this point on we want a longer mempool window for InstantSend testing.
  # Restart the background miner loop with the steady-state BLOCK_TIME.
  stopminer
  startminer "$BLOCK_TIME"

  # Fund master address for simulation tests.
  echo "Funding master address for simulation tests: $MASTER_ADDR"
  dash-cli sendtoaddress "$MASTER_ADDR" 50000
  dash-cli generatetoaddress 1 "$genesisAddress"

  # Check master address balance.
  sleep 2
  masterBalance=$(dash-cli getreceivedbyaddress "$MASTER_ADDR" 0 2>/dev/null || echo "0")
  echo "Master address balance: $masterBalance DASH"

  echo "Following dashd log from start:"
  tail -f -n +1 "$logPath"
}

genesis &
exitonsigterm
