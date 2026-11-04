# Blur Full Reimplementation Plan

> **Execution status:** Long-range product backlog. The original macOS-first
> ordering and fixed storage choice are superseded by
> `2026-11-04-blur-cross-platform-foundation.md`. Resume this backlog only
> through the gates and interfaces established by that foundation.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a clean-room, open-source macOS personal memory and writing assistant in Go with encrypted local capture, efficient retrieval, provider-neutral AI, inline writing, dictation, proactive memory, and MCP.

**Architecture:** A Wails 3 GUI talks to a single-writer Go daemon over an authenticated user-only Unix socket; separate `blur-mcp` and `blurctl` binaries reuse the daemon API. The daemon stores deduplicated, compressed observations in encrypted SQLite, performs deterministic retrieval first, and sends only bounded evidence packs through an explicit local/remote provider policy.

**Tech Stack:** Go 1.27.3, Wails 3.0, Svelte 5, TypeScript, Vite, ncruces/go-sqlite3 encrypted Adiantum VFS + FTS5, zstd, official MCP Go SDK v1.8+, macOS AXUIElement/AppKit/Keychain/Audio shims.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md`

## Global Constraints

- Initial platform is macOS 13 or newer on arm64 and amd64.
- Working product name is Blur; license is Apache-2.0.
- Capture accessible text only; never request Screen Recording permission.
- Capture, storage, search, timeline, and time accounting must work without AI or an account.
- The default database, WAL, journals, temp data, backups, and exports must never contain plaintext memory at rest.
- API keys and the 32-byte database key live in macOS Keychain, never config or logs.
- The daemon listens on a mode-0600 Unix socket, verifies peer UID where supported, and requires an install token; there is no default TCP listener.
- Captured content is untrusted evidence and cannot alter policies, tool permissions, settings, or provider routing.
- Capture never invokes AI; semantic embeddings target chronicles/wiki pages, not every raw observation.
- Strict Local Mode blocks non-loopback AI payloads at the network layer.
- Remote background AI is disabled by default.
- Default AI caps are inline 4k/1k, chat 12k/2k, chronicle 8k/1k, wiki 6k/1k, and classification 2k/400 input/output tokens.
- Logs and crash diagnostics contain identifiers, timings, and counters only—never captured, chat, rewrite, or transcript text.
- No original Goldfish code, prompts, assets, branding, or secrets may enter the repository.
- Production Go must pass `go test -race ./...`, `go vet ./...`, `govulncheck ./...`, and frontend typecheck/tests.

## Review Focus

1. Secure/password fields and recognizable credentials must be rejected or redacted before storage, indexing, logs, or model egress. Pinned by Tasks 5, 8, and 12.
2. If the focused application, field, selection, or original draft changes during generation, Blur must not inject the result. Pinned by Tasks 17 and 18.
3. Kill/restart during database write, migration, import, export, rekey, or delete must recover without corruption or plaintext artifacts. Pinned by Tasks 3, 21, and 22.
4. Instructions embedded in captured pages must remain quoted evidence and must not change the system prompt, call a mutating tool, or bypass provider policy. Pinned by Task 14.
5. Strict Local Mode must reject DNS aliases, redirects, proxy paths, and non-loopback endpoints before sending any payload. Pinned by Task 12.

---

## Repository map

```text
cmd/blur/                 Wails application entrypoint
cmd/blur-daemon/          background daemon entrypoint
cmd/blur-mcp/             stdio MCP entrypoint
cmd/blurctl/              administrative CLI
internal/platform/        portable OS capability interfaces and fakes
internal/platform/macos/  cgo/Objective-C macOS adapters
internal/securestore/     Keychain-backed keys and credentials
internal/storage/         encrypted SQLite, migrations, repositories
internal/privacy/         exclusions, secure-field policy, redaction
internal/capture/         bounded AX frame ingestion and deduplication
internal/activity/        foreground sessions, recent buffer, time ledger
internal/search/          FTS, timeline, session and source retrieval
internal/ai/              providers, policy, token accounting and cache
internal/memory/          chronicles, wiki, facts, identity and style
internal/retrieval/       query routing and evidence-pack construction
internal/chat/            conversations and streamed answer orchestration
internal/inline/          target discovery, rewrite/edit and safe delivery
internal/dictation/       audio capture, transcription and insertion
internal/proactive/       schedules, open loops, briefs, workflows, nudges
internal/localapi/        authenticated Unix-socket API/client
internal/mcpserver/       MCP tools over the local API
internal/lifecycle/       export, import, retention, deletion and rekey
frontend/                 Svelte UI and generated Wails bindings
build/darwin/             entitlements, Info.plist and bundle resources
packaging/                release, signing, notarization and update metadata
testdata/                 synthetic, non-personal fixtures only
docs/                     architecture, threat model, user and contributor docs
```

### Task 1: Bootstrap the OSS repository and quality gates

**Files:**
- Create: `go.mod`, `go.sum`, `Makefile`, `.gitignore`, `.editorconfig`
- Create: `LICENSE`, `README.md`, `CONTRIBUTING.md`, `SECURITY.md`
- Create: `.github/workflows/ci.yml`, `.github/dependabot.yml`
- Create: `frontend/package.json`, `frontend/package-lock.json`, `frontend/tsconfig.json`, `frontend/vite.config.ts`
- Create: `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Produces: module `github.com/blur-memory/blur`; `buildinfo.Info` and `buildinfo.Current() Info` used by every binary.

