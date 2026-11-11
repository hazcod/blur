# Implemented architecture

This document describes the cross-platform foundation and durable memory core
as of the 2026-11-06 implementation checkpoint. The broader product design is
a roadmap, not a claim that the desktop product is complete.

## Runtime shape

The current runtime is one Go process. `cmd/blur` opens the encrypted durable
store through an OS secret adapter and shows the desktop shell on macOS,
Windows, and Linux amd64; accessibility capture is wired for macOS, Windows
amd64, and Linux amd64/arm64, with live Windows validation outstanding.
`blurctl status` and `blurctl capabilities` create an in-process demo client,
invoke the service, print bounded JSON, and exit. `blur-core` prints the same
demo status. The demo uses `EphemeralMemoryStore` and explicitly reports
`mode=ephemeral_memory`, `encrypted_at_rest=false`, `ai_enabled=false`, and
`network_listening=false`. There is no always-on daemon or listener. An
explicitly enabled Claude integration starts a current-user AF_UNIX socket on
macOS/Linux or a SID-restricted named pipe on Windows only while the desktop
is running; a separate `blur mcp` stdio process
uses an OS-secret-backed read grant to call that owner process and never opens
the database. It exposes only exact `search_memory`; all other and mutation
methods are rejected. It is off by default. The Windows pipe uses an explicit
current-SID DACL, rejects remote clients, and verifies the peer process token;
this is user-level authorization, not process isolation. The desktop's quick-note flow can explicitly opt into an existing
Ollama model or an OpenAI-compatible provider from Privacy settings; no model
is downloaded. Only sanitized user-authored notes enter the note-processing
flow, and only after the user adds them. A separate Ask action retrieves up to
eight exact local matches and packs at most 4 KiB of evidence. By default it
uses explicitly configured loopback Ollama. An OpenAI-compatible endpoint can
receive Ask evidence only after a separate, persisted Privacy-settings opt-in;
changing its endpoint or model clears that consent. Background summaries and
knowledge derivation remain local-Ollama-only. Both local and remote model
services are separate trust boundaries and may retain request logs or model
state. Preferences contain only provider/endpoint/model and an opaque
credential reference in a private config file; compatible-provider credentials
are held only by the OS secret store. Remote endpoints require HTTPS. Remote
providers may retain request data under their own policies. The Ollama extractor sets
`options.num_ctx` to 8,192, caps output at 384 tokens, and refuses prompts
whose UTF-8 byte upper bound plus 512 tokens of template headroom and the full
output allowance exceeds that context. The note remains saved and deterministic
TODO parsing continues when model admission refuses it. Ask output is bounded,
validated as structured data, restricted to citations from the packed sources,
and remains transient in the client rather than persisted as generated memory.

```text
blur desktop                         blurctl / blur-core
    │                                      │ in process
    ▼                                      ▼
local bootstrap                         core demo
    │ encrypted durable store
    └── desktop client
test/future host ── CaptureSource probe ── bounded raw staging ── redaction
                                                              └── opaque
                                                                  sanitized value
```

## Implemented boundaries

### Platform capabilities

`internal/platform` defines narrow interfaces for capture probes/events, text
target leases, hotkeys, surfaces, audio, secret storage, local transport,
private paths, autostart, and session state. Reports use a closed state
vocabulary and fixed evidence codes. The Darwin, Windows, and Linux capture
adapters are not at parity. Darwin has a TCC-gated AppKit target probe,
event-driven app/focused-context notifications, and a bounded AXUIElement
reader; capability checks are non-interactive and report permission-required
when trust is absent. If capture is enabled at startup, the controller may ask
macOS to show its own Accessibility guidance once; Blur never grants or edits
TCC state, and it continues checking capability independently afterward.
Linux has a private-session-bus AT-SPI2 focus/event source and bounded
accessibility-tree reader. It registers only focus, text-change,
children-change, and window-activate events, does not consume event bodies,
checks same-UID processes and focused state around reads, and is wired into the
Linux amd64/arm64 X11 desktop. Real-desktop runtime validation remains open.
Windows has foreground process identity leases, secure-desktop/elevation
preflight, coalesced WinEvent notifications, and a bounded UI Automation
reader; the Windows desktop connects it to the same explicit start/pause and
encrypted-ingest path. UIA cleanup is asynchronous after cancellation and
serialized through one session slot: Pause does not wait on a stuck provider,
and a provider that never returns cannot accumulate one automation session per
focus event. Native Windows runtime and fixture-app validation remain open.
On macOS, Linux, and Windows, known browsers expose only their current
foreground Accessibility tree to the capture pipeline. Address-bar, tab,
toolbar, and form-entry roles are filtered; a bounded OS window-title
preflight rejects recognized private-browsing markers and is repeated around
value reads. The Windows target lease carries only a private/eligible bit, not
the title text. Capture never navigates or queries browser history. Private-
mode detection depends on browser-provided, localized window-title markers
and needs live validation across supported browser versions and locales
before release; missing or unreadable titles fail closed.

