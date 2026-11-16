# Goldfish-level recall parity — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) tracking.

**Goal:** Make Blur's Ask and MCP recall match Goldfish 0.1.70's observable behavior on realistic multi-app, multi-day memory while encrypted captures stay authoritative and the model stays the last step.

**Spec:** `docs/superpowers/specs/2026-11-16-goldfish-recall-parity.md`. Background: `docs/goldfish-feature-parity.md`, `docs/goldfish-local-architecture.md`.

## Global Constraints

- Encrypted captures are the only evidence. Every derived record (recap, rollup, report, session page) carries source IDs that resolve through `get_snapshots`; derived text is never returned as evidence without them.
- Model is the last step. No model call on the exact-match fast path. New background generation runs only through the existing local background provider policy (Apple Foundation Models or Ollama; never a remote provider), through the existing `internal/memory` queue/worker pattern, and is skipped cleanly when no local provider is configured.
- Clean room: do not copy any Goldfish prompt text, strings, or schema names into Blur. Match behavior (bullet counts, word limits, sections, routing), write original wording.
- No storage format regression: `durable.OpenSchemaVersion` stays 7; new records are additive; stores written by current `main` open unchanged through `durable.OpenExisting`.
- MCP tools are read-only, go through the existing authenticated local RPC proof path (`internal/localapi`), respect existing per-tool byte budgets in `internal/client/mcp_read.go`, and always exclude Blur's own interface captures (the self app identity from `internal/appidentity`).
- Fail closed on invalid input: bad IDs, out-of-range limits, malformed period keys, windows over the maximum, stale memory epochs.
- Go only, cgo-free, no new module dependencies; `go vet ./...` and the affected package tests pass; keep `go test ./...` green.
- Do not launch, quit, or open Blur.app. Build only with `make macos-app-stable`.
- Production code must not be changed in Task 1. Tasks 2 and 3 must not edit `internal/client/scenario_*_test.go`.

### Task 1: Recall scenario harness

**Files:** `internal/client/scenario_corpus_test.go` (new), `internal/client/scenario_recall_test.go` (new). No production code changes.

