# Dynamic personal-memory scenarios

The current priority is basic live acceptance before larger campaigns. The
2026-11-22 million-target run was intentionally stopped at 47,762 executions
(869 new interesting inputs, exit 130) on its earlier immutable source. It is
incomplete. Executions are not distinct user needs or a million unique use
cases. No campaign expansion is part of the current capture/voice/notes/task
checks; six voice journey seeds are regression coverage only. Observed live
outcomes and remaining limits are recorded in [the parity checklist](goldfish-feature-parity.md).

The personal-memory objective requires useful behavior across changing state,
not a large catalog of phrasing variants. `FuzzPersonalMemoryJourney` builds a
temporary encrypted memory, then Go's coverage-guided fuzzer mutates actions,
parameters and ordering at runtime. Inputs that discover new execution paths
are retained by Go; failures are minimized and saved for exact replay. There is
no pre-generated million-row corpus and no model creating its own answer key.

Run a campaign (the default target is one million executions):

```sh
python3 tools/memory-journeys/run.py --report .Codex/journey-report.json
```

For a short iteration, add `--executions 100`. Coverage/failure minimization
has a five-second limit per attempt (override with `--minimize-seconds`). The
campaign itself uses its execution target, with no package wall-time cutoff.
A failed campaign stops for
investigation; its target is never recorded as its achieved count. The report
contains the actual count reported by Go, newly interesting inputs, exit code
and elapsed time. One million executions are not one million distinct user
needs, semantic correctness proofs, or verified macOS applications. Execution
counts, coverage discoveries, independent oracles and real-app acceptance are
separate evidence.

## What changes during a journey

- Sources arrive from messaging, email, browser, editor, terminal, document,
  voice-transcript and unknown-app identities; Blur's own interface is excluded.
- The resolved zone varies between UTC and Kathmandu; capture-day boundaries
  are checked against independent fixed offsets, including both start and end.
- Source age spans the previous month; text may be short or at a long page's
  tail. Work and personal subject matter includes release gates, security
  retests, research, invoices, travel, family arrangements and maintenance.
- Appointment confirmations vary future event dates (1–90 days), capture dates
  (1–30 days earlier), clock times and Mail/Outlook/browser origin independently.
  Event-time questions must recover the confirmation while preserving capture
  provenance and the source's missing-end wording. Dedicated native regressions
  also cover future visits, nominal events, mixed event/capture dates and date
  continuations. These are retrieval/extractive checks, not calendar integration
  or proofs of arbitrary model-generated date interpretation.
- Dutch appointment journeys independently vary future event dates, older
  observed weekdays, clocks and mail/browser origin. A Dutch event question and
  a Dutch past-read question must recover the same independently stored source
  while applying different date rules. Numbered subjects also vary Dutch
  route/factuur/project/zaak families against newer date-token collisions.
  Task-collection reads alternate English and Dutch while checking the same
  independent task-state and notification oracle. The input action decoder and
  saved minimized regressions retain their original meanings.
- Numbered-topic journeys independently vary route/invoice/project/case numbers,
  event dates, source dates and app origin. A newer conflicting subject shares
  the date/family cues and states a different time. Ask and either Search or MCP
  must rank the actual numbered topic first and flag the conflict as a closest
  match. Each source keeps a separate reference token so later recall/deletion
  assertions remain independent of this deliberately shared query context.
- Questions vary between recall, decisions, stored records and direct finding.
  The oracle checks retrieval/citation, not arbitrary answer quality.
- Identical content is re-sighted at new event times. A repeated event at the
  same instant is idempotent, separately covered by sighting regressions.
- Notes with TODOs and quoted reminders remain saved evidence, without creating
  active tasks. Explicit task/reminder requests go through Entry and the real
  task/reminder services; returned state, due instant and notification payload
  are checked against the requested action.
- Tasks are completed, then read through natural-language Entry and the full
  task collection. The compact answer's eight-item limit and remaining count
  are part of the oracle.
- Sources are deleted before new questions or conversational follow-ups.
  Deleted/self-only targets must receive no citations; live targets must be
  cited. Remaining unrelated sources cannot satisfy that assertion.
