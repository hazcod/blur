# Blur Durable Memory Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` task by task. Apply
> `superpowers:test-driven-development` before implementation and independent
> review after each task.

**Goal:** Turn the approved ephemeral foundation into a durable, authenticated,
AI-optional local memory engine with exact retrieval, passive time accounting,
an O(1) Today recap, and a browsable evidence-backed knowledge library.

**Architecture:** A vetted pure-Go transactional KV engine is only the physical
container. Blur encrypts every personal value with AES-256-GCM using domain-
separated keys derived from an opaque 32-byte data key. Physical lookup and
index keys are keyed hashes, authenticated associated data binds store/schema/
namespace/key/revision, and a signed manifest detects unauthorized record-set
changes. Durable repositories implement the existing `EncryptedStore` contract;
new activity, Today, and knowledge repositories expose bounded page APIs.
Rollback of an entire store remains a documented residual risk until a trusted
external monotonic anchor exists.

**Candidate:** Pin stable `go.etcd.io/bbolt` only if Task 1 proves the selected
version is cgo-free across the release matrix and passes crash, corruption,
license, allocation, and sentinel gates. Its public documentation describes a
pure-Go, ACID, cross-platform, single-writer/multi-reader store. Pebble and
Wasm SQLite remain measured alternatives, not transitive fallbacks.

**Baseline:** reviewed commit `40476c7` on branch
`feature/durable-memory-core`.

## Global limits and security rules

- Directory mode `0700`, data and lock files `0600`; refuse symlinked roots,
  non-regular files, ambiguous ownership/ACL evidence, and permissive modes.
- No plaintext personal content, logical content IDs, exact terms, app/domain
  identifiers, entity names, todos, or summaries in physical keys, values,
  temp files, logs, errors, filenames, or backups.
- AEAD nonces are random and never reused under one key. Wrong keys, swapped
  records, bit flips, manifest mismatch, truncated writes, and schema mismatch
  fail closed with content-free typed errors.
- The physical engine may expose file size, page shape, and approximate write
  activity. Document this metadata leakage; do not call it full-disk secrecy.
- Reads/writes, page sizes, transaction duration, queues, compaction, memory,
  and response bytes are bounded. Do not copy or decrypt an unbounded page.
- Derivations are immutable and source-linked. A source mutation/deletion or
  new secret finding quarantines descendants before they can be served.
- Today hover performs exactly one bounded local snapshot read, with no AI,
  network, live full-store scan, or on-hover materialization.
- Knowledge entities and facts are views over evidence/derivations. Confirmed,
  deterministic, and model-proposed states are distinct; merges are reversible.

## Task 1: Gate the physical store and authenticated record codec

**Files:** `go.mod`, `go.sum`, `internal/storage/recordcodec/*.go`,
`internal/storage/recordcodec/*_test.go`,
`internal/storage/physical/bolt/*.go`,
`internal/storage/physical/bolt/*_test.go`,
`docs/adr/0002-durable-store.md`, `Makefile`, `.github/workflows/ci.yml`

- [ ] Write failing table/property tests for key derivation, nonce uniqueness,
  AAD binding, round trips, wrong key, swapped namespace/key/revision, bit
  flips, truncation, oversize input, invalid UTF-8 metadata, and content-free
  formatting. Keep all fixtures synthetic.
- [ ] Write a physical-store gate that tests mode/regular-file/symlink refusal,
  single-writer transactions, concurrent readers, kill-before/after-commit
  recovery, corruption behavior, close/reopen, and no sentinel plaintext in
  the database, freelist, temp directory, logs, or errors.
- [ ] Pin the smallest current stable bbolt release only after recording its
  license, Go/toolchain compatibility, selected dependency graph, `CgoFiles`,
  `import "C"`, and five-target cross-build results. If any hard gate fails,
  record the failure in ADR 0002 and test the next candidate instead.
- [ ] Implement a standard-library record codec using `crypto/aes`, GCM,
  `crypto/rand`, `crypto/hmac`/SHA-256 (and standard-library HKDF if present),
  versioned envelopes, fixed maximum sizes, and best-effort key-buffer clearing.
  The physical adapter exposes transactions and opaque byte keys/values only.
