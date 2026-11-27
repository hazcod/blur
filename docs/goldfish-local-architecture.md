# Goldfish local architecture inspection

Inspected the installed Goldfish 0.1.69 bundle and local database read-only on 2026-11-12, after the settings UI audit. This is a structural inspection, not a complete reconstruction of its control flow or proof of runtime correctness. No credentials were read, no database was copied, no captured text was dumped, and no Goldfish configuration was changed.

## Directly observed

- The bundle contains a Tauri desktop executable, a separate `goldfish_d` daemon, and a `goldfish-mcp` executable. Compiled Rust module paths identify native capture, coalescing, structured browser capture, chronicle catch-up/rollup, lexical/vector search, graph search, and Wiki search components.
- The local store is `~/Library/Application Support/goldfish/goldfish.db`, about 2.37 GB at inspection, with SQLite schema version 62. Standard SQLite opened it without an application key. Schema and natural-language text patterns in snapshot content and Wiki bodies were accessible. This establishes readable application data in this file; it does not assess FileVault, filesystem permissions, or every other artifact.
- A separate `goldfish.hnsw.v3` index is about 108 MB. The binary includes HNSW loading, rebuild, save/recovery, and reverse-map messages.
- FTS5 indexes cover raw snapshots, chronicle summaries, topical index entries, Wiki pages, and Wiki facts. SQLite triggers maintain the snapshot, chronicle, Wiki-page, and fact indexes on insert/update/delete.
- The daemon contains `model2vec-rs`, tokenizer, safetensors, and Hugging Face download components. The embedded model identifier is `minishlab/potion-base-32M`. All 1,198 current Wiki embeddings use `potion-base-32M/title-aliases-summary-body600/v1`, with 2,048-byte vector blobs and a source revision. This identifies the stored Wiki representation; it does not prove the format of every vector in the HNSW sidecar.
- The local configuration enables vector search and background AI, with a 6-second capture interval, 5-second flush interval, 300-second coalescing window, and working-memory capacity 2,000. These are this installation's settings, not verified universal defaults. Privacy UI displayed capture paused during the audit.

## Memory layers in the schema

| Layer | Relevant structure | What this establishes |
| --- | --- | --- |
| Original observations | `snapshots`: content/body, app/window/URL/domain, capture/last-seen times, duration, sighting count, focused element metadata | Content deduplication preserves repeated sightings and temporal context |
| Activity episodes | `activity_events`: snapshot link, start/last-seen/duration, app/site and excerpt | Work episodes are represented separately from content records |
| Short-term context | `working_memory`: snapshot ID, added time, relevance | A bounded recent-context layer exists independently of long-term summaries |
| Temporal summaries | `chronicle`: level, period, text, source/child IDs, model, dirty state | Hour/day/week/month rollups retain source and child relationships |
| Topic retrieval | `index_entries` plus FTS5 | Search has a topical/entity directory beyond raw keyword matches |
| Wiki | `wiki_pages`: typed page, aliases, summary/body/links/source IDs, mentions, first/last seen, revision and regeneration state | Library is an automatically maintained projection with regeneration bookkeeping |
| Time-sensitive facts | `wiki_facts`: as-of time, source, confirmation/invalidation times, importance | Facts can become outdated without overwriting the original evidence |
| Relations | `kg_entities`, `kg_relations`: subject/predicate/object, validity interval and source snapshot | Relationships carry provenance and temporal validity |
| Explicit corrections | `personalization_wiki` and history: revision, pinned state, corrections and draft | User edits are stored separately from generated Wiki content |
| Writing rules | `personalization_style`: original source/surface, structured note, enabled state and revision | Style preferences have provenance and lifecycle controls |
| Follow-through | `open_loops`: evidence, due/status/confidence, closure evidence, dedupe/canonical IDs, revision and explanations; linked `todos` | Suggested obligations, confirmed tasks and closure evidence are distinct concepts |
| Calendar/attention | `attention_items` and processing queue: dated event, recurrence, wall-clock/zone source, evidence and status | Meeting context has a separate dated representation |
| Conversation learning | `chat_turns` and distillation/folding state | Chat context can feed memory through a tracked background process |
| Scheduled prompts | `proactive_rules`: prompt, schedule, timezone, enabled state and last firing | Recurring AI check-ins differ from a task's notification trigger |

At inspection the store held 92,212 snapshots, 756 chronicles (667 hour, 74 day, 12 week, 3 month), 1,198 Wiki pages and embeddings, and 106,385 relation rows. Counts demonstrate populated structures, not retrieval quality or factual correctness. Working memory was empty; the inspection did not establish why.

## Inferences and limits

The combined schema, native UI and binary markers support a layered design: capture original evidence, preserve activity/sightings, maintain recent context, produce hierarchical summaries and entity pages, and retrieve using lexical/vector/graph components. Binary paths and labels alone do not establish exact ranking weights, graph traversal rules, token budgets, or query planning. No daemon endpoints were invoked and no execution tracing, authentication bypass, binary modification, or network interception was performed.

Goldfish's own release history and UI are feature claims, not proof that all those paths work. Its current paused capture state explains the lack of current capture in that installation; it does not explain Blur's capture gaps.

## Consequences for Blur

The 2026-11-22 basic live run proved one native TextEdit capture through saved
original retrieval, and one system-audio recording through actual on-device
transcription, pause exclusion/resume, indexing, recap and grounded voice recall.
It also demonstrated why acquisition and retrieval need separate checks: an
ordinary note failed before persistence due to differing local-zone names,
while a Find command spent 14 seconds in answer/ranking stages despite the
indexed voice hit being available. These are Blur observations, with fixes
under verification, not comparative Goldfish measurements. Microphone input,
notification delivery, browser capture and Terminal dedup/ranking remain open.

