# Instant Observations

Instant observations allow THORChain to acknowledge outbound transactions immediately after TSS signing, rather than waiting for the transaction to be confirmed on-chain through normal block scanning.

## How It Works

1. **SignTx** returns a `*types.TxInItem` as its 3rd return value (the "observation")
2. The **signer** (`bifrost/signer/sign.go`) calls `shouldAutoObserve()` — the `AutoObserve` config flag is only one of the ways it returns true
3. If it returns true, it calls `observer.ObserveSigned()` with the observation wrapped in a `TxIn` marked `AllowFutureObservation: true`
4. The **attestation gossip layer** distributes the observation to other nodes
5. **Quorum** is unchanged: the attestation supermajority is always counted against the active validator set, even for instant observations

## SignTx Return Value

The 3rd return value of `SignTx` must be a `*types.TxInItem` containing:

```go
observation := &types.TxInItem{
    BlockHeight: height,
    Tx:          txID,           // Transaction hash/ID
    Memo:        memo,           // The outbound memo
    Sender:      fromAddress,    // Vault address (sender)
    To:          toAddress,      // Destination address
    Coins: common.Coins{
        common.NewCoin(asset, amount),
    },
    Gas: common.Gas{
        common.NewCoin(gasAsset, gasAmount),
    },
    ObservedVaultPubKey: vaultPubKey,
}
```

The gas amount in the instant observation is an estimate — it will be corrected when the normal block scanner re-observes the transaction with the actual gas used.

## Signer Flow

From `bifrost/signer/sign.go` (lines ~1348-1358):

```go
if s.shouldAutoObserve(item.TxOutItem) && obs != nil {
    s.observer.ObserveSigned(types.TxIn{
        Chain:                  item.TxOutItem.Chain,
        TxArray:                []*types.TxInItem{obs},
        MemPool:                true,
        Filtered:               true,
        ConfirmationRequired:   0,
        AllowFutureObservation: true,
    })
}
```

`shouldAutoObserve` (lines ~262-274) is not just the config flag — a chain can _require_ instant
observations:

```go
func (s *Signer) shouldAutoObserve(tx types.TxOutItem) bool {
    if tx.Chain.Equals(common.XMRChain) {
        return true // scanner sees only the change output; it cannot attribute the outbound
    }
    // memoless outbounds carry no on-chain memo, so no scanner observation can
    // reconstruct the inbound linkage
    return s.cfg.Signer.AutoObserve || (tx.Memo == "" && tx.OriginalMemo != "")
}
```

So if your chain broadcasts outbounds without the canonical memo, `SignTx` **must** return a usable
observation regardless of how `AutoObserve` is configured.

## AllowFutureObservation Flag

The `AllowFutureObservation` field on `TxIn` (defined in `bifrost/thorclient/types/tx_in.go`) marks
an observation produced by a signing-party member immediately after signing — before the scanner has
seen the transaction, and with gas that will need a later re-observation to correct.

**It does not change quorum.** The attestation supermajority is always counted against the active
validator set. `bifrost/observer/attestation_handlers_impl.go` says so explicitly:

```go
// Observation quorum is independent of the keysign committee. Even future
// outbound observations must match thornode's active-validator quorum check.
attestationCount := state.AttestationCount()
total := s.activeValidatorCount()
```

What the flag actually does:

1. **Attestation keying**: it is a field of `txKey` (`bifrost/observer/attestation_gossip.go`), so the
   instant observation and the later scanner observation of the same transaction accumulate
   attestations under separate keys rather than colliding. The durable on-deck key does the same —
   `createObserverTxKey` in `bifrost/observer/observer_storage.go` appends `:future`.

2. **Signed-tx cache gating**: `Observer.ObserveSigned` (`bifrost/observer/observe.go`) adds the txids
   to `signedTxOutCache` only when the flag is `false`. With it `true`, the later mempool/block
   observation is deliberately _not_ suppressed — that re-observation is what corrects the gas.

3. **Forwarded to thornode**: carried through on `common.QuorumTx.AllowFutureObservation` when the
   attestations are submitted.

## Confirmation Count Strategy

Each chain must implement `GetConfirmationCount()` and `ConfirmationCountReady()` based on its finality model:

### Instant Finality Chains (return 0)

These chains have deterministic finality — once a block is committed, it cannot be reverted:

- **Solana**: Uses "finalized" commitment level
- **Cosmos** (GAIA, NOBLE): Tendermint/CometBFT consensus
- **XRP**: XRPL consensus
- **AVAX**: Avalanche consensus (Snow protocol)

### Probabilistic Finality Chains (return N)

These chains require multiple block confirmations for safety:

- **ETH**: ~2 blocks (post-merge, but still uses confirmation count)
- **BSC**: 3 blocks
- **POL** (Polygon): 15 blocks
- **BASE**: ~12 blocks (L1 finalization)
- **TRON**: 1 confirmation + 19 finality blocks

### Implementation Pattern

```go
func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
    // For instant finality:
    return 0

    // For probabilistic finality:
    return requiredConfirmations
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
    // For instant finality:
    return true

    // For probabilistic finality:
    currentHeight, _ := c.GetHeight()
    return currentHeight - txIn.BlockHeight >= requiredConfirmations
}
```

## Key Files

| File                                            | Purpose                                      |
| ----------------------------------------------- | -------------------------------------------- |
| `bifrost/signer/sign.go`                        | Initiates instant observations after signing |
| `bifrost/observer/observe.go`                   | `ObserveSigned` handler                      |
| `bifrost/observer/attestation_handlers_impl.go` | Quorum validation and supermajority logic    |
| `bifrost/observer/attestation_gossip.go`        | P2P gossip distribution                      |
| `bifrost/thorclient/types/tx_in.go`             | `TxIn` struct with `AllowFutureObservation`  |
| `config/config.go`                              | `AutoObserve` flag definition                |

## Testing Instant Observations

In unit tests, verify that `SignTx` returns a correct `*TxInItem`:

```go
func (s *MySuite) TestSignTxReturnsObservation(c *C) {
    txOutItem := types.TxOutItem{
        Chain:       common.MYCHAINChain,
        ToAddress:   common.Address("destination"),
        VaultPubKey: vaultPubKey,
        Coins:       common.Coins{common.NewCoin(common.MYCHAINAsset, cosmos.NewUint(100))},
        Memo:        "OUT:txhash",
        GasRate:     10,
    }

    signed, _, obs, err := client.SignTx(txOutItem, 100)
    c.Assert(err, IsNil)
    c.Assert(signed, NotNil)
    c.Assert(obs, NotNil)
    c.Assert(obs.Tx, Not(Equals), "")
    c.Assert(obs.Memo, Equals, txOutItem.Memo)
    c.Assert(obs.To, Equals, txOutItem.ToAddress.String())
}
```
