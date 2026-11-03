# Testing and Safety for Query Endpoints

## Consensus-critical queries (WASM)

`wasmAcceptedQueries` in app/wasm.go:16 lets CosmWasm contracts call these
queries inside transactions:

`Network`, `LiquidityProvider`, `MimirWithKey`, `Node`, `OutboundFee`, `Pool`,
`QuoteSwap`, `SecuredAsset`, `OraclePrice`, `SwapQueue`

Their output becomes consensus state. Any change to the logic or response
shape of these is a consensus change and must be deterministic: no map-order
iteration, no wall-clock time, no node-local config. Review rules for this are in
the blockchain-code-review skill. Adding a query to the list carries the same
weight.

## Non-WASM queries

Other queries run only on the serving node. They may use node-local state, but
they must never write to the store. Queries share `queryServer` across
concurrent calls (`initOnce` in grpc_query.go), so keep `queryServer` stateless.

## Unit tests

- `QuerierSuite` in x/thorchain/querier_test.go is gocheck. It builds
  `NewQueryServerImpl(s.mgr, txConfig, s.kb)` in `SetUpTest` (line 83). Tests
  call the lowercase `queryX` or the exported wrapper with a request struct.
- Conformance: the keysign test (querier_test.go:137) marshals the response and
  unmarshals it into the `openapi/gen` type. Do the same for any response with a
  custom `MarshalJSONPB`. It is the only guard that JSON matches the spec.
- Topic tests sit next to their file: querier_limit_swaps_test.go,
  querier_quotes_test.go, querier_reference_memo_test.go, ...

## Regression tests

- Suites: test/regression/suites/api/ (cosmos-endpoint-height.yaml, logs.yaml,
  supply.yaml, quotes/). Any suite can check any endpoint:

  ```yaml
  type: check
  endpoint: http://localhost:1317/thorchain/<path>
  asserts:
    - .field == 1
  ```

- Assertions are jq expressions over the JSON, so they test the real gateway
  encoding (snake_case, int64 shape).
- Run `make test-regression RUN=api` (see regression-test).

## Fixtures

test/fixtures/endpoints/\* are canned responses for bifrost thorclient tests
(e.g. bifrost/thorclient/block_height_test.go). They are not querier tests.
If you change the JSON shape of an endpoint bifrost reads, update the fixture.

## Legacy code to avoid

`querier.go` still reads the legacy streaming-swap store next to the advanced
swap queue (around line 1682). The legacy swap queue is being removed (!5084).
New endpoints should read only the advanced swap queue.
