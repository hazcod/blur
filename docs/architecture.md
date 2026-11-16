# Implemented architecture

This document describes the current cross-platform foundation and durable
memory core as of the 2026-11-15 checkpoint. The broader product design remains
a roadmap, not a claim that native acceptance or the desktop product is complete.

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
this is user-level authorization, not process isolation.

On macOS, the default model setting selects Apple's on-device system model and
enables automatic Library processing and background recaps. One provider
selection serves meaning search, answers, saved-note processing, intent, and
eligible background work. Exact local search remains available without a model.
Ollama is an optional loopback provider; Claude, ChatGPT, and OpenAI-compatible
providers are separate explicit choices with scoped consent for sending saved
memory or notes. The standard runtime does not start the legacy standalone
embedding service or expose a second embedding-model choice. No provider switch
silently falls back to a remote service.

Only sanitized user-authored notes enter note processing, after the user adds
them. Cloud evidence and note processing each require their applicable persisted
consent. Preferences contain provider/model routing and opaque account or
credential references in private configuration; secrets are held only by the
OS secret store. Remote endpoints require HTTPS and may retain request data
under their policies. The Ollama extractor sets `options.num_ctx` to 8,192,
caps output at 384 tokens, and refuses prompts whose UTF-8 byte upper bound plus
512 tokens of template headroom and the full output allowance exceeds that
context. A note remains saved when model admission refuses it, and deterministic
TODO suggestions stay unconfirmed and source-linked. Ask output is bounded,
validated as structured data, restricted to citations from packed sources, and
remains transient rather than becoming generated memory.

Ask's local evidence path is available without an answer model or populated
Library. Ambiguous identity/factual questions and short keyword queries first
probe bounded local retrieval; a matching source turns the request into a memory
question. These ambiguity probes require a topic match and cannot substitute
unrelated recent content from a named app. Explicit general mode,
writing/explanation requests and arithmetic keep the general path. Unmatched questions can use the configured
general model without sending saved evidence. A pending index prevents a false
absence conclusion. Model readiness is checked after personal-memory retrieval:
when no answer model is admitted, Ask returns bounded quoted passages with
source links, full original inspection, partial/omitted coverage and zero model
transmissions. Generated synthesis still requires an admitted provider. Remote
Ask consent gates model egress, not the ability to inspect local evidence.

Clear question prefixes with an unambiguous read plan skip model-based intent
classification. Pending tasks/reminders are handled first; ambiguous help and
polite action requests still use typed interpretation. Interactive local
answers request ten minutes of Ollama residency, bounded and optional at the
model transport; background calls retain the provider default. This can keep
the model's RAM occupied longer, and the provider may still unload it. There is
no background warm-up request. Simple factual answers request one direct
sentence; detailed questions, conflicting evidence and coverage gaps retain
their explanation and the existing output budget. Source validation, context
admission and local-model manifest checks remain in place. Content-free debug
timings identify the request surface, elapsed time and source count without
logging questions or answers.

### Apple on-device model

Apple's [`SystemLanguageModel`](https://developer.apple.com/documentation/foundationmodels/systemlanguagemodel)
is an explicit local completion provider on eligible Macs running macOS26 or
later with Apple Intelligence enabled and its model ready. It requires no
account login, API key or separately configured server. Existing saved provider
preferences remain active until a new selection is saved. Settings query
availability asynchronously and explain unsupported OS/hardware, disabled
Apple Intelligence and model assets that are not ready. There is no automatic
remote fallback. The selected provider also handles meaning search and note
processing; exact local search remains immediate. The legacy embedding service
is dormant in standard startup and is not exposed as a second model setting.

A bundled Swift helper weak-links FoundationModels with a macOS13 deployment
target, preserving the application's existing launch floor. The universal
helper is signed before the application; sandbox builds give it only the
sandbox/inherit entitlements. Go resolves it relative to the application
executable, never through PATH. Each request uses bounded private stdin/stdout,
a fresh session, default guardrails and the concrete on-device system model.
The transport applies deadlines, cancellation and a global inference gate;
questions and native errors never enter process arguments or diagnostic logs.

