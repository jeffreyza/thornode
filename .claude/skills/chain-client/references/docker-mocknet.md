# Docker and Mocknet Setup for New Chains

Every new chain needs Docker/mocknet infrastructure to be testable locally. This covers the chain daemon service, Bifrost configuration, and initialization scripts.

## Key Files

- `build/docker/docker-compose.yml` — all service definitions
- `build/docker/mocknet/` — chain-specific init scripts
- `build/scripts/` — genesis, bifrost, and validator startup scripts
- `config/default.yaml` — default Bifrost chain configuration

## Step 1: Add Chain Daemon Service

Add a service definition in `build/docker/docker-compose.yml`:

```yaml
newchain:
  profiles:
    - mocknet
    - mocknet-cluster
  image: <registry>/<daemon>:<version>
  restart: unless-stopped
  environment:
    BLOCK_TIME: ${BLOCK_TIME:-1}
    # Chain-specific env vars
  ports:
    - "<port>:<port>"
  volumes:
    - "./mocknet/<chain>:/mocknet"
    - "../scripts:/docker/scripts"
  entrypoint: /mocknet/init-newchain.sh
```

Existing chain daemon patterns by type:

| Type       | Example       | Mode                    | Block Production            |
| ---------- | ------------- | ----------------------- | --------------------------- |
| **UTXO**   | bitcoin       | regtest                 | `generatetoaddress` in loop |
| **EVM**    | ethereum/geth | `--dev --dev.period`    | auto-mining                 |
| **Cosmos** | gaia          | custom chain-id         | validator auto-produces     |
| **Solana** | agave         | `solana-test-validator` | auto-produces               |
| **XRP**    | xrpld         | `--standalone`          | `ledger_accept` in loop     |
| **TRON**   | tron-daemon   | mocknet config          | supernode + fullnode pair   |

## Step 2: Add Initialization Script

Create `build/docker/mocknet/init-newchain.sh` (or use the daemon's built-in entrypoint):

```bash
#!/bin/bash
set -euo pipefail

# 1. Start the daemon
newchain-daemon --testnet --rpc-port=<port> &

# 2. Wait for daemon to be ready
until newchain-cli getinfo 2>/dev/null; do sleep 1; done

# 3. Fund master account (for simulation test seeding)
MASTER_ADDR="<test-address>"
newchain-cli fund "$MASTER_ADDR" 1000000

# 4. Generate initial blocks (if needed)
newchain-cli generate 100

# 5. Keep producing blocks
while true; do
    newchain-cli generate 1
    sleep "${BLOCK_TIME:-1}"
done
```

## Step 3: Configure Bifrost Environment Variables

In the `bifrost` service section of `docker-compose.yml`:

```yaml
bifrost:
  environment:
    # Enable/disable toggle
    BIFROST_CHAINS_NEWCHAIN_DISABLED: "${BIFROST_CHAINS_NEWCHAIN_DISABLED:-false}"

    # RPC endpoint
    BIFROST_CHAINS_NEWCHAIN_RPC_HOST: ${NEWCHAIN_HOST:-http://newchain:<port>}

    # For Cosmos chains additionally:
    BIFROST_CHAINS_NEWCHAIN_COSMOS_GRPC_HOST: newchain:9090
    BIFROST_CHAINS_NEWCHAIN_COSMOS_CHAIN_ID: "localnewchain"

    # Block scanner settings (adjust per chain):
    BIFROST_CHAINS_NEWCHAIN_BLOCK_SCANNER_OBSERVATION_FLEXIBILITY_BLOCKS: "5"

    # For EVM chains additionally:
    BIFROST_CHAINS_NEWCHAIN_BLOCK_SCANNER_FIXED_GAS_RATE: 20_000_000_000
    BIFROST_CHAINS_NEWCHAIN_BLOCK_SCANNER_MAX_GAS_LIMIT: 80000
    BIFROST_CHAINS_NEWCHAIN_BLOCK_SCANNER_WHITELIST_TOKENS: "<addr1>,<addr2>"
```

Also add the host variable to the `thornode` service:

```yaml
thornode:
  environment:
    NEWCHAIN_HOST: ${NEWCHAIN_HOST:-http://newchain:<port>}
```

## Step 4: Add Default Configuration

In `config/default.yaml`, add a section under `bifrost.chains`:

```yaml
newchain:
  chain_id: NEWCHAIN
  rpc_host: "${NEWCHAIN_HOST:-http://localhost:<port>}"
  disabled: true # disabled by default until chain goes live
  block_scanner:
    chain_id: NEWCHAIN
    enforce_block_height: false
    block_scan_processors: 1
    block_height_discover_back_off: 1s
    observation_flexibility_blocks: 5
    solvency_blocks: 10
    gas_price_resolution: 1
    max_reorg_rescan_blocks: 100
```

## Step 5: EVM Contract Deployment (EVM chains only)

If the chain uses the THORChain router contract:

1. Add deployment to `build/scripts/core-unsafe.sh`:

```bash
deploy_newchain_contract() {
    python3 /scripts/evm/evm-tool.py --chain NEWCHAIN \
        --rpc "http://newchain:<port>" --action deploy
}
```

2. Add to `deploy_evm_contracts()` in `genesis.sh`
3. Add `set_newchain_contract()` function to register the contract address in genesis

## Step 6: Add Bifrost Service Dependency

Ensure bifrost depends on the new chain daemon:

```yaml
bifrost:
  depends_on:
    newchain:
      condition: service_started # or service_healthy if healthcheck defined
```

## Running Mocknet

```bash
# Start with new chain
make run-mocknet

# Or manually
docker compose --profile mocknet up -d

# Check logs for new chain
docker compose logs -f newchain

# Check bifrost connected to new chain
docker compose logs bifrost | grep -i newchain
```

## Existing Chain Ports Reference

| Chain        | Service       | Port                     |
| ------------ | ------------- | ------------------------ |
| Bitcoin      | bitcoin       | 18443                    |
| Bitcoin Cash | bitcoin-cash  | 28443                    |
| Litecoin     | litecoin      | 38443                    |
| Dogecoin     | dogecoin      | 18332                    |
| Ethereum     | ethereum      | 8545                     |
| Avalanche    | avalanche     | 9650                     |
| BSC          | binance-smart | 8546                     |
| Base         | base          | 8547                     |
| Polygon      | polygon       | 8548                     |
| XRP          | xrp           | 5005                     |
| TRON         | tron-fullnode | 8090, 8091               |
| Gaia         | gaia          | 23357 (RPC), 9091 (gRPC) |
| Noble        | noble         | 20257 (RPC), 9092 (gRPC) |
| Solana       | solana        | 8899                     |
| Zcash        | zcash         | 18232                    |
