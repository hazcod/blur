# Final Review Fix Round 1

## Outcome

Both Important findings from `final-review.md` are addressed on top of HEAD
`c749163`. No dependencies, cgo, network paths, or unrelated feature work were
added.

## Grant lifecycle

`GrantStore` now enforces a hard 1,024-record cap. Issuance and authorization
prune expired/revoked records; explicit revocation removes a grant immediately.
Removal clears nonce and scope maps and overwrites authenticator-key bytes
before deleting the record. Issuance fails with the existing content-free
`ErrInvalidGrant` when the active cap remains full. Tests exercise cap
rejection, capacity recovery, old-proof denial, and observable key/map clearing
for revoke and both expiry paths.

Key overwriting is best-effort process-memory cleanup. It cannot erase caller
copies, prior runtime copies, swap, or crash artifacts.

## Search bounds

Search is capped at 64 observations and 2 MiB of aggregate text/app/domain
bytes, with four-row pages. The storage listing contract now requires a
byte-bounded page request and returns exact field-byte counts plus `HasMore`.
The memory store indexes searchable field lengths and checks the budget before
cloning an observation into a page. Search checks returned accounting before
tokenization, reports scanned bytes, and reports truncation/omissions
separately. A missing `scan_bytes` field retains the prior request shape and
uses the same fixed 2 MiB maximum; the proof digest binds that effective limit.
The core admits at most two concurrent searches; waiting uses context
cancellation and every acquired slot is released on return. Tests cover
maximum-size observations, page limits/metadata, accounting, response bytes,
effective-limit digest semantics, bounded concurrency, waiter cancellation,
and slot reuse.

The concurrency bound applies at the core service boundary. A canceled waiter
has already consumed its one-use proof. Store implementations must honor the
byte-bounded listing contract; the search-side checks cannot undo allocation by
an implementation that violates it.

## Verification

On `go1.27.3 darwin/arm64`, all of the following passed after the final code
changes:

- `go test ./...`
- `go test -race ./...`
- `go test -race ./internal/localapi ./internal/search ./internal/storage ./internal/core -count=1`
- `go vet ./...`
- `make build cross-build cross-test-compile no-cgo-audit`
- `go run ./cmd/blur-core`
- `go run ./cmd/blurctl status`
- `go run ./cmd/blurctl capabilities`
- `git diff --check`

The cross-build matrix compiled Darwin arm64/amd64, Windows amd64, and Linux
amd64/arm64 command binaries. Cross-test compilation covered Darwin arm64,
Windows amd64, and Linux amd64. These are compile checks, not execution on
those foreign operating systems. Host smokes reported the expected
ephemeral-memory/offline status and disabled capability set.