- The store is closed and reopened mid-journey. Source and task state must
  survive; the simulated OS notification queue survives app reopening too.
- Source-backed Library proposals are indexed and found through unaccented
  names. Dedicated regressions additionally cover legacy migration, scoped
  corrections, context collisions, read-budget boundaries and deletion.

Each journey performs at most sixteen mutations. This keeps failure replay
bounded while exploring different histories rather than a fixed list of final
questions. The compact seed journeys exercise each action; they are starting
points for mutation, not a claim of exhaustive scenario diversity.

## Relevance and remaining coverage

The existing realistic recall/search corpus remains a separate regression set.
A read-only Goldfish context inspection on 2026-11-21 also identified workflows
that need broader acceptance: appointments found in mail, schedules observed in
browser/Outlook, missing end times, and observation dates differing from event
dates. Event/observation distinctions and missing-end wording now have runtime
journey coverage and dedicated native regressions. Dutch requests still need
broader acceptance alongside English. No personal names, event content or identifiers
from that inspection are copied into fixtures.

Recognized Dutch personal recall/search forms now use the local planner and
the same Interact read route, including without a question mark. Original
topics, literal Search and questions remain in their original language. Event
dates stay content cues; a relative read clause supplies an independent capture
window. App/date and bare-weekday continuations retain the event topic. Generic
Dutch task-collection questions reuse the local status/calendar/count filters
without granting mutation authority. Explicit note/task prefixes retain their
save behavior. Native regressions cover actual encrypted source IDs, original
capture times, missing-end wording, deletion with previous answer prose and
the Entry boundary. Domain weekday words, explicit web/procedural requests and
activity adjectives have separate routing regressions.

This is bounded request grammar, not arbitrary Dutch understanding, source
translation, Dutch task/reminder creation or general multilingual acceptance.
The long-running million-target campaign records its immutable source hash;
later language changes have separate current-source regression/mutation runs.
An older campaign's counts are never presented as validation of newer code.

A synthetic mail-based probe found a specific date gap: a Monday appointment
question applied a past Monday capture-time window and excluded its Thursday
confirmation. Clear time-question grammar now separates event content dates
from observation windows. A subordinate read/capture clause can independently
name an observation date. Date-only continuations replace the prior event date
without becoming capture recaps. Future activities and nominal events such as
a book reading have regressions. Follow-up tests also preserve numeric/month
topic names that occur inside a date, replace relative/ordinal/numeric date
cues, and retain the event topic across app refinements and answer shortening.
The inherited topic is separate from the replaceable date cues.
This does not implement arbitrary event-date
extraction, calendar synchronization or full multilingual understanding.

An independently reproduced relevance defect matched `23` from a source's date
when the question meant Route 23. Numbered topic pairs now retain local phrase
support through date, app and grounded follow-ups. Ask, native Search and MCP
rank that support before broad token/app/recency boosts; unverified pairs are
closest matches even when all individual words occur. Bare/entity Ask and
unquoted topics with quoted Search qualifiers use the same cues. Source/title
reads stay bounded and report incomplete ranking coverage. Native regressions
also cover accents, reversed word order, multiple pairs and deletion of the
real subject, leaving only a clearly labelled closest match.

This improves local lexical disambiguation; it does not establish arbitrary
semantic ID extraction. Unsupported paraphrases remain available as closest
matches. Clock, date, range and version components are excluded from creating
numbered-topic cues. Literal-only Search keeps its existing term contract.

This runner enters after acquisition: it does not prove Accessibility coverage
of real apps, OCR, virtualized/offscreen content, microphone transcription, live
UI responsiveness, actual OS notification delivery, model understanding,
calendar synchronization, or unrestricted language/query support. Those remain
separate requirements in the active goal. The notification port is synthetic;
all persistence, retrieval, Entry and native Client Ask behavior is real.

Acquisition has a separate runtime mutation target on macOS:

```sh
go test ./internal/capture -run '^$' -fuzz '^FuzzNativeRangeDocumentStability$' -fuzztime=30s -parallel=2
```

