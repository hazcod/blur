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
  matching targets. Runtime conformance ran for Darwin on this host; the
  Windows and Linux conformance binaries were cross-compiled here and their
  tests run in the matching native CI jobs.

## Fix Round 1

Addressed both review findings:

- Replaced the shell regex and `CGO_ENABLED=0` CgoFiles query with a small
  standard-library Go audit command. It queries the repository dependency
  graph with `go list -deps -json` and cgo enabled for every supported target,
  rejects CgoFiles in each non-standard package, and parses selected and
  ignored Go files with the Go parser to find any import path `C`, including
  grouped and aliased imports. Standard-library packages are exempt from the
  third-party prohibition.
- Split conformance into a shared behavioral assertion and Darwin, Windows,
  and Linux native-build test files. Each checks its exact evidence-backed
  report and verifies both `Probe` and `Next` return the matching typed
  unavailable error. Native CI tests each target; `cross-test-compile` also
  compiles all three conformance test binaries with cgo disabled.

Fix Round 1 verification:

- Adversarial audit fixtures passed for CgoFiles, grouped/aliased `import C`,
  ignored source files, the standard-library exemption, and a pure-Go
  dependency.
- `go test ./...` and `go vet ./...` passed.
- `make cross-build cross-test-compile no-cgo-audit` passed for all supported
  command build targets. The audit reports no CgoFiles or `import C` in
  non-standard repository dependencies.
