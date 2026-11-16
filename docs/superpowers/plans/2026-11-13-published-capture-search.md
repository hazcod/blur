# Published Capture Search Implementation Record

**Current status:** Production macOS capture, durable publication, resumable
part indexing, public Search and Ask retrieval are wired in the worktree. The
full Go suite and the large-capture tail-to-Ask integration case pass. Remaining
acceptance is runtime coverage across real applications and their Accessibility
trees, including large pages, cancellation, and partial-content reporting.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline. No implementer agents; one final read-only review.

**Goal:** Make published encrypted capture parts searchable with resumable work, exact visits, complete deletion, and production ingestion/recall wiring.

**Architecture:** One edition work receipt drives bounded shared-tree term batches and visit backfill. Same-snapshot source matching, lower-bound visit lookup and existing erasure journal maintain live membership and correct dates.

**Tech Stack:** Existing Go/encrypted bbolt, no dependency.

**Spec:** `docs/superpowers/specs/2026-11-13-published-capture-search-design.md`

## Global Constraints

- Schema7 rejects old6 before new rows are produced; metadata-only1–6 migration preserves historical primary/capture bytes. Build the standard app from this active worktree and keep its existing bundle ID, signing identity, and user store.
- One work round: at most one64-term part batch plus one old visit, with at most one preceding backfill enqueue transaction; maxBatches1..1024. Each transaction retains existing physical bounds; no total content/visit cap.
- Keep capture canonical format/namespaces/content identity, scoped tree0x41 vs observation0x40, original live Tx/manifest/gates and existing native prep order. No raw spool, fragment re-redaction or observation-only exactInline dispatch for capture work.
- Shared dirty worktree retained; no commit/merge/reset, user data/key/settings access or GUI/lifecycle/prerequisite app build.
- Runtime acceptance across a representative set of real applications, large documents, and Accessibility tree behaviors remains open; repository integration tests do not prove every app exposes complete text.

## Review Focus

- A cached part job resumes after its edition is deleted/recreated with identical content; old incarnation must neither index nor hydrate the new one.
- An unchanged page accumulates many out-of-order visits; time-window queries must seek exact visits with bounded reads and preserve start-inclusive/end-exclusive behavior.
- A part is only partly indexed when erasure/restart happens; no posting, proof, part map, time map or work receipt may survive successful deletion.
- A legacy6 capture has no index state and is deleted while its backfill cursor points to it; remaining captures still backfill once without blocking new captures.
- Mixed original-Tx writes must preserve the full authenticated manifest, and weighted scoped results must preserve deterministic ordered IDs/scores after reopen.

## Task 1: Queue published work, exact visits, bounded indexing and erasure together

**Files:** create `internal/storage/durable/capture_index.go`, `capture_index_records.go`, `capture_index_visits.go`, `capture_index_test.go`; modify `captures.go`, `capture_records.go`, `capture_visits.go`, `capture_cleanup.go`, `capture_erasure.go`, `source_erasure.go`, `store.go`, `index_pending.go`, `index.go`, schema tests and local build docs.

**Interfaces:** `(*Store).CaptureIndexPending(context.Context)(bool,error)`; `IndexPendingCaptures(ctx context.Context,maxBatches int)(bool,error)`; private `(*searchIndexTx).indexSearchTermsBatchLocked(id,app,domain string,sourceBytes int,terms map[string]byte,ordered []string)(bool,error)` reuses observation batch logic. Private edition queue/source-map/backfill/visit helpers and schema7 are owned here. New visit kind0x13 uses existing tree mutation and authenticated visit maps; no date query API yet.

- [ ] Write genuine RED tests: publication queues only committed editions, bounded part progress survives restart, dense part terms index all pages, no raw/locator on disk, late/suppressed jobs, duplicate exact visits without reindex, mixed-source manifest reopen, source/family/all/retention deletion including interrupted indexed-part/visit erasure, schema6 authenticated rejection/migration/backfill and cursor deletion. Typed fail-closed scaffolds only if needed to compile tests.
- [ ] Implement queue/progress/maps/visit postings and shared term-batch helper. Add complete bounded erasure hooks and schema7 boundary before publication enables these rows. Initialize bounded legacy backfill and preserve newer real visits.
- [ ] Run `go test ./internal/storage/durable ./internal/deletion ./internal/capture -count=1`; Expected: PASS; inspect full output before Task2.

## Task 2: Live capture search, exact visit seeking and incarnation-safe hydration

**Files:** create `internal/storage/durable/capture_search.go`, `capture_search_test.go`; modify `index.go`, `index_tree.go`; extend Task1 tests only where needed.

**Interfaces:** `CapturePartHit{SourceID,EditionID,Publication string; PartIndex int64; Score,Bytes int; CapturedAt time.Time}`; `CaptureSearchPage{Hits []CapturePartHit; Rows,Bytes,Nodes int; HasMore,IndexingPending bool}`; `CapturePartEvidence{Summary capture.CaptureSummary; Part capture.CapturePart; Publication string}`; `SearchCaptureParts(ctx context.Context,q search.ExactQuery)(CaptureSearchPage,error)` (reject nonempty ScopeSourceIDs until an explicit edition scope contract exists); `ReadCapturePart(ctx context.Context,id,publication string,index int64)(CapturePartEvidence,error)`. Same-snapshot matcher receives reader/sourceID/sourceBytes/query/page and runs before ranking. Private lower-bound iterator uses canonical33-byte order keys; metadata reader charges actual physical bytes/nodes.

- [ ] Write RED tests for saved >1MiB tail retrieval/Unicode/source spans/reopen, incomplete work disclosure, weighted deterministic IDs/scores after reopen, app/domain/time/exclusion/min-term constraints, zero/budget/cancel requests, many/out-of-order/sub-hour/negative/epoch/DST visits, split/deferred visit trees, concurrent deletion/recreation and stale hydration.
- [ ] Implement bounded source matching, visit seek/proofs and hydrated membership. Preserve observation query/iterator behavior and apply every read to existing query budgets.
- [ ] Run `go test ./internal/storage/durable ./internal/search ./internal/client -count=1`; Expected: PASS; inspect full output before Task3.

## Task 3: Full gates, one final review and full-parent handoff

- [ ] Run full tests, affected races, vet, CGO0build, cross-build/test compile, gofmt/diff checks; Expected: exit0, full output read.
- [ ] Request one fresh read-only final review of the source delta and all Review Focus cases. Material findings get one genuine RED/GREEN fix pass plus full relevant gates; no re-review loop.
- [ ] Record remaining cross-app Accessibility acceptance, semantic retrieval coverage, legacy Unicode privacy audit, and native performance measurements. Preserve the exact installed signed app and user store; leave the broader product goal active.
