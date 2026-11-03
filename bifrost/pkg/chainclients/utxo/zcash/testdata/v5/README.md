# Transparent v5 vectors

Static regression fixtures for transparent v5 (ZIP-225 format, ZIP-244 non-malleable
txid and SIGHASH_ALL) transactions. They reuse the transparent input/output shapes of
the `../v6` fixtures (branch IDs, 1/2/253 inputs, distinct P2PKH/P2SH previous-output
scripts, memo outputs) so the v5-vs-v6 txid delta on identical transparent content is on
record; every `txid` here differs from its `../v6` counterpart.

The serialization, transaction IDs and signing digests were produced by this package and
**independently validated by `TestV5Zebra`**: an official `zebrad` (2.5.0) accepts the
signed v5 transactions into its mempool — verifying each per-input SIGHASH_ALL against the
attached ECDSA signatures — and mines them as version 5, with the node's reported txid
equal to `ID()`. Regenerate with `GEN_V5=1 go test -run TestGenerateV5Vectors`.

v5 is a valid transaction version both before and after NU7 (only v4 and earlier are
rejected at NU7). The vault now signs fresh transparent payments as **v6** (see `../v6`);
these v5 vectors remain as regression fixtures and cover the resume path, which still
accepts and finishes an in-flight v5 checkpoint. The `nConsensusBranchID` in the header is
the only digest input that changes across the NU6.x→NU7 boundary.

`bitcoin` is the btcd input/output container, `raw` the Zcash v5 wire encoding, `txid`
RPC display order, and `sighashes` raw per-input SIGHASH_ALL digest bytes.
