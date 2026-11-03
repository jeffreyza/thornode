---
name: api-endpoint
description: Add, change, explain or debug THORNode query endpoints (/thorchain/... REST via gRPC-Gateway). Use whenever someone touches query.proto, querier*.go, grpc_query.go, openapi.yaml or jsonpb_marshalers.go, asks why a response has strings instead of numbers or a missing field, or wants a new query, even if the user doesn't say "endpoint". For live network data use health or tc-debug; for WASM-visible query review use blockchain-code-review.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# THORNode Query Endpoints

Every `/thorchain/...` GET is a gRPC `Query` rpc exposed through gRPC-Gateway.
To add one, you write a proto rpc, a thin exported wrapper and a lowercase
implementation, often a JSON marshaler override, and a hand-written openapi
entry. Nothing checks that proto and openapi agree, so keep them in sync
yourself.

## Decision tree

| Intent                                                | Go to                                                         |
| ----------------------------------------------------- | ------------------------------------------------------------- |
| "Add a new query endpoint"                            | Add an endpoint                                               |
| "Add a field to a response"                           | Change a response                                             |
| "Response JSON looks wrong (strings, names, missing)" | Debug the JSON                                                |
| "Where is endpoint X implemented?"                    | Find an endpoint                                              |
| "Is changing this query consensus-relevant?"          | references/testing-and-safety.md, then blockchain-code-review |
| Values on a live network                              | hand off to health / tc-debug                                 |

## References

- `references/wiring.md`: read this when you need the layer-by-layer file map,
  JSON encoding rules, the height parameter, pagination, or make targets.
- `references/testing-and-safety.md`: read this when you write tests, touch a
  WASM-accepted query, or need the regression `check` format.

## Find an endpoint

1. Grep the path in `proto/thorchain/v1/types/query.proto`
   (`grep -n '"/thorchain/<path>' proto/thorchain/v1/types/query.proto`) to get
   the rpc name `X`.
2. The wrapper is `func (s *queryServer) X` in x/thorchain/grpc_query.go.
3. The implementation is `queryX` in x/thorchain/querier.go or a
   `querier_<topic>.go`.
4. Check x/thorchain/types/jsonpb_marshalers.go for a `QueryXResponse`
   `MarshalJSONPB` override that changes the shape.
5. The documented shape is the path in openapi/openapi.yaml. It may be missing
   or stale.

The proto lives under `proto/thorchain/v1/types/`, not `x/thorchain/types/`,
and its package is `types`. The `.pb.go` files in x/thorchain/types are
generated.

## Add an endpoint

1. Messages: add `QueryXRequest` (include `string height` for historical
   queries) and `QueryXResponse` to `proto/thorchain/v1/types/query_<topic>.proto`.
   Create the file if the topic is new and import it from query.proto.
2. Route: add an rpc to `service Query` in query.proto with
   `option (google.api.http).get = "/thorchain/<path>/{param}";`.
3. Run `make proto-gen`, then `make proto-format` (or `make proto-all`). Both
   need Docker.
4. Wrapper: add `func (s *queryServer) X(c context.Context, req *types.QueryXRequest)`
   to grpc_query.go. Copy the `checkHeightParam`, `unwrapSdkContext`, `s.queryX`
   pattern from a neighbour. Registration is automatic (module.go:162).
5. Implementation: add `func (qs queryServer) queryX(ctx cosmos.Context, req ...)`
   in querier.go or a new `querier_<topic>.go`. Read through `qs.mgr.Keeper()`.
   Validate request params and return errors, not panics.
   - Never write state.
   - Bound any iteration. For lists, use the hand-rolled pagination in
     querier_limit_swaps.go, not `query.Paginate`.
   - Read only the advanced swap queue. The legacy swap queue is being removed
     (!5084).
6. JSON: if the response has int64 fields, maps, or should be a bare
   array/value, add `MarshalJSONPB` plus its interface assertion in
   jsonpb_marshalers.go.
7. Spec: add the path and schema to openapi/openapi.yaml. Reuse
   `#/components/parameters/queryHeight`. Then run `make openapi` to regenerate
   openapi/gen.
8. Tests:
   - a `QuerierSuite` gocheck test that includes an unmarshal into the
     `openapi/gen` type
   - a regression `type: check` block if behaviour depends on chain state
9. Commit the generated proto and openapi output. CI `generate` runs
   `make generate` and fails on any diff (scripts/lint-generated.bash).

## Change a response

1. Proto fields: add only, with new field numbers. Never renumber or retype.
   `make proto-check-breaking` catches it.
2. If the query is in `wasmAcceptedQueries` (app/wasm.go:16), the change is a
   consensus change. See references/testing-and-safety.md and get a review under
   blockchain-code-review.
3. Update the custom `MarshalJSONPB` if one exists. Otherwise the new field may
   come out as a string, or not at all.
4. Update openapi.yaml, run `make openapi`, and fix the callers of `openapi/gen`
   that break (test/regression, test/simulation, tools/, bifrost health).
5. If bifrost reads this endpoint, update test/fixtures/endpoints/<name>.

## Debug the JSON

1. Numbers as strings: the default gogo JSONPb renders int64 as strings. The
   fix is a `MarshalJSONPB` override (see wiring.md, JSON encoding).
2. snake_case names and zero values present: expected, because the gateway uses
   `OrigName: true, EmitDefaults: true` (module.go:437). Use
   `(gogoproto.jsontag)` only to rename.
3. Endpoint in the proto but not in the Swagger UI: openapi.yaml is
   hand-written and wasn't updated. `/thorchain/mimir/nodes` is a live example.
4. `?height=` ignored: the path must start with `/thorchain/`, `/cosmos/` or
   `/bank/balances/`, and the request must carry `height`.
5. Compare against `curl localhost:1317/thorchain/<path>` on mocknet
   (mocknet-debug), or a regression `check` with jq asserts.

## Related skills

| Skill                  | Use for                                                                                   |
| ---------------------- | ----------------------------------------------------------------------------------------- |
| blockchain-code-review | Reviewing changes to WASM-accepted queries                                                |
| health / tc-debug      | Reading endpoints on live networks; tx pipeline map in tc-debug/references/tx-pipeline.md |
| mimir-parameter        | Semantics of the /thorchain/mimir/\* endpoints                                            |
| regression-test        | `type: check` suites                                                                      |
| mocknet-debug          | Hitting endpoints on a local network                                                      |
