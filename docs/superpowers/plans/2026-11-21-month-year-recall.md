# Month and year recall implementation plan

> **For agentic workers:** Use superpowers:executing-plans to implement this
> plan task by task. The parent owns implementation; independent review is
> read-only.

**Goal:** Answer month/year activity questions from source-linked stored
summaries and bounded fallback reads, with historical backfill, visible pending
work and deletion-safe regeneration.

**Architecture:** Extend the existing hour → day → week hierarchy with day →
month → year. Calendar months/years use local midnight and half-open UTC bounds;
ISO weeks remain a separate branch. Keep encrypted schema-7 storage and existing
IDs. Add an encrypted dirty-period index and restart-safe bounded backfill over
existing hour/day indexes, so old work is not stranded behind recent scan
cutoffs. Reuse the existing local-only provider, generation budget, source
admission and revision fences. Long Ask windows split into bounded reads instead
of weakening the storage window limits or retaining a year's raw text.

**Tech stack:** Go, existing encrypted durable index records, native Client Ask,
chronicle worker and MCP reads. No new dependency or model requirement for
planning/retrieval.

**Spec:** Active personal-memory objective and the existing Goldfish parity
checklist's month/year gap; this plan records the implementer's design choices.

## Constraints and review focus

- Preserve schema 7, stored memory, current source IDs and signing identity.
- Generate months from daily summaries and years from months, never combine
  ISO weeks across calendar boundaries into a purported month/year.
- Keep each storage range/page and model input/output bounded. Retain at most
  the existing raw-source budget in long-window retrieval and disclose sampling.
- Persist input coverage, including legacy-unknown coverage, excerpt/source
  limits and inherited sampling. Balance month/year input across the period.
- Fence stale publication after deleting all recaps, including delete/recreate;
  admit the maximum source union within the physical transaction budget.
- Exclude hourly summaries straddling local period boundaries; use primary
  evidence for those gaps and report partial summary coverage.
- Queue historical dirty periods with bounded restart-safe backfill; new input
  arriving before or after its cursor must not be missed or permanently starved.
- Propagate input changes/deletion to all calendar ancestors, including erased
  input-only sources not selected among a summary's bounded citations. Reject
  generation based on stale revisions or erased evidence.
- Test leap years, DST/zone changes, partial current periods, week/month/year
  boundaries, historical work outside recent horizons, reopen and mutation during
  generation. Tests use independent dates/source IDs, not generated answer keys.
- Build only `make macos-app-stable` to the canonical Downloads app. No launch
  without the separate pending authorization.

## Tasks

Initial ruling (2026-11-21): ship the bounded foundation and storage/erasure
prerequisites first. Those increments are recorded below. Calendar integration
now implements all four persisted levels, including compatible historical
rediscovery, ancestor invalidation and bounded generation. Native source-card
expansion and long exact Search/time are now covered by Tasks 5a–5c below; live
app acceptance remains a separate gap.

- [x] Reproduce missing calendar scopes, unsupported persisted periods and
      long/historical recall through planner, durable worker and native Client.
- [x] Add month/year IDs, periods, child validation, bounded prompts and input
      receipt hashing that includes child identity/provenance.
- [x] Add additive encrypted dirty work/backfill and transactional ancestor
      invalidation; exercise interruption, deletion, arrival and stale writers.
- [x] Extend bounded worker ordering/generation and telemetry to all levels,
      processing historical queued work without widening per-pass inference.
- [x] Extend native Ask, source navigation, Search and MCP period reads across
      long windows; preserve literal/event-date interpretation and coverage flags.
- [x] Add runtime lifecycle scenarios with independently varied dates, sources,
      hierarchy updates and erasure, beyond a static list of period questions.
- [x] Independently review, run full checks/vet and rebuild/strictly verify the
      same signed app. Universal query/app acceptance remains a separate audit.

## Foundation verification record (earlier increment)

- Calendar period and bounded input types are implemented. The durable API
  explicitly rejects month/year records until their index, dirty work and
  deletion lifecycle are implemented; the existing day/week index remains safe.
- Native month/year recap scopes use a disclosed bounded fallback, up to 400 days.
  Empty samples remain limited; they do not claim no saved activity exists.
