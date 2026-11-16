# Personal Memory Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Implement and verify the complete personal memory milestone in [the spec](personal-memory-assistant.md).

**Architecture:** Primary encrypted evidence feeds one bounded retrieval path. Exact, chronological, semantic, and entity retrieval produce cited candidates; explicit actions write durable typed records. Background enrichment is local, resumable, source-linked, and fenced against deletion.

**Tech Stack:** Go, existing authenticated Bolt records, purego native macOS APIs, GoUI, local Ollama and explicitly configured answer providers.

**Spec:** `docs/personal-memory-assistant.md`

## Global constraints

- macOS first; preserve existing Windows/Linux compile boundaries.
- No required Safari extension for active browser text capture.
- Preserve encryption, exclusions before reading text, remote consent, bounded work, source citations, and deletion cascades.
- Keep cgo disabled and use the existing stable app bundle/signing path.
- The user explicitly authorised autonomous execution without an additional plan approval step.
- Checkpoint progress and keep the goal active until all explicit requirements have direct evidence.

## Review focus

- More than 64 unique captures in one hour must remain discoverable and deletable.
- Repeated content seen on another day must retain its later occurrence context.
- A source deleted while an embedding, fact, or answer is running must not reappear.
- A reminder edited across a DST transition must fire once at the requested local time.
- Browser content with unknown domain/private state must obey exclusion policy and report missing coverage honestly.

## Task 1: Finish the complete evidence/time path

**Files:** `internal/storage/store.go`, `internal/storage/durable/observation_timeline.go` (new), `internal/storage/durable/store.go`, `internal/storage/durable/index.go`, `internal/client/ask_retrieval.go`, `internal/client/recent_captures.go` (new), `internal/client/client.go`, `internal/client/desktop.go`.

**Interfaces:** `ListObservationWindow(context.Context, storage.ObservationWindowRequest) (storage.ObservationWindow, error)` returns newest-first primary observations with timestamps and stable continuation. `TodayView.RecentCaptures` exposes primary evidence without a generated snapshot.

- [x] Add capture-window contract and cross-backend ordering/cursor checks.
- [x] Let recap retrieval find primary encrypted captures before activity/chronicles exist.
- [x] Show recent primary captures on Today without a materialized recap.
- [x] Replace capped chronicle-hour lookup with a separate paged encrypted timeline; backfill existing sources and remove entries atomically on deletion.
- [x] Verify >64 captures/hour, out-of-order insertion, continuation, reopen/backfill, delete, byte bounds, and local-day capture scope.
- [x] Make lookup/capture retrieval independent of closed activity segments; add explicit repeated-sighting context.
- [x] Run storage/client suites and record current evidence.

Task 1 remaining detail: lookup and capture are independent of closed segments, persisted sightings enter scoped exact and semantic ranking immediately, and direct results carry inspectable sources. Capture-time migration baselines now remain explicitly undated in recall, while retention metadata is preserved. Daily recap selection and model packing distribute evidence across the requested period and disclose actual sampling/excerpts; broader native quality acceptance remains open.

### Task 1 continuation: recap coverage

The current fallback takes six newest captures; two summaries disable that fallback, and greedy context packing can spend the entire text allowance on one source. This violates the requested day-recall behavior. Continue under the existing autonomous authorization; do not restart completed Task 1 work.

**Files:** new `internal/client/recap_retrieval.go` and tests; narrow changes to `ask_retrieval.go`, `recent_captures.go`, assistant context/conversation/service and their tests, plus dashboard/popup coverage copy.

**Interfaces:** bounded raw selection reports whether candidates were omitted. Recaps read the complete bounded chronicle range, supplement it with at most eight time-distributed raw captures, and return at most twelve time-distributed items. Each source retains full saved text for inspection, with at most eight 1 MiB raw records retained; individual window reads keep their existing 2 MiB bound. `Diagnosis.RecapSampled` describes retrieval selection. Recap context shares its existing 4 KiB text budget among admitted sources and includes sampled-window metadata. `Result.CoverageLimited` includes retrieval sampling and actual context omissions/excerpts, and drives a fixed UI disclosure.

- [x] Add failing busy-day, summary-gap, week-pagination, app/domain/time, large-source and cancellation regressions. Expected: morning and late-day evidence reach the answer; unrelated/out-of-window evidence does not.
- [x] Implement time-distributed selection and bounded forward-only chronicle paging, preserving source IDs and current deletion/publication fences. Clamp current-day raw probes to the current time; do not query future activity.
- [x] Add failing context tests showing multiple long sources currently collapse to one; implement recap-only fair text and serialized-byte admission. Preserve ordinary lookup ranking and full originals.
- [x] Carry actual limited-coverage state to generated/offline answers and both UI surfaces. Test the provider payload, original citations, and copy without relying on model compliance.
- [x] Run focused and full Go/race/build checks, independent review, actual Apple synthetic day recall, and replace only the same signed `~/Downloads/Blur.app`. Native UI and real-store recovery remain separate unverified gates.

Repeated-sighting persistence, legacy time provenance and complete native large-page continuation remain open after this continuation.

### Task 1 continuation: repeated sightings

**Decision:** Retain immutable source text and its first-capture time. Extend the encrypted hour timeline to one row per source/hour, with its latest visit, and store exact visit times separately. A source-linked hour chain supports bounded, resumable erasure; a small source/month index supports time-scoped membership before ranking. Existing schema-3 timeline rows remain valid without backfilling source text. Schema 4 rejects older writers that cannot erase visit metadata. The existing phase-7 erasure drains visit hours before removing primary evidence, so old interrupted journals remain resumable.

