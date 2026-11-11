# Blur foundation and durable-core threat model

Status: desktop-client checkpoint, 2026-11-07. This model covers code in the
current Go desktop and durable core, not every capability in the product
design. The regular CLI demo commands still use ephemeral, unencrypted memory;
the macOS desktop uses an encrypted durable store and Keychain-wrapped key.
`blurctl fixture` can read only explicitly marked synthetic stores in the OS
temporary directory using a public fixture key. Selected changes have received
scoped reviews; this is not a comprehensive independent security assessment
or a security guarantee.

## Assets

The design protects foreground text and metadata, observation identity and
provenance, grant credentials and HMAC keys, future data-encryption keys,
capability configuration, derived personal facts, and any future provider
credentials. Synthetic/test or caller-supplied values exist in process
memory; durable-store tests also create encrypted temporary database files.
The desktop has an on-disk encrypted store and may hold an explicitly
configured remote-provider credential in OS secret storage.

## Trust boundaries

```text
untrusted desktop target / fake adapter
                │ probe metadata; gated bounded read
                ▼
capture package: private raw staging → redactor → validated observation
                │ in-process value boundary only
                ▼
core: scoped proof check → search/store interface
                              ├── ephemeral memory store (regular demo)
                              └── AEAD durable store (desktop and fixtures)

CLI ── in-process demo client or explicit fixture-only durable reader; no endpoint
desktop ── encrypted durable client; native OS secret adapters; optional note model
optional Safari ingest uses a bounded AF_UNIX endpoint; process isolation is
not present. An opt-in, read-only Claude MCP bridge uses a separate bounded
AF_UNIX endpoint on macOS/Linux or a user-DACL named pipe on Windows and the
already-open desktop store owner; its CLI process never opens the database.
```

1. **Desktop target to adapter:** native adapters may observe potentially
   hostile content. macOS, Windows, and Linux have accessibility-backed
   capture paths; live Windows and Linux desktop testing remains open.
2. **Adapter to capture:** the probe is restricted to app ID, domain, opaque
   target ID, and secure/private/elevated flags. It is not itself content. A
   configured capture reader runs only after capability validation and
   preflight exclusions.
3. **Raw staging to sanitized observation:** raw frame/node types are private
   to `capture`; reads have byte/node/depth/time bounds; deterministic
   redaction precedes construction; validation is repeated at the store/core
   boundary. This is a type/package boundary in one process, not hostile-code
   isolation. Capture owns a deterministic bounded binary persistence codec;
   the durable backend authenticates its bytes with AEAD before calling
   restore, which revalidates canonical identity and the current redactor.
4. **Client to core:** in-memory grants bind a client, scope, expiry and
   request semantics through a one-use HMAC proof. Current CLI access is an
   in-process demonstration and no grant crosses IPC.
5. **Core to persistence:** CLI demo backends are explicitly unencrypted and
   ephemeral; the desktop opens the encrypted durable store through an OS
   secret adapter and refuses plaintext fallback. The
   durable backend encrypts each versioned record with AEAD, uses
   domain-separated keyed HMACs for physical logical-ID keys, and verifies an
   encrypted keyed record-set manifest. The persistent store identity and
   per-record schema context are authenticated in AEAD associated data. It
   requires explicit create/open mode, authenticates only a small sealed size
   record before deciding whether to fetch/decrypt an observation, and refuses
   malformed, wrong-key, or unsupported stores without creating or overwriting
   them. It depends on a 32-byte key; the desktop wraps it with platform secret
   storage. Desktop bootstrap holds a per-user lock during app lifetime to
   coordinate key creation and prevent another instance opening the same
   store; direct library callers must coordinate themselves. Backup and key
   rotation are not implemented. The explicit fixture CLI reads Today,
   knowledge, and bounded doctor samples from a caller-selected synthetic
   store path below the OS temp directory.
