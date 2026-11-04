# Blur: Clean-Room Personal Memory Design

**Status:** Approved cross-platform foundation; implementation in progress  
**Date:** 2026-11-04  
**Working name:** Blur  
**License:** Apache-2.0  
**Tier 1 targets:** macOS 13+, Windows 11, and tested GNOME/KDE sessions on Linux

## Intent

Blur is an open-source, local-first memory and writing assistant for a person's
computer. It observes text in the foreground application through the native
accessibility service available on each supported desktop, stores the resulting memory locally, makes it searchable,
and uses a user-selected local or remote AI provider for chat, writing,
dictation, and background organization.

This is a clean-room reimplementation based on public product documentation
and observable behavior of an installed application. It must not reuse the
original application's source code, prompts, branding, icons, animation, or
other creative assets. Compatibility work is limited to public interfaces and
user-owned local data that the user explicitly chooses to import.

The project exists because the useful part of this product category—persistent
personal context—should be inspectable, self-hostable, provider-neutral, and
safe to run over sensitive work.

## Product principles

1. **Local is the default boundary.** Capture, storage, retrieval, indexing,
   scheduling, and time accounting work without a cloud account.
2. **No screenshots.** Capture accessible text, metadata, and focused-control
   breadcrumbs only. Never request Screen Recording permission.
3. **AI is optional and replaceable.** Deterministic capture and keyword search
   remain useful without a model. Ollama and OpenAI-compatible local endpoints
   are first-class; remote providers are explicit choices.
4. **No invisible egress.** Every feature has an AI routing policy. The UI can
   show what context and approximate token count will leave the device.
5. **Captured text is untrusted data.** Instructions found in a page, message,
   or document never become control-plane instructions.
6. **Spend context once.** Deduplicate frames, retrieve before generating,
   summarize incrementally, roll summaries upward, and cache deterministic
   model work.
7. **Evidence before synthesis.** Summaries and wiki pages retain provenance;
   exact answers open the source observation.
8. **The user can stop and erase it.** Pause, exclusions, retention, export,
   targeted deletion, and delete-all are core features.

## Goals

- Capture foreground accessible text and useful metadata with low CPU, memory,
  battery, and disk overhead.
- Provide recent-activity, full-text, timeline, entity/wiki, and time-spent
  retrieval with citations to original observations.
- Provide an edge-of-screen chat panel, settings/dashboard, menu bar controls,
  and an optional original desktop companion.
- Write or rewrite text in the focused field from a configurable global key,
  including selection-only rewriting and iterative edit mode.
- Support push-to-talk dictation with local or configured remote transcription.
- Learn an editable identity profile and writing style from accepted edits.
- Build hierarchical hour/day/week/month/year chronicles, an entity wiki,
  facts, open loops, reminders, workflows, and proactive briefs.
- Expose memory through a local, read-only-by-default MCP stdio server.
- Support Ollama, LM Studio, llama.cpp, other OpenAI-compatible endpoints,
  OpenAI, and Anthropic without coupling domain code to a provider.
- Ship as a signed/notarizable macOS app with equivalent signed Windows
  packages and reproducible Linux packages, deterministic builds, SBOMs,
  security checks, and complete self-hosting documentation. Release timing may
  be staggered, but every adapter and its conformance fixtures remain in-tree.

## Non-goals

- Capturing pixels, OCRing the screen, recording video, or recording keystrokes.
- Syncing a raw memory database to a Blur-operated cloud.
- Sending messages, modifying documents, or taking external actions on the
  user's behalf in the first release.
- Reproducing the original brand, fish artwork, proprietary prompts, account,
  billing, analytics, or hosted AI gateway.
- Claiming that pattern-based redaction is perfect.
- Pixel-identical overlays or edge placement across compositors and window
  managers. Workflow parity is required; visual placement may degrade openly.
- Automating elevated, secure-desktop, locked-session, password, or otherwise
  protected targets.
- Silently falling back to clipboard mutation, synthetic input, screenshots,
  OCR, or a remote AI provider when a capability is unavailable.

## Anti-goals

The build is a failure even if it appears functional when it:

- uploads captured context without a visible, feature-specific policy;
- stores raw memory, vector files, temporary databases, or logs unencrypted;
- sends every frame to a model or embeds every captured frame by default;
- lets captured content alter system prompts, tool permissions, provider
  routing, or retention settings;
