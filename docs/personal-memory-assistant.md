# Personal memory assistant milestone

## Goal and completion boundary

Make Blur a dependable, macOS-first personal brain and memory assistant for a technical user. The user explicitly requested autonomous implementation and verification of every requirement below. This document records the requested milestone, observed gaps, and acceptance cases; it does not describe all of these features as shipped.

1. Inspect the installed Goldfish UI and features and audit realistic daily use cases.
2. Capture useful app and browser content without requiring a Safari extension.
3. Answer natural-language questions across encrypted captures and notes using semantic retrieval, time context, and source citations.
4. Make “What did I do today?” and “Where did I leave off?” reliable.
5. Automatically populate Library with connected people, projects, topics, and source-backed memories; make inferred information inspectable and correctable.
6. Support general questions, notes, tasks, and actual scheduled reminders through one intelligent entry point.
7. Remember explicit personal preferences and writing rules.
8. Preserve privacy controls, exclusions, encrypted storage, and complete deletion.
9. Repair capture/index gaps, backfill existing memory where possible, and verify the main flows end to end in the native macOS app.

Completion means current source, automated checks, and native runtime evidence demonstrate all nine requirements. A passing storage test cannot establish native capture coverage. A passing retrieval test cannot establish reminder delivery. Missing native evidence remains an open requirement.

## Evidence from Goldfish

The user's screenshot shows Goldfish 0.1.69, including Home, Timeline, Aquarium, Inline rewrite, Edit mode, Voice dictation, MCP, Reminders, Wiki, Writing style, Memory map, About you, Import memory, Account, Privacy, and Ignore list. Writing rules can be searched and filtered by app/site, individually paused, and added. Visible rules say they were learned from edits. This is evidence of controls and displayed claims, not verification of how their background implementation works.