### Capture and observation boundary

`internal/capture` owns its raw frame and node types; these have no exported
representation and cannot be JSON-decoded into a trusted observation. A
pipeline checks capability, probes only bounded non-content metadata, validates
the probe, rejects secure/elevated/private or explicitly excluded targets,
then calls the bounded reader. It enforces caller-supplied byte, node, depth,
and duration limits and redacts before it constructs a `SanitizedObservation`.
The private application-preference file supplies bounded, normalized app-ID
and domain exclusions; changing the list pauses the controller before saving
and restarts it with the new capture policy. The preference is private and
atomically replaced. Exclusions affect future observations only and do not
delete already stored history.
On macOS browser captures, the Darwin reader also reads the focused window's
bounded `AXDocument` attribute and reduces an HTTP(S) URL to its normalized
hostname. The raw URL is transient: paths, queries, fragments, and credentials
are never stored or logged. Domain allow/block rules are evaluated after bounded
structure collection but before any deferred page-text value is read; if a
browser host is unknown while domain rules are configured, capture fails
closed. When a domain rule is active, the host is revalidated around each
deferred value read to reject navigation races. This reads only the active
document's Accessibility metadata and never navigates, enumerates tabs, or
queries browser history.
Capture errors do not include underlying reader errors or captured values.
Common controls are filtered before their deferred values are read. Editable
text fields, text areas, search fields, and combo boxes are marked sensitive;
their values and descendants are never requested, so unsent drafts and search
queries stay out of memory while rendered message/history text remains
eligible. On Linux, the AT-SPI entry, date-editor, edit-bar, password-text,
and text roles are mapped conservatively to these sensitive roles before
traversal.
Built-in Slack profiles filter tab groups, tabs, images, and separators; user
profile roles are merged within fixed profile/role limits, while other
descendants remain eligible so message content already loaded off-screen can
be retained. The
Darwin reader revalidates both the frontmost process and focused-window
identity around reads; the Linux reader revalidates its focus lease and AT-SPI
focused state around reads; the Windows reader revalidates foreground window,
PID, process creation time, executable identity, elevation, and secure-desktop
state around structure and each deferred value read. Capture is event-driven
and enabled by default after first launch, subject to permission, privacy
filters, and the user's persisted pause choice; its controller writes sanitized
observations and source-linked activity segments to the encrypted store. Pause
waits for the worker to stop and then unregisters passive OS listeners; a later
Start may subscribe again. Normal worker exit follows the same suspension path.

An observation has private fields and a validating constructor. Its storage
boundary checks canonical app/domain identifiers, the current redaction
version, redactor fixed-point status, bounded text and counters, and a
recomputed content identity. JSON marshal is available for the validated
sanitized form; JSON unmarshal is rejected. The storage and core APIs accept
this type, not raw frames. This is an in-process Go type boundary; it is not
yet a wire protocol or process isolation boundary. Capture also owns a
versioned deterministic bounded binary persistence codec. The durable store
calls its restore path only after AEAD authentication; restore reconstructs
private fields, checks canonical encoding, current redaction policy and the
recomputed content identity. JSON remains unable to construct a trusted value.

Redaction is deterministic and heuristic. Current patterns cover labeled
secrets, selected provider tokens, authorization headers, private-key blocks,
common sensitive URL components, US SSN shape, checksum-valid card numbers,
and IBANs. It also supports bounded UTF-8 truncation without leaving a
recognized partial secret. It cannot guarantee detection of arbitrary,
unlabeled, novel, split, encoded, or application-specific sensitive content.
The current pipeline does not persist or emit audit logs.

### Authorization and transport contracts

