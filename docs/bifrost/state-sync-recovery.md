# Startup State Recovery

Bifrost starts one best-effort collection when this node enters Active, including
the first status check after restart. The ordinary deck tick schedules each group
independently of activation notices and scanner rollback.

## Bounds

Each activation permits at most **50 launched peer attempts** in a fixed **two-hour
window**. Failed dials, busy/reset peers, empty replies, partial imports and timeouts
all consume an attempt. Neither bound resets when another group starts. The window
starts at activation, including time spent joining work from an earlier activation.
These are
operational limits, not evidence-completeness guarantees. Fifty attempts allow
five default ten-peer groups; two hours permit several slow transfers using the
default 30-minute StateSyncTimeout, with the last transfer capped by the remaining
window. Backoff and time spent without eligible peers consume that window.

Only one local group runs. Its fanout is at most min(AskPeers, remaining requests,
fresh eligible peers); the default AskPeers is ten, and the fixed request limit
also caps an unusually large configured fanout at 50. Each attempted peer occupies
one outgoing stream at most, including during dial/processing. Peers never attempted
in this activation precede reuse in each selection pass, including peers discovered
after reuse began. Between groups equal jitter chooses
half to all of a 5-second exponential delay, capped at one minute. The ordinary
deck tick can add dispatch latency; no separate retry scheduler or sleeping worker
owns backoff.

With five full groups, the four base delays are 5, 10, 20 and 40 seconds:
**37.5–75 seconds of total backoff**, plus deck-tick latency and transfer time.
The one-minute cap is not reached in that case. Smaller peer groups can require
more rounds and reach the cap. The two-hour window is a ceiling, not a promise to
keep retrying for two hours; fast empty/busy responses can exhaust all 50 attempts
much sooner.

The original StateSyncTimeout begins before dialing and persists through stream
I/O, import processing, cancellation-aware lock waits and queries. It is capped
by the collection deadline. Ordinary P2P frame timeouts still apply. The
processing-dependent header/final ACKs use the original transfer deadline. A
serving node admits at most two copied export snapshots, with one per peer.
The receiver acknowledges each frame promptly and processes batches asynchronously,
so a slow import does not delay the next frame ACK from an older sender. The final
ACK waits for all batch workers. On transport failure or cancellation, workers are
cancelled and joined before returning the import result or closing observer storage.

This version does not impose the later bounded receiver's batch, item or signature
count limits. Asynchronous workers and retained payloads can still grow with the
snapshot size. The request and export-slot limits are not total memory bounds.

For N restarting nodes, outgoing requests are bounded by 50N per activation,
with default per-node concurrency ten. Imports still perform the existing signature
verification and THORNode calls; retries can repeat that work even for deduplicated
evidence. Peer selection is linear in the eligible peer count per round, with an
additional scan when selecting peers for reuse. The controller retains at most 50
attempted peer identities, counters and one group result; it queues no decoded
snapshots.

## Outcomes and cancellation

An empty response is a valid completed transfer. It does not stop another running
peer. A validated transfer that fails before its complete final handshake remains
failed/partial; evidence already imported is preserved and deduplicated on retry.
A group with no completed non-empty snapshot schedules another group within both
bounds. A completed non-empty snapshot ends new rounds after the rest of that
group's imports finish within their original deadlines. Shutdown, departure from
Active or the collection deadline can cancel those imports.

A wire item counts as non-empty only when an existing handler accepts at least
one attestation, including a valid duplicate already retained locally. A duplicate
must match the retained signer, signature and signed payload; a bucket-key match
alone is insufficient. A definitive
admission rejection, invalid signature, invalid fee or price-feed fields, or an object with no
attestations does not count. A rejected-only snapshot completes as empty and can
therefore be retried within the normal limits.

A temporary failure before evidence is stored, including an XMR per-call deadline
or an admission-storage read error, makes the transfer incomplete. Later items and
frames still import while the original transfer context remains live. Cancellation
of that context or a transport/protocol error ends the attempt. A finished wire
exchange closes normally even if local evidence remains incomplete and eligible
for retry. A later THORNode submission or spent-reference delivery failure does
not undo a successful import;
existing delivery and recovery paths retain responsibility for that evidence.

Each group logs one summary. The final collection log includes completed-empty,
completed-non-empty, failed and partial counts, requests and the stop reason.
Partial is a subset of failed and means at least one wire item had accepted
evidence before the incomplete attempt ended; accepted duplicates count. Empty-only,
mixed empty/failed, partial-only and no completed snapshot have distinct outcomes.
No completed response establishes globally complete evidence, and live gossip
need not replay every missing historical attestation.

A later bounded-receiver serving peer also resets requests when its local active-validator list does not
yet include the requester. This can happen after the serving peer restarts or
while membership refresh lags behind the requester's activation. These resets
count as failed attempts under the same group-level retry rules. A later
successful membership refresh can let another bounded round import evidence;
refresh timing and recovery before exhaustion are not guaranteed. Genuinely
inactive requesters remain rejected by those peers. This version retains S14a's
export admission policy and does not add that membership gate.

## Deliberate recovery after exhaustion

After suitable reviewed peers are serving retained evidence, **restart Bifrost**
using the deployment's existing restart procedure. A fresh service's initial
Unknown-to-Active status check arms a new bounded collection. Restarting does not
reconstruct evidence no peer retains. A genuine departure from Active followed by
reactivation also starts a fresh collection after the old group has joined.

Ordinary deck ticks, signing resumption and a serving peer's upgrade do not re-arm
an exhausted collection. Arrange a deliberate restart once serving peers are
ready if the initial bounds expired. Signing inactivity around deployment can
reduce pending evidence but guarantees neither small snapshots nor complete
recovery. No automatic upgrade detection, operator endpoint, resume protocol or
wire negotiation is provided.
