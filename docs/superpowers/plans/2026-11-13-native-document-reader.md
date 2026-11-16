# Native Document Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Continue inline under the user's autonomous instruction; one fresh final review, no implementer agents.

**Goal:** Provide a bounded, continuable macOS AX document reader for the full large-document capture path.

**Architecture:** A private DFS cursor owns paged child references and one immutable text lease. Caller guards and current node policy surround every text range; rounds yield normally while owner cancellation closes all references.

**Tech Stack:** Go, existing purego AX/CoreFoundation bindings, standard library tests.

**Spec:** `docs/superpowers/specs/2026-11-13-large-document-capture-design.md`

## Global Constraints

- This prerequisite does not complete large-page ingestion or whole-document recall. The parent design's seven parts remain required.
- Raw interfaces/text remain private to `internal/capture`; no persistence, model call, user setting or new dependency.
- Child pages at most 32; maximum depth 64; one text lease; at most 2,113 retained AX nodes under these constants.
- Each round requires valid existing `Limits`, `MaxBytes >= 4`; yield on byte/node/time budget. Owner cancellation/deadline is terminal.
- Preserve DFS text order; never insert a separator between ranges of one value. The consumer receives a `first` flag for each nonempty value and inserts its logical separator later.
- Guard the selected path before/after each value range. Recheck sensitivity and role before reading; ignored values are skipped, sensitive subtrees are skipped.
- Consumers receive the round context and consume a complete piece on success or leave it unconsumed on error. A round timeout retains that pending piece for validated retry; owner cancellation is terminal.
- Same stable signed app policy applies. Do not replace it for this unconnected prerequisite, launch native apps, read private user memory, or change TCC/signing.
- Existing shared dirty branch: do not create an indiscriminate commit or revert unrelated edits. Record verified progress in this plan's ignored ledger.

## Review Focus

- A selected node or its ancestor becoming sensitive between ranges must prevent the next raw range read. Off-path descendants are not rescanned; the retained value is an immutable snapshot, not a fresh read of a newly added child.
- The parent count can remain unchanged while a selected child is replaced; identity/path validation must reject before the next range.
- A supplementary Unicode scalar at a byte budget boundary must not split or create a no-progress continuation loop.
- A timeout/error after prior emitted text must release all remaining child/text references exactly once; it is not proof of a complete document.
- Broad/deep/cyclic trees must stay within active-reference and per-round work bounds without a document-size ceiling.

## Task 1: Common cursor and lifecycle

**Files:** create `internal/capture/ax_document.go`, `internal/capture/ax_document_test.go`.

**Interfaces:**

- `axDocumentNode` embeds `axTreeNode` and adds `childrenPage(context.Context, int, int, int) ([]axTreeNode, int, error)`, `childMatches(context.Context, int, int, uintptr) (bool, error)` , `openText(context.Context) (axDocumentText, error)` and `sameElement(axTreeNode) bool`.
- `axDocumentText` provides `read(context.Context, int) (string, bool, error)` and `release()`.
- `newAXDocumentCursor(root axDocumentNode, maxDepth int, guard func(context.Context) error, ignored func(string) bool) (*axDocumentCursor, error)` takes ownership even when arguments are rejected.
- `(*axDocumentCursor).read(ctx context.Context, limits Limits, emit func(context.Context, string, bool) error) (axDocumentProgress, error)` returns `Done`, `StructureLimited`, `Bytes`, `Nodes`; `first` is true on the first nonempty range of each value.
- `(*axDocumentCursor).close()` is idempotent. The cursor is owned by one capture job and is not concurrently mutated.

- [x] Write RED tests for content beyond 1 MiB with rounds of 64 KiB/17 nodes, exact DFS order, 130 siblings across pages, sensitive/ignored branches, guard and path replacement before the second range, cancellation/error cleanup, Unicode boundaries, depth/cycle coverage, invalid budgets and exact ownership counts.
- [x] Run `go test ./internal/capture -run 'TestAXDocument' -count=1`; expected behavioral RED after the minimal fail-closed private interface scaffold, before traversal implementation.
- [x] Implement the private interfaces/cursor, explicit stack ownership transfer, bound page size, ancestor-cycle detection, immutable value continuation and per-round step/time budget.
- [x] Run the same tests; expected PASS. Record RED/GREEN and retained-reference evidence in the ledger.

## Task 2: Native immutable text leases

**Files:** modify `internal/capture/pipeline_darwin.go`; create `internal/capture/ax_document_darwin.go` and `internal/capture/ax_document_darwin_test.go`.

**Interfaces:** `(*nativeAXNode).openText(context.Context) (axDocumentText, error)` implements Task 1. Bind `CFStringCreateCopy` in `nativeAXAPI`; `nativeAXDocumentText.read` delegates to existing scalar `copyCFStringRange` with a retained immutable value and UTF-16 offset.

- [x] Write RED tests with the existing fake AX/CoreFoundation API for one copy, many ranges, supplementary characters, nonstring/no-value, cancellation and exact release on every error/close path. Assert mutable original changes do not change the retained copy.
- [x] Run `go test ./internal/capture -run 'TestNativeAXDocument' -count=1`; expected behavioral RED before implementing the native method.
- [x] Add the binding, native lease and method; keep existing `value` and production capture selection unchanged.
- [x] Run the targeted tests and complete capture tests; expected PASS.

## Task 3: Review and proof

- [x] Run `go test ./...` (including capture and ingest), `go test -race ./internal/capture`, `go vet ./...`, `CGO_ENABLED=0 go build ./...`, and `make cross-build cross-test-compile`; expected exit 0. Add missing plan-implied tests discovered during implementation.
- [x] Use requesting-code-review for one fresh read-only review of the cursor/native leases and parent design constraints, with Review Focus verbatim and ledger rulings. Fix material findings in one RED/GREEN pass and rerun the complete relevant gates; no re-review loop.
- [x] Record the full large-document path as still unfinished. Leave the current signed Downloads app intact and resume streaming redaction design/implementation next.
