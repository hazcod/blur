# Blur functional gap review — 2026-11-22

Scope: source capture and publication, exact/semantic indexing, derivation and recaps, retrieval and follow-ups, voice persistence, authored notes, tasks/reminders, retention/deletion, sync and lifecycle. Review used source tracing and reproducible Go overlay tests against temporary stores. No UI, production database writes, or application restart. This is a code-level functional review, not certification of every native OS/provider combination.

The governing rule is: resource limits bound one request, page or work quantum. Accepted content needs durable continuation, or an explicit failure/coverage receipt. A constant is not justified merely because it keeps a request small.

## Confirmed findings

| Priority | Gap and concrete consequence | Location | Verification / disposition |
|---|---|---|---|
| P1 | Semantic work saves only after the entire source. A later batch timeout repeatedly discards successful earlier embeddings; large or slow sources and later backfill never finish. | `internal/semantic/service.go`, `buildCapture`, `indexSource`, worker quantum | Fixed with encrypted per-batch partial progress, exact revision/model checks and final atomic publication. Restart/eventual-completion and deletion tests pass. |
| P1 | Retention invalidation clears an unsaved authored Markdown draft, even when the sweep removes zero sources. | `internal/client/note_document_view.go`, memory invalidation; retention callback in `bootstrap.go` | New authored drafts now survive background invalidation; explicit discard and stored-note deletion still clear their views. Real zero-deletion retention regression RED→GREEN; note erasure/late-receipt tests pass. Editing an existing stored note still uses conservative full invalidation. |
| P1 | Export cursor advances across imported observations. A fast peer's timestamp causes subsequent local observations to fall permanently below the export watermark. | `internal/replica/writer.go`, `Flush` | Fixed: authenticated exact-revision receipts replace timestamp authority, with bounded physical scans and idempotent acknowledgement pages. Three-device future-peer, equal-time/backdated, same-ID/time revision and real 513-row continuation tests pass, including reopen/race checks. |
| P1 | Segment publication precedes local sequence persistence. After a state-write failure/crash, retry overwrites an already consumed sequence with different content. | `internal/replica/writer.go`, `Flush` | Fixed with a locally sealed pending segment, immutable exclusive publication and exact replay. Actual post-publication state failure, peer consumption and encrypted-state reopen preserve segment bytes and deliver later text. Deleted unpublished work and failed privacy-state writes cannot republish erased data. |
| P1 | Changing sync folder A→B→A resets sequence while retaining device identity, overwriting old segment identities that peers already consumed. | `internal/client/sync_settings.go`, folder change/reset | Fixed: changed folders rotate writer identity and reset that generation's progress. Native A→B→A tests preserve consumed public bytes and deliver new text contiguously; focused race checks pass. |
| P2 | Safari DOM extraction restarts from the beginning at each node/byte/time limit; tail-only changes remain invisible. Its truncation flag is also dropped before storage. | `platform/macos/SafariExtension/Resources/Injected.js`; `ExtensionHandler.swift`; `internal/browsercapture/snapshot.go` | Fixed with acknowledged bounded batches, retained DOM cursor, revision fencing, private multipart staging and explicit partial provenance. Fourteen Node tests, real encrypted tail-search/redaction/retry tests, browsercapture race checks and native Swift protocol tests pass. Continuously rewritten pages can remain explicitly partial; evicted/private interrupted scans restart. |
| P2 | Activity retry queue evicts its oldest segment at 257 pending writes, permanently losing duration and source links. | `internal/ingest/service.go`, pending activity | Fixed with encrypted durable acceptance, atomic final write/acknowledgement, storage backpressure and no-event restart continuation. 257-row reopen/exact-duration, idle recovery and deletion-after-peek tests pass. |
| P2 | Safari bridge saves bypass shared post-capture notifications, delaying Library/semantic work and omitting live current-hour recap input. | `internal/client/capture_darwin.go`; `internal/browsercapture/bridge.go`, `persist` | Fixed: shared observation/document commit callbacks run after preference admission. Native-publication client integration tests pass. |
| P2 | A whole authored Library fact is discarded when it crosses a 1,500-byte digest gate. It may have no original-source fallback. | `internal/client/ask_retrieval.go`, entity evidence construction | Fixed: full authored/inferred fact evidence reaches normal context packing. Tests cover ~1.7 KB facts and a stored ~1 MB entity, retaining inspectable evidence and honest answer-excerpt coverage. |
| P2 | Offline/Apple direct entity shortcut reads original captures and bypasses saved Library corrections. | `internal/client/ask_retrieval.go`, `directEntitySummary` | Fixed through scoped correction authority, original-source suppression and no-revival handling for dismissed facts. Real encrypted offline/controlled-Apple, folded-name, app/date, legacy correction and note revision/deletion regressions pass. Direct sample/excerpt/index-pending coverage now reaches Result; focused race checks pass. Integration evidence is recorded below. |
| P2 | Initial topic Ask submits periods over 31 days directly to an exact-search API that rejects them. | `internal/client/ask_retrieval.go`, windowed search helpers | Fixed through lazy 28-day physical queries and global top-K merging. Year, three-year and ten-year regressions include early/middle/tail and older revisited sources; final integration passed. |
| P2 | Dated topic Ask prepends all-time inferred facts without checking supporting capture dates. | `internal/client/knowledge_enrichment.go`, `library_time_scope.go` | Fixed with per-claim observed support, exact historical revisits and monthly physical paging for long ranges. Undated authored claims do not inherit unrelated dates; correction suppression remains authoritative. Eight focused regressions pass under race checks, with independent review. |
| P2 | Dirty calendar rollup counts as covered, suppressing fresh child-hour recaps; pending status never reaches final answer coverage. | `internal/client/rollup_retrieval.go`; `recap_retrieval.go` | Fixed: dirty parents yield to fresh child recaps, dirty-only results disclose refresh pending. Day/hour and week/day regressions pass. |
| P2 | Literal/technical Ask cuts candidates to eight but ignores HasMore, reporting no coverage gap. | `internal/client/ask_retrieval.go`, `lookupEvidence` | Fixed: eight complete matches remain complete; nine matches returning eight are explicitly limited. Unknown omission counts remain unknown. |
| P2 | Deleting any voice note clears all native reminders. An unrelated due reminder can become permanently elapsed before reconstruction. | `internal/deletion/meetings.go`, `GuardAll`; `internal/reminders/service.go`, reconciliation | Fixed with targeted cancellation under the native scheduling guard, using authenticated cleanup-only metadata during erasure recovery. Unrelated pending/delivered notices and attempted receipts remain intact. Interrupted-journal, stale-source, full 1,024-notice and deletion/reminder race tests pass. |
| P2 | Disabling sync does not invalidate an already running export. It can publish new data after Turn off succeeds. | `internal/client/sync_settings.go`; `internal/replica/writer.go` | Fixed with operation cancellation and a configuration gate shared with immutable publication/replay. Deterministic blocked-export and native Turn off tests prove successful disable excludes later publication; race checks pass. |
| P2 | A source loses activity links after 64 non-coalescing intervals, breaking duration attribution and complete erasure. | `internal/storage/durable/activity.go`, `activity_links.go` | Fixed: encrypted linked reverse directory replaces the total-count cap, retaining old array entries. 130-interval, reopen and bounded erasure regressions pass. Already discarded historical links cannot be reconstructed. |
| P2 | Daily activity storage rejects its 4,097th interval as Invalid; retry code then discards that already accepted work. | `internal/storage/durable/activity_index.go` | Fixed: linked live pages with unbounded day continuation, direct cursor lookup and bounded empty-page unlinking. Tests save/page more than 4,096 real intervals, preserve middle cursors and delete the last row after a million-page historical high-water without scanning it. |
| P2 | A 400-day search horizon and eagerly aggregated activity IDs reject large histories; dense short Ask also preloads too many intervals. | `internal/client/search_scope*.go`, `ask_retrieval.go`; `internal/semantic/service.go` | Lazy window and activity-page iteration replaces the total horizon/global-ID cap, with one global selected-model ranking call. Ten-year, 80,000-ID, dense two-day Ask and cancellation tests pass; final integration passed. |
| P2 | Relative question parsing silently rejects more than 366 days, 720 hours or 10,080 minutes and does not recognize numeric weeks/months/years. | `internal/assistant/planner_relative_period.go` | Fixed. Actual question-planning regressions cover three years, 24 months, 104 weeks, 1,000 days and longer hour/minute periods. Overflow includes all representable history rather than wrapping into recent time; repeated topic numbers survive. |
| P2 | Observation and document timelines reject writes after 1,024 pages in an hour, conflating a read-work budget with storage capacity. | `internal/storage/durable/observation_timeline.go`, `capture_timeline.go` | Fixed writer/metadata continuation beyond that boundary. Only the binary uint32 representation guard remains; independent dense-read scan budgets still return explicit Incomplete. Boundary tests exercise actual tail rollover without claiming a 65,537-primary-source load test. |
| P1 | A processor/model-generation reset prunes all source-linked Library records inside one transaction. Large shared records exceed the physical byte budget, so every retry strands the same source and directory cursor. | `internal/storage/durable/knowledge_enrichment.go`, `enrichment_generation_reset.go` | Fixed with bounded per-entity cleanup and exact old-progress validation at each step and final publication. Maximum-fanout, interruption/reopen, deletion, correction, cursor race and Library-clear regressions pass; dedicated race checks and fresh independent review pass. |
| P2 | Voice history rejects session 4,097 even though its records already support bounded pagination. | `internal/storage/durable/meetings.go`, catalog admission/header validation | Fixed. A regression creates 4,100 real encrypted sessions, reopens and pages all rows, deletes cursor/hole rows, adds another session and reuses the old cursor after another reopen. The actual 48-bit ordinal and Live<=Last invariants remain. |
| P2 | A voice session stops at 32,768 final segments, 16 MiB transcript, 256 manual notes, 1 MiB manual text or eight hours; the engine retains every final and duplicate key in RAM. Native admission separately drops offsets after 24 hours. | `internal/meetings/engine.go`; `internal/storage/durable/meetings.go`; `internal/client/meeting_controller.go`; native `MeetingAudio` | Fixed production cumulative caps with authoritative durable identity lookup and bounded engine hot state. A 33,026-record encrypted fixture crosses the old totals, reopens and pages all records. Native 40-hour/representation/overflow and full ABI checks pass. Review caught and resolved two lookup/control/retry races; focused races and combined review pass. Individual event protocol bounds remain. |
| P2 | Reopening a Ready voice agenda replays/scans every historical note before recording can start, and a 257-page fallback or the five-minute operation timeout blocks large agendas. | `internal/client/meeting_controller.go`, `restoreReadyAgenda` | Fixed through authenticated bounded recent-tail reads; a million-position cursor regression verifies one tail call and no full-history scan. Sparse deletion holes preserve append cursors and disclose earlier saved text. Full history remains separately paged. |
| P2 | A question comparing separately logged agent/code identifiers requires all labels in one source, so two-agent and ten-agent questions return no evidence despite stored records. | `internal/client/ask_retrieval.go`, technical-subject retrieval; assistant coverage metadata | Fixed with per-subject strict retrieval and subject-weighted passage packing. Encrypted/reopened regressions cover separate records, distant excerpts in one original, nine-plus-one mixed allocation, missing/deleted subjects and real context omissions. Citation coverage is revalidated after clipping; dated/pronoun follow-ups retain the subject set while Search and relationship questions retain conjunctions. Focused tests and independent interaction review pass; final integration checks remain below. |
| P2 | Newer component-word/prefix lookalikes exhaust an Ask candidate pool or consume the source-byte budget before an older exact technical label is validated. | `internal/client/technical_subjects.go`, `technical_subject_candidates.go` | Fixed: validate complete labels inside each physical app/date page before merging accepted candidates or charging returned source bytes. Real encrypted/reopened Ask tests cover 70 newer 32KiB decoys, 34 newer 64KiB decoys and cross-app/window displacement. Focused normal and race checks plus independent review pass; physical request bounds remain. |
| P2 | Exact indexing tokenizes fixed-size capture parts independently, permanently losing a word spanning a boundary while reporting no indexing work pending. | Capture builder partitioning; `internal/storage/durable/capture_index.go` | Fixed through resumable Unicode token digests and authenticated original-range evidence, with old-index rebuild and original ciphertext preservation. Exact word/technical delimiter, Ask/Search/MCP, reopen and deletion tests pass. Generated differential checks caught and resolved normalization/query and part-provenance errors. Final review exposed multiple-token coarse Unicode spans; bounded, independently resumable projections fix that case without merging a small term into an oversized word. Core-token postings prevent artificial fragments or a neighboring large word from blocking short-term recall. Queue/core-state restart, reset and erasure controls pass. |
| P1 | Coordinated archive deletion still uses the removed 33,024-record session limit, so larger accepted archives cannot reach erasure. | `internal/deletion/meetings.go`, `prepareMeetingSources` | Fixed by following bounded pages to their advancing cursor end or cancellation. Service-level page-adapter regression fails after preparing 33,000 of 33,025 accepted rows before the fix. Full deletion suite and focused race checks pass; actual large encrypted record persistence/erasure remains covered separately by durable tests. |
| P1 | Sync exports only legacy observations; published multipart text captures never enter its version-1 observation transport. | `internal/storage/durable/replica.go`, `ExportObservations`; `internal/replica/segment.go` | Confirmed source/transport boundary. Unfixed; requires capture publication/provenance transport and old-peer compatibility design. The current sync integrity repair does not claim to synchronize multipart capture text. |
| P2 | Passive capture discards every source below 32 non-whitespace characters, including short terminal commands and agent status lines. | `internal/capture/pipeline.go`, `MeetsMinimumContent`; core/ingest/browser admission | Fixed with uniform non-empty sanitized-text admission. Real encrypted Terminal persistence, continuation, deduplication, reopen and exact recall regressions pass, including single-code-point Unicode. Signed multi-batch browser documents preserve short originals and reject blank text. Full capture/ingest/core/browser suites, focused race checks and independent privacy/cancellation review pass. |

