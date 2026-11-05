# Task 1 implementation report — durable store and record codec

Date: 2026-11-05
Status: **DONE_WITH_CONCERNS** — after the audit-scope ruling, bbolt v1.5.0
passes Task 1's clarified gates and is selected in ADR 0002. The initial
blocked assessment is retained below as historical evidence and superseded by
the continuation section.

## Initial outcome (superseded after the audit-scope ruling)

The record codec and bounded opaque physical transaction API were implemented
and locally verified. bbolt v1.5.0, v1.4.3, and v1.3.12 were evaluated as the
latest stable release on each maintained line. Under the initial overly broad
audit, all three had working
`CGO_ENABLED=0` physical-package builds for the five supported target tuples,
but all three bring in `golang.org/x/sys/unix/syscall_hurd.go`, which contains
`import "C"`. The unchanged `make no-cgo-audit` scans ignored as well as
selected source files and fails on this file. Per the hard gate, no candidate
is selected and no audit exception is proposed. The detailed decision is in
[ADR 0002](../../../docs/adr/0002-durable-store.md).

At that initial stage, `go.mod` reflected the last tested candidate, bbolt
v1.3.12, only so the blocked implementation remained reproducible. The
continuation below supersedes that trial pin with the selected v1.5.0.

## Initial file inventory (candidate status superseded below)

- `internal/storage/recordcodec/recordcodec.go` and
  `internal/storage/recordcodec/recordcodec_test.go`: standard-library
  HKDF-SHA-256 key derivation, AES-256-GCM versioned envelope, random nonce,
  AAD binding of namespace/key/revision, fixed size limits, UTF-8 metadata
  validation, content-free errors/formatting, best-effort key clearing, and
  focused tamper/fuzz tests.
- `internal/storage/physical/bolt/bolt.go` and
  `internal/storage/physical/bolt/bolt_test.go`: opaque, bounded transactions;
  private regular-file checks; reader/writer behavior; recovery, corruption,
  and sentinel tests; and a small physical-update benchmark.
- `go.mod` and `go.sum`: at this initial checkpoint, the trial pin was bbolt
  v1.3.12. The continuation below records the final selected v1.5.0 pin.
- `Makefile`: adds the physical-package five-target build gate and makes
  `cross-build` include it.
- `.github/workflows/ci.yml`: updates the cross-build check label to reflect
  physical storage; the existing CI command now inherits the physical build.
- `docs/adr/0002-durable-store.md`: initially recorded the blocked decision;
  the continuation supersedes it with the accepted v1.5.0 decision.
- This report at the requested `.superpowers` path.

No unrelated files were edited. The worktree started at baseline
`e542c35654a8b7a753a0f060c4cf37c228ca4aae`.

## TDD record

Tests were added before the corresponding implementations.

- Red: `go test ./internal/storage/recordcodec` failed to compile because the
  codec API/types and derivation function did not yet exist (`RecordMetadata`,
  `deriveKey`, `NewCodec`, and envelope constants were undefined).
- Green: after implementing the codec, `go test ./internal/storage/recordcodec`
  passed. One intermediate assertion expected JSON serialization to succeed;
  it was corrected to expect the specified serialization refusal.
- Red: `go test ./internal/storage/physical/bolt` failed to compile because
  `DB`, `Open`, and the physical error sentinels did not yet exist.
- Green: after implementing the adapter and correcting one assertion to expect
  the transaction-specific `ErrInvalidTx`,
  `go test ./internal/storage/physical/bolt` passed.
- The final candidate-specific trial also exposed that bbolt v1.3.12 predates
  the `Options.Logger` field. The adapter was made compatible with that
  candidate; its focused and full tests then passed. This API difference is
  recorded rather than hidden in the candidate comparison.

## Candidate and dependency evidence

