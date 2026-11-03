# Affiliate Revenue Share (REVSHARE) Implementation Plan

> **Collector upgrade:** The owner-based collector examples in this historical plan predate module migration 18 to 19. Collectors now use THORName keys. Migration moves a legacy balance to the owner's sole stored THORName if its spelling meets current registration rules, or pays the owner in RUNE otherwise. Module-owned legacy balances go to Reserve. See the [current collector and migration rules](../../affiliate-guide/thorname-guide.md#upgrade-from-shared-owner-collectors) when working on the current implementation.

> **Status (2026-05-04): Implemented.** The design landed via the
> branch documented in this plan; the shipped code is the authoritative
> reference. **One API shape differs from earlier drafts of this plan:**
> the keeper's bulk read is `GetAffiliateLiquidityFees(ctx)
([]AffiliateLiquidityFee, error)` returning a slice sorted
> lexicographically by `Thorname`, **not** a `map[string]cosmos.Uint`
> and **not** a `GetAffiliateLiquidityFeesIterator`. The slice form is
> consensus-critical: every node must iterate in the same order, so the
> determinism guarantee lives in the type rather than relying on
> per-call iteration discipline. References below have been updated to
> the slice form. If a future engineer reads this plan as a template,
> follow the slice shape; do **not** restore the map / iterator API.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-THORName revenue share of swap liquidity fees, gated by `REVSHARE-<thorname>` operational mimir (capped at 5000 bps), routed through the existing `AffiliateCollector`, deducted from end-of-block system income before all other splits.

**Architecture:** New per-block keeper accumulator `AffiliateLiquidityFees[thorname] -> Uint` is written during swap fee recording. At end-of-block in `UpdateNetwork()`, the accumulator is iterated: for each thorname a `rev_share` event is emitted, and if `REVSHARE-<thorname>` mimir is set, a payout (`bps × accrued / 10_000`) is transferred from `ReserveName` module to `AffiliateCollector` module and deducted from `systemIncome` before downstream splits run. The accumulator is cleared each block.

**Tech Stack:** Go 1.x, Cosmos SDK v0.50+, THORChain (thornode v3), semver-gated managers, Protobuf gogo proto, THORChain regression test framework.

**Spec:** `docs/superpowers/specs/2026-04-20-affiliate-revshare-design.md`

**Version gating:** All behavior changes land in `*_current.go` files (the current-version manager and swapper). Those ship atomically at the next release/hard fork, so no inline `if version.GTE(...)` guards are needed. If the team's release cadence requires an explicit gate, wrap the two behavior changes (attribution hook in `swap_current.go`, deduction loop in `manager_network_current.go`) in a `version.GTE(semver.MustParse("X.Y.Z"))` check using the version bump the lead uses for the next release. The additive parts (keeper state, event type, mimir validation, REVSHARE classification) are safe to deploy without a gate.

**Key context for the engineer:**

- Do **not** edit generated files (`*.pb.go`, `*.pulsar.go`, `openapi/gen`, `test/regression/mnt`). After editing `.proto` or OpenAPI files, run `make generate`.
- After editing any Go file, run `goimports -w <file>`.
- Build with `make build`. Unit tests with `make test`. Single test: `go test -tags=mocknet -parallel=1 -run TestName ./...`.
- For regression tests: `CI=1 EXPORT=1 DEBUG=1 make test-regression` (env vars go AFTER the command per user convention). Filter a suite with `RUN`.
- Commit messages follow the repo's style: `feat:`, `fix:`, `docs:` prefix; include a short subject and optional body. Use a HEREDOC for multi-line messages.
- Every task ends with a commit.

---

## File Map

- **Create:** `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go` — per-block accumulator keeper implementation.
- **Create:** `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee_test.go` — tests for the accumulator.
- **Create:** `test/regression/suites/affiliate-revshare.yaml` — end-to-end regression suite.
- **Modify:** `constants/constants.go` — add `RevShareMaxBps`.
- **Modify:** `constants/mimir_strings.go` — add `MimirTemplateRevShareThorName`.
- **Modify:** `x/thorchain/keeper/v1/keeper.go` — add `prefixAffiliateLiquidityFee`.
- **Modify:** `x/thorchain/keeper/keeper.go` — add `AffiliateLiquidityFee` struct and `KeeperAffiliateLiquidityFees` sub-interface; include in main `Keeper` interface.
- **Modify:** `x/thorchain/keeper/keeper_dummy.go` — add no-op stubs for `AddAffiliateLiquidityFee`, `GetAffiliateLiquidityFees`, `DeleteAffiliateLiquidityFees`.
- **Modify:** `proto/thorchain/v1/types/type_events.proto` — add `message EventRevShare`.
- **Modify:** `x/thorchain/types/type_event.go` — add `RevShareEventType`, `NewEventRevShare`, `Type()`, `Events()` methods.
- **Modify:** `x/thorchain/handler_mimir.go` — validate `REVSHARE-<thorname>` at set time.
- **Modify:** `x/thorchain/keeper/v1/keeper_mimir.go` — add `"REVSHARE"` to `IsOperationalMimir` `partialMatches`.
- **Modify:** `x/thorchain/swap_current.go` — resolve first affiliate from `tx.Memo` and call `AddAffiliateLiquidityFee` at the liquidity-fee recording site.
- **Modify:** `x/thorchain/manager_network_current.go` — insert REVSHARE deduction loop between `systemIncome` calculation and existing splits; emit events; clear accumulator.

---

## Task 1: Add Constants

**Files:**

- Modify: `constants/constants.go`
- Modify: `constants/mimir_strings.go`

- [ ] **Step 1: Add RevShareMaxBps to constants.go**

Open `constants/constants.go`. After the existing `MaxBasisPts` constant (around line 25), add:

```go
// RevShareMaxBps is the maximum basis points a single REVSHARE-<thorname>
// operational mimir may be set to (50% of any attributed liquidity fee).
const RevShareMaxBps = 5000
```

- [ ] **Step 2: Add mimir template to mimir_strings.go**

Open `constants/mimir_strings.go`. Inside the existing `const ( ... )` block, alongside other templates like `MimirTemplateMaxGas`, add:

```go
MimirTemplateRevShareThorName = "REVSHARE-%s" // Use with thorname (e.g., REVSHARE-foobar)
```

- [ ] **Step 3: Verify the package builds**

Run: `go build ./constants/...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add constants/constants.go constants/mimir_strings.go
git commit -m "$(cat <<'EOF'
feat(constants): add REVSHARE mimir template and max-bps cap

