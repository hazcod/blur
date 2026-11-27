# Work-driven Source Compaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Compact all accepted source text through resumable queued work without waiting for hours.

**Architecture:** Extend the existing source-enrichment worker with a combined local compaction/optional-proposal request. Retain independent encrypted source-span blocks, atomically with cursor/counters; source-erasure journals drain every block. Recap retrieval may consume all completed, labelled source compactions while exact recall retains originals.

**Tech Stack:** Go, existing local completion adapters, encrypted schema-8 storage with atomic in-place schema-7 migration.

**Spec:** `docs/superpowers/specs/2026-11-22-source-compaction-design.md`

## Global Constraints

- Preserve authoritative source text, privacy gates, power admission and deletion fences.
- Four model calls per quantum; no hour-based gate.
- Build only `make macos-app-stable`; no app launch/restart.
- Do not revert other changes, commit or push.

## Review Focus

- A long multibyte source survives interruption and resumes its exact next span.
- A refusal or invalid response never claims its unread tail complete.
- Summary-only processing does not create Library proposals.
- Library capacity cannot strand source compaction or later sources.
- Deletion removes summaries and stale generated output cannot republish them.

### Task 1: Combined bounded model request

- [x] Add failing tests in `internal/knowledge/derive/compaction_test.go` for independent exact-span input, source labels, strict output validation, token counting and processor identity.
- [x] Implement `CompactingGenerator`, `CountCompactedInputTokens` and `GenerateCompacted` in `internal/knowledge/derive/compaction.go`; use per-request model/power output admission and decoded record/protocol guards, without a whole-source summary cap. Preserve the ordinary proposal generator.
- [x] Run focused derive tests and the opt-in synthetic native generator check; fix omitted technical identifiers and the missing disabled suggestions field exposed by the actual provider.

### Task 2: Atomic resumable source summaries

- [x] Add failing encrypted-store integration tests in `internal/knowledge/enrich/compaction_test.go` for full source coverage, restart, terminal outcomes, completion deduplication and erasure.
- [x] Extend `Progress`/`Chunk` with compaction text/version and limited-claims metadata; integrate optional combined generation into `Service.process` with the existing four-call budget.
- [x] Validate and atomically persist summaries in `internal/storage/durable/knowledge_enrichment.go`; on claim capacity preserve a limited-claims receipt rather than starving compaction.
- [x] Run focused enrich/durable tests and race checks; final whole-repository verification remains below.

### Task 3: Runtime and recall integration

- [x] Add tests for local summary-only worker eligibility and completed-source recap projections.
- [x] Configure compaction in `internal/client/knowledge_enrichment.go`, preserving optional Library extraction and local-only consent. Refresh eligibility when background-summary settings change.
- [x] Add an explicit generated source-compaction evidence layer and a recap projection helper; never replace precise raw evidence.
- [x] Run relevant client/assistant tests, vet, independent review and canonical signed build.

### Task 4: Comparable-gap review

- [x] Consolidate independent capture/index, retrieval, and feature-durability findings into `docs/blur-functional-gap-review.md`, with severity, concrete trigger, path/line and reproduction.
- [x] Report verified fixes, unresolved findings and activation state separately. The gap review retains unfixed findings; the rebuilt app was not launched or restarted.

### Task 5: Large-data continuation across storage and voice

- [x] Prove a megabyte multipart capture visits every accepted span and persists every summary block across encrypted reopen, using a deterministic provider rather than claiming model throughput.
- [x] Split maximum-fanout Library generation retirement into recoverable per-entity work; preserve corrections, confirmed reverse receipts and exact old-cursor checks. Verify interruption, deletion, clear and concurrent progress changes.
- [x] Remove the voice catalog's 4,096-session ceiling; exercise 4,100 real encrypted creations with reopen, deletion holes and old cursors.
- [x] Replace native 24-hour timing restrictions with checked duration/sample representation and validate synthetic 40-hour offsets through the Swift library and Go ABI.
- [x] Remove cumulative production voice-session count/text caps with authoritative durable duplicate/conflict/deletion lookup and bounded engine hot state.
- [x] Start saved agendas through an authenticated bounded recent-tail read, preserving full paged history and truthful older-text state.
- [x] Resolve independent review findings, run final repository/race/native checks and build the canonical signed app without restarting it.

### Task 6: Text recall across agents, large histories and derived facts

- [x] Reproduce separate two-agent and ten-agent text questions, older exact identifiers crowded out by component-word lookalikes, and dated questions polluted by all-time Library facts.
- [x] Validate exact technical labels inside each physical app/date page before merging accepted candidates; retain real query-work bounds and disclosed coverage.
- [x] Filter dated Library claims by their own observed support, preserving historical revisits, corrections, legacy suppression, deletion and undated behavior.
- [x] Admit distinct per-agent passages fairly through actual context packing, preserve Search/relationship conjunctions and require correct per-subject citations or explicit missing/omitted-subject disclosure. Independent review also reproduced and verified shared-log excerpts, mixed nine-plus-one allocation, dated/pronoun follow-ups and each-other relationship guards.
- [x] Add a bounded coverage-guided text journey campaign varying source state, agents, scopes, lookalikes and reopen; report actual executions separately from distinct user needs. Seven seeds and one 30-second campaign passed; 13 executions, zero new interesting inputs. This small run is not broad coverage.
- [x] Complete independent integration review, final whole-repository checks and the canonical signed build. Final suite passed 56 tested packages; client 305.160s. Vet and focused text race checks passed. Exact certificate/designated requirement and unsandboxed universal bundle were verified; no app lifecycle action.
