# ADR 0002: Select bbolt v1.5.0 behind the record-encrypted physical boundary

Status: accepted — bbolt v1.5.0 selected for the Task 1 physical adapter
(2026-11-05).

This ADR supersedes ADR 0001's deferred selection for the Task 1 physical
adapter. ADR 0001 remains relevant to key lifecycle, `EncryptedStore`
integration, and the unselected alternatives.

## Context and audit contract

The authoritative cross-platform design requires cgo-free shipped binaries
and authenticated persistent records. The physical adapter must expose only
bounded opaque transactions; record-level authenticated encryption is handled
by the standard-library codec in `internal/storage/recordcodec`.

The parent ruling clarifies the no-cgo boundary:

1. Inspect every Blur repository `.go` source file regardless of build tags.
2. For each of the five shipped `GOOS/GOARCH` pairs, run `go list -deps` with
   `CGO_ENABLED=1` and inspect the selected `GoFiles` and `CgoFiles` of every
   non-standard dependency package. Reject selected `CgoFiles` or any selected
   `import "C"`.
3. Files in external dependencies that are ignored on every shipped target
   are not part of the shipped source graph and do not fail this gate.
4. Separately cross-build the physical package and shipped command binaries
   for every target with `CGO_ENABLED=0`.

`internal/nocgoaudit` now implements that scope. Adversarial tests establish
that a selected supported-target `import "C"` fails, an ignored Hurd-only
dependency file does not fail, and a build-tagged repository source is still
scanned even when `go list` omits its directory for the target. New shipped
targets must be added to both `SupportedTargets` and the Makefile's
`CGO_ENABLED=0` physical/command matrices.

The earlier interim audit incorrectly included external `IgnoredGoFiles` and
blocked all bbolt candidates on `golang.org/x/sys/unix/syscall_hurd.go`. That
file is tagged for an unsupported target and is not selected for any shipped
target. Narrowing the dependency scan to the clarified shipped graph is not an
exception: repository source remains fully scanned and the selected-dependency
and release-binary gates remain enforced.