- requires an online account to search or export local data;
- uses a broad Accessibility tree crawl when event-driven or bounded reads are
  sufficient;
- exposes a localhost service without peer checks and an install secret;
- silently edits third-party AI-client configuration;
- makes deletion claims that SSD and SQLite behavior cannot support.

## Clean-room evidence

Public documentation establishes the product category and user-visible
workflows: foreground Accessibility text capture, local memory, inline writing,
edit mode, dictation, chat, schedules, wiki pages, time accounting, and MCP.
The installed bundle was inspected only for observable metadata, process
boundaries, public local protocol shapes, and database schema names. No binary
decompilation or proprietary source extraction is part of the implementation.

Observed scale on the local installation—used only for capacity planning—was
about 92,000 observations, a 2.3 GB SQLite store, and a 108 MB HNSW file. Blur
must be benchmarked at 100,000 and 1,000,000 observations.

References:

- https://goldfish.sh/
- https://docs.goldfish.sh/
- https://docs.goldfish.sh/privacy/
- https://docs.goldfish.sh/guides/the-goldfish-key/
- https://docs.goldfish.sh/guides/edit-mode/
- https://docs.goldfish.sh/guides/voice-dictation/
- https://docs.goldfish.sh/guides/chat/
- https://docs.goldfish.sh/guides/ai-clients/
- https://docs.goldfish.sh/reference/settings/

## Primary user workflows

### Capture and recall

After granting Accessibility permission, the user works normally. Blur records
bounded text from the foreground window, the application identity, title,
browser URL when exposed, domain, focused-control role/name, and up to three
accessible ancestors. Repeated frames update an observation rather than create
copies. The user can ask “what was I working on yesterday?” or search for a
specific phrase and open the source observation.

### Inline writing

A clean tap of the configured activation key operates on the focused editable
field:

- selected text: rewrite only the selection;
- non-empty unselected field: treat its contents as a rough draft/instruction;
- empty field: infer a reply or draft from bounded surrounding context;
- text beginning `/b`: answer the instruction directly into the field.

A small original progress indicator appears near the caret. The field is not
changed until a complete answer is ready. Escape cancels. A hold or double tap
opens iterative edit mode, where typed or spoken instructions apply to the
current draft. Every replacement validates that focus and original text have
not changed before injection.

### Chat and memory

A slim “memory bar” lives at a chosen screen edge or corner. Hover/click/hotkey
opens a resizable chat. Hover is instant and model-free: it reads a locally
materialized “Today” snapshot containing recent changes, active subjects,
confirmed and inferred todos, open loops, and a time split. Click expands the
snapshot into a cited daily recap with controls to confirm, correct, dismiss,
or promote suggestions. Answers cite source observations. The dashboard
exposes identity, a browsable knowledge library, wiki pages, timeline, time
reports, reminders, provider controls, privacy controls, MCP setup,
diagnostics, and token usage.

The knowledge library groups People, Projects, Topics, Places, and Workflows.
Each page has a current summary, evidence-backed facts, open loops, related
entities, activity history, and exact sources. It is a reversible materialized
view over the evidence/derivation store, not a second source of truth. Exact
search and browsing require no AI; models may propose aliases, merges, or
summaries but their output remains visibly untrusted until corrected or
confirmed.

### Dictation

Holding the dictation key records microphone audio; releasing it transcribes
and inserts cleaned text at the focused caret. Double tap latches recording.
The default path is a local configured transcription endpoint or executable.
Remote transcription requires an explicit routing policy and shows as remote.

### External AI clients

`blur-mcp` runs over stdio and offers read-only memory tools. It talks to the
daemon through a user-only Unix-domain socket. Client configuration is manual
or emitted as copyable commands; Blur does not edit unrelated configuration
files automatically.

## System architecture

### Authoritative cross-platform amendment

This section supersedes conflicting macOS-only process, storage, transport,
provider, retrieval, and platform choices elsewhere in this document. Those
older sections remain as clean-room product research until they are folded into
the implementation-level design.

Blur is split into five least-authority components:

1. A signed **platform host** owns desktop permissions, probes the focused
   target, performs a bounded read only after policy approval, rejects secure
   targets, and redacts before emitting a sanitized observation. It owns no
   database or provider credential.
2. **`blur-core`** owns the wrapped data key, encrypted store, deterministic
   policy, search, activity accounting, jobs, and immutable derivation ledger.
