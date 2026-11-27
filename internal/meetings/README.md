# Meeting text session engine

This package implements a provider-neutral, bounded session for transcript text
and separately authored notes. The engine owns no audio capture, speech provider,
UI, summary model, or action execution. `Repository` is implemented by the
existing authenticated encrypted store in `internal/storage/durable`; its
separate tests use temporary encrypted stores and synthetic text. Engine tests
use an in-memory sink fixture. These tests do not establish real call recording,
speech quality, or macOS permission readiness. App/native wiring is owned by the
client integration.

## API

```go
session, err := meetings.New(ctx, meetings.Config{
    SessionID: "meeting-opaque-id",
    Tracks: []string{"microphone", "system_audio"},
}, encryptedAdapter)

token, err := session.Start(ctx) // explicit; construction remains Ready
err = session.Submit(ctx, token, meetings.Event{
    Kind: meetings.Partial, SessionID: "meeting-opaque-id",
    TrackID: "microphone", SegmentID: "segment-1", Revision: 1,
    Start: 0, End: time.Second, Text: "Volatile words",
})
err = session.AddNote(ctx, meetings.ManualNoteInput{
    ID: "note-1", At: time.Second, Text: "My own notes.\n",
})
snapshot := session.Snapshot()
```

`Start` and `Resume` return a new callback token. Native millisecond offsets can
be converted to `time.Duration`; the adapter must reject conversion overflow
before calling this package. Session, track and segment identifiers are bounded
opaque ASCII IDs, not speaker names. Two audio tracks do not identify every
person on a call.

Partials replace only a higher revision of the same active segment on that
track. An identical partial retry is harmless. A final may use the same revision
as its partial only when its text and time range are identical; otherwise the
revision must increase. Segment start time is fixed, end time cannot go backward,
and finalized spans cannot overlap earlier finals on that track. Tracks may
interleave and overlap each other. Finish the current partial segment before
starting another on the same track. Late partials for a finalized segment are
rejected without rewriting it.

Only `Final` events reach `Sink.Append`. Every append contains one final segment
or one manual note, never the whole transcript. A duplicate final or manual note
does not append twice. Conflicting finalized payloads/revisions fail visibly.
Manual notes remain distinct `UserNote` records with their own source receipts.
Text is preserved byte for byte after validation; this package performs no
redaction or derived action extraction. The durable storage adapter owns those
existing policies. Meeting content is data, and no task, reminder, message or
external action is executed here.

## Persistence trust boundary

`Sink` must explicitly declare `AuthenticatedEncryption` and
`AtomicIdempotent: true`. Missing, plaintext, or non-idempotent capabilities fail
closed at construction and are rechecked before each append. **These are adapter
declarations, not a cryptographic proof supplied by this engine.** A production
adapter must be separately reviewed and tested against the encrypted store.

The adapter contract is strict:

- Atomically store the bounded record and its stable `Record.Key`, returning an
  opaque source reference. Do not persist raw PCM, partials, or plaintext sidecars.
- Repeating exactly the same key and payload returns the same source reference.
  A changed payload at the same key returns `meetings.ErrConflict`.
- Honor context cancellation and bound its own latency. Do not report acceptance
  before the record is durable. Never require an optional model to save evidence.
- Retain idempotency and conflict metadata for the evidence's durable lifetime,
  including across process restarts. This package cannot enforce a sink's promise.

A sink failure or uncertain/canceled append leaves the complete candidate in
`Snapshot.Pending`, marks the session `Failed`, and invalidates callback tokens.
`Retry` submits that exact candidate and key again. A write admitted while
running recovers into `Paused`; an explicit `Resume` is required for further
native input. A manual note admitted while `Ready` or `Stopped` recovers into
that original state. Recovery never converts a stopped session into a resumable
one. A Stop accepted while a write is draining also remains terminal after a
failed write is retried. A retained manual note is
never relabeled as transcript evidence. Source references are opaque data, not
filesystem paths. Error messages produced by this package contain no transcript
or adapter error text; failure codes are available through `Snapshot.Issue`.

## Controls and integration order

`Pause` and `Stop` invalidate the current epoch before waiting for the one
already-admitted append. New callbacks using an old token fail immediately,
including while persistence is slow. Volatile partials are released. An append
admitted before the control operation can finish and appear in the snapshot;
the control does not return success until it has drained. A lifecycle operation
reserves transition ownership before waiting on persistence. Overlapping
Start/Resume/Pause/Stop/Retry calls reject `ErrState`; they cannot queue against a
future state and undo an operation that is still completing. `Stop` is idempotent
after completion. Stopped sessions cannot restart, while manual notes can still
be appended.

Callback session/epoch authority is checked before its context can affect the
session, including when waiting for the admission gate. A canceled callback from
an obsolete epoch returns `ErrEpoch`; it cannot fail a newer resumed session.

