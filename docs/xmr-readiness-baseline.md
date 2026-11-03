# XMR readiness baseline

This baseline supplies shared startup, signer configuration, and simulation
prerequisites for branches that use or test XMR. Its mocknet timing settings and
multichain simulation support also affect non-XMR chains.

It does not enable XMR key generation on develop. Bifrost still skips XMR
monovault keygen until a descendant includes the enable/halt gate and ceremony
changes from [S00 / MR !5026](https://gitlab.com/thorchain/thornode/-/merge_requests/5026).
Setting `ENABLE_MONOVAULT_KEYGEN=1` alone cannot override that skip.

## Startup and signer configuration

- Mocknet registration waits for successful committed bond, node-key, IP, and
  version transactions. Only recognized sequence rejections retry, with limits.
  Permanent failures and uncertain outcomes stop startup.
- Churn readiness requires valid registered IPs. Standby nodes remain eligible
  for the readiness check while `HaltChurning` pauses their transition to Ready.
- Each validator uses its corresponding signer, matching credentials and public
  test seed. Images are pinned by digest and supported request limits are set.
  Non-mocknet signers read seeds from mounted files.
- The standard cluster has five validator/signer pairs, allowing one validator
  to leave while retaining four active participants.
- Standard mocknet signing uses a 45-second Bifrost timeout and a 60-block jail
  on every THORNode/Bifrost pair. At mocknet's one-second block time, the jail
  must outlast signing; changing only the timeout prevents Bifrost startup.
- Mocknet XMR scanning uses `BLOCK_SCANNER_BACKOFF`, one second by default.
  The production default remains 30 seconds. Scanner health and signer request
  limits remain enforced.

| Compose profile                   | Signer identity | Monero daemon network | Authentication                       |
| --------------------------------- | --------------- | --------------------- | ------------------------------------ |
| `docker-compose.yml`              | `mocknet`       | mocknet               | Local test default or explicit token |
| `docker-compose.xmr-chainnet.yml` | `chainnet`      | mainnet               | Explicit token required              |
| `docker-compose.xmr-stagenet.yml` | `mocknet`       | mainnet               | Explicit token required              |

The file named `xmr-stagenet` starts a private THORChain mocknet against a real
Monero mainnet daemon. Its filename is not the signer's network identity.
For both private profiles, set `XMR_FROST_AUTH_BEARER` when rendering or starting
Compose. Bifrost and its signer receive the same token. These isolated test
profiles explicitly permit HTTP; the global default and mainnet HTTPS rule
remain unchanged. Keep actual node secrets outside the repository.

The simulation helper has a separate image pin: its faucet behavior is part of
the test harness and must not be replaced implicitly with the signer image.
Image/configuration checks do not establish production signer acceptance or
combined runtime-identity/effective-birthday validation.

## Simulation guarantees

- Full cluster runs require XMR by default. They wait up to eight minutes for
  an initial inbound address before constructing actors, then fail if it is
  absent. Explicitly optional local runs retain a 30-second fallback and can
  omit XMR when no inbound address exists; they are not XMR validation.
- `ClusterChurn` includes initial activation, Bond Yield checks, validator
  replacement, and Mimir cleanup. Select it with `cluster-churn`; `bond-yield`
  remains an alias, and selecting both runs the suite once.
- When both `cluster-churn` and `xmr-post-churn` are selected, the runner shares
  the churn: snapshot XMR before the leave, then verify its replacement after
  cleanup, before solvency. It omits the separate initial `churn` and later XMR
  stage. This shared path also matches vault membership to the active validators
  before and after the leave. Standalone `xmr-post-churn` retains its own churn
  path without that membership assertion.
- Both paths require a funded and spendable replacement and prove that vault
  signs a BTC-to-XMR swap when XMR coverage is enabled or required.
  Lookup failures retry; unavailable funding fails explicitly. Optional ordinary
  swaps can still skip when unfunded.
- XMR deposits wait for a committed positive network-fee size and rate within
  the actor deadline. They do not assume a genesis fee. XMR source quotes retain
  the selected user's refund address through retries and refreshed quotes.
- The spent-reference actor uses the ordinary XMR swap's eight-minute budget,
  including fee readiness and spent-reference observation. It follows its
  submitted outbound and reports refunds,
  reverts, or finalization without a scheduled outbound. It reads the captured
  vault's stored references for that outbound ID and rejects previously spent
  outputs. Historical references cannot satisfy the check. The outbound's
  consensus observation verifies chain, vault, and completed status; the
  separately approved reference set need not appear in that observation.
- First-churn scheduling recognizes a genesis vault with omitted `status_since`.
  Refund actors size deposits against current outbound fees, with headroom.

## Other chain fixtures

The TRON fixture maintains a producer connection and sets
`minEffectiveConnection=1`. Startup accepts exactly one supported assignment
(`0` or `1`) and rejects configuration drift. This reduces disconnected
submissions, but connectivity errors can still follow local transaction
acceptance. The fixture does not disable witness synchronization checks.

TRON simulation submission retains the signed bytes and original transaction
ID. Accepted, duplicate, lost, malformed, or ambiguous replies trigger bounded
confirmation polling and same-payload rebroadcasts before expiry. Only a known
first-submission rejection can release the identity for a later new signing.
`NOT_ENOUGH_EFFECTIVE_CONNECTION` is ambiguous because java-tron can return it
after pending-pool insertion. A rejection on a resend cannot erase earlier
uncertainty. The polling deadline is the later of the normal observation budget
and signed expiry plus confirmation grace, subject to RPC timeouts. If inclusion
remains unresolved, the actor fails with `ErrSimTxUncertain`; expiry does not
permit an automatic replacement. Production receipt/scanner fixes are separate
from this baseline.

The XRP fixture retains its local ledger history so standby scanners can catch
up after activation and observe migration transactions.

## Validation

Run the startup and rendered-Compose contract checks from the repository root:

```sh
make test-mocknet-startup test-xmr-readiness-config
```

These tests do not start a cluster or wait for live readiness. They check all
three profiles, inherited authentication/network settings, signer seed routing,
image pins, request limits, and standard mocknet timing on all five pairs.

On a descendant containing S00's keygen changes, run:

```sh
PARALLELISM=4 make test-simulation-xmr-readiness
```

This selects `seed,bootstrap,xmr-post-churn` and forces
`TEST_SIMULATION_REQUIRE_XMR=1`, passed to the runner as
`XMR_POST_CHURN_REQUIRED=1`, even when a caller sets the former to `0`.
It resets the selected Compose project's volumes: use a dedicated project.

The full `make test-simulation-cluster` runs startup/configuration checks and
requires XMR by default with `TEST_SIMULATION_REQUIRE_XMR=1`. Its default stages
include ordinary and memoless swaps, `xmr-spent-refs`, shared cluster churn,
post-churn XMR verification, solvency, and Ragnarok. The CI job explicitly passes
the requirement on the Make command line, so an inherited environment value of
`0` cannot silently skip XMR. Branches inheriting these settings inherit that
coverage requirement, not a guarantee that their cluster tests pass.

For a deliberately optional local diagnostic run, explicitly set
`TEST_SIMULATION_REQUIRE_XMR=0`. Such a run, or a run with a reduced stage list,
is not a substitute for full cluster CI. The dedicated readiness target above
is also narrower than the full cluster run.

## Registration restart protection

Fresh mocknet validators atomically write `registering` to
`/root/.thornode/mocknet-registration.state` before initialization or registration.
Marker write failures stop startup.

A restart in the `registering` state resumes registration from committed chain
state. Before each of the four registration transactions (bond, node keys, IP
address, version), startup reads the signer account and the node records from
the peer API at one height. It skips a step that is already recorded for this
node and sends only the missing ones. A changed IP address is updated and a
newer registered version is kept.

Each transaction is signed with the account sequence from that same read. A
sequence can be used only once, so a transaction that is still pending, or that
committed before the client learned the result, cannot execute alongside a
retry. When the outcome is unknown or the sequence was already used, startup
reads committed state again before deciding whether to send. After a confirmed
transaction, it waits for the API to reach that height. If the node record at
that height still lacks the step, startup stops. Queries and retries are
bounded to 10 attempts each.

State that belongs to another setup stops startup with a
`conflicts with committed state` error: a node operated by another address,
different keys already set on this node, or these keys registered to another
node.

After all four steps are recorded, the marker becomes `registered`. A restart
then skips registration and resumes configuration rendering and normal startup,
including after a failed `render-config`. Existing initialized volumes without
a marker retain their normal startup.