It varies Unicode content, range size, per-round byte budgets and the point at
which a provider edits text, changes its reported extent, becomes secure or
stops supplying ranges. The real native CF conversion and resumable document
cursor must preserve the whole source on a stable read, reject changed/private
reads and never complete missing, repeated or mixed text. A same-length edit
regression first exposed a mixed document receiving a complete receipt; a
bounded second verification pass now checks repeatability before completion.
Dedicated job tests cover verification-only progress, timeouts, cancellation
and visible-only partial coverage. The external AX provider is simulated, so
these execution counts remain separate from real-app acceptance and memory
journey counts.

On failure, replay the saved case with the command Go prints, for example:

```sh
go test ./internal/client -run 'FuzzPersonalMemoryJourney/<case-id>'
```

Keep the minimized case, investigate the production behavior and the oracle,
and retain meaningful failures. Do not relabel a production defect to obtain a
green counter. The initial mutation campaign exposed an oracle that incorrectly
counted the exact same sighting instant as a new visit; the journey now supplies
distinct event instants and retains that action sequence for replay.

Two later minimized cases found real routing defects after deleting the only
source: `Find <topic>` (`43348c9e8104b4ec`) and `Where is the record for <topic>?`
(`e625722f281215e5`) fell through to unavailable generation instead of returning
missing evidence. Both remain as replay cases, with the routing fixed. Quoted
Search subjects have a separate regression so request grammar does not become
an extra mandatory search term.

Legacy capture-format migration has additional lifecycle regressions with
authentic old accented tokens, dense/partly indexed proof pages, interruption,
cursor deletion and new arrivals. Every work quantum must preserve cited source
access, visit times and deletion through an existing part ID. These dedicated
regressions complement the changing journey histories; they are not added to
the mutation execution count.

Calendar recall has a separate runtime lifecycle target:

```sh
go test ./internal/client -run '^$' -fuzz '^FuzzCalendarRecallLifecycle$' -fuzztime=60s -parallel=2
```

It varies historical years (including leap years), months, app scope, observed
dates near both ends of the period, source identities and work/personal facts.
Actions replace evidence, insert an interior source, erase boundary sources,
reopen the encrypted store and revisit an actual previous answer. After every
action, native Ask must cite the independently stored live boundary IDs and
exclude erased or other-app captures. An omitted interior source requires a
coverage limit; an empty sample cannot imply an empty year. Expected IDs come
from insertion receipts, not the planner. These
counts remain separate from the long personal-memory campaign.

Month/year recap requests now have a bounded fallback over at most eight
time-distributed windows, each no longer than 31 days. A year retains at most
eight raw originals and the ordinary final evidence budget; sampling is
disclosed. Requests wider than 400 days return
a limit instead of claiming a complete result. Stored month/year recaps now precede this fallback when their whole periods
fit the scope. Calendar generation and historical queue processing are implemented.
Long Search, native original-source expansion and exact time reports now have
separate bounded implementations and regression coverage below.
All four levels share an encrypted historical queue and restart-safe backfill. New reverse
references use bounded source/year records instead of a lifetime 512-reference
array; existing arrays remain readable and are drained without a whole-store
migration.

Summary inputs now preserve source/child identity in their receipt and persist
coverage limits through day/week/month/year ancestry, Ask, Search and MCP. New generated
rollups cite at most 64 sources so admission and erasure fit bounded storage
transactions; the legacy 256-source record reader is preserved. Legacy records
without coverage metadata are reported as limited. A persisted erasure epoch
rejects old rollup publication after recap deletion/recreation. UTC hours that
cross a fractional-offset local midnight are excluded from that day's summary;
primary captures provide boundary detail. These regressions prove the stated
paths, not arbitrary historical source fanout or full calendar parity.

Historical derived-summary work has its own runtime target:

```sh
go test ./internal/storage/durable -run '^$' -fuzz '^FuzzRollupWorkLifecycle$' -fuzztime=60s -parallel=2
```

