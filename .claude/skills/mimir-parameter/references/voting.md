# Node Mimir Voting

There is no admin mimir on develop. The only way a mimir value changes at runtime
is an active node's `MsgMimir` vote. A few store migrations and
`router_upgrade_controller.go` also call `DeleteMimir`. The CLI help text
"updates a mimir attribute (admin only)" (x/thorchain/client/cli/tx.go:120) is
stale.

## Flow of one vote (x/thorchain/handler_mimir.go)

1. Ante handler: `MimirAnteHandler` calls `validateMimirAuth`, which calls
   `activeNodeAccountsSignerPriority`. The signer must be an active node.
2. `validate` (line 51):
   - `ValidateBasic`: the key must be non-empty and the signer present. Negative
     values are allowed.
   - The key must match `constants.MimirKeyRegex` (`^[a-zA-Z0-9-]+$`) and be at
     most `constants.MaxMimirLength` (128) characters.
   - `REVSHARE-<thorname>`: the thorname must exist (after `revShareAliasToThorname`
     maps a mimir-safe alias back to the real name), and the value must be between
     0 and `constants.RevShareMaxBps` (5000).
   - Keys ending in `SLIPMINBPS` must have a value of at most the `SlipMinBpsMax`
     config (100).
3. `handle` (line 104):
   - Cost and record. The handler charges `GetNativeTxFee` (capped at the node's
     bond), moves it from bond to reserve, and calls
     `SetNodeMimir(ctx, key, value, signer)`. It emits `set_node_mimir` and `bond`
     events. This happens even when the vote is a duplicate. A negative value
     removes that node's vote (keeper_mimir.go:42).
   - Early exits. The handler returns if `msg.Value == current`. It then computes
     the effective value and returns if that value is `< 0`, equal to the current
     value, or `!= msg.Value`.
   - Only then does it call `SetMimir(ctx, key, effective)`, bump the
     `thornode_mimir_changed` counter, and emit `set_mimir`.

Consequences:

- Votes can set or change a mimir, but they can never unset one. The effective
  value is never negative.
- Consensus is evaluated only when a vote arrives. If the active set shrinks so
  that existing votes now form a supermajority, nothing changes until some node
  re-votes the winning value. The handler comment says this explicitly.
- Each vote costs the node bond.

## Operational vs economic

`IsOperationalMimir(key)` is in x/thorchain/keeper/v1/keeper_mimir.go:108.

| Rule                           | Keys                                                                                                                                                                                                              |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| exact match (case-insensitive) | MintSynths, TradeAccountsEnabled, RUNEPoolEnabled, EVMDisableContractWhitelist, MaxOutboundAttempts, AdvSwapQueueRapidSwapMax, EnableADVSwapQueue, StreamingLimitSwapMaxAge, plus several constants listed inline |
| exact unmatch (economic)       | NodePauseChainBlocks, PauseOnSlashThreshold                                                                                                                                                                       |
| prefix                         | COMPROMISEDVAULT-, ALLOWNOVAULT-, XMRCONSOLIDATION, L1DYNAMICFEE, OVERSOLVENCYTOTREASURYBPS-                                                                                                                      |
| contains                       | HALT, PAUSE, STOPSOLVENCYCHECK, MIMIRUPGRADECONTRACT, EVMALLOWANCECHECK, POLRESERVEBLACKLIST, POLRESERVEWHITELIST, DYNAMICFEE-WHITELIST, REVSHARE, EVMDIRECTERC20INBOUND, SKIPSIGNERCACHE                         |
| suffix                         | SLIPMINBPS (but not SlipMinBpsMax)                                                                                                                                                                                |

Read the function itself before answering for a specific key. The lists change.

| Class       | Consensus                                                                                          | Code                                                |
| ----------- | -------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| Operational | at least `OperationalVotesMin` (default 3) active votes for the most-voted value; a tie returns -1 | `NodeMimirs.ValueOfOperational` (type_mimir.go:157) |
| Economic    | at least 2/3 (rounded up) of active nodes vote one value (`types.HasSuperMajority`)                | `NodeMimirs.HasSuperMajority` (type_mimir.go:98)    |

`ValueOfEconomic` (type_mimir.go:113) exists but is only used in tests.

Operational votes are purged at every churn: `PurgeOperationalNodeMimirs`
(called from manager_validator_current.go:690). The set value stays. Only the
votes go, so the next change needs fresh votes. Economic votes persist across
churns.

Because `HALT` and `PAUSE` match as substrings, any new key containing them is
operational automatically. Name new economic keys carefully.

## Reading values

| Call                                       | Returns                                                                  |
| ------------------------------------------ | ------------------------------------------------------------------------ |
| `k.GetMimir(ctx, key) (int64, error)`      | -1 if unset. Keys are upper-cased before storage.                        |
| `k.GetMimirWithRef(ctx, template, ref...)` | `GetMimir(fmt.Sprintf(template, ref...))`                                |
| `k.GetConfigInt64(ctx, constants.X)`       | the mimir value if >= 0, else the constant default (keeper_config.go:14) |

- Mainnet, stagenet and chainnet builds use keeper_mimir_mainnet.go.
- The mocknet build (`keeper_mimir_mocknet.go`) falls back to the env var
  `CamelToSnakeUpper(key)` with `-` replaced by `_` when the store has no value.
- `MimirCache` (x/thorchain/keeper/mimir_cache.go) is disabled for the rest of the
  context on any `SetMimir`/`DeleteMimir`.

## API

| Endpoint                          | Shows                                                                                                                                       |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `/thorchain/mimir`                | every set value (store iteration, negatives skipped)                                                                                        |
| `/thorchain/mimir/key/{key}`      | `GetMimir`: -1 when unset, not the constant default                                                                                         |
| `/thorchain/mimir/admin`          | legacy name; iterates the same mimir store, not admin-only values                                                                           |
| `/thorchain/mimir/nodes_all`      | raw votes per node                                                                                                                          |
| `/thorchain/mimir/nodes`          | only keys whose votes currently have a supermajority. Operational values reached by minimum votes do not appear. Missing from openapi.yaml. |
| `/thorchain/mimir/node/{address}` | one node's votes                                                                                                                            |
| `/thorchain/constants`            | constant defaults                                                                                                                           |

Handlers: x/thorchain/querier.go:3399-3546. Routes: proto/thorchain/v1/types/query.proto:223-248.
