# Recap Knowledge Suggestions Implementation Plan

> **For agentic workers:** Execute inline using `superpowers:executing-plans`. Each task uses test-first RED/GREEN steps.

**Goal:** Let a user ask local Ollama to suggest source-linked knowledge from one hourly recap and add only reviewed suggestions to the encrypted Library.

**Architecture:** A bounded provider adapter receives only sanitized sources cited by one recap and returns validated, untrusted suggestions. The desktop shows each suggestion with its source; accepting it creates one confirmed Library entity/fact with captured-source provenance. No background generation, remote routing, merge, or task creation is added.

**Tech Stack:** Go standard library, existing `model.Ollama`, `chronicle`, `knowledge`, durable storage, and GoUI.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md` (Chat and memory, AI provider layer, token/model efficiency, and evidence-before-synthesis).

## Global Constraints

- Do not use cgo; preserve the existing GoUI shell.
- Models receive only bounded, already-sanitized evidence and are treated as untrusted.
- Captured sources remain local; this feature uses loopback Ollama only and only after a user action.
- A generated suggestion remains transient until accepted; accepted facts cite an immutable source ID.
- Bound source count, input bytes, output tokens, response bytes, deadlines, and displayed text.
- Source deletion must remove/quarantine any accepted source-derived Library content through existing durable cascades.

## Review Focus

- Missing/deleted source during extraction or acceptance: fail closed and never create an uncited fact.
- Duplicate Library name: preserve the existing entry and show a safe conflict result.
- Prompt injection or malformed/oversized model JSON: reject output and do not persist it.
- Disabled/remote-only model configuration: no provider request is made.
- Memory invalidation while suggestions are visible: clear suggestions and reject stale acceptance.

## Tasks

### Task 1: Bounded local suggestion domain and adapter

**Files:** Create `internal/knowledge/derive.go`, `internal/knowledge/derive_test.go`, `internal/knowledge/ollama.go`, and `internal/knowledge/ollama_test.go`.

**Interfaces:** `derive.Evidence{SourceID, Text}`, `derive.Proposal{Kind, Name, Fact, SourceID}`, `derive.Generator{CountInputTokens, Generate}`, and `derive.OllamaGenerator`. Cap input at 8 sources/16 KiB, output at 3 suggestions/512 bytes each, and output tokens at 192. Validate all source IDs against the supplied evidence; never accept model-provided state, IDs, aliases, or merge instructions.

- [ ] Write tests for bounded, cited output and malformed/injected/unbounded input; run and observe the missing API fail.
- [ ] Implement validation and a strict JSON loopback-Ollama adapter using the existing `model.Ollama` client.
- [ ] Run `go test ./internal/knowledge` and `go vet ./internal/knowledge`.

### Task 2: Explicit recap extraction and reviewed persistence

**Files:** Modify `internal/client/client.go`, `internal/client/local_ai_controller.go`, `internal/client/client_test.go`, `internal/client/local_ai_test.go`, and `internal/storage/durable/knowledge.go`; add relevant durable tests.

**Interfaces:** `Client.SuggestKnowledgeFromChronicle(ctx, hourID) ([]derive.Proposal, error)` hydrates at most 8 cited sources from exactly one recap and calls the local model only when explicitly invoked. `Client.AcceptKnowledgeSuggestion(ctx, hourID, proposal) (knowledge.Entity, error)` verifies the source belongs to that recap and atomically stores a confirmed entity/fact with that source citation.

- [ ] Write tests for local-only/user-triggered routing, source/recap bounds, deleted-source rejection, and duplicate preservation; observe RED.
- [ ] Implement the client/controller flow and acceptance through the existing encrypted `PutKnowledgeEntity` transaction.
- [ ] Test source deletion cascade for accepted facts and run focused client/durable tests.

### Task 3: Recap review UI and documentation

**Files:** Modify `internal/client/desktop.go`, `internal/client/desktop_test.go`, `README.md`, and `docs/architecture.md`.

**Interfaces:** Expanded recap view exposes one explicit “Suggest Library facts” action. Results are transient, source-labeled, individually accept/dismiss, cleared on memory invalidation or recap change, and unavailable for remote providers.

- [ ] Write UI state/helper tests for disabled remote/offline state and stale-result invalidation; observe RED.
- [ ] Implement bounded async action and per-proposal review; no automatic persistence.
- [ ] Run the full Go suite, race tests for changed packages, `go vet ./...`, no-cgo audit, macOS app packaging, and cross-build gates.

**Commit policy:** Do not commit; the current worktree contains the user's broad, uncommitted implementation.