Each journey builds an encrypted store, varies old dates and four app identities,
and changes actual hourly inputs through replacement, hour/original erasure,
recap erasure, store restart and UTC/Amsterdam rebasing. High-bit erasure actions
also interrupt a committed cleanup intent, optionally delete an original during
cleanup, reopen, and finish through model-free maintenance. An all-recap clear
updates the independent hour ledger; a rollups-only clear preserves its hours.
The independent final oracle checks hours and exact child/source IDs in day,
ISO-week, month and year rows, with no obsolete-zone or erased-source content.
Boundary actions include leap day, month changes and UTC New Year across zones. The local generator is
controlled: these executions validate storage/worker transitions, not model
interpretation or app capture. Counts are separate from the other campaigns.

The durable worker rotates persisted per-level cursors, visits at most 64 rows per
level and calls the model at most 6 times per pass. It backfills 4 indexed UTC dates
or 16 existing rollups per quantum. Weeks wait for hourly-input discovery and dirty children;
higher levels each receive a reserved opportunity under continuous day changes.
Months use days and years use months; they wait for compatible historical
discovery and clean children. Version-2 backfill revisits completed version-1
day/week history so its older months/years are discovered.
Successful finite backfill/model batches continue promptly; waiting current
periods retain the ordinary cadence and errors retain retry backoff. Hour changes
and day/month publication dirty their calendar ancestors atomically. Epoch and active-zone
fences reject late publication before the bounded scan reaches a row.

Admission authenticates small source receipts and presence inside the write
transaction; actual evidence reads still verify full ciphertext/content/part
chains. Regressions cover 16 ordinary 1 MiB originals, 64 published parts, 64 distinct
historical capture editions with later visits, invalid raw receipt binding,
missing payloads, token-migration continuity and erasure. Legacy 256-source
rows drain old reverse receipts in 32-edge claims before a bounded disjoint
replacement can publish. New supporting links are grouped by each rollup ID's
calendar/ISO year, with at most 432 possible period IDs per leaf. The complete
four-digit-year directory has a fixed logical ceiling and an adaptive encrypted
encoding: a common single-year directory takes 4 bytes; a dense one at most 1,251 bytes.
An erasure quantum visits at most 16 empty year leaves separately from 8 forward
row invalidations. Ordinary rewrites retain empty leaves for bounded cost.

Regressions reference one actual source from 530 daily rollups across years,
reopen and erase it; mix old/new reverse representations; and sweep 512 empty
year leaves before erasing a live tail. Distinct 64 capture replacements with new
year records are tested against the real physical transaction budget. Arbitrary
manual 256-source publication and unrestricted calendar history remain outside
these guarantees.

Input erasure immediately clears public recap text, child IDs and source IDs.
Separate encrypted cleanup receipts retain only the pending reverse obligations,
which drain in 32-edge transactions before a replacement may publish. Hard deletion
keeps its forward catalog entry until receipt and row can disappear atomically.
A persisted erasure intent suppresses affected reads/publication during restart
recovery, and the ending epoch rejects claims made during the clear. Model-free
storage maintenance resumes an intent even with AI disabled; idle maintenance
reads only the journal and does not start summary generation or backfill.

This source-history increment passed independent review, the full suite
(56 tested packages), vet and focused race checks. Its separate current-source
runtime lifecycle run passed 68 reported executions with 11 new
coverage-interesting inputs from 50 baseline entries. These numbers are
executions, not distinct user needs. The same signed universal app was rebuilt
and verified; no native app acceptance run was performed. The older
million-target campaign continues against its own recorded immutable baseline;
it does not validate later code changes.


The calendar hierarchy increment adds actual native long-scope Ask, Search and
MCP reads against an encrypted store populated by the worker, then reopens derived
evidence and erases an original. A completed legacy backfill is reopened and
rediscovered. Hot unrelated daily work cannot starve a changed historical month
or year within the six-call budget. Input-only erasure covers all four legacy
levels. The fractional-offset New Year fixture uses 64 distinct published capture
sources with existing reverse links, requiring eight atomic ancestor updates.
Real MCP transport and mounted native widgets retain limited-coverage metadata.
Independent review, the complete suite (56 tested packages), vet and targeted
race checks passed. The final-source lifecycle run passed 78 reported executions
with four new coverage-interesting inputs from 68 baseline entries. The canonical
signed universal app was rebuilt and strictly verified, without launching it. No
count from the older million-target run is attributed to this source revision.


