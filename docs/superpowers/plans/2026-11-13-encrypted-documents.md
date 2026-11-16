# Encrypted Captures Implementation Plan

Terminology: saved pages, notes and app activity are **captures**. This plan's historical filename is retained; encrypted format identifiers stay unchanged.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline. No implementer agents; one fresh final read-only review.

**Goal:** Persist complete ordered sanitized capture editions with atomic publication, exact visit history, restart recovery and deletion-safe schema6 compatibility.

**Architecture:** Capture builds immutable canonical parts from the mapped redactor. Durable storage stages only those parts, publishes a checked descriptor atomically, and uses bounded encrypted catalogs/journals for cleanup and erasure.

**Tech Stack:** Go standard library, existing privacy and encrypted bbolt layers, no new dependency.

**Spec:** `docs/superpowers/specs/2026-11-13-encrypted-documents-design.md`

## Global Constraints

- Full seven-part parent scope remains binding. No production capture/recall wiring or app replacement for this storage-only phase.
- Input64KiB, part text32KiB,256 origins per part, encoded part512KiB max; no total capture-size ceiling. Every bound splits or yields, never drops remaining text.
- Write-admissible capture values are distinct from authenticated read restoration. Never re-redact independent fragments to validate storage.
- Canonical part/hash identity ignores random staging nonce/input call boundaries and includes maps/coverage. Private staging retry is idempotent; visibility is publication-only.
- Apply all/family/edition fences; successful generic deletion includes captures. Cleanup bounded/resumable, no abandoned plaintext or staging-as-evidence.
- Schema6 rejects old writers; existing observations stay readable. Shared dirty branch retained; no indiscriminate commit/merge/clean; no user store/key/settings/native lifecycle actions.

## Review Focus

- Output from several pending redactor stages exceeds one normal input's size; staging retry after a partial private batch must not duplicate publication or lose the tail.
- A secret changes length while sanitized text is identical; edition identity and maps must not reuse stale locations.
- A read-restored part is copied and submitted for staging; structural validity must not grant write admission.
- A family is forgotten while an older different edition is staged; late append/finish and reopen cannot resurrect it, while newer captures retain normal semantics.
- Thousands of parts/visits are deleted or abandoned across restart; work and metadata remain bounded and no generic Delete all success leaves text.

## Task 1: Canonical capture builder and record format

**Files:** create `internal/capture/capture.go`, `capture_builder.go`, `capture_codec.go`, `capture_test.go`.

**Interfaces:** `NewCaptureBuilder(appID,domain,sourceKey string,at time.Time)(*CaptureBuilder,error)`; `Header() CaptureHeader`; `Write(ctx,raw,sink func(context.Context,[]CapturePart)error)error`; `Finish(ctx,coverage CaptureCoverage,sink)(CaptureSummary,error)`; `Abort()`. Header/part/summary expose read getters, structural Validate and write-only ValidateForAdmission; bounded MarshalAuthenticatedCapture*/RestoreAuthenticatedCapture* retain read-only restoration. Parts expose Index/Offset/Text/Origins/Digest/CaptureID, header exposes FamilyID/CaptureID/AppID/Domain/SourceKey/CapturedAt/InitialRoot, summary exposes ContentID/Root/PartCount/Bytes/RawBytes/Coverage/Counts/Header; `AdvanceCaptureRoot(root string,part CapturePart)(string,error)` shares canonical membership hashing.

- [x] Write tests for >1MiB Unicode/tail/split secrets, piece-independent canonical parts/edition IDs, changed-secret-length maps, nonce distinction, bounds/origin-density splitting, safe getters, malformed records/read-only restore, sink retry/deadline/no-output/Abort/reentry/cancellation/overflow. Observe behavioral RED with a typed fail-closed scaffold.
- [x] Implement mapped redaction, canonical bounded buffering/chain, transaction ownership and deterministic bounded record codecs with separate admission capability.
- [x] Run `go test ./internal/capture ./internal/privacy -count=1`; expected PASS; read full output and task-done before Task2.

## Task 2: Encrypted staging, checked publication and ordered reads

**Files:** create `internal/storage/durable/captures.go`, `capture_records.go`, `capture_visits.go`, `captures_test.go`; consume Task1 types.

**Interfaces:** `BeginCapture(ctx,header)error`; `AppendCaptureParts(ctx,header,[]capture.CapturePart)error`; `PublishCapture(ctx,summary)(CaptureEdition,error)`; `GetCapture(ctx,id)(CaptureEdition,bool,error)`; `ReadCaptureParts(ctx,id,after int64,limit int)(CapturePartPage,error)`; `ListCaptures(ctx,after string,limit int)(CapturePage,error)`; `ListCaptureVisits(ctx,id,after string,limit int)(CaptureVisitPage,error)`. Bounded encrypted linked catalogs, same-capture progress, canonical chain/summary checks and exact idempotent visits. Cleanup interfaces produced for Task3 must be documented in its brief before execution.

- [x] Write genuine failing tests for encrypted >1MiB full ordered tail reads, absent unfinished stages, retries/conflicts/mixed captures, atomic checked publication/dedup/visits, changed maps, bounded paging, corrupt membership and read-only admission rejection.
- [x] Implement one bounded part/progress transaction per append, descriptor/catalog publication and receipt atomically, exact bounded visit pages and safe ordered reads. No whole-capture buffering.
- [x] Run `go test ./internal/storage/durable ./internal/capture -count=1`; expected PASS with current schema5 until Task3 invariants and migration are complete.

## Task 3: Restart cleanup, deletion integration and schema6

**Files:** create `internal/storage/durable/capture_cleanup.go`, `capture_cleanup_test.go`, `capture_schema_test.go`; modify `store.go`, `source_erasure.go`, `deletion_cutoffs.go`, `internal/deletion/service.go` and tests only as required.

**Interfaces:** finalize Task2 cleanup interfaces in this task's brief. Store open resumes cleanup before publication; generic source deletion erases capture-owned rows; family forget and Delete all record permanent cutoffs before bounded cleanup. Schema6 migration retains old envelopes and rejects schema5 writers.

- [x] Write tests for discard-on-reopen, partial append cleanup, interrupted erase/restart, source/family/all erasure, stale begin/append/publication/different edition, newer captures, generic Delete all text removal and schema5 rejection/old observation readability. Observe genuine RED.
- [x] Implement bounded authenticated cleanup journals/catalog updates, fence admission and generic deletion hooks. Raise OpenSchemaVersion to6 only with passing invariants; update local build documentation accurately without replacing the app.
- [x] Run full durable/deletion/client/capture suites and affected races; expected PASS, inspect complete output.

## Task 4: Review, full gates and full-parent handoff

- [x] Run `go test ./...`, affected race tests, `go vet ./...`, `CGO_ENABLED=0 go build ./...`, `make cross-build cross-test-compile`, gofmt and diff check; expected exit0, read every output.
- [x] Request one fresh read-only final review with spec/Review Focus/parent constraints. Fix material findings once via genuine RED/GREEN; rerun all relevant gates, no re-review loop.
- [x] Record outstanding private part indexing, production capture/activity, whole-capture Ask/Search/MCP/inspection, legacy privacy audit, native acceptance and actual Library startup diagnostic. Preserve signed Downloads app until the complete usable path is ready.
