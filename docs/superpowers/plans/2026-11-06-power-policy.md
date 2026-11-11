# Configurable Power Policy Implementation Plan

> **For agentic workers:** This continuation is executing natively in the current worktree; keep the goal active until the full product and release gates are verified.

**Goal:** Let Blur follow verified OS battery-saver mode or use an explicit Balanced, Saver, or Performance policy without suppressing capture, retention, or user-requested AI.

**Architecture:** A pure policy package resolves a closed OS-neutral mode into bounded admission limits. OS readers query the native low-power/battery-saver signal only when policy is evaluated; unknown state falls back to Balanced. A private local config file stores the user's non-secret preference. The macOS Privacy screen exposes the setting; future background AI/index jobs must consume the policy at admission.

**Tech Stack:** Go stdlib, existing purego Objective-C bridge on macOS, Win32 via x/sys/windows, existing godbus on Linux; cgo remains disabled.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md`, power policy section, adjusted by the user's explicit correction that long battery use or unknown power state must not indefinitely stop work.

## Global Constraints

- Follow-system mode uses a verified OS battery-saver signal; unknown power state resolves to Balanced.
- Passive capture, retention-critical deletion, deterministic work required by user actions, and interactive AI remain enabled in every mode.
- Background AI is local-only, time/token bounded, and concurrency capped; power policy never routes work to a remote provider.
- OS power is read on demand; no recurring polling timer or extra background wakeup is introduced.
- Preference files are private, bounded, strict JSON and atomically replaced.
- Native capture/desktop builds remain cgo-free on Darwin, Windows, and Linux amd64/arm64.

## Review Focus

1. Unknown or unsupported OS power signal must resolve to Balanced, not stall work; test missing/invalid signal behavior.
2. Explicit Saver must never stop capture, retention, or user-requested model calls; cover all modes.
3. Remote provider must never qualify as background AI, including Performance; cover admission.
4. Malformed, symlinked, over-permissioned, oversized, or truncated preferences must not be accepted; test file boundaries.
5. OS readers must not leak battery percentage, machine identity, or raw backend errors; return only closed saver state.

---

### Task 1: Pure policy and OS state readers

**Files:** `internal/power/policy.go`, `internal/power/policy_test.go`, and build-specific `system_state_*.go`.

- [x] Write and run the policy decision tests before implementing the package.
- [x] Implement mode resolution, bounded local background-AI admission, and on-demand native saver-state readers.
- [x] Verify focused tests, race tests, supported-target cross-builds, and no-cgo audit.

### Task 2: Private persisted preference and macOS controls

**Files:** `internal/power/config_store.go`, `internal/power/config_store_test.go`, `internal/client/client.go`, `internal/client/bootstrap.go`, `internal/client/desktop.go`, and `internal/client/desktop_test.go`.

- [x] Persist validated policy in a separate private atomic file.
- [x] Expose policy in Privacy settings and clarify that battery alone does not pause background work.
- [x] Expose bounded per-mode job, token, and duration ceilings; validate them before saving.
- [x] Verify controller and UI state/error cases, then full test, vet, cross-build, and no-cgo gates.

### Task 3: Verify background-work consumers

**Files:** `internal/chronicle`, encrypted storage, macOS client, and their tests.

- [x] Route optional hourly summaries through a fresh policy admission; background provider is local-only and consent defaults off.
- [x] Keep user-initiated note processing and Ask separate from optional background limits and provider routing.
- [ ] Measure idle wakeups and CPU/memory before claiming battery savings.
