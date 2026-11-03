#!/bin/bash

set -o pipefail
format_1e8() {
  printf "%.2f\n" "$(jq -n "$1"/100000000 2>/dev/null)" 2>/dev/null | sed ':a;s/\B[0-9]\{3\}\>/,&/;ta'
}

format_int() {
  printf "%.0f\n" "$1" 2>/dev/null | sed ':a;s/\B[0-9]\{3\}\>/,&/;ta'
}

# A height is usable only if it is a plain non-negative integer. There are two
# ways for one to be missing and they look nothing alike: an unreachable
# endpoint yields an empty body, while a JSON-RPC error — which is served with
# HTTP 200, so curl --fail passes it straight through — yields a body with no
# .result, from which jq prints the literal string "null". Both used to reach
# the comparisons below, where "" = "" and "null" = "null" each compare equal
# and render a confident "100.000%" for a chain that answered nothing at all.
is_height() {
  case "$1" in
  '' | null) return 1 ;;
  *[!0-9]*) return 1 ;;
  esac
}

# json_height <json> [filter] — a height from a response, or nothing when the
# response is missing or carries no such field. EVM heights arrive as hex
# strings, which printf converts; feeding it "null" would print a confident 0.
# So would any other non-numeric value: printf rejects it but still writes 0,
# so its status is checked and the result must itself be a height.
json_height() {
  local raw height
  raw=$(printf '%s' "$1" | jq -r "${2:-.result} // empty" 2>/dev/null)
  [ -z "$raw" ] && return
  height=$(printf "%.0f" "$raw" 2>/dev/null) || return
  is_height "$height" && printf '%s' "$height"
}

calc_progress() {
  if ! is_height "$1" || ! is_height "$2" || [ "$2" = "0" ]; then
    echo "Error"
  elif [ "$1" = "$2" ]; then
    echo "100.000%"
  elif [ -n "$3" ]; then
    progress="$(echo "$3 100" | awk '{printf "%.6f", $1 * $2}' 2>/dev/null)" && printf "%.3f%%" "$progress" || echo "Error"
  else
    progress="$(echo "$1 $2" | awk '{printf "%.6f", ($1/$2) * 100}' 2>/dev/null)" && printf "%.3f%%" "$progress" || echo "Error"
  fi
}

API=http://thornode:1317
THORNODE_PORT="${THORNODE_SERVICE_PORT_RPC:-27147}"
RPC=http://thornode:${THORNODE_PORT}

BITCOIN_ENDPOINT="${BIFROST_CHAINS_BTC_RPC_HOST:-${BTC_HOST:-bitcoin-daemon:${BITCOIN_DAEMON_SERVICE_PORT_RPC:-8332}}}"
LITECOIN_ENDPOINT="${BIFROST_CHAINS_LTC_RPC_HOST:-${LTC_HOST:-litecoin-daemon:${LITECOIN_DAEMON_SERVICE_PORT_RPC:-9332}}}"
BITCOIN_CASH_ENDPOINT="${BIFROST_CHAINS_BCH_RPC_HOST:-${BCH_HOST:-bitcoin-cash-daemon:${BITCOIN_CASH_DAEMON_SERVICE_PORT_RPC:-8332}}}"
DOGECOIN_ENDPOINT="${BIFROST_CHAINS_DOGE_RPC_HOST:-${DOGE_HOST:-dogecoin-daemon:${DOGECOIN_DAEMON_SERVICE_PORT_RPC:-22555}}}"
ETHEREUM_ENDPOINT="${BIFROST_CHAINS_ETH_RPC_HOST:-${ETH_HOST:-http://ethereum-daemon:${ETHEREUM_DAEMON_SERVICE_PORT_RPC:-8545}}}"
# The beacon API conventionally sits beside the execution client on port 3500.
# That holds for the in-cluster daemon and usually for a self-hosted node, but
# a provider URL says nothing about where its beacon API lives: rewriting its
# port drops the path and always failed. So only a path-less endpoint is
# rewritten, and outside the cluster the result is a guess — one that does not
# answer means no co-located beacon, not a broken one, and the row reads "-".
# Set ETH_BEACON_HOST to name a beacon node explicitly.
ETHEREUM_BEACON_GUESSED=false
if [ -n "$ETH_BEACON_HOST" ]; then
  ETHEREUM_BEACON_ENDPOINT="$ETH_BEACON_HOST"