- [ ] Build a corpus helper that opens a durable store at `t.TempDir()` with `securestore.OfflineFixtureKey()` and `durable.CreateNew`, seeds captures with explicit capture times through the same path `TestNativeMultipartTailReachesPublicSearchAndAskThenWholeSourceDeletion` (`internal/client/multipart_ingest_retrieval_test.go`) uses, so activity segments, the exact index and the observation window are populated the way production ingest populates them. Fix the planning clock to 2026-11-16 15:30 Europe/Brussels using the mechanism `Client.AskTime()`/`askClock` already supports in tests. Record every seeded capture under a short label so assertions read `mustCite: []string{"slack-dima-retest"}`.
- [ ] Seed these captures (write realistic text with UI chrome, not one-liners):
  1. `slack-dima-1`, `slack-dima-2`, `slack-dima-retest`: Slack DM with Dima (app `com.tinyspeck.slackmacgap`, domain `app.slack.com`), sidebar + messages on 2026-11-14 10:12, 2026-11-14 16:40, and 2026-11-15 14:47; only the last contains "the Orfit external retest is scheduled for Thursday 20 November, I will send the scope tomorrow".
  2. `slack-samira-1..4`: Slack DM with Samira on 2026-11-13 11:05, 2026-11-14 09:20, 2026-11-15 10:30, 2026-11-15 17:05 about the "AG presentations" and a deadline "end of day Friday".
  3. `mail-inbox`, `mail-chloe`: Apple Mail (`com.apple.mail`) inbox list at 2026-11-14 08:50 and an opened message from Chloe Britton at 2026-11-14 08:55 asking for the consultant reporting template by Wednesday.
  4. `orion-article`: Orion (`com.kagi.kagimacOS`, domain `tiiny.ai`) long article about running local LLMs, 60,000–70,000 bytes of generated paragraphs with the distinctive sentence "Tiiny Pocket runs a 120B model without a cloud" in the first 500 bytes and "the last paragraph mentions the always-on companion" in the final 500 bytes, captured 2026-11-16 10:07 and sighted again 10:14.
  5. `terminal-venice`: Terminal (`com.apple.Terminal`) session on 2026-11-12 15:22 containing `export VENICE_API_KEY=$(op read "op://Infra/venice/api-key")` among 40 unrelated shell lines.
  6. `notion-pentest-plan`: Notion (app `notion.id`, domain `notion.so`) page "Pentest planning Q4" captured 2026-11-16 15:05, the newest capture overall.
  7. `okta-login`: Orion page at domain `cresco.okta.com` captured 2026-11-15 09:00 with text "Sign in to Cresco — Dima pentest portal — Username — Password — Keep me signed in".
  8. `blur-self`: a capture whose app ID is Blur's own identity (from `internal/appidentity`) at 2026-11-16 15:20 containing "Ask Blur: what did Dima say about the pentest retest".
  9. `teams-idelux` (Microsoft Teams, 2026-11-15 14:10, "Idelux local project — need to report, testing period 20–21 October"), `preview-programme` (Preview, 2026-11-15 16:30, "Security First 2026 Brussels programme, Youssef Bey project lead"), `finder-downloads` (Finder, 2026-11-15 09:30, "Downloads — 14 items").
  10. `voice-youssef`: a voice-note transcript at 2026-11-16 11:40, "Call with Youssef: he will send the SOW by Friday and wants the AG deck first", seeded through the same recorded-content path `internal/client/voice_video_recall_test.go` uses.
  11. `note-buildwise`: a typed note saved on 2026-11-13 12:00 through the notes path, "Buildwise evidence packet URL: https://evidence.buildwise.example/packets/2026-11".
  12. `sublime-sync-1`, `terminal-sync-2`: Sublime Text and Terminal work on "blur sync segments" on 2026-11-09 14:00 and 2026-11-10 16:00.
  13. Hourly recaps for 2026-11-15 13:00–17:00 and 2026-11-16 09:00–15:00 seeded through the chronicle store the way `TestSuggestKnowledgeFromChronicleRequiresAnExplicitLocalProvider` seeds (`client.chronicles`), each with the matching labels' source IDs and a two-bullet text.
- [ ] Scenarios, each run in mode `noModel` (`assistant.NewService(client, nil, nil, assistant.ContextTokenLimit)`) and mode `fakeGen` (a generator that returns a short answer citing the first two packed source IDs, modelled on `integrationAnswerGenerator`/`multipartAnswerGenerator`):
  | name | question | mustCite | mustNotCite | expectInsufficient |
  |---|---|---|---|---|
  | today-recap | what did I do today | ≥2 of orion-article, notion-pentest-plan, voice-youssef | blur-self | no |
  | just-now | what was I just working on | notion-pentest-plan | blur-self, okta-login | no |
  | venice-key | where is the API key for venice stored again? | terminal-venice | — | no |
  | samira-recap | recap my convo with Samira | ≥2 of slack-samira-1..4 | slack-dima-*, okta-login | no |
  | dima-retest | what did Dima say about the pentest retest? | slack-dima-retest | okta-login, blur-self | no |
  | orion-article | summarize that article I read in Orion about local LLMs | orion-article | — | no |
  | yesterday-afternoon | what did I do yesterday afternoon? | ≥1 of teams-idelux, preview-programme, slack-dima-retest | finder-downloads, okta-login | no |
  | last-week | what did I do last week? | ≥1 of sublime-sync-1, terminal-sync-2, slack-samira-1 | — | no |
  | youssef-promise | what did Youssef promise? | voice-youssef | — | no |
  | buildwise-url | what's the buildwise evidence packet url? | note-buildwise | — | no |
  | who-is-dima | who is Dima? | ≥1 of slack-dima-* | okta-login | no |
  | unknown-person | what did I discuss with Marco? | — | any | yes |
  | followup-when | (history: dima-retest) when did he say that? | slack-dima-retest | — | no |
  | reminder-create | remind me tomorrow at 9 to send the AG report | — | — | no; assert a reminder for 2026-11-17 09:00 local exists via the client's reminder/task listing |
  | task-create-list | add a task: follow up with Chloe on the consultant reporting → then: what's on my todo list? | — | — | no; the second answer lists the task |