**Alternatives considered:** Replacing first-capture time loses earlier history. Duplicating source text per visit makes large-page capture unnecessarily expensive. Activity-only recall misses active sessions until they close. Separate unmerged chronological indexes double sparse-range probes; extending the existing timeline avoids that.

- [x] Prove exact visit windows, repeated-source pagination, out-of-order timestamps, encrypted reopen, cancellation and deletion admission with failing store tests; implement bounded encrypted visit metadata and schema migration.
- [x] Preserve visit metadata in ephemeral conformance; record unchanged/revisited captures immediately and notify readers without re-enriching unchanged text.
- [x] Join visit times before exact/semantic ranking and use them for Ask and Today. Preserve originals and first-capture provenance; unscoped MCP still returns the same original source.
- [x] Add time-aware natural query routing to Search and MCP; verify shared visit windows in encrypted-store fixtures. Native acceptance and broader historical windows remain open.
- [x] Exercise interrupted large-history erasure, migration rollback and complete deletion; run full tests, race/build checks and independent review before replacing the same signed Downloads app.

### Task 3 continuation: natural Search and MCP

**Decision:** Add a search mode to the existing deterministic Ask planner. It always interprets input as a read request, extracts the same app/domain/time scopes, and preserves quoted text and technical identifiers as literal terms. Search and MCP share scoped candidate retrieval with configurable result limits instead of inheriting Ask's eight-source answer limit. Topic queries never fall back to unrelated recent pages; a scope-only query lists matching captures. Explicit task queries retain calendar/state/reminder constraints. MCP projects bounded evidence and validates current task revisions before returning it; it never executes actions or invokes an answer model.

**Alternatives considered:** An extra model call adds latency and inconsistent interpretation. Filtering unscoped winners loses scoped matches below the original top results. Separate parsers drift across Ask, Search and MCP.

- [x] Reproduce time/app/domain recall, scope-only browsing, technical literal protection, irrelevant-topic exclusion, shared Search/MCP source results and result-limit behavior with failing tests.
- [x] Implement shared planning, candidate filters before exact/semantic ranking, chronological scope-only results and current task constraints; retain byte, cancellation and deletion/publication fences.
- [x] Expose known visit times in Search/MCP evidence, update the MCP schema/copy and verify bounded output, pending-index responses and concurrent task changes.
- [x] Run full tests, affected race checks, independent review and stable signed-bundle delivery. Broad native acceptance and historical windows beyond the existing retrieval bounds remain explicit unfinished work.

Source review corrections: app and site constraints intersect; alternatives within each dimension remain a union. Literal terms cannot relax to partial or semantic-only matches, overflow is rejected, and explicit URL qualifiers keep only their host. Quoted task topics are restored after unquoted task grammar is removed. Completion/cancellation periods use transition history after resolving the shared scope, including last week and topic-free queries; reminder periods retain full calendar bounds. Every accepted task title remains searchable regardless of the query's token limit. Search/MCP disclose unavailable meaning search or omitted task history separately from a bounded result page. Native storage startup is still unverified: the user-reported failure needs its safe diagnostic, and CUA was unavailable on 2026-11-13.

### Task 1 continuation: legacy capture-time provenance

**Decision:** Keep the authenticated migration baseline for retention and existing index formats. Load that immutable value before publishing the store and classify an equal source timestamp as unknown for activity recall. This conservatively handles the indistinguishable exact-nanosecond collision with a genuine capture. Later recorded sightings remain observed times. No new storage schema or user-data rewrite is needed.

**Alternatives considered:** Treating the baseline as observed invents activity. Clearing stored times breaks retention, index validation and restart compatibility. A new per-source migration cannot reconstruct lost original dates.

**Files:** durable capture-time migration, sightings, timeline, capture-hour and chronicle readers; semantic service; client source hydration and evidence; assistant context; Search/MCP metadata and native captions.

- [x] Reproduce an authenticated baseline record appearing in dated windows, exact/semantic recall and hourly evidence; prove original text and retention baseline survive reopen.
- [x] Exclude baseline-only occurrences, including seeded same-hour visit arrays, before date ranking. Keep genuine earlier/later sightings and stable pagination. Prevent new false-dated chronicles and omit existing summaries containing baseline-only sources.
- [x] Preserve unscoped recall, expose unknown capture time in Search/MCP and model evidence, and retain verified activity timestamps. Prove bounded projection and metadata schema acceptance.
- [x] Run full tests, relevant race/build/cross checks and independent review, then replace the same signed Downloads app. Actual storage-startup diagnosis and the broader native acceptance gates remain open.

## Task 2: Extension-free browser coverage and capture health

### Large-capture continuation: verified storage prerequisite

The dedicated native-reader, streaming-redaction and source-position plans are
complete as source prerequisites. The encrypted-capture plan now implements
canonical capture-owned parts, encrypted private staging, checked atomic edition
publication, exact visits, ordered bounded reads and bounded restart cleanup.
Source/family/whole-capture erasure uses the existing gated journal; newer real
visits survive conditional deletion. A permanent strict capture-retention cutoff
also rejects older jobs whose first staging transaction arrives after cleanup.
Schema 6 upgrades 1–5 metadata without rewriting historical observation bytes;
the schema 5 reader boundary rejects it. Full source tests, affected races, vet,
builds and cross compilation pass. The final independent review found two
material issues (cursor recreation and duplicate cleanup); both were reproduced
and fixed once, then the complete source gates passed again. This is not a
shipped or complete large-capture path.

