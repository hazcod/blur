# ADR 0003: Add a private, bounded exact-search index

Status: accepted — durable-memory-core Task 3 (2026-11-05); amended for durable queued indexing (2026-11-12).

## Context

Durable observations are sanitized and encrypted, but scanning them for every
query becomes costly as the local store grows. The initial store lacked an
authoritative observation timestamp; authenticated capture metadata and a
separate timeline have since supplied that capability. The bounded writer is
still the atomic boundary for each primary-source receipt or derived-index
batch and its authenticated manifest.

## Decision

Use a private exact index. Normalize tokens by Go Unicode lowercase mapping,
split on Unicode runes other than letters and digits, deduplicate, then sort by
UTF-8 byte order. There is no locale-specific folding, stemming,
transliteration, or Unicode normalization. The complete term set is indexed.
The former 128-term document rejection was a capture-loss bug: it coupled a
batch budget to source acceptance. `InlineObservationTerms` now names only an
optimization threshold. It never limits document coverage.

Primary source, authenticated time/size metadata, timeline/hour links and an
encrypted ID-only indexing receipt commit first. Posting maintenance runs in
separate transactions. Small sources attempt the existing inline tree update
after that commit; failure leaves the receipt for repair and cannot undo the
accepted source. Larger sources advance at most 64 terms per transaction.
An always-on local worker resumes receipts independently of all AI settings.

Term, app-filter, domain-filter, and reverse-link lookup keys are HMAC-SHA-256
values with separate domain labels and the store's keyed identifier secret.
Each private posting list is an encrypted B+ tree. Leaves contain at most 64
source IDs; packed branch nodes contain at most 1,024 child references and
encrypted source-ID-order fences. Node contents, source IDs, child IDs, branch
fences, and deferred sibling links are encrypted/authenticated. Node IDs are
128-bit opaque values inside encrypted nodes; physical node records are
addressed by keyed HMACs. Reverse links map an observation to every lookup key
it references, so its postings can be removed completely. Small completed
sources retain the bounded v4 reverse record. Large sources use a v5 reverse
root with term cursor, total, erasure cursor and app/domain references. Bounded
encrypted reverse pages enumerate every indexed term for deletion. Keyed,
encrypted per-term membership proofs let a query score a large source without
loading its whole vocabulary. Each batch publishes its postings, proof records,
reverse page and cursor atomically with the manifest. Source existence is
rechecked in every writer transaction; erasure cannot be undone by a late job.
Large-source erasure drains reverse pages under the existing encrypted erasure
journal/publication gate, then removes the source and final receipt atomically.

Index format version 4 uses encrypted deferred right-sibling links for a
non-root branch split. This avoids materializing/rebucketing a second full
1,024-entry packed branch in the observation transaction. Readers follow the
encrypted link within the same root-to-leaf depth and still prove the ordered
top-K prefix. A following non-splitting mutation repairs the parent pointer in
that same transaction; a second split at a branch with an outstanding link
fails closed and rolls back instead of creating an unbounded sibling chain.
The format-version mismatch causes versions 1 through 3 to be cleared and
rebuilt before the store is exposed; there is no mixed-version query state.

When opening an existing store without a completed-index marker, opening first
removes any incompatible index, queues authenticated sources in bounded
transactions, and commits the format marker last. Small sources are usually
indexed before return; large-source receipts resume in the worker. Ordinary
restart retains committed term batches and continues their cursors. A missing
format marker still restarts format reconstruction. Format readiness is
separate from complete coverage: exact pages and MCP expose `indexing_pending`,
and native Search/Ask/Settings explain incomplete coverage. Pending empty
results never assert that no source was saved.

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
per branch). A deferred split adds a node record just like a regular split;
the encrypted sibling link and ordering fence are not visible in plaintext.
Write timing and key insertion/deletion reveal update activity.
This residual structural and equality leakage is accepted for this local exact
index and must remain explicit in threat-model reviews.

Leaf and packed-branch splits move a median split along the root-to-leaf path;
they do not rebucket all siblings. The maximum tree depth is four and all node
reads, rows, bytes, and mutations remain bounded. The declared write estimate
is seven operations per affected posting tree plus 20 fixed observation,
reverse-link, size-row, and manifest operations. With the inline threshold of 128 unique
terms plus app and domain trees (130 trees), the estimate is 930
operations, leaving headroom under the physical writer's 1,024-operation cap.
Those estimates describe the optional small-source posting transaction,
separate from source acceptance. A posting transaction that reaches the
physical operation/byte ceiling aborts atomically and leaves work pending.
Large sources intentionally use multiple resumable commits. The 64-term batch
bound also reserves space for proof/page/cursor records; no physical cap was
raised. The
16 MiB limit is per physical transaction, not a global posting-store cap. No
global posting-count or total-index-size cap is currently enforced; bounded
page/value sizes and query/transaction budgets are not a global storage quota.

The synchronized max-130 branch-split fixture measured 918 writer operations
and 13,754,884 charged bytes; ordinary sibling repair measured 788 operations
and 3,405,197 bytes; repair concurrent with a full-leaf split measured 918
operations and 7,243,219 bytes. These figures include manifest load/flush
work, and each stayed below the 1,024-operation and 16 MiB transaction caps
with at least 64 operations and 1 MiB of byte headroom.

The packed non-root branch cascade has an opt-in focused benchmark. It builds a
synthetic 1,024-child encrypted branch with a full 64-row leaf, then records 20
independent split samples while resetting the fixture outside each timed
sample. Reproduce with:

```sh
BLUR_RUN_BRANCH_CASCADE_BENCH=1 go test ./internal/storage/durable -run '^$' \
	-bench '^BenchmarkDurableIndexPackedBranchSplitCascade$' -benchtime=1x -count=1 -v
```

On the environment above, the 20-sample split p50/p95 were 8.876/11.846 ms;
the operation used 9 charged writer operations and 102,020 transaction bytes.
These are local synthetic fixture measurements, not portable latency targets.

## Scale benchmark

The measurements below predate queued source acceptance and its additional
transactions. They remain historical posting-tree baselines, not verified
latency claims for the amended ingestion path.

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

The table above is the format-v3 scale baseline. The current format-v4
sibling-link implementation has now completed both corpus sizes on the same
host. These are fresh `-benchtime=1x` focused runs; the 1M run used
`-timeout=30m` and completed in 788.072 s total test time:

| Format-v4 scale | Total setup/ingest | 32-row write p50 / p95 | Index read p50 / p95 | Search p50 / p95 | Reopen p50 / p95 | DB bytes | Heap-in-use |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100k | 66.707 s | 21.615 / 24.581 ms | 0.209 / 0.388 ms | 1.657 / 1.862 ms | 0.227 / 0.230 s | 101,253,120 | 1.633 MiB |
| 1M | 773.275 s (12m53.275s) | 23.784 / 29.752 ms | 0.220 / 0.429 ms | 1.613 / 1.672 ms | 2.384 / 2.403 s | 913,293,312 | 7.930 MiB |

Both current-format scales remain below the local regression budgets above.
The 1M corpus consumes 871.0 MiB on disk, which remains a material storage
trade-off. These results do not represent low-end hardware, cold-cache
percentiles, or real user corpora.

A separate `BenchmarkDurableExactIndexRankedOverCap` run exercises a 1,036-
record corpus where field-score winners occur beyond the prior 1,000-row
candidate prefix. It prebuilds once, then takes 20 bounded ranked-query samples
with `-benchtime=1x`. The recorded p50/p95 were 134,583/348,541 ns. With only 20
samples, these percentile labels describe the bounded local sample, not a
population-level statistical estimate.