## Compaction correction

The initially proposed single rolling 512-byte summary was rejected during implementation and user review. It couples lifetime memory to one request's budget and repeatedly forgets earlier details. The replacement retains independent source-span summaries, pages them without a lifetime count cap, and resumes from an atomic encrypted cursor. Request input and output are admitted against model and power budgets; chunks shrink without skipping content. Originals remain authoritative. Source erasure drains summary blocks through the existing durable deletion journal.

Calendar recaps still have bounded model context and may select sources. That is a query/recap coverage limit, not evidence that source processing has visited every capture or that omitted activity did not happen.

The source worker rotates pending captures and reserves work for both multipart and legacy history, preventing a continuous live feed from starving old sources. A real on-device synthetic test caught a model dropping an exact agent name; app-owned source-identifier context now preserves names such as `review_large_context` outside the generated prose. Native queued processing then covered a 19,940-byte ten-agent log in five calls (largest input 4,096 bytes, 9.483 seconds), retaining all ten exact agent names. The actual provider also exposed omitted disabled extraction metadata, now accepted only in summary-only mode. Independent review exposed a smallest-batch Unicode overlap loop, fixed with a strictly advancing cursor and durable mixed-width regression. These establish the tested paths, not perfect summarization or million-record throughput.

A deterministic-provider integration test additionally processes 1,048,651 accepted bytes through 33 published capture parts and 289 persisted summary blocks, interrupting and reopening the encrypted store. It verifies every non-whitespace source byte was visited, every part has its exact completed cursor, and every generated span has a paged stored block. Its 8.07-second run measures the tested queue/storage path with a stub provider, not native model throughput or generated-summary accuracy.

