# macOS Background Menu and Default Capture Plan

> **For agentic workers:** implement in the current worktree using test-first steps; preserve unrelated dirty changes.

**Goal:** Keep Blur and capture active when the main macOS window closes, provide a configurable menu-bar item with Open/Quit actions, and start capture by default unless the user has explicitly paused it.

**Architecture:** Persist only the non-secret capture/menu-bar preferences in an owner-private local settings file. AppKit owns a minimal status item and menu; GoUI owns window state, and close is vetoed while the native window is hidden. Quit remains the only menu action that ends the process and runs capture/store cleanup.

**Tech Stack:** Go stdlib, existing GoUI and purego Objective-C bridge; no cgo.

## Requirements and decisions

- First-run defaults: capture on and menu-bar item on.
- Respect an explicit persisted capture pause on subsequent launches.
- Startup capture may ask macOS once to display its own Accessibility guidance; Blur never grants or edits TCC state, unavailable access is reported, and the wizard rechecks automatically.
- Closing the main window hides rather than destroys it; reopening uses the status item, or the Dock if the item is disabled.
- Menu-bar Quit routes through normal NSApplication termination; capture shutdown and encrypted-store close remain in the existing process cleanup path.
- Keep current Today/dashboard design unchanged in this slice. Prototype the centered quick-capture surface separately after reviewing the proposed direction with the user.

## Review focus

- Missing settings must mean default-on, while malformed settings must fail closed for capture.
- Hidden window remains mounted and can be shown repeatedly without rebuilding or losing UI state.
- Disabling the menu item removes it without quitting the app; Quit stops the app and its capture worker.
- Permission unavailable at startup may show the native macOS guidance once, must never grant TCC access, and must not claim capture is active while periodic capability checks continue.
- Repeated status-item creation/removal must not leak native status items or menus.

## Implementation tasks

1. Add private preferences store and tests for first-run defaults, persisted pause/menu visibility, strict parsing, context cancellation, and file permissions.
2. Wire preferences into `LocalApp`/`Client` and add startup capture with an explicit unavailable state; persist manual pause/resume.
3. Add a Darwin-only AppKit status item with Show and Quit actions, replace window close with hide, and add a Privacy setting to configure visibility.
4. Add Darwin native integration tests, lifecycle regression coverage, docs/checkpoint update, then run full tests, race tests, vet, cross-builds, and no-cgo audit.
