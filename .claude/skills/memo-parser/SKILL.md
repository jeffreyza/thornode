---
name: memo-parser
description: Parse, explain, debug or add THORChain transaction memos in x/thorchain/memo/. Use whenever a memo string appears (=:, +:, -:, ~:, trade+, pool-, r:, bond, tcy...), when a memo fails with "invalid tx type" or "not enough parameters", when adding a memo type or field, or when asking how a memo becomes a Msg, even if the user doesn't say "memo parser". For a live mainnet tx that was refunded or stuck use tc-debug; for the handler behind a new memo use handler-development.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Memo Parser

Memos are the user-facing API of THORChain. Each inbound memo is parsed in
`x/thorchain/memo/`, turned into a Msg in `x/thorchain/handler.go`, and executed
by a handler. User docs: `docs/concepts/memos.md` (formats),
`docs/aggregators/memos.md`, `docs/concepts/memo-length-reduction.md`,
`docs/swap-guide/memoless-swaps.md`. Point users there instead of restating them.

## Decision tree

| Intent                                         | Go to                   |
| ---------------------------------------------- | ----------------------- |
| "What does this memo do / why won't it parse?" | Explain or debug a memo |
| "Add a new memo type"                          | Add a memo type         |
| "Add a field to an existing memo"              | Add a field             |
| "Write tests for memo parsing"                 | Testing                 |
| A real tx on mainnet/stagenet behaved oddly    | hand off to tc-debug    |

## References

- `references/memo-formats.md`: read this when you need the exact prefix list,
  field order for a type, swap/streaming/affiliate rules, or asset short codes.
- `references/parse-pipeline.md`: read this when you need to know which parse
  entry point runs where, the keeper-nil V1 fallback, memo-to-Msg wiring,
  reference/memoless memos, or size limits.

## Explain or debug a memo

1. Split mentally the way `newParser` does (memo_parser.go:26): drop everything
   after the first `|`, then split on `:`. Part 0 is the prefix, lowercased.
2. Look up the prefix in `stringToTxTypeMap` (memo.go:61). If it is missing, the
   error is `invalid tx type: <prefix>`. Old docs and other skills still mention
   prefixes that do not exist (`a:`, `$+`, `loan+`, `x:`/`exec`).
3. Open `memo_<type>.go` and read its `Parse*Memo`. The `p.get(n)` indexes are
   the field positions. Don't guess the order from docs.
4. Check which entry point was used. A keeper-nil `ParseMemo` (bifrost, tools,
   many tests) sends swap and add through V1 parsers that ignore streaming,
   refund address, multiple affiliates and DEX fields. Behaviour can differ
   from what the chain does.
5. If it parses but the chain rejects it, read `processOneTxIn` (handler.go:215):
   it needs exactly one coin, does fuzzy asset matching, and calls `ValidateBasic`.
6. For a too-long memo, compare against `constants.MaxMemoSize` (250) and the
   chain's `MaxMemoLength()`. UTXO chains are limited by OP_RETURN (80 bytes,
   extended via fake addresses).

Gotchas that repeatedly produce wrong answers:

- Streaming is `LIMIT/INTERVAL/QUANTITY`. `0/100/10` means interval 100 blocks
  and 10 sub-swaps, not the reverse.
- `trade-` and `secure-` take the L1 destination address, not a THOR address or
  a TXID. `m=<` takes source coin, target coin and new target amount, with no
  TXID.
- `tcy` (claim) requires a THOR address field; `tcy+` takes none.
- There is no version dispatch in the parser. Never explain behaviour with
  `version.GTE(...)` or `parseXxxV98`-style functions; they do not exist.
- Fuzzy asset resolution (`ETH.USDC` to the full contract symbol) is
  `fuzzyAssetMatch` in handler.go, not the parser. Short codes are a fixed map
  built from each gas asset's `ShortCode()` in `assetShortCodes` (common/asset.go:133).

## Add a memo type

1. Add a `Tx<Name>` constant to the enum in memo.go:22. Add the prefix(es) to
   `stringToTxTypeMap` and the canonical string to `txToStringMap`. Update
   `IsInbound`/`IsOutbound`/`IsInternal` (memo.go:153-219) if needed.
2. Create `memo_<name>.go` modelled on a similar simple type (memo_switch.go,
   memo_tcy_claim.go). Embed `MemoBase`, add `String()`, a `New<Name>Memo`
   constructor and a `(p *parser) Parse<Name>Memo()` that uses the
   `(idx, required, def)` getters and returns `p.Error()`.
3. Add a `case Tx<Name>:` to `parser.parse()` (memo_parser.go:58).
4. Define the Msg in `proto/thorchain/v1/types/msg_<name>.proto` and run
   `make proto-gen`.
5. Add a `case <Name>Memo:` to `processOneTxIn` (handler.go:215) that builds
   the Msg, and register the handler in `getInternalHandlerMapping` (handler.go:51).
   The handler itself is handler-development territory.
6. Re-export anything external callers need via `x/thorchain/alias.go`.
7. Document the format in `docs/concepts/memos.md`.
8. Test it (see Testing). Run `go test ./x/thorchain/memo/... ./x/thorchain/`.

Do not gate the new type on a version. Consensus changes ship ungated.

## Add a field

1. Append the field at the end of the existing format. Inserting in the middle
   breaks every wallet and aggregator that already builds the memo.
2. Make it optional (`required=false`) with a default that keeps old memos
   working.
3. If the field is on swap or add, change only the keeper-backed path. The V1
   fallback is left as is (it is marked `TODO confirm and remove`).
4. Mind the length budget: every byte added is a byte UTXO users lose. Prefer
   `/`-joined sub-fields inside an existing part only when the type already
   does that (swap DEST, LIMIT, AFF).
5. Add the getter to the memo struct and, if the Memo interface needs it, a
   default on `MemoBase` (memo.go:285-306).

## Testing

1. Unit tests in `x/thorchain/memo/` use gocheck (`. "gopkg.in/check.v1"`), not
   testify. `MemoSuite` in memo_test.go builds a real keeper. Use it whenever the
   path under test is swap/add with keeper features or THORName resolution.
2. Pure prefix/field tests can use `ParseMemo(common.LatestVersion, ...)`. Keep
   the V1 fallback in mind for swap and add.
3. Add hostile inputs to memo_security_fuzz_test.go when touching getters
   (scientific-notation limits, huge exponents, empty sub-fields).
4. Round-trip: `String()` of the parsed memo should parse back to the same value.
5. End-to-end: `test/regression/suites/core/memo.yaml` and the feature suite for
   the type (`make test-regression RUN=<suite>`; see regression-test).

## Related skills

| Skill               | Use for                                                                                                               |
| ------------------- | --------------------------------------------------------------------------------------------------------------------- |
| tc-debug            | A real transaction whose memo misbehaved on a live network; code-path stage map in tc-debug/references/tx-pipeline.md |
| handler-development | Writing the handler a memo dispatches to                                                                              |
| regression-test     | YAML end-to-end tests for memo flows                                                                                  |
| mimir-parameter     | Mimirs that affect parsing (MultipleAffiliatesMaxCount, MemolessTxnRefCount)                                          |
