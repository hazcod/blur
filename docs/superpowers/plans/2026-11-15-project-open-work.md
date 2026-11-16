# Source-Backed Project Open Work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Make clearly unresolved, source-backed project follow-ups discoverable in Ask as tentative Library proposals.

**Architecture:** Extend local Library extraction with an explicit open-work classification. Verify that the cited evidence contains an unresolved-work cue, then add the tentative marker in Blur. Reuse proposal provenance, correction, and dismissal, then expose only that marker in the existing structured outstanding-work answer.

**Tech Stack:** Go, encrypted durable Library, existing on-device/local model adapters.

**Spec:** `docs/superpowers/specs/2026-11-15-project-open-work-design.md`

## Global Constraints

- Captured evidence remains encrypted at rest and local to the selected model.
- Model output remains proposed and source-linked; it cannot schedule tasks.
- Existing exact search and source deletion behavior remain authoritative.

## Review Focus

- Ordinary proposed facts must not be mistaken for open work.
- Proposed follow-ups must retain their source citation and remain dismissible.
- Credential-form captures must not be returned as project follow-ups.
- Existing confirmed tasks and open loops must remain unchanged.
- Prompt changes must invalidate prior enrichment progress so existing sources can be reconsidered.

### Task 1: Extract tentative open work

**Files:** `internal/knowledge/derive/ollama.go`, `internal/knowledge/enrich/types.go`, and derivation tests.

- [x] Add prompt and normalization assertions for concise facts, technical source text, and explicit open-work classification.
- [x] Update the local extraction prompt and enrichment version to reprocess existing sources.
- [x] Verify focused derivation and enrichment behavior.

### Task 2: Include marked proposals in structured Ask

**Files:** `internal/client/ask_retrieval.go`, `internal/client/ask_retrieval_test.go`.

- [x] Add a regression showing a proposed marked fact appears as tentative open work with its source.
- [x] Add a regression showing an unmarked proposed fact is excluded.
- [x] Collect marked facts from inferred Library entities into the existing possible-open-work digest.
- [x] Verify focused retrieval and answer behavior, including the credential-page case.

### Task 3: Review and package

- [x] Review compatibility with existing correction, dismissal, source erasure, and retry behavior.
- [x] Build and sign `~/Downloads/Blur.app` with the established identity and bundle ID.
- [x] Verify the package signature and leave the app closed.

### Task 4: Verify the on-device open-work classification

- [x] Replace prose-prefix guessing with an explicit `open_work` boolean in the model contract; Blur verifies source cues and applies the tentative marker deterministically.
- [x] Remove schema-shaped prompt injection from Apple's permissive source-transform requests, which produced invalid JSON on code and log inputs; retain strict parsing, citation mapping, and proposal validation in Go.
- [x] Validate the synthetic CraftedSignal vendor follow-up with Apple's on-device model.
- [x] Validate synthetic source-code, build-log, security-review, and open-follow-up examples with Apple's on-device model.
- [x] Run the affected package suites and rebuild the canonical signed app with this iteration's changes.
- [x] Make open-work cue validation negation-aware and status-ordered, then invalidate prior Library extraction progress.
