# Text data integrity follow-through

User direction: keep working on text acquisition, processing and recall; fix
material functional gaps without returning to voice or cosmetic work.

## Constraints

- Preserve all existing dirty changes, stored memory and compatible readers.
- Reproduce each defect against temporary storage before changing production.
- No UI, production database writes, app lifecycle actions, commit or push.
- Build only the canonical app with its exact certificate and requirement.
- Root owns integration/docs; sync and correction owners have disjoint files.

## Tasks

- [x] Repair sync progress so imported future captures cannot skip local text.
- [x] Make export publication/retry preserve immutable consumed segment identities
  across local state-write failure, restart and concurrent flushes.
- [x] Preserve identity/sequence authority across folder changes and prevent
  publication after successful disable or configuration replacement.
- [x] Make direct entity answers honor current Library corrections with their
  app/date/deletion authority; cover offline and controlled provider paths.
- [x] Audit remaining text-only source/index/compaction continuation for concrete
  accepted-data loss or false completion, excluding already disclosed budgets.
- [x] Repair permanently missing tokens across capture-part boundaries with a
  resumable token stream and authenticated original-span evidence. Reindex old
  captures without rewriting their parts or inventing fragment matches.
- [x] Protect new source-linked export/span metadata with schema 9. Prove atomic
  schema-7/8 admission upgrades, ciphertext preservation and old-reader rejection.
- [x] Remove the stale session-total budget from coordinated archive deletion;
  retain bounded pages, monotonic cursor checks and cancellation. A service-level
  page adapter reproduces failure at row 33,025; the full deletion suite passes.
- [x] Page native deletion preparation under the existing exclusive erasure
  gate, preserving preparation before journal admission. A real 513-part,
  16,777,224-byte capture exposed a transaction-total ceiling.
- [x] Page sync receipt acknowledgements idempotently while retaining the
  sealed pending segment until every receipt and final state commit succeed.
  A real 512-item batch exposed the physical operation budget.
- [x] Preserve unrelated reminders while cancelling erased-source requests,
  including interrupted-journal recovery and stale source/revision checks.
- [x] Persist multiple original range projections emitted by a single Unicode
  normalization segment, deduplicating only identical spans and draining them
  before physical part advancement. Cover interruption and erasure.
- [x] Replace the 32-character passive-capture threshold with non-empty
  sanitized-text admission. Preserve short terminal commands/status lines and
  browser text; retain privacy, empty-content, deduplication and commit checks.
- [x] Independently review the combined repair, run the full suite/vet and
  relevant race checks, update the gap report and build the canonical signed app.

The sync owner will record the detailed recovery design after tracing current
storage and reproducing the failures. This plan does not authorize protocol or
storage-format incompatibility; any required migration needs preservation tests.

## Recovery decisions

Sync keeps version-1 peer segments. A keyed, authenticated per-source receipt
matches the writer generation and exact original-order fingerprint. It replaces
capture timestamps as export authority. One locally sealed pending segment is
durable before immutable publication and replays exact bytes after interruption.
Folder changes rotate writer identity; cancellation and a configuration fence
cover both replay and fresh publication. Multipart capture transport remains a
separate existing protocol gap, rather than being silently treated as synced.

Cross-part indexing preserves all original bytes and source identities. A bounded
token stream carries digest/range state across arbitrary parts; completed seam
tokens receive authenticated range receipts that read from the originals.
Queryable spans exceeding a read envelope report a budget error. Old index
formats rebuild through persisted continuation, with deletion covering spans and
partial work. Artificial word-length clipping is excluded.

These new private namespaces require schema-9 reader admission so schema-8
deletion cannot orphan their source links. The migration changes authenticated
metadata atomically and retains old ciphertext. Production memory remains
untouched until a later launch; these tests use temporary stores.

## Verification record

The first full run exited 1 for four obsolete test expectations across client
and durable storage; its log is retained. Test-only corrections passed both full
package reruns (491.041s and 515.998s). The later non-empty-text policy passed
all four affected packages and a further complete client run (308.375s).
Whole-repository vet, relevant races, independent review and exact canonical
build signing checks pass. The gap report records the source fingerprint and
remaining scope; the original broader parity goal is not declared complete.
