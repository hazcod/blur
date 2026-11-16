# Full Capture and Processing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task by task. Preserve other workers' edits in the shared active worktree.

**Goal:** Connect complete acquired app content to encrypted storage, indexing and all memory consumers.

**Architecture:** Bounded document continuations publish authenticated multipart captures. Generic trusted evidence readers join their actual part IDs into the existing search and enrichment paths. Browser pixels require a verified page region; source transformations use the appropriate public Apple API.

**Tech Stack:** Go, Swift, Accessibility, ScreenCaptureKit, Vision, Apple Foundation Models, encrypted durable storage, GoUI.

**Spec:** [Full capture design](../specs/2026-11-13-full-capture-processing-design.md).

## Global Constraints

- Active source: `.worktrees/durable-memory-core`; no destructive worktree operations or blanket commits.
- Preserve privacy, source authority, deletion fences and existing encrypted identities.
- Keep no-cgo supported targets compiling; platform-specific capabilities stay behind ports.
- Only canonical same-certificate `~/Downloads/Blur.app`; no app lifecycle action implicit in a build.

## Review Focus

- A large source's last part must be retrievable, cited and enriched after restart.
- A refused source must not prevent subsequent sources or raw retrieval.
- Erasing a parent capture must erase every part-derived memory, recap and index.
- Mid-read app, page, permission, focus or exclusion changes must prevent publication.
- Browser fallback must exclude tabs/address chrome before pixels are acquired.

## Task 1: Remove proved Orion AX overhead

**Owner:** herdr_capture_failure.
**Files:** `internal/capture/pipeline_darwin.go`, `ax_tree.go`, `read_issue.go`, `pipeline.go`, tests.

- [x] Reproduce deadline loss on valueless containers in a fully traversed tree.
- [x] Skip only positively proven absent text accessors; preserve every actual-text privacy check.
- [x] Preserve closed timeout stage through error wrapping and cancellation.
- [x] Run full capture/ingest races, lint and vet.
- [x] Join content-free stage diagnostics into ingestion and the client status copy.

## Task 2: Wire full documents into production storage and indexing

**Owner:** natural_intent_processing.
**Files:** new `internal/capture` document/evidence adapters, `internal/ingest`, `internal/storage`, `internal/storage/durable/capture*.go` and focused tests.

**Interfaces:** `capture.Evidence` exposes `SourceID`, app/domain, text, coverage and provenance without impersonating a legacy observation. Optional `GetSourceEvidenceWithCaptureTime` and bounded generic source-page ports preserve legacy readers.

- [x] Add authenticated legacy/part evidence tests, including invalid membership and unpublished/deleted sources.
- [x] Connect bounded native document continuation to capture staging/publication.
- [x] Connect part indexing to production pending-index drain and exact search.
- [x] Add restart, last-part lookup, retained source-time and deletion/retention tests.
- [x] Prove cancellation and mid-read privacy changes publish no incomplete source.

## Task 3: Join all memory consumers

**Owners:** root for client/Search/Ask/recap/semantic; lexical_coverage_corpus for Library and its durable validator.
**Files:** `internal/search/search.go`, client source-resolution/worker adapters, `internal/knowledge/enrich`, `internal/storage/durable/knowledge_enrichment.go`, tests.

- [x] Use the generic trusted reader for source resolution, preserving every ID/evidence comparison.
- [x] Enumerate published parts in Library and recap source adapters under bounded cursors.
- [x] Test a large document's tail through actual encrypted storage, exact search, public Ask and Library.
- [x] Test source deletion while inference ignores cancellation; no late part projection may survive.

## Task 4: Process existing text through the supported Apple mode

**Owner:** root.
**Files:** `internal/model/apple.go`, `platform/macos/FoundationModels/main.swift`, source-processing constructors and tests.

- [x] Add a failing wire-policy parity test for count and generation.
- [x] Add a dedicated source transformer, string generation with complete schema guidance, and strict existing downstream validation.
- [x] Version its identity so prior default-policy refusals can be reconsidered once.
- [x] Run the real signed helper on synthetic technical sources and benign quoted sensitive text; record actual outcomes without claiming the user's trigger was reproduced.
- [x] Preserve general-question behavior, token budgets, output validation and foreground priority.

## Task 5: Add verified browser page OCR

**Owner:** herdr_capture_failure after Task 1.
**Files:** capture visual/Darwin adapters, ScreenText Swift/Go bridge and tests.

- [x] Define a page-region lease bound to the exact foreground window/document.
- [x] Test geometry across scale/display origins, private/secure/stale windows and crop bounds.
- [x] Sample only the verified region with an exact-window filter; test that browser chrome is excluded before OCR.
- [x] Preserve existing permission checks and fail closed when region authority is unavailable.

## Task 6: Final review, packaging and evidence

**Owner:** root, with independent bounded reviews.

- [x] Run affected repeated races, the whole Go suite, lint, vet and supported cross-build checks.
- [x] Build the canonical bundle and compare exact signer/requirements for every code component.
- [x] Verify packaged native libraries without devices and the model with synthetic sources.
- [x] Record live app matrix separately from synthetic results and update delivery status honestly.