- [ ] **Step 1: Write the failing build-info test**

```go
func TestCurrentHasNonEmptyVersionAndCommit(t *testing.T) {
    got := Current()
    require.NotEmpty(t, got.Version)
    require.NotEmpty(t, got.Commit)
}
```

- [ ] **Step 2: Run `go test ./internal/buildinfo` and verify it fails because the package does not exist.**
- [ ] **Step 3: Create the Go module, Wails/Svelte dependency manifests, OSS policy files, CI jobs, and `Current() Info`; pin exact dependency versions and Go 1.27.3.**
- [ ] **Step 4: Run `go test ./...`, `go vet ./...`, `npm --prefix frontend ci`, and `npm --prefix frontend run check`; expect all to pass.**
- [ ] **Step 5: Commit with `chore: bootstrap Blur repository`.**

### Task 2: Define platform contracts and deterministic fakes

**Files:**
- Create: `internal/platform/platform.go`, `internal/platform/types.go`
- Create: `internal/platform/fake/accessibility.go`, `hotkeys.go`, `audio.go`, `system.go`
- Test: `internal/platform/fake/fake_test.go`

**Interfaces:**
- Produces: `platform.Accessibility`, `Hotkeys`, `AudioRecorder`, `SecretStore`, `Notifier`, `SystemState`, `LoginItem`; types `WindowSnapshot`, `TextTarget`, `Replacement`, `AXEvent`, `PermissionState`.

- [ ] **Step 1: Write compile-time interface assertions and a test where fake AX events are delivered in order and cancellation closes the stream.**
- [ ] **Step 2: Run `go test ./internal/platform/...`; expect undefined interface/type failures.**
- [ ] **Step 3: Implement focused interfaces with context-aware methods; fakes must record calls and never use wall-clock sleeps.**
- [ ] **Step 4: Run `go test -race ./internal/platform/...`; expect pass and no goroutine leak.**
- [ ] **Step 5: Commit with `feat: define platform capability boundaries`.**

### Task 3: Open an encrypted database and manage keys safely

**Files:**
- Create: `internal/securestore/store.go`, `memory.go`
- Create: `internal/platform/macos/keychain_darwin.go`, `keychain_darwin.m`, `keychain_stub.go`
- Create: `internal/storage/open.go`, `pragmas.go`, `migrate.go`, `migrations/0001_core.sql`
- Test: `internal/storage/open_test.go`, `migrate_test.go`, `crash_test.go`

**Interfaces:**
- Consumes: `platform.SecretStore` from Task 2.
- Produces: `storage.Open(ctx context.Context, cfg storage.Config, secrets platform.SecretStore) (*storage.Store, error)`; `(*Store).DB() *sql.DB`; `(*Store).Close() error`; `(*Store).IntegrityCheck(context.Context) error`.

- [ ] **Step 1: Write tests asserting the database header and WAL do not contain `SQLite format 3` or a sentinel secret, the key is requested from `SecretStore`, wrong keys fail, and a killed transaction reopens consistently.**
- [ ] **Step 2: Run `go test ./internal/storage`; expect package/function failures.**
- [ ] **Step 3: Implement ncruces SQLite with Adiantum VFS, random 32-byte key creation, mode-0700/0600 paths, `temp_store=memory`, WAL, foreign keys, busy timeout, secure delete, bounded pool, embedded migrations, and clean close/checkpoint.**
- [ ] **Step 4: Run `go test -race ./internal/storage/...` and inspect test temp files with `rg -a 'sentinel secret|SQLite format 3'`; expect no matches.**
- [ ] **Step 5: Commit with `feat: add encrypted SQLite foundation`.**

### Task 4: Create the domain schema and repositories

**Files:**
- Create: `internal/storage/migrations/0002_memory.sql`, `0003_assistant.sql`, `0004_audit.sql`
- Create: `internal/storage/models/*.go`, `internal/storage/repository/*.go`
- Test: `internal/storage/repository/*_test.go`, `internal/storage/schema_test.go`

**Interfaces:**
- Produces: repositories for content blobs, observations, activity, chronicles, wiki/facts, identity/style, chat/rewrite, schedules/nudges/open loops/workflows, time rules/entries, AI runs/cache/token ledger, exclusions/settings/audit.
- Key signatures: `UpsertObservation(ctx, capture.ObservationInput) (storage.Observation, bool, error)`, `Observation(ctx, id string)`, `WithTx(ctx, func(*repository.Set) error) error`.

