# Parse Pipeline: raw memo to handler

## 1. Entry points (x/thorchain/memo/memo.go)

| Function                                     | Line | Keeper | Used by                                                                                     |
| -------------------------------------------- | ---- | ------ | ------------------------------------------------------------------------------------------- |
| `ParseMemo(version, memo)`                   | 313  | nil    | bifrost, tools, many unit tests                                                             |
| `ParseMemoWithTHORNames(ctx, keeper, memo)`  | 329  | real   | `processOneTxIn`, handlers, queriers                                                        |
| `ParseMemoWithTHORNamesCached(ctx, k, memo)` | 375  | real   | advanced swap queue EndBlock, after `WithMemoCache` (manager_adv_swap_queue_current.go:799) |

All three recover panics into `errMemoPanic`.

**Keeper-nil fallback.** With no keeper, `ParseSwapMemo` calls `ParseSwapMemoV1`
(memo_swap.go:255) and `ParseAddLiquidityMemo` calls `ParseAddLiquidityMemoV1`
(memo_add.go:97). The V1 swap path:

- has no streaming support (`0/100/10` fails as an invalid price limit)
- has no refund address, multiple affiliates or DEX aggregator fields
- always yields market `SwapType` (so `=<` is not a limit swap)
- does no THORName resolution

A test written with `ParseMemo` therefore does not exercise the chain's swap
parser. Use a keeper-backed test (see `MemoSuite` in memo_test.go).

**Cache invariants** (comment above `ParseMemoCache`):

- The key is the raw memo only. This is safe only while THORNames and
  `MultipleAffiliatesMaxCount` cannot change.
- Cached slices (`Affiliates`, `AffiliatesBasisPoints`) are shared and read-only.
- Parse errors are cached too.

## 2. Parser (x/thorchain/memo/memo_parser.go)

- `newParser` (line 26) rejects an empty memo, drops everything after the first
  `|`, then splits on `:`. It returns a value, not a pointer. It does no
  whitespace trimming.
- `parse()` (line 58) is a plain type switch on TxType. There is no version
  dispatch and no `version.GTE` gate (see the repo-wide "handlers are not
  version-gated" rule).
- Field getters (`getAsset`, `getAddressWithKeeper`, `getUint`, `getTHORName`, ...)
  take `(idx, required, def)`. They append to `p.errs` instead of returning early,
  and `incRequired` counts required fields.
- `Error()` (line 143) first checks `hasMinParams(requiredFields+1)` and then
  joins all errors.

Real error strings to grep for:

- `invalid tx type: %s`
- `not enough parameters: %d/%d`
- `cannot parse '%s' as an asset`
- `MEMO: ... PARSE FAILURE(S): ...`

## 3. Memo to Msg (x/thorchain/handler.go)

- `processOneTxIn` (line 215) requires exactly one coin. It calls
  `ParseMemoWithTHORNames`, switches on the concrete memo type to a
  `getMsg*FromMemo` helper (lines 88-208, or inline for the newer types), and then
  calls `ValidateBasic`. An unhandled memo type returns `errInvalidMemo`
  (errors.go:52).
- `getMsgSwapFromMemo` (line 88) builds a `SwapVersion_v2` MsgSwap when
  `AdvSwapQueueEnabled`. The legacy swap queue is being removed (!5084); don't
  add code that targets it.
- Handlers are found by `sdk.MsgTypeURL(msg)` in `getInternalHandlerMapping`
  (line 51).

## 4. Native deposits and reference memos

- `handler_deposit.go`: a MsgDeposit that has coins and an empty memo gets an
  `r:<ref>` memo, with the reference derived from the amount
  (`extractReferenceFromNativeAmount`).
- `resolveReferenceMemo` (handler_observed_tx_helpers.go:1856) expands an `r:`
  memo through `fetchMemoFromReference` (line 1924). If the expanded memo is
  outbound or internal, it is blanked.
- Memoless inbounds take the reference from the amount's trailing digits via
  `ExtractReferenceFromAmount` (line 2044). The width comes from the
  `MemolessTxnRefCount` mimir (default 99_999).
- Docs: `docs/swap-guide/memoless-swaps.md`, `docs/concepts/memo-length-reduction.md`.

## 5. Size limits

| Limit                               | Value          | Where                                                             |
| ----------------------------------- | -------------- | ----------------------------------------------------------------- |
| `constants.MaxMemoSize`             | 250 bytes      | constants/constants.go:34, enforced in common/tx.go:248           |
| `constants.MaxOpReturnDataSize`     | 80 bytes       | constants/constants.go:72                                         |
| `constants.MaxMemoSizeUtxoExtended` | 80 - 1 + 8\*20 | constants/constants.go:77 (extra bytes via fake output addresses) |
| `Chain.MaxMemoLength()`             | per chain      | common/chain.go:536                                               |

A UTXO memo that fits in 250 bytes can still fail at the L1 layer. Short
prefixes (`=`, `+`, `-`, `~`), asset short codes and THORNames exist to save
bytes.