- [ ] Run focused tests/race, fuzz smoke, `go vet ./...`, the full no-cgo/cross
  matrix, dependency/license inspection, sentinel scan, and physical
  benchmarks. Commit only if ADR 0002 selects a passing candidate.

## Task 2: Implement the persistent encrypted repositories

**Files:** `internal/storage/durable/*.go`,
`internal/storage/durable/*_test.go`, `internal/storage/store.go`,
`internal/storage/store_test.go`, `docs/architecture.md`,
`docs/threat-model.md`

- [ ] Write contract tests shared by the ephemeral and durable stores for
  observations, byte-bounded pages, derivations, generations, activation,
  duplicate handling, ordering, cancellation, close, and concurrent readers.
- [ ] Add adversarial tests for wrong keys, record swapping, missing records,
  unauthorized record insertion/deletion, manifest tampering, crash/reopen,
  interrupted schema upgrade, key loss, and 100k synthetic tiny records.
- [ ] Implement private physical keys as domain-separated HMACs of logical IDs;
  encrypt canonical versioned values; update a keyed record-set manifest in the
  same physical transaction; never persist a plaintext logical ID as a key.
- [ ] Provide idempotent schema migrations and `OpenDurable` with explicit
  create/open modes so missing, malformed, locked, corrupt, wrong-key, and
  unsupported-version states never create or overwrite a store.
- [ ] Prove page byte budgets are enforced before decryption/copy of additional
  records, all returned values are independent copies, and no personal value
  appears in physical bytes. Update architecture and threat limits.

## Task 3: Add a private exact index and durable retrieval path

**Files:** `internal/storage/durable/index*.go`,
`internal/search/index.go`, `internal/search/search.go`,
`internal/search/*_test.go`, `testdata/search/*.jsonl`

- [ ] Write failing tests for Unicode token normalization, AND queries,
  app/domain/time filters, deterministic rank/ties, update/delete consistency,
  stale postings, cancellation, byte/result limits, and source provenance.
- [ ] Store term/app/domain lookup keys as domain-separated HMACs. Posting
  values are encrypted, delta-encoded bounded pages updated atomically with the
  observation and manifest. No term frequency or logical ID is plaintext.
- [ ] Let the search engine prefer the bounded index capability and retain the
  existing scan engine only as an explicit small-store conformance fallback.
  Query proofs bind every effective filter, row, byte, and response limit.
- [x] Benchmark 100k and 1M synthetic observations for ingest, exact search,
  reopen, memory, and disk. Establish measured p50/p95 budgets in the ADR; do
  not claim semantic retrieval in this phase.

## Task 4: Build activity sessions and passive time accounting

**Files:** `internal/activity/*.go`, `internal/activity/*_test.go`,
`internal/storage/durable/activity*.go`

- [ ] Use fake-clock tests for repeated sightings, app/subject switches,
  idle/lock/sleep, missing events, clock jumps, midnight, timezone and DST,
  rule precedence, retroactive classification, and exact total conservation.
- [ ] Implement a bounded recent ring plus durable immutable activity segments.
  Sessionization uses monotonic elapsed time while wall time is metadata; no
  background timer is required when no observation/state event arrives.
- [ ] Add deterministic time rules and reports by app, domain, project/topic,
  confirmed classification, and unclassified time. Store inputs and aggregates
  encrypted with source IDs and versioned rule provenance.
- [ ] Race-test concurrent observation/state events and benchmark a year of
  synthetic segments. Prove report buckets sum exactly to eligible duration.

## Task 5: Materialize the model-free Today snapshot

**Files:** `internal/today/*.go`, `internal/today/*_test.go`,
`internal/storage/durable/today*.go`

- [ ] Define a bounded immutable `Snapshot` with local date/zone, generated-at,
  active subjects, recent changes, confirmed todos, inferred todo candidates,
  open loops, time split, source IDs, and completeness/omission counters.