The previous processing pass introduced schema 8 for its new private namespaces. The upgrade changes authenticated metadata atomically while preserving existing schema-7 payload ciphertext; old readers reject the store before their older deletion logic can orphan new derivatives. Migration preservation/rollback/authenticated-rejection tests pass. No production database has been migrated by these checks.

## Previous processing-pass verification and activation

The previous processing-pass whole-repository suite passed all 56 tested packages
(client 305.160s); vet and focused text race checks passed. Independent review
replayed five interaction regressions covering shared-source passages, mixed
subject allocation, scoped follow-ups and relationship conjunctions. The seven
dynamic text seeds also passed on this final source. Their separate 30-second
mutation campaign reported 13 executions and zero new interesting inputs before
the final interaction repairs; it is limited evidence, not broad or unique-case
coverage.

`make macos-app-stable` rebuilt the canonical `~/Downloads/Blur.app` with bundle
ID `com.hazcod.blur`. Strict verification, exact leaf-certificate and designated
requirement comparisons passed. The bundle retains arm64/x86_64, Hardened Runtime
and the audio-input entitlement, without App Sandbox. It was not launched or
restarted; activation occurs on a later launch. The unfixed rows and remaining
capacity constraints in this report remain open.

## Text data integrity verification and activation

The follow-through plan is `docs/superpowers/plans/2026-11-22-text-data-integrity.md`.
New export receipts and cross-part search-range metadata require schema-9 reader
admission so older deletion code cannot orphan them. Atomic schema-7/8 upgrade,
rollback, retained-history/ciphertext and old-reader rejection tests pass. No
production store was migrated.