Ask/general answers, sanitized-note proposals, typed intent, Chronicle and
Library use their existing authoritative validators. Explicit background
opt-ins remain required. Common JSON envelopes use guided generation; the
intent adapter uses closed kind classification followed, when needed, by a
closed schema containing only that kind's fields. Kind-specific validation
remains authoritative. Generation structure does not establish semantic
correctness or authorize a mutation. Runtime token counts and context size
admit the complete output budget before inference, with conservative byte
accounting on supported systems without native tokenizer APIs. The effective
context is capped at8192; a smaller context is reflected in upstream budget
admission. Large evidence and indexing remain independent of model limits.

Apple controls model assets and residency. Its job identity names the provider,
OS build and effective context; Apple does not expose an immutable model-asset
digest, so this cannot prove identical weights across jobs or asset updates.
On this Mac, native synthetic source/unknown/refusal/context checks pass; app
UI selection and capture/reminder acceptance remain separate gates in the
personal-memory plan. Memory measurements of the small helper process do not
represent the total shared system model footprint.

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
event-driven app/focused-context notifications, an idle-aware periodic refresh, and a bounded AXUIElement
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
Common controls are filtered before their deferred values are read. On macOS,
visible edited text is eligible just like read-only document content. Secure
text/password roles and subroles are pruned before text or children are read,
and sensitivity is rechecked around deferred native values. The native reader
accepts CFString and CFAttributedString values and a bounded AXStringForRange
fallback; long values prefer the current visible range to an old scrollback
prefix. Omitted off-screen content is reported as partial. Linux and Windows
retain their conservative editable-role exclusion until equivalent native
sensitivity contracts are implemented. Blur's own interface is excluded on
every platform, keeping its searches and answers out of passive capture.
Built-in Slack profiles filter tab groups, tabs, images, and separators; user
profile roles are merged within fixed profile/role limits, while other
descendants remain eligible so message content already loaded off-screen can
be retained. The
Darwin reader revalidates both the frontmost process and focused-window
identity around reads; the Linux reader revalidates its focus lease and AT-SPI
focused state around reads; the Windows reader revalidates foreground window,
PID, process creation time, executable identity, elevation, and secure-desktop
state around structure and each deferred value read. Capture uses events, with
a macOS refresh after 15 seconds without one, and is enabled by default after first launch, subject to permission, privacy
filters, and the user's persisted pause choice; its controller writes sanitized
observations and source-linked activity segments to the encrypted store. Pause
waits for the worker to stop and then unregisters passive OS listeners; a later
Start may subscribe again. Normal worker exit follows the same suspension path.

The macOS browser reader locates one visible outer web area using bounded
structural metadata. Browser chrome and input subtrees are not read for text.
The page's retained parent/child path is checked before and after the frame;
page/window URL hosts are rechecked around values. Selection never walks the
full window separately for every value. CFURL and CFString location attributes
are accepted, and unknown required domains fail before text reads. Supported
applications receive a rate-bounded AXManualAccessibility request under existing
AX permission; this does not open an OS permission prompt.