elif [[ $ETHEREUM_ENDPOINT =~ ^([a-zA-Z][a-zA-Z0-9+.-]*://[^/:]+)(:[0-9]+)?/?$ ]]; then
  ETHEREUM_BEACON_ENDPOINT="${BASH_REMATCH[1]}:3500"
  [[ $ETHEREUM_ENDPOINT == *"ethereum-daemon"* ]] || ETHEREUM_BEACON_GUESSED=true
else
  ETHEREUM_BEACON_ENDPOINT=""
fi
BINANCE_SMART_ENDPOINT="${BIFROST_CHAINS_BSC_RPC_HOST:-http://binance-smart-daemon:${BINANCE_SMART_DAEMON_SERVICE_PORT_RPC:-8545}}"
BASE_ENDPOINT="${BIFROST_CHAINS_BASE_RPC_HOST:-http://base-daemon:${BASE_DAEMON_SERVICE_PORT_RPC:-8545}}"
GAIA_ENDPOINT="${BIFROST_CHAINS_GAIA_RPC_HOST:-${GAIA_HOST:-http://gaia-daemon:26657}}"
AVALANCHE_ENDPOINT="${BIFROST_CHAINS_AVAX_RPC_HOST:-${AVAX_HOST:-http://avalanche-daemon:9650/ext/bc/C/rpc}}"
SOLANA_ENDPOINT="${BIFROST_CHAINS_SOL_RPC_HOST:-${SOL_HOST:-http://solana-daemon:8899}}"
XRP_ENDPOINT="${BIFROST_CHAINS_XRP_RPC_HOST:-http://xrp-daemon:${XRP_DAEMON_SERVICE_PORT_RPC:-51234}}"
TRON_ENDPOINT="${BIFROST_CHAINS_TRON_API_HOST:-http://tron-daemon:16667}"

# Bifrost's own view of each chain, for the BIFROST columns: how far its block
# scanner has read, and how far it trails the chain tip it observes. Independent
# of the daemon's pair, which compares the configured endpoint against a public
# reference — a daemon sitting exactly at tip can still have a scanner that is
# stalled or catching up, and that lag is what accrues observation slash points.
# Served by the bifrost pod, so only a validator asks: a fullnode has no bifrost,
# and prints no row that has a scanner.
BIFROST=http://bifrost:${BIFROST_SERVICE_PORT_HTTP:-6040}
# The handler calls GetHeight() on every configured chain and waits for all of
# them, so this request is as slow as the slowest RPC endpoint; when a stalled
# provider makes it exceed the timeout, the cells below say so rather than
# rendering as an absent bifrost. Fetched with the rest, read after the wait.

# scanner_status <chain key> — sets SCANNER_SYNC and SCANNER_BEHIND for the row,
# bifrost's counterparts to the daemon's pair. Sets globals rather than echoing
# because it also flags the unhealthy footnote, and a command substitution would
# run it in a subshell and discard that.
scanner_status() {
  local key="$1" fields diff scanned tip healthy
  SCANNER_SYNC="-"
  SCANNER_BEHIND="-"
  # No key: THOR and the beacon row. Bifrost reports no block scanner height for
  # THORChain (hardcoded -1, pending upstream), so "-" is the honest cell — and
  # stays "-" even when bifrost itself did not answer.
  [ -z "$key" ] && return
  # A validator always runs bifrost, so no usable answer is an Error, never "-".
  if [ -z "$SCANNER_JSON" ]; then
    SCANNER_SYNC="Error"
    return
  fi
  # All three fields in one pass. A jq spawn costs around 20ms in-container, and
  # reading them separately paid that four times for every chain in the table.
  # Not ".healthy // empty": jq's alternative operator treats false as absent,
  # which would discard the only value this check exists to catch. @tsv renders
  # an absent healthy (THOR) as an empty field, which likewise is not "false".
  fields=$(echo "$SCANNER_JSON" | jq -r --arg c "$key" \
    '(.[$c] // empty) | [.block_scanner_height, .chain_height, .healthy] | @tsv' 2>/dev/null)
  if [ -z "$fields" ]; then
    # Bifrost holds no client for this chain: it failed to construct at startup
    # (unreachable endpoint) and the chain is not being observed at all, which
    # the chain's own endpoint being reachable now would otherwise mask.
    SCANNER_SYNC="missing"
    return
  fi
  IFS=$'\t' read -r scanned tip healthy <<<"$fields"
  # Bifrost reports -1 for a height it could not read. Tested on the heights
  # rather than on scanner_height_diff, whose -1 sentinel is ambiguous: a
  # scanner one block ahead of the tip — ordinary skew when the endpoint is
  # load-balanced — legitimately reports a diff of -1.
  if ! is_height "$scanned" || ! is_height "$tip"; then
    SCANNER_SYNC="Error"
    return
  fi
  # How far through the chain the scanner has read, against the tip bifrost sees
  # — the daemon's SYNC measures a different pair of heights entirely. BEHIND is
  # computed from the same two heights in the daemon column's convention,
  # negative when behind, rather than negating bifrost's reported diff.
  SCANNER_SYNC=$(calc_progress "$scanned" "$tip")
  diff=$((scanned - tip))
  if [ "$healthy" = "false" ]; then
    # Scanner is not progressing: chain halted by mimir, or fetches failing.
    SCANNER_BEHIND="$(format_int "$diff")*"
    SCANNER_UNHEALTHY=true
  else
    SCANNER_BEHIND=$(format_int "$diff")
  fi
}

# The BIFROST_CHAINS_*_RPC_HOST endpoints are full URLs, while the in-cluster
# daemon defaults are bare host:port needing the chart's credentials. Prefixing
# "http://thorchain:password@" onto a URL that already carries a scheme yields a
# malformed request that returns nothing — which calc_progress used to render as
# a healthy "100.000%".
utxo_url() {
  case "$1" in
  http://* | https://*) echo "$1" ;;
  *) echo "http://thorchain:password@$1" ;;
  esac
}

# Every probe below is an independent round trip, and against endpoints that
# are not in-cluster the script spent essentially all of its time waiting: some
# thirty requests issued one at a time. Fetch them concurrently into a scratch
# directory and parse the cached bodies, so the total wait is the slowest
# single request rather than the sum of all of them.
RESP=$(mktemp -d)
trap 'rm -rf "$RESP"' EXIT

# fetch <name> <curl args...> — runs in the background; the response body, or
# nothing if the request failed, lands in $RESP/<name>.
fetch() {
  local name=$1
  shift
  curl -sL --fail -m 10 "$@" >"$RESP/$name" 2>/dev/null &
}

# body <name> — the cached response for a fetch, empty if it failed.
body() { cat "$RESP/$1" 2>/dev/null; }

RPC_JSON='content-type: application/json'
UTXO_INFO='{"jsonrpc": "1.0", "id": "node-status", "method": "getblockchaininfo", "params": []}'
EVM_SYNCING='{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}'
EVM_HEIGHT='{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
XRP_LEDGER='{"jsonrpc":"2.0","method":"ledger","params":[{"ledger_index":"validated"}],"id":1}'
SOL_SLOT='{"jsonrpc":"2.0","method":"getSlot","params":[{"commitment":"finalized"}],"id":1}'

ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show "$SIGNER_NAME" -a --keyring-backend file)

fetch node "$API/thorchain/node/$ADDRESS"
fetch thor_local "thornode:$THORNODE_PORT/status"
fetch mimir "$API/thorchain/mimir"
fetch constants "$API/thorchain/constants"
fetch vaults "$API/thorchain/vaults/asgard"

# The reference for THOR's tip: an explicit peer, else the first seed to
# answer, else the network's public RPC. Resolved in the background because the
# seed list is tried in order and must stay sequential.
(
  if [ -n "$PEER" ]; then
    curl -sL --fail -m 10 "$PEER:$THORNODE_PORT/status"
  elif [ -n "$SEEDS" ]; then
    OLD_IFS=$IFS
    IFS=","
    # Each attempt is held until it proves to carry a height: written straight
    # to the output, a seed that timed out partway through its body would leave
    # those bytes in front of the next seed's answer and make both unparsable,
    # and a seed answering 200 with an error page would end the search.
    for SEED in $SEEDS; do
      seed_status=$(curl -sL --fail -m 10 "$SEED:$THORNODE_PORT/status") || continue
      if [ -n "$(json_height "$seed_status" .result.sync_info.latest_block_height)" ]; then
        printf '%s' "$seed_status"
        break
      fi
    done
    IFS=$OLD_IFS
  elif [ "$NET" = "mainnet" ]; then
    curl -sL --fail -m 10 https://gateway.liquify.com/chain/thorchain_rpc/status
  elif [ "$NET" = "stagenet" ]; then
    curl -sL --fail -m 10 https://stagenet-rpc.ninerealms.com/status
  elif [ "$NET" = "chainnet" ]; then
    curl -sL --fail -m 10 https://chainnet-rpc.thorchain.network/status
  fi
) >"$RESP/thor_ref" 2>/dev/null &

if [ "$VALIDATOR" = "true" ]; then
  fetch scanner "$BIFROST/status/scanner"

  # Each chain is probed under the same condition that prints its row below: a
  # disabled chain's requests, some of them to public reference endpoints,
  # would otherwise be spent on a row that is never shown.
  if [ "$BIFROST_CHAINS_BTC_DISABLED" != "true" ]; then
    fetch btc --data-binary "$UTXO_INFO" -H 'content-type: text/plain;' "$(utxo_url "$BITCOIN_ENDPOINT")"
  fi
  if [ "$BIFROST_CHAINS_LTC_DISABLED" != "true" ]; then
    fetch ltc --data-binary "$UTXO_INFO" -H 'content-type: text/plain;' "$(utxo_url "$LITECOIN_ENDPOINT")"
  fi
  if [ "$BIFROST_CHAINS_BCH_DISABLED" != "true" ]; then
    fetch bch --data-binary "$UTXO_INFO" -H 'content-type: text/plain;' "$(utxo_url "$BITCOIN_CASH_ENDPOINT")"
  fi
  if [ "$BIFROST_CHAINS_DOGE_DISABLED" != "true" ]; then
    fetch doge --data-binary "$UTXO_INFO" -H 'content-type: text/plain;' "$(utxo_url "$DOGECOIN_ENDPOINT")"
  fi

  if [ "$BIFROST_CHAINS_ETH_DISABLED" != "true" ]; then
    # Both ETH requests are issued upfront: which one answers the question is
    # not known until eth_syncing comes back, and waiting to find out would put
    # a second serial round trip back into the critical path.
    fetch eth_syncing -X POST --data "$EVM_SYNCING" -H "$RPC_JSON" "$ETHEREUM_ENDPOINT"
    fetch eth_height -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" "$ETHEREUM_ENDPOINT"
    # A guessed beacon gets a short connect timeout: a provider that filters
    # port 3500 would otherwise hold the whole table for the full ten seconds.
    if [ "$ETHEREUM_BEACON_GUESSED" = "true" ]; then
      fetch beacon --connect-timeout 3 "$ETHEREUM_BEACON_ENDPOINT/eth/v1/node/syncing"
    elif [ -n "$ETHEREUM_BEACON_ENDPOINT" ]; then
      fetch beacon "$ETHEREUM_BEACON_ENDPOINT/eth/v1/node/syncing"
    fi
  fi

  if [ "$BIFROST_CHAINS_GAIA_DISABLED" != "true" ]; then
    fetch gaia_ref https://cosmos-rpc.polkachu.com/status
    fetch gaia "$GAIA_ENDPOINT/status"
  fi
  if [ "$BIFROST_CHAINS_AVAX_DISABLED" != "true" ]; then
    fetch avax_ref -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" https://avalanche-c-chain-rpc.publicnode.com
    fetch avax -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" "$AVALANCHE_ENDPOINT"
  fi
  if [ "$BIFROST_CHAINS_BSC_DISABLED" != "true" ]; then
    fetch bsc_ref -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" https://bsc.nodereal.io
    fetch bsc -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" "$BINANCE_SMART_ENDPOINT"
  fi
  if [ "$BIFROST_CHAINS_BASE_DISABLED" != "true" ]; then
    fetch base_ref -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" https://mainnet.base.org
    fetch base -X POST --data "$EVM_HEIGHT" -H "$RPC_JSON" "$BASE_ENDPOINT"
  fi

  if [ "$BIFROST_CHAINS_XRP_DISABLED" = "false" ]; then
    fetch xrp_ref -X POST --data "$XRP_LEDGER" -H "$RPC_JSON" https://s1.ripple.com:51234
    fetch xrp -X POST --data "$XRP_LEDGER" -H "$RPC_JSON" "$XRP_ENDPOINT"
  fi
  if [ "$BIFROST_CHAINS_TRON_DISABLED" = "false" ]; then
    fetch tron_ref https://api.trongrid.io/wallet/getnowblock
    fetch tron "$TRON_ENDPOINT/wallet/getnowblock"
  fi
  if [ "$BIFROST_CHAINS_SOL_DISABLED" = "false" ]; then
    fetch sol_ref -X POST --data "$SOL_SLOT" -H "$RPC_JSON" https://api.mainnet-beta.solana.com
    fetch sol -X POST --data "$SOL_SLOT" -H "$RPC_JSON" "$SOLANA_ENDPOINT"
  fi
fi

wait

SCANNER_JSON=$(body scanner)
# A body that is not a JSON object — an error page, or one cut off by the
# timeout — has no chain in it, and read as one every row would say "missing",
# blaming bifrost's clients for what is a failed request. It is no answer.
if ! printf '%s' "$SCANNER_JSON" | jq -e 'type == "object"' >/dev/null 2>&1; then
  SCANNER_JSON=""
fi

JSON=$(body node)

IP=$(echo "$JSON" | jq -r ".ip_address")
VERSION=$(echo "$JSON" | jq -r ".version")
BOND=$(echo "$JSON" | jq -r ".total_bond")
REWARDS=$(echo "$JSON" | jq -r ".current_award")
SLASH=$(echo "$JSON" | jq -r ".slash_points")
STATUS=$(echo "$JSON" | jq -r ".status")
PREFLIGHT=$(echo "$JSON" | jq -r ".preflight_status")
PUB_KEY=$(echo "$JSON" | jq -r ".pub_key_set.secp256k1")
[ "$VALIDATOR" = "false" ] && IP=$EXTERNAL_IP

if [ "$VALIDATOR" = "true" ]; then
  # calculate BTC chain sync progress
  BTC_RESULT=$(body btc)
  BTC_HEIGHT=$(json_height "$BTC_RESULT" .result.headers)
  BTC_SYNC_HEIGHT=$(json_height "$BTC_RESULT" .result.blocks)
  BTC_PROGRESS=$(printf '%s' "$BTC_RESULT" | jq -r ".result.verificationprogress // empty" 2>/dev/null)
  BTC_PROGRESS=$(calc_progress "$BTC_SYNC_HEIGHT" "$BTC_HEIGHT" "$BTC_PROGRESS")

  # calculate LTC chain sync progress
  LTC_RESULT=$(body ltc)
  LTC_HEIGHT=$(json_height "$LTC_RESULT" .result.headers)
  LTC_SYNC_HEIGHT=$(json_height "$LTC_RESULT" .result.blocks)
  LTC_PROGRESS=$(printf '%s' "$LTC_RESULT" | jq -r ".result.verificationprogress // empty" 2>/dev/null)
  LTC_PROGRESS=$(calc_progress "$LTC_SYNC_HEIGHT" "$LTC_HEIGHT" "$LTC_PROGRESS")

  ETH_RESULT=$(body eth_syncing)
  # Byte-exact matching broke on any whitespace/field-order variation between
  # providers; ask jq whether .result is literally false instead.
  if [ "$(echo "$ETH_RESULT" | jq -r '.result == false' 2>/dev/null)" = "true" ]; then
    ETH_RESULT=$(body eth_height)
    ETH_HEIGHT=$(json_height "$ETH_RESULT")
    ETH_SYNC_HEIGHT=$ETH_HEIGHT
    ETH_PROGRESS=$(calc_progress "$ETH_SYNC_HEIGHT" "$ETH_HEIGHT")
  elif [ -n "$ETH_RESULT" ]; then
    ETH_HEIGHT=$(json_height "$ETH_RESULT" .result.highestBlock)
    ETH_SYNC_HEIGHT=$(json_height "$ETH_RESULT" .result.currentBlock)
    # A syncing node reports both heights; without this the row printed an
    # empty SYNC cell for the one state where progress is most interesting.
    ETH_PROGRESS=$(calc_progress "$ETH_SYNC_HEIGHT" "$ETH_HEIGHT")
  else
    ETH_PROGRESS=Error
  fi

  # calculate ETH chain sync progress
  ETH_BEACON_RESULT=$(body beacon)
  if [ -n "$ETH_BEACON_RESULT" ]; then
    ETH_BEACON_HEIGHT=$(json_height "$ETH_BEACON_RESULT" '(.data.head_slot|tonumber)+(.data.sync_distance|tonumber)')
    ETH_BEACON_SYNC_HEIGHT=$(json_height "$ETH_BEACON_RESULT" '.data.head_slot|tonumber')
    ETH_BEACON_PROGRESS=$(calc_progress "$ETH_BEACON_SYNC_HEIGHT" "$ETH_BEACON_HEIGHT")
  else
    ETH_BEACON_PROGRESS=Error
  fi
  # No beacon endpoint is nothing to measure, which is the same "-" the table
  # prints wherever a measurement does not apply — as bifrost's cells do for
  # THORChain. So is a guessed co-located beacon that did not answer with a
  # slot: there is no beacon there. One that was named or is in-cluster still
  # reads Error, so a missing beacon stays distinguishable from a broken one.
  if [ -z "$ETHEREUM_BEACON_ENDPOINT" ] ||
    { [ "$ETHEREUM_BEACON_GUESSED" = "true" ] && [ "$ETH_BEACON_PROGRESS" = "Error" ]; }; then
    ETH_BEACON_PROGRESS="-"
  fi

  # calculate BCH chain sync progress
  BCH_RESULT=$(body bch)
  BCH_HEIGHT=$(json_height "$BCH_RESULT" .result.headers)
  BCH_SYNC_HEIGHT=$(json_height "$BCH_RESULT" .result.blocks)
  BCH_PROGRESS=$(printf '%s' "$BCH_RESULT" | jq -r ".result.verificationprogress // empty" 2>/dev/null)
  BCH_PROGRESS=$(calc_progress "$BCH_SYNC_HEIGHT" "$BCH_HEIGHT" "$BCH_PROGRESS")

  # calculate DOGE chain sync progress
  DOGE_RESULT=$(body doge)
  DOGE_HEIGHT=$(json_height "$DOGE_RESULT" .result.headers)
  DOGE_SYNC_HEIGHT=$(json_height "$DOGE_RESULT" .result.blocks)
  DOGE_PROGRESS=$(printf '%s' "$DOGE_RESULT" | jq -r ".result.verificationprogress // empty" 2>/dev/null)
  DOGE_PROGRESS=$(calc_progress "$DOGE_SYNC_HEIGHT" "$DOGE_HEIGHT" "$DOGE_PROGRESS")

  # calculate Gaia chain sync progress
  GAIA_HEIGHT=$(json_height "$(body gaia_ref)" .result.sync_info.latest_block_height)
  GAIA_SYNC_HEIGHT=$(json_height "$(body gaia)" .result.sync_info.latest_block_height)
  GAIA_PROGRESS=$(calc_progress "$GAIA_SYNC_HEIGHT" "$GAIA_HEIGHT")

  # calculate AVAX chain sync progress
  AVAX_HEIGHT_RESULT=$(body avax_ref)
  AVAX_SYNC_HEIGHT_RESULT=$(body avax)
  AVAX_HEIGHT=$(json_height "$AVAX_HEIGHT_RESULT")
  AVAX_SYNC_HEIGHT=$(json_height "$AVAX_SYNC_HEIGHT_RESULT")
  AVAX_PROGRESS=$(calc_progress "$AVAX_SYNC_HEIGHT" "$AVAX_HEIGHT")

  # calculate BSC chain sync progress
  BSC_HEIGHT_RESULT=$(body bsc_ref)
  BSC_SYNC_HEIGHT_RESULT=$(body bsc)
  BSC_HEIGHT=$(json_height "$BSC_HEIGHT_RESULT")
  BSC_SYNC_HEIGHT=$(json_height "$BSC_SYNC_HEIGHT_RESULT")
  BSC_PROGRESS=$(calc_progress "$BSC_SYNC_HEIGHT" "$BSC_HEIGHT")

  # calculate BASE chain sync progress
  BASE_HEIGHT_RESULT=$(body base_ref)
  BASE_SYNC_HEIGHT_RESULT=$(body base)
  BASE_HEIGHT=$(json_height "$BASE_HEIGHT_RESULT")
  BASE_SYNC_HEIGHT=$(json_height "$BASE_SYNC_HEIGHT_RESULT")
  BASE_PROGRESS=$(calc_progress "$BASE_SYNC_HEIGHT" "$BASE_HEIGHT")

  # calculate XRP chain sync progress
  if [ "$BIFROST_CHAINS_XRP_DISABLED" = "false" ]; then
    XRP_HEIGHT_RESULT=$(body xrp_ref)
    XRP_SYNC_HEIGHT_RESULT=$(body xrp)
    XRP_HEIGHT=$(json_height "$XRP_HEIGHT_RESULT" .result.ledger.ledger_index)
    XRP_SYNC_HEIGHT=$(json_height "$XRP_SYNC_HEIGHT_RESULT" .result.ledger.ledger_index)
    XRP_PROGRESS=$(calc_progress "$XRP_SYNC_HEIGHT" "$XRP_HEIGHT")
  fi

  if [ "$BIFROST_CHAINS_SOL_DISABLED" = "false" ]; then
    SOL_HEIGHT_RESULT=$(body sol_ref)
    SOL_SYNC_HEIGHT_RESULT=$(body sol)
    SOL_HEIGHT=$(json_height "$SOL_HEIGHT_RESULT")
    SOL_SYNC_HEIGHT=$(json_height "$SOL_SYNC_HEIGHT_RESULT")
    SOL_PROGRESS=$(calc_progress "$SOL_SYNC_HEIGHT" "$SOL_HEIGHT")
  fi

  # calculate Tron chain sync progress
  if [ "$BIFROST_CHAINS_TRON_DISABLED" = "false" ]; then
    TRON_HEIGHT=$(json_height "$(body tron_ref)" .block_header.raw_data.number)
    TRON_SYNC_HEIGHT=$(json_height "$(body tron)" .block_header.raw_data.number)
    TRON_PROGRESS=$(calc_progress "$TRON_SYNC_HEIGHT" "$TRON_HEIGHT")
  fi
fi

# calculate THOR chain sync progress
THOR_LOCAL=$(body thor_local)
THOR_SYNC_HEIGHT=$(json_height "$THOR_LOCAL" .result.sync_info.latest_block_height)
# Not "// empty": jq's alternative operator treats false as absent.
THOR_CATCHING_UP=$(printf '%s' "$THOR_LOCAL" | jq -r '.result.sync_info.catching_up' 2>/dev/null)
THOR_HEIGHT=$(json_height "$(body thor_ref)" .result.sync_info.latest_block_height)
# THOR_BASIS is the one height the churn estimates below measure from.
THOR_BASIS=""
if is_height "$THOR_HEIGHT"; then
  THOR_PROGRESS=$(calc_progress "$THOR_SYNC_HEIGHT" "$THOR_HEIGHT")
  # A node still catching up is not at tip whatever the reference says: the
  # reference may itself be stale, and three decimals round a node a handful
  # of blocks behind up to 100.000%.
  if [ "$THOR_CATCHING_UP" = "true" ] && [[ $THOR_PROGRESS == 100.* || $THOR_PROGRESS == 1[0-9][0-9].* ]]; then
    THOR_PROGRESS="syncing"
  fi
  THOR_BASIS=$THOR_HEIGHT
else
  # No reference configured for this network, or it did not answer. Using the
  # node as its own reference printed 100.000% for a stalled or catching-up
  # node; there is nothing to compare against, so say so. The node's own
  # catching_up still decides whether its height can stand in for the tip.
  THOR_PROGRESS=Error
  [ "$THOR_CATCHING_UP" = "false" ] && THOR_BASIS=$THOR_SYNC_HEIGHT
fi

cat <<"EOF"
 ________ ______  ___  _  __        __
/_  __/ // / __ \/ _ \/ |/ /__  ___/ /__
 / / / _  / /_/ / , _/    / _ \/ _  / -_)
