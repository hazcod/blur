# Ordered encrypted capture editions

Terminology: saved pages, notes and app activity are **captures**. This plan's historical filename is retained; encrypted format identifiers stay unchanged.

## Scope and current evidence

Continue parts3–4 of the [large-capture design](2026-11-13-large-document-capture-design.md).
Native continuation, streaming redaction and source mapping are implemented and
verified independently. Production capture is still unchanged. The previous goal
turn made source progress; the actual user Library startup failure remains unknown
pending its existing safe diagnostic. Do not repeat that request or access keys.

The complete deliverable here is a capture-owned bounded capture format plus
encrypted staging/publication, deduplication/visits, ordered reads, restart cleanup,
schema compatibility, deletion fences and bounded erasure. Search indexing,
production ingestion/activity, whole-capture reasoning/inspection and native
acceptance remain required parent work, not replaced by this deliverable.

## Decisions

Use immutable capture-owned parts and summaries, then stage only sanitized bytes
in the existing encrypted record boundary. Whole-capture observation records
retain a size ceiling; independent fragment sanitation changes redaction context.
No raw disk spool, dependency, new settings or model request is introduced.
Implementation remains inline under the user's existing autonomous instruction.

### Capture format

`NewCaptureBuilder(appID,domain,sourceKey string,at time.Time)` owns a mapped
redactor. SourceKey is a stable opaque64-hex source locator supplied by capture;
family identity hashes canonical app/domain/sourceKey. A random32-byte capture
nonce distinguishes staging attempts; it is excluded from edition identity.
The canonical header explicitly records redaction version2; the initial chain
and edition identity bind it. Metadata bounds apply before normalization.
Input calls remain64KiB; no total page cap. Capture inserts real value separators,
never range separators. Exact/opaque/OpenEnded source spans retain the mapped
stream contract in original logical UTF-8 capture coordinates. Native navigation
and UTF-16 locators must not be fabricated from those byte positions.

Canonical parts contain at most32KiB text and256 coalesced origin spans. Split
at scalar boundaries and origin-count boundaries, preserving all remaining text.
Coalesce across incoming batches so input call boundaries do not change parts.
Parts carry capture nonce, ordinal, sanitized offset, text, source spans and a
digest. A running chain binds part order and membership. Finish returns a summary
binding family, chain, part/byte totals, raw length, coverage and committed counts.
Coverage distinguishes complete reads from structural omissions. Abort emits no
pending content. Empty captures are not published.

Only the builder creates write-admissible parts and summaries. Fields are private;
copies/getters do not alias retained state. Canonical bounded authenticated-record
restore functions create read-only values, never values eligible for staging.
The store first authenticates bytes before restoration. Do not independently
re-redact fragments to validate them; a boundary changes regex context. Restoring
arbitrary caller bytes must never manufacture a write-admissible value.

Builder writes/Finish share the redactor transaction: sanitized partial buffers,
chain, ordinals, offsets and counts commit only after sink success. The sink gets
one batch of complete parts per callback. A batch is bounded by one64KiB input
plus existing bounded redactor buffers and fixed metadata expansion. Reentry is
rejected; Abort inside callbacks remains terminal; accepted callbacks commit even
if their context expires immediately afterward. Unknown sink errors are safe.

### Encrypted staging and atomic publication

Namespace all capture records separately inside authenticated encrypted extension
records; logical keys remain HMAC-derived. Staging nonce+ordinal locates a part.
Append only consecutive write-admissible parts from the same capture. Strictly
identical retries are idempotent; conflicting retries fail without replacement.
Each part append and its progress commit together in a bounded transaction.
Large batches may make private staging progress before an error; retries verify
the already staged prefix. No partial batch is visible as published evidence.

Publication compares the summary against staged chain/count/offset/max-origin
progress and checks deletion fences in the same transaction that publishes an
immutable edition descriptor. Edition identity includes canonical parts/maps and
coverage, not the random nonce; changing a secret's length can change original
positions even when visible text is unchanged. Deduplicate unchanged editions and
record each capture visit idempotently, retaining exact times in bounded pages.
Published descriptors own their canonical payload nonce; no shared part refcounts.
Unneeded duplicate staging payloads are cleaned in bounded resumable work before
normal publication returns. Publication commits first; a cleanup failure returns
the committed nonzero edition plus its error. Retrying the same summary checks
its receipt and resumes cleanup without adding a visit. Reopen covers a crash
between publication and cleanup.

Keep authenticated bounded linked catalogs for pending stages, published editions,
and editions per family. Directory links and descriptor publication commit
together. Reads return only published, live editions; ordered part reads validate
membership, digest, offsets and bounds. Metadata queries read bounded catalog
pages, not every database record. A cursor invalidated by deletion must report
that it changed, not silently report an empty complete result. Catalog cursors
bind the content ID to a fresh authenticated publication incarnation, independent
of the capture nonce, so even reusing a deleted capture cannot reuse its cursor.

### Recovery, deletion and compatibility

Discard unfinished staging on reopen before serving the store: raw recognizer
state cannot be resumed from sanitized prefixes. Cleanup progresses one bounded
record group at a time and persists its cursor with each mutation. Committed
editions survive. Failed cleanup prevents publishing partially recovered state.

Apply permanent all-source, family and edition deletion cutoffs at begin, append
and publication. Family forget records its cutoff before erasing older editions
and pending attempts, so a different older edition cannot resurrect a forgotten
page. New real captures after the cutoff follow existing admission semantics.
Do not use a missing primary as proof that no capture-owned rows exist.

Extend generic source erasure and Delete all/retention coordination to include
capture membership, parts, visits/receipts and capture projections. A successful
generic erasure must not leave capture text. Bound each step and resume after
context cancellation or restart; indexes/derivatives must be invisible while
their source is suppressed. Preserve newer captures where a conditional deletion
does not cover them. Test interruption at every meaningful stage and late jobs.

Raise the store to schema6 only together with these invariants. Migrate schema1–5
using authenticated version changes, leaving existing observation bytes readable
and their coverage honest. Older schema5 writers must reject the store. No user
store is migrated by this development task; the existing Downloads app stays5
until the entire usable capture/storage/recall path is ready for one stable build.

## Required proof

- >1MiB captures retain ordered Unicode text, tail facts and split-secret
  protection. Canonical chunks and edition IDs ignore input piece boundaries.
- Authenticated read restoration cannot bypass write admission. Invalid bounds,
  digests, ordinals, metadata, source maps, counts or mixed capture nonces fail.
- Sink failures/deadlines/retry, no-output buffers, callback Abort/reentry and
  caller mutation preserve accepted consumption exactly once.
- Unpublished/mixed/incomplete/conflicting stages never appear in ordered reads.
  Publication is atomic; identical captures deduplicate and retain visits.
- Encrypted records expose no plaintext fixture or locator on disk. Reopen
  discards stages in bounded steps while retaining published editions.
- Family/source/all erasure, newer-capture preservation, stale begin/append/finish,
  interrupted cleanup/reopen and schema5 rejection are directly tested.
- Full tests, affected race, vet, cgo-disabled build and cross-target compilation,
  then one fresh read-only final review and one material RED/GREEN pass.

## Delivery boundary

Keep the stable bundle path, identity, sandbox and settings. Do not build or
launch a prerequisite app. Full capture/recall/native acceptance and the actual
Library startup diagnosis remain open. The inherited version1 Unicode privacy
edge needs a migration-safe audit before the next user-facing release.