Only aggregate input age is queried for macOS idle detection using
[CGEventSourceSecondsSinceLastEventType](https://developer.apple.com/documentation/coregraphics/cgeventsource/secondssincelasteventtype(_:eventtype:)).
After three minutes without input, capture rests until activity resumes. No
individual input events or keys are observed. Capture failures, exclusions,
and idle transitions close attribution to the previously seen source; measured
durations use the monotonic clock, with sub-millisecond wall-clock skew absorbed
to keep adjacent segments coalescible. Content-free health distinguishes idle,
permission, exclusions, inaccessible page/domain, empty text, timeout and storage
failure, and preserves the last unresolved problem across an excluded UI focus.

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
new writes use the current schema. Schema 6 atomically upgrades version 1–5
metadata while retaining the authenticated older value contexts. Its
identity version makes older clients reject a store whose personal-memory
projections, repeated-visit metadata, capture editions and permanent deletion
fences they cannot honor. Open requires explicit create-new or
open-existing mode; reads verify the manifest before serving the store, and
observation pages authenticate a small sealed size record to enforce byte
ceilings before fetching/decrypting a record that will not fit. Each size
record also authenticates a store insertion timestamp without including it in
the content-derived source ID. On upgrade, old size records receive a stable
encrypted migration-time baseline in bounded transactions; this conservatively
starts any future age-based policy at upgrade rather than guessing old dates.
The baseline is not an observed capture time. Before publishing a store, Blur
loads its authenticated baseline and conservatively treats an equal timestamp
as unknown for dated recall. The value remains unchanged for retention and
index compatibility. Real later sightings remain dated evidence. Search, MCP,
and model context expose unknown original capture times without inventing a
date; date windows and hourly recaps exclude baseline-only occurrences.
A single
internal transaction extension boundary is available to later index
materializers.
The durable backend now includes encrypted exact search, activity segments,
materialized Today snapshots, and the evidence-backed knowledge library. The
store contract exposes `DeleteObservationRecord` for idempotent source removal;
the durable implementation deletes the authenticated source and exact-search
postings and removes its knowledge-source links. Ordinary bounded cascades
commit in one transaction. When a valid cascade exceeds a physical budget,
an encrypted source-erasure journal drives bounded task/entity/date/activity
steps. A store-wide publication gate rejects reads and writes while cleanup
is pending; old transactions drain before the first step. Each step and cursor
commit together, the final source/journal removal is atomic, and reopen finishes
recovery before publishing the store. Interrupted cleanup stays unavailable
until recovery succeeds. Transaction limits remain unchanged. Claims and loops
with no remaining source are dropped; saved merge originals are cleaned too,
so unmerge cannot retain erased provenance. Today snapshots are updated via
an encrypted source-to-date index, removing all text-bearing entries that cite
the source while preserving unrelated evidence. Activity segments are deleted
through an encrypted source-to-segment index. These cascades participate in the same deletion publication boundary; each reverse index is bounded to 64 references per
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
Primary evidence is committed independently of exact posting maintenance with
an encrypted ID-only work receipt. The 128-term inline threshold is only an
optimization; large sources retain their complete text and are fully indexed
in resumable 64-term transactions by a local worker that needs no AI model.
Per-term proofs keep query reads independent of document vocabulary size;
paged reverse references participate in staged source erasure. Search, Ask,
MCP and Settings disclose pending coverage. A failed derived write cannot roll
back an accepted capture. Query-selected passages let long-page tail matches
reach the model and MCP while preserving the full original for inspection.
Local hydration budgets are separate from external response/context budgets.
The passive AX reader still has per-frame work bounds, and the legacy single
observation format has a 1 MiB text bound. On macOS, the production capture
controller also uses a whole-document path when the focused app exposes a
continuable Accessibility tree: bounded Unicode reads feed continuous mapped
redaction and canonical parts of at most 32 KiB and 256 source spans. That path
has no total document-size ceiling. Apps that do not expose a continuable tree
fall back to bounded observation capture, where partial coverage is reported;
support across every app and long-running native acceptance remain open.

The encrypted capture repository stages parts privately, verifies the complete
chain at atomic publication, deduplicates editions, and retains exact capture
visits. Production capture writes through this path. Publication queues
resumable part indexing and exact visit indexing; public exact search merges
capture parts and observations under shared query budgets. Ask, Search, source
inspection, MCP and Today hydrate published part evidence with its original
provenance. Open discards unfinished and duplicate staging in bounded resumable
steps. Generic source erasure, family forget, Delete All and retention include
capture payloads and derived indexes through the existing gated erasure journal.
Conditional deletion preserves newer real visits; a monotonic capture retention
cutoff rejects delayed older jobs. Catalog cursors bind publication
incarnations, and native preparation finishes before a family intent becomes
restart-resumable. The durable store schema is 7; opening it performs bounded
metadata/index initialization and preserves the existing encrypted user store.
Saved pages, notes and app activity are called captures. The original encrypted
namespace, hash domains and journal field names remain unchanged for compatibility.
The bounded exact posting tree now supports a private capture-part scope with
separate keyed lookup domains, record namespaces and physical group 0x41. The
observation scope retains its existing group 0x40 and format. Both scopes share
the same database lifetime and deletion gate. A scoped search transaction facade
uses the original transaction's writer, mutex and manifest, so future part erasure
can update postings and capture records together. Isolation, rollback, reopen,
split trees, paged term proofs and foreign-ciphertext rejection are tested.
The production publisher queues capture-part index rows without tokenizing the
whole source during publication. A local worker advances bounded batches and
reports pending coverage. Search checks live edition membership before ranking
and verifies the publication incarnation during hydration. Native acceptance
still needs broad application coverage for apps with different Accessibility
trees, large documents, cancellation and partial-content behavior.
Partial capture provenance is retained from the authenticated source through
Search, Ask, MCP and source inspection, and appears in model context separately
from choosing a bounded answer excerpt. Current content-free capture health
also reports whether its last readable content was partial.
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
generate or persist derived content. Optional local semantic retrieval now
combines with exact matches, primary captures, and available hourly summaries;
its implementation and remaining acceptance limits are described below.

Primary observations also have a separate encrypted, paged capture-time
index. `ListObservationWindow` reads at most 31 days, returning at most 64
sources and 2 MiB of source data in newest-first order with a timestamp/source
cursor. App, domain, and app-exclusion filters run before selecting that
prefix. This index is independent of the 64-source hourly chronicle recovery
selection: an hour may contain up to 65,536 primary sources, and exceeding
that hard capacity refuses the write rather than silently omitting evidence.
Timeline reads use a separate 4,096-operation read-only snapshot, with the
existing 16 MiB transaction byte ceiling and cancellation checks. Default reads
and all writes retain their 1,024-operation ceiling. Dense requests return
`storage.Incomplete` instead of a partial ranking.
Opening an older store backfills the index from authenticated observations
and timestamps with an encrypted, resumable source cursor. Legacy timestamps
previously replaced by the capture-time migration baseline cannot recover the
original observation dates. Such baseline-only rows do not qualify as observed
visits. Existing hourly summaries with those undated sources are omitted from
date-based recall, while the original content remains searchable unscoped.
Source deletion updates the timeline in the same
transaction and refuses deletion when its reverse provenance is missing.

Ask's exact-index scope applies capture time before global top-k selection,
additionally admitting source IDs from activity sightings inside the requested
window. Thus new captures need no closed activity segment to be found, while
an older source seen again can retain its later activity context. Client Ask
and Search exclude older captures of Blur's own interface before ranking;
future self-capture is blocked at capture policy too. Direct capture answers
carry inspectable source evidence without invoking a model. Today reads a
small primary-capture preview without waiting for a materialized snapshot or
hourly summary.

Repeated visits preserve immutable primary text and its first-capture time.
The encrypted timeline stores one latest-visit row per source/hour; separate
source-linked hour records retain exact timestamps for arbitrary sub-hour
windows. Month metadata admits visits before exact and semantic ranking.
Native capture, the optional Safari bridge, authenticated ingest, and repeated
quick notes record new visits without re-embedding unchanged source text.
Chronological pagination returns each source once at its latest visit within
the requested interval. A linked source/hour chain allows phase-7 erasure to
drain one hour atomically with its month membership and chain reference;
interrupted erasure resumes before publishing the store. Original phase-7
journals from schema 3 remain compatible. Visit history currently remains local
to this device; cross-device visit replication and legacy timestamp provenance
remain separate acceptance work.

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
host, and verifies both signatures. The baseline foreground AX pipeline now
includes Safari and other recognized browsers without an extension; actual
coverage still needs native acceptance for each browser. The optional extension
route has separate identifiers, release signing/notarization, and live Safari
verification outstanding. The complete
proposed direction and gates are in the
[design](superpowers/specs/2026-11-04-blur-design.md) and
[implementation plan](superpowers/plans/2026-11-04-blur-cross-platform-foundation.md).

## Legacy source-linked embedding index

`internal/semantic` chunks sanitized sources on UTF-8 boundaries, embeds them
using a local Ollama embedding model, and keeps an HNSW graph only in process
memory. The graph/heap core is adapted from coder/hnsw under CC0 (see its local
README and license); no graph file import/export API is included. Scoped
queries rank eligible documents before selection, preserving app, domain,
capture-time, and later-sighting filters.

Durable vectors use an authenticated encrypted binary record per source and
an encrypted directory and backfill checkpoint. Model manifest digest,
dimensions, and input format identify each compatible generation. Publication
checks the original source and timestamp in the same transaction. Deletion
removes vectors, directory records, and a matching cursor atomically. Oversized
deletion batches are split only after a classified atomic transaction-limit
rejection; corruption and general storage errors are not retried.

The worker reloads encrypted vectors after restart, resumes source traversal,
and queues only source IDs for new captures and notes. Queue overflow schedules
a repair pass; memory mutations cancel jobs and queries and detach the cached
graph before deletion begins. Background embedding follows power policy;
interactive queries can still use the saved index in Saver mode.

The former `memory-index.json` endpoint/model choice is not part of the standard
product flow. Standard bootstrap leaves the legacy embedding index dormant and
preserves any old settings file without starting its model service. The one
selected provider handles meaning search, answers, and notes; exact search
remains available without it. The encrypted vector implementation remains
source-compatible for legacy and isolated acceptance paths, but must not become
a separate user-facing model choice again.


## Connected answer accounts

ChatGPT uses official public-client OIDC with PKCE and a loopback callback, verified JWKS identity, installation-stable host identity, and separate account registrations. Rotating tokens live only in the injected OS secret store. Responses streaming requires successful terminal completion; partial text, tool output, and mismatched output streams are rejected. The preview API lacks a hard per-request output-token cap, so the adapter explicitly admits local byte/deadline limits instead of sending unsupported fields.

Claude uses the fixed official Anthropic origin, current Messages API, explicit API key and optional workspace, metadata-only model discovery, and bounded schema-validated completion. Consumer Claude login is not offered.

Connection, selection, saved-memory permission, and note-processing permission are distinct. New cloud selections can answer public questions while saved memory is shown locally as quotations until evidence permission is granted. Notes stay local until separate note-processing permission. Permissions bind to provider/account/model/destination/workspace and clear for replacement API keys. Provider transitions cancel registered Ask work before admitting the replacement policy. Personal facts, writing rules, task context, conversation history, background recaps, and automatic Library do not implicitly enter cloud requests.

### Permanent deletion cutoffs and sync admission

Schema 5 stores a sealed maximum deletion cutoff per source and a maximum
all-sources cutoff. They do not evict. Per-source deletion includes a capture
at the cutoff; all-before deletion is strict. Every primary, repeated sighting
and note admission checks the cutoffs inside its write transaction. Prepared
Today projections verify that every referenced primary still exists at commit.
An imported note failure never falls back to a raw observation write. Imported
origin tracking commits with source admission; a later genuine local recapture
clears a stale imported origin.

Each advancing cutoff appends its original deletion time to an encrypted
64-event page using a monotonic local sequence. Sync exports at most 256
changes per read and persists that sequence as its cursor. The runner drains up to 200 segments per pass and queues an immediate continuation for remaining work; caller cancellation and shutdown deadlines apply. Relayed older times
and equal timestamps cannot fall behind a time watermark. An absent legacy
cursor starts at zero; a changed sync folder also resets it. The compatibility
Tombstones snapshot is bounded and is not an admission/export capability.

Startup advances authenticated identity before migrating the intact historical
log in atomic batches of 32. Sealed progress, cutoffs and change pages commit
together; restart resumes the last checkpoint before the store is published.
The completion marker, sequence and final page are checked before publication.
Older clients reject the new identity. Previously evicted historical deletions
cannot be reconstructed, and this migration does not claim to recover them.

Replayed deletion checks the current primary's capture time under the exclusive
erasure gate. Bounded cleanup commits in one atomic transaction; a classified
budget failure rolls back before resumable erasure begins. A newer primary survives. A missing primary still runs derivative cleanup, covering authenticated legacy orphaned projections. The reminder service pauses scheduling
before that gate; linked tasks are read under the gate and their exact native
notices are canceled before the deletion intent commits. A skipped cutoff
does not cancel surviving reminders. Cancellation failure prevents erasure; a
committed erasure journal remains resumable. Import retries recover it inside the client publication window before attempting normal gated reads or writes. Native delivery acceptance is a
separate gate from the deterministic notification-port regressions.