/_/ /_//_/\____/_/|_/_/|_/\___/\_,_/\__/
EOF
echo

if [ "$VALIDATOR" = "true" ]; then
  echo "ADDRESS     $ADDRESS"
  echo "IP          $IP"
  echo "VERSION     $VERSION"
  echo "STATUS      $STATUS"
  echo "BOND        $(format_1e8 "$BOND")"
  echo "REWARDS     $(format_1e8 "$REWARDS")"
  echo "SLASH       $(format_int "$SLASH")"
  echo "PREFLIGHT   $PREFLIGHT"
fi

echo
echo "API         http://$IP:1317/thorchain/doc/"
echo "RPC         http://$IP:$THORNODE_PORT"
echo "MIDGARD     http://$IP:8080/v2/doc"

echo
# print_chain <label> <progress> <sync height> <tip height> [scanner key]
# Unknown heights render as "-" instead of fabricated figures: an unreachable
# endpoint previously printed a lag of minus the entire chain height, and a
# blocked reference printed "inf%". A known tip is still shown on its own. A
# sync height of 0 is a real height — a daemon at the start of initial sync —
# and prints its very large negative lag rather than being hidden as unknown.
print_chain() {
  local label="$1" progress="$2" sync="$3" tip="$4"
  scanner_status "$5"
  if ! is_height "$tip" || [ "$tip" = "0" ]; then
    row "$label" "$SCANNER_SYNC" "$SCANNER_BEHIND" "$progress" "-" "-"
  elif ! is_height "$sync"; then
    row "$label" "$SCANNER_SYNC" "$SCANNER_BEHIND" "$progress" "-" "$(format_int "$tip")"
  else
    row "$label" "$SCANNER_SYNC" "$SCANNER_BEHIND" "$progress" "$(format_int $((sync - tip)))" "$(format_int "$tip")"
  fi
}