- [ ] **Step 1: Write schema tests for every required table/index/foreign key and repository tests for insert/read/update/delete, pagination stability, and transaction rollback.**
- [ ] **Step 2: Run `go test ./internal/storage/...`; expect missing migration/repository failures.**
- [ ] **Step 3: Implement typed models and narrow repositories with SQL in owned files; use contentless-delete FTS, zstd blob columns, provenance link tables, UTC integer timestamps, and no ORM.**
- [ ] **Step 4: Run `go test -race ./internal/storage/...`; expect all tests pass with encrypted fixtures only.**
- [ ] **Step 5: Commit with `feat: add memory domain schema and repositories`.**

### Task 5: Redact sensitive data and enforce exclusions before persistence

**Files:**
- Create: `internal/privacy/redactor.go`, `rules.go`, `luhn.go`, `iban.go`, `url.go`, `exclusions.go`, `policy.go`
- Create: `testdata/privacy/redaction_cases.json`, `exclusion_cases.json`
- Test: `internal/privacy/*_test.go`, `fuzz_test.go`

**Interfaces:**
- Produces: `privacy.Filter(ctx context.Context, frame platform.WindowSnapshot, policy privacy.Policy) (privacy.Result, error)`; `Result` is either excluded or contains redacted fields and aggregate rule counts; `Redactor.Version() int`.

- [ ] **Step 1: Write table/fuzz tests covering secure controls, private keys, auth headers, tokens, database URLs, URL secrets, Luhn cards, labeled CVVs, IBANs, SSNs, false positives, password managers, finance/sign-in/checkout/private-mode exclusions, and maximum input sizes.**
- [ ] **Step 2: Run `go test ./internal/privacy/...`; expect missing implementation failures.**
- [ ] **Step 3: Implement deterministic pre-read exclusions and post-read masking with typed constant-length placeholders; return counters only and guarantee errors format no source text.**
- [ ] **Step 4: Run `go test -race ./internal/privacy/...` and `go test -fuzz=FuzzRedactor -fuzztime=30s ./internal/privacy`; expect pass and no sentinel value in test logs.**
- [ ] **Step 5: Commit with `feat: redact and exclude sensitive capture`.**

### Task 6: Secure the daemon local API

**Files:**
- Create: `internal/localapi/server.go`, `client.go`, `auth.go`, `peer_darwin.go`, `limits.go`, `routes.go`, `types.go`
- Test: `internal/localapi/server_test.go`, `auth_test.go`, `fuzz_test.go`

**Interfaces:**
- Consumes: `platform.SecretStore`, repository set.
- Produces: `localapi.Listen(ctx, Config, Handler) (*Server, error)` and `localapi.Dial(Config, SecretSource) (*Client, error)`; route interfaces consumed by GUI, MCP, and CLI.

- [ ] **Step 1: Write tests for mode-0600 socket, token/peer rejection, request/response limits, timeouts, mutation nonce/origin binding, concurrency caps, malformed/nested JSON, and cleanup of stale sockets.**
- [ ] **Step 2: Run `go test ./internal/localapi`; expect missing server/client failures.**
- [ ] **Step 3: Implement HTTP-over-Unix-socket transport with peer UID checks, constant-time token comparison, separate read/mutation middleware, strict JSON decoding, and content-free audit records.**
- [ ] **Step 4: Run `go test -race ./internal/localapi` and its fuzz smoke; confirm `lsof` integration test shows no TCP listener.**
- [ ] **Step 5: Commit with `feat: add authenticated Unix socket API`.**

### Task 7: Implement the daemon lifecycle and health surface

**Files:**
- Create: `cmd/blur-daemon/main.go`, `internal/daemon/daemon.go`, `health.go`, `jobs.go`, `shutdown.go`
- Create: `cmd/blurctl/main.go`, `internal/cli/*.go`
- Test: `internal/daemon/daemon_test.go`, `cmd/blurctl/main_test.go`

**Interfaces:**
- Consumes: storage and local API.
- Produces: `daemon.New(Config, Dependencies) *Daemon`; `Run(context.Context) error`; health/status API; `blurctl status`, `doctor`, `pause`, and `resume`.

- [ ] **Step 1: Write tests for singleton lock, startup ordering, health degradation, signal shutdown, job cancellation, stale-lock recovery, and CLI exit codes.**
- [ ] **Step 2: Run `go test ./internal/daemon ./cmd/blurctl`; expect failure.**
- [ ] **Step 3: Implement structured lifecycle with one database writer, bounded job queues, explicit degraded states, slog redaction handler, and clean shutdown/checkpoint.**
- [ ] **Step 4: Run daemon/CLI integration tests under the race detector and assert captured sentinel text never appears in logs.**
- [ ] **Step 5: Commit with `feat: run secure Blur daemon`.**

### Task 8: Capture macOS Accessibility text safely

