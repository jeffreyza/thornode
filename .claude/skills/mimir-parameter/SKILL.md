---
name: mimir-parameter
description: Add, read, explain or test THORChain mimir parameters and protocol constants. Use whenever someone asks how a mimir gets set, why a vote didn't take effect, whether a key is operational or economic, how to add a constant or mimir key, how to halt or pause something, or how to set mimirs in a test, even if the user doesn't say "mimir". There is no admin mimir. To look at current live values on a network use health or tc-debug; for adding the query endpoint itself use api-endpoint.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Mimir Parameters

Mimir is THORChain's on-chain override layer. Each protocol constant can be
overridden by node votes, and free-standing keys (halts, per-chain templates)
exist only as mimirs. **Only active-node votes (`MsgMimir`) set mimirs. There is
no admin tier, no admin key and no `set-admin-mimir` command on develop.**

User catalogue of keys: `docs/mimir.md`.

## Decision tree

| Intent                                                 | Go to                         |
| ------------------------------------------------------ | ----------------------------- |
| "Why didn't my vote set the mimir?" / how voting works | Explain voting                |
| "Add a new tunable parameter"                          | Add a constant-backed mimir   |
| "Add a per-chain / per-asset halt or knob"             | Add a template key            |
| "Read a mimir in my code"                              | Read a value                  |
| "Set a mimir in a regression/unit test or on mocknet"  | Testing                       |
| "What is the value right now on mainnet?"              | hand off to health / tc-debug |

## References

- `references/voting.md`: read this when you need the vote handler flow,
  operational vs economic rules, churn purge, read semantics, the cache, or the
  mimir API endpoints.
- `references/keys-and-constants.md`: read this when you add or look up a key:
  constant enum, defaults, stringer generation, templates, network overrides, and
  keys that don't exist.

## Explain voting

1. Find the class: read `IsOperationalMimir` (x/thorchain/keeper/v1/keeper_mimir.go:108).
   Operational keys need `OperationalVotesMin` (default 3) matching active votes
   with no tie. Economic keys need a 2/3 supermajority of active nodes.
2. Walk `MimirHandler.handle` (x/thorchain/handler_mimir.go:104):
   - the vote is charged and recorded
   - the set happens only if the effective value is `>= 0`, differs from the
     current value, and equals this vote's value
3. Common reasons a vote "did nothing":
   - The vote was a duplicate of the current value.
   - Not enough active voters. Standby nodes' votes don't count.
   - A tie on an operational key.
   - The active set changed and nobody re-voted.
   - Operational votes were purged at the last churn (`PurgeOperationalNodeMimirs`).
   - The voter sent -1 hoping to unset the mimir. Votes can never unset a mimir.
4. Check `/thorchain/mimir/nodes_all` for raw votes. `/thorchain/mimir/nodes`
   hides operational values that were reached by minimum votes, so don't use it
   to judge operational keys.

## Add a constant-backed mimir

1. Add the name to the `ConstantName` iota in constants/constant_values.go, before
   the "implicitly-0" comment if it has a nonzero default.
2. Add the default to `NewConstantValue()` in constants/constants_v1.go.
3. Run `make go-generate` to regenerate constants/constantname_string.go. Commit
   the generated file. CI's `generate` job fails on a diff.
4. Read it with `k.GetConfigInt64(ctx, constants.YourName)`. Don't read the
   constant directly, or the mimir override is ignored.
5. Decide the class. If it must react fast (a halt, pause or operational
   toggle), either name it so `IsOperationalMimir` matches or add it to the
   exact-match list, and add a case to keeper_mimir_test.go. Remember that any key
   containing `HALT` or `PAUSE` is operational automatically.
6. If a bad value could break consensus or drain funds, add validation in
   `MimirHandler.validate` (like the `SLIPMINBPS` and `REVSHARE-` checks), and
   clamp at the read site.
7. Document it in docs/mimir.md.
8. Do not gate on version. Defaults and new behaviour ship ungated.

## Add a template key

1. Add `MimirTemplate<Name> = "<Prefix>-%s"` to constants/mimir_strings.go, with
   a comment saying what fills `%s`.
2. Read it with `k.GetMimirWithRef(ctx, constants.MimirTemplate<Name>, chain.String())`.
   For assets, pass `asset.MimirString()`.
3. Keep the expanded key within `^[a-zA-Z0-9-]+$` and 128 chars.
4. Follow steps 5-7 above (class, validation, docs).

## Read a value

- `GetConfigInt64` is for constant-backed keys. It returns the mimir if
  `>= 0`, else the default.
- `GetMimir` / `GetMimirWithRef` return `-1` when unset. Treat `< 0` as unset
  and choose the default explicitly.
- `/thorchain/mimir/key/{key}` also returns -1 when unset. It does not
  fall back to the constant; use `/thorchain/constants` for defaults.
- Mocknet builds read unset mimirs from env vars (see the reference).

## Testing

1. Unit tests: set values directly with `k.SetMimir(ctx, "Key", v)`. For vote
   logic, call `k.SetNodeMimir` per node and run the handler. Existing examples
   are in handler_mimir_test.go and keeper/v1/keeper_mimir_test.go (gocheck).
2. Regression state: a template's genesis can preload
   `thorchain: mimirs: - key: ... value: "1"`
   (see test/regression/templates/btc-eth-3usd-pool-state.yaml).
3. Regression votes: `type: tx-mimir` blocks with `key`, `value` and `signer`
   (test/regression/suites/mimir/node-mimir.yaml). Assert with
   `type: check`, `endpoint: http://localhost:1317/thorchain/mimir`, and
   `asserts: - .YOURKEY == 1`. Keys are upper-case in the response.
4. Run `make test-regression RUN=mimir` (see regression-test).

## Related skills

| Skill            | Use for                                                    |
| ---------------- | ---------------------------------------------------------- |
| health           | Current mimir values and halts on a live network           |
| tc-debug         | A live tx affected by a halt or pause                      |
| api-endpoint     | Adding or changing query endpoints (mimir routes included) |
| regression-test  | Writing the YAML suite                                     |
| protocol-upgrade | Store migrations that delete or rewrite mimirs             |