- [ ] MCP checks via `client.HandleMCPRead`: `search_memory` for "Dima retest" ranks `slack-dima-retest` first and never returns `blur-self`; `get_working_memory` newest item is `notion-pentest-plan` and excludes `blur-self`; `get_recent_activity` includes `notion-pentest-plan`; `get_snapshots` on `orion-article` pages through the whole 60 KB with `next_offset_bytes` until done and the final page contains the tail sentence.
- [ ] Each scenario logs `SCENARIO <name>/<mode>: PASS|FAIL|KNOWN-GAP — cited=<labels> diagnosis=<Result diagnosis fields>`. A scenario struct field `knownGap string` marks currently failing cases: a failure with `knownGap` set logs KNOWN-GAP and does not fail the test; a pass with `knownGap` set fails the test with "scenario now passes, remove knownGap". Set `knownGap` only after confirming the failure twice and describing the observed behavior in one sentence. Never weaken `mustCite`/`mustNotCite` to make a scenario pass.
- [ ] Run `go test ./internal/client -run 'TestRecallScenario' -count=1 -v` and record the full PASS/FAIL/KNOWN-GAP table plus each failure's diagnosis in the report file.

### Task 2: Day and week chronicle rollups

**Files:** `internal/storage/chronicle.go` (or wherever `ChronicleStore` lives), `internal/storage/durable/*`, `internal/storage/store.go` (ephemeral), `internal/chronicle/rollup.go` (new), `internal/chronicle/worker.go`, `internal/chronicle/worker_status.go`, `internal/client/recap_retrieval.go`, `internal/client/mcp_read.go`, `internal/localapi/rpc.go`, `internal/mcpserver/server.go`, `internal/assistant/plan.go` (layer constant), tests beside each.

- [ ] Add a `ChronicleRollup` record: `ID` (`day:2026-11-15` or `week:2026-W46`), `Level` (`day`|`week`), `PeriodKey`, `Start`/`End` (UTC), `Zone` (IANA name), `Text` (≤ 2,048 bytes, validated UTF-8, no control chars), `ChildIDs` (hour IDs for a day, day IDs for a week, sorted unique, ≤ 64), `SourceIDs` (union of child source IDs, sorted unique, ≤ 256), `GeneratedAt`, `Model`, `InputHash` (hex SHA-256 of the ordered child texts), `Dirty bool`. Store it additively in the durable store (new record namespace, schema stays 7) and in `EphemeralMemoryStore`, behind `PutChronicleRollup`, `GetChronicleRollup(ctx, id)`, `ListChronicleRollups(ctx, level, start, end, after, limit ≤ 64)` on `ChronicleStore`. Round-trip, bounds, and open-existing tests.
- [ ] Rollup scheduling in `internal/chronicle`: when an hour recap is persisted or deleted, mark its local day rollup dirty (create a dirty placeholder if absent); a day becomes generatable once the local day (in the recap zone) has ended or the day already has a rollup and is dirty; a week once its ISO week has ended; dirty rollups regenerate only when `InputHash` changed. Generation uses the existing local background provider only and is a no-op without one (status reports `skipped_no_local_provider`). Prompt (original wording) asks for 4–7 bullets within 180 words for a day plus an optional final section of at most 4 commitments or upcoming items, 3–5 bullets within 120 words for a week, every bullet grounded in the supplied child summaries, no invention; input = child texts in order prefixed with their period label, capped at 6,000 bytes dropping oldest first with a `(earlier hours omitted)` marker. Validate the output (non-empty, ≤ 2,048 bytes, no `src:`/ID-like tokens) before persisting.
- [ ] Deletion fence: when a source ID is deleted, every rollup referencing it drops the ID from `SourceIDs` and becomes dirty, following the existing hour-recap deletion pattern.
- [ ] Native Ask: in `recapEvidence`, when the plan window spans at least one full local day and has no app/domain filter, add day rollups covering whole days inside the window as evidence with a new `assistant.LayerDay` (and `LayerWeek` for whole ISO weeks) ahead of hour recaps, carrying `SourceIDs` for provenance and the synthetic-ID convention hour recaps use; hour recaps and raw captures still fill partial days. Diagnosis gains `RollupsDirty int`.
- [ ] MCP: `get_chronicle` accepts optional `level` (`hour` default, `day`, `week`) and `period` (`YYYY-MM-DD` / `YYYY-Www`); day/week responses return `level`, `period`, `text`, `child_ids`, `source_ids` (first 64) plus `source_ids_truncated`, `generated_at`, `dirty`. Malformed level/period fails closed with the existing invalid-request error.
- [ ] Background status (`worker_status.go` and the existing Settings > AI background panel data): add `rollups_pending` and `rollups_dirty` counters so the panel shows rollup work.
- [ ] Tests: storage round-trip and bounds; worker generates a day after its hours, regenerates only on input change, skips without a local provider, week after day; deletion fence; `recapEvidence` prefers the day rollup for "yesterday" and still uses hours for "this afternoon"; MCP level/period validation. Run `go test ./internal/storage/... ./internal/chronicle/... ./internal/client/... ./internal/mcpserver/... -count=1`.

