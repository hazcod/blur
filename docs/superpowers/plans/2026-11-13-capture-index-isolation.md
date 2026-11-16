# Capture Index Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline. No implementer agents; one final read-only review.

**Goal:** Reuse the encrypted exact tree for an independent capture-part scope without changing observation records or transaction ownership.

**Architecture:** Immutable private Store views share storeState. A searchIndexTx facade embeds the original *Tx and owns only an immutable scope view; physical writes and manifests stay on that same transaction.

**Tech Stack:** Existing Go/bbolt encrypted tree, no dependency.

**Spec:** `docs/superpowers/specs/2026-11-13-capture-index-isolation-design.md`

## Global Constraints

- Observation group 0x40 and existing key/hash/record/version bytes stay unchanged; capture scope uses group 0x41 and distinct hash/record domains.
- Do not copy Tx/mutex/manifest, mutate shared scope, create a second DB owner, or weaken shared deletion/close gates.
- No production capture-index creation or prerequisite app replacement. Full parent capture/recall/native scope stays open; actual startup cause remains unknown.
- Existing dirty worktree and settings retained; no commit/merge/reset, user store/key access or GUI lifecycle action.

## Review Focus

- A scope clear must leave the other scope's ciphertext bytes unchanged and searchable after reopen.
- Both scopes index the same source ID with different words; no posting, reverse or pending collision.
- A tree splits beyond 64 postings while another scope is written concurrently; independent ranking and one valid manifest remain.
- Replay a ciphertext from one scope under the other's key; authentication must fail without accepting a foreign state/root.
- A failed multi-scope transaction must roll back both scopes; deletion and close gates block both.

## Task 1: Scope the existing tree without duplicating transactions

**Files:** create `internal/storage/durable/index_scope.go`, `index_scope_test.go`; modify `store.go`, `index.go`, `index_tree.go`, `index_pending.go`.

**Interfaces:** private `(*Store).capturePartIndex() *Store`; `(*Tx).observationSearch() *searchIndexTx`; `(*Tx).capturePartSearch() *searchIndexTx`. `searchIndexTx` embeds *Tx and shadows only store with its immutable scoped view. Store helpers `exactGroup() byte`, `exactNamespace(string) string`, `exactStateKey() []byte`, `exactPrefix() []byte`, `exactPendingPrefix() []byte`. Existing Tx index wrappers keep their signatures. Scoped Store.SearchExact uses existing search.ExactQuery/ExactPage; observation-specific bootstrap/primary APIs are not called on the private capture view.

- [x] Write real encrypted scope isolation/filter/pending/rollback/reopen/clear/split/replay/concurrency/gate tests. First run must fail because capture scope is absent; no fake DB or source-text assertions.
- [x] Introduce immutable scope helpers and the live Tx facade; route tree keys, hash domains, encrypted record namespaces, state/pending reads and scope clear through them. Keep existing observation wrappers and all physical writes on the original Tx.
- [x] Run `go test ./... -count=1` (includes the complete durable suite); expected PASS. Then affected races, vet, CGO-disabled build and cross compile; expected exit 0 with full output read.
- [x] One fresh read-only review; material findings get one RED/GREEN fix pass and relevant full gates. Record deferred parent work and unchanged installed app. Do not call the whole goal complete.