The full [seven-part design](superpowers/specs/2026-11-13-large-document-capture-design.md)
still requires private part/time indexing; production capture jobs with focus,
scope, private-state and permission cancellation; useful honest coverage on short
views; one activity visit per capture; the explicit read-only technical text-area
policy; whole-capture Ask/Search/MCP and source inspection; migration-safe audit
of the inherited version-1 Unicode redaction edge; and native acceptance. Keep
the signed Downloads app at its current schema 5 build until that usable path is
complete. The actual Library startup failure remains an independent diagnostic
gate; source verification does not establish runtime recovery.

### Capture-part search isolation: verified source prerequisite

The [isolation plan](superpowers/plans/2026-11-13-capture-index-isolation.md)
reuses the bounded encrypted exact tree with a separate capture-part scope.
It shares the original live transaction/manifest, database lifetime and deletion
gate; observation key/domain/format bytes remain unchanged. Seven new encrypted
fixture tests, full tests, affected races, vet, builds and cross compilation pass.
The independent review found no material defects. Two additional proof cases
(weighted ordered IDs/scores and manifest verification after a mixed facade write)
remain assigned to capture integration. Production does not create this index yet.

Next integration must queue published editions with bounded part work, validate
live edition membership and exact visit times, erase all index rows through the
existing journal, establish a newer supported-writer/schema boundary, and connect
Search/Ask/MCP/native capture. The observation-only post-commit inline optimization
must not dispatch capture work through the outer Store scope. The full seven-part
scope and actual Library startup diagnostic gate remain open; keep the installed
schema-5 app unchanged until a complete usable path is ready.

**Files:** `internal/appidentity/browser.go`, `internal/capture/profiles.go`, `internal/capture/pipeline_darwin.go`, `internal/platform/darwin/adapter_darwin.go`, `internal/client/capture_darwin.go`, `internal/client/desktop.go`, `internal/client/capture_health.go` (new).

**Interfaces:** Browser domain discovery returns `(domain string, known bool, err error)` before text-value callbacks. Capture health exposes latest success/failure category, capture coverage, and index progress without logging captured content.

- [x] Register Orion in browser policy, profiles, display names, and question app aliases.
- [ ] Check active web-area URL/domain discovery for Safari, Orion, Chromium, and Firefox; preserve private-window and focus-race checks.
- [x] Distinguish permission, exclusion, inaccessible page, empty capture, and index state in UI/Ask diagnostics.
- [ ] Verify real active-page capture with no extension and exclusion before text reads.

Task 2 implementation evidence (2026-11-12): browser capture now selects one visible outer AXWebArea under a bounded metadata walk, prunes toolbar/tab/form subtrees before any values, and retains a structural lease. Page-path checks run before/after capture; direct page/window URL checks surround each value, preserving domain/private/focus checks without a per-value whole-window rescan. CFURL/CFString locations are supported. Missing/ambiguous pages and unknown required domains have distinct safe categories. Optional AXManualAccessibility requests are rate bounded per foreground PID. macOS refreshes content every 15 seconds while input-age metadata reports the user present, closing the gap when content loads after focus; idle or unreadable/excluded contexts stop time attribution to the previous source. Health on Today/Settings and empty Ask answers retains the last unresolved problem even while Blur's own excluded interface is foreground. Contract/native-ABI tests pass; actual Safari/Orion/Chromium/Firefox and large-page native coverage remain unproved. Bounded native AXTextArea capture is implemented and covered by synthetic Terminal/iTerm2/unknown-reader tests, including secure-role checks; live acceptance in Terminal/iTerm2 and large technical documents remains unverified.

## Task 3: Semantic retrieval and resumable backfill

**Files:** `internal/semantic/` (new), `internal/storage/durable/semantic.go` (new), `internal/client/memory_index_settings.go` (new), `internal/client/memory_index_controller.go` (new), `internal/client/memory_index_view.go` (new), `internal/client/hybrid_search.go` (new), `internal/client/ask_retrieval.go`, `internal/client/client.go`, `internal/client/desktop.go`, MCP search adapter.

**Interfaces:** An embedding provider returns bounded vectors for source chunks and queries, identified by model/dimension/version. The semantic store persists source-linked vectors and generation/cursor state. A hybrid retriever merges exact and semantic results under one candidate/evidence budget with app/domain/time scopes.

- [x] Define chunk, embedding, generation, progress, query, and ranked-hit contracts with strict limits.
- [x] Implement local Ollama embeddings and encrypted persistence/deletion; refuse model/dimension mismatch.
- [x] Add resumable initial backfill and incremental enrichment; expose indexing coverage and retry state.
- [x] Combine exact/semantic/temporal/entity candidates for Ask, Search, and MCP.
- [ ] Verify paraphrases with no shared query words, code strings, time/app filters, model replacement, restart, cancellation, and concurrent deletion.

Task 3 historical evidence: local `embeddinggemma` and the opt-in encrypted-client acceptance test cover the legacy vector path, including backfill, scoped retrieval, MCP, reopen, and deletion. That path is not the product's standard search route. The later single-model decision uses the selected answer/note provider for model-assisted meaning search and leaves legacy `memory-index.json` settings dormant and unmodified; see `single_model_search_test.go`. Future ranking, indexing, or UI work must preserve one user-visible model setting. Native UI acceptance and broader real-capture relevance/scale review remain open. Initial cross-build found an upstream renameio Windows failure: the graph/heap core is now included locally under CC0 with portable cosine and no plaintext persistence API.

## Task 4: Automatic, correctable Library