The engine does not own a device or a speech-analysis stream. The integration
owner must explicitly stop native capture, drain its valid final events using
the current token, and only then call engine `Stop`. Calling engine `Stop` before
native final drainage intentionally rejects those later callbacks. Native queues
need their own bounds, and the app must visibly handle failures. A sink that
ignores cancellation can delay drainage; this package creates no background
worker or unbounded queue to conceal that delay.

Canceled operations, sink failures, conflicting finals and retention limits are
visible failures. Invalid IDs, malformed text, invalid state transitions and
out-of-order events return errors without silently saving them. A caller must
surface those errors and terminate or recover the native path; errors must not
be presented as successful recording.

## Memory bounds and snapshots

Production repositories implement `RecordLookup`, an authenticated durable
stable-key index. With this port, zero configured cumulative limits impose no
session count or text ceiling. The engine keeps only a live tail of up to 20
finals and 20 notes, each with a 128KiB text budget, one partial per configured
track, one exact pending record, and checked counters/per-track timestamps.
Complete history is read through `Repository.MeetingRecords` in bounded pages.
`Snapshot` is a live view, not an export of the complete recording.

| Resource | Bound |
| --- | ---: |
| Configured tracks | 8 |
| Active partials | One per configured track |
| One transcript event | 16 KiB UTF-8 |
| One manual note | 64 KiB UTF-8 |
| Source reference | 256 bytes |
| Session/segment ID | 64 bytes |
| Track ID | 32 bytes |
| Transcript relative time | Checked `time.Duration` representation |
| Persisted source time | Checked canonical Unix-nanosecond representation |

These single-record bounds remain protocol/work bounds. Supporting one native
result larger than 16KiB or one authored note larger than 64KiB requires a separate
multipart evidence protocol; increasing total captured history does not raise
those individual limits.

Before any old timestamp is rejected, durable identity lookup distinguishes an
exact existing record, a conflicting payload, a deleted stable key and a new
key. The same check applies to hot-tail duplicates, so an erased source cannot
be acknowledged from stale RAM. Partials for an existing finalized key remain
out of order. A transient identity-read failure or failed write keeps one exact
pending payload for retry. Identity-read retries repeat lookup and normal order
and budget admission before attempting an append. Admission and lookup use a synchronous gate, so event producers receive
backpressure and controls can invalidate callback epochs during a slow lookup.

Callers can supply explicit nonzero cumulative work budgets. Legacy sinks that
lack durable lookup retain finite `DefaultLimits()` budgets (32,768 transcript
segments, 16MiB transcript text, 256 notes, 1MiB note text) and complete in-memory
key history within those bounds; this compatibility path does not back the
production repository. Neither path silently truncates persisted evidence.

All accepted strings are cloned so a small substring cannot retain an
arbitrarily large caller allocation. Hot-tail eviction clears old references.
Snapshots copy slices and the pending pointer; callers that retain repeated
snapshots own that extra memory. The continuation regression crosses the former
session count and byte ceilings while checking bounded hot state, and encrypted
repository/controller integration covers exact retries, restart, deletion,
long offsets, tail restoration and complete paged retrieval.

## Verification and implementation rulings

Task 1 follows `docs/superpowers/specs/2026-11-13-meeting-memory.md` and its
implementation plan. The isolated task's allowed scope is this directory;
integration and final cross-module review belong to the root agent. No commit,
app build, app lifecycle change, microphone access, user store or user key is part
of this work.

Rulings made for this slice:

1. Use a synchronous admission gate rather than a background queue. This bounds
   engine work and makes sink backpressure explicit; the native adapter must
   provide its own bounded event delivery.
2. Do not evict finalized metadata. A full session fails visibly rather than
   weakening replay/conflict guarantees; longer sessions need a later paging
   integration.
3. Retry uncertain persistence with the exact same record key. Running writes
   recover paused, while Ready/Stopped authored-note writes keep that state.
   This avoids an automatic recording restart or reopening a terminal session.
4. Treat sink capabilities as an explicit audited-adapter trust boundary. A fake
   test sink's declaration proves admission checks, not encryption itself.

Tests were written and observed failing before the implementation. Additional
regressions were observed failing for capped-note retries, wrapped cancellation
messages, partial/pending double retention, queued Resume/Pause combinations,
Ready/Stopped manual-note retry state, and obsolete callback cancellation before
those fixes. Additional tests combine blocked writes with Stop/failure/retry and
with canceled obsolete admission waiters.

```sh
go test ./internal/meetings -count=1
go test ./internal/meetings -race -count=1
go vet ./internal/meetings
golangci-lint run ./internal/meetings
```

## Durable repository and bounded reads

