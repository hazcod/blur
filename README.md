# Blur

Blur is a local-first memory and writing assistant in development. This
repository currently contains a Go foundation: bounded capture and redaction
contracts, scoped in-process authorization proofs, deterministic exact-term
search, capability reports, and an explicitly ephemeral in-memory demo. It is
not yet a desktop application and does not capture from native applications.

The implementation status is described in [architecture](docs/architecture.md)
and [threat model](docs/threat-model.md). The product direction is in the
[design](docs/superpowers/specs/2026-11-04-blur-design.md); it includes proposed
capabilities that are not shipped here.

## Build and run

Requires Go 1.27.3 (module language version 1.27). The module currently has no
third-party Go module dependencies.

```sh
go test ./...
go test -race ./...
go vet ./...
make build
make cross-build
make cross-test-compile
make no-cgo-audit
go run ./cmd/blurctl status
go run ./cmd/blurctl capabilities
go run ./cmd/blur-core
# Read-only, synthetic fixture commands only:
go run ./cmd/blurctl fixture doctor --fixture --store "$TMPDIR/blur-fixture-demo.db"
go run ./cmd/blurctl fixture today --fixture --store "$TMPDIR/blur-fixture-demo.db"
go run ./cmd/blurctl fixture knowledge --fixture --store "$TMPDIR/blur-fixture-demo.db"
```

`blurctl` accepts only `status` and `capabilities`. Both commands start an
in-process ephemeral demonstration service; they do not contact a daemon or
open a local endpoint. `blur-core` prints the same one-shot development status.
Expect `mode: ephemeral_memory`, `encrypted_at_rest: false`,
`ai_enabled: false`, and `network_listening: false`. Memory exists only for the
process lifetime and is not suitable for real personal data.

The separate `blurctl fixture` commands open an existing store read-only with a
fixed, publicly known fixture key. They are only for synthetic offline test
fixtures, require the explicit `--fixture` marker and a
`blur-fixture-*.db` file under the operating system temporary directory, and
cannot open a store protected by an OS secret adapter.
They never accept keys through arguments, environment variables, or config.
`doctor` reports counters and fixed capability states; `today` and `knowledge`
print bounded fixture content. Do not place personal data in a fixture store.
Production key wiring remains blocked on the OS secret adapters.

`make cross-build` builds `blur-core` and `blurctl` with cgo disabled for
Darwin arm64/amd64, Windows amd64, and Linux amd64/arm64. The
`make cross-test-compile` target compiles platform conformance tests for Darwin
arm64, Windows amd64, and Linux amd64. `make no-cgo-audit` inspects command
dependency graphs for Cgo files and `import "C"` across the supported target
matrix.

## Security status

The current code has no network client, AI provider, screenshot/OCR path,
native capture implementation, persistent database, OS secret-store adapter,
or native local IPC listener. macOS AXUIElement, Windows UI Automation, and
Linux AT-SPI2 adapters are placeholders that report capture as user-disabled.
Unavailable ports fail closed. No screenshot, OCR, clipboard, keystroke,
remote-provider, or other alternate capture path is wired in.

The in-process capture pipeline requires an available capability report,
validates a non-content probe, rejects secure, elevated, private, and excluded
targets before value reads, bounds work by bytes/nodes/depth/time, and redacts
heuristically before producing an opaque validated observation. This is a
foundation control, not a guarantee that all secrets are recognized. Do not
send real sensitive data through the demo or treat this repository as a
secure persistent memory product.

Local grants use scoped HMAC request proofs with expirations, one-use nonces,
replay limits, and revocation in memory. The store caps grants at 1,024 and
clears retained key/map state when grants are revoked or pruned after expiry.
They do not yet protect a socket, process boundary, or persisted client
identity. Search binds a per-request byte budget into its proof, scans at most
64 observations and 2 MiB (the omitted byte-limit field defaults to that cap),
fetches at most four rows per page, and the core allows at most two concurrent
searches. `securestore` defines a strict
key-state contract, but no OS secret backend or encrypted store is connected.
No deletion, backup, key-rotation, or at-rest confidentiality guarantee is
implemented. See the [threat model](docs/threat-model.md) for limits and
residual risks.

## Checks

The foundation verification commands are listed above. Vulnerability scanning
with `govulncheck` was not run where the tool is unavailable; install it through
the normal Go security toolchain before release review. A clean `go list -m
all` currently reports only this module. Passing compile and unit checks do
not replace the deferred persistence, platform, privacy, and independent
security reviews.

The Go module path `github.com/nielsgroen/blur` is a temporary OSS identity and
may change before the first public release. License: Apache-2.0; see
[LICENSE](LICENSE).
