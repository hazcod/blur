# ADR 0001: Defer encrypted-store selection behind a common gate

Status: proposed; no production backend selected (2026-11-05).

## Context and decision

Blur stores long-lived personal memory. The production `EncryptedStore` must
authenticate persistent data before releasing plaintext. The portable core's
in-process backend must build with `CGO_ENABLED=0` on Darwin, Windows, and
Linux for both amd64 and arm64. A candidate that fails either condition cannot
be the in-process default. The `EphemeralMemoryStore` in this repository is a
test-only conformance backend, reports `encrypted_at_rest=false`, and must not
be used for user data. This ADR selects no winner until evidence from the gate
below is reviewed.

The data-encryption key (DEK) is 32 cryptographically random bytes, separate
from the OS wrapping key. The platform `SecretStore` may report exactly found,
not_found, locked, corrupt, denied, or unavailable. Only an explicit, valid
not_found result authorizes creation. A found key of the wrong size, nil/nil,
unknown state, cancellation, or backend failure fails closed. The current port
has Get/Put, not conditional create. In-process serialization prevents
duplicate creations within the process; desktop bootstrap additionally holds
an owner-only data-directory lock for the lifetime of the opened app, so two
desktop instances cannot race key/store creation or access the same store.
Direct securestore callers remain responsible for cross-process coordination.
The backend must never overwrite a found key. Key material must not enter logs,
JSON, or error text.

## Candidates for the same evaluation

| Candidate | Potential benefit | Current concern / disposition |
| --- | --- | --- |
| cgo-free pure-Go SQLite or SQLite-in-WASM with authenticated page/record envelope | SQL semantics and familiar query planning without an in-process native toolchain | Authentication, WAL and temp-file coverage, SQLite integration, and packaging remain unproven. Evaluate concrete libraries and versions, not the category alone. |
| ncruces Adiantum VFS | cgo-free SQLite-compatible confidentiality path | Adiantum is confidentiality-only here; tampering may release altered plaintext. Experimental reference, not a secure default unchanged. |
| SQLCipher | Mature authenticated-page security/performance reference | Native in-process use conflicts with the no-cgo gate. Measure as an isolated helper/reference only unless that architectural rule changes. Helper IPC, lifecycle, and key exposure add their own gate. |
| Pure-Go record-encrypted KV plus inverted index | Explicit AEAD per record and full control of persistent files | Query, indexing, compaction, transactional consistency, and migration cost are ours to own. Deterministic HMAC tokens can reveal term equality/frequency and access patterns; key rotation must rebuild indexes. |

Encryption alone does not hide record count, access timing, file sizes, or
search patterns. A contentless plaintext index is unacceptable. Deterministic
HMAC search tokens hide the literal token but reveal equality and frequency;
prefix, phrase, and range indexes reveal more structure. Every candidate must
document its leakage model and test the claimed boundaries.

## Identical pass/fail gate

Run the same synthetic corpus, scripts, adversarial fixtures, hardware classes,
and release packaging for every viable in-process candidate. Keep raw results,
profiles, tool versions, platform versions, and failures in the decision record.
The thresholds below are initial acceptance targets from the design; revise
only before a comparison run, with reasons recorded.

1. **Packaging and dependencies:** clean, reproducible `CGO_ENABLED=0` builds
   and signed development packages for Darwin, Windows, and Linux on amd64 and
   arm64. Audit the full dependency tree for cgo and `import "C"`; produce an
   SBOM, license inventory, provenance and update policy. A missing platform
   build fails the in-process gate.
2. **Key-state matrix:** exercise valid/wrong/missing keys and locked, corrupt,
   denied, unavailable, nil, unknown, cancelled, and backend-error states.
   Only explicit not_found can create; no ambiguous result may create, truncate,
   migrate, or open data. Concurrent open/resolve must use one key. Any leaked
   key or secret-bearing error fails.
3. **Authentication and plaintext containment:** flip, truncate, swap, replay,
   and roll back database pages/records, index entries, WAL, journal, temp,
   backup, and export artifacts. A tampered artifact must be rejected before
   plaintext is returned. Scan all managed files and crash residue for known
   plaintext sentinels. Any plaintext residue or unauthenticated release fails.
4. **Durability and recovery:** kill/fault inject at every write, checkpoint,
   rotation, backup, restore, and migration boundary. Recovery must yield the
   previous or next committed state, with no corruption or plaintext residue.
   Test concurrent readers with a single writer, reader backpressure, WAL
   growth, and bounded recovery time. No silent partial commit passes.
5. **Scale and latency:** run identical 100k and 1M observation corpora with
   realistic text sizes and duplicate rates. At 100k, capture-to-durable-store
   p95 must be under 250 ms for a 100 KB frame, recent queries p95 under 50 ms,
   and exact/bounded search p95 under 150 ms. At 1M, exact/bounded search p95
   must be under 500 ms. Measure p50/p95/p99 ingest and search, disk bytes,
   WAL/temp growth, RSS (core target under 150 MB excluding model runtimes),
   and CPU. Typical store target is under 750 MB per 100k observations on the
   published corpus, excluding exports. No unbounded search or list API passes.
6. **Key lifecycle:** verify crash-safe rotation, authenticated encrypted
   backups, restore to a new device, wrong-key refusal, and failure rollback.
   A lost or locked wrapping key must not trigger a replacement DEK. Document
   the limits of deletion on SSDs, OS snapshots, and user backups.

SQLCipher runs the same security, fault, scale, and lifecycle tests as a
reference or isolated-helper option; it does not satisfy the in-process
no-cgo gate by exception. Adiantum runs the same tests, and its expected
authentication failure is recorded, not waived. A future decision will name
specific versions, measured results, residual leakage, and the selected
deployment boundary. No database migration engine or production encryption
implementation is authorized by this ADR.