6. **Core to diagnostics/network:** lifecycle jobs use bounded queues,
   coalescing and cancellation; doctor output uses capped metadata samples and
   closed content-free vocabularies. A conditional AF_UNIX listener exists only
   for configured, user-started Safari ingestion; there is no crash upload or
   telemetry path. Note processing sends sanitized user-authored notes only
   after explicit addition. Ask separately retrieves at most eight encrypted,
   exact-match sources and bounds the evidence packet. It uses loopback Ollama
   by default; an OpenAI-compatible endpoint receives captured evidence only
   after a separate persisted Privacy-settings opt-in, cleared when endpoint or
   model changes. Remote endpoints require HTTPS and credentials stay in the
   OS secret store. Local and remote providers may retain request logs or model
   state; remote-provider retention is outside Blur's control. Background
   recaps and knowledge derivation remain local-only. There is no general
   logging sink.

## Attacker goals, controls, and residual risks

| Attacker goal | Enforced control in this foundation | Residual risk or limit |
| --- | --- | --- |
| Read sensitive values from protected or excluded targets | Probe preflight rejects secure, elevated, private and app-excluded targets before reading structure. On macOS, browser domain policy uses only the active focused window's `AXDocument` host; it is checked before page-text values and revalidated around each deferred read. An unknown host with configured domain rules fails closed. The private preference file is bounded and atomically replaced; changes pause capture before the next runner loads the new policy. Unknown capability reports fail closed. Native adapters revalidate focused-target identity around bounded reads. | Correctly classifying every protected/private target remains difficult; AXDocument host availability and private-window classification need live browser/version validation. Exclusions are identifiers/domains rather than app-specific semantic rules, and they do not remove previously captured history. |
| Smuggle text through limits or a malformed raw tree | Reader input is checked against byte, node, depth and duration bounds; invalid UTF-8, duplicate/cyclic nodes and excessive declared sizes are rejected. | Memory allocated by an adapter before it returns its raw frame is outside the pipeline's allocation control. No OS read timeout can preempt a reader that ignores context. |
| Get raw data into storage or JSON through forged observations | Raw frame types are package-private; JSON unmarshal into `SanitizedObservation` is rejected; validation checks canonical metadata, redaction fixed point, version, size, counters and content ID at boundaries. | Go visibility is not a sandbox against compromised code in the same process. The exported sanitizer accepts raw text as an in-process caller input; it sanitizes heuristically but cannot make arbitrary input safe. No wire protocol is implemented. |
| Recover a known secret using patterned capture | Redaction handles a selected deterministic set of recognizable patterns before observation construction and counts rules, not matched values. | Heuristics can miss novel formats, unlabeled prose, split values, encodings, images, application-specific secrets, or data added after redaction. Redaction does not establish confidentiality or make the demo appropriate for real secrets. |
| Forge, broaden, or replay a local operation | Proofs bind grant/client/scope/timestamp/nonce/fresh connection challenge/request digest; authorizer checks expiration, skew, revocation, constant-time MAC equality, replay and bounded nonce capacity. Scope vocabulary is closed. The default-off Unix endpoint checks an owner-only parent, socket mode `0600`, and kernel-reported same-UID peers. The Windows pipe uses a protected DACL granting access only to the current SID, rejects remote clients, and verifies the peer process token SID. The persistent MCP credential is stored in the OS secret store and restored only for the bounded read scope. | This is same-user authorization, not process isolation: other code running as the same user can reach the endpoint, and compromise of Blur or the credential can authorize its permitted reads. Windows peer inspection may fail closed under restricted process-token access; runtime validation on Windows is required. |
| Exhaust memory or CPU through grants or search | GrantStore holds at most 1,024 grants and prunes expired grants on issuance/authorization; removal clears nonce/scope maps and overwrites the authenticator key first. Search limits each request to 64 observations and 2 MiB of scanned text/app/domain bytes, fetches at most four rows per byte-bounded page, and the core admits at most two concurrent searches with context-aware waiting. | The caps still permit bounded transient work and memory use. Key overwrite is best-effort in Go process memory and cannot erase prior copies, runtime artifacts, swap, or credentials held by callers. A request waiting for a search slot has already consumed its one-use proof. |
| Expose persistent memory from a lost device | CLI demo retains memory only in process and reports `encrypted_at_rest=false`; the desktop stores records, capture timestamps, and retention policy using authenticated encryption and an OS-wrapped key. Unconfigured stores default to 90-day pruning; users may choose 30/90/180/365 days or explicitly turn automatic pruning off. A two-step logical delete-all is also available. | A compromised/unlocked account or process can access data. Backup/restore, key rotation, and device-loss recovery are not implemented. Logical deletion does not guarantee physical erasure from database freelists, snapshots, backups, swap, or media. Never use the fixture path for personal data. |
| Read or alter a durable database file | The durable backend uses AES-GCM authenticated records, binds ciphertext to store identity, schema context, physical key/namespace and revision, HMACs logical IDs, and verifies an authenticated keyed record-set manifest on open. Wrong keys, tampering, cross-store swaps, unauthorized insertions/deletions, and interrupted migration have tests. The physical adapter enforces private file/directory boundaries and durable bbolt transactions. The exact index encrypts posting contents and HMACs lookup values. | Whole-store rollback to an earlier consistent file is not detected. Index artifacts reveal record counts, tree shape, approximate posting occupancy, same-store equality/linkage for repeated terms, and write timing. Anyone who obtains the data key can decrypt or forge records; direct library callers remain responsible for cross-process coordination, while backup/restore and rotation remain open. Filesystem snapshots and storage media may retain prior ciphertext. |
| Leak data through errors, status or logs | User-facing capture/storage errors and status are bounded; no telemetry, remote crash upload, or support-bundle upload exists. macOS writes a bounded rotating app log, startup trace, and runtime stderr under the per-user Logs directory (cache fallback); Windows GUI writes a bounded rotating app log and a per-run crash log under the user's config directory, retaining one previous crash log. Windows protects its Blur/Logs directories and log files with an explicit protected DACL granting full control only to the current user and SYSTEM; the Windows regression checks the resulting ACL. The MCP console companion keeps stdout/stderr for its protocol. Linux startup diagnostics remain on stderr. Captured UI text and model prompts are not intentionally logged. | Lower-level error strings can expose local paths, native error details, or backend state; treat diagnostics as private. Windows native ACL behavior still requires execution on Windows CI. Crash output is local and not uploaded, but a panic may include sensitive values. In-process memory remains readable to a process-level attacker. Future logging, traces, crash output, test output, and support bundles need separate review. |
| Trigger hidden screenshot, OCR or network fallback | No screenshot, OCR, clipboard, keystroke, or hidden capture listener is wired. Note AI and Ask are separate explicit actions with bounded inputs; Ask uses loopback Ollama by default, while sending captured evidence to a remote compatible endpoint requires a separate persisted opt-in that is cleared when endpoint/model changes. Remote providers require HTTPS and user-authored notes are processed only when saved. Unavailable adapters return disabled/error states. | Local and remote model processes are separate trust boundaries and can retain request logs/model state. Remote providers may retain note and explicitly consented Ask requests under their own policies. Future changes could add paths; review and CI need to preserve explicit opt-in and no-fallback controls. |
| Use dependency/native code to cross the cgo boundary | The runtime store graph uses bbolt and x/sys; cgo-disabled cross-builds and the repository audit reject selected Cgo files or `import "C"` in non-standard dependencies for shipped targets. The 2026-11-07 `govulncheck` run found no known vulnerabilities. | This does not establish supply-chain provenance, signed artifacts, license policy, JavaScript dependency posture, freshness of future advisory data, or future package safety. |