- [ ] Write tests for incremental updates, restart, late/duplicate observations,
  timezone/DST boundaries, source deletion/quarantine, confirmation/correction/
  dismissal, caps, deterministic ordering, and no-AI/no-network behavior.
- [ ] Implement deterministic subject/change grouping and conservative todo
  candidates from explicit markers only (`TODO`, unchecked boxes, user-created
  items). Candidates remain visually/statefully distinct until confirmed.
- [ ] Persist the snapshot atomically as a materialized view with generation
  provenance. The hover read is one bounded repository lookup and never scans,
  derives, embeds, tokenizes, or calls a provider.
- [ ] Benchmark update latency and hover reads; set a local p95 target and add a
  regression test that provider/network test doubles receive zero calls.

## Task 6: Implement the browsable knowledge library

**Files:** `internal/knowledge/*.go`, `internal/knowledge/*_test.go`,
`internal/storage/durable/knowledge*.go`

- [ ] Define entities for People, Projects, Topics, Places, and Workflows;
  aliases, evidence-backed facts, open loops, related entities, activity
  history, source links, state (`confirmed`, `deterministic`, `proposed`,
  `dismissed`, `merged`), and reversible merge lineage.
- [ ] Test bounded list/show/exact-search, stable pagination, alias collision,
  merge/unmerge, fact correction, source deletion/quarantine, stale derivation,
  restart, and deterministic ordering without AI.
- [ ] Implement user-confirmed CRUD and conservative deterministic materializers
  from explicit tags/rules. Do not guess People names or sensitive attributes
  from prose. Future model proposals use the same quarantined `proposed` path.
- [ ] Store only encrypted records and keyed exact indexes. Every displayed
  fact/open loop links to exact source IDs or an explicit user-authored source;
  no entity page becomes an independent source of truth.
- [ ] Add Today-to-entity navigation contracts and 100k-source benchmarks.

## Task 7: Integrate durable jobs, lifecycle, and diagnostics

**Files:** `internal/memory/*.go`, `internal/memory/*_test.go`,
`internal/lifecycle/*.go`, `internal/diagnostics/*.go`,
`cmd/blur-core/*`, `cmd/blurctl/*`, `README.md`

- [ ] Build an in-process orchestration service with one bounded writer queue,
  explicit backpressure, coalesced materialization jobs, graceful cancellation,
  clean checkpoint/close, and restart recovery. No daemon socket is added yet.
- [ ] Add content-free `log/slog` events for lifecycle/counters/latency/error
  codes only and a doctor report for store version, integrity, queue pressure,
  stale/quarantined derivations, Today freshness, and capability states.
- [ ] Add safe offline CLI fixtures for `doctor`, `today`, and `knowledge`
  against a caller-supplied test key/store path. Never put keys in arguments,
  environment variables, config, shell history, or logs; production key wiring
  remains blocked on Phase 3 OS secret adapters.
- [ ] Test crash/restart during each job, saturated queues, cancellation,
  repeated materialization, wrong key, and sentinel absence from diagnostics.

## Task 8: Durable-core security and release checkpoint

**Files:** `docs/architecture.md`, `docs/threat-model.md`, `README.md`,
`docs/adr/0002-durable-store.md`

- [ ] Run unit/race/fuzz suites, vet, current vulnerability scan, dependency and
  license audit, no-cgo/cross-build gates, 100k/1M benchmarks, crash/tamper/
  wrong-key/sentinel tests, and `git diff --check`.
- [ ] Document what the encrypted container hides and leaks, rollback limits,
  key-memory and swap/crash-dump limits, bbolt/selected-engine caveats, repair
  policy, and why no actual personal store opens without an OS-wrapped key.
- [ ] Obtain an independent whole-phase code/security review. Do not begin OS
  secret/capture/IPC wiring until no Critical or Important finding remains.

## Completion gate

This phase is complete only when a wrong key or tamper cannot return data, no
synthetic personal sentinel appears in any physical artifact, durable exact
search is bounded and benchmarked, activity totals conserve time, Today hover
is one model-free local read, every knowledge fact has provenance, all target
builds remain cgo-free, and independent review approves the phase.
