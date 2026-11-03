#!/bin/bash
set -euo pipefail

config="${TRON_NODE_TYPE}.conf"
extra_args=()

case "${TRON_NODE_TYPE}" in
mocknet-fullnode)
  heap_size="${TRON_HEAP_SIZE:-1536m}"
  if [[ -n ${TRON_SEED_NODE-} ]]; then
    sed -i "s/localhost:18888/${TRON_SEED_NODE}/" "$HOME/${config}"
    # Seed discovery finds the producer; active keeps the single-peer mocknet
    # connection dialed so the fullnode tip does not go stale mid-simulation.
    awk -v peer="${TRON_SEED_NODE}" '
      /^  active = \[/ {
        print "  active = ["
        print "    \"" peer "\""
        print "  ]"
        in_active = 1
        next
      }
      in_active && /^  \]/ {
        in_active = 0
        next
      }
      !in_active { print }
    ' "$HOME/${config}" >"$HOME/${config}.tmp"
    mv "$HOME/${config}.tmp" "$HOME/${config}"
  fi
  # Require the producer connection before accepting a broadcast. Otherwise
  # a transient peer disconnect can return success after sending to zero peers,
  # leaving both simulation deposits and vault migrations unconfirmed.
  # Validate the fixture assignment before replacing the config: a silent no-op
  # would leave broadcasts unguarded if the image changes its config format.
  if ! awk '
    {
      setting = $0
      sub(/(#|\/\/).*$/, "", setting)
      if (index(setting, "minEffectiveConnection")) {
        count++
        if (setting !~ /^[[:space:]]*minEffectiveConnection[[:space:]]*=[[:space:]]*[01][[:space:]]*$/) {
          invalid = 1
        } else if (setting ~ /=[[:space:]]*0[[:space:]]*$/) {
          sub(/=[[:space:]]*0/, "= 1")
        }
      }
      print
    }
    END { exit(count != 1 || invalid) }
  ' "$HOME/${config}" >"$HOME/${config}.tmp"; then
    rm -f "$HOME/${config}.tmp"
    echo "Invalid TRON mocknet config: expected exactly one minEffectiveConnection assignment set to 0 or 1 in $HOME/${config}" >&2
    exit 1
  fi
  mv "$HOME/${config}.tmp" "$HOME/${config}"
  ;;
mocknet-supernode)
  heap_size="${TRON_HEAP_SIZE:-1024m}"
  extra_args=(--witness)
  # Mocknet uses a single local witness, so the default participation floor
  # eventually stalls block production with LOW_PARTICIPATION.
  sed -i 's/minParticipationRate = 15/minParticipationRate = 0/' "$HOME/${config}"
  ;;
testnet-nile)
  heap_size="${TRON_HEAP_SIZE:-16g}"
  ;;
mainnet)
  heap_size="${TRON_HEAP_SIZE:-24g}"
  ;;
*)
  echo "TRON_NODE_TYPE: '${TRON_NODE_TYPE}' not supported" >&2
  exit 1
  ;;
esac

exec /usr/bin/java -Xmx"${heap_size}" -XX:+HeapDumpOnOutOfMemoryError \
  -Dlogback.configurationFile=/scripts/logback.xml \
  -jar FullNode.jar -c "${config}" "${extra_args[@]}"
