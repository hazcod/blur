# Foundation threat model

Status: implementation checkpoint, 2026-11-05. This model covers code in the
current Go foundation, not every capability described by the product design.
The demo is not suitable for storing real personal data: its service and
commands still use ephemeral, unencrypted memory and have no native capture
or production durable local service. An internal encrypted durable backend
exists and is tested, but is not yet wired into those entry points.

## Assets

The design protects foreground text and metadata, observation identity and
provenance, grant credentials and HMAC keys, future data-encryption keys,
capability configuration, derived personal facts, and any future provider
credentials. Synthetic/test or caller-supplied values exist in process
memory; durable-store tests also create encrypted temporary database files.
There is no on-disk user store wired into the application or provider secret.

## Trust boundaries

```text
untrusted desktop target / fake adapter
                │ probe metadata; gated bounded read
                ▼
capture package: private raw staging → redactor → validated observation
                │ in-process value boundary only
                ▼
core: scoped proof check → search/store interface
                              ├── ephemeral memory store (wired demo backend)
                              └── internal AEAD durable store (not wired)

CLI ── in-process demo client; no endpoint
future IPC, OS identity integration, production key management, and providers
are not present
```

1. **Desktop target to adapter:** a future native adapter will observe
   potentially hostile content. Current platform adapters do not read native
   targets; test fakes provide probes and readers.
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
5. **Core to persistence:** the demo's only wired backend is explicitly
   unencrypted and ephemeral. The production constructor refuses it. The
   internal durable backend encrypts each versioned record with AEAD, uses
   domain-separated keyed HMACs for physical logical-ID keys, and verifies an
   encrypted keyed record-set manifest. It requires explicit create/open mode,
   has byte-aware non-decrypting page admission, and refuses malformed,
   wrong-key, or unsupported stores without creating or overwriting them. It
   is not yet selected by a production service entry point and depends on a
   caller-provided 32-byte key; OS wrapping, cross-process key coordination,
   backup and key rotation are not implemented.
6. **Core to diagnostics/network:** current commands print bounded structured
   status/capabilities to stdout. No network, AI, logging sink, crash upload,
   or telemetry path is present.

## Attacker goals, controls, and residual risks

| Attacker goal | Enforced control in this foundation | Residual risk or limit |
| --- | --- | --- |
| Read sensitive values from protected or excluded targets | Probe preflight rejects secure, elevated, private and configured app/domain exclusions before invoking the value reader. Unknown capability reports fail closed. | Native adapters are not implemented; future adapters must correctly identify protected state and avoid reading before policy. Exclusions are caller configuration, not an end-user settings UI or organization policy system. |
| Smuggle text through limits or a malformed raw tree | Reader input is checked against byte, node, depth and duration bounds; invalid UTF-8, duplicate/cyclic nodes and excessive declared sizes are rejected. | Memory allocated by an adapter before it returns its raw frame is outside the pipeline's allocation control. No OS read timeout can preempt a reader that ignores context. |
| Get raw data into storage or JSON through forged observations | Raw frame types are package-private; JSON unmarshal into `SanitizedObservation` is rejected; validation checks canonical metadata, redaction fixed point, version, size, counters and content ID at boundaries. | Go visibility is not a sandbox against compromised code in the same process. The exported sanitizer accepts raw text as an in-process caller input; it sanitizes heuristically but cannot make arbitrary input safe. No wire protocol is implemented. |
| Recover a known secret using patterned capture | Redaction handles a selected deterministic set of recognizable patterns before observation construction and counts rules, not matched values. | Heuristics can miss novel formats, unlabeled prose, split values, encodings, images, application-specific secrets, or data added after redaction. Redaction does not establish confidentiality or make the demo appropriate for real secrets. |
| Forge, broaden, or replay a local operation | Proofs bind grant/client/scope/timestamp/nonce/request digest; authorizer checks expiration, skew, revocation, constant-time MAC equality, replay and bounded nonce capacity. Scope vocabulary is closed. | All state and credentials are in process memory; there is no peer identity, endpoint ACL, secure provisioning, persistence or revocation across restarts. Compromise of the process or credential can authorize its permitted scopes. |
| Exhaust memory or CPU through grants or search | GrantStore holds at most 1,024 grants and prunes expired grants on issuance/authorization; removal clears nonce/scope maps and overwrites the authenticator key first. Search limits each request to 64 observations and 2 MiB of scanned text/app/domain bytes, fetches at most four rows per byte-bounded page, and the core admits at most two concurrent searches with context-aware waiting. | The caps still permit bounded transient work and memory use. Key overwrite is best-effort in Go process memory and cannot erase prior copies, runtime artifacts, swap, or credentials held by callers. A request waiting for a search slot has already consumed its one-use proof. |
| Expose persistent memory from a lost device | Current demo retains memory only in process and reports `encrypted_at_rest=false`; production constructor refuses the in-memory store. The unintegrated durable backend encrypts records at rest when given a data key. | There is no production-wired persistent store or OS wrapping key, backup, or device-loss key recovery. Users should not treat the demo as safe storage. |
| Read or alter a durable database file | The internal durable backend uses AES-GCM authenticated records, binds ciphertext to the physical key/namespace/revision, HMACs logical IDs, and verifies an authenticated keyed record-set manifest on open. Wrong keys, tampering, record swaps, unauthorized insertions/deletions, and interrupted migration are covered by tests. The physical adapter enforces private file/directory boundaries and durable bbolt transactions. | The backend is not wired into the demo or production service. Whole-store rollback to an earlier consistent file is not detected. Anyone who obtains the data key can decrypt or forge records; process memory, key storage/wrapping, cross-process creation coordination, backup/restore, rotation, and deletion are outside this phase. Filesystem snapshots and storage media may retain prior ciphertext. |
| Leak data through errors, status or logs | Capture, secret-store, and durable-store errors suppress payloads and backend details; report vocabularies and CLI output are bounded and content-free; secret-bearing grant/proof/store types redact formatting or refuse serialization. | No general logger/crash/diagnostics implementation exists. Future logs, traces, crash dumps, test output and support bundles need separate review. In-process memory remains readable to a process-level attacker. |
| Trigger hidden screenshot, OCR or network fallback | No screenshot, OCR, clipboard, keystroke, network client, AI/provider code, or native capture path is wired. Unavailable adapters return disabled/error states. | Future changes could add paths; review and CI need to preserve the explicit no-fallback contract. |
| Use dependency/native code to cross the cgo boundary | The runtime store graph uses bbolt and x/sys; cgo-disabled cross-builds and the repository audit reject selected Cgo files or `import "C"` in non-standard dependencies for shipped targets. | This does not establish supply-chain provenance, signed artifacts, license policy, JavaScript dependency posture, or future package safety. `govulncheck` was unavailable at this checkpoint. |