## macOS App Sandbox (default)

`make macos-app` (default `MACOS_SANDBOX=1`; `MACOS_SANDBOX=0` builds unsandboxed) signs the desktop app with the App Sandbox
(`platform/macos/Blur.sandbox.entitlements`). It mitigates a compromised or
buggy Blur process (or a hostile page/model output reaching it) reading or
writing the rest of the user's home directory: the process is confined to its
container (`~/Library/Containers/com.hazcod.blur`), outbound network is limited
to the client entitlement, and only user-picked folders (sync) are otherwise
writable. It does not weaken or replace the encrypted store, Keychain key, or
local-API grant model, and it is not a defence against same-user processes that
can already read the container. The sandbox does not remove the need for the
macOS Accessibility (TCC) permission, which is still required for capture and
is tied to the bundle ID and signing identity; Blur cannot grant it.

Residual risk: the entitlements carry a temporary exception,
`com.apple.security.temporary-exception.files.home-relative-path.read-only`
for `/Library/Application Support/Blur/` and `/Library/Logs/Blur/`, so a
sandboxed launch can copy a pre-sandbox store into the container. Migration is
latest wins: on every sandboxed launch, if the legacy `memory.db` is newer than
the container's (an unsandboxed run kept writing it) or the container has none,
the legacy store and preferences replace the container copy through a temporary
directory and atomic renames; legacy data is only read. While the exception is
present a compromised process can read those two legacy locations, so it
remains a development-build exception to remove once unsandboxed builds are no
longer used.