- Calendar grammar preserves repeated identifiers and month-named topics across
  narrowed read clauses and possessive folding. Event dates retain their rules.
- Existing day/week receipt, erasure ABA, fractional-midnight and omission
  defects have permanent reproductions. New generations cap citations at 64;
  legacy 256-source read support remains, while arbitrary legacy publication
  and large historical reverse-fanout transactions remain future work.
- Independent review's threeinitial reproductions plus the activity-clause variant
  are retained. Runtime journeys vary raw calendar recall/deletion/reopen and
  interior-sample disclosure, not the unfinished persisted calendar hierarchy.

The bounded foundation passed full tests, vet, independent review and a
current-source runtime mutation run. The canonical universal app was rebuilt
and strictly verified with its original certificate and designated requirement;
it was not launched. The remaining unchecked tasks still require their own
implementation and verification.

## Historical day/week queue increment

The historical queue/backfill and bounded worker path now exist for the
already-persisted day/week levels. Preparation scans four indexed UTC hour
dates or sixteen existing rows per transaction; queue membership, counts and
strict per-level cursors persist encrypted. New writes and deletions update
queue membership atomically. Hour changes and daily publication invalidate
weekly revisions immediately; active-zone and erasure epochs fence delayed
writers. Weekly generation waits for hourly-input discovery and dirty children and has a
reserved opportunity under continuous day generation. Successful finite work
continues promptly without changing the local-only policy or six-call budget.

Small authenticated source receipts plus presence checks replace large-body
reads during publication. Full evidence validation remains on reads, and the
optional hourly follow-up still hydrates its source. Bounded receipt caching
keeps64distinct historical capture sources within physical transaction limits.
Legacy 256-source rows discard text and drain32old supporting edges per claim
before generating a bounded disjoint replacement.

At this day/week checkpoint, month/year persistence and ancestor lifecycle
remained gated; the lifetime 512-reference ceiling was a separate storage
prerequisite. The later increment below replaces that representation. Runtime rollup lifecycle journeys verify the day/week increment
against an independent insertion/deletion ledger.

The day/week increment passed independent review, the full suite (56 tested
packages), vet and a current-source runtime lifecycle run (54 reported
executions, 11 new coverage-interesting inputs). These are separate from the
older million-execution campaign and do not count distinct user needs. The
canonical app was rebuilt and strictly verified with its original certificate,
designated requirement and unsandboxed entitlements; it was not launched.
The remaining calendar tasks above are still open. The later source-history
increment below addresses the reverse-link storage prerequisite.

## Calendar source-reference storage design

The selected design replaces the lifetime 512-reference array for new
rollup links with bounded per-source/per-period-year shards. A shard has at
most432distinct period IDs (366days,53ISOweeks,12months,1year). The four-digit
year domain permits a fixed1250-byte encrypted directory bitmap; this avoids
a growing lifetime directory or linked-neighbor writes. Ordinary unlink keeps
empty year records, while bounded erasure sweeps remove them and their bits.
Legacy512arrays are read/drained alongside the new representation without
migrating every edge during an ordinary publication. This is the implementer's
choice within the existing authorized historical queue/calendar plan.

Input erasure must keep public Text/ChildIDs/SourceIDs immediately empty.
A separate encrypted cleanup receipt retains pending reverse edges until
bounded cleanup; both original erasure and generation/deletion must account
for those receipts. Tests must prove 64-source writes/replacements, historical
recurrence beyond 512, legacy fanout, reopen, interrupted erasure and mutations
during generation within physical operation/byte budgets.


## Source-history and erasure prerequisite increment

The planned source/year representation and deferred cleanup are implemented.
Its logical 1,250-byte directory uses a sparse encoding for ordinary histories
(single-year 4 bytes) and dense encoding capped at 1,251 bytes. New references no
longer inherit the lifetime 512-array ceiling; legacy arrays remain compatible.
Input erasure preserves the immediate public-field clearing contract while
private receipts drain 32 reverse obligations per claim. Hard deletion retains a
catalogued row until final cleanup. A persisted recap-erasure intent gates
publication and visibility across interruption, with an ending epoch fence.