Goldfish's [introduction](https://docs.goldfish.sh/) and [privacy documentation](https://docs.goldfish.sh/privacy/) describe foreground-window text capture through the operating system's accessibility layer, with no screenshots or screen recording. Its [settings reference](https://docs.goldfish.sh/reference/settings/) describes automatically built person/project/topic pages, an editable identity, proactive messages, and scheduled reminders. The reference differs from the screenshot and cannot substitute for the requested installed UI audit.

Installed UI audit completed through CUA on 2026-11-12, Goldfish 0.1.69. Opening the dashboard with Command-comma and dismissing an open native menu restored access after earlier window-selection failures. Reviewed Home, Timeline, Aquarium, Inline rewrite, Edit mode, Voice dictation, MCP, Reminders, Wiki (including sources and its editor), Writing style (including its editor), Memory map, About you, Import memory, Account, Privacy, Ignore list, Tutorials, What's new, Refer a friend, Report a bug, and Community. The Support & docs navigation item was visible; its linked documentation was not opened during this UI pass. Returned to Home, canceled editors, and left settings and stored data unchanged. No report, invitation, or rewrite example was sent.

Observed product behavior and controls:

| Area | Installed UI evidence | Implication for Blur |
| --- | --- | --- |
| Home and Timeline | Activity counts, app/category time, dated daily stories, and proposed tasks with source excerpts, due labels, Open, Mark done, and Dismiss | A useful memory dashboard joins capture health, grounded time, source-backed task suggestions, and day/resume recall |
| Capture and privacy | Accessibility permission, text-only capture explanation, pause/resume, weekday/weekend schedules, optional copied-text context, app/site exclusions, pattern-masking caveat, and Delete all data | Expose capture state and coverage; apply exclusions before reads; separate local storage from model disclosure. The UI claimed protections; this audit did not verify their implementation |
| Current capture state | Privacy displayed Paused; Home had no captured work time for today | Goldfish's empty current day has an observable capture-state explanation. This does not establish the cause of Blur's missing captures |
| Wiki | Search and category filters for people/projects/organizations/topics/tools; automatically maintained entries, mention counts, linked entities, related pages, and dated expandable recent captures | Automatic Library pages need evidence inspection and correction, beyond a list of search requests |
| Wiki editing | Separate Summary, Page, and optional Your corrections fields with Cancel/Save | Keep explicit corrections distinguishable from generated content and stable through regeneration |
| Memory map | Connected entity graph, category filters, search by meaning, labels, 3D, reset, and fullscreen | Semantic discovery and connected memory matter; decorative graph rendering is outside this milestone |
| Writing style | Search and app/site filter; rules labeled learned from edits or observed writing; pause, add, edit, delete, scope and priority controls | Explicit scoped rules need inspection and lifecycle controls. Passive style learning is a later feature with separate provenance |
| About you | Editable personal profile and dated memory digest; UI says changes apply on next retrieval | Saved preferences/facts should be inspectable, correctable, and used by retrieval without waiting for a restart |
| Reminders | One-off reminders with name, message, and date/time; proactive-message master control, notification sound, optional meeting briefs | Durable scheduled reminders must be separate from suggested task dates; expose actual delivery state |
| Check-ins | Recurring AI prompts with daily/weekday/selected-day/hourly schedules; editable morning/evening built-ins; pause/edit/remove controls | Scheduled AI work is a distinct feature from notification reminders; do not imply Blur's recurring reminders execute an AI prompt |
| Intelligent entry and editing | Chat about remembered work, contextual drafting, inline rewrite, iterative typed/spoken edit instructions, and voice dictation | Unified questions/actions and useful follow-up context are core. System-wide writing replacement and dictation remain outside this milestone |
| MCP | Local stdio server command and automatic AI-client connection controls | Memory should be available consistently in native Ask, Search, and MCP without confusing tool use with captured activity |
| Import | File/folder/path preview and import for AI exports, Markdown vaults, and session logs | Useful future ingestion path; not required to make existing capture/note backfill work |
| Help and feedback | Feature tutorials, replay onboarding, release history, account/billing/password controls, referrals/community, report preview, opt-in rewrite sharing | Diagnostics must clearly disclose any transmission. No sharing or account changes were performed |

These observations establish displayed UI features, not Goldfish's internal architecture, capture reliability, model routing, or notification delivery. The Privacy page describes remote AI processing separately from local memory storage; Blur retains its own stricter local-processing consent boundaries.

A subsequent [read-only local architecture inspection](goldfish-local-architecture.md) examined the installed daemon's static markers and SQLite schema. It confirms raw snapshots with sighting metadata, distinct activity records, recent working memory, hour/day/week/month chronicles, source-linked Wiki/facts/relations, separate corrections, FTS5 indexes, and a separate HNSW index with a small local embedding model. Exact retrieval control flow and ranking remain unverified. Blur's encrypted store can implement the same useful layers without adopting Goldfish's readable SQLite storage.

Goldfish is a reference for the experience, not an instruction to duplicate all of its features in this milestone. Inline rewriting, voice dictation, and a decorative memory graph are audited as use cases; the named milestone requires remembered preferences and writing rules, not a clone of every competitor control.

## Use-case audit

| Job | Example | Required outcome | Observed starting gap |
| --- | --- | --- | --- |
| Recall the day | What did I do today? | A grounded account of captured work, notes, and gaps, with timestamps and citations | Recap depended on activity/chronicle projections; stored captures could be missed |
| Resume work | Where did I leave off with Blur? | Last relevant state, decisions, unfinished steps, and useful sources | No reliable cross-source resume retrieval |
| Find a half-remembered page | Find that post about making Go builds reproducible | Match concepts and paraphrases; return original evidence | Exact terms only |
| Recover an exact detail | What was that compiler error or command? | Preserve exact code/error strings and source context | Bounded exact search exists; retrieval can wrongly depend on activity segments |
| Connect research | Compare the approaches I read about yesterday | Find multiple related sources, distinguish them, cite comparisons | No semantic discovery or complete time retrieval |
| Recover a decision | Why did we choose Bolt instead of SQLite? | Cite recorded reasoning; distinguish inference and uncertainty | Topic overlap does not reliably recover reasoning |
| Track change | What changed in the deployment plan this week? | Order conflicting versions and show which is newer | Source content and observation episodes are not unified |
| Recall people | What did Mira ask me to do? | Join relevant captured messages, notes, and open loops | Library is manually curated; no automatic connections |
| Recall a project | What do you know about Northstar? | A current, source-backed project page and useful timeline | Library stays empty until manual creation or accepted suggestions |
| Save a note | Note: investigate the flaky login test | Persist encrypted text immediately, then make it retrievable | Note flow exists; inference and intent classification are limited |
| Save a fact | Remember that Northstar's deploy window is Tuesday | Save an explicit memory with user provenance | No general durable personal-memory command |
| Remember a preference | Use short paragraphs; use Dutch for family messages | Persist explicit, scoped preferences and apply relevant rules | No durable profile or writing-rule service |
| Create a task | Add “finish the release notes” to my list | Durable task with a clear state, editable and completable | Today proposals exist; task lifecycle is incomplete |
| Create a reminder | Remind me tomorrow at 9 to review the release | Persist timezone/due date; deliver once; show pending status | “Reminder” is a proposal, not a scheduled notification |
| Manage a reminder | Move that reminder to Friday; cancel it | Resolve the target, persist the edit, cancel old delivery | No scheduling or durable reminder lifecycle |
| Ask general questions | Explain this Go error; what is a mutex? | Answer normally without inventing personal history | General path exists but intent detection can misroute input |
| Follow up naturally | And what about the other option? | Carry conversation context and resolve references | Only short in-memory history/scope inheritance |
| Review time | How long did I spend coding versus chatting? | Exact eligible app time; clearly label inferred categories | App totals exist, but cannot prove attention or outcomes |
| Recall web content | What was that Orion article I read this morning? | Capture active page text without an extension; identify site when exposed | Native AX path exists; Orion not classified as a browser |
| Understand missing data | Why can't you find the page I just read? | Explain capture state, permission, exclusions, last successful capture, and index coverage | Empty results collapse distinct failure states |
| Correct memory | Mira works on Atlas now; that old fact is wrong | Preserve explicit correction and show source/version history | User-authored entities exist; inference correction workflow absent |
| Forget something | Forget that source / person / date range | Invalidate text, vectors, facts, cached answers, and pending derivations consistently | Existing deletion cascades need extension for new derived data |
| Use another AI | Find this context from Claude via MCP | Same bounded semantic retrieval and citation rules | MCP exposes separate exact-only search |
| Work offline | Save a note or task without a model | Immediate durable write; process enrichment later | Core note writes work; new flows must preserve this |
| Mix languages | Dutch question about an English source | The selected model can reformulate and rank bounded local candidates across languages | Exact token matching alone cannot provide this |
| Exclude sensitive work | Never capture this banking domain or private window | Exclude before reading text, with honest coverage limits | Browser identity/domain extraction must be consistent |
| Build memory over months | Find an old source without scanning the database on every ask | Bounded indexes, resumable backfill, honest incomplete status | Hour recovery index silently limits sources to 64 per hour |

## Architecture decisions

### One evidence system

Encrypted observations are primary evidence. Activity, summaries, Library, profile, vectors, and reminders are projections or explicit user records with provenance. Capture visibility and retrieval must not depend on a summary being generated or an activity segment closing. Repeated sightings of the same content need occurrence metadata; content deduplication must not erase when it was seen again.

Preserve complete accepted source text independently of derived indexing. Transaction budgets bound indexing batches, never a document's vocabulary. Large pages require resumable term indexing, source-linked continuation beyond capture/record bounds, and honest pending/truncated coverage. Whole-document questions must consider all relevant chunks; choosing one bounded passage is sufficient only for a passage-level question.

Provide a complete encrypted time index with bounded pages and stable continuation. Keep the existing 64-source hourly chronicle selection as a summarisation budget, never as the only searchable timeline. Backfill from authenticated observation metadata in resumable transactions. Older timestamps already replaced by a migration baseline cannot be reconstructed; expose that limitation rather than inventing dates.

### Natural language retrieval

Combine exact terms (code, names, errors), selected-model query reformulation and bounded candidate ranking, time/app/domain filters, knowledge links, and recent occurrence context. Use one selected model for meaning search, answers, note processing, and other model work; do not require a separate embedding-model setting or server. Exact searches remain immediate when the selected model is unavailable. Rank and deduplicate candidates before hydrating bounded evidence; deterministic code remains authoritative for scope limits, permission, citation validation, and writes.

The former standalone embedding index is legacy and dormant in standard startup. Its saved settings are preserved without starting another model service. If a future implementation adds vector retrieval, it must remain under the single selected-model setting, retain source/chunk identity, and invalidate incompatible derived generations safely. Remote model use continues to require the applicable explicit consent.

#### Apple Ask latency and evidence gate (2026-11-15)

On an exact-search miss, selected-model recall currently makes one generation
request for alternate lexical queries, searches those terms, then makes another
generation request to rank the resulting passages. Ask may then make its answer
generation request. The Apple helper handles one request per process and
constructs a new `LanguageModelSession` for each generation, so those stages do
not share session KV cache. Apple documents session reuse and prefix prewarming
as ways to reduce repeated input processing; see
[Apple's session-cache guidance](https://developer.apple.com/documentation/foundationmodels/optimizing-key-value-caching-in-language-model-sessions).

Do not remove the relevance pass merely because query expansion found one
source. A model-generated alternate term can match an unrelated source exactly;
`TestAskRejectsSingleUnrelatedModelExpandedCandidate` protects against exposing
that source to Ask. The next latency change should preserve ranking and scope an
ephemeral Foundation Models session to one Ask operation, then discard its
transcript. It must remain cancelable on deletion or AI-policy changes, and
must not retain source text across separate questions. Measure query rewrite,
candidate search, ranking, and final answer separately before and after; a
lower model-call count alone does not demonstrate lower end-to-end latency.

Ask, Search, Today, Library exploration, and MCP should consume the same evidence contracts. Search should offer natural-language recall while keeping exact-match behavior available for technical strings. Empty answers must distinguish no capture, excluded source, index still building, model unavailable, and genuinely insufficient evidence.

### Memory that grows and can be corrected

Automatically extract bounded, source-linked entity/fact candidates from new captures and notes when local enrichment is configured. Show inferred versus explicit/confirmed provenance. Link entities across captures using aliases and conservative matching; do not merge people on a name alone. Explicit user corrections override inference and survive regeneration. Deleted evidence cannot be recreated by an already running job.

Remember explicit profile facts and writing rules immediately as encrypted user-authored records. Rules may have a global, app, domain, project, or audience scope. Retrieval selects only applicable rules. The UI supports inspecting, editing, pausing, and deleting them. Inference from edits may be offered later with separate provenance; explicit rules are required now.

### One entry point with real actions

Use deterministic parsing for clear prefixes and time commands, and bounded model intent parsing for ambiguous natural language where configured. Expose the interpreted action in the result, preserve text on failure, and never claim an action succeeded before the durable write. Questions should not silently become notes. A note containing a date is not automatically a reminder.

Tasks and reminders have durable IDs and state. Reminders store the requested local time, IANA zone, resolved next delivery time, and delivery state. Native notifications are idempotent, cancellable, rescheduled on edits, and recovered after restart. The UI reports notification permission and whether delivery requires the app to remain running. Ambiguous essential scheduling details prompt a specific question rather than guessing silently.

### Privacy remains part of every flow

Native browser capture reads only the active window's accessible content, with no requirement for an extension. Apply app/domain exclusions and private-window checks before values are read. Handle supported browser identity consistently, including Orion. Extensions can enrich active-page metadata but remain optional.

Capture audit follow-up (source state, 2026-11-15): bounded discovery now selects and leases the active visible `AXWebArea`, prunes browser chrome, checks page/window URL metadata, and revalidates the selected page and focus around capture. Optional `AXManualAccessibility` activation is scoped to the verified foreground PID and rate-limited. The native reader supports text areas, text fields, search fields, combo boxes, static text, web areas, headings, cells, and documents; secure roles and password subroles are rejected before reading, with sensitivity rechecked before deferred reads. Synthetic AX/browser fixtures cover these policies and pagination. They do not prove compatibility with live Safari, Orion, Chromium, Firefox, Terminal, or third-party apps. Native app/browser coverage, large-page acceptance, and the user's actual Downloads build remain open; source-level support must not be described as universal runtime support. [Electron's official guide](https://www.electronjs.org/docs/latest/tutorial/accessibility) documents `AXManualAccessibility` for third-party assistive clients; [Chromium's accessibility guide](https://www.chromium.org/developers/design-documents/accessibility/) explains on-demand activation.

Indexing and inference honor the same source exclusions and deletion fences as Ask. Derived indexes remain encrypted. Do not recapture Blur's own generated answers as independent evidence. Preserve remote-provider disclosure and consent boundaries. “Complete deletion” means no source or derived content remains reachable through Blur's storage/read APIs; physical erasure limitations still apply to backups, filesystem snapshots, and media.

## Native acceptance run

Use disposable, clearly labelled fixture content and avoid changing the user's actual memories to test behavior. Record evidence for: native app capture; an active browser page with extension disabled; note save and semantic paraphrase recall; today's recap before an hourly summary exists; project resume; Library automatic population and correction; task completion; scheduled notification delivery/edit/cancel/restart; scoped writing preference application; deletion during indexing/Ask; excluded domain/private window; and honest unavailable/offline states.

The installed Goldfish settings review is complete. The native Blur acceptance run remains open; current CUA access to Goldfish is not proof that the changed Blur build has been exercised. Automated coverage and bundle builds continue independently.
