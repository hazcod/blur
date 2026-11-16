# Goldfish-level recall parity

Date: 2026-11-16

## Evidence (installed Goldfish 0.1.70, read-only inspection)

Tagged VERBATIM where taken from strings/SQL in the shipped binaries or from the
MCP tool descriptions, INFERRED otherwise. No Goldfish text is copied into Blur:
Blur is a clean-room reimplementation and only behavior is matched.

- Retrieval layers (VERBATIM tool contract): `get_recent_activity` (last 30 min,
  forward timeline, tail excerpt, focused-element breadcrumb), `get_working_memory`
  (last few hours, newest-first, full text), `get_chronicle` (hour/day/week/month/
  year summaries with `child_ids`, `snapshot_count`, hour `snapshot_ids_sample`),
  `search_memory` (FTS5 `ORDER BY rank`, ANDed keywords, ~200-char excerpts, results
  may include `chronicle:day:…` rows), `get_snapshots` (verbatim text), `read_session`
  (one app over a time window, in order), `search_wiki`/`get_wiki_page`/`list_wiki`
  (entity axis), `get_promises`/`get_work_context`/`get_memory_facts`/
  `get_attention_context`/`get_nudge_history` (follow-through), `get_time_report`/
  `list_time_rules` (passive time tracking).
- Agent routing (VERBATIM chat rules): check recent activity first for vague or
  "just now" questions; chronicle plus working memory for day/week questions; open
  snapshots before quoting anything from messaging apps; wiki and chronicle are
  orientation, never evidence.
- Chronicle shape (VERBATIM prompt limits): hour = 4–6 bullets ≈ 80 words; day =
  4–7 bullets ≈ 180 words plus an optional commitments/future section ≤ 4 items;
  week/year = short thematic focus. Summaries carry `snapshot_ids` and `child_ids`
  and an `is_dirty` flag for regeneration.
- Capture (VERBATIM SQL): identical content seen again updates the row in place
  (`last_seen_at`, `duration_s +=`, `sighting_count +=`); password fields are never
  read; Goldfish's own windows, system chrome and content under 32 chars are dropped.
- Ranking (INFERRED): BM25 only, display text prefers a normalized `body` over raw
  `content`; no recency decay was recoverable. Vector search covers wiki pages via a
  small static embedder; raw snapshots are lexical.

## Goal

Blur's native Ask and MCP surface answer the questions a Goldfish user asks
("what did I do yesterday afternoon", "what did Dima say about the retest",
"where is that API key stored again", "recap my convo with Samira", "what did I do
last week", "what was I just doing") from realistic multi-app, multi-day memory,
citing only verified captures, with the model as the last step.

## Design

1. Scenario harness: a realistic synthetic corpus (Slack, Mail, Orion, Terminal,
   Notion, Teams, Preview, Finder, a login page, Blur's own UI, a voice note, a typed
   note, hourly recaps) and natural-language scenarios run through the real Ask
   pipeline in no-model and fake-generator modes plus the MCP read tools. Known
   failures are tagged, never hidden; the harness stays green and flips tags as
   fixes land.
2. Stored multi-scale chronicles: day and ISO-week rollups derived from hour recaps,
   source-linked, dirty-tracked, deletion-fenced, generated only by the local
   background provider, read by native recap questions and by `get_chronicle`
   with a `level`.
3. MCP parity tools: `read_session`, `list_wiki`, `get_time_report` as bounded,
   read-only, self-excluding reads over existing records.
4. Harness-driven retrieval fixes, each proven by a scenario flipping from
   known-gap to pass.
5. Parity record updated with what remains; signed canonical build.

## Acceptance criteria

- Exact matches never trigger a model call; no new remote model paths.
- Every derived record returned to Ask or MCP carries source IDs that resolve via
  `get_snapshots`; Blur's own interface captures are never cited.
- Schema stays 7; stores written by current `main` open unchanged.
- `go vet ./...` and `go test ./...` pass; `make macos-app-stable` produces the
  canonical signed bundle without launching it.