The first whole-repository integration run passed 54 tested packages and skipped
seven without tests, but exited 1 for four obsolete test expectations in client
and storage. One injected the former all-reminders removal method; three pinned
the former schema floor. Test-only corrections now exercise targeted removal and
authenticated schema preservation. Complete affected-package reruns passed:
client 491.041 seconds and durable storage 515.998 seconds. The initial failing
log is retained rather than reported as a successful whole-repository command.

The later short-text admission change passed all four directly affected package
suites, focused races and independent privacy/cancellation controls. An additional
complete client integration run against that final change passed in 308.375
seconds. The resulting validation matrix has passing results for all 56 tested
packages, plus seven without tests; it combines the retained initial run with
complete affected-package reruns rather than claiming the first command passed.
Current whole-repository vet and diff checks pass. Unchanged tokenizer code and
oracles passed a 30-second mutation campaign with 12,459 reported executions and
24 new coverage-interesting inputs; these are execution/coverage counts, not
distinct user use cases. Current focused races also cover native Ask/Search/MCP
range citations and erasure, original-range queue/migration, schema admission,
paged deletion preparation, export receipts and sync recovery.

The final canonical `~/Downloads/Blur.app` was rebuilt with `make macos-app-stable`.
Strict verification and exact certificate/designated-requirement comparisons
pass. It retains `com.hazcod.blur`, both Mac architectures, Hardened Runtime and
the audio-input entitlement, without App Sandbox. Its product/test source
fingerprint is `1e09c299e51f98796482b43ddeb4380871c31f39bce8a9810d3809ee1069af51`.
No application launch or restart occurred; activation and production schema
admission await a later launch. The machine-readable evidence is
`.Codex/large-data-verification/text-integrity-report.json`; raw logs remain
beside it, with short-text-specific results retained in the agent tool transcript.