`internal/localapi` issues per-client, scoped grants. The client receives a
random bearer credential; the store retains its SHA-256-derived HMAC key. A
proof binds grant ID, client ID, scope, timestamp, nonce, a fresh per-connection
server challenge, and operation digest.
The verifier checks scope, grant time bounds, clock skew, expiry, revocation,
MAC, duplicate nonce, and bounded per-grant replay state. Expiry denies new
requests; explicit revocation invalidates and removes the grant. Revocation
and expiry clear nonce/scope maps and overwrite the retained authenticator key
before the record is deleted. Issuance prunes expired records and rejects new
grants at the hard 1,024-record cap. Proofs are single-use within the
in-memory store and the nonce capacity limit rejects new proofs rather than
forgetting old nonces. The connection challenge prevents a captured proof from
being replayed after restart without persisting replay state. There is no global
install token.

`MemoryTransport` remains the bounded conformance transport. A bounded
AF_UNIX transport also exists: it requires a private owner-only parent
directory, creates a user-only socket, and verifies the connecting peer UID on
Darwin and Linux. It is wired to the explicitly configured, user-started Safari
ingest bridge and the opt-in Claude read bridge. Safari uses an expiring
shared-Keychain credential; Claude uses its OS-secret-backed bounded-read
grant. There is no Windows named-pipe implementation or process-isolation
boundary.

The opt-in Claude bridge separately uses bounded AF_UNIX transport on
macOS/Linux and a bounded, remote-client-rejecting named pipe on Windows, a
one-year `mcp:bounded-read` credential protected by the OS secret
store, challenge-bound HMAC request proofs with replay protection, and explicit revocation and
credential deletion when disabled. Its sole operation runs exact local search,
reads no more than 64 KiB of source data, returns up to eight 2 KiB text
excerpts, and caps serialized output at 16 KiB. The MCP-launched process reads
its grant and connects to the already-open desktop store owner. Results cross
to the MCP host and may be sent to its configured model provider; Blur itself
makes no provider request for this tool. This is same-user authorization, not
process isolation. The Windows endpoint DACL and peer-token check restrict
connections to the current account; other code running as that same account
remains within the trust boundary.

### Core, store, and search

The `EncryptedStore` interface requires bounded ordered listing, validated
sanitized observations, and immutable derivation/generation metadata. The
explicitly named `EphemeralMemoryStore` stores copies in process memory,
reports `EncryptedAtRest() == false`, and loses all data when the process ends.
An internal `durable.Store` now provides the same contract over the Task 1
opaque bbolt adapter: logical IDs are domain-separated keyed HMACs, values are
versioned and AEAD-encrypted, and an encrypted keyed record-set manifest is
updated in the same transaction. A random persistent store identity and each
record's schema context are authenticated as AEAD associated data. Schema
upgrades atomically advance identity/schema/manifest metadata; old values
remain readable only under their authenticated older schema context, while
new writes use the current schema. Open requires explicit create-new or
open-existing mode; reads verify the manifest before serving the store, and
observation pages authenticate a small sealed size record to enforce byte
ceilings before fetching/decrypting a record that will not fit. Each size
record also authenticates a store insertion timestamp without including it in
the content-derived source ID. On upgrade, old size records receive a stable
encrypted migration-time baseline in bounded transactions; this conservatively
starts any future age-based policy at upgrade rather than guessing old dates.
A single
internal transaction extension boundary is available to later index
materializers.
The durable backend now includes encrypted exact search, activity segments,
materialized Today snapshots, and the evidence-backed knowledge library. The
store contract exposes `DeleteObservationRecord` for idempotent source removal;
the durable implementation deletes the authenticated source and exact-search
postings, then removes its knowledge-source links in one transaction. Claims
and loops with no remaining source are dropped. Today snapshots are updated via
an encrypted source-to-date index, removing all text-bearing entries that cite
the source while preserving unrelated evidence. Activity segments are deleted
through an encrypted source-to-segment index. These cascades share the source
deletion transaction; each reverse index is bounded to 64 references per
source and rejects writes that would exceed that limit. Existing stores created
before these indexes require an explicit backfill migration before deletion can
be represented as complete. Today snapshot validation also requires every
source-bearing item and follow-up answer reference to exist in its bounded
reverse-deletion index. New notes remain encrypted and searchable when the
per-day source cap is reached, but their preview is omitted from Today and the
omission is recorded; answering a follow-up is refused if its answer source
cannot be indexed. The current opaque keyed index format does not
permit an in-place enumeration of those legacy records, so opening such a store
does not claim coverage and deletion returns `ErrProjectionIndexUnavailable`
until an explicit migration/rebuild path is supplied. `internal/deletion`
coordinates source deletion with the live activity engine: it scrubs buffered
segments first, then requests durable deletion. If persistence fails, live
memory stays scrubbed and the caller receives the error for retry. The macOS
bootstrap binds the service to its client; search results offer a two-step
inline confirmation. If capture is running, the desktop pauses and joins it
before deletion to prevent immediate recapture; capture stays paused after
success. The result disappears and Today reloads only after durable deletion
succeeds. Privacy settings also provide an explicit two-step “Delete all
memory” action. It pages through the bounded observation API and uses small,
atomic multi-source transactions sized against indexed-term cost; process-local
activity is scrubbed before each batch and a failed batch can be retried. The
physical transaction cap remains authoritative and rolls back any over-budget
batch. It removes source-linked
derivatives while preserving the encryption key and model settings. This is
logical deletion, not guaranteed physical erasure from database freelists,
snapshots, backups, swap, or storage media. Privacy settings support Off, 30,
90, 180, and 365 days; unconfigured stores default to 90 days, while an
explicit Off selection is preserved. Changing the interval requires a second
confirmation and immediately prunes expired records; launch and a daily
low-frequency worker enforce it afterward; failures retry every 15 minutes
until one completes. A sweep marks itself complete only after all
source-linked deletions succeed. The macOS bootstrap shares its process-owned activity engine with
capture and deletion. The explicit `blurctl fixture` commands can read
synthetic stores under the OS temporary directory using a public test key;
those commands are fixture-only.
The macOS bootstrap resolves the key through the injected platform secret
store and refuses fixture-key or plaintext fallback. The store
does not protect against whole-file rollback, compromised process memory, or
loss/theft of the data key. See [ADR 0002](adr/0002-durable-store.md) and
[ADR 0003](adr/0003-private-exact-search-index.md).

