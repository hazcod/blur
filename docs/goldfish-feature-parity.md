# Observable Goldfish feature parity

This checklist is based on Goldfish 0.1.70's installed app manual and static
binary strings, with Blur's current implementation as of 2026-11-15. It tracks
observable feature areas rather than asserting internal source-code parity.
The stripped Goldfish binaries do not expose enough information to reconstruct
every ranking rule, model prompt, or runtime edge case.

| Area | Goldfish evidence | Blur status |
| --- | --- | --- |
| Background desktop capture | App manual and daemon capture strings | Implemented for macOS; native acceptance and cross-app coverage still require validation |
| Search and assistant recall | MCP search, recent activity, working memory, chronicles, Wiki and full snapshots | `get_working_memory` reads up to six hours of newest source-linked captures; ranged and single-hour chronicle reads, source reads, and Library entity search are available over MCP. A persisted short-term memory layer remains missing |
| Original evidence | Full snapshot retrieval after excerpt search | `get_snapshots` reads verified local sources in UTF-8-safe pages, with multipart provenance |
| Time and activity | Timeline, time reports, chronicle at hour/day/week/month/year levels | `get_recent_activity` exposes bounded source-linked activity; `get_chronicle` reads one hourly recap and `get_chronicles` pages hourly recaps by range so connected assistants can synthesize day/week overviews. Stored multi-scale summaries and time-report tools remain missing |
| Entity memory | Wiki, aliases, facts, relations, memory map | `search_wiki` finds bounded Library entries and `get_wiki_page` pages through one subject's source-linked facts/open loops; graph relations and memory map remain missing |
| Commitments and follow-through | Promises, follow-ups, blockers, To Do, closure evidence | `list_tasks` exposes explicit task/reminder state and provenance; inferred promises, blockers, and closure-evidence lifecycle remain missing |
| Conversational workspace | Aquarium threads, inbox, queues, notifications, follow-up prompts | Blur has Ask/quick panel and question history; Aquarium thread and inbox workflow parity is incomplete |
| Writing assistance | Inline rewrite, edit mode, learned writing rules | Blur's current parity is incomplete; inspect implementation before claiming support |
| Voice | Dictation, spoken briefs, voice interactions | Blur has on-device voice notes/transcription and recap; spoken briefs and broader dictation workflows remain gaps |
| Privacy controls | Pause, never-capture categories, ignore list, masking | Blur has capture/privacy controls; the policy model and settings experience differ and require a separate audit |
| Proactive work | Reminders, scheduled prompts, morning/evening briefs, contextual nudges | Blur has reminders, hourly recaps and background work; prompt rules, nudges and brief parity are incomplete |
| Time tracking | Rules, assignment, reports | Blur stores activity but Goldfish-style editable tracking rules and reporting MCP tools remain gaps |
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