**Files:**
- Create: `internal/platform/macos/accessibility_darwin.go`, `ax_bridge_darwin.h`, `ax_bridge_darwin.m`, `ax_types.go`
- Create: `internal/capture/reader.go`, `normalise.go`, `bounds.go`
- Create: `test/axfixture/` fixture app source
- Test: `internal/capture/*_test.go`, `internal/platform/macos/accessibility_integration_test.go`

**Interfaces:**
- Implements: `platform.Accessibility`.
- Produces: bounded `platform.WindowSnapshot` with app/bundle/title/URL/domain/content/focus breadcrumb and `Secure`/`PrivateMode` flags.

- [ ] **Step 1: Write pure normalization/bounds tests and macOS fixture tests for text fields, web areas, large/deep trees, timeouts, secure fields, focus changes, Electron apps, browsers, and permission loss.**
- [ ] **Step 2: Run unit tests and tagged integration tests; expect adapter failures.**
- [ ] **Step 3: Implement AX permission checks, frontmost-app reads, AXObserver events, bounded iterative traversal, secure-role refusal, focus breadcrumb, URL extraction, cancellation, and fallback sampling.**
- [ ] **Step 4: Run `go test -race ./internal/capture/...` and `go test -tags=axintegration ./internal/platform/macos/...`; verify the secure fixture sentinel is never returned.**
- [ ] **Step 5: Commit with `feat: capture bounded macOS accessibility text`.**

### Task 9: Ingest, deduplicate, compress, and index observations

**Files:**
- Create: `internal/capture/pipeline.go`, `dedup.go`, `compress.go`, `service.go`, `backpressure.go`
- Test: `internal/capture/pipeline_test.go`, `benchmark_test.go`

**Interfaces:**
- Consumes: `platform.Accessibility`, `privacy.Filter`, content/observation repositories.
- Produces: `capture.Pipeline.Ingest(ctx context.Context, frame platform.WindowSnapshot) (storage.Observation, capture.Disposition, error)` and `capture.Service.Run(context.Context) error`.

- [ ] **Step 1: Write tests for exclusion-before-content-read, redaction-before-write/index, exact dedup, repeat sightings, changed frames, compression, transactional FTS consistency, bounded queue/backpressure, and duplicate concurrent events.**
- [ ] **Step 2: Run `go test ./internal/capture`; expect missing pipeline failures.**
- [ ] **Step 3: Implement normalization hash, zstd blob reuse, observation coalescing, contentless FTS write, recent-buffer notification, and idempotency keys inside one transaction.**
- [ ] **Step 4: Run race tests and benchmarks with 100 KB/400 KB frames; inspect database/WAL/logs to prove pre-redaction sentinel text is absent.**
- [ ] **Step 5: Commit with `feat: ingest deduplicated encrypted observations`.**

### Task 10: Build foreground sessions and passive time accounting

**Files:**
- Create: `internal/activity/tracker.go`, `recent.go`, `sessions.go`, `ledger.go`, `idle.go`, `rules.go`
- Test: `internal/activity/*_test.go`

**Interfaces:**
- Consumes: accepted capture observations and `platform.SystemState`.
- Produces: `Tracker.Observe(ObservationEvent)`, `Recent(ctx, minutes, limit)`, `ReadSession(ctx, app, from, to, limit)`, `TimeReport(ctx, Range)`, and time-rule CRUD.

- [ ] **Step 1: Write fake-clock tests for app switches, repeated sightings, sleep/lock, idle subtraction, clock jumps, midnight/timezone/DST, rule precedence, retroactive classification, and unclassified totals.**
- [ ] **Step 2: Run `go test ./internal/activity`; expect missing package failures.**
- [ ] **Step 3: Implement monotonic sessionization, bounded recent ring, persisted segments, hourly aggregation, deterministic rule matching, and reproducible report buckets.**
- [ ] **Step 4: Run race tests and assert report bucket totals exactly equal tracked seconds across DST fixtures.**
- [ ] **Step 5: Commit with `feat: add recent activity and time ledger`.**

### Task 11: Implement full-text, timeline, source, and semantic retrieval

**Files:**
- Create: `internal/search/service.go`, `fts.go`, `rank.go`, `timeline.go`, `sources.go`, `semantic.go`, `query.go`
- Test: `internal/search/*_test.go`, `benchmark_test.go`
- Create: `testdata/search/synthetic.jsonl`

**Interfaces:**
- Produces: `Search(ctx, search.Query) ([]search.Hit, error)`, `GetObservations(ctx, []string)`, `Timeline(ctx, Range)`, `WorkingMemory(ctx, limit)`, `Semantic(ctx, query, kinds, limit)`.