Introduces constants.RevShareMaxBps (5000) and
constants.MimirTemplateRevShareThorName ("REVSHARE-%s") for the upcoming
affiliate revenue share feature.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Declare DB Prefix and Keeper Interface

**Files:**

- Modify: `x/thorchain/keeper/v1/keeper.go` (prefix constants block near line 29)
- Modify: `x/thorchain/keeper/keeper.go` (main `Keeper` interface and sub-interfaces)
- Modify: `x/thorchain/keeper/keeper_dummy.go` (no-op stubs)

- [ ] **Step 1: Add prefix constant**

Open `x/thorchain/keeper/v1/keeper.go`. Inside the `const (` block that declares `prefixAffiliateCollector` etc. (around line 92), add a new line alongside:

```go
	prefixAffiliateLiquidityFee     types.DbPrefix = "affiliate_liquidity_fee/"
```

- [ ] **Step 2: Add struct and sub-interface to keeper.go**

Open `x/thorchain/keeper/keeper.go`. After the existing `KeeperLiquidityFees interface` block (around line 232), add a struct and a new sub-interface:

```go
// AffiliateLiquidityFee is a single accumulator entry returned by
// GetAffiliateLiquidityFees, sorted by Thorname.
type AffiliateLiquidityFee struct {
	Thorname string
	Fee      cosmos.Uint
}

type KeeperAffiliateLiquidityFees interface {
	AddAffiliateLiquidityFee(ctx cosmos.Context, thorname string, fee cosmos.Uint) error
	GetAffiliateLiquidityFees(ctx cosmos.Context) ([]AffiliateLiquidityFee, error)
	DeleteAffiliateLiquidityFees(ctx cosmos.Context)
}
```

The slice return type is consensus-critical: callers iterate in slice order to drive end-of-block payouts and event emission, so non-deterministic ordering would diverge state across nodes. Returning a sorted slice (rather than a map plus a comment about ordering) puts the guarantee in the type itself.

Then inside the main `Keeper interface` block (around line 16), add the sub-interface to the embedded list (e.g., right after `KeeperLiquidityFees`):

```go
	KeeperLiquidityFees
	KeeperAffiliateLiquidityFees
```

- [ ] **Step 3: Add dummy-keeper stubs**

Open `x/thorchain/keeper/keeper_dummy.go`. Near the other no-op methods (grouped by interface), append:

```go
func (k KVStoreDummy) AddAffiliateLiquidityFee(ctx cosmos.Context, thorname string, fee cosmos.Uint) error {
	return kaboom
}
func (k KVStoreDummy) GetAffiliateLiquidityFees(ctx cosmos.Context) ([]AffiliateLiquidityFee, error) {
	return nil, kaboom
}
func (k KVStoreDummy) DeleteAffiliateLiquidityFees(ctx cosmos.Context) {}
```

(`kaboom` is the existing sentinel error value used by other dummy methods — verify by reading the top of `keeper_dummy.go`. If its name differs, use the same sentinel used by nearby stubs.)

- [ ] **Step 4: Verify build**