### Task 3: MCP parity tools `read_session`, `list_wiki`, `get_time_report`

**Files:** `internal/client/mcp_read.go`, `internal/localapi/rpc.go`, `internal/mcpserver/server.go`, `internal/client/mcp_time_report.go` (new), tests beside each. Depends on Task 2 being merged (shared files).

- [ ] `read_session {app, from, to, limit}`: `app` matched case-insensitively against app IDs and the display names `KnownApps` reports; `from`/`to` Unix seconds, `to - from` ≤ 24h; `limit` 1–20 default 10; returns captures for that app inside the window in capture-time order with full text pages under the existing `get_snapshots` byte budget (same `next_offset_bytes` convention), `captured_at`, `last_seen_at`, `source_id`; excludes Blur's own interface; empty window returns an honest empty result with `indexing_pending` when the exact index is behind.
- [ ] `list_wiki {type?, limit}`: Library entries ordered by (confirmed facts + open loops) descending then name, `limit` 1–50 default 50, optional `type` restricted to the existing `knowledge` kinds; each row has `id`, `kind`, `name`, `aliases`, `summary` (first confirmed fact text, ≤ 160 bytes), `fact_count`, `open_loop_count`.
- [ ] `get_time_report {period?, from?, to?}`: `period` one of `today|yesterday|this_week|last_week|last_7_days|last_30_days` (weeks start Monday, local zone) or inclusive local dates `from`/`to` ≤ 31 days; computed from activity segments: `total_seconds`, `buckets` (per app, then per domain for browsers) each with `seconds`, `by_day` map, `top_segments` (app/domain/title with seconds, ≤ 20) and `omitted_segments_seconds`; a `note` string states this is tracked foreground time, not a timesheet; Blur's own interface excluded. Time rules are out of scope and documented as a gap.
- [ ] Register the three tools in `server.go` with descriptions that tell the host when to use each (recent timeline → read_session after chronicle/recent activity; list_wiki to orient before search_wiki; time report framed as tracked screen time) and MCP server tests for discovery, validation failures, and one happy path each.

### Task 4: Harness-driven retrieval fixes

- [ ] Specified from the Task 1 report once it exists: one sub-task per failing scenario cluster (ranking, routing, exclusion, follow-up, creation), each fix accompanied by flipping the scenario's `knownGap` to empty. Do not start before the Task 1 report is read and this section is filled in.

### Task 5: Parity record and canonical build

- [ ] Update `docs/goldfish-feature-parity.md` statuses for chronicles, MCP tools, time, and the scenario harness; append a dated "Behavioral findings (2026-11-16)" section to `docs/goldfish-local-architecture.md` describing the recovered behavior in Blur's own words (no Goldfish text), and a "Remaining gaps" list.
- [ ] `make macos-app-stable`, verify `codesign -dv --verbose=2 ~/Downloads/Blur.app` reports `com.hazcod.blur` with the existing Developer ID, do not launch the app.
