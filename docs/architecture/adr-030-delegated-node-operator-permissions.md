# ADR 030: Delegated Node Operator Permissions

## Changelog

- 20/05/2026: Created
- 27/05/2026: Add optional per-delegate expiry (block offset) — see Detailed Design
- 13/07/2026: Update ADR number to 030

## Status

Proposed

## Context

A THORChain node has a single operator address, stored as `NodeAccount.BondAddress`. This address is responsible for all node-management actions that originate off-node and arrive through the deposit/memo pipeline:

- `MAINT` — toggle maintenance mode ([handler_maint.go](../../x/thorchain/handler_maint.go))
- `LEAVE` — request to churn out ([handler_leave.go](../../x/thorchain/handler_leave.go))
- `BOND` — set the operator fee and whitelist bond providers ([handler_bond.go](../../x/thorchain/handler_bond.go))
- `UNBOND` — unbond and remove bond providers ([handler_unbond.go](../../x/thorchain/handler_unbond.go))
- `OPERATOR_ROTATE` — rotate the operator address itself ([handler_operator_rotate.go](../../x/thorchain/handler_operator_rotate.go))

Every one of these handlers authorizes the same way — a single equality check against `BondAddress`, for example in `handler_maint.go`:

```go
if !bondAddr.Equals(signer) {
    return cosmos.ErrUnauthorized(...)
}
```

The problem is that `BondAddress` is **overloaded**. It is simultaneously:

1. the **authorization key** for all of the above management commands, and
2. the **custody address** that holds the operator's bonded RUNE and receives operator-fee rewards.

This conflation is poor opsec, and it is a particular barrier for institutional node operators (companies running nodes professionally). A company would like to keep the high-value custody key in cold storage / multisig and hand routine operational duties — toggling maintenance, requesting churn-out, managing the bond-provider whitelist — to a separate, lower-privilege hot wallet operated by its on-call staff. Today that is impossible: any key that can manage the node necessarily also has custody of the bond.

There is currently no delegation concept in the protocol. The bond-provider list is the nearest analogue, but it expresses _fund ownership_, not _management authority_.

This ADR proposes a native, per-node delegation registry that lets the operator grant a subset (or all) of its management permissions to one or more delegate addresses, without surrendering custody.

## Alternative Approaches

### 1. Cosmos SDK `x/authz` module

THORChain is Cosmos-SDK based, and the stock `authz` module is purpose-built for msg-level grants. It was rejected because THORChain's operator commands do not flow through the normal msg-service router — they arrive as `MsgDeposit` transactions carrying a memo, and the resulting `Msg…` types (`MsgMaint`, `MsgLeave`, etc.) are constructed internally by the memo handler rather than submitted directly by external signers. Making `authz` work would require additionally exposing these messages as directly-routable, broadening the external attack surface and diverging from THORChain's established memo-driven UX. A native registry fits the existing model with far less disruption.

### 2. Reuse `OPERATOR_ROTATE` to point at a hot wallet

The operator could simply rotate `BondAddress` to a hot wallet. Rejected: this does not separate authorization from custody — it just moves both to a hotter key, making the opsec posture strictly worse. Rotation is also gated (no active nodes, first half of churn interval) and so is unsuitable for routine delegation.

### 3. Multisig operator address

An operator could use a multisig account as `BondAddress`. Rejected as a _substitute_ for this feature: multisig raises the bar for compromise but still requires every routine action to be co-signed by custody-key holders, which is exactly the operational friction institutions want to avoid. Multisig and delegation are complementary — an operator may use a multisig custody key _and_ delegate routine ops to a hot wallet.

## Decision

Introduce a native **per-node delegation registry**. The operator (`BondAddress`) may register delegate addresses, each carrying a permission bitmask, via a new dedicated memo. Affected handlers authorize against `BondAddress` **or** a delegate holding the relevant permission bit.

Key decisions:

- **Granularity.** Permissions are a bitmask so any subset can be delegated. Proposed bits: `MAINT`, `LEAVE`, `WHITELIST_BP` (manage bond-provider whitelist), `SET_FEE` (set operator fee).
- **Optional expiry.** Each delegation may carry a **block-offset expiry**. The operator submits an offset; the handler stores the absolute block height (`current_block + offset`). Past that height the delegate is treated as unauthorized. An offset of `0` means **no expiry** (delegation persists until explicitly revoked). This bounds the blast radius of a compromised hot wallet without requiring the operator to remember to revoke.
- **Non-delegable commands.** `OPERATOR_ROTATE` (hands over the node) and managing the delegation registry itself are **never** delegable — both remain exclusive to `BondAddress`. This guarantees a delegate can never escalate to take over the node or grant further delegations.
- **`UNBOND` is not delegated** in the initial scope. It moves funds; even though refunds are paid to the bond-provider address (not the delegate), allowing a delegate to forcibly unbond is a denial-of-service vector. It can be revisited later behind its own permission bit.
- **Custody is never delegated.** Delegates gain _management authority only_. Bonded RUNE and operator-fee rewards continue to settle exclusively to `BondAddress` and the bond-provider list.

## Detailed Design

The design follows the existing memo-driven, `MsgDeposit`-routed pattern used by all current operator commands. It introduces one new keeper store, two new messages (delegation management + fund-free bond-provider management), two corresponding memos, and a single shared auth helper that replaces the ad-hoc `BondAddress` equality checks scattered across the affected handlers.

### User requirements

An institutional operator wants to:

1. Keep `BondAddress` (custody + bond rewards) in cold storage / multisig.
2. Authorize a hot wallet to perform a chosen subset of routine actions.
3. Revoke or change a delegate's permissions at any time, from the cold key.
4. Inspect current delegations via a query endpoint.
5. Have a global kill-switch so the protocol can disable delegation entirely if a vulnerability is discovered, without forcing every operator to act.
6. Time-box a delegation so it auto-expires after a chosen number of blocks (optional; `0` = no expiry).

### New data structures

A new keeper-stored type, keyed by node address. Proto definition (style mirrors [`proto/thorchain/v1/types/type_node_account.proto`](../../proto/thorchain/v1/types/type_node_account.proto)):

```protobuf
// proto/thorchain/v1/types/type_node_delegations.proto
syntax = "proto3";
package types;

option go_package = "gitlab.com/thorchain/thornode/v3/x/thorchain/types";

import "gogoproto/gogo.proto";
import "amino/amino.proto";

message NodeDelegations {
  bytes node_address = 1 [
    (gogoproto.casttype) = "github.com/cosmos/cosmos-sdk/types.AccAddress",
    (amino.encoding) = "bech32"
  ];
  repeated Delegate delegates = 2 [(gogoproto.nullable) = false];
}

message Delegate {
  bytes address = 1 [
    (gogoproto.casttype) = "github.com/cosmos/cosmos-sdk/types.AccAddress",
    (amino.encoding) = "bech32"
  ];
  uint64 permissions = 2; // bitmask, see PermXxx constants
  // Absolute block height at which this delegation expires. 0 = no expiry.
  // Computed at upsert time as ctx.BlockHeight() + block_offset from the
  // submitted memo, so state is deterministic and independent of when the
  // tx is read back.
  int64 expiry_block_height = 3;
}
```

Permission bits (defined as Go constants alongside `NodeDelegations`):

```go
const (
    PermMaint       uint64 = 1 << 0 // toggle MAINT
    PermLeave       uint64 = 1 << 1 // request LEAVE
    PermWhitelistBP uint64 = 1 << 2 // add/remove bond-provider whitelist entries
    PermSetFee      uint64 = 1 << 3 // set operator fee
    PermAll                = PermMaint | PermLeave | PermWhitelistBP | PermSetFee
)
```

Helper methods on `NodeDelegations` / `Delegate`:

```go
func (d *NodeDelegations) Get(addr cosmos.AccAddress) (Delegate, bool)
func (d *NodeDelegations) Set(addr cosmos.AccAddress, perms uint64, expiry int64) // upsert
func (d *NodeDelegations) Remove(addr cosmos.AccAddress) bool

// Has returns true only if the delegate holds `perm` AND has not expired
// at currentBlock. Expiry of 0 means never expires.
func (d *NodeDelegations) Has(addr cosmos.AccAddress, perm uint64, currentBlock int64) bool

func (d Delegate) IsExpired(currentBlock int64) bool {
    return d.ExpiryBlockHeight != 0 && currentBlock >= d.ExpiryBlockHeight
}
```