Voice delivery retains the durable read token through question history and
queued original/recap installation, including terminal model errors. A scoped
voice token is selected only after live meeting membership and record-kind
authentication. Client publication gates permit overlapping readers while
mutations and Close retain exclusive access; native delivery rejects a busy
writer without waiting. The full-suite run exposed false mutation errors when
two unchanged readers shared an exclusive mutex, and a controlled concurrent
read regression plus twenty recap repetitions exercise that correction.
Ask cancellation bookkeeping has its own mutex. Registration retains the
shared memory guard until insertion, so deletion cannot miss a newly admitted
ask; registration and cleanup no longer act as evidence writers.

1. Keep encrypted source observations authoritative. Encryption is compatible with in-process decryption, bounded retrieval and RAM-only search indexes; changing to plaintext SQLite is not required.
2. Persist repeated sightings and last-seen times while capture continues, independently of activity-segment closure or summary generation.
3. Retrieve current raw evidence and the requested full time span; recent context and hierarchical summaries can improve recall without becoming the only searchable memory.
4. Keep inferred Library content, explicit corrections, tasks, and scheduled delivery as separate records with source links and revisions.
5. Make query routing, capture/index coverage, incomplete results, and source inspection observable. A stronger answer model cannot recover content that was never captured or admitted to retrieval.
6. Preserve complete deletion and sync/backfill fences across every projection. Blur still has open scale and native acceptance gates; this inspection does not certify its architecture as finished.

## Capture read-path reconstruction (2026-11-13)

Bounded static inspection of the installed universal Rust daemon
`Goldfish.app/Contents/MacOS/goldfish_d`, version **0.1.69**, SHA-256
`4e14823dc9939aad20d194a46ed3691599ef84e1035512c8f28542f8df84ed35`.
Addresses below are unslid arm64 virtual addresses for this binary only. No
process was launched, attached, hooked or modified; no user data, credentials,
TCC settings, or network endpoints were accessed during this reconstruction.
A temporary thin binary and a small local analysis harness were used.

Directly verified call paths and behavior:

- The main capture path at `0x1007c5840` reaches WebArea discovery
  `0x1007ce980`, wrapper `0x1007d62f4`, and structured walker `0x1007d6740`.
  It also reaches a generic AX walker at `0x1007cf5c0`.
- Capture helper `0x1007d64d8` requests `AXTextMarkerRangeForUIElement`, passing
  the element itself, then `AXStringForTextMarkerRange`. If the string request
  is unavailable it tries `AXAttributedStringForTextMarkerRange`
  (`0x1007d6610`). Shared result conversion `0x1007e4fc0` accepts CFString or
  CFAttributedString and uses `CFAttributedStringGetString` for the latter.
- Another capture helper, `0x1007d4ce8`, reads `AXVisibleCharacterRange` and
  requests `AXStringForRange`; its native-walker call is at `0x1007cf1f4`.
- The generic walker batches `AXValue`, `AXDescription`, `AXTitle`,
  `AXSelectedText`, `AXChildren`, and `AXRole`. The structured walker batches
  role, subrole, value, title, description, children, position, and size.
- Both walkers call secure predicate `0x1007d0e90` before accepting AXValue.
  Predicate `0x1004da018` recognizes `PasswordField`, `SecureTextField`, and
  `SecureTextArea`, including the subrole for TextField. Structured handling
  explicitly includes TextArea, TextField, SearchField and ComboBox values
  (`0x1007d772c–0x1007d77f8`): TextArea is not blanket-excluded as an editor.
  The capture-side `AXUIElementIsAttributeSettable` call at `0x1007cd7d8`
  tests `AXManualAccessibility`, not AXValue; the observed AXValue settable
  check at `0x10089ba24` belongs to another inline-interaction path.
- Native value cropping at `0x1007d2728` applies to TextArea/TextField/StaticText
  after 4,096 UTF-8 bytes and at least 32,001 UTF-16 units. It selects at most
  32,000 UTF-16 units around the clamped visible range, adjusts to include the
  visible end, and trims incomplete edge lines. Missing range information
  yields a prefix. This in-memory crop is distinct from AXStringForRange.
- WebArea discovery caps depth at 80; generic and structured walks cap at 200.
  The shared collected-text budget is 200,000 bytes (length accumulation at
  `0x1007cfee0–0x1007cff0c`). Marker output also caps at 200,000 UTF-8 bytes,
  backing up to a code-point boundary. Conditional elapsed budgets are 1 or 3
  seconds; AX messaging timeouts are 0.5 or 3 seconds
  (`0x1007c6008–0x1007c601c`). The exact classification choosing each budget
  was not fully reconstructed.
- Accessibility wake helper `0x1007ce140` deduplicates its app/window inputs,
  sets both `AXEnhancedUserInterface` and `AXManualAccessibility` false, waits
  160 ms, then sets both true. The referenced CFBoolean imports were verified.

Limits: bulk marker text is obtained before the structured walk; its helper has
no verified input/draft filter. Static evidence therefore does not establish
whether AX omits input contents or subsequent processing removes drafts. The
`AXStartTextMarker`/`AXEndTextMarker` and unordered-marker errors observed in
other functions were not established as reachable from this capture chain.
Exact observer debounce/cancellation behavior remains unverified. The earlier
six-second cadence is an inspected installation setting, not a universal
default. No linked Vision/ScreenCaptureKit framework or matching OCR imports
were found; this absence does not prove OCR can never be loaded dynamically.

## Meeting audio evidence (2026-11-13)

Read-only inspection of the installed public main executable's undefined
symbols (`nm -u`) confirms Core Audio I/O imports: AudioComponent creation,
AudioObject property/listener APIs, AudioOutputUnitStart/Stop and
AudioUnitInitialize/Render. This supports audio capability in the main app.
It does **not** identify a meeting recording control flow, transcription model,
speaker recognition method, remote processing policy or audio storage format.
No private Goldfish data was accessed during this follow-up.

