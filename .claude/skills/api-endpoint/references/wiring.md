# Query Endpoint Wiring

One REST endpoint touches five layers. Proto routes and openapi.yaml are
maintained separately. Nothing cross-checks them: today
`/thorchain/mimir/nodes` is in query.proto:243 but missing from openapi.yaml.

## Layers

| Layer           | File(s)                                                                   | Notes                                                                                                                                                              |
| --------------- | ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Service + route | `proto/thorchain/v1/types/query.proto`                                    | `package types;`, `go_package = ".../v3/x/thorchain/types"`. Each rpc has `option (google.api.http).get = "/thorchain/..."`.                                       |
| Messages        | `proto/thorchain/v1/types/query_<topic>.proto`                            | Request/Response types; gogoproto options such as `(gogoproto.jsontag)` (e.g. query_mimir.proto:30).                                                               |
| Generated Go    | `x/thorchain/types/query*.pb.go`, `query.pb.gw.go`; pulsar code in `api/` | `make proto-gen` (scripts/protocgen.sh in the pinned proto-builder Docker image). Never hand-edit.                                                                 |
| gRPC wrapper    | `x/thorchain/grpc_query.go`                                               | Exported `func (s *queryServer) X(c context.Context, req)`: `checkHeightParam(req.Height)` then `ctx := s.unwrapSdkContext(c)` then `s.queryX(ctx, req)`.          |
| Implementation  | `x/thorchain/querier.go` or `querier_<topic>.go`                          | Lowercase `func (qs queryServer) queryX(ctx cosmos.Context, req) (*types.QueryXResponse, error)`, reading via `qs.mgr.Keeper()`.                                   |
| JSON shape      | `x/thorchain/types/jsonpb_marshalers.go`                                  | `MarshalJSONPB` overrides for responses with int64, maps or top-level slices.                                                                                      |
| Spec            | `openapi/openapi.yaml` (hand-written)                                     | Embedded and served at `/thorchain/doc`, `/thorchain/doc/openapi.yaml`, `/thorchain/doc/openapi.json` (app/app.go:905-908).                                        |
| Go client       | `openapi/gen/`                                                            | `make openapi` (openapi-generator-cli v6.0.0 in Docker). Used by test/regression, test/simulation, tools/, cmd/bifrost/health.go, and jsonpb_marshalers.go itself. |

Registration needs no per-endpoint code. `module.go:162` calls
`types.RegisterQueryServer(cfg.QueryServer(), am.queryServer)`, and
`RegisterGRPCGatewayRoutes` (module.go:91) calls `types.RegisterQueryHandlerClient`.
A new rpc is live once the wrapper and implementation compile.

## JSON encoding

`CustomGRPCGatewayRouter` (x/thorchain/module.go:432) uses gogo
`gateway.JSONPb{EmitDefaults: true, OrigName: true}`:

- Field names are proto names (snake_case), not camelCase.
- Zero values are emitted.
- Plain proto JSON renders int64 as strings. If openapi.yaml declares an integer,
  or the response should be a bare map or array, add a `MarshalJSONPB` that
  converts it to the openapi shape. `QueryMimirValuesResponse` turns a repeated
  `{key,value}` into `map[string]int64`, and `QueryMimirWithKeyResponse` returns
  the bare value.
- After adding one, add the `var _ jsonpb.JSONPBMarshaler = &QueryXResponse{}`
  assertion at the top of the file.

## Height parameter

`?height=N` is moved into gRPC metadata for paths starting `/thorchain/`,
`/cosmos/` or `/bank/balances/` (the `WithMetadata` func in module.go). Give the
request message a `string height` field. The wrapper's `checkHeightParam`
rejects non-numeric and negative values. In openapi, reuse
`$ref: "#/components/parameters/queryHeight"`.

## Pagination

There is no `query.Paginate`. Paginated endpoints are hand-rolled:

- read `config.GetThornode().API.Pagination.DefaultPageSize` and `MaxPageSize`
  for the limit
- return `types.PaginationMeta`

Copy `queryLimitSwaps` in x/thorchain/querier_limit_swaps.go:15.

## Make targets

| Target                                        | Does                                                               |
| --------------------------------------------- | ------------------------------------------------------------------ |
| `make proto-gen`                              | buf generate (gogo and pulsar) inside proto-builder                |
| `make proto-format`                           | clang-format all .proto                                            |
| `make proto-format-check` / `make proto-lint` | CI checks (buf lint in proto/thorchain/v1)                         |
| `make proto-check-breaking`                   | buf breaking against `PROTO_BREAKING_BRANCH`                       |
| `make proto-all`                              | proto-format, proto-lint, proto-gen, format                        |
| `make openapi`                                | regenerate openapi/gen from openapi.yaml                           |
| `make generate`                               | go-generate, generate-abi, openapi, proto-gen, scripts/generate.py |

All of them need Docker. The CI `generate` job runs `make generate`, then
`scripts/lint-generated.bash`, which fails on any `git status` diff. Commit the
generated files.
