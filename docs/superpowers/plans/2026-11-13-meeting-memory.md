# Meeting Memory Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans for the assigned isolated task. The user explicitly requested parallel execution.

**Goal:** Establish a bounded, local meeting transcription path with durable-text semantics and explicit recording controls.

**Architecture:** Provider-neutral Go session engine; native Swift audio and SpeechAnalyzer boundary; encrypted repository adapter and app wiring in a subsequent integration slice. No automatic recording or plaintext audio files.

**Tech stack:** Existing cgo-free Go/purego, Swift 6, AVAudioEngine, Core Audio taps, SpeechAnalyzer on macOS 26+.

**Spec:** `docs/superpowers/specs/2026-11-13-meeting-memory.md`.

## Task 1 — Session engine

Owner: `internal/meetings/**`. No client, notes-service or existing storage edits.

- [x] Define typed events and an encrypted sink port. Validate session/track IDs,
  monotonic time ranges, text and revisions. Expose snapshots without mutable aliases.
- [x] Write/observe failing tests for partial replacement, final-only writes,
  idempotent retries, conflicting finals, interleaved tracks, pause/stop late-event
  rejection, cancellation, sink failure, input bounds and recording-state errors.
- [x] Implement serial bounded admission and incremental final persistence. Keep
  manual notes distinct and source-linked. No model or raw-audio/file operations.
- [x] Run package tests/races/lint; document exactly what remains unconnected.

## Task 2 — Native audio/transcription boundary

Owner: `platform/macos/MeetingAudio/**`, `internal/platform/darwin/meeting_audio*`.
Root owns packaging, UI and policy integration.

- [x] Check current Apple API/source and define a versioned bounded C ABI. Probe
  availability without prompting, downloading assets, opening audio or networking.
- [x] Add synthetic RED/GREEN tests for explicit start configuration, bounded
  queues/events, partial replacement, track timestamps, stop/cancel, device errors
  and late callback rejection. Use generated PCM, never a real microphone.
- [x] Implement separate microphone/system-audio tracks and local transcriber
  behind injected native device boundaries. No voice processing that affects a
  shared call microphone; no automatic asset download or silent track downgrade.
- [x] Build universal with macOS 13 deployment plus runtime gates/weak frameworks;
  test Go loader without real capture, cross-compile and lint. If an API requires
  a different baseline, report it rather than quietly changing app deployment.

## Task 3 — Integration after those contracts settle

- [ ] Review epoch ownership, callback lifetime and encrypted sink guarantees.
- [ ] Add encrypted incremental repository adapter, deletion/retention coverage,
  then wire explicit app controls and read-only availability.
- [ ] Use finalized text plus manual notes for a bounded summary. Keep action
  proposals unconfirmed and preserve evidence/time references.
- [ ] Package/sign with the same Blur identity; perform synthetic checks before
  asking for real device acceptance. Build does not authorize app lifecycle.

## Review focus

- A stopped/paused session must never admit late transcript text.
- Slow persistence or device restarts must not allocate an unbounded PCM queue.
- Duplicate event delivery must not duplicate encrypted transcript evidence.
- Full sink failures and unsupported assets must surface as errors, preserving
  already stored text and unsaved manual notes.
- Partial text, raw audio and meeting contents must not appear in logs or files.
