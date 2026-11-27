# Observable Goldfish feature parity

This checklist is based on Goldfish 0.1.70's installed app manual and static
binary strings, with Blur's implementation status maintained through 2026-11-22 on `main`. It tracks
observable feature areas rather than asserting internal source-code parity.
The stripped Goldfish binaries do not expose enough information to reconstruct
every ranking rule, model prompt, or runtime edge case.

Live basic acceptance on 2026-11-22 used disposable local content in the
canonical installed app. TextEdit capture was found in Search and opened with
its complete saved text and filename. A system-audio recording produced seven
saved transcript passages, a recap, a matching indexed Voice note, and a
voice-specific Ask answer whose citation opened that recording. Pause froze
the clock and excluded the spoken paused marker; the resumed marker was saved.
The user performed Stop. Task creation, completion, reopening and macOS reminder
scheduling were observed. Microphone capture, notification delivery, browser
capture and comparative recording quality remain unverified.

That live run also exposed an ordinary note-save failure and a slow Find route.
The note failure was reproduced with encrypted fixtures: durable saves used
`Local` while Today used the canonical machine zone. Both now share the resolver,
including compatible legacy snapshots. Explicit source-finding commands now
use indexed Search while factual/contextual requests retain Ask. The installed
app's Find request took 14,021 ms in its diagnostic stages, including 8,065 ms
ranking task titles; the voice-specific answer took 1,797 ms. Rebuilt-app checks
of these fixes are pending. Thirteen near-duplicate Terminal captures ranked
above the matching voice hit; ranking/dedup quality remains open. These results
establish specific flows, not Goldfish parity or universal app coverage.

Accepted text now has queue-driven, resumable source compaction with independent
encrypted span summaries and retained originals. Successful work checkpoints
across restart; Library extraction capacity does not strand compaction. A
deterministic-provider encrypted test covers 1,048,651 bytes across 33 capture
parts and 289 blocks; this measures persistence and traversal, not model quality.

Text recall tests now cover ten separately logged agents, distant passages in
one log, mixed source distributions, exact identifiers among newer lookalikes,
date-scoped Library claims and comparison follow-ups. Actual clipped context
controls subject coverage and citations. Search and relationship questions keep
their conjunctions. The [functional gap review](blur-functional-gap-review.md)
records remaining defects and scale constraints, and the
[dynamic test record](dynamic-memory-scenarios.md) separates executions from
unique user needs. No current million-case or full-parity claim is made.

Short commands and status lines now qualify for capture with any non-whitespace
Unicode code point after sanitization; the former 32-character gate is removed.
Encrypted Terminal persistence/reopen/recall and signed multi-batch browser tests
cover the change. Cross-part exact indexing also preserves words and technical
identifiers across physical boundaries, citing original ranges through native
Ask/Search and MCP. Sync recovery now uses exact-revision receipts and immutable
retry segments, but multipart capture transport remains an explicit open gap.
See the functional review for current verification and activation status.