Activity accounting is event-driven and uses monotonic elapsed time. On macOS,
each sanitized capture event advances a process-owned engine and emitted
source-linked segments are encrypted immediately; pausing capture closes the
session with a bounded final write, so paused intervals are excluded. The same
engine is shared with deletion coordination. Durable activity now has an
encrypted UTC-day directory with bounded 64-item pages (at most 4096 segments
per UTC day); callers can list and hydrate pages without prior knowledge of
segment IDs. The desktop content facade exposes local-calendar time reports
over a caller-selected zone and inclusive date span; reads are capped at 31
days and 4096 inspected segments, and boundary segments are clipped before
aggregation. The required UTC partitions are hydrated under one Bolt read
snapshot with an 8 MiB decoded-data ceiling. Arbitrary broader scans remain
explicitly caller-bounded. The native-window desktop shell exposes this as an explicit
local-timezone date-range report with deterministic app totals; it refreshes
only on user request and never invokes AI. `BenchmarkReportYear` exercises
4,015 synthetic one-minute segments (11 per day) across a 365-day UTC year;
the 2026-11-06 Apple M5 Max Darwin/arm64 run measured 0.932 ms/op, 139 KB/op,
and 8,050 allocations for one report. This is a single local reference sample,
not a cross-device service-level objective. Today is a deterministic, bounded
model-free projection persisted as one encrypted per-date value. On a Today
read, the client lazily joins bounded local-day activity segments into active
subjects, recent app-focus entries, and time splits, preserving note tasks and
questions. It clips UTC-indexed segments to the local calendar day and avoids
rewriting an unchanged activity generation. The UI refreshes after capture
start/pause and receives a coalesced, payload-free signal after newly persisted
observations; it reloads only while Today is visible, the selected date is the
local current day, capture is running, and no load is in flight. Automatic
knowledge materialization is user-triggered only: an expanded hourly recap may
be sent to loopback Ollama for up to three bounded, source-cited Library fact
proposals. Proposals remain transient until individually accepted; acceptance
creates a confirmed fact whose source is checked atomically with the write.
Remote providers and background workers do not receive this evidence, and
source deletion continues to remove facts that lose their evidence.
Knowledge entities, facts, open loops, aliases, merge history, and source
links are encrypted; exact name lookup uses keyed normalized terms, and source
quarantine removes claims that lose all evidence. Lifecycle callbacks exist,
but capture never derives Library content automatically.
The desktop Library supports explicit user-authored entries with optional
user-authored notes, per-entry two-step removal, and merge recovery. A merged
source can be unmerged back into its prior source and destination records;
destinations with linked records must have those entries unmerged before they
can be removed. Removing a library entity does not remove captured observations
that may have informed it; global Delete All remains the path for removing both
sources and their derivatives.