Independent review reproduced a liveness defect when no inference worker exists.
The model-free exact-index storage service now owns a bounded erasure-resume
capability, honoring the memory-changing gate. Idle calls read only the journal;
they do not begin backfill. Real bootstrap/reopen with AI disabled, malformed
directories, mixed legacy/new links, 530 recurring daily references and 512 empty
year leaves have focused regression coverage. Runtime lifecycle mutations now
include interrupted intent, optional source erasure, reopen/model-free recovery
and an independent hour ledger. Independent final review found no remaining material issue in this increment.
The current runtime run passed 68 reported executions with 11 new
coverage-interesting inputs, from 50 baseline entries; these are executions,
not distinct needs. The full suite passed (56 tested packages), as did vet
and the targeted concurrency race checks. The canonical app was rebuilt
and independently verified against its original certificate and designated
requirement, with both architectures and only the existing audio-input
entitlement. It was not launched.

At that checkpoint, full calendar tasks remained open: month/year persisted
rows and ancestor lifecycle were gated until the later integration below.


## Calendar integration design reference

- Historical discovery must revisit existing completed day/week backfills when
  the calendar levels are enabled. Version1/Phase2 currently means only those
  two levels were discovered. Add a compatible backfill-version transition;
  retaining Phase2 unchanged would strand old months and years. Keep each
  discovery transaction bounded and restart-safe.
- Extend persisted ID validation, shard selection/overlap, queue claims and
  reader levels together. Month shards can contain twelve months per year;
  choose a bounded year shard and preserve existing day/week shard IDs. New
  levels must remain rejected until their complete write/deletion lifecycle
  passes. New source/year leaves already have room for their full annual set.
- Every changed hour dirties day, ISO week, calendar month and calendar year.
  Day publication invalidates week/month/year; month publication invalidates
  year. Input-only erasure must stage cleanup for every overlapping ancestor,
  including fractional-midnight and old-zone rows. Extend the real legacy
  256-source input-only erasure fixture to all four levels before enabling them.
- Six calls per pass remain the total bound. Reserve opportunities for each
  higher level under continuous day changes, and retain independent bounded
  visit cursors. Months wait for daily children; years wait for monthly children,
  hourly discovery and any required new-level backfill. Use local calendar
  bounds and ended periods, not ISO-week composition or fixed24-hour arithmetic.
- Generate months from daily recaps and years from monthly recaps. Bind all
  child/source/coverage metadata into the input receipt, balance excerpts within
  the existing input budget, and propagate source/coverage limits. Recheck
  revision, erasure epoch, active zone and source admission at publication.
- Extend native Ask selection, derived-source reopening, Search summary rows
  and MCP level/period reads together. Preserve literal/event-date routing,
  final evidence budgets and sampled/legacy coverage. Keep the distributed raw
  fallback for missing or filtered summaries and disclose its limits. Long
  exact time totals remain separate from adding summary periods.
- Extend the runtime action ledger to independent month/year child sets,
  boundary crossings, period generations, deletion, late publication and reopen.
  A completed-backfill fixture from the old format, four-ancestor erasure and
  actual native long-scope reads are required before marking calendar tasks done.


## Calendar hierarchy integration

Persisted day/week/month/year records now use the same encrypted catalog, dirty
queue, revision/epoch fences and deferred erasure lifecycle. Backfill version 2
revisits completed version-1 history, discovering calendar ancestors from both
hours and existing rows. Discovery remains bounded to four UTC dates or sixteen
rows per transaction. Changed hours invalidate every overlapping local day, ISO
week, month and year atomically; fractional-offset New Year hours may touch eight
ancestors. Day and month publication propagate invalidation to their ancestors.

Months are generated from days and years from months, using local calendar
bounds and ended periods. Six model calls remain the total per-pass limit, with
reserved opportunities for week/month/year under continuous daily changes.
Calendar parents wait for complete compatible backfill and clean children.
Source IDs and coverage limits propagate through every level.

