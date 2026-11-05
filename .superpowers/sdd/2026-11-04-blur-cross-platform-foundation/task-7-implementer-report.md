# Task 7 implementer report

## Summary

- Added cgo-free Darwin, Windows, and Linux capture adapter placeholders for
  AXUIElement, UI Automation, and AT-SPI2. Each reports the core-approved
  `capture/user_disabled/adapter_unimplemented` tuple with fixed API evidence.
- Every currently unimplemented capture operation (`Probe` and `Next`) fails
  closed with a typed `platform.UnavailableError` carrying that report.
- Added a conformance test for report validation and refusal behavior.
- Added no-cgo native builds, command binary cross-builds for Darwin, Windows,
  and Linux, and checks for application `import "C"` and dependency graph
  `CgoFiles`. Race tests remain a separate Make target and CI step.

## Verification

- TDD red: `go test ./internal/platform/conformance` failed because the Darwin
  adapter package did not exist.
- TDD green: `go test ./internal/platform/conformance` passed after adding the
  adapter implementation.
- `make cross-build no-cgo-audit` passed for Darwin/arm64, Darwin/amd64,
  Windows/amd64, Linux/amd64, and Linux/arm64; both command binaries compiled
  for every target with `CGO_ENABLED=0`.
- `go test ./...` passed.
- `go vet ./...` passed.
- `git diff --check` passed.

## Caveats

- No native API calls or cgo were added. The adapter evidence values identify
  intended native mechanisms; they do not claim those mechanisms have been
  probed or initialized.
- Go selects `_darwin.go`, `_windows.go`, and `_linux.go` files only on their
  matching targets. The conformance test therefore runs against the Darwin
  adapter on the current Darwin host; Windows and Linux behavior is covered by
  compile-time interface assertions and their cross-builds, not runtime tests
  in this host environment.