For an inspectable implementation, Blur also studied
[Yogurt's native audio source at a pinned revision](https://github.com/FrigadeHQ/yogurt/blob/41c936ae83697b2894f52746dc9c24cb16bab474/app/native/yogurt-audio/main.swift)
and its
[bounded speech chunker](https://github.com/FrigadeHQ/yogurt/blob/41c936ae83697b2894f52746dc9c24cb16bab474/app/src/main/asr/chunker.ts).
See `superpowers/specs/2026-11-13-meeting-memory.md` for Blur's design choices
and the separate, publicly documented Notion meeting workflow.

## Content matching and parsing follow-up (2026-11-15)

Read-only follow-up against the installed Goldfish **0.1.70** daemon. No
personal capture rows, credentials, or Goldfish query endpoints were read.
Evidence comes from executable strings and the already inspected schema; this
does not establish runtime ranking weights or prove each path is active.

- Raw snapshot FTS5 matches `content`, `app`, `window_title`, and `domain`, and
  orders results by FTS rank (`ORDER BY rank`). The full-text query is therefore
  a lexical first-pass over captured UI text and metadata, not semantic search.
- Separate FTS5 indexes match topic/entity index entries, Wiki page
  title/aliases/summary/body, chronicle summaries, and Wiki facts. The binary
  also contains a `potion-base-32M` model2vec embedder and HNSW vector index.
  This supports a multi-stage path: raw term search plus compact topic/entity
  routing, then Wiki/graph/chronicle and vector recall. It does not mean every
  raw snapshot has an embedding.
- Snapshots retain both `content` and a versioned `body` (`body_v`). Query
  strings show some retrieval paths choosing `COALESCE(body, content)` while
  raw snapshot FTS indexes `content`; thus capture evidence and normalized
  retrieval text are distinct representations.
- A per-surface `boilerplate_meta` / `boilerplate_frags` detector stores a fit
  sequence, detector version, context/frame counts, and fragment kind/key/DF.
  Related strings include a `body dictionary fitted` maintenance event. This is
  direct evidence for learned repeated-fragment detection during body
  normalization, though the detector's exact scoring/removal policy is not
  recoverable from these artifacts alone.
- Query normalization includes a compiled Hugging Face tokenizer and
  `precompiled_charBertPreTokenizerCharDelimiterSplitter`. This is richer
  Unicode/token-boundary handling than Blur's current exact index, which
  lowercases and splits on non-letter/digit runes. The tokenizer's use in each
  search route and any stemming/synonym behavior remain unverified.
- Compiled prompt strings expose a source-assessment format that distinguishes
  `observed_action` (the user demonstrably did something) from
  `on_screen_content` (something was merely visible), asks for cited evidence,
  and grades each item `core`, `detail`, or `noise`. The prompt explicitly
  treats generic feature lists and transaction/site chrome as noise. This is
  direct evidence of a designed content relevance/attribution stage, not proof
  that every capture passes through it or that its classifications are correct.
- A separate open-loop consolidator consumes the currently tracked loops,
  typed flags from screen activity, and day summaries. Its prompt forbids
  inferring completion from editing, silence, age, or a summary alone; closing
  a loop requires a `resolution_evidence` item with the loop key, source
  snapshot ID, and an exact supporting quote. This makes task state a
  source-backed lifecycle rather than a text match. The prompt also says
  source material is untrusted input. As above, embedded prompt text proves
  intended constraints, not runtime adherence.

Taken together, Goldfish appears to parse at more than one level: a normalized
body to reduce repeated surface chrome, cited assessments to distinguish user
actions from viewed content and rank useful detail, then a separate stateful
open-loop pass to track promises and validate closure. These are separate
representations and decisions; a single cleaned text string would not preserve
the provenance or confidence needed for reliable answers.

### What Blur should borrow

Keep the encrypted capture as authoritative evidence, then build reversible
derived layers: surface-aware cleanup, evidence assessments with provenance,
and explicit temporal/task state. Rank candidate passages by their ability to
answer the user's intent, not shared tokens alone. In particular,
login/consent/navigation chrome should lose to a matching substantive page,
while remaining inspectable as a capture. Learned boilerplate must be scoped
to an app/domain/surface, versioned, reversible, and prevented from erasing
short user-authored content. Track whether text describes an observed user
action, merely viewed material, or an inference; require source-linked evidence
before marking an obligation complete. Search should combine lexical matches
with entity aliases, temporal/activity scope, and (where available) semantic
similarity; answers and inferred state must cite original source captures.

Blur currently has a concrete quality fix: whole-document retrieval excludes
credential/login captures before they can crowd out substantive matching
content. This is narrower than Goldfish's learned boilerplate processing and
does not yet implement a general content parser or ranked relevance model.

## Memory-agent routing and feature inventory (2026-11-15)

The installed **0.1.70** app contains its full feature manual and its MCP
assistant's operating instructions as static strings. The main application and
MCP executables are stripped of useful debug symbols, so these strings expose
the documented user-facing behavior and the intended tool contract, not the
private ranking implementation. No Goldfish data store was read during this
binary-only follow-up.

Directly observed feature areas include:

- An edge/notch chat surface with searchable threads, rename/delete, inbox,
  queued prompts, notifications, and follow-up questions.
- Inline rewrite, edit mode, voice dictation, and spoken briefs.
- Automatic capture with pause/ignore/privacy controls; timeline and
  time-tracking rules.
- Memory organized by recent activity, short-term working memory, chronicle
  summaries at multiple time scales, entity Wiki, writing rules, and profile.
- Follow-ups/commitments, To Do, reminders and scheduled prompts, contextual
  nudges, meeting/calendar context, imports, and MCP access.

The MCP executable embeds distinct tools for raw search, full snapshot reads,
recent activity, working memory, chronicles, Wiki/entity pages, promises and
follow-ups, time reports/rules, attention context, nudges, identity/profile,
capture controls, privacy blacklist, and diagnostics. Tool names/descriptions
are implementation evidence, not a claim that every tool can be used safely
or that every backend path is healthy. The app manual also describes chat
memory learning, memory digests, morning/evening briefs, meeting briefs,
work-pattern reports, and importing ChatGPT/Claude history.

The embedded memory-agent instructions prescribe a retrieval sequence rather
than one broad search: recent activity for vague/current questions; working
memory for the last few hours; chronicle plus multiple distinct keyword
searches for day/week questions; Wiki for entities; then full source snapshots
for exact claims, messages, decisions, or values. Search excerpts and summaries
are treated as indexes, not primary evidence. Independent tool reads should be
batched in one round where possible to reduce latency. This is direct evidence
of Goldfish's intended orchestration contract. Exact prompt execution order,
ranking weights and runtime model behavior remain unverified.

Blur now exposes bounded `search_memory`, `get_snapshots`, `get_chronicle`,
`get_recent_activity`, `search_wiki`, `get_wiki_page`, and `list_tasks` through
its authenticated local MCP owner. It validates source hashes and provenance,
reads original sanitized evidence, hourly summaries, activity metadata,
source-backed Library facts/open loops, and explicit task/reminder state from
the already-open encrypted store, and applies the memory-mutation fence before
returning data. Full captures and Library details are paginated; multipart
capture provenance and capture time are retained when available. MCP tool
descriptions explicitly disclose that the host may forward returned content
to its configured model provider. This remains a partial parity slice: working
memory, multi-scale chronicles, inferred promises/follow-through, time reports,
attention context, privacy controls, and Goldfish's broader workflows are not
yet exposed through MCP.

## Behavioral findings (2026-11-16)

Read-only inspection of the installed Goldfish **0.1.70**: `strings` over the
shipped binaries, the published MCP tool descriptions and the local SQLite
schema. No process was run or attached, no private rows were copied, and no
Goldfish wording is reused in Blur. VERBATIM marks facts taken directly from
strings, SQL or tool descriptions; INFERRED marks deductions.

### Retrieval

- Raw search is FTS5 over snapshots ordered by `rank` (BM25); the displayed text
  prefers the normalized body over the raw capture, and a windowed variant
  filters on capture time. Chronicle summaries, Wiki pages, Wiki facts and the
  topic index each have their own FTS table queried the same way. (VERBATIM)
- Result-source labels for OR-joined BM25, embedding search and graph
  PageRank sit side by side (INFERRED: three candidate sources feed one list).
  No fusion weights, decay or recency boost were recoverable; recency appears
  only as "last seen" ordering and in a working-memory relevance column.
- Keyword search ANDs terms, returns ~200-character excerpts, defaults to 20
  results (max 30) and may return day/hour summary rows alongside captures.
  Working memory is the last few hours newest-first with full text and a
  focused-element breadcrumb; recent activity is the last 30 minutes (max 360)
  in forward order with a tail excerpt; a session read returns one app over a
  time window in order. (VERBATIM)
- Routing rules given to the assistant: recent activity first for vague or
  "just now" questions; summaries plus working memory for day/week questions;
  open full captures before quoting anything from a messaging app; Wiki and
  summaries are orientation, never evidence. (VERBATIM)

### Processing

- Summary shape: hour ≈ 4–6 bullets / 80 words; day ≈ 4–7 bullets / 180 words
  plus an optional short list (≤ 4) of commitments and upcoming items; week and
  year are thematic. Summaries keep child IDs, source snapshot IDs and a dirty
  flag so only changed periods regenerate. (VERBATIM limits)
- Hourly extraction yields entities (person, project, topic, tool, org, with
  aliases and relations) and typed flags (commitment, blocker, decision,
  artifact, pivot, question) each tied to a source snapshot; already-known page
  names are supplied so entities do not fragment. (VERBATIM)
- Wiki pages are assembled from atomic claims tagged by subject (user, world,
  third party), basis (observed action, on-screen content, user-stated,
  inferred), worth (core, detail, noise) and cited excerpt indexes; noise and
  uncited claims are discarded and claims describe what was observed rather
  than inferred roles. (VERBATIM)
- Open loops are consolidated in bounded passes (≤ 6 items) that must re-list
  kept loops by key; each carries a source snapshot, an exact quote, a horizon
  and a confidence where ≥ 0.85 is reserved for near-certain items. Closing a
  loop requires quoted resolution evidence; silence, age, viewing or drafting
  never count as done. Default relevance windows: 3 days, 24 hours for
  same-day or meeting items, 30 days for projects. (VERBATIM)
- Attention extraction runs per screen (≤ 12 email/event/commitment items)
  with numbered screen lines cited by id. Answer grounding uses a claim
  verification pass that cites evidence-span handles resolved by code, then a
  supported/issues gate with a single rewrite; nothing is sent when no
  supported content remains. (VERBATIM)
- Proactive messages are JSON (send, title, body, deep link, skip reason); a
  privacy block skips sensitive context; contextual nudges are capped at one
  per day; meeting briefs follow who / last time / since then / open / bring
  up; the UI schedules a morning brief at 08:45 and an evening wrap at 16:45.
  A ≈ 400-word memory digest (focus, projects, people, decisions and
  commitments, preferences and tools) is rebuilt daily and injected into chat.
  (VERBATIM)

### Capture and privacy

- Identical content seen again updates the existing row in place: last-seen
  time, accumulated duration, sighting count and the latest focused element.
  (VERBATIM SQL)
- Secure text fields are never read; private-browsing windows are detected by
  title; a user blacklist, built-in rules and a remotely fetched exclusion
  list gate capture; secrets are redacted by more than 100 patterns; frames
  from Goldfish itself, system chrome and low-signal windows are dropped, and
  content under 32 characters is excluded from indexing and summaries.
  (VERBATIM)
- Not recoverable without disassembly: capture and scheduler intervals, token
  budgets, vector index parameters, fusion weights and dedup thresholds.

### Model and transport

- Backends: a hosted gateway by default, Azure OpenAI, an OpenAI-compatible
  bring-your-own-key path and an Anthropic path; the local embedder is a small
  static model used for Wiki vectors. The daemon serves the same read-only data
  over loopback HTTP with a bearer token, and sixteen MCP tools: search,
  chronicle, working memory, attention context, nudge history, promises, work
  context, memory facts, recent activity, snapshots, session read, Wiki search,
  page and list, time report and time rules. (VERBATIM)

### What Blur takes from this (plan 2026-11-16)

Merged on 2026-11-16 (branch `feature/goldfish-recall-parity`): a realistic
recall scenario harness (15 natural-language scenarios × no-model and
fake-generator modes, all passing); stored, source-linked day and ISO-week
rollups generated only by the local background provider; `read_session`,
`list_wiki`, `get_time_report` and `get_promises` reads; planner routing for
recency, day parts, conversation recaps, speaker questions, pronoun follow-ups
and stored-location cues; majority-term ranking with login-chrome demotion and
chronological multi-day recaps; no-model extractive whole-page answers,
bare-hour reminders and task-creation phrasings; a 32-character minimum-content
filter on every write path with per-source sighting counts; a focused-element
breadcrumb per capture; and persistent, deletion-fenced question history.

The 32-character admission gate described in that historical plan was removed
on 2026-11-22. Current Blur capture admits any non-empty sanitized text, including
short terminal commands and one non-whitespace Unicode code point. Blank text,
privacy exclusions, authorization and current-revision checks still gate writes.

### Remaining gaps (2026-11-16)

Inside the recall goal:

- Normalized body text with learned, per-surface boilerplate stripping: Blur
  still indexes the raw sanitized capture and only drops Accessibility chrome
  by role. Repeated sidebars and navigation can still outrank substantive text
  for rare terms.
- Inferred commitments: open loops are user-authored or explicit markers;
  nothing yet reads captured prose for promises with confidence, horizon and
  quoted closure evidence.
- Entity detection only recognises capitalised names or Library matches at
  retrieval time; the planner cannot consult the Library, so lowercase names
  ("what did dima say") do not become a subject.
- Native original-source expansion from derived recaps, long direct totals and
  exact Search across long periods, and rule-based project time remain open.
  Persisted day/week/month/year work now shares an encrypted fair queue, bounded
  compatible historical backfill, zone rebasing and calendar ancestor fences.
  Months use days and years use months within six total model calls per pass.
  Admission uses authenticated metadata receipts; legacy supporting edges drain
  in bounded claims. Source/year reverse records remove the old lifetime
  512-reference ceiling, with deferred cleanup and restartable model-free erasure.
  Arbitrary manual 256-source publication remains open. Native long recaps prefer
  stored calendar parents and retain a disclosed distributed fallback (up to
  400 days, at most eight raw originals).
- Semantic recall covers only the optional local index; raw captures are
  lexical, as in Goldfish, but Blur has no vector index over Library pages.
- The live Accessibility breadcrumb path, long-session voice capture and
  cross-app native acceptance remain unverified until a native acceptance run.
- Background visibility: rollup counters joined the Settings panel; voice,
  meeting and open-loop jobs still report nothing there.

### Update 2026-11-19: search parity

Merged on `main`: window titles on every capture (redacted, bounded, indexed
with weight 2, exposed as `window_title`); diacritic folding and possessive
stripping in the one tokenizer used by index, queries and the planner;
background re-index of existing stores after the format bump (open no longer
blocks; `indexing_pending` until done); a shared ranked search core for the
native Search page and MCP `search_memory` (all-terms, then flagged
majority-term "closest matches"; matched terms, rarity, closeness, recency);
summary rows from day/week rollups and hour recaps; match-centred excerpts;
`search_memory` limits raised to Goldfish's (default 20, max 30) under the
16 KiB budget; a second harness of search queries over both surfaces.

Upgrade note: the first launch of this build re-indexes existing captures in
the background (search reports "indexing pending" until it finishes, roughly
0.7 s of queueing per 1,000 captures at open plus the background pass).
Captures now carry a version-3 record when a window title exists; a Mac still
running an older build cannot import sync segments that contain them and stops
at the first such segment until it is upgraded (nothing is lost). Upgrade all
synced Macs together.

The inferred Library folded-name gap was resolved on 2026-11-21 (below).
Still open for search: a vector index over Library pages (Goldfish embeds Wiki pages; Blur's optional
meaning index covers captures only) and lowercase entity detection in the planner. Old published capture-part tokens now migrate in bounded background work (below). Observation open-path cost: queuing the re-index is
still O(N) in receipts (about 0.7 s per 1,000 captures measured).

Additional parity gaps (not started): morning/evening briefs and contextual nudges,
inline writing assistance and learned writing rules, time-rule editing UI,
attention items from observed calendars and mail, chat threads/inbox, and
ChatGPT/Claude history import. Details per area are in
`docs/goldfish-feature-parity.md`.


### Update 2026-11-21: folded Library names and dynamic journeys

Inferred Library identity keys stay unchanged; an additive, authenticated
folded-name posting index resolves accent/composition variants without merging
same-named mentions across contexts. Values remain encrypted and names in keys
remain keyed. New writes and erasure maintain postings atomically. A durable
cursor backfills one entity per transaction through the existing local indexing
worker, even with AI disabled. Opening does not scan all entity bodies. Empty
Libraries do not report false pending coverage. Reader allowances include the
bounded posting scans; the 128-context/six-sources-per-context regression passes.
Scoped reads apply app/domain filters before the eligible-result limit and
preserve a correction in a requested domain behind other same-name contexts.

The new stateful, coverage-guided journey runner changes action order and saved
state instead of generating a static list of phrasing variants. Native Client
Ask, Entry, encrypted persistence, tasks/reminders, source deletion, follow-ups
and reopen behavior are exercised. Mutation first exposed a sighting-event
oracle error (duplicate event instants are idempotent), then a real routing gap:
bare Find stopped being a local lookup after its only source was deleted.
Recognized retrieval commands now retain their memory intent and return a
missing-evidence answer without requiring a model. Procedural requests,
ordinary factual probes and explicit general mode retain separate routes. A later minimized
case found the same missing-evidence gap for stored-record location questions;
that route now also preserves memory intent, including quoted Search topics.

The one-million-execution campaign is a target, not achieved coverage. Report
actual execution counts and minimized failures, and keep the full objective
active. See [dynamic memory scenarios](dynamic-memory-scenarios.md) for the
runner, independent oracles, replay commands and remaining acceptance gaps.
Explicit Library exact-identity APIs are unchanged. Live app acquisition,
unrestricted language/intent understanding remain outside this change's evidence.


### Update 2026-11-21: retained capture token migration

Published parts with old accented tokens now enter a durable content-format
backfill. Open updates bounded metadata; the existing local worker resets one
edition's derived progress and removes/rebuilds each part's authenticated
proofs in bounded batches. Fully indexed and partially indexed old parts are
covered. Original encrypted part payloads, publication IDs, source IDs and
visit provenance stay unchanged. Reopening resumes; completed current stores
remain read-only on open. Search reports pending coverage until work finishes.

Review found a transient citation-resolution gap in an intermediate version.
Permanent regressions now check raw evidence, sighting times and whole-capture
deletion through cited part IDs between every work quantum. The final proof
removal atomically creates an empty current root and preserves part membership;
term indexing happens in a later round. Deletion before the cursor and during
rebuilding, an old queue head ahead of backfill, and newly arriving captures
have separate lifecycle coverage. This does not establish universal app capture
or unrestricted natural-language understanding.


### Update 2026-11-21: event dates versus observation dates

Synthetic mail confirms a calendar-recall defect: a Monday appointment question
previously filtered captures to the past Monday and hid its Thursday-observed
confirmation. Clear event-time grammar now retains dates as content cues while
keeping observation provenance intact. Explicit activity questions and read
clauses retain their own capture windows. Event questions avoid inheriting an
unrelated activity window; date-only continuations retain the topic and replace
old date cues. Future visits, nominal book readings, mixed date clauses and
bounded continuation terms have planner/native regressions.

An inherited event topic is kept separately from replaceable date cues. Span
classification preserves a token that also occurs outside a date (Route 23 on
23 November, or a May project in May). Native chains include app refinement and
answer shortening between date updates. A separate probe found a lexical
limitation: a source date can supply the same token as a route number, even in a
first-turn query. The following update adds numbered-phrase support for it.

### Update 2026-11-21: numbered topic phrase support

The date/route token collision now has bounded local phrase support. Planning
retains adjacent topic/number pairs independently of date spans and carries
them through date/app/grounded refinements. Numbered-topic evidence precedes
broad term counts and app/recency boosts; unverified pairs are closest matches.
The shared Search/MCP core classifies its bounded candidate pool before page
truncation, including when every independent word occurred in an unsupported
source. Ask uses the same source/title matcher and flags, including late bare
and entity lookup plans. Quoted qualifiers preserve unquoted numbered cues;
wholly literal subjects keep their term contract.

Candidate reads share the existing aggregate hydration byte ceiling and report
incomplete support coverage instead of claiming an exhaustive ranking. Runtime
journeys vary numbered subjects against newer date-token collisions and check
Ask plus Search/MCP. These are lexical relevance checks, not general semantic
ID resolution or native acquisition acceptance.

The runtime journey now also mutates future event dates, older capture dates,
clock times and mail/browser origin independently, checking the actual source
and extractive missing-end wording. Existing minimized failure selectors remain
stable. Generated-answer semantics, general event extraction, Dutch acceptance
and calendar integration remain unverified; a large mutation count cannot
establish them.

### Update 2026-11-21: range-only capture consistency

A native CF/range regression reproduced a same-length edit producing mixed
chunks while retaining the window, path, role and character count. Range-only
document values now require a matching second streamed content digest before
completion. Both passes retain path/privacy/count checks and bounded round work;
verification advances the job without duplicating text or allocating a whole
document. Immutable AXValue snapshots remain the preferred path. Unknown-range
visible slices remain partial. Mutation tests vary multilingual text, edit
timing, read sizes, secure transitions and unavailable providers; job regressions
also check deadline continuation, cancellation and publication receipts.

This establishes repeatable observed ranges, not atomic app snapshots or
universal native capture. A non-interactive standalone capability probe returned
`permission_required` on this machine; this does not establish the signed app's
grant. No permissions were changed. The signed app has not been launched for
this check because AGENTS.md requires separate launch authorization.

### Update 2026-11-21: Dutch recall and Interact read authority

Explicit Dutch personal recall/search now shares local planning with English.
A grammar-only calendar view reuses observation-window calculation while
retaining original topic terms and question text. Event-time questions keep
content dates separate from relative past-read clauses; app/date and bounded
bare-weekday follow-ups carry the event topic and numbered-subject cues.
Search retains its domain/literal exclusions. Generic Dutch task collections
reuse local status/calendar/count filters without new mutation authority.

Native encrypted-store regressions exposed an integration gap beyond planning:
unpunctuated Dutch reads could fall through Interact to saving a note. The
composer now shares the Dutch read recognizer with the planner, preserving
explicit note/task prefixes. Independent review also reproduced and verified
fixes for weekday domains, explicit web requests, nominal activity adjectives
and bare-date follow-ups. Deletion tests include previous answer prose and
require grounded continuations to abstain after the sources disappear.

Runtime journeys vary Dutch source/query content, dates, clocks, app origins,
numbered conflicts and collection reads against independent expected records.
The older million-target campaign continues against its recorded immutable
baseline; newer language code has separate current-source checks. This does
not establish arbitrary Dutch semantics, task/reminder creation, source
translation, model-answer correctness or universal native app acquisition.


### Bounded historical source references and erasure recovery

New rollup/source links use encrypted per-source/year leaves (at most 432 possible
calendar/ISO period IDs) and a fixed logical four-digit-year bitmap directory.
The directory encodes sparse nonzero bytes for ordinary histories and uses a
dense form only when smaller; encoded size stays between 4 and 1,251 bytes. Legacy
512-element reverse arrays are read/drained alongside new links, without eagerly
rewriting them. Ordinary unlinks retain empty leaves, and source erasure removes
at most 16 empty years separately from 8 forward-row invalidations per transaction.

Input-only erasure clears public recap text, child/source IDs and generation
metadata immediately. A separate encrypted receipt retains pending reverse edges,
with at most 32 unlinks per cleanup claim. Publication is blocked while that receipt
exists, including candidates with a fresh revision. A hard-delete receipt keeps
the forward row catalogued until final receipt/row removal can commit together.

Recap clearing has its own encrypted intent, separate from original-source
deletion. It fences reads and writes while deleting at most one hour or one
rollup-cleanup quantum per transaction. An ending epoch fences claims acquired
during clearing. The always-running local storage service resumes intents without
a model, including after reopening with AI disabled; it never starts historical
discovery when no intent exists. These are storage/worker guarantees. They do
not establish arbitrary-app capture or full persisted month/year acceptance.


### Persisted calendar hierarchy integration

Calendar month/year persistence, historical discovery and generation now use the
full source-linked lifecycle. Backfill version 2 revisits completed version-1
history; the fixed four-date/sixteen-row discovery quantum remains. Day/week/month/year
claims have separate persisted cursors. The six-call per-pass inference budget
reserves opportunities for higher levels, and month/year generation waits for
compatible discovery and clean children. Local calendar bounds, active zones,
revisions and erasure epochs fence publication.

Hour changes atomically invalidate every overlapping calendar ancestor; input-only
erasure clears public fields at all four levels. A Kathmandu New Year regression
with 64 revisited capture sources crosses eight ancestors and fits the existing
physical transaction limit. A bounded mutation-aware encrypted-record cache removes
redundant metadata reads while retaining normal integrity-manifest writes. Reopen
and rollback probes verify repeated writes, deletion and recreation.

Native Ask selects stored calendar parents, Search indexes them, MCP exposes their
period/child hierarchy, and grounded follow-ups stop reopening erased summaries.
Limited coverage survives both MCP search transport and native summary rows.
Independent review and targeted checks pass; current runtime lifecycle checks
passed 78 reported executions with four new coverage-interesting inputs from
68 baseline entries on the final test revision. The canonical app was rebuilt
and strictly verified; the complete suite passed (56 tested packages), as did vet and targeted race checks. This does not establish live app capture or arbitrary-model
semantic quality.


### Native recap originals and changing-memory publication

Hour/day/week/month/year source cards can expand the recap's saved supporting
originals inline. The reader authenticates the current stored period from the
answer's existing derived handle and time bounds; cached answer text and claimed
source IDs provide no read authority. A receipt binds complete persisted recap
metadata, so unchanged wording with new provenance still invalidates old page
cursors and full-source requests. Each request hydrates at most eight originals;
previews retain at most 4,096 UTF-8 bytes each, and one full original is held
separately. Closing sources, leaving Ask, browsing, replacing the answer or opening
the note editor cancels loading and releases these payloads.

Reads check current provenance again after hydration. Queued UI publication also
holds both the Client deletion fence and a durable committed-write fence through
the tiny state assignment. This conservatively retries after any intervening
store write, including background recap regeneration. UI installation performs
no database I/O and never waits for an active writer. Failed transactions retain
the earlier valid token. Preview truncation clones the visible bytes to release
unshown capture-part backing storage. Ask planning now converts the real clock to
the same resolved local zone as the recap worker, including when Go previously
cached a different process zone.

`FuzzRecapSourceNavigation` generates changing histories against actual encrypted
storage, the calendar worker, native Ask and mounted source actions. Its separate
insertion ledger checks exact live source IDs and original text after edits,
erasure, reopening, recap clearing, pagination, full reads and queued publication.
Inputs vary calendar years/months, UTC/Kathmandu, app origins, personal/work topics,
large multilingual bodies and action order. Mutation feedback retains inputs
that exercise new code coverage; no static catalogue is presented as a million
unique needs. The local summary generator and GUI dispatch boundary are controlled.
These checks do not establish live cross-app acquisition, arbitrary model semantics
or visual acceptance in a launched app.

This increment passed independent review, the complete suite (56 tested packages),
vet and targeted race checks. The final-source navigation mutation run passed
25 executions with three new coverage-interesting inputs from nineteen baseline
entries, including the retained minimized scenario. The canonical universal app
was rebuilt and strictly verified against the exact original signing certificate
and designated requirement, with only the audio-input entitlement. No app
lifecycle action was performed; the live acceptance gaps above remain open.


Long-scope Search now covers explicit month/year capture windows up to400 days
through at most15 small index queries. Native Search and MCP share global ranking,
source deduplication, actual matching visit instants and preserved coverage flags.
Activity-only legacy membership, app/domain intersections and quoted years have
actual encrypted-store regressions. Selected-model recent/rewrite candidates and
embedding sighting resolution retain bounded storage calls; model quality is not
claimed. A durable commit token rejects a split read if memory changes during it.
Browse/meaning candidate reads share 2 MiB of returned source bytes across windows
and filters and stop before issuing an invalid smaller storage request.

The final long-Search source passed the complete suite (56 tested packages), vet,
focused race checks and all retained runtime source-navigation cases. The
current-source mutation run passed 28 reported executions with 1 new interesting
input from 22 baseline entries. A fresh review exposed mixed snapshots, meaning
integration, timestamp and coverage/admission defects; retained RED regressions
now pass. The canonical signed universal app was rebuilt and independently
verified against its exact original certificate and designated requirement.
No app lifecycle action was performed. Live cross-app, voice, arbitrary-model
and native visual acceptance remain open. These counts
are never added to older campaigns as unique user needs.


Exact month/year tracked foreground time now shares a metadata-only scanner
across native Ask, Time and MCP. Up to 400 local dates require at most fifteen
28-day windows; existing storage row/date limits stay unchanged. Each piece is
clipped to its child, original IDs count sessions once, and durations stay in
nanoseconds until presentation. The independent retained-session and aggregate
metadata ceilings are each 65,536 entries. Mandatory failures, cancellation or
committed memory changes discard the total. Empty-app unknown recovery gaps and
Blur itself cannot become tracked foreground time.

Native Time pages 20 app rows. Its queued installation checks dates, zone, page
and the exact durable read token. Direct time Ask carries an opaque publication
guard through the assistant service; its own convenience history write compares
and advances that token atomically, and native installation rechecks it without
IO. A source erased before queue dispatch cannot reappear as a stale total.
The shared day splitter stops at actual zone transitions and strictly advances,
including skipped/repeated midnights. Report/MCP date endpoints use the first
real instant of the civil date. App and website qualifiers are supported;
unverified topic/project totals receive explicit insufficient evidence.

The changing-history target exercises actual encrypted source/activity
replacement, erasure, reopen, duplicate rejection and sightings, plus real
Engine-produced unknown gaps and varied natural time wording. It compares native
reports, MCP and Ask against an independent inserted-duration ledger. Its counts
are stress executions, not semantic uniqueness or live macOS acquisition proof.


The final tracked-time source passed the complete suite (56 tested packages),
vet and focused race checks. The current-source mutation run passed 63 reported
executions with 12 new interesting inputs from 40 baseline entries; it includes
grounded time follow-ups after mutations. A first full run exposed follow-up
integration failures and incorrect test assumptions, now covered by passing
regressions. The durable suite completed in 724.443 seconds with a 30-minute
package budget; production operation deadlines were unchanged. The canonical
universal app was rebuilt and strictly verified against the exact original
certificate and designated requirement, retaining only the audio-input
entitlement. It was not launched. The larger personal-memory goal remains open;
these counts are not semantic-unique use cases or live macOS acquisition proof.

## Work-driven text processing and multi-subject recall

All accepted source text can enter the existing local enrichment queue in
summary-only mode, independently of optional Library extraction. Compaction
retains independent encrypted original-span blocks instead of a rolling
whole-source summary. The next source cursor and its block commit atomically;
successful batches resume after interruption, and erasure drains each block.
Input spans adapt to actual model/power admission. Pending sources rotate with
reserved opportunities for multipart and legacy history. Semantic indexing also
checkpoints successful partial batches and publishes only complete revisions.
Schema 8 introduced these private namespaces through an atomic metadata-only migration
from schema 7, retaining existing payload ciphertext and identity.

Technical comparisons retrieve each named subject independently while preserving
the strict identifier conjunction within that lookup. Complete labels are
validated within every physical app/date page before accepted results compete
for ranking and returned-source bytes. Distinct original passages can share one
real source ID; bounded UTF-8 ranges retain the full original separately and
render explicit gaps. Context allocation accounts for the subjects supported by
each selected passage. Actual text/JSON clipping recomputes which subjects have
support, and citations must cover those subjects. Missing or omitted subjects
are disclosed. Ordinary Search and explicit relationships retain conjunctions;
comparison follow-ups retain their original subject scope.

Dated Library projections validate each fact/open loop against its own observed
source support, including exact historical visits. An unrelated recent entity
mention cannot date an older claim. Corrections suppress their originals before
date projection, and the stored Library is not rewritten. See the
[functional review](blur-functional-gap-review.md) for verified cases and open
issues; these mechanisms do not establish unlimited model context, million-record
throughput, complete Accessibility acquisition or Goldfish parity.

## Text integrity receipts and original-range search

The current reader floor is schema 9. Existing schema-7/8 stores upgrade through
an atomic authenticated metadata change; original payload ciphertext and saved
identity remain intact. Older readers reject admission before their erasure code
can leave new source-linked metadata behind.

Capture exact indexing now carries a bounded Unicode normalization segment,
original part boundaries and an unfinished token digest across physical parts.
Completed seam tokens use authenticated original-range evidence, while ordinary
tokens retain their canonical part IDs. Citation reads authenticate the same
publication and intersecting original parts; they retain neighboring original
context without copying or rewriting the capture. Old derived indexes rebuild
through persisted work and retire their obsolete fragment postings.

One normalization segment can emit several terms with overlapping original
spans. Encrypted pending and active work rows preserve those projections
independently and drain before advancing the input part. Only identical spans
merge core digests. Word projections index their original global token digests;
neighbor context does not invent substring or sibling postings. A short match
therefore remains readable beside a word exceeding one source-read envelope.
Identifier-run projections separately preserve joined technical labels.
Oversized requested evidence returns an explicit budget error. Reset and erasure
remove pending work, core digests, range proofs and derived postings.

Replica export authority is an authenticated receipt for the writer generation
and exact source revision, rather than the largest capture timestamp. Bounded
physical scans and acknowledgement batches retain continuation through equal-time
and backdated inputs. One private sealed outbox becomes durable before immutable
public publication and replays the exact segment bytes after interruption.
Configuration commits share a publication gate; folder changes rotate writer
identity, disable cancels old work, and erasure retires unpublished private data.
The public wire format remains version 1. Published multipart captures still need
a compatible transport and are not yet synchronized by that observation format.

Native deletion preparation reads source/range task metadata in separate bounded
transactions while holding exclusive deletion authority and the scheduling
guard. Native failure or cancellation before journal admission preserves the
source. Targeted reminder cleanup authenticates current source/revision authority
without exposing captured text and preserves unrelated pending and delivered
notices. The actual erasure journal then drains every accepted part and derivative.