**Files:** `internal/knowledge/derive/`, `internal/knowledge/`, `internal/storage/durable/knowledge*.go`, `internal/client/knowledge_enrichment.go` (new), `internal/client/desktop.go`.

**Interfaces:** Enrichment consumes source IDs and bounded evidence and produces inferred entity/fact proposals with explicit provenance. Acceptance/materialization validates source existence inside the write transaction. Corrections are separate user records applied over inferred generations.

- [x] Add a local background source queue and bounded entity/fact extraction.
- [ ] Persist inferred facts and connections automatically with visible provenance and conservative identity matching.
- [ ] Add inspect/edit/correct/dismiss controls and source links; preserve corrections through rebuild.
- [x] Backfill existing evidence; verify no manual “Suggest facts” click is required for population.
- [ ] Verify ambiguous names, contradictions, repeated extraction, restart, and source deletion.

Task 4 source implementation now includes encrypted inferred entities and reverse provenance, contextual names without the legacy unique-alias constraint, source-backed connections, correction/dismissal precedence, and native inspect/edit/source controls. Automated tests cover contradictory evidence, same names across sites, restart, source deletion during a stubborn model call, manifest replacement, queue overflow before an incomplete cursor, and deletion at 128 sources per entity / 64 entities per source. The real local qwen3:14b acceptance test populates Library through background backfill without Suggest, preserves a correction through rebuild/reopen, removes source-linked claims on deletion, and clears Library on Delete all. Logs: `.superpowers/sdd/personal-memory-plan/task-4-local-integration.log`, `task-4-race.log`, `task-4-final-delta.log`. Native UI acceptance, ambiguity within one app/site, correction retrieval for arbitrary non-entity queries, and broader scale/quality review are still open; remaining checkboxes intentionally stay open.

## Task 5: Durable tasks and real reminders

**Files:** `internal/tasks/` (new), `internal/reminders/` (new), `internal/storage/durable/reminders.go` (new), `internal/platform/darwin/notifications_darwin.go` (new), `internal/client/quick_panel.go`, `internal/client/desktop.go`, bootstrap/lifecycle.

**Interfaces:** Typed task/reminder records support create/update/complete/cancel. The scheduler uses a clock, encrypted repository, and native delivery port; stable delivery IDs make scheduling/recovery idempotent.

- [x] Add encrypted task/reminder state and explicit scheduling parser with IANA timezones.
- [x] Implement purego native notification permission/status and delivery with cancellable pending requests.
- [ ] Recover schedules after restart; handle overdue, DST, recurring requests, edits, and cancellation.
- [ ] Integrate one entry point and a browsable pending/completed UI; report exact scheduled outcome.
- [ ] Verify delivery once, edit/cancel/restart, denied notifications, and deletion semantics in tests and native UI.

Task 5 source implementation now includes encrypted, revisioned tasks and reminders, a first-class Tasks page, explicit task/reminder quick-entry actions, purego UserNotifications scheduling, generic notification content, pending/delivered reconciliation, native cancellation fencing, and deletion/retention cleanup. Delivery stores the requested schedule separately from the actual next native trigger time. Automated checks cover denied permission, restart, overdue unsent intent, uncertain/dismissed delivery without re-alerting, daily/weekly schedules, DST gaps/folds and explicit fold choices, edit/cancel/complete/delete, source fanout, idempotent submission retries, and encrypted reopen. SDK tests create real native triggers and exercise genuine framework completion blocks against a synthetic loopback server; they do not post user notifications. Current full affected race log: `.superpowers/sdd/personal-memory-plan/task-5-race.log`. Native permission/delivery/edit/cancel/restart remains unverified in the actual app, so remaining checkboxes stay open. Today suggestions now create real source-linked tasks atomically with an idempotent receipt. Legacy explicitly confirmed rows backfill; pending candidates never activate automatically, proposed dates do not authorize scheduling, and edited/completed/removed tasks are not recreated. Today reads current task state, supports task editing/completion/source inspection, and refreshes when revisited. Native acceptance of this flow remains open. Audit native pending-request capacity before claiming scale coverage.

## Task 6: Profile, writing rules, and intelligent intent

**Files:** `internal/profile/` (new), `internal/storage/durable/profile.go` (new), `internal/assistant/service.go`, `internal/assistant/planner.go`, `internal/client/quick_panel.go`, `internal/client/desktop.go`.

**Interfaces:** Explicit user memories and rules have durable IDs, scope, enabled state, timestamps, and user provenance. Intent parsing returns a typed ask/note/task/reminder/preference action and any missing essential field. Applicable profile/rule records enter the bounded answer context.

- [x] Save explicit “remember” facts and writing rules without requiring a model.
- [ ] Add inspection/edit/pause/delete controls and global/app/domain/project/audience scopes.
- [x] Improve ambiguous natural-language routing with bounded model parsing and deterministic validation.
- [ ] Carry enough conversation context for follow-ups, resume requests, and reminder edits.
- [ ] Verify questions stay questions, offline writes persist, scoped rules apply correctly, and failed actions preserve input.

Task 6 source progress: explicit user facts and writing rules have an encrypted revisioned repository, independent Delete All cleanup, offline quick-entry writes, and a Preferences page with edit/pause/delete plus combined app/domain/project/audience scopes. Local Ollama receives bounded applicable rules and relevant authored facts; general answers receive style rules without personal facts, and previous remote Ask consent does not admit this new context. Direct preference inspection works without a model. Real synthetic qwen3:14b checks cover cited fact recall, scoped Dutch style, pause, and a detailed six-paragraph general answer.