# Each component gets a SYNC/BEHIND pair under its own heading, ordered so that
# each column is measured against the one to its right: bifrost's block scanner
# trails the daemon, and the daemon trails the TIP reference. Both BEHIND
# columns share a sign convention — negative means behind — and are 14 wide
# because a component syncing from genesis reports a lag of very nearly the
# whole chain height (a negated Solana slot height is 12 characters, 13 with
# the unhealthy marker). SYNC keeps three decimals: a component a few blocks
# off tip is not visible at fewer, and for the UTXO chains the figure is the
# daemon's own verification progress rather than a ratio of the heights beside
# it. TIP is last and unpadded, so a row runs to roughly eighty characters.
row() {
  printf "%-18s %-10s %-14s %-10s %-14s %s\n" "$@"
}

printf "%-18s %-25s %s\n" "" "BIFROST" "DAEMON"
row CHAIN SYNC BEHIND SYNC BEHIND TIP
# No scanner key: bifrost reports no block scanner height for THORChain.
if is_height "$THOR_HEIGHT"; then
  print_chain THOR "$THOR_PROGRESS" "$THOR_SYNC_HEIGHT" "$THOR_HEIGHT"
elif is_height "$THOR_SYNC_HEIGHT"; then
  # No reference: the node's own height is the only tip there is, and how far
  # behind it is cannot be known.
  row THOR "-" "-" "$THOR_PROGRESS" "-" "$(format_int "$THOR_SYNC_HEIGHT")"
