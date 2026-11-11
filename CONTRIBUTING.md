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
