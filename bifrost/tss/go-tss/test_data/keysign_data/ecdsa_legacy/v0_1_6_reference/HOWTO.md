# v0.1.6 reference fixtures

These four files are verbatim copies of the binance-chain/tss-lib v0.1.6
ECDSA test fixtures. They capture the on-disk shape a real v0.1.6 keyshare
takes, which differs from a v3 keyshare in exactly one place:

- `PaillierSK` contains only `{N, LambdaN, PhiN}` — the v3-only safe primes
  `P` and `Q` are absent.

All other fields (top-level `Alpha`, `Beta`, `P`, `Q`, `NTildei`, `H1i`,
`H2i`, `Xi`, `ShareID`, `Ks`, `NTildej`, `H1j`, `H2j`, `BigXj`,
`PaillierPKs`, `ECDSAPub`) are structurally identical to a v3 keyshare and
deserialise into v3 structs without modification.

## Usage

These files are loaded by `bifrost/tss/go-tss/keysign/ecdsa/keyshare_version_test.go`
to assert that `DetectKeyshareVersion` and `ValidateForKeysign` correctly
classify and accept real v0.1.6 data. They are **not** used by the
integration test `TestSignMessageLegacyKeyshare` because their `Ks` values
are arbitrary test IDs that do not align with thornode's testPubKey-derived
party IDs.

## Source

```
gitlab.com/thorchain/tss/tss-lib @ v0.1.6
test/_ecdsa_fixtures/keygen_data_{0,1,2,3}.json
```

Local checkout used: `/Users/cbarraford/go/src/gitlab.com/thorchain/tss/tss-lib`.

## Regenerating

The source repo is read-only for our purposes. To refresh, re-clone tss-lib
at v0.1.6 and copy the four files. There is no build step.