Run: `go build ./x/thorchain/...`
Expected: no output, exit 0. (Compile will fail at this point because `KVStore` (v1) does not yet implement the interface — that's fine; we add the impl in Task 3. If the compile error is anything other than "KVStore does not implement KeeperAffiliateLiquidityFees", stop and investigate.)

- [ ] **Step 5: Do not commit yet**

Do not commit yet — the build intentionally fails at this point (`KVStore` does not implement `KeeperAffiliateLiquidityFees` until Task 3). Task 3 adds the v1 impl and Task 3 Step 6 commits all of Task 2 + Task 3 together.

---

## Task 3: Implement the Keeper With Tests (TDD)

**Files:**

- Create: `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go`
- Create: `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee_test.go`

- [ ] **Step 1: Write the failing test**

Create `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee_test.go` with:

```go
package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type KeeperAffiliateLiquidityFeeSuite struct{}

var _ = Suite(&KeeperAffiliateLiquidityFeeSuite{})

func (s *KeeperAffiliateLiquidityFeeSuite) TestAddGetDelete(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(100)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(50)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "bar", cosmos.NewUint(75)), IsNil)

	got, err := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 2)
	// Order is lexicographic by upper-cased Thorname (BAR before FOO).
	c.Assert(got[0].Thorname, Equals, "BAR")
	c.Assert(got[0].Fee.Equal(cosmos.NewUint(75)), Equals, true)
	c.Assert(got[1].Thorname, Equals, "FOO")
	c.Assert(got[1].Fee.Equal(cosmos.NewUint(150)), Equals, true)

	// Zero fee is a no-op.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.ZeroUint()), IsNil)
	got, err = k.GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(got[1].Fee.Equal(cosmos.NewUint(150)), Equals, true)

	k.DeleteAffiliateLiquidityFees(ctx)
	got, err = k.GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 0)
}
```

The test relies on the existing `setupKeeperForTest` helper used by other keeper tests in this directory (see `keeper_network_fee_test.go` for an example of the same setup pattern).

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test -tags=mocknet -run KeeperAffiliateLiquidityFeeSuite ./x/thorchain/keeper/v1/... -v`
Expected: FAIL — `AddAffiliateLiquidityFee` is not defined on `KVStore`.

- [ ] **Step 3: Implement keeper_affiliate_liquidity_fee.go**

Create `x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go`:

```go
package keeperv1

import (
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// AddAffiliateLiquidityFee accumulates the given rune-denominated liquidity
// fee against the supplied thorname for the current block. Called from the
// swap path when the swap's first memo affiliate resolves to an active
// THORName. The accumulator is cleared at end-of-block by
// DeleteAffiliateLiquidityFees after rev-share payouts are processed.
func (k KVStore) AddAffiliateLiquidityFee(ctx cosmos.Context, thorname string, fee cosmos.Uint) error {
	if fee.IsZero() {
		return nil
	}
	key := k.GetKey(prefixAffiliateLiquidityFee, thorname)
	var current uint64
	if _, err := k.getUint64(ctx, key, &current); err != nil {
		return err
	}
	k.setUint64(ctx, key, current+fee.Uint64())
	return nil
}

// GetAffiliateLiquidityFees returns all per-block accumulator entries,
// sorted lexicographically by Thorname (upper-cased). The lex ordering
// inherits from the IAVL store iteration and is preserved explicitly so
// callers in consensus paths can rely on it without re-sorting. Returns
// an empty (non-nil) slice when the accumulator is empty.
//
// A per-entry decode failure is logged and the entry is skipped rather
// than aborting the whole iteration: the end-of-block drain in
// processRevShare unconditionally deletes the accumulator, so returning
// an error here would silently discard every sibling entry's
// attribution for the block.
func (k KVStore) GetAffiliateLiquidityFees(ctx cosmos.Context) ([]keeper.AffiliateLiquidityFee, error) {
	out := []keeper.AffiliateLiquidityFee{}
	iter := k.getIterator(ctx, prefixAffiliateLiquidityFee)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		rawKey := strings.TrimPrefix(string(iter.Key()), string(prefixAffiliateLiquidityFee)+"/")
		var v uint64
		if _, err := k.getUint64(ctx, iter.Key(), &v); err != nil {
			ctx.Logger().Error("fail to decode affiliate liquidity fee entry; skipping",
				"thorname", rawKey, "error", err)
			continue
		}
		out = append(out, keeper.AffiliateLiquidityFee{
			Thorname: rawKey,
			Fee:      cosmos.NewUint(v),
		})
	}
	return out, nil
}

// DeleteAffiliateLiquidityFees removes every accumulator entry. Called at
// end-of-block after REVSHARE payouts are made.
func (k KVStore) DeleteAffiliateLiquidityFees(ctx cosmos.Context) {
	iter := k.getIterator(ctx, prefixAffiliateLiquidityFee)
	defer iter.Close()
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	// Collect keys first; mutating the store while iterating is unsafe.
	var keys [][]byte
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	for _, key := range keys {
		store.Delete(key)
	}
}
```

- [ ] **Step 4: Run goimports and the test**

```bash
goimports -w x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee_test.go
go test -tags=mocknet -run KeeperAffiliateLiquidityFeeSuite ./x/thorchain/keeper/v1/... -v
```

Expected: PASS. Build of the full `x/thorchain` package now succeeds.

- [ ] **Step 5: Run the broader build to confirm nothing else broke**

Run: `go build ./x/thorchain/...`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee.go x/thorchain/keeper/v1/keeper_affiliate_liquidity_fee_test.go x/thorchain/keeper/v1/keeper.go x/thorchain/keeper/keeper.go x/thorchain/keeper/keeper_dummy.go
git commit -m "$(cat <<'EOF'
feat(thorchain): per-block affiliate liquidity fee accumulator

Adds KeeperAffiliateLiquidityFees keeper methods for a per-block
accumulator keyed by thorname. Written during swap fee recording in a
follow-up commit; cleared at end-of-block after REVSHARE payouts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `EventRevShare` (Proto + Go)

**Files:**

- Modify: `proto/thorchain/v1/types/type_events.proto`
- Modify: `x/thorchain/types/type_event.go`

- [ ] **Step 1: Add proto message**

Open `proto/thorchain/v1/types/type_events.proto`. After the existing `message EventAffiliateFee { ... }` block (around line 114), add:

```protobuf
message EventRevShare {
  string thorname = 1;
  bytes owner = 2 [
    (gogoproto.casttype) = "github.com/cosmos/cosmos-sdk/types.AccAddress"
  ];
  string accrued_fee = 3 [
    (gogoproto.customtype) = "cosmossdk.io/math.Uint",
    (gogoproto.nullable) = false
  ];
  int64 bps = 4;
  string payout = 5 [
    (gogoproto.customtype) = "cosmossdk.io/math.Uint",
    (gogoproto.nullable) = false
  ];
}
```

- [ ] **Step 2: Format the proto file**

Run: `make proto-format`
Expected: the file is reformatted in-place. Diff should look clean.

- [ ] **Step 3: Regenerate bindings**

Run: `make generate`
Expected: `proto/thorchain/v1/types/type_events.pb.go` is regenerated. Do not hand-edit the generated file.

- [ ] **Step 4: Add event type constant**

Open `x/thorchain/types/type_event.go`. In the top-level event-types `const ( ... )` block (line 15-66), add after `AffiliateFeeEventType`:

```go
	RevShareEventType               = "rev_share"
```

- [ ] **Step 5: Add constructor and methods**

Open `x/thorchain/types/type_event.go`. After the `NewEventAffiliateFee` / `Events()` block (around line 271), append:

```go
// NewEventRevShare creates a new rev_share event. accruedFee is the raw
// liquidity fee attributed to the thorname this block. bps is the
// REVSHARE-<thorname> mimir value applied (0 if unset). payout is the RUNE
// moved from reserve to AffiliateCollector (0 if no payout).
func NewEventRevShare(thorname string, owner cosmos.AccAddress, accruedFee cosmos.Uint, bps int64, payout cosmos.Uint) *EventRevShare {
	return &EventRevShare{
		Thorname:   thorname,
		Owner:      owner,
		AccruedFee: accruedFee,
		Bps:        bps,
		Payout:     payout,
	}
}

func (m *EventRevShare) Type() string {
	return RevShareEventType
}

func (m *EventRevShare) Events() (cosmos.Events, error) {
	return cosmos.Events{
		cosmos.NewEvent(m.Type(),
			cosmos.NewAttribute("thorname", m.Thorname),
			cosmos.NewAttribute("owner", m.Owner.String()),
			cosmos.NewAttribute("accrued_fee", m.AccruedFee.String()),
			cosmos.NewAttribute("bps", strconv.FormatInt(m.Bps, 10)),
			cosmos.NewAttribute("payout", m.Payout.String()),
		),
	}, nil
}
```

- [ ] **Step 6: Format and build**

```bash
goimports -w x/thorchain/types/type_event.go
go build ./x/thorchain/...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add proto/thorchain/v1/types/type_events.proto proto/thorchain/v1/types/type_events.pb.go x/thorchain/types/type_event.go
git commit -m "$(cat <<'EOF'
feat(thorchain): add EventRevShare event type

Adds the rev_share event emitted at end-of-block for every thorname with
non-zero per-block fee accrual, including when REVSHARE is unset
(bps=0, payout=0). Consumed by off-chain indexers for gross
attribution and rev-share payout queries.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Mimir Validation and Operational Classification

**Files:**

- Modify: `x/thorchain/handler_mimir.go` (validate function, line 49-72)
- Modify: `x/thorchain/keeper/v1/keeper_mimir.go` (IsOperationalMimir, line 141-148)
- Modify: `x/thorchain/handler_mimir_test.go` (add tests — discover test file path if different)

- [ ] **Step 1: Write the failing test**

Open `x/thorchain/handler_mimir_test.go`. Add this test method to the existing `MimirSuite`:

```go
func (HandlerMimirSuite) TestRevShareValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	h := NewMimirHandler(mgr)

	// Create a thorname so the validation passes for existence checks.
	name := types.NewTHORName("foobar", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
	k.SetTHORName(ctx, name)

	signerAcc := GetRandomBech32Addr()
	mkMsg := func(key string, value int64) MsgMimir {
		return MsgMimir{Key: key, Value: value, Signer: signerAcc}
	}

	// 1. Non-existent thorname -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-nobody", 100)), NotNil)

	// 2. Existing thorname, valid value -> accept
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", 100)), IsNil)

	// 3. Value at cap -> accept
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", constants.RevShareMaxBps)), IsNil)

	// 4. Value over cap -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", constants.RevShareMaxBps+1)), NotNil)

	// 5. Negative value -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", -1)), NotNil)
}

func (HandlerMimirSuite) TestRevShareIsOperational(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_ = ctx
	c.Assert(mgr.Keeper().IsOperationalMimir("REVSHARE-foobar"), Equals, true)
	c.Assert(mgr.Keeper().IsOperationalMimir("revshare-foobar"), Equals, true) // case-insensitive
}
```

If test helper imports are missing (e.g., `constants`), add them at the top of the file.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test -tags=mocknet -parallel=1 -run 'MimirSuite|HandlerMimirSuite' ./x/thorchain/... -v`
Expected: FAIL on both new tests.

- [ ] **Step 3: Add validation in handler_mimir.go**

Open `x/thorchain/handler_mimir.go`. Inside the `validate` method, insert immediately before the final `return nil` on line 72 (after the min-slip block):

```go
	// REVSHARE-<thorname> operational mimir validation.
	if strings.HasPrefix(strings.ToUpper(msg.Key), "REVSHARE-") {
		thornameName := msg.Key[len("REVSHARE-"):]
		if !h.mgr.Keeper().THORNameExists(ctx, thornameName) {
			return cosmos.ErrUnknownRequest("REVSHARE mimir requires existing thorname")
		}
		if msg.Value < 0 || msg.Value > constants.RevShareMaxBps {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("REVSHARE value must be between 0 and %d bps", constants.RevShareMaxBps))
		}
	}
