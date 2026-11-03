#!/bin/bash

. /mocknet/core.sh

initialBlocks="${initialBlocks:-1000}"
logPath="${logPath:-/home/dash/.dashcore/dashd.log}"
configPath="${configPath:-/home/dash/.dashcore/dash.conf}"

masternode() {
  printsignerconfig
  writedashdconfig

  waitforblock dash1 "$initialBlocks"

  dashd -addnode=dash1 1>"$logPath" &
  dashdPID="$!"

  waitforblock "$(hostname)" "$initialBlocks"

  echo "Creating default wallet"
  dash-cli createwallet "" >/dev/null

  echo "Requesting mining address from genesis node"
  genesisAddress=$(dash-cli -rpcconnect=dash1 getnewaddress "")
  if [[ $genesisAddress == "" ]]; then
    echo "Failed to request genesis node mining address"
    sleep infinity
  else
    echo "--> $genesisAddress"
  fi

  ownerAddress=$(dash-cli getnewaddress "")
  collateralAddress=$(dash-cli getnewaddress "")
  # Masternode registration values used by protx register_fund below.
  collateralVout=1
  ipPort="$NODE_IP:19899"
  votingAddress=$(dash-cli getnewaddress "")
  operatorBLS=$(dash-cli bls generate)
  operatorPrivKey=$(json_get_string secret "$operatorBLS")
  operatorPubKey=$(json_get_string public "$operatorBLS")
  operatorReward=0
  payoutAddress=$(dash-cli getnewaddress "")
  feeSourceAddress=$ownerAddress
  fundAddress=$(dash-cli getnewaddress "")

  echo "Sending 1001 DASH to masternode fund address $fundAddress"
  fundHash=$(dash-cli -rpcconnect=dash1 sendtoaddress "$fundAddress" 1001 2>&1)
  if [[ $fundHash == *"error code"* ]]; then
    echo "Fund transaction failed: $fundHash"
    sleep infinity
  else
    echo "Fund tx hash: $fundHash"
  fi

  echo "Sending 1000 DASH to master address $MASTER_ADDR"
  masterHash=$(dash-cli -rpcconnect=dash1 sendtoaddress "$MASTER_ADDR" 1000 2>&1)
  if [[ $masterHash == *"error code"* ]]; then
    echo "Master fund transaction failed: $masterHash"
    sleep infinity
  else
    echo "Master fund tx hash: $masterHash"
  fi

  echo "Sending 1001 DASH to masternode collateral address $collateralAddress"
  collateralHash=$(dash-cli -rpcconnect=dash1 sendtoaddress "$collateralAddress" 1001 2>&1)
  if [[ $collateralHash == *"error code"* ]]; then
    echo "Collateral transaction failed: $collateralHash"
    sleep infinity
  else
    echo "Collateral tx hash: $collateralHash"
  fi

  echo "Generating confirmation blocks..."
  dash-cli -rpcconnect=dash1 generatetoaddress 20 "$genesisAddress"

  echo "
--------------- Masternode Configuration ---------------
collateralAddress        $collateralAddress
collateralHash           $collateralHash
collateralVout           $collateralVout
ipPort                   $ipPort
ownerAddress             $ownerAddress
operatorPrivKey          $operatorPrivKey
operatorPubKey           $operatorPubKey
votingAddress            $votingAddress
operatorReward           $operatorReward
payoutAddress            $payoutAddress
feeSourceAddress         $feeSourceAddress
fundAddress              $fundAddress
"

  echo "Sending protx register command"
  registerHash=$(dash-cli protx register_fund \
    "$collateralAddress" \
    "$ipPort" \
    "$ownerAddress" \
    "$operatorPubKey" \
    "$votingAddress" \
    "$operatorReward" \
    "$payoutAddress" \
    "$fundAddress")

  echo "Protx registration tx hash: $registerHash"

  if [[ $registerHash == "" ]]; then
    echo "Protx register failed."
    sleep infinity
  fi

  while true; do
    output=$(dash-cli -rpcconnect=dash1 getrawtransaction "$registerHash" true 2>&1)
    if [[ $output == *"No such mempool or blockchain transaction."* || $output == *"error code"* ]]; then
      echo "Protx not accepted by genesis node, re-sending..."
      rawTx=$(dash-cli getrawtransaction "$registerHash" 2>&1)
      dash-cli sendrawtransaction "$rawTx" &>/dev/null
      sleep 1
    else
      echo "Protx accepted by genesis node!"
      break
    fi
  done

  echo "Setting masternode operator bls key in dash.conf"
  echo "  masternodeblsprivkey=$operatorPrivKey" >>"$configPath"

  echo "Restarting dashd to initiate masternode sync"
  killpidandwait "$dashdPID"
  sleep 2
  dashd -connect=dash1 1>"$logPath" &
  dashdPID="$!"

  waitforverificationprogresscomplete "$(hostname)"
  waitformasternodestatus "$(hostname)" READY
  dash-cli mnsync next
  dash-cli mnsync next
  waitformasternodesync "$(hostname)"

  echo "Adding other masternode peers..."
  host="$(hostname)"
  # Start with the existing dash1 connection, then add every other masternode
  # except self and wait until all expected peer links are visible.
  expectedConnections=1
  for peer in dash2 dash3 dash4 dash5; do
    [[ $host == "$peer" ]] && continue
    dash-cli addnode "$peer" add
    expectedConnections=$((expectedConnections + 1))
  done
  waitforpeerconnections "$host" "$expectedConnections"

  waitforquorumwithname llmq_test
  printtimetostart

  echo "Following dashd log from start:"
  tail -f -n +1 "$logPath"
}

masternode &
exitonsigterm