The new preparation path also pages task and range metadata while retaining the
exclusive deletion gate and native scheduling guard. A real 16,777,224-byte,
513-part accepted capture reproduced a one-transaction preparation failure;
the repair completes actual deletion without raising transaction budgets.
Cancellation or native cleanup failure before journal admission preserves the
source. Focused race checks include indexed ranges and full per-source task
snapshots.

The generated tokenizer checks compare query-side normalization with streamed
digests under varying Unicode text, physical partitions, work quanta and JSON
checkpoints. They also validate original coordinates, part membership and term
support within each original slice. These are runtime-generated correctness
checks, not a count of distinct user use cases or million-capture throughput.

## Limit inventory and review policy

| Limit family | Required interpretation | Current status |
|---|---|---|
| Capture part size / AX range work | Page a document; preserve coverage and continue from a stable revision. Never silently clip a whole document. | AX range continuation and acknowledged Safari continuation retain complete text or explicit partial provenance. |
| Model context, token/duration/power budgets | Bound a call, checkpoint completed work, resume later. Too-small policies must be visibly deferred. | Compaction adapts spans; semantic checkpoints persist completed batches. |
| Page rows / response bytes / UI rows | Return a continuation or truthful coverage metadata. | Most storage paths page; literal Ask omitted HasMore until this fix. |
| Queue capacity | Backpressure or recover durable pending work; never evict accepted information. | Activity durable outbox and no-event continuation are verified; source-enrichment IDs have durable directory repair and fair scheduling. |
| Facts / entities / derived-record size | Split storage/work or report a correctable capacity gap; never claim complete extraction after dropping details. | Source compaction must progress independently of Library capacity and retain span summaries. Library capacity remains a disclosed gap needing further design. |
| Summary byte/output size | Bound one generated block, not the amount of a source's memory retained. | Rolling 512-byte design removed; independent blocks retained. |
| Time partitions | Partition a query and merge results; a supported year must not hit a 31-day storage API unchanged. | Lazy search and semantic resolver paging remove the search horizon; Time/report UI has separate bounded report behavior. |
| Protocol text/identifier bounds | Validate explicit input and report rejection; preserve accepted data. | Retained as input/protocol guards, not justification for content loss. |
| Privacy / deletion journals | Bounded steps must drain every derivative, including in-flight and partial work. | New compaction blocks and partial semantic vectors require the same erasure hooks and restart tests. |