else
  row THOR "-" "-" "$THOR_PROGRESS" "-" "-"
fi

if [ "$VALIDATOR" = "true" ]; then
  # Hide disabled chains, extending the gating XRP/TRON/SOL already had: a
  # disabled chain's heights are structurally zero, and printing them as
  # "0.000%  0  0" reads as a broken chain rather than a configured one.
  if [ "$BIFROST_CHAINS_BTC_DISABLED" != "true" ]; then
    print_chain BTC "$BTC_PROGRESS" "$BTC_SYNC_HEIGHT" "$BTC_HEIGHT" BTC
  fi
  if [ "$BIFROST_CHAINS_ETH_DISABLED" != "true" ]; then
    print_chain ETH "$ETH_PROGRESS" "$ETH_SYNC_HEIGHT" "$ETH_HEIGHT" ETH
    # No scanner key: bifrost observes ETH through its execution endpoint and
    # reports no separate beacon scanner.
    print_chain "ETH (beacon slot)" "$ETH_BEACON_PROGRESS" "$ETH_BEACON_SYNC_HEIGHT" "$ETH_BEACON_HEIGHT"
  fi
  if [ "$BIFROST_CHAINS_LTC_DISABLED" != "true" ]; then
    print_chain LTC "$LTC_PROGRESS" "$LTC_SYNC_HEIGHT" "$LTC_HEIGHT" LTC
  fi
  if [ "$BIFROST_CHAINS_BCH_DISABLED" != "true" ]; then
    print_chain BCH "$BCH_PROGRESS" "$BCH_SYNC_HEIGHT" "$BCH_HEIGHT" BCH
  fi
  if [ "$BIFROST_CHAINS_DOGE_DISABLED" != "true" ]; then
    print_chain DOGE "$DOGE_PROGRESS" "$DOGE_SYNC_HEIGHT" "$DOGE_HEIGHT" DOGE
  fi
  if [ "$BIFROST_CHAINS_GAIA_DISABLED" != "true" ]; then
    print_chain GAIA "$GAIA_PROGRESS" "$GAIA_SYNC_HEIGHT" "$GAIA_HEIGHT" GAIA
  fi
  if [ "$BIFROST_CHAINS_AVAX_DISABLED" != "true" ]; then
    print_chain AVAX "$AVAX_PROGRESS" "$AVAX_SYNC_HEIGHT" "$AVAX_HEIGHT" AVAX
  fi
  if [ "$BIFROST_CHAINS_BSC_DISABLED" != "true" ]; then
    print_chain BSC "$BSC_PROGRESS" "$BSC_SYNC_HEIGHT" "$BSC_HEIGHT" BSC
  fi
  if [ "$BIFROST_CHAINS_BASE_DISABLED" != "true" ]; then
    print_chain BASE "$BASE_PROGRESS" "$BASE_SYNC_HEIGHT" "$BASE_HEIGHT" BASE
  fi
  if [ "$BIFROST_CHAINS_XRP_DISABLED" = "false" ]; then
    print_chain XRP "$XRP_PROGRESS" "$XRP_SYNC_HEIGHT" "$XRP_HEIGHT" XRP
  fi
  if [ "$BIFROST_CHAINS_TRON_DISABLED" = "false" ]; then
    print_chain TRON "$TRON_PROGRESS" "$TRON_SYNC_HEIGHT" "$TRON_HEIGHT" TRON
  fi
  if [ "$BIFROST_CHAINS_SOL_DISABLED" = "false" ]; then
    print_chain SOL "$SOL_PROGRESS" "$SOL_SYNC_HEIGHT" "$SOL_HEIGHT" SOL
  fi