```

Ensure `"fmt"` and `"gitlab.com/thorchain/thornode/v3/constants"` are imported (the file already imports both).

- [ ] **Step 4: Add REVSHARE to operational partialMatches**

Open `x/thorchain/keeper/v1/keeper_mimir.go`. Inside `IsOperationalMimir` (line 106), modify the `partialMatches` slice (line 141-148) to include `"REVSHARE"`:

```go
	partialMatches := []string{
		"HALT",
		"PAUSE",
		"STOPSOLVENCYCHECK",
		"MIMIRUPGRADECONTRACT",
		"EVMALLOWANCECHECK",
		"POLRESERVEBLACKLIST",
		"REVSHARE",
	}
```

- [ ] **Step 5: Run the tests and verify they pass**

```bash
goimports -w x/thorchain/handler_mimir.go x/thorchain/handler_mimir_test.go x/thorchain/keeper/v1/keeper_mimir.go
go test -tags=mocknet -parallel=1 -run 'MimirSuite|HandlerMimirSuite' ./x/thorchain/... -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add x/thorchain/handler_mimir.go x/thorchain/handler_mimir_test.go x/thorchain/keeper/v1/keeper_mimir.go
git commit -m "$(cat <<'EOF'
feat(thorchain): validate REVSHARE mimir and mark it operational