Quick entry now interprets bounded natural language into validated ask/note/task/reminder/preference actions. Writes preserve original text; actions bind app-owned IDs and revisions, reject ambiguous targets, clarify missing reminder fields, and execute through real task scheduling APIs. Encrypted task state history supports offline current/completed/next questions, source-backed day context and current-task follow-ups. Real qwen3:14b checks cover technical general questions, personal project routing, prose notes, tasks, reminder creation and edits, and a code-edit follow-up. Tests also cover stale action targets and deletion invalidation.

General conversation carries only consecutive exchanges explicitly generated without personal memory. Private answers and action receipts end that context; older remote consent cannot admit it. Input accepts up to4096 bytes, complete validated answers remain available in the transcript, and actual serialized context packing reduces evidence/rules to fit the configured budget while preserving full inspectable citations and reporting omissions/excerpts. The full Go suite, affected race suite, local-model checks and cross-platform build gates have passed during this work; current terminal results are recorded in the checkpoint and Task6 logs. Today-to-task unification and offline inspection of original task evidence are implemented, including multi-source provenance, deletion fences, stale snapshot receipt preservation, and UI navigation/refresh regression coverage. Source-backed rewriting of earlier personal answers, broader topic/semantic task retrieval, a remote personalization consent control, and actual native acceptance remain open. The remaining Task6 checkboxes deliberately stay open until their full scope is verified.

## Task 7: Privacy, backfill, and native completion audit

**Files:** deletion coordinator, retention worker, replica importer/exporter, all new derived stores, docs, native acceptance record.

- [ ] Extend atomic deletion/retention/invalidation to semantic data, inferred facts, profile links, queues, and active jobs.
- [ ] Verify sync/backfill cannot restore a deleted source or erase explicit corrections.
- [x] Finish the installed Goldfish UI audit; update use cases from observed controls.

Installed Goldfish 0.1.69 audit completed through CUA on 2026-11-12. Product/settings sections, Wiki correction/source controls, recurring check-ins, and help/feedback pages were inspected; editors were canceled and settings/data left unchanged. Detailed observations and scope distinctions are in `docs/personal-memory-assistant.md`. Read-only installed binary/schema inspection is recorded in `docs/goldfish-local-architecture.md`; exact retrieval planning/ranking is not reconstructed.
- [ ] Run the native acceptance cases from the spec and record reproducible outcomes.
- [ ] Run relevant race tests, vet, cgo-disabled build, cross-build/test compile, and diff checks.
- [ ] Audit every numbered spec requirement against authoritative evidence. Mark the active goal complete only when all are proved.

### Outstanding storage and native gates

Schema3 migration now rejects older clients and preserves authenticated old evidence; rollback, reopen, full suite, affected race, and cross-platform gates passed. The next storage work must retain bounded transactions rather than assume the already-passing shared-source task fanout covers every layout.

- [x] Reproduce distinct-secondary task fanout and combined near-maximum Library/legacy/task/vector deletion. The first case failed, then passed with staged erasure. Combined coverage includes64 near96KiB inferred entries (preserving explicit corrections and unrelated claims),2048 legacy entities,64 tasks, and maximum896×1024 semantic vectors.
- [x] Add bounded resumable erasure with an encrypted journal and exclusive publication gate. All eight phase interruptions, populated Today/activity cleanup, real subprocess exit without Close, corrupt-journal rejection, cancellation, service retry order and reader admission checks pass. Merge history is cleaned as well as current entities. Full suite, affected race, build/vet/cross/audit gates passed in task-7-erasure-final-* logs. Native notification cancellation remains a prerequisite through the deletion coordinator; native acceptance remains open.
- [x] Repair capture persistence across exact-index term limits in source and automated checks. The original probe's128/256-word rejection reproduced as a genuine RED, then all three64/128/256-word cases persisted. Primary evidence/time links and an encrypted ID-only work receipt commit independently of posting maintenance. `InlineObservationTerms` is only an optimization; larger sources index all terms in64-term resumable transactions without an AI model. Bounded membership proofs support querying without loading the whole vocabulary. Staged erasure drains every proof/reverse page. Tests cover20,000 unique terms, an exact1MiB source, tail terms/passages, a real process exit without Close, partial/complete erasure interrupted after a page, corruption/repair with the original retained, model-free startup resume, pending Search/Ask/MCP coverage and large-result hydration. A real local `qwen3:14b` answer retrieves/cites a release-gate fact after20,000 distinct words with the original source still inspectable. Current full suite, affected race, fuzz, build/vet/cross/audit gates pass (task-7-large-* logs). Native acceptance remains open.
- [x] Repair native prefix loss and propagate partial-source provenance in source/automated checks. Genuine REDs proved that macOS `CFStringGetCString` returned no prefix for oversized values and the common reader discarded collected text when another node exhausted the byte budget. The bounded scalar CoreFoundation converter now returns valid prefixes, exposes UTF16 continuation offsets, preserves embedded-NUL tails and surrogate boundaries, and checks cancellation on fast and ranged paths. Actual framework fixtures reconstruct1.8MB in bounded pieces. Native child pages support offsets beyond4096 nodes with parent-count fences and safe short reads. Capture health, Search/Ask/MCP/source inspection and model context distinguish partial captures from complete originals. Full live-app capture continuation is not implied by these framework/contract tests.
- [ ] Complete large-page native ingestion and whole-document question coverage. Passive AX still has256KiB/2048-node/750ms per-frame bounds and individual authenticated sources have a1MiB text bound. Add safe source-linked continuation and honest truncation/coverage handling for longer pages. Verify end-of-page recall and whole-document summaries using all relevant chunks; bounded answer context must not masquerade as complete page coverage.
- Audit permanent sync deletion records and importer admission atomically. The current bounded phase-one tombstone log has an explicitly documented eviction limitation; that is not proof of indefinite protection from replay.
- Persist sighting metadata independently of subsequent activity intervals; audit retention against last seen and recap selection across the full requested day. The Goldfish inspection reinforces these existing spec requirements.
- Complete the async UI write audit. Today task creation/edit/complete and question answer/dismiss now bind the click epoch; other mutation entry points still require review.
- Continue native acceptance using the stable `~/Downloads/Blur.app` with its existing bundle identifier and signing certificate. The earlier isolated fixture checks below are historical evidence; they do not authorize creating or launching alternate user bundles. Capture and notification delivery still require native acceptance.

