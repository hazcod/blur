# Safari DOM continuation

Approved in the delegated capture audit on 2026-11-22. Capture all permitted DOM text through bounded continuation, retaining current privacy, active-page, encrypted-storage and preference admission requirements. Do not change live app state or capture databases.

## Approach

Use sequenced v2 browser batches and existing authenticated multipart CaptureBuilder/storage publication. Merely raising the 256KiB/10,000-node/100ms ceilings would retain permanent omissions; a partial flag alone would not recover the tail. Keep these work ceilings and the 288KiB wire ceiling. Transport text batches are at most 32KiB so escaped JSON fits without clipping.

JS retains TreeWalker and UTF-16 text-node position, reads substringData ranges, and keeps whitespace normalization across batches. Each revision fences the initial final DOM node. Appends after the fence queue another revision without invalidating progress. Changes inside the fence end the stable prefix as structure-limited; a later revision starts fresh. Constant rewrites of existing nodes can remain visibly partial; AX capture remains enabled.

Only one batch is outstanding per page. Native acknowledgements advance its sequence; failed/lost ACKs retry the identical text. Swift validates sequence and completion, rechecks active/non-private Safari state, binds a native hashed page identity, and sends the full batch without truncation. Raw URL paths/query strings never cross the native-to-Go bridge.

Go validates signed bounded batches, stages through the existing streaming redactor, and publishes only ordered final summaries. Duplicate identical batches are idempotent; changed/skipped retries restart the scan. At most 4 jobs retain bounded builder state; eviction discards private staging and requires restart. Pausing drains and cleans every stage. Native preference admission wraps each begin/append/publish; notifications run after admission returns. Committed multipart documents invoke the shared document callback. Legacy complete observations invoke the observation callback; explicitly partial legacy snapshots are rejected.

Capture only the top-level body. Body replacement stops its old cursor and requests fresh native authority. Fragment-only navigation preserves its document identity; path/query navigation starts a new scan. Freeze the first native title for retry stability, so changing unread badges cannot change already staged bytes. Native URL and title metadata retain explicit 4KiB/8KiB protocol guards; unsupported metadata disables the Safari path and leaves ordinary AX capture active.

In-memory continuation is bounded and restartable rather than persisted across extension or app process restarts. A missing/evicted job rejects a continuation and triggers a fresh revision. Existing canonical publication deduplication keeps successful retries from duplicating editions. Constant rewrites within the initial document boundary may remain structure-limited; no mixed revision is reported complete.

## Verification

Node tests cover >256KiB, >10,000 nodes, large single nodes, Unicode/whitespace boundaries, stable append-only fences, tail edits, retries and cancellation. Go tests use a temporary durable store to verify private staging, complete tail indexing, source provenance, duplicate publication, partial coverage, sequence errors, pause and policy. Swift protocol runtime checks and full handler typechecking verify bounded frames and ACK parsing.