Rejects REVSHARE-<thorname> mimirs when the thorname does not exist or
when the value is outside [0, RevShareMaxBps]. Classifies REVSHARE keys
as operational so they can be purged by PurgeOperationalNodeMimirs at
end-of-block alongside HALT/PAUSE/etc.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Attribute Liquidity Fees to First Affiliate

**Files:**

- Modify: `x/thorchain/swap_current.go` (fee recording site at line 450, inside `Swap`)
- Modify: `x/thorchain/swap_current_test.go` (add attribution test)

- [ ] **Step 1: Write the failing test**

Open `x/thorchain/swap_current_test.go`. Find an existing swap-success test that produces a non-zero liquidity fee. At the top of that file, or alongside existing tests, add a new test that exercises the attribution hook. Use the smallest reproducible harness; adapt the setup pattern from a neighboring test:

```go
func (s *SwapSuite) TestAffiliateAttribution(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// set up a pool so a swap can produce a non-zero liquidity fee
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100*common.One)

	// set up a thorname that will receive attribution
	addr := GetRandomTHORAddress()
	accAddr, err := addr.AccAddress()
	c.Assert(err, IsNil)
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: addr}})
	name.Owner = accAddr
	mgr.Keeper().SetTHORName(ctx, name)

	// construct a swap tx with memo that names affx first
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"=:r:"+GetRandomRUNEAddress().String()+"::affx:100",
	)

	dest := GetRandomRUNEAddress()
	_, _, err = newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), dest, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr)
	c.Assert(err, IsNil)

	// accumulator should have an entry for AFFX with fee > 0
	got, err := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1, Commentf("expected accumulator to contain an attribution entry"))
	c.Assert(got[0].Thorname, Equals, "AFFX")
	c.Assert(got[0].Fee.IsZero(), Equals, false)
}
```

Adjust helpers (`FundModule`, `setupManagerForTest`, address helpers) to match those in neighboring tests. If `GetRandomRUNEAddress` is unavailable, use a thor address helper used elsewhere in the file.

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test -tags=mocknet -parallel=1 -run 'SwapSuite.TestAffiliateAttribution' ./x/thorchain/... -v`
Expected: FAIL (accumulator is empty because the hook is not yet wired).

- [ ] **Step 3: Wire attribution in swap_current.go**

Open `x/thorchain/swap_current.go`. Inside `SwapperImpl.Swap`, locate the block that records the liquidity fee (around line 448-453, the `if !pool.Asset.IsTCY()` block that calls `AddToLiquidityFees`). Right after the successful `AddToLiquidityFees` call, insert:

```go
				if attributeThorname, ok := resolveFirstAffiliateTHORName(ctx, keeper, tx.Memo); ok {
					if err := keeper.AddAffiliateLiquidityFee(ctx, attributeThorname, swapEvt.LiquidityFeeInRune); err != nil {
						ctx.Logger().Error("fail to attribute affiliate liquidity fee", "thorname", attributeThorname, "error", err)
						// non-fatal — attribution failure must not fail the swap
					}
				}