3. Optional **`blur-provider`** owns remote-provider credentials and is the
   only component permitted to send memory-derived payloads off-device. Strict
   Local Mode omits this process entirely.
4. The disposable **Wails renderer** receives a narrow view facade and never a
   database key or raw capture capability.
5. **`blur-mcp`** and **`blurctl`** are stateless, keyless clients with scoped,
   revocable grants.

The capture contract is enforced as a staged type flow:

```text
ProbeFrontmost -> DecideProbe -> ReadBounded -> SanitizeFrame -> Ingest
```

Raw frames are package-private and non-serializable. Only
`SanitizedObservation` may cross IPC or reach storage. The portable core uses
narrow capabilities (`CaptureSource`, `TextTargets`, `GestureHotkeys`,
`SurfaceManager`, `AudioRecorder`, `SecretStore`, `LocalTransport`,
`PrivatePath`, `Autostart`, and `SessionState`) rather than a monolithic
Accessibility interface. Every capability reports a structured state such as
`available`, `permission_required`, `unsupported_target`, `secure_target`,
`target_elevated`, `portal_missing`, `temporarily_unresponsive`, or
`user_disabled`.

Platform adapters use AXUIElement on macOS, UI Automation on Windows, and
AT-SPI2 on Linux. Wayland-specific global shortcuts use the portal when
available. Missing global geometry, tray hosting, injection, or edge placement
is reported as a degraded capability and never triggers a hidden fallback.
Writing uses a revalidated `TargetLease` and is guarded best effort, not an
atomic compare-and-set into another process.

Local clients receive per-client grants such as `capture:ingest`, `ui:read`,
`ui:mutate`, `mcp:bounded-read`, and `cli:admin`. macOS and Linux use AF_UNIX
with peer evidence; Windows uses named pipes with an explicit current-user ACL
and connected-token validation. Peer identity supplements rather than replaces
the grant.

Storage is accessed only through `EncryptedStore`. A random data-encryption key
is wrapped by Keychain, DPAPI CurrentUser, or Secret Service/passphrase
fallback. The in-process backend must be cgo-free and authenticated at the
record or page boundary. SQLCipher remains a security/performance reference
and may only run as an isolated helper unless the no-cgo rule changes;
ncruces Adiantum remains experimental because it provides confidentiality but
not authentication. Pure-Go candidates must pass identical benchmark, crash,
tamper, key-loss, and packaging gates. Only a typed `NotFound` secret state may
create a new key.

AI ports are separated into `Generator`, `Embedder`, `Transcriber`, `Reranker`,
and `TokenCounter`, each with independent admission policy. Local-to-remote
fallback is forbidden. Retrieval uses raw deduplicated FTS, deterministic
session/topic units with optional local embeddings, and generated/user-edited
chronicles and entities. Rank lists are fused deterministically, diversified,
and expanded back to exact source leaves before token packing. Generated
artifacts are immutable derivations with source hashes and states (`building`,
`active`, `stale`, `quarantined`, `failed`, `superseded`); deletion or new
secret detection immediately quarantines descendants.

This architecture defines parity as equivalent capture/search/chat/pause/
evidence workflows plus honest runtime capability negotiation—not identical
desktop mechanics on every OS.

All shipped Go binaries and dependencies are cgo-free. Release and cross-build
gates run with `CGO_ENABLED=0`, and CI rejects packages containing Cgo files or
`import "C"`. Native access uses the standard library where practical, then a
narrowly audited pure-Go binding or system protocol; an isolated signed native
helper is a last-resort process boundary, never a cgo link into the core.
Structured logging uses `log/slog` behind a content-free event vocabulary. No
logging dependency is added without a measured need.

```text
┌──────────────────────── Blur.app / Wails 3 ────────────────────────┐
│ memory bar · chat · dashboard · tray · overlays · onboarding       │
│ Go service bindings + Svelte/TypeScript UI                          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ authenticated Unix socket
┌──────────────────────── blur-daemon ────────────────────────────────┐
│ AX capture → privacy filter → dedup/content store → activity ledger │
│                       ↓                        ↓                     │
│             FTS/recent/timeline          background jobs            │
│                       ↓                        ↓                     │
│              retrieval planner ← chronicles/wiki/style/open loops   │
│                       ↓                                              │
│          provider router → local or allowed remote model             │
└───────────────┬──────────────────────────┬───────────────────────────┘
                │                          │
      encrypted SQLite              provider endpoints
      Keychain-held key             (deny by default)
                │
┌──────── blur-mcp ────────┐   ┌──────── blurctl ─────────┐
│ official Go MCP SDK      │   │ status/export/import/etc │
│ stdio, bounded results   │   │ explicit local commands │
└──────────────────────────┘   └──────────────────────────┘
```

