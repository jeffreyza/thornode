# BFT Chain Clients

THORChain supports Cosmos SDK-based (BFT) chains via a shared `CosmosClient` implementation in `/bifrost/pkg/chainclients/gaia/cosmos_client.go`. Currently supported chains are Cosmos Hub (GAIA) and Noble. The same client handles both, differentiated by `ChainID` and `CosmosChainID` configuration. The client requires two endpoints on the node: a Cosmos SDK gRPC endpoint (`cosmos_grpc_host`, TLS via `cosmos_grpc_tls`) for transaction broadcast, transaction lookup, and account/balance queries; and a CometBFT RPC endpoint (`rpc_host`) for block scanning and mempool checks.

## Inbound Observations

Inbound transactions on BFT chains are observed by scanning finalized blocks over the CometBFT RPC endpoint (`Block` and `BlockResults`). A transaction is valid for observation if:

- It includes a `MsgSend` to an Asgard vault address
- It includes a valid memo matching THORChain memo structure

BFT chains do not support mempool observation. Only confirmed transactions can be processed.

See [Inbound Transactions](../bifrost/how-bifrost-works.md#observing-inbound-transactions).
For wallet developers, see [Sending Transactions](../concepts/sending-transactions.md#bft-chains).

## Confirmation Counting

BFT chains achieve deterministic finality, so transactions are treated as final after one block. No extended confirmation counting logic is applied.

See [Finality & Confirmation Counting](../bifrost/how-bifrost-works.md#finality--pre-confirmation).

## Gas Tracking

Gas is tracked per transaction based on actual gas usage and price reported on-chain. The fee is calculated as:

```go
Fee = gas_used * gas_price
```

Reported to THORChain in native asset units.

See [Gas Tracking](../bifrost/how-bifrost-works.md#gas-tracking).

## Vault Address Format

BFT chains use Bech32-encoded addresses. The vault address is derived from the chain-specific prefix and public key.

Example:

```go
sdk.AccAddress(pubKey.Address()).String() // Cosmos SDK
```

## Reorg Handling

Reorgs are extremely rare on BFT chains. No special reorg detection or `ErrataTx` logic is required.

## Other Considerations

- Only the base asset of the BFT chain is supported (e.g., ATOM for Cosmos Hub).
- Non-standard transaction types (e.g., smart contract calls) are not supported.
- Transactions must not contain multiple outputs or messages.

## Related Pages

- [Chain Clients Overview](./README.md)
- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Sending Transactions](../concepts/sending-transactions.md)
- [Memos](../concepts/memos.md)
