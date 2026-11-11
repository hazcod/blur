# Safari Active-Page Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ingest only fresh, active, non-private Safari page text through an authenticated in-memory bridge into Blur's existing redaction and encrypted storage pipeline.

**Architecture:** A Safari App Extension sends a bounded snapshot from its current injected page script to its native handler. The handler verifies the originating `SFSafariPage` and sends an HMAC-authenticated message over a same-user AF_UNIX socket in a configured App Group; a Go receiver rechecks capture policy, sanitizes, and persists only validated observations. Shared Keychain grants are created on capture start and revoked on pause.

**Tech Stack:** Go standard library, existing `internal/localapi` Unix transport and HMAC grant store, existing `internal/browsercapture` sanitizer, Swift Safari App Extension, JavaScript DOM traversal, Apple App Groups and Keychain.

**Spec:** `docs/superpowers/specs/2026-11-06-safari-active-page-capture-design.md`

## Global Constraints

- Safari capture remains opt-in and accepts only a fresh, active, privacy-known, non-private page.
- The extension never navigates, controls, enumerates history, clicks, scrolls, paginates, or searches.
- It may include loaded off-screen DOM text, bounded by 10,000 nodes, 256 KiB, and 100 ms per extraction; mutations are debounced and no more than one refresh per 10 seconds.
- URL paths, queries, fragments, and credentials are stripped by native Swift before IPC; Go independently persists only the normalized host.
- Raw page text crosses only process memory over an App Group AF_UNIX socket; no plaintext queue, log, or AI request is allowed.
- Receiver frames are at most 288 KiB; grants use `capture:ingest`, expire within 24 hours, and use one-use replay-protected proofs.
- Pausing capture closes the ingest gate, waits for in-flight writes, revokes the grant, and deletes its shared Keychain credential before pause returns.
- The encrypted observation sink is mandatory; unknown or unavailable native capabilities fail closed.
- All Go targets stay `CGO_ENABLED=0`; no cgo package or import is added.
- Preserve the shared dirty worktree; do not stage or commit unrelated files.

## Review Focus

1. Replayed or stale proof: same payload is rejected on second delivery; timestamp beyond skew is rejected.
2. Private, inactive, unknown-privacy, or mismatched Safari page: reject before the encrypted sink is called.
3. Pause racing an accepted message: pause must return only after no further write can occur.
4. Oversized, malformed, duplicate-key, invalid-UTF-8, or deeply nested input: bounded rejection without panic or content-bearing error/log.
5. Large DOM or repeated mutations: extraction stops at node/byte/time limits and does not busy-loop or resend unchanged content.

### Task 1: Go wire protocol and verified receiver

**Files:**
- Create: `internal/browsercapture/bridge.go`
- Modify: `internal/browsercapture/snapshot_test.go`
- Test: `internal/browsercapture/bridge_test.go`

**Interfaces:**
- Consumes: `Snapshot`, `Sanitize`, `localapi.Proof`, `localapi.GrantStore`, `storage.EncryptedStore` contract.
- Produces: `NewReceiver(grants, sink, enabled, clock) (*Receiver, error)`, `(*Receiver).Accept(ctx, wire []byte) error`, and `(*Receiver).Pause()`. `Accept` accepts a versioned envelope whose `Payload` is raw JSON and whose explicit wire proof fields map into `localapi.Proof`; it hashes the exact payload bytes before authorization.

- [ ] Add tests for authenticated valid input, bad MAC, revoked/expired/replayed proof, disabled capture, private/inactive/unknown privacy, malformed payload, size limit, encrypted sink enforcement, and pause/write ordering.
- [ ] Run `go test ./internal/browsercapture -run 'TestReceiver' -count=1` and verify the new tests fail before implementation.
- [ ] Implement the bounded versioned envelope and receiver; reject duplicate JSON object keys, unknown fields, and invalid UTF-8 before sanitization.
- [ ] Write only the `SanitizedObservation` to an encrypted sink; do not retain or log raw payloads.
- [ ] Run `go test -race ./internal/browsercapture` and `go vet ./internal/browsercapture`.

### Task 2: Unix socket lifecycle and shared grant storage

