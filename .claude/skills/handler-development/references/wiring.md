# Wiring a New Msg End to End

Two worked examples on develop: `MsgOperatorRotate` (internal, memo-driven) and `MsgBan` (external, node-signed). Grep the example name to see every touch-point; the tables below are the checklist.

## Internal msg: trace of `MsgOperatorRotate`

| Step | File                                                 | What                                                                                           |
| ---- | ---------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| 1    | `proto/thorchain/v1/types/msg_operator_rotate.proto` | `message MsgOperatorRotate`; regenerate with `make proto-gen`                                  |
| 2    | `x/thorchain/types/msg_rotate.go`                    | `NewMsgOperatorRotate`, `ValidateBasic`, `GetSigners`                                          |
| 3    | `x/thorchain/alias.go`                               | Re-export `MsgOperatorRotate`, `NewMsgOperatorRotate`, `OperatorRotateMemo`                    |
| 4    | `x/thorchain/memo/memo.go`                           | `TxOperatorRotate` in the `TxType` enum, `"operator"` in `stringToTxTypeMap`, `IsInbound` list |
| 5    | `x/thorchain/memo/memo_rotate.go`                    | `OperatorRotateMemo`, `NewOperatorRotateMemo`, `(p *parser) ParseOperatorRotate`               |
| 6    | `x/thorchain/memo/memo_parser.go`                    | `case TxOperatorRotate: return p.ParseOperatorRotate()`                                        |
| 7    | `x/thorchain/handler_operator_rotate.go`             | `NewOperatorRotateHandler`, `Run`, `validate`, `handle`                                        |
| 8    | `x/thorchain/handler.go` `getInternalHandlerMapping` | `m[sdk.MsgTypeURL(&MsgOperatorRotate{})] = NewOperatorRotateHandler(mgr)`                      |
| 9    | `x/thorchain/handler.go` `processOneTxIn`            | `case OperatorRotateMemo:` builds the msg from the memo and `tx.Tx.Coins`                      |
| 10   | `x/thorchain/handler_operator_rotate_test.go`        | `HandlerOperatorRotateSuite`                                                                   |
| 11   | `x/thorchain/memo/memo_parser_test.go`               | TxType list, string map and parse cases                                                        |
| 12   | `test/regression/suites/operator-rotate/rotate.yaml` | End-to-end regression                                                                          |

Notes:

- Internal msgs are not registered in `x/thorchain/types/codec.go` and have no `rpc` in `tx.proto`: nobody signs them directly, they are built from an observed tx.
- New `TxType` values: append to the enum and keep `memo_parser_test.go` lists in sync. Check every predicate in `memo.go` (`IsInbound`, `IsOutbound`, `IsOutboundMemoless`, `IsInternal`, `HasOutbound`) - a missing entry silently changes refund and fee behaviour.
- `processOneTxIn` is where coins, signer and memo fields become msg fields. Validate coin count/asset in `ValidateBasic` or the handler's `validate`, not by indexing blindly.

## External msg: trace of `MsgBan`

| Step | File                                                    | What                                                                                   |
| ---- | ------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| 1    | `proto/thorchain/v1/types/msg_ban.proto`                | `message MsgBan`                                                                       |
| 2    | `proto/thorchain/v1/types/tx.proto`                     | `rpc Ban(MsgBan) returns (MsgEmpty);` in `service Msg`; then `make proto-gen`          |
| 3    | `x/thorchain/types/msg_ban.go`                          | `NewMsgBan`, `ValidateBasic`, `GetSigners`, `MsgBanCustomGetSigners`                   |
| 4    | `x/thorchain/types/codec.go` `RegisterLegacyAminoCodec` | `cdc.RegisterConcrete(&MsgBan{}, ModuleName+"/MsgBan", nil)`                           |
| 5    | `x/thorchain/types/codec.go` `RegisterInterfaces`       | `&MsgBan{}` in the `sdk.Msg` implementations list                                      |
| 6    | `x/thorchain/types/codec.go` `DefineCustomGetSigners`   | `protoreflect.FullName("types.MsgBan")` -> `MsgBanCustomGetSigners`                    |
| 7    | `x/thorchain/handler_ban.go`                            | `NewBanHandler`, `Run`, `validate`, `handle`, and `BanAnteHandler`                     |
| 8    | `x/thorchain/ante.go` `anteHandleMessage`               | `case *types.MsgBan: return BanAnteHandler(ctx, version, ad.keeper, *m)`               |
| 9    | `x/thorchain/msg_server.go`                             | `func (ms msgServer) Ban(...)` -> `externalHandler(goCtx, NewBanHandler(ms.mgr), msg)` |
| 10   | `x/thorchain/handler_ban_test.go`                       | Handler suite                                                                          |

Notes:

- `anteHandleMessage` ends in `default: return ctx, cosmos.ErrUnknownRequest("invalid message type")`. Forgetting step 8 makes every tx with the new msg fail in ante, with no handler log.
- Missing step 6 fails at runtime, not at compile time (`app/app.go` passes `DefineCustomGetSigners` into the tx signing options).
- The ante handler is where node-account authorization is checked before the msg reaches state code (`BanAnteHandler`); keep it cheap and read-only.
- If the msg should also be reachable from a memo or another handler, register it in `getInternalHandlerMapping` too (`MsgSwap` is in both `codec.go` and the internal mapping).

## Shared touch-points

| Concern              | File                                                                        |
| -------------------- | --------------------------------------------------------------------------- |
| Event type           | `x/thorchain/types/type_event.go`; emit via `h.mgr.EventMgr().EmitEvent`    |
| Errors               | `x/thorchain/errors.go` (`errInvalidMessage`, `errInternal`, `ErrInternal`) |
| Auth helpers         | `x/thorchain/helpers.go` (`isSignedByActiveNodeAccounts`, `wrapError`)      |
| Constants and mimir  | `constants/` (see `mimir-parameter`)                                        |
| API / query exposure | `x/thorchain/querier.go`, `openapi/openapi.yaml`                            |
| Docs for users       | `docs/`                                                                     |
