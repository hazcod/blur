# Task 6 implementation report

Implemented the in-process offline ingest/search slice, scoped one-use proof checks, bounded deterministic exact-term search, honest ephemeral status/capabilities, and runnable CLI/core checks. No sockets, filesystem persistence, provider, AI, cgo, or new dependency was added. The memory store is explicitly unencrypted and ephemeral.

## TDD record

- RED: `go test ./internal/search` failed with undefined `Request`/`New`; GREEN after exact-term engine implementation.
- RED: `go test ./internal/core` failed with undefined `Service`/constructors/digests; GREEN after authorization service implementation.
- RED: `go test ./cmd/blurctl` failed with undefined `run`; GREEN after command implementation.
- RED: result omission test reported `Truncated:false` with `Omitted:1`; GREEN after marking omitted output truncated.
- RED: closed-store status test reported `Ready:true`; GREEN after readiness probe.
- RED: CLI output-cap test emitted 65,537 bytes; GREEN after including newline in accounting.
- RED: typed-nil ephemeral store constructor accepted the store; GREEN after fail-closed type/nil check.
- RED: unknown-command factory test failed with undefined `launch`; GREEN after validating command before grant creation.
- RED: overlong store page test accepted 257 rows against a 256-row request; GREEN after page-size enforcement.
- RED: explicit empty capability set was accepted; GREEN after constructor rejection.

Coverage includes zero/invalid/tampered/replayed/expired/wrong-scope/cancelled ingest with no denied write; UI/MCP read and mutate denial; query and limit validation; case/Unicode term boundaries; deterministic insertion-order results; scan/result/response omission accounting; UTF-8 snippets; provenance; capability validation/sorting; CLI output and invalid commands.

## Verification

- `go test -count=1 ./...`: exit 0, all packages passed.
- `go test -race -count=1 ./...`: exit 0, all packages passed.
- `go vet ./...`: exit 0, no output.
- `CGO_ENABLED=0 GOOS={darwin,linux,windows} GOARCH={amd64,arm64} go build ./...`: all six combinations exit 0, no output.
- `CGO_ENABLED=0 go list -deps -f '{{if .CgoFiles}}{{.ImportPath}} {{.CgoFiles}}{{end}}' ./...`: exit 0, no output.
- `rg -n 'import "C"|#cgo' --glob '*.go' .`: exit 1, no matches.
- `git diff --cached --check`: exit 0, no output.

## Exact command smoke output

`go run ./cmd/blur-core` and `go run ./cmd/blurctl status` each exited 0 and emitted exactly:

```json
{"version":"dev","mode":"ephemeral_memory","encrypted_at_rest":false,"ai_enabled":false,"network_listening":false,"ready":true}
```

`go run ./cmd/blurctl capabilities` exited 0 and emitted exactly:

```json
[{"id":"audio_recorder","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"autostart","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"capture_source","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"gesture_hotkeys","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"local_transport","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"private_path","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"secret_store","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"session_state","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"surface_manager","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"text_targets","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"}]
```

Self-review: operation digests bind the requested semantics and are recomputed inside service; proof scopes and nonces are enforced by the injected grant store. Search retains only the capped result set and rejects overlong store pages. The foundation commands deliberately report the unencrypted ephemeral mode and do not claim a daemon connection.

## Fix Round 1

- Capability construction now requires exactly the ten established IDs once each, with `capture` as the capture ID. Reason and evidence are closed content-free vocabularies; unknown values fail construction. A valid complete custom set is sorted by ID.
- Every service request checks its context before and after authorization. Ingest, search, and status check again after their store/search call; capabilities checks after copying reports. Status propagates cancellation and deadline errors, while a non-context store failure reports `ready:false` without backend diagnostics.
- The grant nonce may be consumed if cancellation occurs just after authorization. The service then returns a context error and starts no subsequent write or result delivery. A store write that already committed before cancellation cannot be rolled back through the current store contract; the service still never reports success for that canceled call.

RED: the capability test accepted a nine-port set; the authorization-cancellation test returned success for status/capabilities; the post-store-cancellation test returned success for status/search/ingest. GREEN: focused core tests passed after each corresponding service change. Deterministic tests also cover already-canceled contexts (proof remains usable), nonce consumption during authorization, and a store deadline without a canceled caller context. A first vet run flagged a missing test cleanup; adding `defer cancel()` cleared it.

Final verification: `go test -count=1 ./internal/core ./internal/search ./cmd/blurctl`, `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...` all exited 0. All six `CGO_ENABLED=0` darwin/linux/windows amd64/arm64 builds exited 0. The cgo dependency listing exited 0 with no output; `rg -n 'import "C"|#cgo' --glob '*.go' .` exited 1 with no matches; `git diff --check` exited 0.