```

Then add a helper function at the bottom of `swap_current.go` (or in a new file `x/thorchain/helpers_affiliate_revshare.go` if preferred; keep it in `swap_current.go` for this plan):

```go
// resolveFirstAffiliateTHORName parses memo as a SwapMemo and returns the
// first affiliate name if it resolves to an existing, non-expired THORName.
// Returns ("", false) when the memo isn't a swap memo, has no affiliates,
// or the first affiliate is a raw address or an expired/missing thorname.
func resolveFirstAffiliateTHORName(ctx cosmos.Context, k keeper.Keeper, memoStr string) (string, bool) {
	parsed, err := ParseMemoWithTHORNames(ctx, k, memoStr)
	if err != nil {
		return "", false
	}
	sm, ok := parsed.(SwapMemo)
	if !ok {
		return "", false
	}
	affiliates := sm.GetAffiliates()
	if len(affiliates) == 0 {
		return "", false
	}
	first := affiliates[0]
	if !k.THORNameExists(ctx, first) {
		return "", false
	}
	// THORNameExists checks active-at-this-height; that's sufficient.
	return first, true
}
```

Note the import considerations: `swap_current.go` is in `package thorchain` and already has access to `ParseMemoWithTHORNames` (via `alias.go`) and `SwapMemo`. Ensure `keeper` is imported.

- [ ] **Step 4: Run the test and verify it passes**

```bash
goimports -w x/thorchain/swap_current.go x/thorchain/swap_current_test.go
go test -tags=mocknet -parallel=1 -run 'SwapSuite.TestAffiliateAttribution' ./x/thorchain/... -v
```

Expected: PASS.

- [ ] **Step 5: Run the full swap suite to catch regressions**

Run: `go test -tags=mocknet -parallel=1 -run SwapSuite ./x/thorchain/... -v`
Expected: PASS. If existing tests fail, investigate whether the new call fails silently in a memo context they rely on — the helper returns `("", false)` on any non-swap memo or missing thorname, so it should not affect existing tests. If a regression slips through, the most likely cause is the new helper being called with a nil keeper; verify the keeper is non-nil at the call site.

- [ ] **Step 6: Commit**

```bash
git add x/thorchain/swap_current.go x/thorchain/swap_current_test.go
git commit -m "$(cat <<'EOF'
feat(thorchain): attribute liquidity fees to first memo affiliate

Swapper.Swap now calls AddAffiliateLiquidityFee after recording a
non-zero liquidity fee, when the swap memo's first affiliate resolves
to an active THORName. Applies to every fee-recording path including
streaming sub-swaps (the handler re-parses the same tx memo each
tick).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: End-of-Block REVSHARE Deduction in `UpdateNetwork`

**Files:**

- Modify: `x/thorchain/manager_network_current.go` (calcBlockRewards, around line 1975)
- Modify: `x/thorchain/manager_network_current_test.go` (add deduction test)

- [ ] **Step 1: Write the failing test**

Open `x/thorchain/manager_network_current_test.go`. Add a new test in an appropriate suite (e.g., `NetworkManagerVCURSuite`):

```go
func (s *NetworkManagerVCURSuite) TestRevShareDeductionBeforeSplits(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	// Register a thorname with an owner.
	ownerAddr := GetRandomBech32Addr()
	thorAddr, err := common.NewAddress(ownerAddr.String())
	c.Assert(err, IsNil)
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// Set REVSHARE mimir to 2500 bps (25%).
	k.SetMimir(ctx, "REVSHARE-AFFX", 2500)

	// Seed accumulator with 1000 rune attributed to affx.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affx", cosmos.NewUint(1000)), IsNil)

	// Fund reserve with enough rune to cover the expected payout (>= 250).
	FundModule(c, ctx, k, ReserveName, 1000)

	// Snapshot balances.
	affcolBefore, _ := k.GetAffiliateCollector(ctx, ownerAddr)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	// Call the manager entry point that triggers revshare processing.
	// Use whichever NetworkMgr method drives the UpdateNetwork flow;
	// inspect the manager's Setup/EndBlock for the minimal invocation.
	netMgr := newNetworkMgrVCUR(k, mgr.EventMgr(), mgr.GasMgr(), mgr)
	c.Assert(netMgr.UpdateNetwork(ctx, mgr.GetConstants(), mgr.GasMgr(), mgr.EventMgr()), IsNil)

	// Expected payout = 1000 * 2500 / 10000 = 250.
	expectedPayout := cosmos.NewUint(250)

	affcolAfter, _ := k.GetAffiliateCollector(ctx, ownerAddr)
	c.Assert(affcolAfter.RuneAmount.Sub(affcolBefore.RuneAmount).Equal(expectedPayout), Equals, true,
		Commentf("affiliate collector delta != expected"))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveBefore.Sub(reserveAfter).GTE(expectedPayout), Equals, true,
		Commentf("reserve did not decrease by at least expected payout"))

	// Accumulator must be cleared.
	left, err := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(left, HasLen, 0)
}

func (s *NetworkManagerVCURSuite) TestRevShareEventEvenWhenNoMimirSet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ownerAddr := GetRandomBech32Addr()
	thorAddr, _ := common.NewAddress(ownerAddr.String())
	name := NewTHORName("affy", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// NO REVSHARE mimir set.

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affy", cosmos.NewUint(500)), IsNil)

	FundModule(c, ctx, k, ReserveName, 1000)

	netMgr := newNetworkMgrVCUR(k, mgr.EventMgr(), mgr.GasMgr(), mgr)
	c.Assert(netMgr.UpdateNetwork(ctx, mgr.GetConstants(), mgr.GasMgr(), mgr.EventMgr()), IsNil)

	// Accumulator cleared.
	left, err := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(left, HasLen, 0)

	// Affiliate collector unchanged — no mimir means no payout.
	col, _ := k.GetAffiliateCollector(ctx, ownerAddr)
	c.Assert(col.RuneAmount.IsZero(), Equals, true)

	// But a rev_share event should still have been emitted. Inspect
	// ctx.EventManager().Events() and assert one has type "rev_share".
	emitted := false
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == "rev_share" {
			emitted = true
			break
		}
	}
	c.Assert(emitted, Equals, true)
}
```