## Local grant proof lifecycle

A grant is created for one valid client ID, one or more closed-vocabulary
scopes, and a positive TTL. The credential is returned to that client once;
the store retains a SHA-256-derived key used for HMAC proofs. Each proof binds
the grant ID, client ID, requested scope, canonical timestamp, a random
24-byte nonce, and an operation digest. Authorization checks the grant's
issuance and expiration times, allowed clock skew, revocation flag, scope,
MAC, and nonce state before consuming that nonce.

Replay state is bounded per grant. When full, authorization rejects new
proofs; it does not discard old nonce values to make room. Revocation removes
the record immediately after clearing nonce and scope maps and overwriting the
authenticator key bytes. Expired records are denied and cleared when
authorization encounters them; issuance also prunes expired/revoked records
before enforcing the hard 1,024-record total cap. These writes are best-effort
in-process cleanup, not a guarantee that Go's runtime, prior copies, swap,
crash dumps, or caller-held credentials are erased. Current grants, credentials,
and replay history exist only in `GrantStore` memory and vanish on process exit.
This is not a transport-authentication design by itself: future IPC must add
endpoint access control and OS peer identity, then bind those identities to
grant provisioning.

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
Capture also doesn't perform window-title or full URL extraction. The
development adapters never read at all.

## Platform failure behavior and Wayland

Darwin, Windows and Linux capture adapters report `user_disabled` with
`adapter_unimplemented`; default capability reports use
`foundation_only`/`adapter_not_connected`. Their operations fail closed. The
portable capability vocabulary has room for permission, secure/elevated
target, unsupported target, missing portal, and temporary unresponsive states;
the foundation does not yet negotiate them against live OS services.

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
- The internal durable store has no backup, export, rotation, or deletion
  operations. No guarantee is made about erasure from SSDs, filesystem
  snapshots, backups, swap, hibernation, crash dumps, or later persistent
  stores. A future delete/key-destroy design must document those limits and
  test authenticated recovery.
- No native capture, UI, AI, MCP, writing, dictation, production-wired durable
  store, provider egress, or user-facing privacy controls are implemented. The
  design and ADR describe requirements and deferred gates, not shipped
  controls.
- Capability status and content-free errors reduce accidental exposure; they
  are not a substitute for OS-level process isolation or independent security
  review.

## Review gates before expanding trust

Before native capture: adversarial accessibility fixtures must verify secure
target and exclusion behavior, budgets, cancellation, redaction, and sentinel
absence from outputs/errors. Before native IPC: review ACL/peer evidence,
grant issuance/revocation, bounded decoding, restart semantics and race/fuzz
tests. Before wiring persistent user data: integrate the internal backend
with production key management and cross-process creation coordination, then
re-validate tamper, crash, key-loss, migration, rollback, backup and
plaintext-residue behavior. Whole-store rollback remains a documented
residual risk. Before provider code: define feature-specific disclosure and
validate network admission, redirect, certificate, credential and
prompt-injection behavior.
