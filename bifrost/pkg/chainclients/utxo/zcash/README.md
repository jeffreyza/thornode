# Transparent Zcash support

Shared Zcash address and transaction handling for Bifrost, THORChain and
simulation. Supports transparent and TEX addresses, v6 serialization, and
ZIP 244/229 transaction IDs and signing digests. Shielded payments are not
supported.

Consensus selection uses Zebra's upgrade schedule, with expiry before the next
upgrade. Branch IDs come from the daemon without a compiled allowlist.
Bifrost checkpoints preserve the branch and expiry across retries.
Legacy v3/v4 decoding supports already-signed payloads; unfinished legacy
checkpoints cannot be converted to v6.

[Reference vectors](testdata/v6/README.md) cover serialization and signing digests.
Daemon and simulation tests exercise the configured Zebra version and network.
