#!/bin/sh

# skip if genesis file already exists
if [ -f /root/.gaia/config/genesis.json ]; then
  exec /entrypoint.sh
  exit 0
fi

# initialize chain
mkdir -p /root/.gaia/config
cp /etc/gaia/app.toml /root/.gaia/config/app.toml
/gaiad init --chain-id localgaia local

# create keys
cat <<EOF | /gaiad keys --keyring-backend file add master --recover
dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil
password
password
EOF

# create genesis accounts
/gaiad genesis add-genesis-account cosmos1hnyy4gp5tgarpg3xu6w5cw4zsyphx2lyq6u60y 10000000uatom      # validator
/gaiad genesis add-genesis-account cosmos1f4l5dlqhaujgkxxqmug4stfvmvt58vx2fqfdej 1000000000000uatom # master

# replace stake with uatom
sed -i 's/"stake"/"uatom"/g' /root/.gaia/config/genesis.json

# update min gas prices and disable fee market
jq '.app_state.feemarket.params.enabled = false|
  .app_state.feemarket.params.min_base_gas_price = "0.001000000000000000"|
  .app_state.feemarket.state.base_gas_price = "0.001000000000000000"' \
  /root/.gaia/config/genesis.json >/tmp/genesis.json
mv /tmp/genesis.json /root/.gaia/config/genesis.json

# create genesis transaction
echo "password" | /gaiad genesis gentx --keyring-backend=file master 10000000uatom --chain-id=localgaia
/gaiad genesis collect-gentxs

exec /entrypoint.sh