### Process model

- `blur`: Wails GUI/menu-bar process. It contains no database key and reaches
  user data through the daemon API. It may invoke narrow macOS UI shims.
- `blur-daemon`: single writer for capture, encrypted storage, indexing,
  scheduler, retrieval, provider calls, and local API.
- `blur-mcp`: stateless stdio adapter using the official MCP Go SDK. It has no
  database access and exits when the client disconnects.
- `blurctl`: administrative CLI using the same authenticated API. Destructive
  commands require interactive confirmation unless `--yes` is explicit.

The bundle contains cgo-free Go binaries. Native access is through pure-Go
system calls/protocols where practical. If a platform API cannot be reached
safely that way, a minimal signed helper may be isolated behind authenticated
IPC, with no database/provider key and no raw-data persistence. There is no
Swift or Rust application layer in the portable core.

### Technology choices

- Go 1.27.3.
- Wails 3.1 or newer is only a shell candidate behind `DesktopShell`, using its
  pure-Go build mode with `CGO_ENABLED=0`. Its currently experimental macOS/
  Linux pure-Go backends must pass signed fixture, renderer recovery, navigation
  isolation, and packaging gates before adoption; framework types never enter
  domain packages.
- Svelte 5, TypeScript, and Vite for a small reactive UI bundle.
- An `EncryptedStore` chosen only after cgo-free authenticated candidates pass
  packaging, tamper, crash, key-loss, durability, and performance gates.
  SQLCipher is a reference/out-of-process option, not an in-process dependency;
  Adiantum is confidentiality-only and cannot be the secure default unchanged.
- `github.com/klauspost/compress/zstd` for deduplicated content blobs.
- `github.com/modelcontextprotocol/go-sdk` v1.8 or newer compatible minor.
- Standard-library `log/slog`, `net/http`, `crypto`, and `testing` wherever
  practical; dependencies are added only behind owned interfaces.

## Domain boundaries

### Platform

`internal/platform` defines narrow interfaces:

```go
type Accessibility interface {
    Permission(context.Context, bool) (PermissionState, error)
    Frontmost(context.Context) (WindowSnapshot, error)
    Observe(context.Context) (<-chan AXEvent, error)
    EditableTarget(context.Context) (TextTarget, error)
    Replace(context.Context, Replacement) error
}

type SecretStore interface {
    Get(context.Context, string) ([]byte, error)
    Put(context.Context, string, []byte) error
    Delete(context.Context, string) error
}
```

Other platform interfaces cover hotkeys, screen geometry, idle/lock state,
audio recording, notifications, login items, and filesystem protection.

### Capture

Capture is event-driven with a bounded fallback sampler. It reads only the
frontmost window and enforces byte, node, depth, and duration limits. It skips
secure text fields before reading values. Browser URL extraction uses exposed
Accessibility attributes only.

The capture pipeline is:

1. preflight exclusion by bundle ID, app name, window title, private mode, and
   known-sensitive categories;
2. bounded Accessibility read;
3. normalization and focused breadcrumb extraction;
4. local redaction;
5. SHA-256 content identity and deduplication;
6. zstd compression and encrypted transaction;
7. contentless FTS indexing and recent-buffer update;
8. activity/time session update.

No model call occurs in this pipeline.

### Privacy filter

The filter is deterministic, versioned, and runs before persistence. It:

- refuses password/secure controls;
- masks private-key blocks, authorization headers, database URLs, common API
  tokens and keys, URL credentials/query secrets, card numbers that pass Luhn,
  labeled security codes, IBANs that pass checksum, and US SSNs;
- preserves placeholder type without preserving length or prefix;
- records only aggregate rule counters, never matched values;
- can reprocess stored data when filter versions change.

Default exclusions include password managers, financial apps/sites, sign-in
and checkout pages, and private/incognito windows. Users and MDM profiles can
add immutable exclusions.

### Storage

One encrypted SQLite database is the source of truth. The 32-byte random key is
created by Keychain and never written to config or logs. Adiantum encrypts the
main database, WAL, journals, and temp files; `temp_store=memory` is mandatory.
The data directory is mode 0700 and files are mode 0600.

