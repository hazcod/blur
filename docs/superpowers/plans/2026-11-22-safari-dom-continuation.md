# Safari DOM Continuation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline; no additional delegation or commits are authorized.

**Goal:** Retain complete permitted Safari DOM text through resumable bounded capture and notify ordinary memory pipelines after publication.

**Architecture:** JS emits ordered revision batches and advances on native/Go ACK. Go feeds existing CaptureBuilder and encrypted multipart publication; native metadata and preferences remain authoritative.

**Tech Stack:** Existing JavaScript, SafariServices Swift, Go browsercapture, localapi transport, capture and durable storage.

**Spec:** `docs/superpowers/specs/2026-11-22-safari-dom-continuation-design.md`

## Global Constraints

- Keep 100ms/10,000-node/256KiB work ceilings and 288KiB frames; use 32KiB transport batches.
- Never publish mixed revisions or silently clip an acknowledged batch.
- Preserve private/active-page and current policy checks at every storage boundary.
- At most 4 retained jobs; clean private staging on replacement, eviction and pause.
- No app launch/restart, live database access, commits or unrelated ingest changes.

## Review Focus

- Huge single text nodes must advance without whole-node normalization.
- Lost final ACK must not duplicate publication or callbacks.
- Continuous tail appends must not starve fenced snapshots.
- Midscan edits and inactive/private transitions must never publish a mixed complete edition.
- Duplicate publication must retain canonical source provenance and notify current visit work.

## Tasks

- [x] Add failing Node continuation/fence/retry tests; implement `createCursor(root, limits)` and acknowledged `install` in Injected.js.
- [x] Add failing Go sequence/coverage/durable-tail tests; implement v2 Snapshot fields, retained browser document jobs and postcommit hooks in browsercapture.
- [x] Add ACK response to the existing framed Unix transport; test retry/restart outcomes without changing physical work budgets.
- [x] Update Swift payload validation and native source identity; round-trip complete batches and bounded ACKs, never truncate.
- [x] Wire client Safari sink adapter and shared observation/document callbacks, after preference admission; verify temporary-store callbacks.
- [x] Run Node, browsercapture Go tests, Swift runtime/typecheck and focused client integration; review diff and report remaining unstable-page limits.

Validation on 2026-11-22: 14 Node tests passed; the full browsercapture suite passed (5.697s), including durable indexed tail changes and cross-batch secret redaction; the browsercapture race suite passed (9.688s). Focused Safari/native publication client tests passed (1.225s). The Swift protocol executable, complete handler typecheck, project/plist lint, and scoped diff check passed. The parent owns the final stable app build; no app launch, live database access or commit was performed here.