**Files:**
- Create: `internal/browsercapture/unix_server.go`
- Modify: `internal/platform/darwin/secret_store_darwin.go`
- Modify: `internal/platform/darwin/secret_store_darwin_test.go`
- Modify: `internal/client/bootstrap.go`
- Test: `internal/browsercapture/unix_server_test.go`

**Interfaces:**
- Consumes: Task 1 `Receiver`, existing `localapi.UnixTransport`, and `platform.SecretStore`.
- Produces: a cancellable `Server` that serves bounded frames at the configured App Group socket and a configurable macOS Keychain service/access-group constructor for a shared capture grant.

- [ ] Add tests proving owner-only endpoint, peer UID validation, frame cap, cancellation, stop ordering, and secret-store service/access-group isolation.
- [ ] Run focused Go tests and confirm expected failures.
- [ ] Implement listener startup only after user capture enable and valid App Group entitlement/path are present; report unavailable rather than starting a weaker transport.
- [ ] On capture start issue/store a maximum-24-hour `capture:ingest` grant; on pause stop/join server, revoke the grant, and delete shared credential.
- [ ] Run `go test -race ./internal/browsercapture ./internal/platform/darwin ./internal/client`.

### Task 3: Safari App Extension capture source

**Files:**
- Create: `platform/macos/SafariExtension/Info.plist`
- Create: `platform/macos/SafariExtension/ExtensionHandler.swift`
- Create: `platform/macos/SafariExtension/Resources/GlobalPage.js`
- Create: `platform/macos/SafariExtension/Resources/Injected.js`
- Create: `platform/macos/SafariExtension/Resources/manifest.json`
- Test: `platform/macos/SafariExtensionTests/`

**Interfaces:**
- Consumes: Task 2 socket path/grant contract and design-spec limits.
- Produces: one `page-snapshot-v1` message from current injected page to native handler; Swift obtains properties from the originating `SFSafariPage`, validates `isActive`, `usesPrivateBrowsing`, HTTP(S) scheme, and size before sending an authenticated frame to the Go socket.

- [ ] Add pure JS tests for bounded DOM traversal, excluded nodes, off-screen inclusion, time/node/byte caps, debounce, rate limit, and unchanged-content suppression.
- [ ] Add Swift tests/fixtures for malformed messages and privacy/active-state rejection.
- [ ] Verify JS tests fail before implementation.
- [ ] Implement a bounded TreeWalker that reads current rendered document text without `innerText` over the whole document or repeated polling; do not read form values.
- [ ] Implement the Safari App Extension handler using only its originating `SFSafariPage`; strip path/query/fragment/credentials before IPC and authenticate socket frames.
- [ ] Run JS and Swift tests. On an Xcode-capable Mac, build and package the extension; this host currently has Command Line Tools only, so do not claim that gate here.

### Task 4: macOS app integration, UI, and operator docs

**Files:**
- Modify: `internal/client/capture_darwin.go`
- Modify: `internal/client/desktop.go`
- Modify: `internal/client/bootstrap.go`
- Modify: `Makefile`
- Modify: `README.md`, `docs/architecture.md`, `docs/threat-model.md`
- Create: `platform/macos/Blur.entitlements`, `platform/macos/SafariExtension/Extension.entitlements`, and an Xcode project or reproducible generator.

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: an explicit Privacy setting and status that explains extension/site permission, capture state, and fail-closed capability; the capture controller starts and stops the bridge along with native capture.

- [ ] Add tests for enable/disable, storage unavailable, missing entitlement, Safari extension unavailable, and pause with in-flight message.
- [ ] Verify tests fail before app integration.
- [ ] Wire lifecycle and UI so browser capture can never start while the global capture controller is paused.
- [ ] Add App Group identifier as an overridable build setting; never hard-code another developer's team identifier.
- [ ] Document Safari website permissions, private-mode exclusion, on-device flow, data retention, and the Xcode signing requirements.
- [ ] Run `go test -race ./...`, `go vet ./...`, cgo audit, `make cross-build cross-test-compile`, and `git diff --check`.
- [ ] On Xcode-capable macOS, install the bundle, grant only the requested Safari website access, verify normal-page capture, verify private-window rejection, verify pause, and confirm no plaintext temporary file is created.

## Completion Gate

This plan is complete only after all Go tests and the signed Safari runtime tests pass. A source-only extension, a sanitizer unit test, or a Go bundle build alone does not prove Safari browser capture works end-to-end.
