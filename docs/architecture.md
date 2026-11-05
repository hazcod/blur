# Implemented architecture

This document describes code present in the cross-platform foundation as of
the 2026-11-05 release checkpoint. The broader product design is a roadmap,
not a description of shipped behavior.

## Runtime shape

The current runtime is one Go process. `blurctl status` and
`blurctl capabilities` create an in-process demo client, invoke the service,
print bounded JSON, and exit. `blur-core` prints the same status shape. There
is no daemon, local socket, named pipe, GUI, MCP server, or provider process.
The demo service uses `EphemeralMemoryStore` and explicitly reports
`mode=ephemeral_memory`, `encrypted_at_rest=false`, `ai_enabled=false`, and
`network_listening=false`.

```text
blurctl / blur-core
        │ in process
        ▼
core service ── scoped GrantStore proofs
        ├── exact-term search ── EncryptedStore interface
        │                        └── EphemeralMemoryStore (current demo)
        └── structured capability reports

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
adapters are placeholders; each returns `user_disabled` with
`adapter_unimplemented`. They do not invoke AXUIElement, UI Automation, or
AT-SPI2. Other ports are not connected to a native host and default to
`foundation_only` / `adapter_not_connected`.

### Capture and observation boundary

`internal/capture` owns its raw frame and node types; these have no exported
representation and cannot be JSON-decoded into a trusted observation. A
pipeline checks capability, probes only bounded non-content metadata, validates
the probe, rejects secure/elevated/private or explicitly excluded targets,
then calls the bounded reader. It enforces caller-supplied byte, node, depth,
and duration limits and redacts before it constructs a `SanitizedObservation`.
Capture errors do not include underlying reader errors or captured values.

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
proof binds grant ID, client ID, scope, timestamp, nonce, and operation digest.
The verifier checks scope, grant time bounds, clock skew, expiry, revocation,
MAC, duplicate nonce, and bounded per-grant replay state. Expiry denies new
requests; explicit revocation invalidates and removes the grant. Revocation
and expiry clear nonce/scope maps and overwrite the retained authenticator key
before the record is deleted. Issuance prunes expired records and rejects new
grants at the hard 1,024-record cap. Proofs are single-use within the
in-memory store and the nonce capacity limit rejects new proofs rather than
forgetting old nonces. There is no global install token.

This mechanism is currently exercised only in process. `MemoryTransport` is a
bounded conformance transport, not an OS IPC implementation; endpoint
descriptors declare user-only access and peer-evidence requirements but do not
enforce ACLs or peer credentials. Unix-domain sockets, Windows named pipes,
peer UID/token checks, grant provisioning across processes, and durable grant
revocation remain deferred.

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
ceilings before fetching/decrypting a record that will not fit. A single
internal transaction extension boundary is available to later index
materializers.
This backend is not yet wired into `NewService` or the demo commands; those
still use the ephemeral store and remain unsuitable for real personal data.
The store does not protect against whole-file rollback, compromised process
memory, or loss/theft of the data key. See [ADR 0002](adr/0002-durable-store.md).

Search is deterministic exact-term matching, capped at 64 observations and
2 MiB of aggregate text/app/domain bytes per request; an omitted request byte
limit uses that same hard maximum. It fetches at most four observations per
page through a byte-bounded store contract; the store reports
page bytes and whether more records remain before returning copies. The core
service permits at most two searches at once and waits for a slot with context
cancellation. Results, snippets, serialized response bytes, and provenance
content IDs are bounded too. Search does not provide persistent indexing,
fuzzy or semantic search, timeline, summaries, or AI answers. Derivation and
generation types record provenance metadata, but the demo does not generate
or persist derived content.

### Secrets and diagnostics

`internal/securestore` defines a 32-byte data-key value that resists accidental
formatting or serialization. Only an explicit typed `NotFound` may authorize
key creation; found-but-malformed, locked, corrupt, denied, unavailable,
ambiguous, or failed states do not create or overwrite a key. A process-local
gate serializes Get-then-Put, but it is not atomic across processes. This
contract is not connected to Keychain, DPAPI, Secret Service, an OS wrapping
key, or an encrypted database.

Capability reports, capture errors, grant/proof formatting, store errors, and
CLI failures use stable codes or generic messages rather than payloads. Status
reports expose mode, encryption, AI/network booleans, readiness, and version;
capability output contains fixed IDs, states, reasons, and evidence codes.
There is no diagnostics service or logging pipeline to claim as implemented.

## Enforced foundation controls

- No network listener or provider code is present; the current commands run
  offline and report network listening disabled.
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
- Command binaries and transitive non-standard dependencies pass the current
  cgo-disabled build and Cgo/import-C audit matrix.

These controls constrain this foundation. They do not establish a complete
desktop security boundary, persistent privacy guarantee, or product workflow.

## Deferred product phases

Native accessibility reads and target leases; OS secret stores and data-key
wrapping; production wiring of authenticated persistence; single-instance and
key-creation coordination;
native IPC, ACLs and peer identity; GUI and onboarding; pause UI and durable
exclusions; real diagnostics and log sinks; AI/provider routing; MCP; writing,
dictation, audio; histories, chronicles, wiki, scheduling; export/import;
retention and deletion; authenticated backups and key rotation; packaging,
signing, release SBOMs; and platform fixture applications remain unimplemented.
The complete proposed direction and gates are in the
[design](superpowers/specs/2026-11-04-blur-design.md) and
[implementation plan](superpowers/plans/2026-11-04-blur-cross-platform-foundation.md).
