# Connecting to THORChain

This guide helps developers connect to THORChain's network for querying data, building wallets, dashboards, or debugging network issues. THORChain provides five primary data sources:

- **Midgard**: Consumer data for swaps, pools, and volume, ideal for dashboards.
- **THORNode**: Raw blockchain data for THORChain-specific queries, used by wallets and explorers.
- **Cosmos RPC**: Generic Cosmos SDK data, such as balances and transactions.
- **Tendermint RPC**: Consensus and node status data for monitoring.
- **gRPC**: Full Cosmos SDK interface via protocol buffers.

```admonish info
The below endpoints are run by specific organisations for public use. There is a cost to running these services. If you want to run your own full node, please see [https://docs.thorchain.org/thornodes/overview.](https://docs.thorchain.org/thornodes/overview)
```

## Quick Reference

| Source             | Purpose                            | Mainnet URLs                                                                                                                                                | Stagenet URLs |
| ------------------ | ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| **Midgard**        | Swap, pool, volume, and user data  | \- gateway.liquify.com/chain/thorchain_midgard<br>- midgard.thorchain.network                                                                               | TBD           |
| **THORNode**       | THORChain-specific blockchain data | \- gateway.liquify.com/chain/thorchain_api<br>- thornode.thorchain.network<br>- Pre-hard-fork (blocks ≤4786559): gateway.liquify.com/chain/thorchain_v1_api | TBD           |
| **Cosmos RPC**     | Cosmos SDK data (e.g., balances)   | \- gateway.liquify.com/chain/thorchain_api<br>- thornode.thorchain.network                                                                                  | TBD           |
| **Tendermint RPC** | Consensus and node status data     | \- gateway.liquify.com/chain/thorchain_rpc<br>- rpc.thorchain.network<br>- Pre-hard-fork (blocks ≤4786559): gateway.liquify.com/chain/thorchain_v1_rpc      | TBD           |
| **gRPC**           | Cosmos SDK via protocol buffers    | \- grpc.thorchain.network                                                                                                                                   | TBD           |

```admonish info
Stagenet endpoints are being transitioned. New URLs will be published when available.
```

```admonish info
THORSwap (`*.thorswap.net`) provides private endpoints for THORSwap application users. Contact THORSwap for access.
```

## Usage Guidelines

- **Rate Limits**: Liquify enforces a rate limit of 50,000 requests per day per IP. Include an `x-client-id` header to identify your application — Liquify can increase limits for applications that include this header.
- **Run Your Own Node**: For production apps, run a THORNode to avoid rate limits and ensure uptime. See THORNode Setup Guide.
- **Hard-Fork Note**: Mainnet hard-fork at block 4786560 requires post-hard-fork endpoints for newer data. Liquify provides full archive support and smart-routes archive requests, so both pre and post hard-fork data is accessible via the same endpoints. Dedicated pre-hard-fork endpoints are also available (see below).
- **Error Handling**: Handle HTTP 429 (rate limit) or 503 (node overload) with exponential backoff for retries.

## Midgard

Midgard provides time-series data for swaps, pools, volume, and liquidity providers, ideal for dashboards and analytics. It proxies THORNode queries to reduce network load and runs on every node.