- [ ] **Step 1: Write tests for AND terms, quoting, Unicode, app/domain/time filters, recency/rank stability, bounded excerpts, source fetch, deleted content, semantic fallback, and result-size caps.**
- [ ] **Step 2: Run `go test ./internal/search`; expect failures.**
- [ ] **Step 3: Implement parameterized FTS5 queries, BM25+recency ranking, safe snippet generation, compressed-source reads, timeline/session queries, and an in-memory semantic index over encrypted chronicle/wiki embeddings only.**
- [ ] **Step 4: Run correctness tests and 100k/1M benchmarks; enforce p95 budgets from the spec.**
- [ ] **Step 5: Commit with `feat: search and retrieve local memory`.**

### Task 12: Route local and remote AI under explicit token/egress policy

**Files:**
- Create: `internal/ai/provider.go`, `types.go`, `registry.go`, `policy.go`, `tokens.go`, `cache.go`, `httpclient.go`
- Create: `internal/ai/openai/provider.go`, `ollama/provider.go`, `anthropic/provider.go`, `whisperexec/provider.go`
- Test: `internal/ai/**/*_test.go`, `testdata/ai/*.json`

**Interfaces:**
- Produces: `ai.Provider`, `Registry`, `Router.Chat`, `Router.Embed`, `Router.Transcribe`, `Policy.Authorize`, `Estimator.Count`, token ledger and prompt cache.

- [ ] **Step 1: Write fake-server contract tests for streaming/non-streaming, cancellation, malformed/oversize responses, retryable status, token usage, local discovery, cache keys, caps, key redaction, and provider capability mismatch.**
- [ ] **Step 2: Add security tests proving Strict Local Mode rejects non-loopback IPv4/IPv6, DNS rebinding/aliases, redirects, proxy attempts, and remote payloads before any request bytes are sent.**
- [ ] **Step 3: Run `go test ./internal/ai/...`; expect missing implementation failures.**
- [ ] **Step 4: Implement provider adapters, Keychain credential references, bounded clients, origin-locked redirects, model-aware estimates, per-feature profiles, local/remote labels, ledger, cache, and content-free audit.**
- [ ] **Step 5: Run race/contract/security tests and commit with `feat: add policy-governed AI providers`.**

### Task 13: Build incremental chronicles, wiki, facts, identity, and style

**Files:**
- Create: `internal/memory/chronicle/*.go`, `wiki/*.go`, `identity/*.go`, `style/*.go`, `jobs.go`, `prompts.go`
- Test: `internal/memory/**/*_test.go`, `testdata/memory/*.json`

**Interfaces:**
- Consumes: search, repositories, `ai.Router`.
- Produces: `Chronicles.CloseHour/Rollup`, `Wiki.ApplyEvidence`, `Facts.Supersede`, `Identity.Update`, `Style.ObserveOutcome`, and background job handlers.

- [ ] **Step 1: Write golden tests for salience selection, one-call-per-dirty-hour idempotency, child-summary rollups, source provenance, conservative entity merge, fact supersession, pinned user corrections, and accepted-versus-abandoned style evidence.**
- [ ] **Step 2: Run `go test ./internal/memory/...`; expect package failures.**
- [ ] **Step 3: Implement versioned prompt builders that accept typed evidence, bounded background jobs, deterministic no-AI fallback summaries, model-generated structured output validation, provenance, retry parking, and embeddings only for derived pages.**
- [ ] **Step 4: Run golden/race tests and assert 24 hour closures trigger at most 24 summary calls plus dirty rollups, never per-frame calls.**
- [ ] **Step 5: Commit with `feat: construct efficient derived memory`.**

### Task 14: Plan evidence and answer chat without trusting captured instructions

**Files:**
- Create: `internal/retrieval/classify.go`, `planner.go`, `evidence.go`, `budget.go`, `citations.go`
- Create: `internal/chat/service.go`, `stream.go`, `history.go`, `prompts.go`
- Test: `internal/retrieval/*_test.go`, `internal/chat/*_test.go`, `testdata/security/prompt_injection.json`

**Interfaces:**
- Produces: `retrieval.Planner.Plan(ctx, Query) (EvidencePlan, error)`, `Build(ctx, EvidencePlan, Budget) (EvidencePack, error)`, `chat.Service.Send(ctx, Request) (<-chan Event, error)`.

- [ ] **Step 1: Write routing tests for immediate/recent/time/exact/entity questions, exact-claim source opening, citation completeness, token-budget pruning, stale facts, no-result behavior, and cancellation.**
- [ ] **Step 2: Write adversarial tests where captured pages request policy changes, tool calls, secret disclosure, remote routing, or system-prompt override; assert these remain escaped evidence and no mutating/provider-policy call occurs.**
- [ ] **Step 3: Run retrieval/chat tests; expect missing implementation failures.**
- [ ] **Step 4: Implement deterministic classification first, typed evidence envelopes, budgeted source selection, explicit provider policy check, streamed answers, source citations, and persisted local history.**
- [ ] **Step 5: Run race/golden/security tests and commit with `feat: add grounded memory chat`.**

### Task 15: Expose bounded read-only MCP tools

