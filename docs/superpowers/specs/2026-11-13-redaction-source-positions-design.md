# Source positions through streaming redaction

## Intent and parent scope

The next document repository must retain source positions after redaction changes
text lengths. The completed native cursor and redaction stream do not yet expose
that mapping. This fills that gap before ordered persistence; it does not replace
the seven-part [large-document design](2026-11-13-large-document-capture-design.md).
The previous goal turn completed the streaming redactor and all post-review gates.
The user's actual Library startup failure remains unresolved independently.

## Decision

Add an optional mapped stream using the same recognizers, transformations and
transaction boundary. Each recognizer emits its input byte start alongside safe
plaintext and bounded matches. An ordered metadata queue per stage composes these
positions through earlier replacements. Whole-document buffering, re-running
redaction on stored fragments, and assigning output offsets directly to raw text
cannot provide correct positions across delayed or length-changing replacements.

This is an inline implementation choice under the user's existing autonomous
instruction; no new product interview or approval is required.

## Contract

In `internal/privacy` add `NewMappedStream() *MappedStream`, with
`Write(context.Context,string,func(context.Context,MappedText)error)error`,
`Finish(context.Context,func(context.Context,MappedText)error)error`, `Abort()` and
`Counts() []Count`. Keep the existing Stream API and output behavior compatible.

`MappedText` contains fully sanitized `Text` and ordered `Spans []OriginSpan`.
Each `OriginSpan` has global, zero-based, exclusive byte coordinates
`OutputStart, OutputEnd, InputStart, InputEnd int64`, plus `Exact, OpenEnded bool`.
Input positions refer to the logical original document stream, including real
separators supplied by capture. Output positions refer to the sanitized stream.
There are no artificial separators between writes.

Spans partition each emitted batch without gaps or overlaps. An Exact span maps
bytes one to one and its original text equals its output text. A transformed
span maps to the envelope of the original inputs that produced it, not a claim
that every output character has a matching original character. Coalesce adjacent
exact contiguous runs and identical opaque envelopes to avoid per-character
metadata on ordinary text. Unicode coordinates are byte positions on scalar
boundaries; native value/UTF-16 locators belong to the capture-owned layer next.

Forced masks use `OpenEnded`: their known input envelope covers the retained
prefix that produced the placeholder, while subsequent drained bytes have no
output. It does not claim the full extent of an oversized/unclosed secret. An
ordinary tail after a drained secret must still have its exact original offset.
Composition preserves this uncertainty rather than inventing a precise extent.

Only sanitized text and numeric metadata reach sinks. Source maps contain no raw
text. Sinks accept the entire batch or nothing. Failure/cancellation rolls back
text, counts, positions and pending metadata together; retry does not advance
offsets twice. Sink success commits even if its deadline expires afterward.
Reentry and Abort during callbacks retain the existing single-owner semantics.
Caller mutation of emitted spans cannot mutate retained stream state.

## Bounds and architecture

Keep the input limit64KiB, recognizer pending limit64KiB and plaintext forwarding
limit16KiB. Stage metadata covers only pending candidates plus the current bounded
work unit. Retire metadata before the pending start, or through the consumed
position while draining; an unbounded drain must not accumulate origin entries.
Clone metadata with the transaction checkpoint and discard it on Abort/Finish.
No total document cap or raw disk spool is introduced. Checked int64 arithmetic
rejects position overflow with a content-free typed error before accepting input.

The normal Stream uses the same transaction implementation without source-map
queues. A small internal `feedSpans` adapter exposes recognizer input positions;
existing callers continue using `feed`. Mapped transformation composition lives
in a separate file rather than expanding the regexp matcher with domain logic.

## Required evidence

- Every valid split of Unicode/plain/secret/URL/nested/replacement-created cases
  produces the same text and counts as the existing stream, with exact source
  bytes for every Exact span and truthful envelopes for transformed spans.
- Accepted-end replay and real original word boundaries preserve coordinates.
- Long ordinary input preserves its entire tail; oversized secrets keep bounded
  metadata and exact tail positions, including after many no-output drain calls.
- Sink error/deadline, no-output pending writes, Finish retry, callback Abort,
  reentry, invalid inputs and position overflow preserve transaction semantics.
- Fresh full tests, privacy/capture race checks, vet, cgo-disabled build and
  cross-target compilation, followed by one fresh read-only review and one
  material RED/GREEN fix pass. No review loop.

## Delivery

No stored schema, user setting, app lifecycle, key or database changes here.
Preserve the signed `~/Downloads/Blur.app` for this unconnected prerequisite.
Next remains capture-owned document values and origin locators, encrypted ordered
editions and recovery, schema-aware erasure/indexing, ingestion/activity,
whole-document Ask/Search/MCP/inspection and native acceptance. A completed source
map is not evidence that long pages or the user's Library now work in the app.