| Area | Goldfish evidence | Blur status |
| --- | --- | --- |
| Background desktop capture | App manual and daemon capture strings | Implemented for macOS. Any non-empty sanitized text qualifies on native, browser and local write paths, including short commands and single-code-point text; blank text is excluded for every app. Privacy, sensitive-field and authorization gates remain authoritative. Repeated sightings of identical content update a per-source sighting count and last-seen time (legacy sources derive both from their sighting chain once), exposed on `get_snapshots`, `get_working_memory` and `read_session`. Each capture also records a focused-element breadcrumb (role, name, up to three ancestor names; never the element's value, nothing for secure fields, redacted and bounded, dropped rather than failing a capture) exposed on the same three reads; the live Accessibility path is unverified until native acceptance. Remaining: learned boilerplate stripping / normalized body text, native acceptance and cross-app coverage validation. |
| Search and assistant recall | MCP search, recent activity, working memory, chronicles, Wiki and full snapshots | A recall scenario harness (`internal/client/scenario_recall_test.go`, 15 natural-language scenarios × no-model and fake-generator modes over a realistic multi-app, multi-day corpus) drives the real Ask pipeline and the MCP reads. Native Ask now routes recency ("what was I just working on", one widening to 6 h), day parts ("yesterday afternoon"), conversation recaps with a person, speaker questions ("what did Dima say about…"), pronoun follow-ups anchored to previously cited sources, and stored-location questions ("where is … stored again?"), all model-free; whole-page requests answer extractively without a model. `get_working_memory`, `get_recent_activity`, `search_memory`, `get_snapshots`, `read_session`, `get_chronicle` (hour/day/week/month/year) are exposed over MCP. Captures now carry the window title (redacted, ≤ 256 bytes, indexed with weight 2 and shown as `window_title` on reads), and tokens fold diacritics and trailing possessives so "résumé"/"resume" and "Dima's"/"Dima" match; existing stores re-index in the background and report `indexing_pending` until done. Lookups now add majority-term matches ranked entity-first (messaging-app boost for speech-verb questions, matched terms, term rarity, recency), demote sign-in chrome before truncation unless the question is about a login page, and pack multi-day recaps chronologically with one item per day first; partial matches are flagged and the no-model answer says "closest matches". All 30 recall harness runs pass. The native Search page and MCP `search_memory` now share one ranked core: all-terms matches first, then majority-term "closest matches" flagged as relaxed, ranked by matched terms, term rarity, term closeness and recency; day/week/month/year rollups and hour recaps that match the query appear as summary rows (`chronicle:day:…`, at most 5, with source samples); rows carry 240-byte match-centred excerpts, window titles, sighting counts and matched terms; `search_memory` returns up to 30 rows (default 20) inside 16 KiB by shortening excerpts before dropping rows, and its description tells hosts to open `get_snapshots` before quoting; the optional meaning step runs only when the exact core is empty or the query is natural language and can only append. The search harness (9 queries across both surfaces) passes. Accent folding covers index, queries, planner, passage selection and meeting recall. Inferred Library names now have a keyed, encrypted folded-name posting index: ASCII questions find accented and decomposed stored names, contexts remain separate, old entries backfill through the model-free indexing worker, and Ask reports pending coverage. App/domain scope is applied before the eligible-result cap so unrelated same-name contexts cannot hide a requested correction; omitted contexts are reported. Recognized Find/Search/Look up/Pull up commands and stored-record location questions keep lookup intent after source deletion and abstain without a model; explicit general mode stays separate. Clear event-time questions now use content date cues instead of capture-date windows; observation clauses retain independent capture scope and provenance. Date continuations preserve numeric/month topic names, including through app refinements and answer shortening. Numbered topic pairs now rank local phrase support before broad token/app/recency boosts across Ask, Search and MCP; unverified relationships are closest matches, and incomplete candidate support is reported. Bare/entity lookup and mixed quoted qualifiers retain the cues. General event extraction and semantic ID resolution remain open. Dynamic stateful journey testing now mutates real encrypted memory and action histories; see [coverage boundaries](dynamic-memory-scenarios.md). Remaining: no vector index over Library pages (raw captures stay lexical, as in Goldfish, but Goldfish adds Wiki vectors); lowercase-name entity detection. Old published capture tokens now migrate through a bounded, restart-safe cursor and per-part proof removal/rebuild. Original payload ciphertext, publication/source identities and visit provenance remain intact; cited parts stay readable and deletable between every work quantum, and pending coverage is reported. |
| Original evidence | Full snapshot retrieval after excerpt search | `get_snapshots` reads verified local sources in UTF-8-safe pages, with multipart provenance |
| Time and activity | Timeline, time reports, chronicle at hour/day/week/month/year levels | Stored, source-linked day and ISO-week rollups are generated from hour recaps, months from days and years from months, by the local background provider only (dirty-tracked, regenerated on input change, cleared and fenced when a cited or input source is erased, never served without source IDs); native recap questions prefer them for whole days, weeks, months and years; `get_chronicle` accepts `level` and `period`; `get_recent_activity`, `get_working_memory`, `read_session` and `get_time_report` (tracked foreground time by app/domain, not rules) are exposed. Month/year recap questions prefer stored calendar parents and retain a disclosed bounded fallback (eight distributed windows, at most eight raw originals, scopes up to 400 days). All four levels use an encrypted historical queue with restart-safe bounded backfill and zone rebasing; compatible version-2 discovery revisits completed day/week history. Higher levels wait for discovery/clean children and receive reserved generation opportunities under hot day changes, within six total model calls. All overlapping calendar ancestors invalidate atomically, including fractional-offset New Year boundaries. Input receipts now bind source/child identity, coverage limits survive Ask/Search/MCP, erased rollup generations are epoch-fenced, and fractional-midnight hours are excluded. Exact tracked time supports up to 400 local dates through bounded metadata reads across native Ask, Time and MCP; unknown engine gaps and Blur are excluded, durations are accumulated before rounding, and queued results retain committed-read guards. New source reverse links use bounded source/year leaves with an adaptive fixed-ceiling directory; old arrays stay readable. Input erasure clears public recap fields immediately while encrypted reverse obligations drain in bounded batches. Persisted recap-erasure intent and a closing epoch fence publication across interruptions; model-free storage maintenance finishes pending erasure even without an AI worker. MCP and native Search preserve per-summary coverage limits, and native Ask labels calendar periods and supporting-source counts. Native recap cards now page current saved originals, with bounded previews, one full expansion, provenance-bound continuation and queued-publication fences against durable writes and deletion. Month/year native Search and MCP now use bounded complete scope queries, including optional meaning retrieval, with durable committed-write fences and preserved visit dates. Remaining: rule-based project time, a time-rules UI, live native acceptance. |
| Entity memory | Wiki, aliases, facts, relations, memory map | `search_wiki`, `get_wiki_page` and `list_wiki` expose bounded Library entries with aliases, source-linked facts and open loops; automatic enrichment proposes entities in the background when a local model is configured. Remaining: graph relations/memory map, lowercase-name entity detection in the planner, dated-fact supersession is modelled but not written by any job. |
| Commitments and follow-through | Promises, follow-ups, blockers, To Do, closure evidence | `list_tasks` and `get_promises` expose explicit tasks, reminders and Library open loops with provenance and a note that closure needs source evidence; tasks and reminders can be created from natural language without a model ("add a task: …", "remind me tomorrow at 9 …"). Remaining: inferred promises from captured prose with confidence and due dates, blockers, closure-evidence lifecycle. |
| Conversational workspace | Aquarium threads, inbox, queues, notifications, follow-up prompts | Blur has the Ask/quick panel with a session conversation and pronoun follow-ups; Ask questions (text, time, plan kind, cited sources) persist in the encrypted store (500 newest, deletion-fenced) and reload at startup. Aquarium-style threads, inbox, queues and notification follow-ups remain gaps. |
| Writing assistance | Inline rewrite, edit mode, learned writing rules | Blur's current parity is incomplete; inspect implementation before claiming support |
| Voice | Dictation, spoken briefs, voice interactions | Blur has on-device voice notes/transcription and recap; spoken briefs and broader dictation workflows remain gaps |
| Privacy controls | Pause, never-capture categories, ignore list, masking | Blur has capture/privacy controls; the policy model and settings experience differ and require a separate audit |
| Proactive work | Reminders, scheduled prompts, morning/evening briefs, contextual nudges | Blur has reminders, hourly recaps and background work; prompt rules, nudges and brief parity are incomplete |
| Time tracking | Rules, assignment, reports | `get_time_report` returns tracked foreground time per app and browser domain with per-day splits and top segments, framed as screen time, not a timesheet; classification rules exist in `internal/activity` but have no editing UI and the report does not apply them. |
| Import and integrations | ChatGPT/Claude memory import, MCP integrations | Blur supports MCP and selected imports; source coverage and workflow parity need a separate audit |

## Immediate implementation sequence

1. Preserve search excerpts as navigation hints and fetch cited full evidence for
   exact answers. (`get_snapshots` implemented.)
2. Add persisted higher-level time summaries so day/week questions can use a
   compact stored recap directly. Persisted day/week/month/year summaries and
   bounded historical generation and exact long-range app/website time totals
   are implemented; unverified topic/project totals remain explicit gaps. `get_working_memory` exposes recent captures.
3. Expose Library entity and explicit task reads with source IDs and current
   state, then teach Ask/MCP routing to combine them with raw captures. (`search_wiki`, `get_wiki_page`, and `list_tasks` implemented.)
4. Audit the remaining user-facing areas above against Blur's app and tests;
   close the important workflow gaps before claiming parity.

Goldfish's static operating instructions specifically say to fetch before
answering memory questions, choose the time/entity/task tool for the question,
batch independent reads, and open original sources for exact claims. Blur's
source reads honor the authenticity and pagination part of that design now.
The checklist intentionally remains open; this is not a 100% parity claim.

Range-only large-document capture additionally verifies a second streamed
content digest before issuing a complete receipt, rejecting same-length edits
that retain the original character count. Native mutation and job tests cover
bounded progress, Unicode, timeouts, cancellation, unavailable reads, secure
transitions and visible-only partial ranges. These tests use simulated external
AX providers and do not close the live cross-app acceptance gap.

Recognized Dutch personal recall/search and generic task-collection questions
now use local planning and Interact's read route, including unpunctuated reads.
Event/observation dates, app/date continuations, numbered subjects, explicit save
authority and source deletion have native encrypted-store regressions and
runtime journey coverage. This is bounded request grammar; general Dutch and
multilingual semantics, Dutch creation and generated-answer acceptance remain
open.