`Repository` adds revisioned metadata, newest-created-first meeting listing,
append-order record paging, and explicit deletion. `PutMeeting` creates revision
1 with expected revision 0; subsequent updates use the stored revision plus one.
The start clock and track list cannot change after creation. `RecordCount` is
repository-owned and incremental appends do not advance the public metadata
revision. The durable catalog pages meeting headers from disk without a total
session cap; its ordinals retain the existing 48-bit representation bound.

Each final/manual note commits as one existing sanitized observation, its exact
relative native time span, and a stable keyed receipt in the same encrypted
transaction. Per-record opaque domains keep identical utterances in different
segments distinct. Authenticated keyed original-payload digests detect conflicts
even when two inputs redact to the same saved text. The original unredacted text
is never stored by this adapter. Redaction expansion that exceeds the per-record
limit is rejected; text is never shortened to fit. Session history has no total
segment, note, or text-byte cap. Counters, ordinals, durations and wall timestamps
retain their checked representation bounds. Partials never reach durable storage.

A successful append completes that source's exact search index without a model,
so existing Ask/Search can retrieve the saved evidence. Raw transcript finals do
not create Today tasks or action candidates. If indexing fails after the primary
commit, append reports failure; replaying the same key completes the durable
index receipt without duplicating evidence.

`LookupRecord` authenticates stable-key identity, original-payload conflicts,
live source membership, and erasure fences before the engine rejects an old
timestamp. It also finishes an existing pending exact-index receipt before
returning success, without creating or restoring evidence.

Read pages have at most 50 rows and 256 KiB of serialized metadata/text, default
64 KiB. Each page scans at most 256 ordinal slots, so deletions can yield an empty
page with `More=true`; callers must continue using `Next`. Record cursors always
preserve the last consumed sequence, including an empty tail. If a single record
does not fit the requested budget, the read returns `ErrLimit` rather than
truncating or silently stalling. Use these pages for UI tail updates instead of
copying full engine snapshots repeatedly.

Ready-agenda startup uses `ReadyAgendaTail`: one snapshot authenticates Ready
state with no recording clock, end clock or prior finals, then probes at most
256 descending ordinal slots for at most 20 live rows/128 KiB. Returned rows are
in ascending order; `Cursor` is the last allocated ordinal including erased
slots. `Older` reports live evidence outside this display tail, including work
stopped by deletion holes or its byte budget. Full history remains available
through `MeetingRecords` pages.

Source erasure atomically removes its record/span links and creates an opaque
key fence. Removing the last source also removes and fences meeting metadata.
Whole-meeting deletion uses the existing authenticated source-erasure journal
and store gate, removing one bounded source cascade at a time. An interrupted
erasure blocks reads/writes and resumes on reopen before serving data. Replay
fences remain after deletion so neither delayed receipt retries nor old metadata
updates can recreate erased evidence. They contain no transcript, title, locale,
track, timing, or speaker metadata; their disk footprint accumulates until a
separate safe replay-expiry policy exists.

`DeleteMeetingsBefore` only cleans empty metadata older than the cutoff. Existing
observation retention uses each segment's exact capture time, preserving newer
segments from a meeting that started before the cutoff. `DeleteAllMeetings`
removes all remaining mapped records and empty metadata. Summaries, speaker
identification, decisions, and action extraction remain separate future work;
no meeting content authorizes an external action.

## Creation, recording and manual-note clocks

`Meeting.StartedAt` remains the immutable creation timestamp and the fallback
origin for legacy transcript offsets. `RecordingStartedAt` is an optional,
separate recording origin. A Ready agenda with no saved finals can acquire that
origin once; it cannot change afterward, and existing legacy transcript origins
cannot be rebased. Starting a yesterday-created agenda therefore admits native
transcript offsets from zero while keeping its earlier notes and source capture
timestamps intact. Transcript offsets and resolved source times must fit their
checked duration and Unix-nanosecond representations.

New manual notes carry an explicit `RecordedAt` wall timestamp and `At=0`, frozen
once per note ID for uncertain-write retries. This timestamp may precede a newly
created meeting header by milliseconds when saving a draft creates the header;
it is preserved exactly, without clamping. Manual notes written days later also
retain their actual recorded time. Legacy notes with zero `RecordedAt` keep
creation-relative `At`, independently of the recording origin; manual offsets do
not inherit the audio duration cap. Ambiguous dual clocks and timestamps outside
the store's lossless nanosecond range fail explicitly.

`RecordTimes` supplies one provenance calculation for storage, summaries and
presentation. New optional fields are appended with `json:",omitzero"`, preserving
canonical JSON and original payload HMAC bytes for old headers and records. No
namespace/schema migration, raw-source rewrite, capture-time rewrite or receipt
change is needed. Encrypted-store regressions exercise old canonical bytes,
reopen/replay, separate recording clocks, later notes and immutable anchors.