`MACOS_SANDBOX_DEV_PATH` adds a second, developer-only exception,
`com.apple.security.temporary-exception.files.absolute-path.read-write` for one
caller-chosen directory, so UI snapshots can be written where a shell can read
them. It is empty by default, absent from the checked-in entitlements (a
`make safari-extension-test` self-check enforces both), emits a build warning,
and must never be set for builds that are used with real data or distributed.

Socket location: the container path is too long for `sun_path` (103 bytes,
`EndpointDescriptor.Validate`), so the local MCP socket is
`<confstr(_CS_DARWIN_USER_TEMP_DIR)>/blur-mcp/claude-mcp.sock`. That per-user
directory is sandbox-accessible and outside the container; Blur creates
`blur-mcp` as a 0700, current-user-owned, non-symlink directory (tightening or
rejecting anything else), the transport requires an owner-only parent, sets the
socket to 0600 and rejects peers with another UID, and the MCP owner still
requires the Keychain-backed grant. Other processes of the same user can reach
the socket path but not authenticate without the grant.

## Local grant proof lifecycle

A grant is created for one valid client ID, one or more closed-vocabulary
scopes, and a positive TTL. The credential is returned to that client once;
the store retains a SHA-256-derived key used for HMAC proofs. Each proof binds
the grant ID, client ID, requested scope, canonical timestamp, a random
24-byte nonce, a fresh per-connection server challenge, and an operation
digest. Authorization checks the grant's
issuance and expiration times, allowed clock skew, revocation flag, scope,
MAC, and nonce state before consuming that nonce.

