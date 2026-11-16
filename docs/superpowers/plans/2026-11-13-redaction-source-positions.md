# Redaction Source Positions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline. No implementer agents; one fresh final read-only reviewer.

**Goal:** Preserve truthful original source positions through bounded streaming redaction, including length-changing replacements and oversized fields.

**Architecture:** Recognizers emit local input coordinates; bounded metadata queues compose those coordinates through the existing cascade. The existing transaction commits sanitized text, counts, queues and offsets together.

**Tech Stack:** Go standard library, existing regexp/syntax recognizers, no new dependency.

**Spec:** `docs/superpowers/specs/2026-11-13-redaction-source-positions-design.md`

## Global Constraints

- Existing Stream text/counts/order/transaction behavior remains compatible; StreamVersion2 unchanged. No stored schema or production capture wiring here.
- Input64KiB, pending64KiB per stage, plaintext forwarding16KiB; no total page cap. Metadata must retire while draining.
- OriginSpan global exclusive byte offsets; Exact means identical original bytes. OpenEnded masks report known prefix only, never fabricate full secret extents.
- Sinks see sanitized text and numeric metadata only. Failure rolls back every state; successful callback consumption is irrevocable. Single owner; no guaranteed erasure of Go runtime copies.
- Active dirty feature worktree only. No indiscriminate commit/integration or stable app replacement for an unconnected prerequisite.

## Review Focus

- A replacement removes a newline and creates a later cross-boundary match; its source envelope must compose all contributing inputs.
- A token's accepted end precedes a replayed suffix; tail offsets must remain original after replay.
- A forced secret drains for thousands of input calls without output; metadata remains bounded and the later tail offset is exact.
- A sink mutates its span slice or calls Abort/reenters; future maps and committed state remain independent and terminal rules hold.
- A multibyte scalar or signed position counter reaches a boundary; no split scalar, negative wrap or accepted partial transaction.

## Task 1: Coordinate-aware recognizer callbacks

**Files:** modify `internal/privacy/stream_match.go`; create `internal/privacy/stream_origin_test.go`.

**Interfaces:** private `feedSpans(ctx,text,final,plain func(string,int64)error,match func(string,bool,int64)error)error`; the int64 is the original input start in this recognizer. Existing feed delegates and discards coordinates. Plain callbacks remain bounded16KiB and must describe contiguous original input.

- [x] Write every-split callback coordinate tests for Unicode, accepted-end replay, quoted/URL/lazy PEM matches and oversized-drain tail; add a typed fail-closed scaffold and observe behavioral RED.
- [x] Implement coordinates at each emission, preserving real prior/lookahead runes and output buffering. Reject input position overflow.
- [x] Run `go test ./internal/privacy -count=1`; expected PASS with existing recognizer/cascade behavior intact.

## Task 2: Bounded transactional source-map composition

**Files:** create `internal/privacy/stream_origin.go` and expand `stream_origin_test.go`; modify only transaction/clone plumbing in `internal/privacy/stream.go`.

**Interfaces:** public OriginSpan/MappedText/NewMappedStream and Write/Finish/Abort/Counts as specified. Private streamLineage maps stage input ranges to original intervals and retires consumed entries. Normal and mapped streams share the existing transaction implementation.

- [x] Write behavioral tests for all valid split positions, exact/plain and opaque/nested composition, unchanged URLs/invalid financial candidates, >1MiB tails and bounded overflow metadata, rejected sink/cancel/Finish retry/Abort/reentry/caller mutation/position overflow.
- [x] Run `go test ./internal/privacy -run TestMappedStream -count=1`; expected behavioral RED before source-map composition works.
- [x] Implement bounded queues, exact/opaque coalescing, checkpoint clone and atomic offsets. Drain retirement uses consumed position; normal retirement uses pending start.
- [x] Run `go test ./internal/privacy ./internal/capture -count=1`; expected PASS, read full output.

## Task 3: Final review and parent handoff

- [x] Run `go test ./...`, `go test -race ./internal/privacy ./internal/capture`, `go vet ./...`, `CGO_ENABLED=0 go build ./...`, `make cross-build cross-test-compile`, gofmt and diff check. Expected all exit0; read every output.
- [x] Request one fresh read-only review with parent constraints and Review Focus. Fix material findings once with genuine RED/GREEN and rerun all relevant gates; no re-review loop.
- [x] Record remaining parent persistence/schema/erasure/indexing/capture/whole-document/native acceptance and actual Library startup gate. Preserve the stable app and shared worktree.

All post-review gates passed on 2026-11-13; complete output was read. Evidence:
`/tmp/blur-origin-final-{suite,race,vet,build,cross}.log` and the dedicated ledger.
One Minor repeated-scan finding was optimized without a second review; the
4096-range helper benchmark improved from4.365ms to0.176ms, which is not an
end-to-end app latency measurement. The final task-done rerun of `go test ./...`
also passed. This prerequisite is complete in source, with no schema/app change.