The initial brief's release hint (“v1.3.11 stable; v1.4.0 prerelease”) was
checked against the official [bbolt release page](https://github.com/etcd-io/bbolt/releases).
At evaluation time it listed v1.5.0 as latest and v1.4.3 and v1.3.12 as stable
releases; v1.4.0 is a release, while v1.4.0-beta.0 is marked pre-release.
Candidates were verified independently through `go list -m -versions` and
`go mod download -json`, which reported the official `https://github.com/etcd-io/bbolt`
repository and matching tag origins.

| Candidate | Module Go/toolchain directive | bbolt license | Runtime dependency | Physical build and hard audit |
| --- | --- | --- | --- | --- |
| v1.5.0 (latest) | `go 1.25.0`, toolchain `go1.25.11` | MIT | `golang.org/x/sys v0.45.0`, BSD-3-Clause; imports `x/sys/unix` | Five-target physical build and project builds pass; hard audit fails on ignored Hurd C import. |
| v1.4.3 (latest 1.4 patch) | `go 1.23`, toolchain `go1.23.12` | MIT | `golang.org/x/sys v0.29.0`, BSD-3-Clause; imports `x/sys/unix` | Five-target physical build and focused physical/codec tests pass; hard audit fails on the same ignored Hurd C import. |
| v1.3.12 (latest 1.3 patch) | `go 1.23`, toolchain `go1.23.12` | MIT | `golang.org/x/sys v0.4.0`, BSD-3-Clause; imports `x/sys/unix` | Five-target physical build and full local test/race/vet suite pass; hard audit fails on the same ignored Hurd C import. |

`go mod download -json` origin commit/hash evidence:

- bbolt v1.5.0: `e7a8b2dd498494a3766ba24dd94d3509e5588485`, module sum
  `h1:S7GAl7Fxv12yohbwFfIbQCGDWbQbtDGPET4P/bD4lxU=`.
- bbolt v1.4.3: `68e6b96e6b74ebc396ac1aa7186c92e616960bd1`, module sum
  `h1:dEadXpI6G79deX5prL3QRNP6JB8UxVkqo4UPnHaNXJo=`.
- bbolt v1.3.12: `10c954b278eae6155881d1545a64673f93157549`, module sum
  `h1:UAxZAIuJqzFwByP19gZC3zd5robK3FOangrGS+Fdczg=`.

Local license inspection found bbolt's MIT license and x/sys's BSD 3-Clause
license in the downloaded module directories. For the final v1.3.12 trial,
`go list -m all` showed bbolt, x/sys, x/sync, gofail, testify, go-spew,
go-difflib, and yaml.v3; only bbolt and `x/sys/unix` are in the physical
package's non-standard runtime import graph. `go mod verify` reported “all
modules verified.” Upstream evidence: [release tags](https://github.com/etcd-io/bbolt/releases),
[bbolt v1.5.0 license](https://github.com/etcd-io/bbolt/blob/v1.5.0/LICENSE),
[x/sys v0.45.0 Hurd source](https://go.googlesource.com/sys/+/refs/tags/v0.45.0/unix/syscall_hurd.go),
and [Go `crypto/hkdf` docs](https://pkg.go.dev/crypto/hkdf).

The current toolchain is `go1.27.3 darwin/arm64`; `go doc crypto/hkdf`
confirmed the standard package and `hkdf.Key` API are available, so no external
HKDF dependency was needed. On each x/sys version above,
`unix/syscall_hurd.go` contains `import "C"` (line 13 in v0.29.0 and v0.45.0;
line 14 in v0.4.0). That Hurd file is not selected for supported targets, and
the physical package has no selected `CgoFiles` for darwin/arm64, darwin/amd64,
windows/amd64, linux/amd64, or linux/arm64. Nevertheless, the unchanged audit
fails with:

```text
go run ./cmd/no-cgo-audit
darwin/arm64: golang.org/x/sys/unix/syscall_hurd.go contains import C
exit status 1
make: *** [no-cgo-audit] Error 1
```

This is the only hard failure among the executed candidate gates:
physical/command cross-builds, focused tests, crash recovery,
tamper/corruption behavior, and sentinel scans passed on the local host. The
design's 100k/1M scale/per-percentile acceptance run remains unmeasured; the
small smoke benchmark below is not a substitute. No candidate was waived
through the audit.

## Verification results

Final full test/race/vet results are on the v1.3.12 trial, unless a row
explicitly identifies a different candidate.

| Command | Result |
| --- | --- |
| `gofmt -w internal/storage/recordcodec/recordcodec.go internal/storage/recordcodec/recordcodec_test.go internal/storage/physical/bolt/bolt.go internal/storage/physical/bolt/bolt_test.go` | Passed. |
| `go test ./internal/storage/recordcodec ./internal/storage/physical/bolt` | Passed on v1.5.0, v1.4.3, and v1.3.12 trial runs. |
| `go test -race ./internal/storage/recordcodec ./internal/storage/physical/bolt` | Passed on the initial v1.5.0 and final v1.3.12 runs; the v1.4.3 physical package race test passed. |
| `go test -run=^$ -fuzz=FuzzOpenRejectsUnauthenticatedEnvelope -fuzztime=2s -parallel=2 ./internal/storage/recordcodec` | Passed; final v1.3.12 smoke executed 258,728 inputs, found 1 new interesting input (6 total). Earlier v1.5.0 smoke also passed. |
| `go test ./...` | Passed on v1.5.0 and v1.3.12. |
| `go test -race ./...` | Passed on v1.5.0 and v1.3.12. |
| `go vet ./...` | Passed on v1.5.0 and v1.3.12. |
| `go test -run='TestEncryptedRecordsLeaveNoPlaintextInFilesOrErrors|TestCorruptDatabaseIsRejectedWithoutContentfulErrors' -v ./internal/storage/physical/bolt` | Passed both named tests. Synthetic sentinel absent from the DB, managed temporary-directory artifacts, captured standard logs, and returned errors. Corrupt file remained unchanged and produced only the generic corruption error. |
| `make physical-cross-build` | Passed for darwin/arm64, darwin/amd64, windows/amd64, linux/amd64, linux/arm64 with `CGO_ENABLED=0`. |
| `make build cross-build cross-test-compile no-cgo-audit` | Builds for `./...`, both command binaries on all five targets, physical package on all five targets, and platform conformance test compilation passed. The final `no-cgo-audit` step failed as documented above. |
| `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/storage/physical/bolt` | Runtime non-standard dependencies: `golang.org/x/sys/unix`, `go.etcd.io/bbolt`, and the local physical package. |
| `go list -m all` | Listed the final v1.3.12 module graph in the dependency evidence section. |
| `go mod verify` | “all modules verified.” |
| `git diff --check` | Passed. |

Physical benchmark command:

```text
go test -run=^$ -bench=BenchmarkUpdateOpaqueRecord -benchmem -count=1 ./internal/storage/physical/bolt
```

On v1.3.12, Darwin/arm64, Apple M5 Max:

```text
BenchmarkUpdateOpaqueRecord-18  171  6631505 ns/op  0.16 MB/s  23941 B/op  57 allocs/op
```

This microbenchmark writes a 1 KiB opaque value under a unique key in each
durable transaction. It is not the design's 100k/1M corpus, p95/p99, memory,
or cross-OS benchmark, and does not select a candidate.

## Self-review and caveats

- The codec and physical adapter are deliberately separate: this physical
  package accepts opaque bytes and cannot establish that callers encrypted
  them. Repository/domain integration is not in scope for Task 1.
- The random GCM nonce uniqueness test checks 512 fresh nonces; it is a smoke
  test, not a proof. Random nonce collision risk remains probabilistic and
  requires a documented key-usage/rotation lifecycle.
- The local crash test kills a subprocess with a transaction open and after a
  successful commit. It does not simulate sudden power loss or filesystem
  faults on every supported OS.
- Unix permission refusal is exercised on Darwin; Windows mode bits are not an
  ACL, and this task does not establish a user-only Windows ACL policy.
- The path check uses `Lstat` before opening; a shared writable parent would
  still need stronger no-follow/descriptor-based handling. Callers must use a
  private data directory, and this deserves review before production use.
- The adapter does not implement DEK wrapping, typed secret-state handling,
  atomic key creation, rotation, backup/restore, indexing, migrations, or a
  production `EncryptedStore` repository.
- bbolt v1.3.12 has no `Options.Logger` API; the newer candidates do. The
  candidate trials account for that API difference. A future selected engine
  must preserve content-free operational logging/errors as well as the source
  audit.
- ADR 0002 records the smallest viable next decisions: authorize a narrowly
  audited bbolt fork/vendor with the unsupported C-import source removed, or
  evaluate a different versioned physical engine that passes the unchanged
  audit. The audit was not weakened.

At the end of the initial turn no version passed the then-interpreted hard
gate and no implementation commit was created. The continuation below records
the parent's clarification and final selection.

## Continuation after clarified no-cgo ruling

The parent clarified the shipped no-cgo boundary: inspect all Blur repository
Go source regardless of build tags; inspect external dependency `GoFiles` and
`CgoFiles` selected for each shipped target with cgo enabled; reject selected
cgo files or `import "C"`; ignore dependency files excluded from every shipped
target; and retain the five-target `CGO_ENABLED=0` binary builds. With that
scope, the Hurd-only x/sys source is not in the shipped graph, so the earlier
false-positive audit failure is superseded. No build exception was granted.

The implementation is now pinned to current stable bbolt v1.5.0 in `go.mod`.
The audit implementation now scans every repository `.go` source (excluding
`.git` and `vendor`) independent of tags, then scans selected dependency source
for each target under `CGO_ENABLED=1`. The five target pairs remain aligned in
`internal/nocgoaudit.SupportedTargets` and the Makefile physical/command build
matrices; both locations document that additions must be kept synchronized.
The audit success message now describes both repository source and selected
dependencies.

### Audit TDD evidence

The adversarial tests were added first. The initial dependency-scope red run:

```text
go test ./internal/nocgoaudit -run 'TestAuditPackagesAllowsUnsupportedTargetIgnoredImportC|TestAuditTargetAllowsIgnoredCImportInExternalDependency' -count=1
```

failed as intended: `AuditPackages` rejected
`example.org/optional/optional_hurd.go contains import C`, and the target-level
test rejected `darwin/arm64: example.test/dep/ignored_hurd.go contains import
C`. After narrowing dependency scanning to selected source, these tests passed.

A repository-scan regression test puts an Hurd-only Go file in a directory
omitted by `go list` for Darwin. With the repository walker temporarily absent,
this command failed as intended because the audit returned nil:

```text
go test ./internal/nocgoaudit -run TestAuditTargetStillRejectsIgnoredCImportInBlurSource -count=1
```

The green targeted command was:

```text
go test ./internal/nocgoaudit -run 'TestAuditPackagesAllowsUnsupportedTargetIgnoredImportC|TestAuditPackagesRejectsSelectedSupportedTargetImportC|TestAuditTargetAllowsIgnoredCImportInExternalDependency|TestAuditTargetRejectsCImportSelectedForSupportedTarget|TestAuditTargetStillRejectsIgnoredCImportInBlurSource' -count=1
```

It passed. The tests exercise real temporary Go modules with a replaced local
dependency and target-specific build tags; no mock substitutes for `go list`.
`go test ./internal/nocgoaudit -count=1` also passed.

### Final verification on bbolt v1.5.0

Fresh final results on the selected version:

| Command | Result |
| --- | --- |
| `go test ./...` | Passed all packages. |
| `go test -race ./...` | Passed all packages. |
| `go vet ./...` | Passed. |
| `go test -race ./internal/storage/recordcodec ./internal/storage/physical/bolt ./internal/nocgoaudit` | Passed. |
| `go test -run=^$ -fuzz=FuzzOpenRejectsUnauthenticatedEnvelope -fuzztime=2s -parallel=2 ./internal/storage/recordcodec` | Passed; 239,525 executions, zero new interesting inputs in this run, 6 corpus inputs total. |
| `go test ./internal/nocgoaudit -count=1` | Passed all audit unit and temporary-module integration tests. |
| `go test -run='TestEncryptedRecordsLeaveNoPlaintextInFilesOrErrors|TestCorruptDatabaseIsRejectedWithoutContentfulErrors' -v ./internal/storage/physical/bolt` | Both tests passed. Sentinel absent from DB/temp artifacts/logs/errors; corrupt bytes preserved and error content-free. |
| `make build cross-build cross-test-compile no-cgo-audit` | Passed, including `CGO_ENABLED=0 go build ./...`, command and physical package cross-builds for five targets, conformance test compile, and the amended source audit. |
| `make physical-cross-build` | Passed for darwin/arm64, darwin/amd64, windows/amd64, linux/amd64, linux/arm64. |
| `CGO_ENABLED=1 GOOS=<target> GOARCH=<target> go list -deps ...` CgoFiles check | No selected non-standard `CgoFiles` for darwin/arm64, darwin/amd64, windows/amd64, linux/amd64, or linux/arm64. |
| `go list -m -versions go.etcd.io/bbolt` / `go mod download -json go.etcd.io/bbolt@v1.5.0` | Confirmed official v1.5.0 tag origin `e7a8b2dd498494a3766ba24dd94d3509e5588485`, module sum `h1:S7GAl7Fxv12yohbwFfIbQCGDWbQbtDGPET4P/bD4lxU=` and go.mod sum `h1:mkltfYE5aUHQxUct9N9V+Kp7aSjFqjgrhcXIS70Lrdk=`. |
| `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/storage/physical/bolt` | Runtime graph includes bbolt core/errors/common/freelist and `golang.org/x/sys/unix`; no other external runtime package. |
| `go list -m all` | Listed bbolt's module graph: aclements/go-moremath, go-spew, mousetrap, go-difflib, cobra, pflag, testify, bbolt, gofail, x/mod, x/perf, x/sync, x/sys, x/tools, yaml.v3. Test/tool modules are not imported into the physical runtime graph. |
| `go mod verify` | “all modules verified.” |
| `go list -m all` / `go mod verify` | Listed the pinned bbolt/x/sys runtime modules plus test/tooling graph; all modules verified. |
| `go doc crypto/hkdf` | Go 1.27.3 exposes standard-library `hkdf.Key`; no external HKDF dependency is used. |
| `git diff --check` | Passed in the final pre-commit verification. |

The upstream tagged module declares Go 1.25.0/toolchain 1.25.11 and MIT;
runtime x/sys v0.45.0 is BSD-3-Clause. `go doc crypto/hkdf` on local Go 1.27.3
confirmed the standard-library `hkdf.Key` API. Official evidence is in
[bbolt releases](https://github.com/etcd-io/bbolt/releases),
[v1.5.0 go.mod](https://github.com/etcd-io/bbolt/blob/v1.5.0/go.mod),
[v1.5.0 LICENSE](https://github.com/etcd-io/bbolt/blob/v1.5.0/LICENSE), and
[the tagged x/sys Hurd source](https://go.googlesource.com/sys/+/refs/tags/v0.45.0/unix/syscall_hurd.go).

The selected-CgoFiles matrix used this command shape, once per listed target:

```sh
CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go list -deps -f '{{if and (not .Standard) (gt (len .CgoFiles) 0)}}{{.ImportPath}}: {{.CgoFiles}}{{end}}' ./...
```

All five runs emitted no package lines; `make no-cgo-audit` also passed and
reported: `No CgoFiles or target-selected import C found in Blur source or
non-standard dependencies across supported targets`.

The final physical benchmark:

```text
go test -run=^$ -bench=BenchmarkUpdateOpaqueRecord -benchmem -count=1 ./internal/storage/physical/bolt
goos: darwin
goarch: arm64
cpu: Apple M5 Max
BenchmarkUpdateOpaqueRecord-18  187  6721887 ns/op  0.16 MB/s  24063 B/op  58 allocs/op
```

This is one durable transaction writing a 1 KiB opaque value under a unique
key. The design's 100k/1M repository/index work belongs to Task 3 and does not
block the Task 1 physical-store selection.

### Final scoped files

- `.github/workflows/ci.yml`, `Makefile`, and `cmd/no-cgo-audit/main.go`:
  include and describe the physical-store build/audit gates.
- `go.mod` and `go.sum`: pin bbolt v1.5.0 and x/sys v0.45.0.
- `internal/nocgoaudit/audit.go` and `audit_test.go`: implement and adversarially
  test the clarified repository-source/selected-dependency audit boundary.
- `internal/storage/recordcodec/recordcodec.go` and its tests; and
  `internal/storage/physical/bolt/bolt.go` and its tests/benchmark.
- `docs/adr/0002-durable-store.md` and this report.

### Final residual concerns

The earlier self-review still applies: the adapter does not implement DEK
wrapping/rotation or the higher-level `EncryptedStore`; subprocess kill tests
are not sudden-power-loss testing; Windows mode bits do not establish a
user-only ACL; and `Lstat` then open is weaker than descriptor-based
no-follow handling if callers use a shared writable parent. These remain
follow-up production-hardening items, not failures of the Task 1 selected
candidate gates. The commit hash is recorded by the final task handoff.