Native Ask prefers stored year/month summaries across long scopes, retaining its
distributed raw fallback and evidence budgets. Search indexes all four levels;
MCP period reads preserve the child hierarchy. Derived follow-up reads reopen
month/year evidence and stop returning it after erasure. Native labels retain the
period and disclose supporting-source counts and limited coverage. Search and
MCP summary rows preserve limited coverage too. Original-source expansion in
native cards and long exact Search/time reports remain open within Task 5.

Review reproduced both an eight-ancestor transaction overflow and MCP search
rejecting per-summary coverage metadata. A stronger revisit fixture exposed
duplicate reads of existing reverse-link roots/tails before ancestor writes.
Hourly publication now uses a bounded mutation-aware cache of small encrypted
records during its closed projection transaction; Put/Delete invalidate their
keys and the original writer is restored before manifest flush. The normal
encryption and integrity-manifest write path remains, with unchanged physical
limits. Known-absence insertion bypasses were removed. Permanent tests cover
64 distinct historical capture sources, linked revisits, the fractional New Year,
legacy replacements and encrypted manifest/reopen after repeated mutations.

Independent follow-up review, focused boundary/transport/native widget checks,
vet and targeted race checks pass. The final-source runtime lifecycle run passed
78 reported executions with four new coverage-interesting inputs from 68 baseline
entries. The canonical app was rebuilt and strictly verified against its original
certificate and requirement. Full package verification passed: 56 tested packages plus six without tests.
Two older assertions were updated to the four-level API while retaining malformed
period rejection and the shared 64-row search budget. The final complete-suite
command passed using the successful current Client and unchanged storage package
results; no application source changed after independent review. No native app lifecycle action is authorized by a build.


### Remaining native source navigation

Ask currently expands only the derived text already in its answer; it does not
open the recap's supporting originals. Existing hour-feed source pagination is
bounded and memory-epoch gated, but accepts only an hour ID. A later Task-5
increment should resolve the current derived evidence handle again, paginate its
live supporting IDs and hydrate at most the existing eight-row source-page
budget. Never trust cached answer text or SourceIDs as current authority. Bind
pagination to the current recap/provenance receipt so regeneration or erasure
cannot continue an obsolete source list. Use the existing memory-generation and
epoch checks for asynchronous publication. Native cards should disclose the
bounded citation sample and offer source expansion without navigating away from
the answer. Verify actual mounted button actions, deletion while loading and
month/year parents; this is separate from the completed derived follow-up API.


### Task 5a: Native recap originals

The parent continues inline within the existing authorized Task 5. Keep the
existing native cards and typography. One recap expands inline at a time;
closing it cancels loading and releases source payloads. No modal or new route.

**Files:** new `internal/client/recap_sources.go` (live reads),
`recap_sources_view.go` (view state and controls), their focused tests; small
integration changes in `desktop.go`, `recap_retrieval.go` and
`rollup_retrieval.go`.

**Interfaces:** `RecapSourcesPage(ctx, anchor assistant.Evidence, after string,
limit int) (RecapSourcePage, error)` returns Hits, Next, Receipt, Total and
CoverageLimited. `RecapSource(ctx, anchor, receipt, sourceID)` returns one
currently supporting original. Page limit 1..8, previews at most 4096 UTF-8
bytes; full-source state retains one original separately from previews.

- [x] Write native mounted-action and encrypted-store regressions. Prove source
  IDs are hydrated from the current recap, not the answer's cached source list;
  cover hour/day/week/month/year and local-offset period boundaries. Expected
  RED: missing native action and missing source-read API.
- [x] Resolve the period from the anchor's layer/time, authenticate its existing
  derived handle against current stored text/bounds, and hash the complete
  current recap metadata into a receipt. Bind page cursors and full reads to
  that receipt; verify it again after hydration. Reject erased/replaced recaps,
  forged membership, changed provenance and memory epochs. Expected GREEN:
  exact live original IDs, bounded reads, no stale continuation.
- [x] Add inline Show saved sources, loading/error/empty/limited states, bounded
  Load more sources and current full-source expansion. Guard asynchronous
  callbacks by request, answer, view generation and both Client/durable committed-write epochs; UI publication never waits or reads storage. Clear/cancel
  on collapse, new answer or memory changes. Test real mounted buttons and
  queued callbacks after deletion, regeneration and view dismissal.
