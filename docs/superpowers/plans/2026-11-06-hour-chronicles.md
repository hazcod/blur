# Source-Linked Hour Chronicles Implementation Plan

> **For agentic workers:** Implement inline in this existing worktree with TDD. This is a vertical slice of delivery-roadmap Phase 7 and the Chronicle requirements in the product spec; later day/week/month/year rollups remain follow-up work.

**Goal:** Create optional, source-linked hourly summaries from closed-period sanitized evidence, store them encrypted, and ensure deletion, local-only routing, power admission, and token/time bounds are enforced.

**Architecture:** A pure `internal/chronicle` package validates closed-hour inputs, bounds evidence packing, and accepts generated summaries only with complete provenance. The durable store persists summaries and a reverse source index in authenticated encrypted records so source deletion can conservatively remove every summary that depends on it. A local-only opt-in worker schedules completed hours from capture events/startup recovery (never by scanning or controlling apps), asks `power.Effective` at admission, and uses bounded concurrency, tokens, duration, retry, and coalescing.

**Tech Stack:** Go stdlib, existing encrypted bbolt record/index APIs, existing Ollama client, existing power policy; no cgo or new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md` (AI provider layer, token efficiency, memory construction, power/background work); `docs/superpowers/plans/2026-11-05-blur-delivery-roadmap.md` (Phase 7).

## Global Constraints

- Capture never invokes AI; only completed hourly windows are summarized.
- Background AI is disabled unless explicitly opted into and only a local provider may receive evidence.
- Summary input, output, token estimate, execution time, retries, pending queue, and concurrency are bounded.
- Every summary links all of its contributing source IDs; deleting a source removes summaries that reference it rather than leaving unattributable prose.
- AI output is untrusted text. It may suggest at most one bounded follow-up question only when a meeting cue is present in the exact cited source; it cannot create tasks, change policy, or cause external actions.
- Capture, retention, and user-started AI remain available in every power mode; unknown OS power resolves to Balanced.
- No application navigation, scrolling, browser-history access, or recurring polling is introduced.
- Tests use synthetic sanitized fixtures only; no personal activity is accessed.

## Review Focus

1. A deleted, quarantined, or retained-out source must not remain quoted by a stored hour summary.
2. Remote provider settings, unknown power state, Saver mode, and malformed consent must not silently schedule background AI.
3. Boundary timestamps, daylight-saving transitions, duplicate events, and partial hours must produce at-most-once, stable hour IDs.
4. Oversized, malformed, prompt-injection-shaped evidence or model output must remain bounded and treated as untrusted data.
5. Worker shutdown, cancellation, failed jobs, and retries must not block capture, user-started work, or store close.

---

### Task 1: Chronicle domain and bounded evidence packing

**Files:** create `internal/chronicle/chronicle.go` and `internal/chronicle/chronicle_test.go`; modify no providers or storage.

**Interfaces:**
- Produces `Hour`, `Evidence`, `Summary`, `PackHour(Hour) (Packed, error)`, and `ValidateSummary(Packed, Summary) error` for Tasks 2 and 3. `Evidence` carries only `capture.SanitizedObservation`, never a raw string. The validator receives the exact packed input so it can reject citations outside the model's allow-list.
- An hour is a UTC half-open interval `[Start, End)` exactly one hour long; sources are stable opaque IDs, sorted and unique; input has bounded evidence count and aggregate bytes; output has a bounded UTF-8 body and must cite a non-empty subset of supplied source IDs.

- [x] Write tests for time boundaries, stable IDs, duplicate/unsorted source rejection, evidence-count/aggregate/item input caps, salient-source selection, output cap, unknown citations, and injection-shaped content treated as evidence.
- [x] Run `go test ./internal/chronicle -run 'Test(Hour|Pack|Validate)' -count=1`; confirm missing package/symbol failure.
- [x] Implement the pure domain model and deterministic bounded packer; keep prompts/data types separate from provider clients.
- [x] Run the focused tests and `go test ./internal/chronicle`.

### Task 2: Encrypted persistence and source-deletion cascade

**Files:** modify `internal/storage/durable`, `internal/storage/store.go`, `internal/deletion`, and contract/durable tests.

**Interfaces:**
- Consumes Task 1 `chronicle.Summary` and validation.
- Produces bounded `PutChronicle`, `GetChronicle`, and paged period listing on encrypted stores.
- Reverse source indexes are encrypted; delete-all and source deletion atomically remove dependent summaries and index entries.

- [x] Add persistence tests for round-trip/reopen, bounds, malformed records, reverse-index updates, source deletion, delete-all, and transaction rollback.
- [x] Run focused tests and observe expected missing-interface failure.
- [x] Implement encrypted namespaced records plus source index using the existing logical-key/AAD envelope; use transactions for summary/index changes and deletion cascade.
- [x] Run durable/store conformance tests and focused race tests.

### Task 3: Local, opt-in, policy-gated hour worker

**Files:** modify `internal/client`, `internal/notes` or add a focused local chronicle model adapter, `internal/power` as needed, and tests.

**Interfaces:**
- Consumes Task 1 bounded packed input and Task 2 encrypted persistence.
- Produces a worker that coalesces closed-hour work, admits only opted-in local model calls after a fresh effective-power read, enforces token/duration/concurrency limits, and never changes route to make a job admissible.
- User-started note processing and Ask bypass this optional-work gate without changing their configured provider.

- [x] Write tests first for default-off consent, local-vs-remote routing, Saver/Balanced/Performance/unknown admission, coalescing, cancellation, budget enforcement, close, and source deletion during or after generation.
- [x] Run focused tests and observe the missing worker/gate behavior.
- [x] Implement event/boundary and opt-in startup recovery of only the previous 24 UTC hours; a bounded power-admitted worker pool, no recurring idle scan, strict local-only endpoint checks, and content-free `slog` events.
- [x] Run package/client race tests and verify capture and user-started AI remain independent.

### Task 4: Privacy controls, UI, and release evidence

**Files:** modify `internal/client/desktop.go`, `internal/client/desktop_test.go`, README, architecture, threat model, checkpoint, and this plan.

- [x] Add an explicit, default-off background-summary opt-in and truthful model/power status; preserve visible source review links and delete behavior.
- [x] Verify background model calls are absent when disabled, remote-selected, or policy-denied; show local summaries only after successful encrypted persistence.
- [x] Allow the local hourly generator to return at most one source-linked missing-follow-up question; validate question shape, exact citation, and a conservative meeting cue before atomically adding it to the encrypted Today projection.
- [x] Run the full test/race/vet/cross-build/no-cgo suite; record runtime limitations without claiming battery savings absent measurements. Performance and native visual measurements remain release gates.
- [x] Update docs and mark only evidence-backed plan items complete.
