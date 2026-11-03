#!/bin/sh

ls -l /root/.noble/config/genesis.json

# skip if genesis file already exists
if [ -f /root/.noble/config/genesis.json ]; then
  exec /entrypoint.sh
  exit 0
fi

# initialize chain
mkdir -p /root/.noble/config
cp /etc/noble/app.toml /root/.noble/config/app.toml
/nobled init --chain-id localnoble local

jq -s '.[0] * .[1]' /root/.noble/config/genesis.json /mocknet/noble-config.json >/tmp/genesis.json
mv /tmp/genesis.json /root/.noble/config/genesis.json

cat <<EOF | /nobled keys --keyring-backend file add master --recover
dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil
password
password
EOF

# create genesis accounts
/nobled genesis add-genesis-account noble1hnyy4gp5tgarpg3xu6w5cw4zsyphx2lygefjh2 10000000stake      # validator
/nobled genesis add-genesis-account noble1f4l5dlqhaujgkxxqmug4stfvmvt58vx2pru9pu 1000000000000uusdc # master

# create genesis transaction
echo "password" | /nobled genesis gentx --keyring-backend=file master 10000000stake --chain-id=localnoble
/nobled genesis collect-gentxs

exec /entrypoint.sh