Adapt helper names (`newNetworkMgrVCUR`, `FundModule`) to match existing convention in the file.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test -tags=mocknet -parallel=1 -run 'NetworkManagerVCURSuite.TestRevShare' ./x/thorchain/... -v`
Expected: FAIL (deduction loop not yet implemented).

- [ ] **Step 3: Implement the REVSHARE deduction loop in calcBlockRewards**

Open `x/thorchain/manager_network_current.go`. Locate line 1975 (`systemIncome := blockReward.Add(totalLiquidityFees)`). Immediately after that line, and **before** any of the `GetSafeShare(..., systemIncome)` calls on lines 1980-1985, insert the new REVSHARE loop:

```go
	// --- REVSHARE: pay attributed share of liquidity fees to affiliate thornames ---
	// Deduct off the top so all subsequent splits work against the reduced base.
	// Emit a rev_share event for every thorname with non-zero accumulator,
	// regardless of whether REVSHARE mimir is set, so indexers can track
	// gross attribution.
	entries, err := vm.k.GetAffiliateLiquidityFees(ctx)
	if err != nil {
		ctx.Logger().Error("fail to read affiliate liquidity fees", "error", err)
		entries = nil
	}
	totalRevShare := cosmos.ZeroUint()
	// entries is sorted lex by Thorname; iterating in slice order is
	// deterministic across nodes.
	for _, entry := range entries {
		thornameName := entry.Thorname
		accruedFee := entry.Fee

		var ownerForEvent cosmos.AccAddress
		tn, tnErr := vm.k.GetTHORName(ctx, thornameName)
		if tnErr == nil {
			ownerForEvent = tn.Owner
		}
		bps, _ := vm.k.GetMimirWithRef(ctx, constants.MimirTemplateRevShareThorName, thornameName)
		payout := cosmos.ZeroUint()
		if tnErr == nil && bps > 0 {
			payout = common.GetSafeShare(cosmos.NewUint(uint64(bps)), cosmos.NewUint(10_000), accruedFee)
			if !payout.IsZero() {
				col, _ := vm.k.GetAffiliateCollector(ctx, tn.Owner)
				col.RuneAmount = col.RuneAmount.Add(payout)
				vm.k.SetAffiliateCollector(ctx, col)
				if err := vm.k.SendFromModuleToModule(ctx, ReserveName, AffiliateCollectorName,
					common.NewCoins(common.NewCoin(common.RuneAsset(), payout))); err != nil {
					ctx.Logger().Error("fail to transfer revshare", "thorname", thornameName, "error", err)
					// Roll back the collector change so state stays consistent
					// when the bank transfer fails.
					col.RuneAmount = common.SafeSub(col.RuneAmount, payout)
					vm.k.SetAffiliateCollector(ctx, col)
					payout = cosmos.ZeroUint()
				} else {
					totalRevShare = totalRevShare.Add(payout)
				}
			}
		}
		// Always emit the event — even when the thorname has been
		// removed (ownerForEvent stays the zero AccAddress) and even
		// when bps == 0. NewEventRevShare clamps a -1 GetMimirWithRef
		// "unset" sentinel to 0 so indexers never see a negative bps.
		if emitErr := vm.eventMgr.EmitEvent(ctx, NewEventRevShare(thornameName, ownerForEvent, accruedFee, bps, payout)); emitErr != nil {
			ctx.Logger().Error("fail to emit rev_share event", "error", emitErr)
		}
	}
	vm.k.DeleteAffiliateLiquidityFees(ctx)
	systemIncome = common.SafeSub(systemIncome, totalRevShare)
	// --- end REVSHARE ---
```

Ensure `"gitlab.com/thorchain/thornode/v3/constants"` is imported in `manager_network_current.go` (likely already imported).

- [ ] **Step 4: Verify block ordering**

After the edit, visually confirm the file layout is (top-to-bottom):

1. `systemIncome := blockReward.Add(totalLiquidityFees)` (original line 1975)
2. **NEW** REVSHARE loop (reduces `systemIncome` by `totalRevShare`)
3. `devFundSystemIncomeBpsUint := ...` through `polReserveDeduct = common.GetSafeShare(..., systemIncome)` (original lines 1976-1985). These now compute their deducts against the reduced base — correct behavior.
4. Existing `if !tcyStakeDeduct.IsZero() { systemIncome = common.SafeSub(systemIncome, tcyStakeDeduct) }` etc. (line 2003+).

If any `GetSafeShare(..., systemIncome)` call ended up above the new REVSHARE block, move the REVSHARE block above it.

- [ ] **Step 5: Run the tests**

```bash
goimports -w x/thorchain/manager_network_current.go x/thorchain/manager_network_current_test.go
go test -tags=mocknet -parallel=1 -run 'NetworkManagerVCURSuite.TestRevShare' ./x/thorchain/... -v
```

Expected: PASS.

- [ ] **Step 6: Run the full manager_network test file to catch regressions**

Run: `go test -tags=mocknet -parallel=1 -run NetworkManagerVCURSuite ./x/thorchain/... -v`
Expected: PASS. If `calcBlockRewards` has other tests that baseline numeric expectations, they may fail because the new block is a no-op only when the accumulator is empty. Seed the accumulator to empty in those tests, or they should already be green (accumulator is empty by default).

- [ ] **Step 7: Commit**

```bash
git add x/thorchain/manager_network_current.go x/thorchain/manager_network_current_test.go
git commit -m "$(cat <<'EOF'
feat(thorchain): REVSHARE end-of-block deduction

