# Usable Meeting Notes Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans for your owned component. The user explicitly requested parallel implementation.

**Goal:** Deliver working menu/keyboard access to local encrypted meeting notes.

**Architecture:** Reviewed Swift/purego audio bridge → app-owned lifecycle controller → meeting session engine → encrypted durable repository, plus existing native dashboard UI.

**Tech stack:** Existing Go/Swift/native UI, public Apple permissions/Speech APIs, current encrypted store.

**Spec:** `docs/superpowers/specs/2026-11-13-meeting-entry-points.md`.

## Parallel ownership

1. Durable worker: repository types, encrypted metadata/records, bounded paging,
   exact searchable sources, idempotence/conflict, deletion/retention fences.
2. Native worker: meeting menu and independent shortcut, explicit permission and
   speech-asset actions, same-certificate native library packaging/release checks.
3. Root: app lifecycle/controller, menu call sites, Meetings dashboard, bounded
   live tail/history, summary action and error handling.
4. Natural-input worker: compact durations, default times,12/24h and locale display.

## Execution checks

- [ ] Agree shared repository/native contracts before dependent implementation.
- [ ] Observe RED→GREEN encrypted record replay/search/delete/paging tests.
- [ ] Observe RED→GREEN synthetic controller start/pause/resume/stop, native final
  drainage, interrupted state and permission/asset/conflict error tests.
- [ ] Wire menu/shortcut to focus the real workspace without starting audio.
- [ ] Keep existing style, explicit controls, bounded visible transcript and
  clear recovery states. Preserve unsaved manual notes during refresh/errors.
- [ ] Add bounded provider-policy-aware summaries; no model required for saving.
- [ ] Verify compact relative durations, defaults, explicit12/24h clocks and
  locale display with DST/timezone regressions and updated behavior contracts.
- [ ] Independent source review; fix material findings in one batch.
- [ ] Run requested tests/races/lint/cross/no-cgo/native synthetic gates, build
  same Downloads app, verify exact identity+Hardened Runtime+nested code.

## Review focus

- Opening a menu/window/shortcut must never start audio or download assets.
- Stop/pause/quit/delete must not accept late audio or resurrect deleted text.
- Slow storage must not grow native buffers or silently lose accepted finals.
- A failed save must retain unsaved notes and show an actionable error.
- Existing Quick ask must keep its own shortcut and menu action.