Replay state is bounded per grant. When full, authorization rejects new
proofs; it does not discard old nonce values to make room. Revocation removes
the record immediately after clearing nonce and scope maps and overwriting the
authenticator key bytes. Expired records are denied and cleared when
authorization encounters them; issuance also prunes expired/revoked records
before enforcing the hard 1,024-record total cap. These writes are best-effort
in-process cleanup, not a guarantee that Go's runtime, prior copies, swap,
crash dumps, or caller-held credentials are erased. Grant authorization and
nonce history exist only in `GrantStore` memory and vanish on process exit; the
fresh per-connection challenge prevents a previously captured proof from being
replayed after restart without persisting that history.
On macOS, Linux, and Windows, browser Accessibility capture reads the active
foreground window without navigation. It ignores address-bar, tab, toolbar,
and form-entry roles and checks the browser window title before and around
each value read. Windows carries only a private/eligible bit in its short-lived
target lease; raw titles are not persisted or logged. Missing or unreadable
titles fail closed. This blocks recognized localized private-mode markers,
but a browser/version whose private mode does not expose a recognized title
marker is a residual privacy risk; the supported-browser runtime matrix is not
complete. On macOS, `AXDocument` is read from the focused browser window to
resolve domain policies; only the normalized hostname is retained. The URL's
path, query, fragment, and credentials are not persisted or logged, and no
browser history or other tab is queried. Runtime availability of this attribute
still needs validation across supported browsers. The optional
Safari bridge stores its one-day client credential in an explicit
shared Keychain access group, deletes that item on pause, and requires the
signed App Group container plus owner-only same-UID AF_UNIX endpoint. Without
both configured entitlements, or without the separate default-off Safari page
capture preference and global capture consent, bridge startup fails closed.
The UI reports whether the bridge is ready or unavailable without including
page content in status notifications. The Swift Safari
extension builds as an unsigned universal `.appex`; an opt-in packaging path
checks separate host and extension provisioning profiles for bundle identity,
team, signing certificate, and the restricted Keychain access group (without
incorrectly requiring profile authorization for unrestricted App Group or
sandbox entitlements), signs inside-out, and verifies both signatures. The default app does not embed it, no real
provisioning profiles or notarized release artifact have been tested, and Safari
has not been exercised end-to-end. This boundary is not yet production browser
capture.

Optional hourly recaps use only the configured local Ollama endpoint under a
separate default-off setting and fresh power-policy admission. The generated
recap may include at most one missing-follow-up question; storage rechecks that
its cited source exists and contains a conservative meeting cue, then writes
the recap and answerable Today question atomically under encryption. It cannot
create tasks or trigger external actions. Prompt injection in captured text can
still influence the wording of this user-reviewed question, so the UI keeps the
original source reviewable and allows dismissal.

## Search resource bounds

Each search request binds its normalized terms, result/snippet/response limits,
row scan limit, and aggregate scan-byte limit into the proof digest. An omitted
byte limit selects the same 2 MiB hard maximum as an explicit one. The scan
limit is at most 64 observations and the byte limit at most 2 MiB over each
observation's text, app ID, and domain. Store pages carry both a row limit and
a byte limit, and return exact scanned-field bytes plus explicit `HasMore`
metadata. The in-memory store indexes those byte lengths at ingest, so an
observation that does not fit is not cloned into the returned page. Search
fetches at most four rows at a time. Search reports bytes actually scanned,
marks partial scans as truncated, and counts `Omitted` only for matching
records it saw but could not return. The core
service permits two concurrent searches; waiters can cancel without holding a
slot. These limits bound app-level work but do not preempt Go/runtime or OS
allocation outside the store contract.

## Capture staging and redaction limits

