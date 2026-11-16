# Observable Goldfish feature parity

This checklist is based on Goldfish 0.1.70's installed app manual and static
binary strings, with Blur's current implementation as of 2026-11-16 (branch `feature/goldfish-recall-parity`). It tracks
observable feature areas rather than asserting internal source-code parity.
The stripped Goldfish binaries do not expose enough information to reconstruct
every ranking rule, model prompt, or runtime edge case.

| Area | Goldfish evidence | Blur status |
| --- | --- | --- |
| Background desktop capture | App manual and daemon capture strings | Implemented for macOS; native acceptance and cross-app coverage still require validation |
| Search and assistant recall | MCP search, recent activity, working memory, chronicles, Wiki and full snapshots | A recall scenario harness (`internal/client/scenario_recall_test.go`, 15 natural-language scenarios × no-model and fake-generator modes over a realistic multi-app, multi-day corpus) drives the real Ask pipeline and the MCP reads. Native Ask now routes recency ("what was I just working on", one widening to 6 h), day parts ("yesterday afternoon"), conversation recaps with a person, speaker questions ("what did Dima say about…"), pronoun follow-ups anchored to previously cited sources, and stored-location questions ("where is … stored again?"), all model-free; whole-page requests answer extractively without a model. `get_working_memory`, `get_recent_activity`, `search_memory`, `get_snapshots`, `read_session`, `get_chronicle` (hour/day/week) are exposed over MCP. Remaining: majority-term ranking and login-chrome demotion land with the ranking task; semantic search still covers only the optional local index. |
| Original evidence | Full snapshot retrieval after excerpt search | `get_snapshots` reads verified local sources in UTF-8-safe pages, with multipart provenance |
| Time and activity | Timeline, time reports, chronicle at hour/day/week/month/year levels | Stored, source-linked day and ISO-week rollups are generated from hour recaps by the local background provider only (dirty-tracked, regenerated on input change, cleared and fenced when a cited or input source is erased, never served without source IDs); native recap questions prefer them for whole days and weeks; `get_chronicle` accepts `level` and `period`; `get_recent_activity`, `get_working_memory`, `read_session` and `get_time_report` (tracked foreground time by app/domain, not rules) are exposed. Remaining: month/year rollups, rule-based project time, a time-rules UI. |
| Entity memory | Wiki, aliases, facts, relations, memory map | `search_wiki`, `get_wiki_page` and `list_wiki` expose bounded Library entries with aliases, source-linked facts and open loops; automatic enrichment proposes entities in the background when a local model is configured. Remaining: graph relations/memory map, lowercase-name entity detection in the planner, dated-fact supersession is modelled but not written by any job. |
| Commitments and follow-through | Promises, follow-ups, blockers, To Do, closure evidence | `list_tasks` and `get_promises` expose explicit tasks, reminders and Library open loops with provenance and a note that closure needs source evidence; tasks and reminders can be created from natural language without a model ("add a task: …", "remind me tomorrow at 9 …"). Remaining: inferred promises from captured prose with confidence and due dates, blockers, closure-evidence lifecycle. |
| Conversational workspace | Aquarium threads, inbox, queues, notifications, follow-up prompts | Blur has the Ask/quick panel with a session conversation and pronoun follow-ups; persistent question history is in progress. Aquarium-style threads, inbox, queues and notification follow-ups remain gaps. |
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
   compact stored recap directly. `get_working_memory` exposes recent captures
   and range reads expose hourly inputs, but Blur does not yet persist a
   short-term memory layer or daily/weekly summaries.
3. Expose Library entity and explicit task reads with source IDs and current
   state, then teach Ask/MCP routing to combine them with raw captures. (`search_wiki`, `get_wiki_page`, and `list_tasks` implemented.)
4. Audit the remaining user-facing areas above against Blur's app and tests;
   close the important workflow gaps before claiming parity.

Goldfish's static operating instructions specifically say to fetch before
answering memory questions, choose the time/entity/task tool for the question,
batch independent reads, and open original sources for exact claims. Blur's
source reads honor the authenticity and pagination part of that design now.
The checklist intentionally remains open; this is not a 100% parity claim.