Core records:

- `content_blobs`: SHA-256, zstd bytes, original size, redaction version;
- `observations`: content reference, app/window/URL/domain, focus breadcrumb,
  first/last seen, duration, sightings;
- `activity_segments`: ordered foreground sessions and durations;
- contentless `observations_fts`: terms keyed to observation/content IDs;
- `chronicles` and `chronicle_sources`: hierarchical summaries/provenance;
- `wiki_pages`, `wiki_facts`, `wiki_links`, and optional compressed embeddings;
- `identity_profiles`, `style_rules`, and accepted/rejected rewrite evidence;
- `open_loops`, `todos`, `schedules`, `notifications`, and `workflows`;
- `time_entries` and `time_rules`;
- `chat_threads`, `chat_turns`, and `rewrite_runs`;
- `ai_runs`, `token_ledger`, `prompt_cache`, and content-free audit events;
- `settings`, `exclusions`, and `schema_migrations`.

SQLite runs foreign keys, WAL, busy timeout, secure delete, bounded connection
pooling, and periodic integrity checks. Backups are encrypted and authenticated.
Key rotation uses a crash-safe copy/verify/swap process, not in-place rekeying.

### Retrieval

Retrieval uses the cheapest sufficient path:

1. immediate context: in-memory activity buffer;
2. recent hours: working-memory query over observations/activity;
3. broad time question: precomputed hierarchical chronicle;
4. exact/topic question: FTS5 with app/domain/time filters;
5. entity question: wiki aliases/facts/links, then source observations;
6. semantic fallback: embeddings over chronicles/wiki pages, not raw frames;
7. exact claims: fetch full source observations before answering.

Every result carries a stable source ID and timestamps. Context packs contain
typed evidence blocks, not concatenated raw pages. Result counts and bytes are
hard-bounded.

### AI provider layer

```go
type Provider interface {
    ID() string
    Capabilities(context.Context) (Capabilities, error)
    Chat(context.Context, ChatRequest) (ChatStream, error)
    Embed(context.Context, EmbedRequest) ([][]float32, error)
    Transcribe(context.Context, TranscribeRequest) (Transcript, error)
}
```

Built-in adapters:

- Ollama native discovery plus its OpenAI-compatible API;
- generic OpenAI-compatible API for llama.cpp, LM Studio, vLLM, LocalAI,
  OpenAI, Azure-style gateways, and user endpoints;
- native Anthropic Messages API;
- local executable adapter for whisper.cpp-compatible transcription.

API keys live in Keychain. Provider configuration stores only identifiers,
base URLs, model names, and non-secret options. Redirects across origins are
rejected. Remote providers require TLS with normal certificate validation.

Each feature maps to an `AIProfile` containing provider/model, local/remote
classification, input/output limits, timeout, retry policy, and whether
background use is allowed. Strict Local Mode is a global network-layer deny for
all AI payloads to non-loopback hosts.

### Token and model efficiency

- Capture never invokes AI.
- Frame deduplication prevents repeated context from entering retrieval.
- Background work is queued by dirty period/entity and coalesced.
- At most one hour-summary generation occurs per closed hour; retries are
  bounded and use the same idempotency key.
- Day/week/month/year summaries roll up child summaries instead of raw frames.
- Wiki updates receive changed facts/evidence, not the full wiki.
- Prompt boilerplate is versioned and provider cache hints are used when
  supported.
- Response/prompt cache keys include provider, model, prompt version, context
  hashes, and privacy-policy version.
- Model-aware token estimates are checked before egress. Context planning drops
  low-scoring evidence, never truncates secrets into visibility, and reports
  omitted evidence count.
- Default hard caps: inline 4k input/1k output, chat 12k/2k, chronicle 8k/1k,
  wiki update 6k/1k, classification 2k/400.
- Remote background AI is off by default. Local background AI is opt-in during
  provider setup because it consumes compute and battery.
- The dashboard exposes requests, input/output tokens, cache hits, latency,
  estimated cost, provider, model, feature, and local/remote status.

### Memory construction

Hour chronicles use deterministic salience selection followed by one model call
when enabled. Higher levels roll up completed children. Each summary records
source IDs, model, prompt version, token use, and generation time.

Wiki extraction proposes typed people, projects, topics, tools, and
organizations from closed periods. Entity resolution is conservative and
alias-aware. Facts are dated, source-linked, and can be superseded rather than
silently overwritten. User corrections are pinned above generated text.

