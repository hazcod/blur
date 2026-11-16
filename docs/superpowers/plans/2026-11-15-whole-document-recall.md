# Whole-Document Recall Implementation Plan

> **For agentic workers:** Continue inline under the user's autonomous implementation instruction. The parent design is already approved; do not replace the stable app until the end-to-end path and native gates pass.

**Goal:** Let Ask answer explicit whole-page/document requests using every part of one or more identified captured editions, with bounded work, source citations, progress, cancellation, and honest coverage.

**Architecture:** Keep ordinary passage recall unchanged. Mark explicit whole-document requests in the deterministic plan, identify live editions from ranked part evidence, then process every part in bounded selected-model batches and reduce those results into a cited answer. If the request cannot identify its document(s) unambiguously, ask a specific follow-up instead of summarizing a convenient excerpt.

**Tech Stack:** Go; existing encrypted durable capture editions, exact and selected-model recall, Apple Foundation Models and configured local model adapters, GoUI.

**Spec:** `docs/superpowers/specs/2026-11-13-large-document-capture-design.md`

## Constraints

- Do not weaken ordinary question retrieval, source citation checks, capture exclusions, deletion fences, selected-provider policy, or remote-consent checks.
- Read published, live capture editions only. A pending index, partial edition, stale source handle, or deleted source must never be presented as complete.
- No whole-document plaintext buffer, total-document-size ceiling, new provider setting, or silent truncation. Process all parts through bounded pages; cancellation and deletion invalidate the answer.
- Do not claim full coverage when document capture itself is partial or the edition changes during processing.
- Preserve exact original text and source positions for inspection. The model receives only sanitized content.
- Keep the current `~/Downloads/Blur.app` untouched until the complete capture and recall path is ready for its supported schema.

## Review Focus

- A tail-only fact in a document beyond 1 MiB appears in a whole-document answer with a citation to its actual part.
- Two matching editions are not silently conflated; an ambiguous “summarize that page” asks which page.
- A deleted source, changed publication, partial capture, or canceled model call cannot publish a stale complete answer.
- Model context limits are measured per request, while the total document is processed incrementally without skipping later parts.
- An ordinary factual question still takes the existing fast passage path and does not fan out over the whole document.

## Task 1: Preserve edition identity through Ask retrieval

**Files:** `internal/assistant/context.go`, `internal/storage/evidence_page.go`, `internal/storage/durable/capture_evidence.go`, `internal/client/ask_retrieval.go`, `internal/client/client.go` and focused tests.

**Interface:** Add opaque edition provenance to hydrated assistant evidence: edition ID, publication, part index/count, total bytes, and capture coverage. Add a bounded encrypted reader contract in `internal/storage` for paging all parts of a specific live edition; implement it by adapting the durable capture reader without exposing internal catalog records.

- [x] Prove an Ask candidate preserves its authenticated edition and part identity through ranking and hydration.
- [x] Prove part pages are ordered, bounded, publication-checked, and fail after a missing edition or changed publication.
- [x] Run focused client/storage tests; existing passage Ask/Search results remain byte-for-byte unchanged.

## Task 2: Deterministic whole-document intent and edition selection

**Files:** `internal/assistant/plan.go`, `internal/assistant/planner.go`, `internal/client/ask_retrieval.go` and tests.

**Interface:** Add a `Plan.WholeDocument` flag for explicit requests to summarize/review/explain an entire page, document, article, or captured source. Passage-level questions remain false. Resolve editions from ranked source evidence and apply the requested app/domain/time/topic scope before expansion.

- [ ] Cover natural phrasings, contractions, punctuation, multilingual-compatible named scopes, and negative/passsage-level cases. (Initial deterministic intent parsing and English/French fixtures are in place; broader named-scope and variant coverage remains.)
- [ ] Ask a concise disambiguation when multiple editions fit; never pick a login page or unrelated recent capture.
- [ ] Verify only the explicit whole-document mode expands beyond the normal evidence limit.

## Task 3: Bounded complete analysis with selected model

**Files:** `internal/assistant/` document analysis service and tests; provider-neutral selected-model orchestration in `internal/client/`.

**Interface:** Process the edition with bounded ordered pages and bounded context requests. Each intermediate result retains the source part IDs it summarizes. Reduce intermediate results in bounded batches, then validate the final answer against the complete set of live cited source IDs. Track processed parts/bytes and document coverage. Never send the entire document in one model request.

- [ ] Demonstrate all parts, including tail-only content, reach the selected provider over multiple measured context-sized requests.
- [ ] Show partial capture and omitted structural coverage in the final answer.
- [ ] Cancel promptly and discard output on model failure, memory change, deletion, changed edition, or user cancellation.
- [ ] Enforce provider-specific context/token limits on every map and reduce request; preserve model and remote-consent settings.

## Task 4: Ask progress, cancellation, and source inspection

**Files:** `internal/client/desktop.go`, `internal/client/memory_entry.go`, Ask UI components and focused UI tests.

**Interface:** Show a quiet inline progress state with processed/total parts and a Cancel action while whole-document analysis runs. Keep the question and previous conversation usable after cancellation/failure. Cite original capture parts in the answer and open the existing source-inspection view at the cited passage.

- [ ] Verify progress advances monotonically, cancellation restores the composer, and ordinary Ask retains its current fast path.
- [ ] Verify citations expose the exact original part and source offsets without showing internal IDs.

## Task 5: End-to-end, native acceptance, and delivery

**Files:** synthetic encrypted-store integration tests, native acceptance fixtures, `docs/full-ingest-status.md`, and `docs/personal-memory-plan.md`.

- [ ] Test >1 MiB, many parts, tail-only facts, restart, duplicate visits, changed edition, cancellation, deletion during analysis, partial capture, ambiguous documents, and ordinary passage-query latency.
- [ ] Verify Orion, Terminal, and Herdr acceptance with the same signed `~/Downloads/Blur.app`; confirm the tail is searchable and whole-document Ask cites it.
- [ ] Run focused tests, full relevant suite, affected race tests, `go vet`, cgo-disabled build, cross-build/audit, and signing/bundle checks; inspect all output.
- [ ] Update status docs from actual evidence. Replace the stable app only when the full path and schema transition are ready; do not launch it as part of a build.