Remaining scale constraints are explicit: dense timeline browsing can exhaust a per-read transaction budget and return Incomplete; Library fact/entity capacity can limit structured extraction even though raw text and source-span compactions continue; the vector accelerator is resident in RAM and has not been benchmarked at a million captures. These are not evidence of silent content loss, but they remain capacity/performance work rather than a claim of unlimited data processing.

## Reproduction commands

These overlays were external to the checkout, preserving the review's read-only baseline. Their tests demonstrate the original failures; tests asserting bad outcomes are evidence, not production regression assertions.

```sh
go test -overlay=/var/folders/mw/377y9_q54p7fph_9s450m6yh0000gn/T/blur-audit-capture-7hfjscw7/overlay.json ./internal/semantic ./internal/ingest -run TestAuditRepro -count=1 -v
go test -overlay=/tmp/blur-recall-audit/overlay.json ./internal/client -run '^TestAudit(DateScopedLibrary|DirectIdentityCorrection|LiteralLookupCoverage|LongLookup|DirtyRollupHidesFreshHour|ManualLibraryLongFact)$' -count=1 -v
go test -overlay=/var/folders/mw/377y9_q54p7fph_9s450m6yh0000gn/T/blur-audit-notes-pyjecgqd/overlay.json ./internal/client -run '^TestAuditEmptyRetentionSweepPreservesUnsavedNoteDraft$' -count=1
go test -overlay=/var/folders/mw/377y9_q54p7fph_9s450m6yh0000gn/T/blur-audit-reminders-o_ugivn9/overlay.json ./internal/reminders -run '^TestAuditVoiceDeletionDropsUnrelatedReminder$' -count=1
go test -overlay=/tmp/blur-audit-sync/overlay.json ./internal/replica -run '^TestAudit' -count=1
```
