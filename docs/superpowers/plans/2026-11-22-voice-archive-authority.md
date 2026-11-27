# Voice archive authority implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Existing inline execution continues.

**Goal:** Recall saved voice evidence across ordinary and historical questions,
and keep Ask/original navigation current through deletion and queued delivery.

**Architecture:** Reuse bounded source-index windows and scoped source browsing,
then require authenticated meeting membership. Share Client/durable read tokens
across direct time and recorded-content Ask, conditional history, native voice
lookup and transcript page installation.

**Tech Stack:** Existing Go Client, assistant, meetings and encrypted durable store;
incumbent native Go UI. No new dependencies or durable format.

**Spec:** `docs/superpowers/specs/2026-11-22-voice-archive-authority-design.md`

## Global constraints

- Root main is explicitly authorized; retain all existing work. No commit/push.
- Schema7 and existing physical read/write/index/voice/model limits unchanged.
- At most15 28-day index windows and400days; source hydration keeps2MiB budget.
- Tiny publication callbacks: loaded state only, no Client/storage calls or IO.
- Build only `make macos-app-stable` to `~/Downloads/Blur.app`, preserve exact
  certificate/requirement, com.hazcod.blur, unsandboxed audio-input-only signing.
- No user store, app lifecycle, microphone/system recording, TCC, trust or asset changes.
- Whole suite uses `go test -timeout=30m ./...`; the durable suite's verified
  successful runtime exceeds the default10-minute package deadline.

## Review focus

- Old explicit voice notes versus ambiguous nearby video: archive evidence must
  not be hidden by the heuristic or acquire unsupported website/speaker identity.
- A year containing both ends and an older late phrase behind newer unrelated
  sessions: index/scoped browse stay bounded without silently dropping the scope.
- A source erased or replaced during a split read, model call, own history write
  or queued native original/Ask delivery must not become a current answer.
- Forged app/domain metadata, authored notes, source-time provenance and local
  boundaries must retain their actual authority through Search/Ask/navigation.
- Dense/empty/cancelled/error/overlong windows and pages must return truthful
  coverage or a zero-result error, and mounted controls stay bounded.

### Task 1: Authoritative voice archive recall

- [x] Add actual encrypted fixtures/tests in `voice_archive_test.go` for old
  explicit voice-note/transcript requests, scoped year topical and no-topic
  recall, early/late sources and a late phrase beyond recent pages; run RED.
- [x] Update `meeting_recall.go` explicit-source classification and reuse
  `splitSearchWindows`/`searchExactWindows` or `browseSearchScope` as appropriate,
  with live meeting authentication and truthful omitted/indexing coverage.
  Preserve ambiguous video behavior. Run existing/new voice and long-Search GREEN.

### Task 2: Result-bound native voice authority

- [x] Add queued real-store tests for native transcript/source opening and Ask,
  direct erasure and replacement, wrong note/request, cancellation, unchanged
  success and own-history write; run RED before changing production.
- [x] Extract the existing read-publication state into a small general private
  token (`memoryReadPublication`) while retaining activity session accounting.
  Extend recorded-content Ask/grounded follow-ups, conditional question history,
  `memory_voice_source.go`, `meeting_view.go`, `meeting_summary.go` and
  `voice_recap.go` original/summary installation. Tiny
  callbacks retain existing labels/paging. Run focused GREEN and incumbent time
  and recap-source publication tests.

### Task 3: Runtime histories and final gates

- [ ] Add `FuzzVoiceArchiveRecall` against actual encrypted meeting/source
  lifecycles with independent inserted-ID/time/text ledger, competing sessions,
  dated and unscoped questions, erasure, reopening and prior-answer follow-ups.
  Add empty input as a real history and retained seeds; run mutation campaign
  with immutable source fingerprint and separate corpus-diversity accounting.
- [ ] Run focused race/vet, one required fresh read-only whole-increment review,
  and RED→GREEN material fixes. Record every declined judgment/ruling.
- [ ] Run complete suite/current-source runtime, canonical stable build and
  strict original certificate/requirement/source fingerprint verification. Update
  parity/scenario/architecture docs, keep broader goal and live acceptance open.

Task test commands (Expected: PASS after each task's RED→GREEN):

1. `go test ./internal/client -run '(VoiceArchive|VoiceFinal|VideoRecall|NaturalMeeting|NaturalConversation|MeetingRecall|LongSearch)' -count=1`
2. `go test ./internal/client -run '(VoiceArchive|VoiceRecap|SavedVoice|MeetingSummary|LongTime|RecapSource|TimeFollowup)' -count=1`
3. `go test ./internal/client -run '(FuzzVoiceArchiveRecall|VoiceArchive)' -count=1`,
   followed by required final race/vet/review/whole-suite/runtime/build gates.

Ruling: existing autonomous goal, established inline method and explicit root-main
build instructions authorize this existing-flow hardening. No new product-choice
or design approval is reopened. This plan's decisions are the implementer's,
not attributed to the user; app launch remains separately unauthorized.
