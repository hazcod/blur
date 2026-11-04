# Task 1 implementation report

## Summary

Bootstrapped the Go OSS repository with the temporary module identity
`github.com/nielsgroen/blur`, Apache-2.0 licensing, contributor and security
guidance, development targets, and native/cross-build GitHub Actions checks.
Added deterministic build identity with a `dev` default and a user-agent made
only from the product name and version.

## TDD evidence

- RED: `GO111MODULE=off go test ./internal/buildinfo` failed to compile because
  `UserAgent` and `Version` were undefined. This was the expected missing
  implementation failure.
- An initial ordinary `go test ./internal/buildinfo` could not run because the
  worktree had no `go.mod` yet; this was a repository bootstrap issue, not the
  behavioral RED check.
- GREEN: after adding `buildinfo.go`,
  `GO111MODULE=off go test ./internal/buildinfo` passed.

## Verification

- `go test ./...` — passed (`internal/buildinfo`).
- `go test -race ./...` — passed (`internal/buildinfo`).
- `go vet ./...` — passed.
- Cross-builds passed for `darwin/arm64`, `darwin/amd64`, `windows/amd64`,
  `linux/amd64`, and `linux/arm64`.
- `git diff --check` — passed.

CI runs tests and vet natively on Ubuntu, macOS, and Windows. Race tests run on
Ubuntu and macOS; Windows is excluded because the race detector depends on a C
toolchain that is not reliably available on hosted Windows runners. The
separate Linux job checks cross-compilation for all five targets above.

## Files changed

- `.gitignore` (retained all existing entries and appended Go build artifacts)
- `.editorconfig`, `LICENSE`, `README.md`, `CONTRIBUTING.md`, `SECURITY.md`
- `Makefile`, `.github/workflows/ci.yml`, `go.mod`
- `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`

## Self-review

- Build identity uses no hostname, user name, commit date, or environment data.
- The README clearly labels the module path as temporary and says it may change
  before the first public release.
- The Makefile targets only invoke Go tooling and do not delete or overwrite
  user data.
- No third-party Go dependencies or frontend/Wails dependencies were added.
- Changes were restricted to Task 1 owned files.

## Concerns

- `SECURITY.md` directs reporters to repository maintainers/private hosting
  vulnerability reporting; maintainers should enable a private reporting
  channel before public release.
- No Go application entry point exists yet, so `go build ./...` validates the
  package set rather than producing an application binary.

## Fix Round 1

### Files changed

- `go.mod`, `.github/workflows/ci.yml`, `README.md`, and `CONTRIBUTING.md` now
  consistently require/use Go 1.27 (`go 1.27.0`, CI `1.27.x`).
- `LICENSE` now contains the complete canonical Apache License 2.0 terms and
  appendix, preceded by `Copyright 2026 Blur contributors`.
- `SECURITY.md` links directly to
  `https://github.com/nielsgroen/blur/security/advisories/new` and says private
  vulnerability reporting must be enabled before public release.
- This report includes this fix-round record.

### Checks and exact results

Toolchain: `go version go1.27.3 darwin/arm64`.

- `diff -u <(tail -n +2 LICENSE) <(curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt)` — passed with no differences after removing the project copyright line.
- `go test ./internal/buildinfo` — passed: `ok github.com/nielsgroen/blur/internal/buildinfo 0.444s`.
- `go test ./...` — passed: `ok github.com/nielsgroen/blur/internal/buildinfo 0.250s`.
- `go test -race ./...` — passed: `ok github.com/nielsgroen/blur/internal/buildinfo 1.439s`.
- `go vet ./...` — passed with no output.
- `set -eu; for target in darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64; do GOOS=${target%/*} GOARCH=${target#*/} go build ./...; done` — passed for all five targets with no output.
- `git diff --check` — passed with no output.

Final verification was rerun after correcting the license spacing. The focused
test, full tests, and race tests each returned `ok
github.com/nielsgroen/blur/internal/buildinfo (cached)`; vet, all five
cross-builds, and `git diff --check` completed with exit status 0 and no output.

An initial canonical-license comparison identified one spacing difference in
the Appendix. It was corrected, and the comparison then passed exactly.

### Self-review

- All declared Go versions now match the binding 1.27 contract, and the local
  Go 1.27.3 toolchain accepted the module syntax and all requested checks.
- The canonical license text compares byte-for-byte after excluding only the
  project copyright line; the license terms were not altered.
- The security URL uses the temporary module/repository identity and does not
  imply that private reporting is currently enabled.
- Changes remain within Task 1 owned files and the implementer report.
