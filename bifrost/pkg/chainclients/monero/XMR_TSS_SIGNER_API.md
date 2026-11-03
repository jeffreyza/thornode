# XMR TSS Signer API

The Monero sidecar exposes HTTP endpoints for DKG, signing rounds, broadcast, and scanner sync/read APIs.

## Configuration

Bifrost (`bifrost.chains.xmr`):

- `signer_backend`: `tss`
- `signer_endpoint`: base URL for the signer service
- `chain_network`: expected Monero daemon identity (`mocknet`, `mainnet`, `stagenet`, or `testnet`)
- `signer_expected_network`: expected THOR deployment/HRP identity reported by the signer
- Auth credentials used by Bifrost when calling sidecar:
  - `authorization_bearer` (Bearer token), or
  - `username` + `password` (HTTP Basic auth)

Sidecar (`xmr-frost-signer` environment):

- `XMR_FROST_CHAIN_NETWORK=mocknet|chainnet|stagenet|mainnet` (required; controls XMR vault pubkey HRP)
- `XMR_FROST_DAEMON_URL` (required)
- `XMR_FROST_STORE_SECRET` (required; mainnet mode requires strong secret material)
- `XMR_FROST_AUTH_BEARER` or (`XMR_FROST_AUTH_USERNAME` + `XMR_FROST_AUTH_PASSWORD`) (required)
- `XMR_FROST_MAX_SIGN_SCAN_LAG_BLOCKS` (optional; default `3`)

`signer_endpoint` is required for the Monero FROST signer path.

Bifrost endpoint policy:

- non-loopback signer endpoints must use `https://`,
- non-loopback signer endpoints must include auth.

Sidecar policy:

- protected `/v1/monero/*` routes always require auth (no unauthenticated mode).

## Hard Cutover Contract

The XMR sidecar storage model is strict and breaking:

- Legacy runtime compatibility paths are removed.
- Runtime migration of persisted sidecar data is unsupported.
- Existing pre-cutover sidecar runtime data is unsupported.
- Operators must start from a clean sidecar data directory for this cutover.

## Signing Model

Signing is a 3-step FROST flow coordinated by Bifrost over libp2p:

1. `POST /v1/monero/sign/prepare`
2. `POST /v1/monero/sign/partial`
3. `POST /v1/monero/sign/combine`

The sidecar does not expose a unified multi-peer signing endpoint.

FROST DKG (keygen) is also coordinated by Bifrost over libp2p. Each node calls only
its local sidecar round endpoints while exchanging round payloads over p2p:

1. `POST /v1/monero/dkg/round1`
2. `POST /v1/monero/dkg/round2`
3. `POST /v1/monero/dkg/round3`
4. `POST /v1/monero/dkg/finalize`

## Endpoints

### `GET /v1/monero/info`

Returns the authenticated compatibility contract. `network` is the configured THOR
deployment identity. `monero_nettype` and `monero_genesis_hash` must be queried from
the connected daemon rather than copied from the deployment label.

```json
{
  "protocol": "thorchain-xmr-frost",
  "api_major": 2,
  "software_version": "3.21.0",
  "git_commit": "0123456789abcdef0123456789abcdef01234567",
  "network": "stagenet",
  "monero_nettype": "stagenet",
  "monero_genesis_hash": "76ee3cc98646292206cd3e86f74d88b4dcc1d937088645e9b0cbca84b7ce74eb",
  "state_schema": 4,
  "capabilities": [
    "atomic-outgoing-v2",
    "balance-reconciliation-v1",
    "bound-recovery-envelope-v2",
    "per-vault-key-status-v1",
    "restart-reservation-sweep-v1",
    "dead-outgoing-quarantine-v1",
    "scanner-continuity-v1",
    "durable-inbound-replay-v1",
    "scanner-bounded-rewind-v1",
    "share-issued-plan-quarantine-v1",
    "verified-absence-balance-quarantine-v1",
    "canonical-outgoing-observation-v1",
    "canonical-tx-location-v1",
    "monero-network-identity-v1"
  ]
}
```

Bifrost rejects a missing capability, unsupported nettype, malformed hash, or mismatch
against the canonical identity selected by `chain_network`.

### `POST /v1/monero/dkg/round1`

Request:

```json
{
  "vault_pub_key": "thorpub1...",
  "threshold": 2,
  "n": 3,
  "participant_index": 1,
  "context": "thorchain:XMR:...",
  "session_id": "dkg:..."
}
```

Response:

```json
{
  "commitments": "..."
}
```

### `POST /v1/monero/dkg/round2`

Request:

