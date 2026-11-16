# Published capture search and exact visits

## Intent and evidence

Connect actual encrypted published captures to bounded searchable parts. A tail
fact beyond1MiB must survive save/restart and be retrievable with its original
source spans. Later visits reuse content indexing and support exact date windows.
The parent seven-part large-capture design remains binding, including production
native ingestion, whole-capture Ask/Search/MCP, inspection and native acceptance.
The last goal turn was progress: isolated shared search trees and verified gates,
with no material review findings. The installed app is unchanged; actual Library
startup diagnosis still awaits the original safe diagnostic. No repeat request,
user store/key access, GUI lifecycle or prerequisite app replacement is allowed.

## Implementation choice

Reuse the isolated tree, its64-term pages and the original-Tx facade. Avoid a
second index implementation and avoid treating parts as unrelated observations.
Publication appends one edition to a bounded encrypted work catalog, never loops
through its parts. Each work round advances one64-term part batch and at most one
legacy visit; one bounded backfill enqueue transaction may precede it. No total
capture/term/visit ceiling. Cancellation stops before the next bounded round.
This is the builder's call under the existing autonomous execution instruction.

Add omitempty mutable work fields to the authenticated edition descriptor:
IndexVersion(0legacy/1current), IndexPart(completed parts), IndexVisits(indexed
visit prefix), IndexQueued and IndexPrev/IndexNext. Canonical summary/content IDs
and original hash/wire namespaces stay unchanged. Pending catalog and one
backfill Next pointer live in the same encrypted capture namespace. Work fields
must validate bounds, self-links and legal legacy state. A nonqueued partial
edition may be suppressed by permanent fences; a newer admitted real visit can
requeue it. Fully indexed unchanged content never reindexes on a new visit.

Raise the supported writer schema to7 before producing the new index rows.
Authenticated1–6 metadata migrates atomically; old6 rejects7. Historical primary
bytes and capture payloads stay readable, without pretending old captures had
unrecorded visits. Open initializes index state and a bounded backfill cursor;
background work walks published catalog metadata, preserving published data.
When an edition at the backfill Next cursor is erased, advance the cursor to its
published successor atomically before unlink. New publications queue themselves.

## Part work and transactional ownership

Part source IDs hash edition ID, publication incarnation and ordinal, so erased
and recreated editions cannot accept old work/citations. The first part batch
atomically creates a bounded source map and paged reverse state; source map has
edition/publication/index/text-byte count. Load authenticated sanitized parts;
never re-redact fragments or manufacture write admission. Compute tokens outside
the write transaction; recheck live edition/incarnation/current progress inside
it. A concurrent worker can advance only freshly verified progress. Reuse one
extracted term-batch helper for observations and capture parts. Capture work
never enters the observation-only outer Store.exactInline optimization.

## Exact visit lookup

Use the same capture-scope tree under a new visit kind, keyed by edition ID.
Encrypted postings sort by inverted normalized64-bit nanoseconds plus24 bytes
of a nonce digest, newest first. Physical keys remain HMAC-derived; no plaintext
time or locator appears in them. An authenticated bounded visit map retains full
capture nonce/time/incarnation and cross-checks the exact existing receipt.
Each newly committed visit publishes its time posting/map atomically. If its
prefix is already indexed, advance IndexVisits too; otherwise background work
fills old visits idempotently. Duplicate publication receipts never add visits.

A bounded lower-bound tree iterator seeks to end-minus1ns, then returns the
newest actual visit in [start,end). It must traverse split/deferred sibling
branches correctly without scanning the edition's entire history. All metadata,
tree nodes and visit proofs count against the caller's byte/node budget. Budget
exhaustion is typed, never an empty complete answer. Preserve legacy iterator
behavior when there is no lower bound. Zero, negative Unix times and DST-derived
UTC windows must retain exact boundary semantics.

## Erasure and recall

Existing global deletion gate/journal remain the only coordinator. Phase8 drains
one part's reverse pages/proofs/postings, then deletes its source map and payload
atomically before advancing. Phase9 erases bounded visit rows, time postings/maps
and receipts with a persisted row cursor. Phase11 unlinks pending work and
backfill references before descriptor/catalog removal. All source/family/all/
retention paths and reopen recovery use this same path; private duplicate-stage
cleanup never erases canonical index entries. No erased job can recreate them.

Search uses the existing ranking engine with a same-snapshot metadata matcher.
Only live edition members qualify before top-K selection, including exact visit
window checks. Public hits retain edition/incarnation/ordinal/score/bytes and an
actual matching visit timestamp. Hydration checks incarnation and full membership
and returns authenticated read-only summary/part with original spans. Pending
edition/backfill work is disclosed even before its first part posting exists.
Scoped raw observation bootstrap/loader/time APIs are not capture adapters.

## Required proof and remaining delivery

Real encrypted fixtures cover >1MiB Unicode/tail/split secrets, restart midway
through indexing, >128 terms in a part, deterministic weighted order/scores,
mixed original-Tx manifest verification/reopen, duplicate visits with unchanged
part rows, out-of-order/many visits and exact boundaries, callback/commit failure,
concurrent index/delete/recreate, bounded interrupted erasure/reopen, late jobs,
schema6 rejection and migration/backfill of authenticated6 captures. Test both
clear directions/multi-batch fixtures and targeted record replay as integration
requires. Full tests/races/vet/CGO0/cross plus one fresh final read-only review.
Do not claim native capture/Ask/UI or the actual installed storage error fixed
by repository-level search proof. Those remain required parent work.