Identity and style are editable. Style learning uses only text the user kept,
sent, or explicitly accepted; generated-but-abandoned outputs do not become
positive examples. Rules require repeated evidence and expose their sources.

### Chat and proactive work

The chat router classifies a request locally and constructs an evidence plan.
It supports memory questions, drafting, todos, reminders, recurring prompts,
time reports, exclusions, and settings help. Destructive configuration changes
require explicit confirmation in the Blur UI.

Schedules are stored as structured rules with timezone and next-fire time.
Morning/evening briefs and contextual nudges are optional, rate-limited,
deduplicated, quiet-hour aware, and source-linked. A sleeping machine catches
up only inside a bounded relevance window.

Open loops and promises remain “observed” until user-confirmed. Reading a
request does not mean the user promised it; generated drafts do not mean an
action was sent.

### MCP

The stdio server uses the official Go SDK and negotiates current and compatible
protocol revisions. It exposes bounded, read-only tools:

- `search_memory`
- `get_recent_activity`
- `get_working_memory`
- `get_chronicle`
- `get_observations`
- `read_session`
- `search_wiki`, `get_wiki_page`, `list_wiki`
- `get_memory_facts`
- `get_work_context`, `get_attention_context`, `get_open_loops`
- `get_nudge_history`
- `get_time_report`, `list_time_rules`

Tool output distinguishes raw evidence from generated summaries. Every schema
has maximum limits. JSON-RPC frame size, nesting, tool concurrency, and daemon
timeouts are bounded. Logs go to stderr and contain no memory content.

### Local API

The daemon listens on a Unix-domain socket in the user's runtime directory,
mode 0600. The server verifies peer UID where supported and requires a random
install token stored in Keychain. Request bodies, response bodies, deadlines,
and concurrency are bounded. State-changing routes require a nonce and origin
binding issued to the GUI/CLI session. There is no TCP listener by default.

## Security and privacy threat model

### Protected assets

- captured text, URLs, titles, focus breadcrumbs, and activity timestamps;
- inferred identity, style, relationships, projects, facts, and open loops;
- chat, rewrite, and dictation content;
- provider credentials, local API credentials, and encryption keys.

### Threats and controls

- **Lost/stolen disk:** encrypted SQLite and backups; key in login Keychain;
  file permissions; no plaintext temp files.
- **Other local user/process:** user-only socket and files, peer UID, install
  token, no unauthenticated localhost HTTP.
- **Prompt injection in captured content:** typed evidence envelopes, fixed
  system policy, no instruction promotion, no action tools in model loop.
- **Accidental remote disclosure:** strict-local network gate, per-feature
  routing, egress preview, remote background disabled, redaction before send.
- **Credential capture:** skip secure controls and exclusions before reading;
  redact before persistence; regression corpus and fuzzing.
- **Malicious provider/base URL:** scheme/host validation, loopback
  classification, redirect rejection, TLS verification, bounded responses.
- **Dependency/supply-chain compromise:** pinned modules and lockfiles,
  govulncheck, npm audit, dependency review, SBOM, signed releases.
- **Malicious MCP client:** read-only tools, bounded output, rate limits, no
  direct database path, local user authentication, structured audit.
- **Content in logs/crashes:** identifiers and counters only; no raw payloads;
  local diagnostics preview; crash upload off.
- **Deletion remnants:** targeted deletes use secure-delete, WAL checkpoint,
  and vacuum; delete-all destroys the encryption key before removing files.
  Documentation states that SSD physical overwrite cannot be guaranteed.

## User interface

Blur uses an original visual identity: quiet graphite surfaces, warm amber
status light, high-contrast type, and restrained motion. It must not recreate
the source product's fish artwork or marketing look.

Surfaces:

- menu bar: capture state, pause durations, open chat/dashboard, local/remote
  AI indicator, token activity, quit;
- memory bar: edge/corner positioning, hover-open, pin, resize, inbox badge;
- chat: streaming answer, evidence drawer, context/token disclosure, provider
  badge, cancel/retry;
- inline indicator and edit command bar near the target caret;
- dashboard pages: Overview, Search, Timeline, Memory, Identity & Style, Time,
  Reminders, AI Providers, Privacy, Shortcuts, MCP, Diagnostics, About;
- onboarding: purpose, local-data promise, Accessibility request, optional
  microphone, provider setup, exclusions, and a real rewrite tutorial.

