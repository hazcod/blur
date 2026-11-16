# Native Capture Coverage Implementation Plan

> **For agentic workers:** Root executes native capture inline with one final
> fresh review. Independent model-search, GitHub issue and release repairs run
> in parallel under explicit user instruction and separate file ownership.

**Goal:** Capture useful text in ordinary macOS apps using the native strategies
verified in Goldfish, with secure-field exclusion and honest coverage.

**Architecture:** Generic native AX values plus bounded attributed/range
fallbacks feed the existing sanitization/encrypted observation path. Read
sensitivity and target guards remain attached to deferred native calls.

**Tech stack:** Existing Go/purego and ApplicationServices/CoreFoundation APIs.

**Spec:** `docs/superpowers/specs/2026-11-13-native-capture-coverage-design.md`

## Global constraints

- No app allowlist for readable content and no blanket editability exclusion.
- Preserve password/private/source/domain/target/deletion protections.
- Native text and API errors do not enter logs or unencrypted disk buffers.
- Existing work budgets bound each request; disclose omitted content.
- Shared worktree, no reset or broad commit; exact app identity and sandbox.
- Live app coverage is unverified while CUA is unavailable.

## Review focus

1. Text becomes secure after tree collection or while AX is answering: no text
   returns or persists.
2. Non-string/attributed values and unknown range types fail safely.
3. Huge/negative/overflowing UTF-16 ranges never trigger unbounded reads.
4. UTF-16 surrogate boundaries and a visible range after a long prefix retain
   readable current text without claiming whole-page coverage.
5. Excluded/private/domain-changing contexts never reach alternate text APIs.

## Task 1: Editable text and generic native fallback

**Files:** native `internal/capture/pipeline_darwin.go`, new
`native_text_darwin.go` and tests, current native role tests and document opener.

- [x] Demonstrate editable editor content RED through actual pipeline fixture.
- [x] Replace editability pruning with fresh secure role/subrole checks; test
  secure transitions before/during values, cancellation and arbitrary app ID.
- [x] Add real CFString/CFAttributedString boundary fixtures and range-only
  source regressions; observe RED before production changes.
- [x] Implement owned text conversion and bounded AXStringForRange fallback,
  with validated metadata, fresh sensitivity, Unicode and partial coverage.
- [ ] Full capture/ingest/native platform tests, affected races and CGO0 build.

## Task 2: Integration, disclosure and release gates

- [ ] Read/review independent model, schema, GitHub issue and release repairs;
  preserve their ownership while making required integration changes.
- [x] Confirm capture error/status copy does not mislabel failed reads as idle
  startup, and capture settings describe edited content accurately.
- [ ] Run complete source tests and lint, affected races, build/cross-compile
  checks and one final fresh review; correct material findings with RED/GREEN.
- [ ] Attempt native inspection only when service state changes; preserve
  exact existing Downloads identity and report any remaining runtime block.
- [ ] Record full large-capture publication/recall and OCR scope honestly;
  do not claim those unfinished parent gates complete.

## Approved correction: unsandboxed direct download

User approved removing App Sandbox on 2026-11-13 after reviewing Apple's
documented restriction and the installed Goldfish signed entitlements.

- [x] Observe existing-container startup losing memory/settings RED under an
  ordinary-home launch; share desktop/MCP data-directory resolution and reuse
  the existing directory in place, then confirm GREEN with a real temporary
  encrypted store and fake OS secret store.
- [x] Cover fresh install, existing container plus legacy directory, rejected
  links/files, denied inspection and existing sandbox config path.
- [x] Worker changes default/stable/release host and helper to unsandboxed,
  preserves explicit compatibility opt-in, and verifies actual signed fixtures.
- [x] Final fresh review of path continuity and build/release policy; all source
  tests, affected races/lint, build canonical signed Downloads app, then verify
  exact existing leaf certificate/designated requirement and absent Sandbox on
  host/helper. No application lifecycle action is implied by build approval.