- [x] Extend runtime calendar journeys with original-source navigation and stale
  cursor/full-read attempts under changing inputs; independent insertion IDs
  remain the oracle. Run focused tests, detector and targeted race checks.
- [x] One fresh read-only review, required material fixes RED→GREEN, complete
  suite/vet and final-source runtime run. Build canonical signed app with
  `make macos-app-stable`, independently verify exact certificate/requirement,
  and recheck the final source fingerprint. No app lifecycle action.

Review focus: same-text/new-provenance regeneration, deleted input not selected
among displayed citations, source erasure between page hydration and queued UI
publication, malformed/replayed cursors across recaps, full original loaded after
its recap changed, and bounded retained text after many expansion actions.


Task 5a verified on final source `25d13d7da81cb3cf6fd2118a4782da8cc9bfa7b549caaf057e7b16ce04e26ccb`:
complete suite PASS (56 tested packages), vet and targeted race checks PASS,
including the retained regeneration input under race. Final navigation mutation
run PASS25 executions/3 new interesting/19 baseline. Canonical
`make macos-app-stable` and independent strict signature verification PASS;
exact original leaf certificate/requirement, universal architectures and only
audio-input entitlement preserved. No app lifecycle action. Task 5's long exact
Search/time reports and broader live acceptance remain open.


### Task 5b: Long-scope Search

Continue inline under the already authorized Task 5. A year-wide invoice search
must include both ends of the requested capture period, preserve app/domain and
literal qualifiers, deduplicate revisits, and reject interrupted/erased reads.
Use the existing Go/native Search/MCP pipeline and schema 7. No raw storage range,
physical node/row/byte budget, app lifecycle or signing policy changes.

**Design:** Build at most fifteen contiguous <=28-day windows for an explicit
scope up to 400 days. Read complete UTC activity partitions for each small window
(including the prior day for overlapping segments), bounded by the existing
4,096-row activity read limit. Retain source IDs/visit instants, not segment text.
Each exact-index query keeps its existing row/node/byte bound; merge its proven
score/ID prefix into one bounded global prefix before the existing relevance
ranking. Deduplicate a source and preserve its latest matching visit. Sum measured
work; preserve pending/more/coverage flags and return no page on an unproven
mandatory query or cancellation. Unqualified scoped browsing merges bounded
source-window pages newest-first without retaining all originals. Search planning
uses the resolved zone, as native Ask already does.

**Files:** new `internal/client/search_scope.go`,
`internal/client/long_search_test.go`; modify `natural_search.go`,
`memory_search.go`, the response publication fence in `client.go`, optional
`selected_model_search.go` and `internal/semantic/service.go`, and runtime
`recap_sources_fuzz_test.go`. Exact durable
storage primitives remain unchanged.

**Interfaces:** `searchScopeWindow` holds Start/End, eligible activity source
IDs and visit instants. `searchScopeWindows(ctx, plan, bounded, segments)` builds
these; `searchExactWindows(ctx, query, windows)` returns one `search.ExactPage`
with at most query.TopK hits. Both are private Client helpers. Existing public
SearchMemoryPageContext and MCP search_memory contracts remain unchanged.

- [x] Write encrypted native/MCP regressions: year-end/beginning originals,
  higher-score old match, excluded app/year, activity-only legacy revisit,
  duplicate visits across windows, bare browsing, month boundary, literal year
  topic, cached process-zone change, cancellation and deletion during reads.
  Run focused tests; Expected RED: ErrActivityLimit for valid long scopes.
- [x] Implement scope construction, global prefix merge and browsing; retain
  each storage limit and final hydration budget. Expected GREEN: exact inserted
  source IDs and matching visit instants, no duplicates/out-of-scope rows.
- [x] Add long Search to changing encrypted-memory runtime histories with the
  insertion ledger as oracle. Run focused tests, current mutation target and
  targeted race checks; Expected PASS without counting executions as unique needs.
- [x] One fresh read-only final review; fix material findings with RED→GREEN.
  Run complete suite/vet, rebuild canonical app and independently verify exact
  original certificate/requirement and final source fingerprint. Expected PASS.