Accessibility requirements include keyboard-complete navigation, reduced
motion, screen-reader labels, color-independent status, 4.5:1 contrast, and
text scaling to 150% without clipping.

## Privacy controls

- Pause for 15 minutes, one hour, until tomorrow, or indefinitely.
- Per-app, bundle-ID, domain, URL-pattern, and title-pattern exclusions.
- Visible immutable organization exclusions.
- Strict Local Mode and per-feature provider routing.
- Configurable retention for raw observations and derived memory.
- Inspect exactly what a selected observation stored after redaction.
- Delete by time range, app/domain, wiki page, chat thread, or all data.
- Export selected data as a password-encrypted archive with a manifest.
- Import Blur archives and explicitly selected Goldfish databases read-only.
- Content-free audit timeline for capture pauses, egress, imports, exports,
  deletes, permission changes, and key rotation.

## Performance and reliability targets

- Capture CPU p95 below 1.5% on an M-series Mac during ordinary work.
- Daemon resident memory below 150 MB excluding optional local model runtimes.
- Capture-to-durable-store p95 below 250 ms for a 100 KB accessible frame.
- Recent-activity query p95 below 50 ms at 100k observations.
- FTS query p95 below 150 ms at 100k and below 500 ms at 1M observations.
- Activation-key overlay visible within 100 ms.
- No more than one database copy of identical normalized content.
- Typical encrypted store below 750 MB per 100k observations, excluding user
  exports, measured on the project benchmark corpus.
- Abrupt termination at every transaction boundary recovers without corruption
  or plaintext residue.
- Capture enters backpressure mode instead of growing unbounded queues.
- Background AI pauses on battery/thermal pressure when configured.

## Compatibility import

An optional importer opens a user-selected Goldfish SQLite database read-only,
detects compatible tables, maps user-owned observations, chronicles, wiki
pages, time rules, chat, and settings into Blur's model, redacts again, and
records provenance. It never writes the source database, never reads account
tokens, and never imports proprietary assets or application secrets. Import is
restartable and reports skipped records.

## Verification strategy

- Unit tests for every pure package and table-driven redaction corpus.
- Property/fuzz tests for redaction, parsers, content normalization, schedules,
  URL classification, API/MCP decoding, and prompt-evidence boundaries.
- Repository tests against an encrypted temporary database.
- Contract tests for each provider using local fake servers.
- Golden retrieval tests with synthetic data and required citations.
- macOS adapter tests in a purpose-built Accessibility fixture application.
- End-to-end tests for onboarding, capture/pause/exclusion, search, chat,
  inline rewrite cancellation, edit mode, dictation, schedules, MCP, export,
  import, deletion, and recovery.
- Crash/fault-injection tests around WAL, content writes, rekey, import, and
  migration.
- Benchmarks at 100k and 1M synthetic observations.
- Security CI: race detector, fuzz smoke, govulncheck, npm audit, secret scan,
  license policy, SBOM, and signed artifact verification.

## Delivery slices

1. **Foundation:** repository, encrypted store, secure local API, privacy filter,
   settings, diagnostics, and CI.
2. **Useful offline memory:** macOS capture, dedup, recent activity, FTS search,
   timeline, exclusions, pause, retention, and time ledger.
3. **Provider-neutral intelligence:** local/remote provider router, token ledger,
   context planner, chronicles, wiki, chat, and evidence citations.
4. **Anywhere writing:** global shortcuts, target discovery, inline rewrite,
   edit mode, style learning, and safe text injection.
5. **Voice and proactive work:** dictation, reminders, briefs, open loops,
   workflows, and notifications.
6. **Interoperability:** MCP, export/import, Goldfish compatibility importer,
   and self-hosting/provider documentation.
7. **Release hardening:** UX/accessibility polish, performance budgets, security
   review, migration/recovery, signing, notarization, updater, and public beta.

Each slice must leave a working, testable application. Features that need AI
degrade visibly when no provider is configured; capture/search/time do not.

## Release acceptance

The first stable release is complete when all primary workflows work from a
fresh install; the app remains useful fully offline; local Ollama chat and
OpenAI-compatible rewrite pass end-to-end tests; no raw content appears in
logs, temp files, or unauthenticated interfaces; MCP tools return bounded
source-linked results; deletion and key-loss behavior are documented and
tested; 100k-observation performance targets pass; and an external security
review has no unresolved high-severity findings.
