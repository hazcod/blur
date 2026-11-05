# ADR 0003: Add a private, bounded exact-search index

Status: accepted — durable-memory-core Task 3 (2026-11-05).

## Context

Durable observations are sanitized and encrypted, but scanning them for every
query becomes costly as the local store grows. The store has no authoritative
observation timestamp, so time-window retrieval cannot be inferred safely.
Task 2's bounded writer transaction is the atomic boundary for updating an
observation, its index entries, and the authenticated manifest.

## Decision

Use an exact inverted index. Normalize tokens by Go Unicode lowercase mapping,
split on Unicode runes other than letters and digits, deduplicate, then sort by
UTF-8 byte order. There is no locale-specific folding, stemming,
transliteration, or Unicode normalization. At most 128 unique terms are
indexed per observation; a larger term set rejects the whole observation
transaction with `storage.Invalid`, rather than silently omitting searchable
terms.

Term, app-filter, domain-filter, and reverse-link lookup keys are HMAC-SHA-256
values with separate domain labels and the store's keyed identifier secret.
Posting pages hold at most 64 source IDs and are encrypted/authenticated; each
bucket's counts and each observation's reverse links are encrypted too. The
reverse link maps an observation back to the pages that must be changed when
the internal deletion fixture removes it. Observation writes/deletes and all
affected pages, metadata, and the manifest share one `Store.Update` transaction.

When opening an existing store without a completed-index marker, opening first
removes any incomplete index, rebuilds it in bounded transactions, and commits
the marker last. `OpenDurable` does not return the store until that succeeds.
After a crash, the missing marker causes the next open to discard partial
pages and repeat the rebuild; a partial index is never exposed as ready.

Queries use exact AND semantics, with canonical bounded app/domain equality
filters. The index starts from the smallest encrypted posting count and
intersects the other terms and filters using encrypted reverse links. Ranking
is deterministic: app term match = 3, domain = 2, text = 1 per distinct query
term; ties use ascending source ID. Results retain
`raw_sanitized_observation` provenance. Search uses a default maximum of 1,000
candidate posting rows and 2 MiB of index/source-field budget; request limits
can reduce but not exceed those ceilings. The byte count includes index keys
and encrypted values plus the authenticated source-field byte estimate used
before hydration; it is a logical retrieval budget, not a measure of bbolt
page I/O or filesystem bytes.

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
posting contents, exact counts, source IDs, and reverse links. The physical
page key still exposes index kind, page sequence, and number of pages; page
counts therefore leak a coarse posting-frequency bound (up to 64 rows per
page). Ciphertext lengths and change timing also leak approximate page
occupancy, record-size class, and update/delete activity. This is accepted for
the local exact index and must remain explicit in threat-model reviews.

The index favors bounded updates and exact retrieval over compactness. Content
IDs in each page are prefix-delta encoded within that fixed 64-row page. Page
splits are append-only; deletions leave holes rather than shifting later page
references, and a later insertion can reuse an empty trailing page.

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
| 100k | 47.181 s | 14.593 / 17.618 ms | 1.346 / 1.536 ms | 3.066 / 3.169 ms | 261.360 / 275.334 ms | 100,925,440 | 3.383 MiB |
| 1M | 803.951 s (13m23.951s) | 22.684 / 41.581 ms | 1.434 / 1.806 ms | 3.419 / 6.071 ms | 3.042 / 5.054 s | 992,755,712 | 6.836 MiB |

For this hardware/toolchain and this dense synthetic corpus, reference p95
regression budgets are: 50 ms per 32-row write, 2.5 ms for a bounded index
read, 8 ms for an end-to-end search returning up to 100 hydrated results, and
6 s for a durable reopen at 1M records. These are local regression budgets,
not portable SLOs. The memory figure is Go heap-in-use after forced GC while
the DB is open; it excludes bbolt-mapped pages and process RSS. The benchmark
stores one nearly-1GB DB for 1M records, so disk footprint remains a material
trade-off even though exact read latency stays bounded.
