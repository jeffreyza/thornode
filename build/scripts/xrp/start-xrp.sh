#!/bin/bash

set -euo pipefail

RIPPLED_BIN=/opt/ripple/bin/rippled
RIPPLED_CONF=/docker/scripts/xrp/rippled.cfg
GENESIS_SECRET=snoPBrXtMeMyMHUVTgbuqAfg1SUTb
SIM_MASTER_ACCOUNT=r3hmW9oETcVqFWdN8jpCXgeK9km4uqNv5N
MASTER_FUND_PAYMENT='{"TransactionType": "Payment", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "Destination": "r3hmW9oETcVqFWdN8jpCXgeK9km4uqNv5N", "Amount": "100000000000000", "NetworkID": 1234}'

wait_for_rippled() {
  until "${RIPPLED_BIN}" server_state >/dev/null 2>&1; do
    sleep 1
  done
}

master_account_ready() {
  "${RIPPLED_BIN}" account_info "${SIM_MASTER_ACCOUNT}" validated 2>/dev/null |
    grep -q "\"Account\" : \"${SIM_MASTER_ACCOUNT}\""
}

# Start xrpl and wait for the local RPC to come up before the bootstrap funding step.
"${RIPPLED_BIN}" --conf "${RIPPLED_CONF}" --standalone &
wait_for_rippled

fund_submitted=false
while ! master_account_ready; do
  if [ "${fund_submitted}" = false ]; then
    submit_output="$("${RIPPLED_BIN}" submit "${GENESIS_SECRET}" "${MASTER_FUND_PAYMENT}" 2>&1 || true)"
    case "${submit_output}" in
    *'"engine_result" : "tesSUCCESS"'* | *'"engine_result" : "tefPAST_SEQ"'*)
      fund_submitted=true
      ;;
    esac
  fi

  "${RIPPLED_BIN}" ledger_accept >/dev/null 2>&1 || true
  sleep 1
done

while true; do
  "${RIPPLED_BIN}" ledger_accept >/dev/null 2>&1 || true
  sleep 2
done