## Native source-navigation histories

A separate target exercises the path from a stored calendar recap to its originals:

```sh
go test ./internal/client -run '^$' -fuzz '^FuzzRecapSourceNavigation$' -fuzztime=60s -parallel=2
```

It constructs actual encrypted sources and generates the calendar hierarchy with
the worker, then varies edits, source deletion, restart, recap clearing, saved
cursors, full reads and mounted native Show saved sources actions. Histories span
1992–2024, calendar months/years, UTC/Kathmandu, four app identities, eight personal
and work topic families, and optional large multilingual sources. Expected IDs
and original text come from the independent insertion ledger. The UI queue can
be held across deletion, same-text provenance replacement, navigation or Browse;
both source pages and full originals must be discarded when their authority or
view changes. These are changing-state histories, rather than a static list of
question phrasings.

Dedicated regressions cover all five recap levels, foreign/malformed cursors,
forged cached source metadata, input-only deletion, local New Year offsets,
process-cached time zones and retained large-string backing storage. Review found
real queued-publication and dismissal defects; permanent regressions now guard
them. A durable committed-write token serializes tiny UI result installation
with mutations without waiting for a writer or doing database I/O on the UI thread.

The final reviewed-source mutation run passed 25 reported executions with three
new coverage-interesting inputs from nineteen baseline entries. It has a separate
source fingerprint and report; its count is not added to the older million-target
campaign. Coverage discoveries are not semantic uniqueness. The local summary
generator and GUI dispatch boundary are controlled, and this target begins after
acquisition; live capture, generated-answer quality and launched-app visual
acceptance remain separate requirements.

A retained mutation also caught an overly strict scenario oracle: temporarily
changing metadata could regenerate an identical row, for which the earlier
receipt remained valid. The scenario now changes an actual hourly recap input,
so ancestor revisions advance through the production invalidation path even
when the controlled generator repeats its wording. The strict stale-cursor
assertion and minimized input remain. A separate personal-memory mutation run
passed 44 executions with thirteen new coverage-interesting inputs on its
recorded revision; these runs are never aggregated as unique use cases.

The source-navigation increment passed the full suite (56 tested packages), vet,
focused race checks and independent review. The same signed universal app was
rebuilt and strictly verified with its original certificate and designated
requirement. No app lifecycle action or live native acceptance run was performed.

## Long-range finding in changing memory

The source-navigation target now also runs native Search or the actual MCP
`search_memory` handler after each state change, including an empty memory.
It varies whole-month/year capture scopes and app qualifiers. Exact live source
IDs and original text come from the insertion ledger, so an edit, deletion or
reopen changes the expected answer. This adds long-range retrieval to existing
histories without replacing their retained action decoder or failure corpus.

Dedicated encrypted-store regressions cover January and December together,
legacy originals linked only by activity, later visits, stronger old matches,
app/domain intersections, quoted years, local offsets, interrupted reads and
mid-query durable changes. Year-wide meaning tests exercise both recent
candidates and rewritten synonyms behind the configured model boundary, with
newer unrelated documents competing for the candidate budget. The semantic
service test uses a controlled embedder and actual stored sighting history.
These establish retrieval behavior, rather than live model quality.

Long Search uses at most fifteen bounded child windows. A durable committed-write
token rejects the whole split read after an intervening change, including
background additions, revisits and erasure. Internal window boundaries cannot
invent later visit times. Browsing and meaning candidates share a 2 MiB returned
source-byte allowance across windows and qualifiers and disclose incomplete
coverage when it is exhausted. That counter does not measure every underlying
decryption performed by the storage adapter. Bare browsing follows the saved
capture/sighting timeline; keyword retrieval additionally uses legacy activity
membership. Exact tracked-time reporting has its own bounded metadata scanner below.


The final long-Search source passed the complete suite (56 tested packages), vet,
focused race checks and all retained runtime source-navigation cases. The
current-source mutation run passed 28 reported executions with 1 new interesting
input from 22 baseline entries. A fresh review exposed mixed snapshots, meaning
integration, timestamp and coverage/admission defects; retained RED regressions
now pass. The canonical signed universal app was rebuilt and independently
verified against its exact original certificate and designated requirement.
No app lifecycle action was performed. Live cross-app, voice, arbitrary-model
and native visual acceptance remain open. These counts
are never added to older campaigns as unique user needs.