Review focus: a source first captured outside the scope but linked only through
old activity metadata; boundary-crossing segments/revisits; global score ties
and duplicates across windows; cancellation or erasure after an early child
query; literal years and event dates that must not turn into capture filters.
Review additions: guard split reads with the durable committed-write token;
route selected-model recent/rewrite candidates and semantic sighting resolution
through bounded windows. Preserve limited flags and clip activity visit instants
only at the actual scope start. Returned source bytes share 2 MiB across
filters/windows; stop before the storage primitive minimum allowance. No claim
that this counter measures all underlying decryption work.

Task 5c remains exact long-range time totals, including Ask/report/MCP; do not
claim the full Task 5 or the personal-memory objective complete after 5b.


Task5b verified on source `518cdbaf7fe41d2cd8df198f329ce9d84d5a02f974137592b43249facabea7ca`:
complete suite PASS (56 tested packages, 6 without tests; client 442.309s), vet PASS,
long-Search/semantic race PASS and retained source-navigation case PASS.
Final runtime PASS: 28 executions, 1 new interesting input, 22 baseline entries. Canonical stable
build and strict original certificate/requirement/universal/audio-only check
PASS. No app lifecycle action. Full personal-memory objective stays active.

### Task 5c investigation notes (before implementation)

`AnswerDirect` rejects time scopes above31days. `ActivityReport` and desktop
range validation also cap31days; MCP `get_time_report` has its own31day custom
range guard. Keep the existing4096-row bounded activity read per child. All
three need a shared complete range scan rather than a larger single read or a
sampled total. The current activity engine emits segments of at most24hours;
read the preceding UTC partition for overlap. `activity.Report` already conserves
nanoseconds across local calendar/DST buckets. Aggregate each clipped child in
nanoseconds, round only for final presentation, and count original segment IDs
once for session counts across artificial boundaries. Preserve self exclusion
and app/domain intersections. Bound retained group/session metadata and reject
an exceeded admission budget; never publish earlier-window totals as complete.
Fence the whole read against durable commits and cancellation. Required REDs:
Jan/Dec inclusion, actual global and internal boundary crossings, local offsets
and DST, same source with distinct sessions, app/domain/self filtering, late
budget/cancellation/store mutation, native Ask/report/MCP equivalence, and
runtime histories checking independently inserted durations after changes.
No Task5c production edit, acceptance claim or plan checkbox is implied here.


### Task 5c: Complete long-range tracked time

Continue the authorized native Task5 inline on root main. User outcome: a whole
year's tracked foreground time includes both ends and every mandatory window,
without inventing sessions or billing untracked time. Existing schema7 and all
storage physical/date limits remain. This is an implementer design choice under
the existing goal and plan, not a new visual direction or permission request.

**Design:** Walk at most15 contiguous28day windows over up to400 local dates;
read each complete UTC partition list plus its preceding day, at most4096rows
per call. Validate, exclude Blur and unknown foreground gaps, intersect app/domain filters, clip each piece
to its child, and aggregate metadata immediately in nanoseconds. Count distinct
original segment IDs for Ask sessions. Keep at most65536 session IDs and65536 aggregate metadata entries separately,
rejecting admission before inserting excess metadata. No source body reads. Questions carrying an unclassified topic/project must
receive an explicit unsupported-scope answer, never the entire period total.
Use a shared, strictly advancing offset-transition-aware local-calendar splitter per child; combine
buckets/days without rounding. MCP top groups retain at most8source IDs each;
presentation remains bounded by existing response/row limits. Capture Client
and durable commit epochs before the scan and reject the whole result on changes,
mandatory failure or cancellation. Queued native report publication also checks
the durable token, requested dates/zone and current Time page. Direct time Ask
carries its read guard through the assistant service to queued native installation;
question history conditionally advances that exact epoch in its own transaction
and cannot adopt an intervening evidence write. Skipped/repeated midnight uses
the first real instant of the requested local date in report/MCP ranges. Native app rows page20at a time with the exact total
unchanged; navigation reaches every app without accumulating mounted rows.