### Keeper API

Mirrors the existing `BondProviders` pattern in [`keeper_node_account.go`](../../x/thorchain/keeper/v1/keeper_node_account.go) (`prefixBondProviders` → `prefixNodeDelegations`):

```go
GetNodeDelegations(ctx, nodeAddr cosmos.AccAddress) (NodeDelegations, error)
SetNodeDelegations(ctx, NodeDelegations) error
DeleteNodeDelegations(ctx, nodeAddr cosmos.AccAddress)
IterateNodeDelegations(ctx, fn func(NodeDelegations) bool) error
```

Absence of a record is equivalent to "no delegates" — `GetNodeDelegations` returns a zero-value `NodeDelegations` without error, matching the convention used for `BondProviders`.

Genesis import/export adds a `node_delegations` field to `GenesisState`.

### New messages, memos, and handlers

#### 1. Manage delegate — `MsgManageDelegate`

Proto (style mirrors [`msg_maint.proto`](../../proto/thorchain/v1/types/msg_maint.proto)):

```protobuf
message MsgManageDelegate {
  bytes node_address      = 1 [(gogoproto.casttype) = "...AccAddress", (amino.encoding) = "bech32"];
  bytes delegate_address  = 2 [(gogoproto.casttype) = "...AccAddress", (amino.encoding) = "bech32"];
  uint64 permissions      = 3;
  // Number of blocks from the handling block at which this delegation
  // expires. 0 = no expiry. The handler computes the absolute expiry
  // height as ctx.BlockHeight() + block_offset and stores that on the
  // Delegate record.
  int64  block_offset     = 4;
  bytes signer            = 5 [(gogoproto.casttype) = "...AccAddress", (amino.encoding) = "bech32"];
}
```

Memo format:

```text
delegate:<node-address>:<delegate-address>:<permission-mask>[:<block-offset>]
```

The trailing block-offset field is optional and defaults to `0` (no expiry), preserving the shorter form for callers who don't want time-boxing.

Parser sketch (mirrors [`memo_maint.go`](../../x/thorchain/memo/memo_maint.go)):

```go
type DelegateMemo struct {
    MemoBase
    NodeAddress     cosmos.AccAddress
    DelegateAddress cosmos.AccAddress
    Permissions     uint64
    BlockOffset     int64 // 0 = no expiry
}

func (p *parser) ParseDelegateMemo() (DelegateMemo, error) {
    node     := p.getAccAddress(1, true, nil)
    delegate := p.getAccAddress(2, true, nil)
    perms    := p.getUint64(3, true, 0)
    offset   := p.getInt64(4, false, 0)
    return NewDelegateMemo(node, delegate, perms, offset), p.Error()
}
```

Handler `handler_manage_delegate.go` validation rules:

