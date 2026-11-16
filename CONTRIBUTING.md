# Contributing

Contributions should preserve the clean-room, local-first, privacy-first design
described in the project specification. Do not copy source code, prompts,
branding, icons, or other creative assets from another product.

## Workflow

1. Fork the repository and create a topic branch.
2. Keep changes focused and document user-visible behavior.
3. Add or update tests for behavior changes.
4. Open a pull request against `main` and fill in the template.

## Verification

Use Go 1.27 or newer (see `go.mod`). Before submitting, run:

```sh
make test race vet
make no-cgo-audit cross-build cross-test-compile
make vulncheck
```

CI also runs `gofmt`, `golangci-lint` (`.github/golangci.yml`), and CodeQL. Please
state the platforms and Go version you used for platform-sensitive changes.

## Local macOS app

Build the app used on this Mac with:

```sh
make macos-app-stable
```

This replaces `~/Downloads/Blur.app` with bundle identifier `com.hazcod.blur`.
Keep using this path for local builds, including builds from worktrees. Do not
create timestamped, renamed, acceptance, or alternate local app bundles.

The build reuses the existing signing identity and checks that the staged app
has the same designated requirement and exact signing certificate before
replacement. If the existing certificate is unavailable, restore that identity;
do not switch to ad-hoc signing, create a replacement certificate, alter trust,
or reset Accessibility permissions. Those changes can cause macOS to request
permission again. Direct-download builds are unsandboxed because macOS forbids
cross-app Accessibility capture inside App Sandbox. Existing container memory
and settings are reused in place. Building does not launch or restart the app.

## Rules

- **cgo-free.** Shipped targets build with `CGO_ENABLED=0`. Do not add cgo or
  dependencies that need it; `make no-cgo-audit` enforces this.
- **Dependency forks.** Two dependencies are replaced by forks in `go.mod` until
  upstream merges our fixes; see `docs/dependency-forks.md`. Avoid new
  dependencies unless the design and security tradeoffs are clear.
- **Privacy.** Accessibility capture and data handling changes must explain
  privacy boundaries and failure behavior.

## Commit messages

Use conventional prefixes: `feat`, `fix`, `docs`, `refactor`, `chore`, `deps`,
optionally scoped, e.g. `fix(capture): bound queue growth`.
