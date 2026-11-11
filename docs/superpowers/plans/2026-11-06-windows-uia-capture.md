# Windows UI Automation Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Windows capture placeholder with passive, bounded capture from the currently foreground UI Automation tree, wired into the encrypted ingest path.

**Architecture:** Keep Windows native calls behind `internal/platform/windows` and `internal/capture`, reusing the existing sanitized `capture.Pipeline` and generic bounded AX-tree walker. COM calls run on a locked MTA thread; UI Automation transaction timeout and the pipeline's node/depth/byte/deadline limits bound provider work. Foreground/focus notifications use coalesced WinEvent hooks, not polling.

**Tech Stack:** Go, `x/sys/windows`, existing `purego` only where Win32 callbacks require it; Windows COM/UI Automation; no cgo.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md`, `docs/superpowers/plans/2026-11-05-blur-delivery-roadmap.md` (Phase 3).

## Global Constraints

- All capture remains passive and restricted to the current user-opened foreground context; no clicks, scrolling, opening, or history traversal.
- Raw UIA text is package-private and is redacted before storage; UIA password controls, elevated targets, and unknown/secure desktops are rejected before text reads.
- Windows capture uses the existing default ceiling: 256 KiB text, 2,048 nodes, depth 32, 750 ms total.
- UIA provider transactions must not use the platform default 20-second timeout; configure a bounded timeout below the overall pipeline deadline.
- Capture pauses release event hooks; resume re-establishes them. No background timer or model call is introduced.
- Windows builds remain cgo-free. Tier-1 implementation target is Windows 11 amd64; arm64 remains a separate capability gate unless native CI proves it.
- Do not request UIAccess, administrator elevation, or a UIPI bypass. Higher-integrity targets report `target_elevated` and are not read.

## Review Focus

1. Password or secure controls: UIA `IsPassword` and secret-like role/property data must be checked before `Value`/`TextPattern` access; test that no read is attempted.
2. Foreground changes during a slow read: stale HWND/process leases must discard the entire frame; test focus/app switch between read start and completion.
3. Unresponsive provider: UIA transaction and outer context deadlines must return without a second concurrent native read; test timeout behavior and worker recovery.
4. Oversized/deep trees: traversal must stop at existing node/depth/byte ceilings and release all COM references; test exact caps and cancellation cleanup.
5. Elevated, locked, secure-desktop, or absent foreground: report structured degradation and perform zero text reads; cover native Windows fixture tests and cross-compile conformance.

---

### Task 1: Implement foreground identity and lifecycle events

**Files:**
- Create: `internal/platform/windows/adapter.go`
- Modify: `internal/platform/windows/adapter_windows.go`
- Create: `internal/platform/windows/foreground_windows.go`
- Create: `internal/platform/windows/events_windows.go`
- Test: `internal/platform/windows/adapter_test.go`

**Interfaces:**
- Produces: `Adapter.WithVerifiedTarget(context.Context, string, func(context.Context, FocusedTarget) error) error`, implementing the same single-read lease contract as Darwin/Linux; `Adapter.Suspend(context.Context) error`; buffered `Next(context.Context)` events.
- Target IDs are opaque random tokens; exported `FocusedTarget` contains HWND/PID/process identity only for the duration of the callback and is never serializable or persisted.

- [ ] Write tests named `TestProbeUsesOpaqueTargetAndStableExecutableIdentity`, `TestVerifyTargetRejectsForegroundSwitch`, `TestElevatedTargetIsNeverRead`, `TestNextCoalescesForegroundAndFocusEvents`, and `TestSuspendReleasesWinEventHooks`.
- [ ] Run `go test ./internal/platform/windows`; expect missing symbols / failing behavior (the adapter policy tests use an injected backend and run on any host).
- [ ] Implement foreground HWND/PID/executable discovery, token-backed lease revalidation, integrity-level rejection, and coalesced `SetWinEventHook` subscriptions. Keep callback payloads metadata-only.
- [ ] Run `go test ./internal/platform/windows`, Windows cross-compilation, and `go test ./internal/platform/conformance`; expect explicit degraded reports when native API initialization fails.

### Task 2: Build the bounded UIA accessibility tree adapter

**Files:**
- Create: `internal/capture/pipeline_windows.go`
- Create: `internal/capture/ax_tree_windows.go`
- Test: `internal/capture/pipeline_windows_test.go`
- Modify: `internal/capture/ax_tree_test.go`

**Interfaces:**
- Consumes: `platform/windows.Adapter.WithVerifiedTarget` and the package-private `axTreeNode` contract in `internal/capture/ax_tree.go`.
- Produces: `capture.NewWindowsPipeline(source *windows.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile) Pipeline`.

- [ ] Write tests named `TestWindowsPipelineRejectsPasswordBeforeReadingValue`, `TestWindowsTreeTraversalStopsAtNodeAndDepthLimits`, `TestWindowsPipelineDiscardsFrameAfterTargetLeaseChanges`, and `TestWindowsPipelineRedactsBeforeObservationConstruction`.
- [ ] Run `go test ./internal/capture -run 'TestWindows'`; expect the Windows pipeline / UIA tree implementation to be missing.
- [ ] Implement COM MTA initialization on a locked worker thread; set UIA transaction timeout; use a foreground-window root and bounded child-by-child control-view traversal. Read only supported text/value patterns after checking password sensitivity and role exclusions.
- [ ] Run capture race tests, Windows cross-compiles for amd64, no-cgo audit, and sentinel scan. Confirm all COM references are released on success, error, timeout, cancellation, and truncation.

### Task 3: Wire Windows capture into the desktop

**Files:**
- Create: `internal/client/capture_windows.go`
- Modify: `cmd/blur/main_windows.go`
- Modify: `internal/platform/conformance/conformance_windows_test.go`
- Modify: `README.md`, `docs/architecture.md`, `docs/threat-model.md`

**Interfaces:**
- Consumes: `capture.NewWindowsPipeline` and the shared `ingest.NewWithActivityAndNotify` service.
- Produces: explicit Start/Pause capture controller parity with Darwin/Linux; disclosure states UIA cannot read elevated or unsupported targets.

- [ ] Write tests named `TestWindowsCaptureControllerIsDormantUntilExplicitStart`, `TestWindowsPauseStopsSubscriptionsAndWorker`, and `TestWindowsCaptureUsesEncryptedSinkAndSanitizedObservations`.
- [ ] Run the focused Windows cross-compile and controller tests; expect missing Windows build-tagged wiring.
- [ ] Implement Windows `CaptureController` and pass it to `RunDesktop`; preserve the existing DPAPI-backed encrypted store and per-user single-instance lock.
- [ ] Update docs to state tested Windows version/architecture and unsupported/elevated app behavior; do not claim support until Windows fixture CI passes.

### Task 4: Add a real Windows fixture and release gate

**Files:**
- Create: `test/axfixture/windows/main.go`
- Create: `internal/platform/windows/uia_integration_windows_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`

**Interfaces:**
- Consumes: completed adapter, pipeline, and desktop wiring.
- Produces: `make windows-uia-test` on a Windows desktop runner, with synthetic accessible document/edit/password controls and no personal applications or data.

- [ ] Write the fixture test first: assert text capture, password exclusion, redaction, foreground lease invalidation, event delivery, pause, and no cgo.
- [ ] Run the Windows integration target before fixture implementation; expect the fixture executable/test to be missing.
- [ ] Implement a minimal synthetic Win32 fixture with UIA-compatible controls; no network or real user documents.
- [ ] Run race tests, Windows amd64 build/test matrix, no-cgo audit, package/license scan, and independent Windows security review. Mark Windows capture supported only after all pass.