### Permanent deletion-fence constraints from the initial audit

The importer currently reads `Tombstones` before preparing writes, then calls
`SaveNote`/`PutObservationAt` later. A concurrent deletion can commit between
those operations. A client UI epoch alone cannot make these durable writes
atomic. `SaveNote` also checks source existence before its transaction and can
skip the source put while publishing a previously prepared Today projection.
Address both admission paths, including the importer's error fallback.

- Persist the latest authenticated deletion cutoff per source and the maximum
  all-sources cutoff in encrypted, non-evicting records. Keep inclusive
  per-source versus strict all-before capture-time semantics explicit.
- Check those records inside the same transaction as every source admission;
  older imported evidence must be skipped with a typed outcome, while an
  actual subsequent capture remains admissible. Source-linked projections
  must recheck live membership within their write transaction as well.
- Export permanent records in bounded pages using a durable local change
  sequence. The original deletion time must remain unchanged for convergence;
  time-only export watermarks can miss a newly relayed, older deletion or split
  equal timestamps. Resume cursors need to describe committed local changes.
- Make the permanent-fence capability mandatory for sync admission/export;
  silently falling back to a bounded snapshot leaves the privacy gap open.
- Upgrade the authenticated schema so an older writer cannot ignore these
  fences. Migrating all16384 legacy tombstones in one physical transaction can
  exceed its budget; use a resumable migration that rejects older clients
  before exposing the new writer, and completes before admitting reads/writes.
- Preserve every still-present legacy tombstone. Previously evicted deletion
  history cannot be reconstructed from this database and must not be claimed
  repaired without external evidence.
- Regression gates: deletion between import preflight and commit; deleted note
  Today/proposal resurrection; more than16384 distinct deletions; late relay
  with older timestamps; equal-time page boundaries; crash at each migration
  checkpoint; corruption rejection; reopen/downgrade refusal; and a legitimate
  post-deletion recapture. Native deletion and notification cancellation remain
  separate acceptance requirements.

### Task 7 continuation: permanent deletion cutoffs and atomic replay admission

**Architecture:** Schema 5 rejects writers that lack durable admission checks. Keep a sealed maximum cutoff per source and one strict all-before cutoff. Append each advancing cutoff to encrypted fixed-size change pages with a local monotonic sequence; no cutoff or change page is evicted. Preserve original deletion times on relay. The historical bounded log remains available for migration/audit and cannot supply sync admission after this upgrade.

**Migration:** Upgrade authenticated identity first, then migrate the still-present legacy log in resumable batches of 32 under a sealed progress marker. Source admission is never exposed until migration and existing erasure recovery finish. The checkpoint, changed cutoffs and sequence pages commit together. Previously evicted history remains unrecoverable and must be disclosed.

**Interfaces:** `ObservationDeletionCovers(ctx, sourceID, capturedAt) (bool, error)` provides a bounded read; `DeletionChanges(ctx, afterSequence, limit) (DeletionChangePage, error)` returns bounded sequence pages. `ImportObservation(ctx, observation, at) (bool, error)` reports admitted versus skipped input and guarantees source/note admission and source-linked projection checks in its transaction. Source-specific replay deletion must preserve primary records captured after the incoming cutoff.

**Files:** new durable `deletion_cutoffs.go`, migration and regression tests; narrow changes to durable store/notes/Today/sighting admission and schema fixtures; replica importer/writer/state and tests; deletion coordinator conditional source replay and tests; sync-state reset; architecture/build guidance.

- [x] Reproduce stale source/note replay and late deletion export with encrypted-store fixtures. Add direct cutoff-boundary and transaction-admission regressions.
- [x] Implement sealed non-evicting cutoffs, bounded sequence export, schema-5 startup migration and interrupted/reopen/corruption/downgrade tests. Prove preservation beyond 16,384 distinct deletions and bounded physical transactions.
- [x] Enforce primary/sighting/note admission and live source membership in the same write transaction. Exercise deletion between preparation and commit; reject orphaned Today/proposal writes and preserve later captures.
- [x] Require the fenced importer/exporter capabilities, replace time-only deletion watermarks with sequence cursors, remove the unsafe note fallback, and preserve newer primary captures when older tombstones arrive. Verify equal timestamps, late relays, skipped writes, cancellation, retry and state persistence.
- [x] Run full suites, affected race checks, build/vet/cross checks and independent review; update the same signed Downloads app only after the complete path passes.
- [ ] Complete native notification acceptance and diagnose the actual user Library startup failure from its safe runtime diagnostic. These are separate from the verified source/build slice.

### Large-page continuation constraints (still open)

The accepted source/index repairs do not yet implement a complete document stream. The next implementation must:

- Consume bounded native child pages and immutable text ranges while rechecking app, window, page path, domain, exclusions, private/secure state and cancellation before each text range. Release references on pause, focus changes and failures.
- Keep redaction state across native values and storage chunks. Splitting raw values and sanitizing each piece independently can expose a private key, authorization value, quoted secret or URL credential that crosses a boundary. Add those regressions before publishing streamed fragments.
- Persist ordered source-linked document membership and explicit complete/partial coverage, with stable deduplication and sightings. A sequence of unrelated observations is not enough to support whole-page inspection, synthesis or deletion.
- Apply bounded resumable indexing and erasure to every document member and metadata record. Every writer must honor live-source/deletion fences. Older clients must reject any new text-bearing projection they cannot erase.
- Retrieve relevant passages for fact questions and cover all relevant chunks for whole-document requests. Keep omitted/partial coverage explicit and preserve inspectable original text and source spans.

Historical native acceptance used a separate build identity, data/preferences/log/socket paths and key namespace, plus fixture-only capture scope. The user subsequently required one stable app at `~/Downloads/Blur.app`, `com.hazcod.blur`, preserving the exact signing certificate and designated requirement. Current builds must follow that policy. Temporary encrypted stores and synthetic CLI tests remain useful for source validation, but do not prove native UI capture or notification delivery. Goldfish inspection is complete; full Blur capture/notification acceptance remains open.

Native acceptance isolation source progress: the `darwin && blur_acceptance`
build uses a linker-bound private root and nonce marker, the separate
`com.hazcod.blur.acceptance` bundle identity, a nonce-specific native Keychain
service, and private Data/Logs directories. It starts paused with no global
shortcut or menu item. An independent required app/domain guard intersects
ordinary capture settings; the production Safari extension bridge is disabled.
Tagged tests found and fixed a real MCP socket collision: fixtures now bind only
inside their own compiled Data directory and reject unrelated directories.
Invalid fixture configuration fails before logging or opening memory, with no
production fallback. Tests cover identity/path/key namespaces, scope intersection,
private directories and symlink rejection. These source tests and building a
bundle do not constitute actual native capture or notification acceptance.

Native Ask regression (Mira): saving “The rollback owner is Mira.” and asking
“Who is Mira?”, “what or who is Mira?” or “Mira?” exposed two defects: a model
availability check ran before evidence retrieval, and bare identity questions
were routed directly to general generation. Source regressions now verify
model-free, source-cited quotations from encrypted notes with Library empty.
Ambiguous identity/keyword requests probe local evidence before resolving their
route; explicit general mode and writing requests preserve their data boundary.
Remote-consent tests require zero requests while local source inspection stays
available. Native cold restart retained the synthetic Aurora/port4317/Mira note.
AI-off Ask returned its quote/source; enabling the already installed local
qwen3:14b model produced a grounded answer identifying Mira as rollback owner.
This verifies that regression, while useful broader reasoning/follow-ups remain
part of the unfinished milestone.

Quick-ask latency regression: a clear unprefixed question made two serial model
requests, first intent classification then answer generation. An isolated real
qwen3:14b probe measured3.153s interpretation +2.944s answer =6.098s. Clear
questions whose deterministic plan resolves the read route now skip intent
inference; the same synthetic encrypted-note question measured0.123ms routing
+2.909s answer, with exactly one model request. Ambiguous personal/general
lookup and polite action requests retain strict typed interpretation. Factual
questions such as “Which port does Aurora use?” now probe saved evidence, and
unmatched app-scoped probes cannot pick unrelated recent content. Full suite,
affected race, build/vet/cross/audit checks pass. The isolated native build is
updated and its Keychain wait has cleared. Native Ask again produced the
correct source-cited Mira answer. Native settings verify Saved/disabled,
Save changes for an edited model draft, and Saved/disabled after reverting it,
without persisting the temporary draft.

The popup focus defect was traced to self-activation always raising the mounted
dashboard. Activation now preserves an open Quick ask; opening the dashboard
clears popup visibility synchronously before handing over the latest exchange.
Both transitions have observed failing-then-passing regressions. Interactive
local answers also request ten minutes of model residency, and simple factual
answers ask for a direct sentence while retaining detail for broader requests,
uncertainty and coverage gaps. No smaller model, lower output cap, answer cache
or background warm-up was introduced. Longer residency can occupy RAM longer
and remains subject to the provider's memory management.

Nine isolated qwen3:14b trials against a synthetic encrypted Mira note made
exactly one generation each and retained the original source. The first took
3.891s, including3.040s model load; the eight warm trials ranged0.546–0.672s
(median0.582s). These are measurements of two simple questions against one
note, not a general latency guarantee. Full suite, affected race, cgo-disabled
build, vet, cross-build/test compilation and no-cgo audit pass. Focused review
found no material source issue. The updated isolated native app starts with its
saved note intact and opens Quick ask. Computer use then disconnected with
“native pipe startup failed” before typing; actual native popup timing, typing,
dashboard handoff and click-away dismissal remain pending reconnection. The
earlier2.909s measurement is the isolated client/model flow, not native timing.

### Apple provider and capture-paused Ask follow-up

Apple Foundation Models is now a selectable local provider for questions,
meaning search, notes, intent, Chronicle and Library. The legacy embedding index
is dormant in standard startup. Its helper is
bundled and signed, weak-linked for the existing application deployment floor,
and checks runtime availability/context before generating. No account, API key,
server endpoint, remote fallback or background warm-up is required. Existing
provider preferences remain active until the new selection is saved.

