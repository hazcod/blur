# Streaming redaction for complete documents

## Intent and scope

Blur must preserve long pages, including their tails, without saving raw secrets
that straddle native text ranges or values. This implements part 2 of the
[large-document design](2026-11-13-large-document-capture-design.md). It does not
enable production capture, introduce a stored document format, or complete recall.
The existing `privacy.Redact` API and version-1 output remain compatible.

The previous goal turn made source progress: the native reader prerequisite is
complete and its review fixes and full verification gates passed. Actual user
Library startup recovery remains an independent unverified runtime gate.

## Approach and alternatives

Use a cascade of stateful rule transducers in the current redactor's order.
Whole-document buffering keeps a total-size ceiling and increases Go memory
with the page. Independently applying Redact to chunks misses split secrets and
replacement-created matches. A union of possible match boundaries cannot account
for earlier replacements removing line breaks. The cascade preserves composition.

This is the implementer's choice under the user's autonomous execution request;
no new product interview or approval gate is required. Go and the standard
library remain the implementation tools.

## Public contract

In `internal/privacy`, provide `NewStream() *Stream`,
`(*Stream).Write(context.Context, string, func(context.Context, string) error) error`,
`(*Stream).Finish(context.Context, func(context.Context, string) error) error`,
`(*Stream).Abort()` and `(*Stream).Counts() []Count`.

The caller owns a stream on one goroutine. Input strings must be valid UTF-8,
contain no NUL, and be at most 64 KiB per call; there is no total document limit.
Native range reads already preserve UTF-8 scalar boundaries. The capture owner
inserts logical newlines between values, never between ranges of one value.

Each Write/Finish works on an independent state checkpoint and collects its
bounded output. It calls the supplied sink once, with fully redacted text. A sink
must accept the entire output on success and accept nothing on error. Only sink
success commits the checkpoint and counts. No-output writes commit without a
sink call. Processing or sink failure leaves the prior stream retryable; errors
are content-free and preserve typed context cancellation/deadline causes. A sink
success commits even if the deadline expires immediately afterward, preventing
accepted output from being repeated. Reentrant writes are rejected.

Finish supplies real end-of-input to every stage in order; repeated Finish and
writes after Finish are rejected. Abort never flushes pending text: it clears
owned mutable buffers, drops state, and closes the stream. Go does not guarantee
erasure of all temporary strings or runtime copies.

Define `StreamVersion = 2`, independent of `Version = 1`. New document types will
use this policy explicitly; no current stored version or schema changes here.

## Rule composition and fidelity

Stages are PEM, Authorization, URL sanitization, URL-aware labels (JSON quoted,
plain quoted, unquoted), token, SSN, card and IBAN. Shared match replacement
helpers keep the batch redactor's output. Malformed-URL receipts are normalized
to count only the final whole-URL mask, after validating every decoded query key;
they no longer count discarded speculative edits or depend on map iteration.
For bounded complete candidates, output and counts equal batch Redact except
for the explicit version-2 privacy corrections below, regardless of boundaries.

Each label pass arbitrates a union of its label regexp and an opaque URL
alternative by earliest start. An earlier label owns its entire value, including
URL text; an earlier URL protects internal label-looking text. There is no
synthetic EOF. This deliberately corrects batch v1's outside-label/inside-URL
leak, while v1 remains unchanged for existing observation validation.

Version-2 unquoted labels require a nonblank first value rune, preventing the
batch rule from masking a separator blank before an already masked quoted value.
Whitespace-only fields contain no secret value and remain unchanged.

Version-2 quoted rules allow an escaped rune to be LF; ownership lasts until the
first unescaped closing quote or real EOF. An opened but unclosed key/quoted
secret at Finish is masked. Exact placeholders remain stable for bounded
candidates. Ordinary text after a completed field survives.

Version-2 replacement captures preserve the preceding-rune boundary already
validated by the incremental recognizer. Requiring a new word boundary at the
start of the isolated buffer can lose a Unicode-folded label such as `ſecret`
after an ASCII word rune. The legacy version-1 capture policy is unchanged and
needs a separate migration-safe privacy audit.

Abort during a sink callback sets a terminal-abort request. An accepted sink
commits counts once, then clears state and stays closed; a rejected sink keeps
previously committed counts but still completes Abort. A no-output commit checks
context immediately before commit.

## Incremental recognition

