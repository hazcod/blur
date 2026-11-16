# Apple Foundation Models implementation plan

> **For agentic workers:** Use executing-plans for root implementation and focused workers for the independent helper/adapters. Preserve the existing worktree and unrelated changes.

**Goal:** Use Apple's on-device foundation model throughout Blur's existing local assistant features, and fix Ask while capture is paused.
**Architecture:** A bundled Swift helper calls only SystemLanguageModel through bounded private stdin/stdout. Go retains retrieval, context admission, citations, typed actions and mutation authority. Apple is an explicit local provider; existing Ollama/remote options remain selectable.
**Tech stack:** Go without cgo; Swift FoundationModels with macOS26 runtime checks and weak linking for existing macOS13 application compatibility.
**Spec:** User requests in this session, plus existing docs/architecture.md Apple feasibility section and personal-memory-plan.md.

## Global constraints
- No user data, credentials or prompts in argv, files or diagnostics; bounded pipes and cancellation.
- Apple runtime context and full requested output admitted before inference; default guardrails, no tools or remote fallback.
- Preserve authoritative source/proposal/action validators and explicit background opt-ins.
- Pausing capture stops new recording; saved-memory Ask and general questions remain available.
- Account-login research is a subsequent provider scope: official ChatGPT/Copilot paths; Claude consumer authentication restrictions respected.

## Review focus
- Unsupported devices, disabled Apple Intelligence, not-ready assets and old OS show actionable unavailable state.
- Malformed, oversized or hanging helper responses fail safely without prompts in errors.
- Context smaller than8192 fits upstream budgets; source-linked outputs reject invented citations.
- Capture-off state cannot disable persisted evidence or question submission.
- Helper is embedded and signed before outer bundle, including sandbox and Safari re-signing.

### Task1: Native model transport
- [x] Add bounded version1 status/count/generate protocol and helper with runtime admission.
- [x] Prove valid output, malformed/bounds/cancellation/context admission via tests; run native synthetic Mira/unknown/refusal cases.
- [x] Add Go LocalCompletion port, Apple transport and truthful OS-build scoped identity; no inference at settings validation time.

### Task2: Existing local adapters
- [x] Reuse assistant/general/notes/intent/Chronicle/Library prompts and validators with Apple constructors.
- [x] Observe failing behavioral regressions, then pass protocol and source validation tests.

### Task3: Client and capture-independent Ask
- [x] Add explicit Apple provider, no endpoint/key fields, asynchronous readiness and correct local disclosure.
- [x] Wire every local completion consumer and retain background opt-ins and remote consent boundaries.
- [x] Reproduce and fix Ask with capture disabled using saved encrypted evidence and UI submission/readiness checks.

### Task4: Bundle and acceptance
- [x] Embed universal weak-linked helper, inner signing/sandbox inheritance and Safari re-sign support.
- [x] Run affected/full/race/cross/no-cgo builds, focused review and synthetic live end-to-end Apple recall.
- [ ] Configure isolated native fixture to Apple through UI when available; verify saved-note Ask with capture paused and record latency/memory limitations honestly.

## Validation outcome

Source integration, full unit/race/build/cross/no-cgo gates, universal signed
sandboxed bundle, source-level sandbox inference, and live synthetic encrypted
recall/intent/notes/Library/Chronicle checks pass. Exact production legacy
byte-count/context branches were forced in a test-only helper copy and all ten
intent fixtures passed; this does not execute an older OS runtime. Fresh focused
review has no open material findings. The original broad milestone is unfinished.

Actual native GUI selection/restart remains blocked by the disconnected CUA
host. The existing running fixture, its data, credentials, and provider setting
were preserved. Updated bundle: /private/tmp/blur-acceptance-qo740lkk/Blur Apple Acceptance.app.
ChatGPT/Copilot login research is recorded in the ignored account-provider-spec;
account connections are not implemented by this Apple provider work.
