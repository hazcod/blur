# Streaming Redaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline under the user's autonomous instruction. No implementer agents; one fresh final read-only reviewer.

**Goal:** Redact complete native document streams with bounded memory, correct cross-boundary rule composition and transactional consumption.

**Architecture:** Private bounded regexp transducers retain possible matches between UTF-8 ranges. A cloned cascade preserves rule order and URL label scopes; one atomic sink call commits each input piece.

**Tech Stack:** Go, standard-library regexp/syntax and tests; no new dependency.

**Spec:** `docs/superpowers/specs/2026-11-13-streaming-redaction-design.md`

## Global Constraints

- Keep batch Redact and version-1 observations compatible. StreamVersion is 2; no stored format/schema changes or production capture wiring in this subplan.
- Input calls at most 64 KiB, valid UTF-8/no NUL; no document-size cap. Pending candidate at most 64 KiB per stage, plaintext forwarding buffers at most 16 KiB; checkpoints duplicate bounded state.
- PEM, Authorization, URL, three URL-aware label passes, token, SSN, card, IBAN preserve existing order and counts for bounded complete candidates.
- Incomplete opened keys/quoted secrets at EOF and oversized ambiguous fields are masked. Abort emits nothing pending; ordinary tails survive.
- Single-owner stream. Sink consumes all on success or nothing on failure; rollback includes pending text, counts, UTF-8/regexp state. Typed context failures are preserved, raw error text is not exposed.
- Shared dirty feature branch: no indiscriminate commits, cleanup or integration. Keep the stable signed Downloads app intact for this unconnected prerequisite.

## Review Focus

- An accepted token ends before a long non-word suffix; replay must not lose a later independent secret or expose an overflowed suffix.
- A key is closed and followed immediately by another field; lazy recognition must release the key and preserve following prose.
- A callback fails after prior successful inputs: only the failed input/output/counts roll back, and retry does not duplicate or omit accepted content.
- An unbounded candidate never completes: memory remains bounded, Abort leaks no suffix, and a subsequent real delimiter restores ordinary text.
- A URL inside a label started earlier belongs to that value; label-like text inside an earlier URL remains opaque, while token rules still process URLs.

## Task 1: Bounded incremental recognizer

**Files:** create `internal/privacy/stream_match.go`, `internal/privacy/stream_recovery.go`, `internal/privacy/stream_match_test.go`.

**Interfaces:** private `streamPattern` holds immutable regexp/program, lazy flag and EOF/overflow policy; `streamMatcher` owns bounded bytes, rune context, viable program states, accepted extent and drain state. `feed(ctx context.Context, text string, final bool, plain func(string) error, match func(string, bool) error) error` sends safe plaintext and resolved matches in order; `bool` marks overflow/unfinished masking. `clone() streamMatcher` is independent; `clear()` drops raw state. Static patterns are built at init, not per input.

- [x] Write tests for split greedy matches/word boundaries, lazy PEM closure, competing candidate starts, accepted-end replay, Unicode folding, EOF, overflow drain/recovery, cancellation and bounded cloned state. Use the actual policy regexps.
- [x] Run `go test ./internal/privacy -run TestStreamMatcher -count=1`; expected behavioral RED after a fail-closed typed scaffold.
- [x] Implement regexp/syntax state transitions, bounded buffering and overflow drain, scalar boundaries, independent clone, context checks and first-rune fast path.
- [x] Run the same command; expected PASS and no lost tail or pending overflow.

## Task 2: Transactional complete redaction cascade

**Files:** create `internal/privacy/stream.go`, `internal/privacy/stream_test.go`; extract only shared match transforms to `internal/privacy/redact_match.go` from `redact.go`.

**Interfaces:** `NewStream() *Stream`, `Write(context.Context,string,func(context.Context,string)error) error`, `Finish(context.Context,func(context.Context,string)error) error`, `Abort()`, `Counts() []Count`; `StreamVersion = 2`. Consumes Task1 matcher with 64-KiB pending and 16-KiB safe output forwarding. Existing `Redact(string)(string,[]Count)` remains version1 and exact-output compatible.

- [x] Write tests for every-rule split positions, malformed URLs, exact placeholders, nested/replacement-created matches, URL/label ownership, all EOF/overflow rules, sink error/retry and deadline rollback, invalid input/closed/reentrancy behavior, >1-MiB ordinary content/tail, bounded internal state and sorted/committed counts.
- [x] Run `go test ./internal/privacy -run TestStream -count=1`; expected behavioral RED before the cascade works.
- [x] Implement shared replacement helpers without changing batch output; wire ordered states and earliest-start label/opaque-URL alternatives, escaped-LF quoted ownership, nonblank unquoted values and safe overflow recovery. Clone the full stream per Write/Finish, buffer sanitized output, call sink once, then commit. Abort discards pending state.
- [x] Run `go test ./internal/privacy ./internal/capture`; expected PASS. Add deterministic randomized-boundary parity and focused throughput benchmarks; inspect measured output before optimizing.

## Task 3: Review and integration evidence

- [x] Run `go test ./...`, `go test -race ./internal/privacy ./internal/capture`, `go vet ./...`, `CGO_ENABLED=0 go build ./...`, and `make cross-build cross-test-compile`; expected exit 0. Read every output.
- [x] Use requesting-code-review for one fresh read-only review of this subplan's complete implementation and parent constraints. Include Review Focus and ledger rulings. Fix material findings once, each genuine RED/GREEN, then rerun all relevant gates; no review loop.
- [x] Record that document persistence, schema/erasure/indexing, capture wiring, whole-document Ask and native acceptance remain required. Preserve the stable Downloads app and return to the full parent design.

All post-review gates passed on 2026-11-13. Evidence is in
`.superpowers/sdd/2026-11-13-streaming-redaction/progress.md` and
`/tmp/blur-stream-final-{suite,race,vet,build,cross}.log`.
Task 3 also reran `go test ./...` successfully through `task-done`; its full
output is retained as `task-3-tests.log`. Shared feature work remains uncommitted
and unintegrated under the parent plan. This does not complete production
large-document capture or recovery of the user's Library.
