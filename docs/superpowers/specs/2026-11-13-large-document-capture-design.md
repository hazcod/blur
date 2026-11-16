# Large document capture and recall

## Intent and current evidence

The user expects Blur to remember long pages and technical work, then answer
natural questions about them. Increasing a prefix limit does not satisfy this.
The macOS app must continue reading the selected document, preserve its order
and coverage, and retrieve either relevant passages or the whole document.

Current `Pipeline.Capture` returns one observation, with production budgets of
256 KiB, 2,048 nodes, depth 32 and 750 ms. `collectAXFrame` stops structural
traversal at that budget. Native `childrenPage` and `copyCFStringRange` already
provide bounded continuations, but the capture path does not consume them.
Observations have a 1 MiB record ceiling. Their content IDs identify a whole
observation, not an ordered document. Individual sanitization of arbitrary
fragments would break redaction across values or ranges.

## Approach

Use continued native reads, a stateful redaction boundary, and encrypted ordered
document editions. Each capture turn has a work budget; it is not a total
document-size ceiling. Ordinary Ask retrieves useful passages. A request about
an entire document must inspect every relevant part, use bounded synthesis
where necessary, and disclose incomplete coverage.

Considered alternatives were larger whole-frame buffers and independently
sanitized fragments. Whole-frame buffers retain a total-size ceiling and make
latency/memory grow with the page. Independent fragments lose ordering and
cross-boundary redaction. The continued document approach is the implementation
call under the user's existing autonomous execution instruction.

## Required end-to-end parts

1. **Native read continuation.** Traverse children in pages of at most 32,
   retain at most one immutable text snapshot, and read UTF-16 ranges as UTF-8
   pieces. Preserve DFS text order and separators. Each round bounds text,
   structural work and elapsed time. No total byte/node cap is introduced.
   Keep depth and active-reference ceilings, marking depth/cycle omissions.
2. **Streaming redaction.** Retain state across native values and pieces.
   The current private-key, authorization, quoted/unquoted secret, URL,
   token and financial/identity rules must protect every split position.
   Nested/replacement-created matches need explicit regressions. Bound raw
   pending memory; an oversized ambiguous field must be masked as a unit while
   later ordinary text remains readable. Abort/cancel cannot emit an uncertain
   raw suffix. This needs its own completed design and implementation gate
   before native streams can be connected to persistence.
3. **Encrypted document editions.** Stage only sanitized pieces. Persist order,
   coverage and original source spans in bounded pages. Publish a stable
   edition only after its membership is consistent. Deduplicate unchanged
   editions and record later visits. Staging must be resumable or discarded on
   reopen; it must not masquerade as complete captured evidence.
4. **Admission, erasure and indexing.** Apply permanent deletion fences at
   commit. Erase document membership, parts and every derived projection in
   bounded resumable work. A newer schema must reject writers that cannot
   maintain or erase this structure. A forgotten page cannot be reconstructed
   by an older capture or pending job. Index parts without scanning the whole
   database per query.
5. **Ingestion and activity.** The native stream stays inside `capture`; only
   sanitized document values cross its boundary. Cancel on changed scope,
   focus/window/page, private/secure state, pause or permission loss. Bind
   capture health and activity to the document instead of counting every part
   as unrelated activity. Passive text must not silently create reminders.
6. **Recall and inspection.** Search/Ask/MCP use consistent document grounding.
   Passage questions retain exact text and source positions. Whole-document
   requests cover all parts through bounded, cancelable analysis and progress;
   model context limits must not silently reduce a whole-page answer to eight
   convenient excerpts. The selected AI and existing disclosure policy apply.
7. **Native acceptance.** Demonstrate content after 256 KiB and 1 MiB, many
   children, split secrets, cancellation, changed page, restart, complete
   deletion, and a tail-only fact through Ask in the same signed Downloads app.

The initial native-reader subplan is a prerequisite, not a replacement for
these seven parts. Until redaction, persistence and retrieval are connected,
the production `Capture` path and its reported limits remain unchanged.

## Native reader contract

The cursor owns its root and all acquired references. Its interfaces and raw
text remain private to package `capture`. `read` emits pieces only after a
caller-supplied target/path guard and fresh node sensitivity/role checks; guard
again after a range before emission. Native text leases use an immutable
`CFStringCreateCopy` snapshot and scalar UTF-16 offsets. Go range buffers are
bounded; native memory remains proportional to one AX value, since AXValue
returns a whole native string. This is a fidelity tradeoff, not a claim of
constant total native memory for arbitrarily large single values.

Separate round and lifetime controls. A round uses existing `Limits`, requires
at least four bytes, and yields normally on byte/node/time budgets. A deadline
or cancellation from the owner is terminal. The native messaging timeout
continues to bound one AX request. At most 32 child references per stack frame,
64 frames, and one text lease are retained. Logical ancestor cycles (including distinct proxies identified by native CFEqual) are released and
reported as omitted structure. Shared sibling references may repeat; no
unbounded global identity map is retained. Invalid/mutating child pages fail
closed. A selected child path is checked before each text range.

An ignored role skips that node's value, retaining existing descendant policy.
A sensitive role skips its subtree. No writable-control capture policy changes
are bundled into the reader. Technical read-only text-area support requires a
separate explicit policy gate.

## Compatibility and delivery

Keep the Go implementation and existing purego macOS ABI. No new dependency,
browser extension, screen recording or raw-text disk buffer is required.
The native-reader prerequisite adds no stored format and no user setting.
The later document repository requires schema/version and migration decisions
before implementation; existing observations must remain readable without
claiming their missing tails were recovered.

Keep `~/Downloads/Blur.app`, `com.hazcod.blur`, its exact certificate,
designated requirement and sandbox. Build only a complete usable path; a
prerequisite module alone is not a reason to replace the user's app. Native
lifecycle remains subject to `AGENTS.md`. The actual reported Library startup
failure remains a separate open diagnostic gate.
