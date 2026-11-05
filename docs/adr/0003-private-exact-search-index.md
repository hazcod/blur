# ADR 0003: Add a private, bounded exact-search index

Status: accepted — durable-memory-core Task 3 (2026-11-05).

## Context

Durable observations are sanitized and encrypted, but scanning them for every
query becomes costly as the local store grows. The store has no authoritative
observation timestamp, so time-window retrieval cannot be inferred safely.
Task 2's bounded writer transaction is the atomic boundary for updating an
observation, its index entries, and the authenticated manifest.

## Decision

Use a private exact index. Normalize tokens by Go Unicode lowercase mapping,
split on Unicode runes other than letters and digits, deduplicate, then sort by
UTF-8 byte order. There is no locale-specific folding, stemming,
transliteration, or Unicode normalization. At most 128 unique terms are
indexed per observation; a larger term set rejects the whole observation
transaction with `storage.Invalid`, rather than silently omitting searchable
terms.

Term, app-filter, domain-filter, and reverse-link lookup keys are HMAC-SHA-256
values with separate domain labels and the store's keyed identifier secret.
Each private posting list is an encrypted B+ tree. Leaves contain at most 64
source IDs; packed branch nodes contain at most 1,024 child references and
encrypted source-ID-order fences. Node contents and source IDs are
encrypted/authenticated. Child node IDs are random and only their keyed
physical addresses are stored. Reverse links map an observation to every
lookup key it references, so its postings can be removed transactionally.
Observation writes/deletes, tree changes, reverse links, and the manifest share
one `Store.Update` transaction.

When opening an existing store without a completed-index marker, opening first
removes any incomplete index, rebuilds it in bounded transactions, and commits
the marker last. `OpenDurable` does not return the store until that succeeds.
After a crash, the missing marker causes the next open to discard partial
pages and repeat the rebuild; a partial index is never exposed as ready.

Queries use exact AND semantics, with canonical bounded app/domain equality
filters. Each posting tree is traversed in rank-descending/source-ID-ascending
order; intersection checks encrypted reverse links and proves the global top-K
prefix before returning it. App-field match scores 3, domain 2, and text 1 per
distinct query term; ties use ascending source ID. If row, node, or byte bounds
are reached before the prefix is proven, the index returns typed
`ErrIndexBudget` rather than a potentially misranked partial page. Results
retain `raw_sanitized_observation` provenance. Search uses a default maximum of
1,000 candidate posting rows, 1,024 index nodes, and 2 MiB of index/source-field
budget; request limits can reduce but not exceed those ceilings. The byte count
includes index keys and encrypted values plus the authenticated source-field
byte estimate used before hydration; it is a logical retrieval budget, not a
measure of bbolt page I/O or filesystem bytes.

Only stores without the exact-index capability may use the old observation
scan, and only when `allow_scan_fallback` is explicitly true. That fallback
remains bounded to 64 rows and 2 MiB. Non-empty time filters return a typed
unsupported-filter error until Task 4 provides source-linked activity segments;
no timestamps are synthesized. The search proof digest binds canonical
app/domain/time filters, fallback selection, and effective defaults for every
result, index, and scan limit.

## Privacy and trade-offs

No plaintext term, app/domain value, logical observation ID, or term-frequency
value appears in index artifacts. HMACs hide lookup values and AEAD protects
posting contents, source IDs, branch fences, and reverse links. The physical
key reveals index kind, stable keyed lookup digest (allowing same-store
equality/linkage for a repeated term or app/domain value), and a keyed node
address. The number of nodes in a tree and its depth are observable from record
counts; encrypted node payload lengths reveal leaf/branch size classes and
approximate occupancy (up to 64 posting rows per leaf and 1,024 child references
per branch). Write timing and key insertion/deletion reveal update activity.
This residual structural and equality leakage is accepted for this local exact
index and must remain explicit in threat-model reviews.

Leaf and packed-branch splits move a median split along the root-to-leaf path;
they do not rebucket all siblings. The maximum tree depth is four and all node
reads, rows, bytes, and mutations remain bounded. A transaction that reaches
the physical operation/byte ceiling aborts atomically; it is never split into
multiple commits for one observation.

## Scale benchmark

`internal/storage/durable/index_bench_test.go` builds 100,000- and 1,000,000-
observation durable synthetic corpora only when explicitly opted in. The
synthetic records vary punctuation (not tokens), keeping a single common term
while preserving unique content IDs. Reproduction command:

```sh
BLUR_RUN_SCALE_BENCH=1 go test ./internal/storage/durable -run '^$' \
	-bench '^BenchmarkDurableExactIndexScale$' -benchtime=1x -count=1 -timeout=45m -v
```

Each corpus is built once in 32-observation write transactions. The benchmark
reports total ingest time and p50/p95 for each transaction, then p50/p95 for 20
bounded 1,000-row index reads and 20 end-to-end searches that return up to 100
hydrated results. It also reports p50/p95 over five store reopen operations.
It records database file size and Go heap-in-use after a forced GC; heap metrics
do not include bbolt's memory-mapped pages or total process RSS.
`-benchtime=1x` is intentional: Go's benchmark calibration otherwise invokes
the benchmark function again, which would rebuild these expensive corpora.
Corpus setup is excluded from the default `ns/op` timer; the separately logged
ingest duration and latency samples include it. Recorded environment and
results below are from the opt-in run on an Apple M5 Max, darwin/arm64,
Go 1.27.3. Query and reopen p50/p95 samples are warm-cache measurements; the
end-to-end sample follows the 20 raw index reads. The percentile method is
median (averaging the two middle values when even) and nearest-rank p95.

| Scale | Total setup/ingest | 32-row write p50 / p95 | Index read p50 / p95 | Search p50 / p95 | Reopen p50 / p95 | DB bytes | Heap-in-use |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100k | 71.468 s | 22.700 / 27.384 ms | 0.238 / 0.530 ms | 1.837 / 2.355 ms | 270.500 / 288.780 ms | 101,302,272 | 5.039 MiB |
| 1M | 802.446 s (13m22.446s) | 24.775 / 30.691 ms | 0.225 / 0.400 ms | 1.612 / 1.749 ms | 2.335 / 2.351 s | 913,227,776 | 7.906 MiB |

For this hardware/toolchain and this dense synthetic corpus, reference p95
regression budgets are: 50 ms per 32-row write, 2.5 ms for a bounded index
read, 8 ms for an end-to-end search returning up to 100 hydrated results, and
6 s for a durable reopen at 1M records. These are local regression budgets,
not portable SLOs. The memory figure is Go heap-in-use after forced GC while
the DB is open; it excludes bbolt-mapped pages and process RSS. The benchmark
stores about 913 MB (870.9 MiB) for 1M records, so disk footprint remains a
material trade-off even though exact read latency stays bounded.

A separate `BenchmarkDurableExactIndexRankedOverCap` run exercises a 1,036-
record corpus where field-score winners occur beyond the prior 1,000-row
candidate prefix. It prebuilds once, then takes 20 bounded ranked-query samples
with `-benchtime=1x`. The recorded p50/p95 were 134,583/348,541 ns. With only 20
samples, these percentile labels describe the bounded local sample, not a
population-level statistical estimate.