**Files:**
- Create: `cmd/blur-mcp/main.go`, `internal/mcpserver/server.go`, `tools.go`, `schemas.go`, `client.go`, `instructions.go`
- Test: `internal/mcpserver/*_test.go`, `cmd/blur-mcp/main_test.go`

**Interfaces:**
- Consumes: `localapi.Client`.
- Produces: the tool set named in the design spec over `mcp.StdioTransport`, with structured outputs and read-only annotations.

- [ ] **Step 1: Write an in-process MCP client test for discovery, every tool schema, bounded defaults/maxima, daemon unavailable errors, cancellation, stderr-only logs, frame limit, and protocol compatibility.**
- [ ] **Step 2: Run `go test ./internal/mcpserver ./cmd/blur-mcp`; expect failure.**
- [ ] **Step 3: Implement tools using `github.com/modelcontextprotocol/go-sdk/mcp`, local API calls only, evidence/summary labels, source IDs, and no mutation/resource/prompt surface.**
- [ ] **Step 4: Run race tests and a JSON-RPC stdio smoke test; verify stdout contains protocol frames only and no unbounded raw result.**
- [ ] **Step 5: Commit with `feat: expose memory through read-only MCP`.**

### Task 16: Build the Wails shell, tray, onboarding, and settings

**Files:**
- Create: `cmd/blur/main.go`, `internal/app/app.go`, `bindings.go`, `windows.go`, `tray.go`
- Create: `frontend/src/app.html`, `main.ts`, `App.svelte`, `lib/api.ts`, `lib/state/*.ts`, `routes/onboarding/*`, `routes/settings/*`, `styles/*`
- Create: `build/darwin/Info.plist`, `Entitlements.plist`
- Test: `internal/app/*_test.go`, `frontend/src/**/*.test.ts`

**Interfaces:**
- Consumes: local API client and platform permission/login interfaces.
- Produces: Wails services `StatusService`, `SettingsService`, `PermissionService`, `ProviderService`; memory bar, dashboard, onboarding, and tray windows.

- [ ] **Step 1: Write Go service tests and frontend component tests for first run, permission denied/revoked, no provider, local provider, capture pause, strict-local status, exclusions, restart-required settings, and accessible keyboard navigation.**
- [ ] **Step 2: Run Go/frontend tests; expect missing shell failures.**
- [ ] **Step 3: Implement Wails 3 app lifecycle, system tray, top/edge panel and dashboard windows, generated bindings, original visual tokens, onboarding, settings pages, provider setup, and live status events.**
- [ ] **Step 4: Run `wails3 build` (development signing), frontend checks, and automated accessibility audit; visually verify at 100% and 150% text scale.**
- [ ] **Step 5: Commit with `feat: add Blur desktop shell and settings`.**

### Task 17: Add searchable memory, timeline, time, and evidence UI

**Files:**
- Create: `frontend/src/routes/chat/*`, `search/*`, `timeline/*`, `memory/*`, `time/*`, `tokens/*`
- Create: `frontend/src/components/EvidenceDrawer.svelte`, `ProviderBadge.svelte`, `TokenDisclosure.svelte`, `VirtualList.svelte`
- Test: adjacent `*.test.ts` and `frontend/e2e/memory.spec.ts`

**Interfaces:**
- Consumes: search/chat/memory/time/token Wails services.
- Produces: complete user UI for chat streams, evidence/source inspection, wiki browsing, recent/timeline search, time reports/rules, and token/egress ledger.

- [ ] **Step 1: Write component/e2e tests for empty/loading/error/large states, stream cancel/retry, citations opening source text, filters, virtualized 100k-result simulation, token disclosure, and local/remote badges.**
- [ ] **Step 2: Run frontend tests; expect missing components.**
- [ ] **Step 3: Implement routes/components with bounded pagination, keyboard navigation, evidence-versus-summary labeling, and no raw HTML rendering from captured/model content.**
- [ ] **Step 4: Run unit/e2e/accessibility tests and inspect CSP; expect no unsafe inline/eval and no layout clipping.**
- [ ] **Step 5: Commit with `feat: add memory and evidence dashboard`.**

### Task 18: Register global shortcuts and safely discover/edit focused text

**Files:**
- Create: `internal/platform/macos/hotkeys_darwin.go`, `hotkeys_darwin.m`, `text_darwin.go`, `text_darwin.m`, `geometry_darwin.go`
- Create: `internal/inline/target.go`, `fingerprint.go`, `replacement.go`, `gestures.go`
- Test: `internal/inline/*_test.go`, tagged macOS fixture tests

**Interfaces:**
- Implements: `platform.Hotkeys`; extends Accessibility target/replace implementation.
- Produces: `inline.TargetService.Snapshot(ctx) TargetSnapshot`, `Validate(ctx, TargetFingerprint) error`, `Replace(ctx, Replacement) error`, and gesture events for tap/hold/double-tap/chord.