UpdateNetwork now iterates the per-block affiliate liquidity fee
accumulator, pays out REVSHARE_bps * accrued / 10_000 from the reserve
to AffiliateCollector (via the thorname owner), reduces systemIncome
by the total before existing splits, and emits a rev_share event per
thorname (including when REVSHARE is unset, for indexers).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Regression Test

**Files:**

- Create: `test/regression/suites/affiliate-revshare.yaml`

- [ ] **Step 1: Author the regression suite**

Create `test/regression/suites/affiliate-revshare.yaml`. Study `test/regression/suites/mimir/mimir.yaml` as the template for YAML format. The suite should:

1. Load default state (`{{ template "default-state.yaml" }}`).
2. Register a thorname (use a tx-deposit with a `~:...` memo, or whichever helper exists in the test framework — inspect `test/regression/suites/mimir/mimir.yaml` and other suites for examples).
3. Set `REVSHARE-<thorname>` to `2500` via `tx-mimir` signed by the operational-threshold signer (follow the pattern in `mimir.yaml`).
4. Advance blocks until the mimir is effective.
5. Perform a swap with a memo whose first affiliate is that thorname, producing a measurable liquidity fee.
6. Advance one block (so `UpdateNetwork` runs with the attribution in the accumulator).
7. Assert:
   - A `rev_share` event was emitted for that thorname (query `/thorchain/events` or the block query endpoint).
   - The thorname owner's `AffiliateCollector` `rune_amount` increased (query `/thorchain/affiliate_collectors/{owner}` or equivalent — inspect the querier).
   - The accumulated fee event `bps` matches `2500`, `payout > 0`.

A stub to begin from:

```yaml
{ { template "default-state.yaml" } }
---
# Register thorname "affx"
type: tx-deposit
signer: { { addr_thor_fox } }
memo: "~:affx:THOR:{{ addr_thor_fox }}"
coins:
  - amount: "1000000000"
    asset: "rune"
---
type: create-blocks
count: 1
---
# Set REVSHARE mimir
type: tx-mimir
key: REVSHARE-affx
value: 2500
signer: { { addr_thor_dog } }
---
type: create-blocks
count: 1
---
# (additional votes to reach operational threshold if required —
#  inspect mimir.yaml for multi-signer operational vote sequence)
---
# Perform swap naming affx as first affiliate
type: tx-observed-in
# ... swap observation pointing to BTC -> RUNE with affiliate "affx"
---
type: create-blocks
count: 2
---
# Check the rev_share event was emitted
type: check
endpoint: http://localhost:1317/thorchain/block
asserts:
  - '[.end_block_events[]? | select(.type == "rev_share") | select(.thorname == "affx")] | length > 0'
  - '[.end_block_events[]? | select(.type == "rev_share") | .bps][0] == "2500"'
---
# Check affiliate collector balance
type: check
endpoint: http://localhost:1317/thorchain/affiliate_collectors
asserts:
  - '[.affiliate_collectors[]? | select(.owner_address == "{{ addr_thor_fox }}") | .rune_amount | tonumber] | first > 0'
```

The exact `tx-observed-in` structure for a swap with an affiliate memo must be pulled from an existing swap suite (`test/regression/suites/swap/` or similar). The assertion jq expressions may need adjusting to the actual event/response shape.

- [ ] **Step 2: Run the suite**

```bash
CI=1 EXPORT=1 DEBUG=1 RUN=affiliate-revshare make test-regression
```

Expected: PASS.

- [ ] **Step 3: Regenerate mnt snapshot if the framework requires it**

If the test framework emits a `test/regression/mnt/blocks/<name>` snapshot, the first run generates it. Re-run without `EXPORT=1` to confirm the snapshot passes. The user's convention requires env vars AFTER the command.

- [ ] **Step 4: Commit**

```bash
git add test/regression/suites/affiliate-revshare.yaml test/regression/mnt/blocks/affiliate-revshare || true
git commit -m "$(cat <<'EOF'
test(regression): affiliate revenue share end-to-end suite

Exercises thorname registration, REVSHARE-<thorname> operational mimir,
a swap with the thorname as first affiliate, the resulting rev_share
event, and the affiliate collector balance delta.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Final Verification

- [ ] **Step 1: Run unit tests (mocknet tag)**

Run: `make test`
Expected: PASS.

- [ ] **Step 2: Run full regression suite**

Run: `CI=1 EXPORT=1 DEBUG=1 make test-regression`
Expected: PASS, including `affiliate-revshare`.

- [ ] **Step 3: Lint and format**

Run: `make format && make lint`
Expected: no violations.

- [ ] **Step 4: Build binary**

Run: `make build`
Expected: binary built, no errors.

- [ ] **Step 5: Confirm all commits are clean**

Run: `git log --oneline develop..HEAD`
Expected: 7–8 commits matching the task structure (Constants / Prefix+Interface / Keeper / Event / Mimir / Attribution / UpdateNetwork / Regression).