**Files/interfaces:** create `internal/client/activity_scope.go` for
`walkActivityScope(ctx,start,end,plan,consume)` returning a publication token and
session count; `activityReportContext` returns accumulated report plus token.
Modify `AnswerDirect`/`directTime` in ask_retrieval.go, `ActivityReport` in
client.go, report publication in desktop.go, MCP reducer in mcp_time_report.go,
and input/output range descriptions/validation in internal/mcpserver/parity.go.
Keep public ActivityReport compatibility; add a context-aware path for lifecycle
admission. Scope max400dates, raw reads unchanged<=33UTCdatepartitions.

- [x] Add actual encrypted-store `long_time_test.go`: Jan/Dec and internal
  boundary segments, requested-boundary clipping, identical source/distinct
  sessions, self/app/domain intersection, local New Year and DST, cancellation,
  late budget failure and actual committed mutations. Native Ask, report and
  MCP must match literal independently derived durations. Run focused RED.
- [x] Implement the shared bounded scanner, accumulation and all three routes;
  update obsolete31day rejection expectations to400date acceptance/401rejection.
  Add MCP transport and native range/publication tests. Run focused GREEN.
- [x] Add runtime histories with independently inserted activity duration/state
  ledger, modifying/replacing/erasing/reopening actual encrypted storage; check
  native Ask/report/MCP totals after transitions. Run focused/race and detector.
- [x] One fresh required read-only final review; material findings RED→GREEN,
  then complete suite/vet/current-source mutation run, canonical stable app build
  and strict original signing/source fingerprint checks. No app lifecycle action.

Review focus: prior UTC partition at fractional midnight; split-session count
and subsecond rounding; qualifier intersection; memory writes during or after
read but before queued native publication; overlong/malformed/empty/dense ranges;
all retained group/source metadata and serialized response budgets. Long-range
listing/search semantics and arbitrary live model/app acceptance are separate.

Task5c fresh review found five Important user failures: unknown Engine gaps were
counted; queued native Ask lost the durable token; skipped-midnight DST could
hang; during/using grammar was treated as a topic; total-screen-time wording
missed the time route. All five have retained failing-before/fixed-after tests.
The same calendar root cause also corrupted requested date endpoints; native
and MCP regressions now cover Santiago's adjacent dates, Havana's repeated
midnight and Apia's skipped date. Empty tracked scopes receive an explicit
missing-saved-time answer without guessing capture coverage. Final focused
regressions pass; complete suite/race/vet/current-source runtime and canonical
build remain final gates, tracked in the progress ledger.

The complete-suite integration pass additionally caught the empty-time response
being promoted to synthetic recorded-activity evidence in follow-ups. That path
now respects insufficient evidence. A new actual queued native follow-up RED
also exposed the direct guard being lost during answer transformation; a
whole-Ask time token now covers those transformed results through conditional
history and native installation. Runtime histories revisit earlier time answers
after mutations and enforce app qualifiers plus anchored numeric prefixes.
One incorrect Today boundary assertion was restored independently of the new
report partition rule, and the unfenced compatibility-store regression now
expects the explicit consistency refusal. The first full run is retained as a
failure; final verification uses a 30-minute package budget because this
repository's durable suite has successful recorded runs above ten minutes.
Production operation deadlines remain unchanged.


Task5c final verification on source `f3e10017aec511143f49c8ae3def0ecde94c71ad6d113e8eee8f2e9d2ff48198`:
complete suite PASS (56 tested packages, 6 without tests; client 528.658s,
durable 724.443s) with a 30-minute package budget; production operation deadlines
unchanged. Vet PASS; focused race PASS (client 30.010s, durable 2.063s).
Current-source runtime PASS: 63 reported executions, 12 new interesting inputs,
40 baseline entries, including grounded follow-ups after state changes. The
retained corpus audit has 52 decoded plans/action-slot orders across 3 zones,
22 years, 5 scope families, 6 initial app families and 8 subject families. These
are separate execution/corpus measures, not semantic-unique needs or live-app
acceptance. Canonical stable build and strict original certificate/requirement,
universal architecture and audio-only unsandboxed checks PASS. No app lifecycle
action. Task5 is verified; the wider personal-memory goal remains active, with
live acquisition, voice, arbitrary-model/native visual acceptance and verified
project classification still open.
