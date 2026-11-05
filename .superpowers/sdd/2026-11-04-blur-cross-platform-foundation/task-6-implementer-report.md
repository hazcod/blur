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