## Changing tracked-time histories

`FuzzLongTrackedTime` creates actual encrypted observations and activity, then
replaces originals, erases evidence, reopens storage, attempts duplicate writes
and adds sightings without inventing additional tracked duration. Month/year
scopes span UTC, Kathmandu and Brussels, with both requested boundaries, an
internal storage-window boundary and subsecond intervals. App, domain, work and
personal subject families vary independently. Some intervals come from the real
activity engine: only its configured known interval enters the independent
ledger; persisted unknown gaps remain excluded even after source erasure.

After each transition, native reports and MCP must equal the independently
inserted, clipped duration. Positive tracked scopes also exercise native Ask
with app qualifiers, during/using grammar or total-screen-time wording. An
earlier time answer is also shortened after later mutations; its current
evidence must recompute the same scope, and erased time must stay absent. App
answers require the actual app qualifier and an anchored numeric prefix, so a
matching number elsewhere in an all-app breakdown cannot satisfy the oracle. MCP
source references must still be live and in scope. Nanoseconds accumulate
before seconds are rounded. The target does not use a generated answer as its
reference and does not count query wording as a distinct user need.

Dedicated native regressions cover original session counts, app/domain
intersection, 400-date admission, 401-date rejection, mandatory read failure,
cancellation, committed additions/erasure, and queued publication on both the
Time and Ask surfaces. A question-history write can advance only its matching
activity read token atomically. It cannot refresh an answer after evidence
changes. Native app rows mount 20 at a time while the complete total stays fixed.
Midnight transitions cover Santiago, Havana and Apia, including exact daily
range endpoints, rather than assuming every civil date starts at 00:00.

Exact tracked reports read at most fifteen 28-day windows, retain at most
65,536 original session IDs and 65,536 aggregate metadata entries separately,
and hydrate no source bodies. Unknown engine gaps and Blur itself are excluded.
Unverified project/topic time requests receive an explicit unsupported-scope
answer; app/website totals cannot stand in for a requested project. Empty
scopes describe missing saved tracked time without claiming an app was never
captured. Live acquisition, real microphone/model acceptance, rule-based
project classification and native visual acceptance remain separate gaps.


The final tracked-time source passed the complete suite (56 tested packages),
vet and focused race checks. The current-source mutation run passed 63 reported
executions with 12 new interesting inputs from 40 baseline entries; it includes
grounded time follow-ups after mutations. A first full run exposed follow-up
integration failures and incorrect test assumptions, now covered by passing
regressions. The durable suite completed in 724.443 seconds with a 30-minute
package budget; production operation deadlines were unchanged. The canonical
universal app was rebuilt and strictly verified against the exact original
certificate and designated requirement, retaining only the audio-input
entitlement. It was not launched. The larger personal-memory goal remains open;
these counts are not semantic-unique use cases or live macOS acquisition proof.

## Changing multi-agent text histories

```sh
go test ./internal/client -run '^$' -fuzz '^FuzzMultiAgentTextRecall$' -fuzztime=30s -parallel=2
```

This target creates small encrypted histories at runtime with two to ten named
agents. It varies factual terminal text at the head, middle or tail of sources
up to 8 KiB, app and date exclusions, recent identifier lookalikes, deletion and
store reopen. An independent insertion ledger checks live source IDs, factual
passages, per-source subject attribution and explicit disclosure of missing
agents. One guaranteed in-scope source prevents an empty answer from satisfying
the test. It preserves the older personal-memory target and its replay corpus.

Seven seed histories passed, including ten agents with 8 KiB tail passages. One
bounded 30-second campaign passed with 13 reported executions, zero new
coverage-interesting inputs and seven corpus entries. Mutation throughput was
low: this establishes the exercised histories, not broad coverage, thirteen
unique user needs or a million-use-case result. The campaign preceded the final
shared-source passage and comparison-follow-up repairs; their dedicated
regressions and the final suite validate those later changes.