fi

if [ "$SCANNER_UNHEALTHY" = "true" ]; then
  echo
  echo "* bifrost scanner unhealthy: chain halted, or block fetches failing"
fi

# Without both heights the estimates below would extrapolate from nothing: an
# unset height is 0 in bash arithmetic, which reads as a node at genesis.
if ! is_height "$THOR_SYNC_HEIGHT" || ! is_height "$THOR_BASIS"; then
  exit 0
fi

# skip unbond eta calculation if not within 100 blocks of tip
# (the previous comparison was inverted: it skipped when the node was AHEAD of
# the reference, and never while actually catching up). Vault membership below
# is read from the node's own state, which is just as stale, so it goes too.
if [ "$((THOR_BASIS - THOR_SYNC_HEIGHT))" -gt 100 ]; then
  exit 0
fi

# get values for churn calculations. "// empty" and the fallbacks rather than
# testing for "null": a mimir request that failed outright has an empty body,
# from which jq prints nothing at all, and the empty value then broke the
# arithmetic below with a syntax error.
MIMIR=$(body mimir)
CONSTANTS=$(body constants)
VAULTS=$(body vaults)
CHURN_MIGRATE_ROUNDS=$(echo "$MIMIR" | jq -r ".CHURNMIGRATEROUNDS // empty" 2>/dev/null)
CHURN_MIGRATE_ROUNDS=${CHURN_MIGRATE_ROUNDS:-$(echo "$CONSTANTS" | jq -r ".int_64_values.ChurnMigrateRounds // empty" 2>/dev/null)}
FUND_MIGRATION_INTERVAL=$(echo "$MIMIR" | jq -r ".FUNDMIGRATIONINTERVAL // empty" 2>/dev/null)
FUND_MIGRATION_INTERVAL=${FUND_MIGRATION_INTERVAL:-$(echo "$CONSTANTS" | jq -r ".int_64_values.FundMigrationInterval // empty" 2>/dev/null)}
CHURN_INTERVAL=$(echo "$MIMIR" | jq -r ".CHURNINTERVAL // empty" 2>/dev/null)
CHURN_INTERVAL=${CHURN_INTERVAL:-$(echo "$CONSTANTS" | jq -r ".int_64_values.ChurnInterval // empty" 2>/dev/null)}
CHURN_RETRY_INTERVAL=$(echo "$MIMIR" | jq -r ".CHURNRETRYINTERVAL // empty" 2>/dev/null)
CHURN_RETRY_INTERVAL=${CHURN_RETRY_INTERVAL:-$(echo "$CONSTANTS" | jq -r ".int_64_values.ChurnRetryInterval // empty" 2>/dev/null)}
HALT_CHURNING=$(echo "$MIMIR" | jq -r ".HALTCHURNING // empty" 2>/dev/null)
HALT_CHURNING=${HALT_CHURNING:-0}
BLOCKS_PER_YEAR=$(echo "$CONSTANTS" | jq -r ".int_64_values.BlocksPerYear // empty" 2>/dev/null)
CHURN_START=$(echo "$VAULTS" | jq -r '[.[]|.block_height]|max' 2>/dev/null)
CHURNING=$(echo "$VAULTS" | jq '[.[]|select(.status=="RetiringVault")]|length > 0' 2>/dev/null)

