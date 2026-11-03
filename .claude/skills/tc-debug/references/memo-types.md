# Memo prefixes and expected pipeline

Prefixes are case-insensitive (`stringToTxTypeMap` in `x/thorchain/memo/memo.go`). Full field formats live in `memo-parser/references/memo-formats.md`; this table only says what pipeline to expect.

| Memo prefix                               | Action                         | Key difference                                  |
| ----------------------------------------- | ------------------------------ | ----------------------------------------------- |
| `SWAP:` or `=` or `s:`                    | Swap                           | Full pipeline: observe → swap → outbound        |
| `=<` / `m=<`                              | Limit swap / modify limit swap | Sits in the advanced queue until filled/expired |
| `ADD:` or `+`                             | Add liquidity                  | No outbound — completes at inbound finalisation |
| `WITHDRAW:` or `-` or `wd:`               | Withdraw liquidity             | Outbound sends LP's share back                  |
| `DONATE:` or `d:`                         | Donate to pool                 | No outbound                                     |
| `BOND:` / `UNBOND:` / `REBOND:`           | Node bond changes              | Bond has no outbound; unbond sends RUNE         |
| `LEAVE:`                                  | Node leave                     | Sends bond back                                 |
| `TRADE+:` / `TRADE-:`                     | Trade account deposit/withdraw | Deposit has no outbound; withdraw does          |
| `SECURE+:` / `SECURE-:`                   | Secured asset deposit/withdraw | Deposit has no outbound; withdraw does          |
| `POOL+:` / `POOL-:`                       | RUNEPool deposit/withdraw      | No L1 outbound                                  |
| `NAME:` or `n:` or `~`                    | THORName register/update       | No outbound                                     |
| `MIGRATE:` / `RAGNAROK:` / `CONSOLIDATE:` | Vault-internal                 | Internal — skip in stuck outbound analysis      |
| `OUT:` / `REFUND:`                        | Outbound / refund              | Should match a planned outbound                 |
| `NOOP:`                                   | No-op                          | Observed only, no action                        |

For add/donate/bond with no expected outbound: if the stage shows completed at `inbound_finalised` and there are no planned outbounds, this is **normal** — not stuck.