Final smoke: `go run ./cmd/blur-core` and `go run ./cmd/blurctl status` each exited 0 and emitted exactly:

```json
{"version":"dev","mode":"ephemeral_memory","encrypted_at_rest":false,"ai_enabled":false,"network_listening":false,"ready":true}
```

`go run ./cmd/blurctl capabilities` exited 0 and emitted exactly:

```json
[{"id":"audio_recorder","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"autostart","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"capture","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"gesture_hotkeys","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"local_transport","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"private_path","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"secret_store","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"session_state","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"surface_manager","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"},{"id":"text_targets","state":"user_disabled","reason":"foundation_only","evidence":"adapter_not_connected"}]
```

## Fix Round 2

This supersedes Fix Round 1's blanket statement about cancellation after a store write. Ingest checks context before and after proof authorization, then delegates the write to `EncryptedStore`. A nil `PutObservation` return is a successful commit and wins a cancellation arriving afterward. A store context error means no write under the store contract. Search, status, and capabilities still discard responses on cancellation because they commit no mutation.

Capability validation now checks complete state/reason/evidence tuples. The closed set covers foundation-disabled, unimplemented adapters, available, permission-required, target/secure/elevated, missing portal, temporary unresponsiveness, and user-disabled conditions with compatible evidence. Contradictory cross-products fail construction; the exact ten capability IDs remain mandatory.

RED: `TestSuccessfulIngestCommitWinsCancellationRace` observed `context canceled` after the underlying store committed. `TestCapabilityTuplesAreCoherent` rejected valid `available/available/axui_element`. GREEN: focused core tests passed after replacing the ingest post-commit context override and adding tuple validation. The store-cancellation regression asserts a context error with zero stored observations; the commit-race regression asserts success with exactly one stored observation.

Final verification: focused (`./internal/core ./internal/search ./cmd/blurctl`), full, and race `go test -count=1` runs all exited 0; `go vet ./...` exited 0 with no output. All six `CGO_ENABLED=0` darwin/linux/windows amd64/arm64 builds exited 0. The cgo dependency listing exited 0 with no output; `rg -n 'import "C"|#cgo' --glob '*.go' .` exited 1 with no matches; `git diff --check` exited 0. Both status smoke commands and the capabilities smoke command exited 0 and emitted the same exact JSON documents recorded in Fix Round 1 above.

## Fix Round 3

Capability validation now binds evidence to the capability ID as well as the state/reason tuple. Each of the ten ports has a closed set of static platform mechanism codes. The foundation tuple alone accepts `adapter_not_connected` across ports. Every port accepts `user_disabled/adapter_unimplemented/platform_stub`, while `platform_stub` cannot support available or permission claims. `configuration` supports explicit user-disabled state. Native API evidence supports unimplemented, permission, and available claims only on its mapped port; target and portal states retain their narrower tuple rules.

RED: the updated complete custom set using `platform_stub` was rejected, and `TestCapabilityEvidenceMatchesPort` rejected the valid capture stub. GREEN: focused capability tests passed after adding the port evidence map and integrating it into tuple validation. The regression test enumerates every port's stub and native mechanisms, then rejects all API evidence from other ports unless that mechanism is explicitly shared; this includes `axui_element` on autostart and audio recorder.

Final verification: `go test -count=1 ./internal/core ./internal/search ./cmd/blurctl`, `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...` all exited 0. Six `CGO_ENABLED=0` darwin/linux/windows amd64/arm64 builds exited 0. The cgo dependency listing exited 0 with no output; the `import "C"|#cgo` audit found no matches; `git diff --check` exited 0. `go run ./cmd/blur-core`, `go run ./cmd/blurctl status`, and `go run ./cmd/blurctl capabilities` each exited 0 and emitted the exact same safe JSON documents recorded in Fix Round 1 above.

## Fix Round 4

Capability validation now applies an explicit per-port semantic matrix: only `capture` and `text_targets` may report target-specific states; only `gesture_hotkeys` and `surface_manager` may report a missing XDG portal; and `permission_required` is limited to capture, text targets, gesture hotkeys, audio recorder, secret store, autostart, and session state. Temporary unresponsiveness requires a runtime-backed native port mechanism. Foundation, configuration, and `platform_stub` evidence remain excluded from runtime states, while the existing available, user-disabled, stub, and foundation tuples remain intact.

RED: `TestCapabilityEvidenceMatchesPort` accepted `gesture_hotkeys/unsupported_target/unsupported_target/event_tap`. GREEN: the test now checks the special-state matrix across each port and native evidence, including forbidden portal and runtime-state combinations; the focused capability tests pass.

Verification: `go test ./...`, `go vet ./...`, all six `CGO_ENABLED=0` darwin/linux/windows amd64/arm64 builds, and the no-cgo dependency listing exited 0. The `import "C"|#cgo` audit found no matches (rg exit 1).