- [ ] **Step 1: Write fake-clock gesture tests and fixture tests for selected/plain/rich text, empty fields, layout combos, Japanese default, focus/app/selection/text changes, caret geometry, and undo grouping.**
- [ ] **Step 2: Run unit/integration tests; expect missing implementation failures.**
- [ ] **Step 3: Implement Carbon/Wails global shortcuts plus AX value/selection reads and compare-before-swap replacement; do not use clipboard unless a target requires the explicit restored-clipboard fallback.**
- [ ] **Step 4: Run fixture tests proving every mid-generation target mutation returns `ErrTargetChanged` and leaves the field untouched.**
- [ ] **Step 5: Commit with `feat: safely target text fields from global shortcuts`.**

### Task 19: Implement inline rewrite and iterative edit mode

**Files:**
- Create: `internal/inline/service.go`, `context.go`, `rewrite.go`, `edit.go`, `outcomes.go`, `prompts.go`
- Create: `frontend/src/overlays/InlineIndicator.svelte`, `EditBar.svelte`
- Test: `internal/inline/service_test.go`, `frontend/src/overlays/*.test.ts`, `frontend/e2e/inline.spec.ts`

**Interfaces:**
- Consumes: target service, retrieval planner, AI router, style service.
- Produces: `inline.Service.Activate`, `SubmitEdit`, `Cancel`, progress events, rewrite outcome recording.

- [ ] **Step 1: Write tests for selected/non-empty/empty/`/b` modes, bounded surrounding context, token cap, provider unavailable, Escape cancel, late response, repeated edit, output validation, and target-change no-write behavior.**
- [ ] **Step 2: Run inline/frontend tests; expect failures.**
- [ ] **Step 3: Implement typed prompt/context building, cancelable streaming buffer, near-caret original overlays, final compare-before-swap delivery, local undo, and accepted/edited/abandoned outcome observation.**
- [ ] **Step 4: Run end-to-end fixture tests across native, WebKit, and Electron text fields; verify no field mutation before a complete valid result.**
- [ ] **Step 5: Commit with `feat: add inline rewrite and edit mode`.**

### Task 20: Add push-to-talk dictation

**Files:**
- Create: `internal/platform/macos/audio_darwin.go`, `audio_darwin.m`
- Create: `internal/dictation/service.go`, `audio.go`, `transcribe.go`, `cleanup.go`
- Create: `frontend/src/overlays/DictationIndicator.svelte`
- Test: `internal/dictation/*_test.go`, audio fixture tests, frontend overlay tests

**Interfaces:**
- Consumes: `platform.AudioRecorder`, `ai.Router.Transcribe`, inline target service.
- Produces: hold/release and latch dictation sessions; local whisper executable/OpenAI-compatible transcription; safe insertion at caret/edit bar.

- [ ] **Step 1: Write tests for permission denial, no device, hold/release, double-tap latch, silence, cancel, max duration/bytes, local/remote policy, temp cleanup, transcription error, and changed target.**
- [ ] **Step 2: Run dictation tests; expect failures.**
- [ ] **Step 3: Implement bounded PCM capture with memory-first buffering, encrypted temporary fallback, provider routing, cleanup on every path, and compare-before-insert delivery.**
- [ ] **Step 4: Run unit/fixture tests and scan temp directories/logs for audio/transcript sentinels after success, error, cancel, and crash simulations.**
- [ ] **Step 5: Commit with `feat: add private push-to-talk dictation`.**

### Task 21: Add reminders, briefs, open loops, workflows, and notifications

**Files:**
- Create: `internal/proactive/scheduler.go`, `rules.go`, `openloops.go`, `briefs.go`, `nudges.go`, `workflows.go`, `quiet.go`
- Create: `frontend/src/routes/reminders/*`, `workflows/*`, `components/Inbox.svelte`
- Test: `internal/proactive/*_test.go`, frontend tests

**Interfaces:**
- Consumes: repositories, memory/retrieval, AI router, `platform.Notifier` and system state.
- Produces: one-off/recurring/interval rules, optional morning/evening briefs, observed open loops, workflow proposals, notification inbox/history.

- [ ] **Step 1: Write fake-clock tests for timezone/DST, sleep/wake windows, dedupe, quiet hours, rate caps, disabled notifications, stale/unknown evidence, recurrence, and “read is not promised/sent” semantics.**
- [ ] **Step 2: Run proactive/frontend tests; expect failures.**
- [ ] **Step 3: Implement durable next-fire scheduling, source-linked brief jobs, conservative open-loop states, workflow proposals, inbox delivery, and structured UI controls.**
- [ ] **Step 4: Run a 90-day accelerated simulation and assert no duplicate fire, bounded AI jobs, and correct quiet/wake behavior.**
- [ ] **Step 5: Commit with `feat: add proactive memory and schedules`.**

### Task 22: Implement retention, encrypted export/import, deletion, and compatibility import