The pipeline's order is `Capability → Probe → validate/exclude → bounded read
→ frame validation → sanitize → validated observation`. Secure, elevated,
private, excluded, invalid, unavailable, and timed-out preflight states return
without calling the value reader. Raw frame and node types are unexported and
have no serializable public DTO. Only the sanitized observation type is
accepted by storage and core operations.

Redaction runs deterministically before validated data leaves the capture
package. Fixed-point validation rejects text that the current redactor would
still alter. Recognized patterns include private-key blocks, authorization
headers, selected token forms, labeled secrets, common URL credentials and
sensitive query values, checksum-valid payment identifiers, and SSN-shaped
values. It does not recognize every secret and is not a DLP classifier.
Capture also doesn't perform window-title or full URL extraction. Windows UIA
reads are foreground-lease-bound and use bounded TextPattern reads; live native
Windows behavior remains unverified at this checkpoint.

## Platform failure behavior and Wayland

Darwin capability checks report Accessibility permission requirements without
prompting; when capture is enabled, startup may ask macOS once to display its
own permission guidance. Blur never modifies TCC state. Linux
reports private-bus/AT-SPI availability; Windows wires foreground identity,
secure-desktop/elevation checks, WinEvent notifications, and bounded UIA reads.
The Windows capability currently reports the implemented boundary available;
native runtime and fixture-app validation remain open. All adapters fail
closed for unknown, secure, elevated, excluded, or stale targets. Other host
ports default to `foundation_only`/`adapter_not_connected`.

The design targets Wayland sessions, where global shortcut and geometry
facilities may depend on desktop portals and compositor policy. There is no
Wayland integration here. When added, missing portal/accessibility support
must be reported as unavailable or degraded. Screenshot capture, OCR,
clipboard mutation, synthetic input, or remote AI must never silently replace
an unavailable accessibility or desktop capability.

## Assumptions and explicit exclusions

- The host process, Go runtime, OS kernel, and code running inside the process
  are trusted for this foundation. An administrator/root user, kernel
  compromise, debugger with process access, or hostile code injection can read
  process memory and is out of scope.
- Application demo data is ephemeral. Process exit loses its observations and
  grants; this does not count as secure deletion because copies may exist in
  runtime memory, swap, hibernation, crash dumps, or host diagnostics.
- Source deletion and its indexed derived-item cascades are implemented, and
  automatic retention defaults to 90 days for unconfigured stores and can be
  explicitly set to Off. Export, backup/restore, and key rotation are not. The
  explicit delete-all action performs logical deletion but does not guarantee
  erasure from SSDs, filesystem snapshots, backups, swap, hibernation, crash
  dumps, or later persistent stores.
- Activity, Today, knowledge, lifecycle, and diagnostics connect to desktop
  storage and native capture, but automatic knowledge derivation remains open.
  The native UI has quick notes and explicit local/remote note processing, but
  a narrow, default-off MCP search tool, but no dictation. Claude's tool can
  return saved text to its host/provider; this is explicit user consent, not a
  guarantee that data stays on-device after the result leaves Blur. The design
  and ADR describe broader deferred capabilities, not shipped controls.
- The MCP tool exposes only exact `search_memory`. Requests are authenticated
  with a Keychain/OS-secret-backed one-year read grant and challenge-bound
  HMAC/replay checks;
  the user-only socket is active only while the preference is enabled and the
  desktop owner process runs. It is not process isolation from other code
  running as the same user. Explicit disable revokes the grant and deletes its
  stored credential. Windows uses the current user's protected named-pipe ACL
  and peer-token verification; native Windows runtime verification remains a
  release gate.
- Opt-in sync across Macs (default off) exports observations, notes, and
  deletion tombstones as AES-256-GCM segments (key: PBKDF2-HMAC-SHA256, 600,000
  iterations, from a user passphrase; kept in the OS secret store, never the
  device store key) to a user-chosen folder carried by a file-sync provider.
  The provider, and anyone who can read that folder, sees segment sizes
  (padded to 64 KiB classes), timing, and device count, and may keep copies
  after deletion; a weak passphrase is exposed to offline guessing. Wrong keys
  and tampering fail closed. Activity and time recaps are not replicated.
- Capability status and content-free errors reduce accidental exposure; they
  are not a substitute for OS-level process isolation or independent security
  review.

## Review gates before expanding trust

Before native capture: adversarial accessibility fixtures must verify secure
target and exclusion behavior, budgets, cancellation, redaction, and sentinel
absence from outputs/errors. Before adding general IPC: review ACL/peer
evidence, grant issuance/revocation, bounded decoding, restart semantics and
race/fuzz tests. Before release of persistent user data, continue validating
production key management, cross-process creation coordination, tamper,
crash, key-loss, migration, rollback, backup and plaintext-residue behavior
against release builds. Whole-store rollback remains a documented residual
risk. Safari bridge shipping additionally requires signed entitlements and
live host-runtime validation. The Ask provider path is loopback-only, refuses redirects and
proxies, bounds request and response bytes, admits only a bounded evidence
packet, treats evidence as untrusted input, and validates citations against the
exact retrieved source IDs. Runtime testing against an installed Ollama model
and a comprehensive independent security assessment remain release gates.