The durable backend has a private encrypted exact search index with bounded
global top-k retrieval, keyed term/app/domain postings and deletion repair.
`search.Engine` uses this index whenever the store implements `ExactIndex`;
the durable store does. Index queries bound rows, bytes, tree nodes, returned
hits, and subsequent source hydration, and reject a partial ranking when the
global top-k cannot be proven within budget. The separate ordered-scan path
is available only when the caller explicitly enables it and is capped at 64
observations and 2 MiB of aggregate text/app/domain bytes per request. It
fetches at most four observations per page through a byte-bounded store
contract; the store reports page bytes and whether more records remain before
returning copies. The core service permits at most two searches at once and
waits for a slot with context cancellation. Results, snippets, serialized
response bytes, and provenance content IDs are bounded too. Search does not
generate or persist derived content. Fuzzy or semantic retrieval and
generated summaries remain unimplemented; the macOS Ask surface is a
transient local answer path over the client's encrypted exact index.

### Secrets and diagnostics

`internal/securestore` defines a 32-byte data-key value that resists accidental
formatting or serialization. Only an explicit typed `NotFound` may authorize
key creation; found-but-malformed, locked, corrupt, denied, unavailable,
ambiguous, or failed states do not create or overwrite a key. A process-local
gate serializes Get-then-Put, but it is not atomic across processes. This
is connected to the macOS Keychain, Windows DPAPI CurrentUser, and a Linux
Secret Service adapter. The Linux adapter uses the session bus and Secret
Service's encrypted DH/AES transfer, refuses ambiguous/locked matches, and does
not create collections or invoke unlock prompts. It is wired into the Linux
X11 application bootstrap on amd64 and arm64. The arm64 UI build uses a narrow
vendored GoUI compatibility patch: its Linux pipe/poll wrappers call the
already-required `x/sys/unix` APIs, and X11 reads the corresponding `Revents`
field. This avoids architecture-specific syscall numbers in Blur. The durable
database uses the resolved key for authenticated encryption.

Capability reports, capture errors, grant/proof formatting, store errors, and
CLI failures use stable codes or generic messages rather than payloads. Status
reports expose mode, encryption, AI/network booleans, readiness, and version;
capability output contains fixed IDs, states, reasons, and evidence codes.
The lifecycle layer provides a bounded writer queue, coalescing for keyed
materialization jobs, explicit backpressure, cancellation, and orderly store
close hooks. Diagnostics builds a content-free doctor report from bounded
durable samples. The fixture CLI supports doctor, Today, and knowledge reads
only with an explicit fixture marker and a `blur-fixture-*.db` path below the
OS temporary directory. It uses a deliberately public synthetic key and is
not a production key flow. The macOS app writes a bounded rotating process log,
a bounded startup trace, and runtime stderr to the per-user Logs directory (or
its cache fallback). The Windows GUI writes a bounded rotating process log and
a per-run runtime crash log under the user's config directory, retaining one
previous run as `.1`; its separate MCP console process keeps stdio for the
client. Linux currently reports startup
errors to stderr and has no persistent desktop log. No platform uploads logs or
crash output. Some lower-level failures are logged as error strings, so
diagnostics may expose local paths or OS/backend details and must be treated as
private; captured UI text and model prompts are not intentionally logged.

