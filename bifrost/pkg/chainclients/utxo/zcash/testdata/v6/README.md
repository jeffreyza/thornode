# Transparent v6 vectors

Static regression fixtures covering branch IDs, 1/2/253 inputs, distinct amounts
and scripts, P2PKH/P2SH recipients, and memo outputs. Serialization, transaction IDs
and signing digests were independently checked against `zcash/librustzcash`
revision `5345dbe0cd6c7f2057e631a34a74dffa84ff1d48` (`zcash_primitives` 0.30.1).

`nu63-multiple` uses different P2PKH/P2SH previous-output scripts to catch input
indexing errors. It tests digests, not executable P2SH spends; Bifrost vault inputs
remain P2PKH.

`bitcoin` is the btcd input/output container, `raw` the Zcash wire encoding,
`txid` RPC display order, and `sighashes` raw digest bytes.