```json
{
  "session_id": "dkg:...",
  "all_commitments": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "encrypted_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

### `POST /v1/monero/dkg/round3`

Request:

```json
{
  "session_id": "dkg:...",
  "all_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "address": "4...",
  "group_key": "..."
}
```

Successful round3 completion stages finalize payload only. Vault registration and key-share
persistence happen on explicit `dkg/finalize`.

### `POST /v1/monero/dkg/finalize`

Request:

```json
{
  "session_id": "dkg:...",
  "group_key": "...",
  "address": "4..."
}
```

Response:

```json
{
  "success": true
}
```

### `POST /v1/monero/sign/prepare`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "vault_pub_key": "thorpub1...",
  "to_address": "4...",
  "amount_atomic": 1000000000000,
  "memo": "OUT:...",
  "fee_rate": 0,
  "decoy_context": "<deterministic-context>"
}
```

Response:

```json
{
  "session_id": "...",
  "preprocess": "..."
}
```

### `POST /v1/monero/sign/partial`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "session_id": "...",
  "all_preprocesses": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "signature_share": "..."
}
```

### `POST /v1/monero/sign/combine`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "session_id": "...",
  "all_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "tx_hash": "...",
  "tx_hex": "...",
  "fee_atomic": 120000000
}
```

### `POST /v1/monero/broadcast`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "tx_hex": "...",
  "vault_pub_key": "thorpub1...",
  "tx_hash": "..."
}
```

Response:

```json
{
  "tx_hash": "..."
}
```

`msg_id` and `vault_pub_key` are required for broadcast, and `msg_id` is required for sign endpoints. `msg_id` is used as the idempotency key.
Replay matching uses canonical payload hashing only.
`sign/prepare` additionally enforces scanner freshness and returns `503` when vault scan height lags behind chain tip beyond configured bounds.

### Scanner/Read endpoints

- `GET /v1/monero/balance/{vault_pub_key}`
- `POST /v1/monero/scanner/sync`
- `GET /v1/monero/transfers?height={h}`
- `GET /v1/monero/transfers?mempool=true`
- `GET /v1/monero/addresses/{vault_pub_key}`
- `GET /v1/monero/outputs/{vault_pub_key}`
- `GET /v1/monero/fee-rate`
- `GET /v1/monero/chain/height`
- `GET /v1/monero/chain/block/{height}`
- `GET /v1/monero/chain/tx/{tx_hash}/exists`

Chain-query endpoint status semantics:

- `GET /v1/monero/chain/height`: `200`, `503`.
- `GET /v1/monero/chain/block/{height}`: `200`, `409` (height above tip), `503`.
- `GET /v1/monero/chain/tx/{tx_hash}/exists`: `200`, `400` (invalid hash), `503`, `500` (ambiguous daemon response, fail-closed).

`GET /v1/monero/outputs/{vault_pub_key}` supports optional lifecycle views:

- `view=spendable` (default): returns spendable outputs only (`state=unspent`).
- `view=all`: returns all tracked outputs (`state=unspent|reserved|spent`).

Example:

```text
GET /v1/monero/outputs/{vault_pub_key}?view=all
```

`GET /v1/monero/transfers` entries may include optional outbound attribution fields:

```json
{
  "from_address": "4...",
  "vault_pub_key": "thorpub1..."
}
```

`to_address` is strictly enforced as a wallet-compatible standard/subaddress Monero address.
Featured destinations are rejected.

`POST /v1/monero/scanner/sync` is the canonical Bifrost-driven block scan API. Bifrost sends
the requested height, tip height, canonical block hash/parent hash, and reorg window. The sidecar
applies deterministic rollback/rescan for that exact height and returns `TransfersResponse`
(`incoming`/`outgoing`) for the synced height.

`POST /v1/monero/scanner/sync` status semantics:

- `200`: success
- `400`: malformed request (for example missing `block_hash`/`parent_hash`)
- `409`: canonical hash mismatch at requested height
- `503`: daemon/RPC unavailable
- `500`: internal persistence/scanner failure

Scanner confirmation/unlock math uses the sidecar-validated synced height (`height` and canonical
header) and ignores caller-provided `tip_height` for security hardening.

`GET /v1/monero/transfers` remains a read-only diagnostic query endpoint and is not the canonical
scan driver.

`GET /v1/monero/transfers?mempool=true` semantics:

- Returns `incoming: []` (inbound mempool attribution is intentionally unsupported for Monero).
- Returns only `outgoing` entries currently in daemon mempool, filtered by known outgoing tx records.

### Health

- `GET /health`

## Response style

Sidecar endpoints return direct JSON payloads. Bifrost client decoding also accepts an envelope
shape for compatibility with older deployments:

```json
{
  "result": { "...": "..." },
  "error": "",
  "message": ""
}
```