# format_duration <seconds> — the "2d 3h 4m 5s" form the estimates already use.
format_duration() {
  local days=$(($1 / 86400))
  date -d "@$1" -u +"${days}d %-Hh %-Mm %-Ss"
}

# eta_seconds <blocks from the tip> — how much time that many blocks represent.
# Measured by preference: ask how long ago the block that far BELOW the tip was
# produced, which follows the chain's actual recent rate rather than assuming
# one. That block is up to a churn interval back, which a pruned or freshly
# synced node may not retain, so fall back to the network's own nominal rate
# from BlocksPerYear — the chain runs a little slower than nominal, so the
# fallback is slightly optimistic, but it is available at any retention.
# Measured against THOR_BASIS, the same height every caller counts from. A
# measurement outside a minute per block — a clock off, a block time from the
# future — is not a measurement, and gives way to the nominal rate likewise.
eta_seconds() {
  local blocks=$1 mirror past elapsed
  is_height "$blocks" || return
  mirror=$((THOR_BASIS - blocks))
  if [ "$blocks" -gt 0 ] && [ "$mirror" -gt 0 ]; then
    past=$(curl -sL --fail -m 10 "${RPC}/block?height=$mirror" |
      jq -r '.result.block.header.time|split(".")[0]+"Z"|fromdateiso8601' 2>/dev/null)
    if is_height "$past" && [ "$past" != "0" ]; then
      elapsed=$(($(date +%s) - past))
      if [ "$elapsed" -gt 0 ] && [ "$elapsed" -le $((blocks * 60)) ]; then
        echo "$elapsed"
        return
      fi
    fi
  fi
  if is_height "$BLOCKS_PER_YEAR" && [ "$BLOCKS_PER_YEAR" != "0" ]; then
    echo $((blocks * 31536000 / BLOCKS_PER_YEAR))
  fi
}

# churning_halted — whether HALTCHURNING stops a churn at the current height,
# by the rule the validator manager applies: set, and not above the height.
churning_halted() {
  is_height "$HALT_CHURNING" && [ "$HALT_CHURNING" != "0" ] && [ "$HALT_CHURNING" -le "$THOR_BASIS" ]
}

# How long after a churn attempt its keygen may still be running: bifrost's
# keygen_timeout (5m by default), doubled to cover the keygen results reaching
# consensus, in blocks at the nominal rate.
KEYGEN_WINDOW=100
if is_height "$BLOCKS_PER_YEAR" && [ "$BLOCKS_PER_YEAR" != "0" ]; then
  KEYGEN_WINDOW=$((600 * BLOCKS_PER_YEAR / 31536000))
fi