A stale capture setup flag could block opening the dashboard after recording
was disabled. That coupling was removed; encrypted-note client/dashboard/popup
regressions keep Ask independent of recording. Possessive factual questions now
retain their named subject rather than requiring the unknown attribute to
appear in saved evidence. Citation and mutation validators remain authoritative.

Live synthetic encrypted-note Apple recall, unknown-detail refusal, general
questions and note intent pass with capture paused. Known-source answers took
1.27–2.01s in the final two-question client run, not a general latency guarantee.
Ten intent cases pass, including a forced test-only conservative4096-context
helper using the actual compiled-schema byte accounting. Live note suggestions,
two source-linked Library facts and a cited Chronicle also pass. A signed
sandbox host successfully calls its inherited helper. Full suite, affected race,
cgo-disabled build/vet/cross/no-cgo audit, tagged fixture gates, and a fresh
focused source review pass. Older macOS runtime/ABI, exact total shared model
RAM, general model quality and Safari runtime remain unverified.

The final isolated universal app is ready, but the native control host still
reports “native pipe startup failed.” The running app was not restarted or
switched to Apple; actual GUI selection and typing remain pending. No production
app, saved provider settings, store or keys were changed. ChatGPT/Copilot account
login was researched through official supported paths and remains future work;
Claude consumer login cannot be offered as a generic third-party provider.

## Current verification

2026-11-11: primary capture/time retrieval, app-scoped recap, scoped exact ranking, self-exclusion before top-k, backfill/reopen, deletion rollback, and cross-backend byte continuation tests pass. `go test -race ./internal/storage/... ./internal/client ./internal/assistant ./internal/search ./internal/capture ./internal/appidentity`, `CGO_ENABLED=0 go build ./...`, `go vet ./...`, and `git diff --check` pass. Full scope remains active: semantic retrieval, automatic Library, scheduled reminders, explicit profile/rules, capture coverage/occurrences, installed UI audit, and native acceptance are incomplete.


### Follow-up: stable app, meaning search, popup controls, cloud connections

The current source/build pass delivers the stable signed `~/Downloads/Blur.app`, metadata-only local model detection, meaning-based recall enabled by default for absent settings (existing disabled choices retained), live task recall/Today visibility, popup selection/copy/Escape and activation fixes, official ChatGPT account sign-in and Claude API connection. Cloud connection, model selection, saved-evidence permission, and note-processing permission are separate; policy changes cancel old Ask work and account/key changes clear permission.

Full tests, affected-package race checks, cgo-free build/vet/cross/audit, real local encrypted source/task meaning recall, and final universal signing/identity checks passed. Native app controls remain unavailable; the shipped UI and real account login were not exercised. Temporary acceptance data remains isolated and was not imported. The original full macOS acceptance milestone stays open.


### Large-document reader prerequisite (2026-11-13)

The [native reader subplan](superpowers/plans/2026-11-13-native-document-reader.md) is complete in source: paged traversal, immutable range reads, bounded rounds and ownership, path/policy checks, Unicode continuation and two reviewed timeout regressions. Full Go tests, capture race tests, vet, cgo-disabled builds and cross-target compilation passed. The reader is not connected to production capture and did not trigger a replacement app build.

The [full large-document design](superpowers/specs/2026-11-13-large-document-capture-design.md) remains open: streaming redaction across boundaries, encrypted ordered editions, ingestion, erasure/indexing, whole-document retrieval/inspection and native acceptance are still required. The reported Library startup failure remains unverified pending its safe runtime diagnostic; reader tests do not validate recovery of the user's store.

### Streaming redaction prerequisite (2026-11-13)

The [streaming redaction subplan](superpowers/plans/2026-11-13-streaming-redaction.md) is complete in source. Bounded incremental recognizers preserve rule order across ranges, keep ordinary document tails, mask unfinished/oversized sensitive fields, and roll back rejected sink output and counts. StreamVersion 2 corrects URL/label ownership, escaped-newline quoted values and Unicode boundary capture. Legacy batch output remains compatible; malformed-URL receipts now count only the final mask deterministically.

One independent final review found three Important issues; each was reproduced and fixed in one pass, including scoped recovery regressions. Fresh full tests, privacy/capture race tests, vet, cgo-disabled builds and cross-platform compilation passed afterward. This remains unconnected to production capture, so there is no replacement app build for this prerequisite.

Next required work is capture-owned sanitized document types with source-position mapping, authenticated ordered encrypted editions and restart cleanup, schema-aware admission/erasure/indexing, ingestion/activity wiring, whole-document recall/inspection and native acceptance. The inherited version-1 Unicode capture edge requires a migration-safe privacy audit. The actual Library startup failure remains unresolved; no user store, key or settings were accessed or changed during this subplan.

### Original source positions through redaction (2026-11-13)

The [source-position subplan](superpowers/plans/2026-11-13-redaction-source-positions.md) is complete in source. Optional mapped streams preserve exact original bytes for unchanged passages and contributing-input envelopes for transformed passages. Forced masks explicitly retain only a known prefix; ordinary text after oversized fields keeps exact source offsets. Pending maps, offsets, text and counts share rollback/commit semantics, and draining does not accumulate metadata.

One fresh source review found no Critical/Important defects and one Minor lookup optimization. Fresh full tests, privacy/capture race checks, vet, cgo-disabled builds and cross-target compilation passed after that optimization and added Unicode/newline-composition regressions. Native locators, capture-owned document values, ordered encrypted persistence/recovery/schema/erasure/indexing, ingestion/activity, whole-document retrieval/inspection and native acceptance remain required. The source mapping is not wired into production capture and no app was replaced. The actual Library startup failure remains unresolved.