- **Mainnet:**
  - [https://gateway.liquify.com/chain/thorchain_midgard/v2/doc](https://gateway.liquify.com/chain/thorchain_midgard/v2/doc)
  - [https://midgard.thorchain.network/v2/doc](https://midgard.thorchain.network/v2/doc)
- **Stagenet:** TBD

## THORNode

THORNode provides raw blockchain data (e.g., balances, transactions) specific to THORChain's state machine, critical for wallets and block explorers. Avoid excessive queries to public nodes to prevent overloading.

- **Mainnet (for post-hard-fork blocks 4786560 and later):**
  - [https://gateway.liquify.com/chain/thorchain_api/thorchain/doc](https://gateway.liquify.com/chain/thorchain_api/thorchain/doc)
  - [https://thornode.thorchain.network/thorchain/doc](https://thornode.thorchain.network/thorchain/doc)
  - **Pre-hard-fork blocks 4786559 and earlier**\
    [https://gateway.liquify.com/chain/thorchain_v1_api/thorchain/doc](https://gateway.liquify.com/chain/thorchain_v1_api/thorchain/doc)
- **Stagenet:** TBD

## Cosmos RPC

Cosmos RPC provides generic Cosmos SDK data (e.g., account balances, transactions). Common endpoints include `/cosmos/bank/v1beta1/balances` and `/cosmos/base/tendermint/v1beta1/blocks`. Not all endpoints are enabled.

### Cosmos Documentation

- Cosmos SDK CLI, gRPC, and REST API - [Cosmos Docs](https://docs.cosmos.network/sdk/latest/learn/concepts/cli-grpc-rest)

### Cosmos Endpoints

Use THORNode URLs with `/cosmos/...` paths.

- **Example**
  - **Mainnet** [https://gateway.liquify.com/chain/thorchain_api/cosmos/bank/v1beta1/balances/thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt](https://gateway.liquify.com/chain/thorchain_api/cosmos/bank/v1beta1/balances/thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt)
  - **Stagenet** TBD

## Tendermint RPC

Tendermint (CometBFT) RPC provides consensus and node status data (e.g., block height, validator status), useful for monitoring and debugging.

### Tendermint Documentation

- CometBFT v0.38 RPC - [CometBFT RPC Guide](https://docs.cometbft.com/v0.38/rpc/)

### Ports

- MAINNET Port: `27147`
- STAGENET Port: `26657`

### Tendermint RPC Endpoints

- **Mainnet: (for post-hard-fork blocks 4786560 and later)**
  - [https://gateway.liquify.com/chain/thorchain_rpc](https://gateway.liquify.com/chain/thorchain_rpc)
  - [https://rpc.thorchain.network](https://rpc.thorchain.network)
- **Mainnet: Pre-hard-fork blocks 4786559 and earlier.**
  - [https://gateway.liquify.com/chain/thorchain_v1_rpc](https://gateway.liquify.com/chain/thorchain_v1_rpc)
- **Stagenet:** TBD

```admonish info
Chainnet (`rpc.thorchain.network`) intentionally restricts some Tendermint RPC endpoints (e.g., `net_info`, `consensus_state`, `dump_consensus_state`). Use Liquify for full Tendermint RPC access.
```

## gRPC

gRPC provides the full Cosmos SDK interface via protocol buffers, useful for programmatic access from languages with gRPC support.

- **Mainnet:** `grpc.thorchain.network`
- **Documentation:** [Cosmos CLI, gRPC, and REST API](https://docs.cosmos.network/sdk/latest/learn/concepts/cli-grpc-rest)
- **Stagenet:** TBD

```admonish info
gRPC uses protocol buffers over HTTP/2, not JSON over HTTP. Standard HTTP clients (curl, browsers) will not work — use a gRPC client library.
```

## P2P

P2P is the peer-to-peer network layer for node communication, useful for debugging connectivity issues between THORChain nodes.

- MAINNET Port: `27146`
- STAGENET Port: `26656`

## P2P Guide

- [https://docs.cometbft.com/v0.38/rpc/](https://docs.cometbft.com/v0.38/rpc/)

### Example: Check Peer Connections

```bash
curl -X GET "https://gateway.liquify.com/chain/thorchain_rpc/net_info" -H "accept: application/json"
```

## Other Services

| Service              | URL                                                 |
| -------------------- | --------------------------------------------------- |
| Transaction tracking | [track.thorchain.org](https://track.thorchain.org/) |
| Haskoin (BCH)        | [api.haskoin.com/bch](https://api.haskoin.com/bch)  |

## External Chain RPCs

THORChain does not provide public RPC proxies for external chains (Bitcoin, Ethereum, etc.). For external chain RPC access, use a provider of your choice or run your own node.