# churn_estimates — the CHURN ESTIMATES section. Returns without printing when
# the inputs are missing, so that the vault membership after it, which needs
# only the vaults, is still shown when mimir and constants did not answer.
churn_estimates() {
  local value
  # Neither mimir nor constants answered, or no vaults: nothing to estimate from.
  for value in "$CHURN_MIGRATE_ROUNDS" "$FUND_MIGRATION_INTERVAL" "$CHURN_INTERVAL" "$CHURN_START"; do
    is_height "$value" || return
  done

  # calculate churn height
  OVERDUE=false
  if [ "$CHURNING" = "false" ]; then
    # set churn height to last churn plus interval
    CHURN_START=$((CHURN_START + CHURN_INTERVAL))

    if [ "$CHURN_START" -le "$THOR_BASIS" ]; then
      # The churn height has passed with no RetiringVault yet. A keygen starts
      # at that height, and again every ChurnRetryInterval blocks after it
      # until one succeeds; while the latest attempt's keygen can still be
      # running, the new vaults do not exist yet and the churn is underway.
      KEYGEN_HEIGHT=$CHURN_START
      if is_height "$CHURN_RETRY_INTERVAL" && [ "$CHURN_RETRY_INTERVAL" != "0" ]; then
        KEYGEN_HEIGHT=$((THOR_BASIS - (THOR_BASIS - CHURN_START) % CHURN_RETRY_INTERVAL))
      fi
      if ! churning_halted && [ $((THOR_BASIS - KEYGEN_HEIGHT)) -le "$KEYGEN_WINDOW" ]; then
        # The migration that follows counts from this attempt, not from the
        # scheduled height.
        CHURN_START=$KEYGEN_HEIGHT
        START_ETA="keygen in progress (started $(format_int $((THOR_BASIS - KEYGEN_HEIGHT))) blocks ago)"
      else
        # Otherwise the churn is overdue — churning is halted, or churn
        # retries are failing. The ETA below extrapolates from a block mirrored
        # BEYOND the tip, which does not exist, so it printed nonsense
        # (e.g."20670d").
        OVERDUE=true
        OVERDUE_BLOCKS=$((THOR_BASIS - CHURN_START))
        OVERDUE_SECONDS=$(eta_seconds "$OVERDUE_BLOCKS")
        START_ETA="overdue by $(format_int "$OVERDUE_BLOCKS") blocks"
        [ -n "$OVERDUE_SECONDS" ] && START_ETA="$START_ETA / $(format_duration "$OVERDUE_SECONDS")"
        START_ETA="$START_ETA (mimir HALTCHURNING=$HALT_CHURNING)"
      fi
    else
      # calculate start churn eta
      SECONDS_LEFT=$(eta_seconds $((CHURN_START - THOR_BASIS)))
      if [ -n "$SECONDS_LEFT" ]; then
        START_ETA=$(format_duration "$SECONDS_LEFT")
      else
        START_ETA="unavailable"
      fi
      # The height is still ahead, but with churning halted it will pass
      # without a churn, so say so: the estimate is when the height arrives,
      # not when a churn happens.
      if [ "$HALT_CHURNING" != "0" ]; then
        START_ETA="$START_ETA (mimir HALTCHURNING=$HALT_CHURNING)"
      fi
    fi
  else
    START_ETA="in progress"
  fi

  # calculate finish churn eta
  CHURN_FINISH=$((CHURN_START + ((CHURN_MIGRATE_ROUNDS + 1) * FUND_MIGRATION_INTERVAL) + 1))
  BLOCKS_LEFT=$((CHURN_FINISH - THOR_BASIS))
  # An overdue churn has not started, so it has no finish to estimate — however
  # far off the nominal finish height still is.
  if [ "$OVERDUE" = "true" ]; then
    FINISH_ETA="n/a (churn overdue; no migration in progress)"
  elif [ "$BLOCKS_LEFT" -lt 0 ]; then
    FINISH_ETA="final migration pending - check for stuck migration or insolvency"
  else
    SECONDS_LEFT=$(eta_seconds "$BLOCKS_LEFT")
    if [ -n "$SECONDS_LEFT" ]; then
      FINISH_ETA=$(format_duration "$SECONDS_LEFT")
    else
      FINISH_ETA="unavailable"
    fi
  fi

  echo
  echo
  echo "CHURN ESTIMATES - failed keygens, halted chains, and stuck migrations not considered"
  echo
  echo "    Churn Interval: $CHURN_INTERVAL"
  echo "Migration Interval: $FUND_MIGRATION_INTERVAL"
  echo "  Migration Rounds: $CHURN_MIGRATE_ROUNDS (plus one additional round expected for dust)"
  echo
  echo "      Start Height: $CHURN_START"
  echo "   ETA Churn Start: $START_ETA"
  echo
  echo "     Finish Height: $CHURN_FINISH"
  echo "  ETA Churn Finish: $FINISH_ETA"
}

churn_estimates

# find retiring and active member vaults. Only from a vault list that answered:
# an empty one has no memberships in it, which would read as Unbond Allowed.
if [ -n "$PUB_KEY" ] && [ "$PUB_KEY" != "null" ] &&
  printf '%s' "$VAULTS" | jq -e 'type == "array"' >/dev/null 2>&1; then
  echo

  MEMBER_ACTIVE=$(echo "$VAULTS" | jq -r ".[] | select(.status == \"ActiveVault\") | select([.membership[] == \"$PUB_KEY\"] | any) | .addresses[] | select(.chain == \"THOR\") | .address")
  MEMBER_RETIRING=$(echo "$VAULTS" | jq -r ".[] | select(.status == \"RetiringVault\") | select([.membership[] == \"$PUB_KEY\"] | any) | .addresses[] | select(.chain == \"THOR\") | .address")
  UNBOND_ALLOWED="true"

  EXPLORER="https://thorchain.net"
  if [ "$NET" = "stagenet" ]; then
    EXPLORER="https://stagenet.thorchain.net"
  fi
  if [ "$NET" = "chainnet" ]; then
    EXPLORER="https://chainnet.thorchain.net"
  fi
  if [ -n "$MEMBER_ACTIVE" ]; then
    echo "  Active Member: $EXPLORER/address/$MEMBER_ACTIVE"
    UNBOND_ALLOWED="false"
  fi
  if [ -n "$MEMBER_RETIRING" ]; then
    echo "Retiring Member: $EXPLORER/address/$MEMBER_RETIRING"
    UNBOND_ALLOWED="false"
  fi

  echo " Unbond Allowed: $UNBOND_ALLOWED"
fi