- `msg.Signer` **must equal** `nodeAccount.BondAddress.AccAddress()` — the cold key. **No delegate may invoke this handler**, even with `PermAll`.
- `msg.DelegateAddress` must not equal `BondAddress` (a delegate self-pointing to the operator is meaningless).
- `msg.DelegateAddress` must not be the `NodeAddress` (avoid the node's own consensus key being used as a delegate).
- `msg.Permissions == 0` removes the delegate; otherwise upserts.
- `msg.Permissions` must contain only bits in `PermAll` (unknown bits are rejected).
- `msg.BlockOffset` must be `>= 0`. A positive value sets `Delegate.ExpiryBlockHeight = ctx.BlockHeight() + msg.BlockOffset`; `0` clears any existing expiry (i.e. makes the delegation perpetual). Optional upper bound `MaxDelegationOffsetBlocks` (suggested ≈ 1 year of blocks) may be enforced via Mimir to prevent absurd values.

Handler effect: load `NodeDelegations`, apply `Set` (with computed absolute `expiry`) or `Remove`, save, emit event.

**Re-issuing the same delegate** is the renewal/extension path: an operator submits `delegate:node:hot:<perms>:<new-offset>` and the handler overwrites the existing entry, including its expiry. There is no separate "extend" memo.

#### 2. Manage bond provider (fund-free) — `MsgManageBondProvider`

This message exists because whitelisting today is a side effect of a _funded_ `BOND` tx — the operator must send RUNE for the whitelist mutation to take effect. A delegate cannot reasonably perform that without the operator's funds, so the `PermWhitelistBP` bit operates on a new fund-free path:

```protobuf
message MsgManageBondProvider {
  bytes node_address          = 1;
  bytes bond_provider_address = 2;
  Action action               = 3;   // ADD or REMOVE
  bytes signer                = 4;
  enum Action { ADD = 0; REMOVE = 1; }
}
```

Memo: `managebp:<node>:<bp-address>:add` / `managebp:<node>:<bp-address>:remove`

Handler validation:

- `canManageNode(ctx, k, nodeAddr, msg.Signer, PermWhitelistBP)` must return true.
- `ADD`: appends to `BondProviders.Providers` if not already present and `len(Providers) < MaxBondProviders`. Identical to the side effect path inside `handler_bond.go`.
- `REMOVE`: same constraints as the existing `BondProviders.Remove` — cannot remove index 0 (the operator), cannot remove a provider whose `Bond` is non-zero.

The funded `BOND` path in `handler_bond.go` is **unchanged**. Operators may still whitelist by sending a `BOND` tx as today; delegates simply have a second, fund-free path available.

### Authorization helper

A single shared helper replaces the ad-hoc `Equals(BondAddress)` checks. Placed alongside other auth helpers in [`helpers.go`](../../x/thorchain/helpers.go):

```go
// canManageNode returns nil if signer is authorized to perform an action
// requiring `requiredPerm` against nodeAddr.
//
// Authorized when EITHER:
//   - signer == nodeAccount.BondAddress (the operator's custody key), OR
//   - signer is a registered delegate of nodeAddr whose Permissions mask
//     includes requiredPerm AND whose expiry has not been reached
//     (ExpiryBlockHeight == 0 || ctx.BlockHeight() < ExpiryBlockHeight)
//     AND delegation has not been globally disabled by Mimir
//     (see DelegationDisabled).
func canManageNode(
    ctx cosmos.Context,
    k keeper.Keeper,
    nodeAddr cosmos.AccAddress,
    signer cosmos.AccAddress,
    requiredPerm uint64,
) error
```

Per-handler call sites (each replaces the existing `bondAddr.Equals(signer)` check):

| Handler                                   | Required permission |
| ----------------------------------------- | ------------------- |
| `handler_maint.go` :: `validateMaintAuth` | `PermMaint`         |
| `handler_leave.go` :: operator branch     | `PermLeave`         |
| `handler_bond.go` :: operator-fee branch  | `PermSetFee`        |
| `handler_manage_bond_provider.go` (new)   | `PermWhitelistBP`   |

`handler_leave.go` retains its existing **secondary** fallback for high-bond bond providers (`bond >= MinimumBondInRune`) — that path is unrelated to delegation and remains a distinct authorisation route.

`handler_unbond.go`, `handler_operator_rotate.go`, and `handler_manage_delegate.go` are **not** wired to `canManageNode`; they continue to require `BondAddress` exactly. This is the non-delegable set called out in the Decision section.

### Systems affected

- `x/thorchain/handler_maint.go`, `handler_leave.go`, `handler_bond.go` — auth call swapped to `canManageNode`.
- `x/thorchain/handler_manage_delegate.go` (new), `handler_manage_bond_provider.go` (new).
- `x/thorchain/memo/memo_delegate.go`, `memo_manage_bond_provider.go` (new) — plus parser dispatch in [`memo_parser.go`](../../x/thorchain/memo/memo_parser.go) and new `TxType` constants (`TxDelegate`, `TxManageBP`) in [`memo.go`](../../x/thorchain/memo/memo.go).
- `x/thorchain/keeper/v1/keeper_node_delegations.go` (new); `prefixNodeDelegations` added to the keeper key prefix list.
- `x/thorchain/types/type_node_delegations.go` and corresponding generated `.pb.go`.
- `x/thorchain/handler_operator_rotate.go` — on successful rotation, call `DeleteNodeDelegations(ctx, nodeAddr)`. A delegate registered for the prior operator must not carry over.
- `x/thorchain/genesis.go` (and the corresponding genesis test) — import/export delegation records.
- `x/thorchain/querier.go` and OpenAPI schema — new query endpoint (see below).
- Constant added in [`constants/constants.go`](../../constants/constants.go): `DelegationDisabled` Mimir kill-switch (default `0`, set to `1` to globally short-circuit delegate authorisation).

### Events

Emitted for observability and downstream indexers; styled to match the existing `toggle_maintenance` event in `handler_maint.go`:

- `set_delegate` — attributes: `node_address`, `delegate_address`, `permissions`, `expiry_block_height` (`0` if perpetual).
- `remove_delegate` — attributes: `node_address`, `delegate_address`.
- `manage_bond_provider` — attributes: `node_address`, `bond_provider_address`, `action`.

### Query endpoint

```text
GET /thorchain/node/{address}/delegations
```

Response:

```json
{
  "node_address": "thor1...",
  "current_block_height": 19384210,
  "delegates": [
    {
      "address": "thor1...",
      "permissions": 5,
      "permission_names": ["MAINT", "WHITELIST_BP"],
      "expiry_block_height": 19500000,
      "blocks_until_expiry": 115790,
      "expired": false
    },
    {
      "address": "thor1...",
      "permissions": 1,
      "permission_names": ["MAINT"],
      "expiry_block_height": 0,
      "blocks_until_expiry": null,
      "expired": false
    }
  ],
  "delegation_disabled": false
}
```

`permission_names`, `blocks_until_expiry`, and `expired` are convenience fields decoded for human readers; `permissions` and `expiry_block_height` remain canonical. `blocks_until_expiry` is `null` when `expiry_block_height == 0` (perpetual).

### Edge cases

- **Operator rotation.** `handler_operator_rotate.go` deletes all delegations for the node atomically with the rotation. Rationale: the new operator must explicitly opt back in to delegation; carry-over would silently re-authorise the previous operator's hot wallet.
- **Delegate is also a bond provider.** Permitted. The two roles are independent — being a bond provider does not grant management permissions, and being a delegate does not affect bond accounting.
- **Delegate is the operator of a different node.** Permitted. Authorisation is scoped per-node via the `NodeAddress` field in the registry.
- **Delegate address re-used across nodes by the same operator.** Permitted; the operator must register the delegate against each node separately. There is no notion of an "account-wide" delegate.
- **In-flight transaction at the moment of revocation.** Authorisation is evaluated at message-handling time against the current keeper state, so a tx that lands one block after a `delegate:…:0` revocation will be rejected. No race window beyond Tendermint's normal block ordering.
- **Expired delegate entries remain in state.** An expired `Delegate` is not auto-deleted; it simply fails the auth check. This keeps the handler logic O(1) (no end-block scans) and preserves an audit trail via the query endpoint. Operators may re-issue (renews the entry with a new expiry) or revoke (`permissions=0`) to clean up. If state growth ever becomes a concern, an end-block prune can be added later behind a Mimir without changing the auth semantics.
- **Renewal / extension.** Re-issuing `delegate:node:hot:<perms>:<offset>` overwrites the existing entry — there is no separate extend memo, and the new offset is always relative to the current block at handling time.
- **Offset of zero on an existing expiring delegate.** Resets the entry to perpetual. This is intentional: a single memo can both "extend" and "make permanent".
- **Global kill-switch.** If Mimir `DelegationDisabled = 1`, `canManageNode` ignores the registry entirely and reverts to `signer == BondAddress`. This is a defence-in-depth lever in case a delegation-specific vulnerability is discovered post-launch; it does not require operators to revoke individually.
- **Max delegates per node.** Capped at a small constant (suggested: `MaxDelegatesPerNode = 10`) to bound state size and discourage misuse. The `MsgManageDelegate` handler rejects upserts that would exceed the cap.

### Security considerations

- A delegate can only ever do what its bitmask permits; it can never touch custody, rotate the operator, manage the delegation registry, or perform `UNBOND`.
- `LEAVE` and `MAINT` are availability-affecting — a compromised delegate holding those bits can churn the node out or pull it offline, but cannot steal funds. Operators choose their own risk tradeoff via the bitmask.
- Removing a delegate is instant and cold-key-only, giving operators a clean revocation path on suspected compromise.
- Rotation invalidates stale delegations (see above) to avoid privilege carry-over.
- The `DelegationDisabled` Mimir provides a protocol-level emergency disable.
- Optional expiry bounds the worst-case exposure window of a compromised hot wallet — even if the operator never notices the compromise, the delegation self-terminates at the chosen block.
- The delegation registry is read on every authorisation check for the affected handlers; the read is a single keyed keeper lookup (O(1) by node address) plus a linear scan over a small bounded slice (≤ `MaxDelegatesPerNode`), so the added gas cost is negligible.

### Testing

Per-handler matrix for `MAINT`, `LEAVE` (operator branch), `BOND` (fee), and the new `MsgManageBondProvider`:

| Scenario                                                | Expected                   |
| ------------------------------------------------------- | -------------------------- |
| Signer == `BondAddress`                                 | ✅ allowed                 |
| Delegate with required bit set, no expiry               | ✅ allowed                 |
| Delegate with required bit set, expiry in future        | ✅ allowed                 |
| Delegate with required bit set, current block == expiry | ❌ unauthorized (boundary) |
| Delegate with required bit set, expiry in past          | ❌ unauthorized            |
| Delegate without required bit                           | ❌ unauthorized            |
| Delegate with unrelated bits only                       | ❌ unauthorized            |
| Delegate after `permissions=0` revocation               | ❌ unauthorized            |
| Random non-delegate, non-operator address               | ❌ unauthorized            |
| `DelegationDisabled = 1`, delegate with bit             | ❌ unauthorized            |
| `DelegationDisabled = 1`, `BondAddress`                 | ✅ allowed                 |

Registry handler (`MsgManageDelegate`) tests:

- Cold key (`BondAddress`) can add/update/remove.
- Delegate with `PermAll` cannot invoke `MsgManageDelegate` (no privilege escalation).
- `delegate == BondAddress` rejected.
- `delegate == NodeAddress` rejected.
- Unknown permission bits rejected.
- Exceeds `MaxDelegatesPerNode` → rejected.
- `permissions=0` removes the entry; idempotent if entry already absent.
- Negative `block_offset` rejected.
- Positive `block_offset` stored as absolute `ctx.BlockHeight() + offset`.
- Re-issue with a new `block_offset` overwrites the prior expiry (renewal path).
- Re-issue with `block_offset == 0` on an existing expiring entry resets to perpetual.
- (If enforced) `block_offset > MaxDelegationOffsetBlocks` rejected.

Cross-handler / integration tests:

- After `OPERATOR_ROTATE`, prior delegates are gone (subsequent `MAINT` from the old delegate fails).
- Delegate that is also a bond provider can still rebond/be unbonded as a provider, independent of delegation state.
- Genesis import/export round-trips an arbitrary registry without drift.
- Funded `BOND` whitelist path and fund-free `MANAGEBP` path produce identical `BondProviders` state.

### Release / migration

No state migration is required — absence of a `NodeDelegations` entry simply means "no delegates", and existing nodes behave exactly as before. The change is **additive and backwards-compatible**; it does not require a breaking release. No SDK coordination is needed.

The `DelegationDisabled` Mimir defaults to `0` (delegation enabled) on upgrade; protocol can flip it to `1` at any time as an emergency lever.

## Consequences

### Positive

- Institutional operators can separate custody from day-to-day operations, materially improving opsec.
- Fine-grained, revocable, per-node delegation with no impact on fund custody.
- Optional expiry caps the worst-case exposure of a forgotten or compromised hot wallet without requiring active revocation.
- Fully backwards-compatible; nodes that never register a delegate are unaffected.

### Negative

- Adds a new message type, memo, keeper store, and query — net increase in protocol surface area and audit scope.
- `LEAVE`/`MAINT` delegation introduces an availability risk if a delegate hot wallet is compromised; mitigated by, but not eliminated by, the bitmask.
- Whitelisting delegation requires a new fund-free memo, adding a second path for bond-provider management that must be kept consistent with the existing funded `BOND` path.

### Neutral

- The bond-provider list and the delegation registry now coexist as two distinct authority concepts (fund ownership vs. management authority); documentation must clearly distinguish them.
