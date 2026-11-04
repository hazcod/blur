# Blur Cross-Platform Secure Foundation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` and
> `superpowers:test-driven-development`. Implement one task at a time, with a
> fresh implementation worker and independent review.

**Goal:** Deliver a runnable, tested Go foundation whose public contracts make
unsafe capture, silent capability fallback, plaintext persistence, and hidden
AI egress difficult by construction on macOS, Windows, and Linux.

**Architecture:** A portable core consumes sanitized observations and narrow
capability ports. OS-specific hosts perform staged probe/read/sanitize capture.
Encrypted storage, scoped local grants, and provider admission are interfaces
with deterministic fakes first; irreversible backend and UI choices remain
behind measured gates.

**Tech stack:** Go 1.27.3, standard library first, platform build tags, Wails
behind an owned facade in a later phase. Apache-2.0.

**Spec:** `docs/superpowers/specs/2026-11-04-blur-design.md`

## Global constraints

- Production code begins from a failing test and stays free of captured user
  content and original Goldfish assets/prompts/code.
- Raw capture types cannot cross package or process boundaries; only sanitized
  observations can be ingested.
- No dependency may silently perform network I/O. Remote provider support is
  absent from this foundation.
- Capability absence is structured data, never a hidden fallback.
- All packages pass race tests and compile for Darwin, Windows, and Linux.
- Logs contain counters and stable error codes, never observation text.

## Task 1: Bootstrap the OSS repository and build identity

**Files:** `go.mod`, `.gitignore`, `.editorconfig`, `LICENSE`, `README.md`,
`CONTRIBUTING.md`, `SECURITY.md`, `Makefile`, `.github/workflows/ci.yml`,
`internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`

- [ ] Write a failing test for deterministic build metadata and a concise user
  agent string with safe development defaults.
- [ ] Implement the smallest `buildinfo` package that passes it.
- [ ] Add OSS/security documents and a CI matrix running tests, race tests,
  vet, and cross-compilation on supported OSes.
- [ ] Verify `go test -race ./...`, `go vet ./...`, and the build matrix locally
  where possible.

## Task 2: Define portable capability contracts

**Files:** `internal/platform/capability.go`,
`internal/platform/capability_test.go`, `internal/platform/contracts.go`,
`internal/platform/fake/fake.go`, `internal/platform/fake/fake_test.go`

- [ ] Test the closed capability-state vocabulary, evidence/reason fields, and
  stable JSON form.
- [ ] Define narrow capture, target, hotkey, surface, audio, secret, transport,
  private-path, autostart, and session interfaces without framework types.
- [ ] Add deterministic fakes and contract tests proving unavailable operations
  fail closed.

## Task 3: Enforce staged, privacy-preserving capture

**Files:** `internal/capture/pipeline.go`, `internal/capture/pipeline_test.go`,
`internal/privacy/redact.go`, `internal/privacy/redact_test.go`

- [ ] Test that secure targets, excluded apps/domains, denied probes, timeout
  states, and byte/depth/node limits never invoke a value reader.
- [ ] Keep raw-frame types unexported and construct only an exported
  `SanitizedObservation` after deterministic redaction.
- [ ] Test recognizable credentials, bounded metadata, truncation, stable
  hashes, and log-safe errors with synthetic fixtures only.

## Task 4: Add scoped local authorization contracts

**Files:** `internal/localapi/grants.go`, `internal/localapi/grants_test.go`,
`internal/localapi/transport.go`, `internal/localapi/memory_transport_test.go`

- [ ] Test per-client scoped, expiring, and revocable grants; default denial;
  replay resistance; and no global install token.
- [ ] Define `LocalTransport` and an in-memory conformance transport without a
  TCP listener.
- [ ] Add platform endpoint descriptors for AF_UNIX and Windows named pipes;
  native adapters follow only after security fixtures exist.

## Task 5: Define encrypted storage and key lifecycle gates

**Files:** `internal/storage/store.go`, `internal/storage/store_test.go`,
`internal/securestore/store.go`, `internal/securestore/store_test.go`,
`docs/adr/0001-encrypted-store-selection.md`

- [ ] Test typed secret states so only `NotFound` permits key creation; locked,
  corrupt, denied, and unavailable stores fail closed.
- [ ] Define `EncryptedStore`, generation metadata, derivation state, and an
  in-memory conformance backend accepting sanitized observations only.
- [ ] Record identical SQLCipher/Adiantum benchmark, crash, tamper, and
  packaging gates without claiming a winner before evidence exists.

## Task 6: Ship the offline ingest/search vertical slice

**Files:** `internal/core/service.go`, `internal/core/service_test.go`,
`internal/search/search.go`, `internal/search/search_test.go`,
`cmd/blur-core/main.go`, `cmd/blurctl/main.go`, `cmd/blurctl/main_test.go`

- [ ] Test authorized sanitized ingest, denied raw/unknown inputs, deterministic
  exact-term search, bounded results, provenance, and stable capability status.
- [ ] Implement an in-memory runnable core with no network and no AI.
- [ ] Add `blurctl status` and `blurctl capabilities`; smoke-test both.

## Task 7: Add OS adapter skeletons and compile gates

**Files:** `internal/platform/darwin/adapter_darwin.go`,
`internal/platform/windows/adapter_windows.go`,
`internal/platform/linux/adapter_linux.go`,
`internal/platform/conformance/conformance_test.go`, `Makefile`,
`.github/workflows/ci.yml`

- [ ] Add conformance tests requiring every adapter to report evidence-backed
  structured states and refuse unimplemented reads/writes.
- [ ] Add build-tagged adapters for AXUIElement, UI Automation, and AT-SPI2 as
  explicit unavailable placeholders; native calls land only with fixtures.
- [ ] Cross-compile command binaries for Darwin, Windows, and Linux.

## Task 8: Foundation threat review and release checkpoint

**Files:** `docs/threat-model.md`, `docs/architecture.md`, `README.md`

- [ ] Document trust boundaries, out-of-scope admin/kernel compromise, Wayland
  degradation, heuristic redaction, deletion limits, and no silent fallback.
- [ ] Run tests, race tests, vet, cross-builds, dependency/vulnerability checks,
  and synthetic sentinel scans.
- [ ] Obtain an independent code/security review before native capture,
  persistent storage, Wails, or provider code begins.

## Completion gate

The foundation is complete only when its binaries run without an account or
network, unsafe capability states fail closed, the capture API exposes no raw
serializable frame, every OS target compiles, and all verification commands
pass. Native capture, database selection, UI, AI, MCP, writing, and dictation
then proceed as separately reviewed phases using these contracts.