**Files:**
- Create: `internal/lifecycle/retention.go`, `export.go`, `import.go`, `rekey.go`, `delete.go`, `manifest.go`, `goldfish.go`
- Create: `cmd/blurctl/export.go`, `import.go`, `delete.go`, `rekey.go`
- Create: `frontend/src/routes/privacy/DataControls.svelte`
- Test: `internal/lifecycle/*_test.go`, `testdata/import/goldfish_minimal.sql`

**Interfaces:**
- Produces: restartable lifecycle jobs with `JobID`, progress, dry-run reports, content-free audit, and explicit confirmation tokens.

- [ ] **Step 1: Write tests for password-encrypted signed export, wrong password, truncated/tampered archive, idempotent import, read-only source, Goldfish table/version variation, re-redaction, skip reports, retention provenance cleanup, targeted deletion, delete-all key destruction, and key rotation.**
- [ ] **Step 2: Add fault-injection tests that kill every job between copy/fsync/verify/swap/checkpoint steps; assert recovery selects one valid encrypted state and leaves no plaintext file.**
- [ ] **Step 3: Run lifecycle tests; expect missing implementation failures.**
- [ ] **Step 4: Implement streaming encrypted archives, manifest authentication, staged import/rekey, read-only compatibility mapping, secure-delete/checkpoint/vacuum, and delete-all key-first destruction.**
- [ ] **Step 5: Run race/fault tests, scan artifacts for sentinels, and commit with `feat: add private data lifecycle controls`.**

### Task 23: Package login startup, signed updates, and recovery

**Files:**
- Create: `internal/platform/macos/loginitem_darwin.go`, `loginitem_darwin.m`
- Create: `internal/update/service.go`, `manifest.go`, `verify.go`
- Create: `packaging/macos/build.sh`, `sign.sh`, `notarize.sh`, `release.sh`
- Create: `build/darwin/Info.plist`, `Entitlements.plist`, `PrivacyInfo.xcprivacy`
- Test: `internal/update/*_test.go`, `packaging/macos/smoke_test.sh`

**Interfaces:**
- Produces: optional login item, Ed25519-signed update manifest/artifacts, staged verified install, rollback marker, crash-loop safe mode, and universal app bundle containing all three binaries.

- [ ] **Step 1: Write tests for signature verification, downgrade refusal, wrong platform/hash, partial download, rollback, crash-loop detection, permission continuity, and disabled updates.**
- [ ] **Step 2: Run update/package tests; expect failures.**
- [ ] **Step 3: Implement login item, updater, recovery/safe mode, universal builds, least-privilege entitlements, signing/notarization scripts, and reproducible version injection.**
- [ ] **Step 4: Build/install a development-signed bundle in a temporary Applications directory; verify launch, daemon, MCP, update rejection, and uninstall instructions.**
- [ ] **Step 5: Commit with `build: package and update Blur safely`.**

### Task 24: Harden, benchmark, document, and release the beta

**Files:**
- Create: `docs/architecture.md`, `docs/threat-model.md`, `docs/privacy.md`, `docs/providers.md`, `docs/mcp.md`, `docs/import.md`, `docs/development.md`, `docs/release.md`
- Create: `test/bench/*`, `test/e2e/*`, `test/security/*`
- Modify: `.github/workflows/ci.yml`, `README.md`, `SECURITY.md`

**Interfaces:**
- Consumes: complete product.
- Produces: benchmark reports, threat-model traceability, SBOM/provenance, user docs, contributor docs, and a beta release candidate.

- [ ] **Step 1: Add end-to-end tests for every primary workflow and explicit regression tests for all five Review Focus items.**
- [ ] **Step 2: Generate synthetic 100k/1M corpora and run CPU/memory/disk/search/capture/activation benchmarks; fail CI when budgets regress beyond the documented tolerance.**
- [ ] **Step 3: Run race detector, fuzz smoke, `go vet`, `govulncheck`, frontend tests/audit, secret scan, license scan, SBOM generation, artifact signature verification, CSP check, and macOS accessibility audit.**
- [ ] **Step 4: Document architecture, privacy/egress behavior, limitations, provider setup, MCP, data lifecycle, reproducible builds, security reporting, and clean-room provenance; commission an independent security review and resolve every high-severity issue.**
- [ ] **Step 5: Build the signed release candidate, run a fresh-machine smoke test plus upgrade/rollback/uninstall test, and commit with `release: prepare Blur public beta`.**

## Self-review results

- **Spec coverage:** Every goal maps to Tasks 3–24; every non-goal is preserved by the global constraints. Windows/Linux remain interface-ready but unscheduled.
- **Step scan:** Each task owns one reviewable deliverable and follows fail/implement/pass/commit. Platform integration is separated from pure domain behavior.
- **Type consistency:** Platform contracts originate in Task 2; storage in Tasks 3–4; local API in Task 6; downstream tasks consume those interfaces rather than bypassing them.
- **Review Focus:** Each of the five high-risk cases has an explicit test step in its owning task and a final end-to-end regression in Task 24.
- **Proportion:** The plan fixes architecture, interfaces, files, tests, and acceptance commands without transcribing implementation bodies.