The earlier release hint was also stale. The official
[bbolt release page](https://github.com/etcd-io/bbolt/releases) lists v1.5.0
as latest, with v1.4.3 and v1.3.12 as older stable lines; v1.4.0 is a stable
release and v1.4.0-beta.0 is the pre-release. `go list -m -versions` and
`go mod download -json` independently confirmed the candidate tags and module
origins.

## Decision

Select and pin **`go.etcd.io/bbolt v1.5.0`**. It is the current stable release,
works with the project's Go 1.27.3 toolchain, and passes the clarified source
audit, all five `CGO_ENABLED=0` physical/command cross-builds, the focused
storage security and recovery tests, the full Go suite/race/vet gates, and the
Task 1 physical microbenchmark smoke gate.

Exact module evidence from `go mod download -json`:

- Official repository/tag: `https://github.com/etcd-io/bbolt`,
  `refs/tags/v1.5.0`.
- Commit: `e7a8b2dd498494a3766ba24dd94d3509e5588485`.
- Module checksum: `h1:S7GAl7Fxv12yohbwFfIbQCGDWbQbtDGPET4P/bD4lxU=`.
- go.mod checksum: `h1:mkltfYE5aUHQxUct9N9V+Kp7aSjFqjgrhcXIS70Lrdk=`.
- Upstream `go.mod`: `go 1.25.0`, toolchain `go1.25.11`; compatible with
  local `go1.27.3`.
- License: MIT, confirmed in the tagged
  [upstream license file](https://github.com/etcd-io/bbolt/blob/v1.5.0/LICENSE).

The physical package's runtime non-standard graph is bbolt and
`golang.org/x/sys/unix` (resolved x/sys v0.45.0, BSD-3-Clause). Other modules
in `go list -m all` are bbolt test/tooling dependencies and are not imported by
the physical package runtime graph. `go mod verify` reports all modules
verified. The pinned x/sys source includes `unix/syscall_hurd.go` with
`import "C"`, but that file is ignored for all five shipped targets; the
selected dependency audit and cross-build matrix both pass.

The other stable lines were evaluated as candidates under the earlier, broader
audit and are retained here for traceability, not as selected alternatives:

| Candidate | Official tag commit / module sum | Go directive / license | Runtime dependency | Earlier candidate result |
| --- | --- | --- | --- | --- |
| bbolt v1.4.3 | `68e6b96e6b74ebc396ac1aa7186c92e616960bd1`; `h1:dEadXpI6G79deX5prL3QRNP6JB8UxVkqo4UPnHaNXJo=` | Go 1.23; MIT | x/sys v0.29.0, BSD-3-Clause | Focused storage/codec tests and five-target physical/command builds passed; rejected only by the superseded all-ignored-source audit. |
| bbolt v1.3.12 | `10c954b278eae6155881d1545a64673f93157549`; `h1:UAxZAIuJqzFwByP19gZC3zd5robK3FOangrGS+Fdczg=` | Go 1.23; MIT | x/sys v0.4.0, BSD-3-Clause | Full tests/race/vet and five-target physical/command builds passed; rejected only by the superseded all-ignored-source audit. |

Candidate release/module evidence is linked from the official
[release page](https://github.com/etcd-io/bbolt/releases),
[v1.5.0 go.mod](https://github.com/etcd-io/bbolt/blob/v1.5.0/go.mod), and
[x/sys v0.45.0 source](https://go.googlesource.com/sys/+/refs/tags/v0.45.0/unix/syscall_hurd.go).

## Authenticated record codec and physical contract

The record codec uses Go 1.27's standard-library `crypto/hkdf`, HMAC-SHA-256,
AES-256-GCM, and `crypto/rand`; no extra crypto module is introduced. Version
1 envelopes contain a version byte, fresh 96-bit nonce, and authenticated
ciphertext/tag. Associated data binds the envelope version, namespace, opaque
record key, and nonzero revision. Plaintext is limited to 4 MiB; metadata is
length-limited UTF-8. Errors and formatting are content/key-free. The codec
copies the 32-byte input key, clears that copy after derivation, and best-effort
clears the retained derived key on `Close`.

The physical adapter provides one fixed record bucket and only bounded opaque
byte operations: 4 KiB maximum keys, 5 MiB values, 16 MiB/1024-operation
transaction limits, copied values, concurrent readers, and serialized
single-writer transactions. Unix directory and file modes are enforced as
0700/0600; symlinks and non-regular entries are refused. bbolt's durability
options stay synchronized (all no-sync options false), and its default logger
discards output. No domain observations or higher-level repository API are
exposed.

## Verification evidence

On the local Darwin/arm64 host, the following passed on v1.5.0:

- `go test ./...`, `go test -race ./...`, and `go vet ./...`.
- Focused recordcodec/physical race tests and a two-second fuzz smoke run.
- Tests for RFC 5869 derivation, round trips, wrong key, namespace/key/revision
  AAD binding, single-bit flips, every truncation prefix, oversize/invalid UTF-8
  rejection, key clearing/formatting, and content-free errors.
- Physical tests for permissions, regular-file/symlink refusal, bounded
  transactions, callback rollback, concurrent readers/single writer,
  close/reopen, process kill before and after commit, corruption refusal, and
  sentinel scans over the database, managed temporary artifacts, logs, and
  returned errors.
- `make build cross-build cross-test-compile no-cgo-audit`; all release
  binaries and the physical package cross-built with cgo disabled for
  darwin/arm64, darwin/amd64, windows/amd64, linux/amd64, and linux/arm64.
  The audit additionally found no selected `CgoFiles` for any shipped target
  when listing with cgo enabled.
- Dependency listing, upstream license inspection, `go mod verify`, and
  `git diff --check`.

The physical microbenchmark performs one durable transaction per 1 KiB opaque
value under a unique key. On an Apple M5 Max, Darwin/arm64, the final rerun
measured 6.72 ms/op, 24,063 B/op, and 58 allocations/op. This meets Task 1's
physical-package benchmark requirement; the 100k/1M repository/index scale
benchmarks belong to Task 3 and do not gate this selection.

## Alternatives and deferred scope

ADR 0001's pure-Go SQLite, Adiantum VFS, SQLCipher, and record-encrypted KV
alternatives remain relevant to a later production repository decision. They
were not interchangeable physical candidates for this Task 1 gate: SQLite's
authenticated record/WAL/temp-file boundary is unproven; Adiantum VFS does not
provide tamper authentication; SQLCipher does not satisfy the in-process
no-cgo boundary; and a custom KV engine would add transaction/index/compaction
work outside this task. Older bbolt v1.4.3 and v1.3.12 were mechanically
verified and cross-built, but v1.5.0 is the current stable line and passed the
same clarified audit and runtime gates, so the older lines were not selected.

## Residual risks and limits

- The microbenchmark is a smoke measure, not a cross-OS percentile or a
  100k/1M repository/index workload.
- The nonce uniqueness test samples 512 fresh nonces; GCM nonce uniqueness is
  probabilistic and still needs an explicit key-volume/rotation policy.
- Subprocess termination tests are not power-loss or filesystem-fault
  injection across every OS.
- Unix mode checks are exercised locally; Windows file mode is not an ACL and
  Task 1 does not establish a user-only Windows ACL policy.
- `Lstat` followed by open leaves a race if a caller places the store under a
  directory writable by another principal. Callers must provide a private
  directory; descriptor-based no-follow handling deserves follow-up review.
- This task does not implement DEK wrapping, typed secret-state handling,
  atomic key creation, key rotation, backup/restore, indexing, migration, or
  the higher-level production `EncryptedStore` repository.

These limitations do not invalidate the Task 1 physical-candidate gates, but
must be addressed by subsequent storage/key-lifecycle tasks before claiming a
complete production memory store.