Compile each existing regexp using `regexp/syntax`. A bounded Thompson state set
tracks viable starts and accepted ends across calls, with real preceding and next
runes for empty-width assertions. Capture groups are needed only when replacing
a resolved bounded match; the existing regexp extracts them from that buffer.

The patterns have greedy unambiguous extent, except PEM's intentionally lazy body.
For greedy stages retain the earliest accepted start and its latest accepted end
until no viable thread at that start or earlier can consume the next rune. For
PEM resolve the earliest closed key immediately when no earlier start remains
viable. This prevents a completed key from retaining the rest of the document.
Runes after an accepted end are replayed through a reset recognizer, with their
original preceding rune, so another match is not lost through state coalescing.

Prefix output is safe only before both the earliest viable start and earliest
accepted match. Per-program-counter states coalesce to the earliest start.
Non-starting ordinary runs can use precomputed first-rune classes to bypass the
NFA; this optimization must preserve ASCII word boundaries and Unicode casefold.
No pattern, partial match, error or diagnostic may include raw content in logs.

## Bounds and oversized fields

Each recognizer retains at most 64 KiB of candidate raw bytes, plus bounded rune
lookahead and program-sized state. Plaintext forwarding buffers are at most 16 KiB. Resolved matched values
may be forwarded whole, bounded by one candidate plus its fixed replacement
expansion. Checkpointing duplicates this fixed state, rather than the
document. Result memory is bounded by one input call plus pending stage buffers
and the fixed replacement expansion; it does not grow with the document.

Overflow starts only when another candidate rune would exceed 64 KiB. Emit one
fixed placeholder, then discard body bytes while retaining bounded policy state.
A fully opened field drains to its syntactic end. Tokens drain the entire
contiguous ASCII credential lexeme `[A-Za-z0-9_-]+`, covering a later credential
whose prefix lies in an earlier token's non-word overhang.

For an oversized partial header that has not established a field, quarantine its
logical record through a permanent newline: a newline outside an opened private
key or sensitive quoted field, with no Authorization header still waiting across
whitespace. Sensitive quote ownership starts at an actual JSON/plain secret
header; unrelated quotes cannot close it. This deliberately masks more of a malformed oversized record; it
preserves following records without exposing a hidden PASSWORD/Authorization
prefix. Program-sized scanners model policy order without retaining raw bodies:
PEM contents cannot alter Authorization or quote state; Authorization bodies
cannot alter later quote state; JSON bodies cannot alter plain quoted state.
Each quoted pass treats earlier URL bodies as opaque, with real URL delimiters
still able to start a new header. An earlier sensitive header owns any URL inside
its value. Fixed placeholders act as opaque non-whitespace atoms. Replay the
bounded candidate through this recovery state when quarantine starts. A newline
meeting these conditions cannot be removed by any later redaction rule, so it
resynchronizes the cascade. Abort/EOF never emits a quarantined suffix.

Fixed-width rules cannot overflow. Oversized URL units use MALFORMED_URL, not a
new rule name. The 64-KiB input limit is a work-unit bound, not a page limit.

Counts aggregate only committed work, use existing rule names, stay sorted in the
public snapshot and must not silently overflow. Dynamic memory/state must remain
bounded on an unterminated multi-megabyte key, URL, quoted label or header.

## Required proof

- Every ASCII split position and valid UTF-8 split position for all rules,
  malformed/double-encoded URLs, exact placeholders and adjacent prose.
- Nested keys/quoted values and replacements that remove line breaks before a
  later label/Authorization rule. Lazy first key footer preserves following text.
- EOF and Abort with opened secrets; cancellation and sink rejection/retry leave
  state/counts/output consistent with exactly-once accepted consumption.
- Multi-megabyte ordinary documents retain their tail and have bounded state;
  oversized ambiguous fields mask as units and recover following prose.
- Batch parity for bounded complete candidates, deterministic randomized chunks,
  Unicode folding/boundaries, card/IBAN validation and scoped URL labels.
- Full Go suite, privacy/capture race checks, vet, cgo-disabled build and cross
  compilation, then one fresh read-only review and one material RED/GREEN fix pass.

Do not pass separately sliced stream output through SanitizeObservation: a part
boundary changes regexp word/EOF context. The subsequent document design must
validate ordered documents as a stream and use a capture-owned sanitized type,
authenticated part membership and schema-aware import/erasure. This prerequisite
alone is not a reason to replace the stable signed Downloads app.