The macOS Privacy screen exposes a separate power policy preference file under
the private per-user app configuration directory. Follow System reads only the
native OS battery-saver signal when requested: Foundation ProcessInfo on macOS,
`GetSystemPowerStatus` on Windows, and the power-profiles-daemon active profile
on Linux ([Apple](https://developer.apple.com/documentation/Foundation/ProcessInfo/isLowPowerModeEnabled),
[Microsoft](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-system_power_status),
[freedesktop.org](https://upower.pages.freedesktop.org/power-profiles-daemon/gdbus-org.freedesktop.UPower.PowerProfiles.html)).
It does not treat battery power itself as saver mode. Unknown state falls back
to Balanced. Saver never disables capture, retention-critical
deletion, deterministic work required by a user action, or interactive AI.
Background AI admission is local-only and bounded by mode, token, duration,
and concurrency caps.
Hourly chronicle summaries consume the policy: consent defaults off, admission
is local-Ollama-only, and a bounded worker pool enforces token and duration
budgets after a fresh power read. Capture stores an encrypted
per-hour source-ID index; startup checks only the prior 24 closed hours when
consent is enabled. While capture is active, a one-shot hour-boundary timer
closes the current period; there is no recurring idle polling. The active hour
uses bounded sanitized evidence in memory, and summaries are encrypted and
removed atomically when a source is deleted. The UI displays summaries and
pages through cited sources on demand. An hour summary may additionally
produce one missing-follow-up question, only when a meeting cue occurs in its
exact cited source; it is validated and atomically linked into the encrypted
Today projection, where the user can answer or dismiss it. It cannot create
tasks or external actions. Power behavior, real idle wakeups, and battery/CPU
savings remain unmeasured release gates.

## Enforced foundation controls

- No network listener is present. The demo reports network listening disabled.
  The desktop's optional note extractor accepts loopback endpoints only and is
  invoked only for user-authored notes after explicit opt-in.
- No screenshot, OCR, clipboard, or keystroke capture path is connected.
- Unknown/unavailable capability states and adapter operations fail closed.
- Probe rejection precedes value reads; read work is bounded and sanitized
  before the validated observation is constructed.
- Raw frame types are package-private and raw observation JSON decoding is
  rejected.
- Production service construction rejects unencrypted stores.
- Local proof verification is scoped, time-bounded, replay-aware, revocable in
  process, and bounded to 1,024 grants with best-effort key clearing on removal.
- Search work is bounded per request and concurrent call, including byte-aware
  pages whose metadata prevents the store from cloning observations that do
  not fit.
- The internal durable store authenticates encrypted records, HMACs physical
  logical-ID keys, detects record-set insertion/deletion/tampering with a
  transactionally updated manifest, and refuses implicit create-on-open.
- Today uses one encrypted materialized value plus a bounded local-day activity
  read when activity data is available; neither path calls a model/provider.
- Activity sessions advance only on capture or OS-state events; recovery after
  missing events is bounded and excess time is explicitly reported as omitted.
- Knowledge claims require source IDs or explicit user-authored provenance;
  source quarantine removes claims whose evidence has been deleted.
- Lifecycle jobs use a bounded queue with explicit rejection, coalescing and
  cancellation. Doctor samples are capped and report metadata only.
- Command binaries and transitive non-standard dependencies pass the current
  cgo-disabled build and Cgo/import-C audit matrix.

These controls constrain this foundation. They do not establish a complete
desktop security boundary, persistent privacy guarantee, or production
personal-memory workflow.

## Deferred product phases

Still-open product work includes Windows real-host UIA validation, richer
target leases and exclusions, production browser-page
capture with verified private-window exclusion, general-purpose IPC and
process isolation, automatic knowledge
materialization, production-core consolidation (the desktop currently calls its
durable store directly), broader provider routing beyond explicit local quick-note processing, broader MCP tools,
dictation/audio, histories and chronicles, scheduling, export/import, retention
UX follow-through, authenticated backups and key rotation, packaging/signing/
release SBOMs, and real platform fixture applications. Accessibility capture
is currently wired for macOS, Windows amd64, and Linux amd64/arm64; OS secret adapters and encrypted
durable storage are wired into desktop bootstrap. Desktop startup uses a
per-user lock to prevent concurrent instances and key/store initialization
races. `internal/browsercapture` defines a bounded sanitizer, exact-payload
HMAC receiver, and private AF_UNIX server for fresh, active, non-private Safari
snapshots. The macOS host can conditionally issue a one-day Keychain-shared
grant and run that receiver only while capture is started. The Safari extension
builds as an unsigned universal `.appex` from the checked-in Xcode project.
Safari page capture additionally requires a separate Privacy opt-in (off by
default, including when upgrading old preferences); it is gated by global
capture state, reports bridge readiness/failure in the UI, and never starts
when the shared entitlements are absent.
The opt-in signed target requires and checks separate host/extension profiles,
emits matching App Group/Keychain entitlements, signs the extension before the
host, and verifies both signatures. The default app still excludes Safari;
registered identifiers, release signing/notarization, and live Safari
verification remain outstanding, so browser capture remains unavailable in
default builds. The complete
proposed direction and gates are in the
[design](superpowers/specs/2026-11-04-blur-design.md) and
[implementation plan](superpowers/plans/2026-11-04-blur-cross-platform-foundation.md).
