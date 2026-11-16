# macOS capture and memory processing

## Connected path

Native app capture now prefers a document job. It reads Accessibility content
across bounded rounds, writes redacted parts into private encrypted staging,
and publishes only after final target and privacy checks. Large sources no
longer stop at the old single-observation byte limit.

Published parts retain their real source IDs, document membership, offsets and
capture times. The production index worker drains their exact index, including
after restart. Generic source readers connect them to:

- Search and Ask, including relevant passages near the end of a large source;
- Today, app recognition and time-scoped recall;
- selected-model meaning-search candidates;
- automatic Library and optional semantic indexing;
- current-hour evidence and recovered hourly recaps;
- confirmed facts, tasks and whole-source deletion.

Real revisit timestamps are separate from the immutable first-save receipt.
Deleting a cited part deletes its parent capture, sibling projections, native
reminders and volatile activity attribution.

## Acquisition coverage

| App content | Acquisition | Coverage boundary |
| --- | --- | --- |
| Text exposed through Accessibility values | Immutable native string, bounded Unicode ranges | Entire exposed value and traversed document |
| Range-based text, including terminal surfaces | Character-count and range accessors | Full known range; visible-only access is marked partial |
| Electron and Chromium apps | Supported manual Accessibility request, then document reader | Content the app exposes |
| Browser pages | Verified active web area; domain and private-window checks | Exposed page content, excluding browser chrome |
| Visible custom or canvas content | On-device Vision and ScreenCaptureKit fallback | Visible verified window or browser viewport only |
| Secure, private or excluded contexts | Privacy checks prevent capture | No acquired evidence |

An app can omit offscreen content from Accessibility. OCR does not claim to
capture offscreen content. Unverified geometry, changed targets, unreadable
privacy metadata and missing permissions remain explicit capture failures.
When Blur has completed a safe, complete Accessibility structure walk but a
text or range value read fails or times out, it now retries through the same
foreground-only OCR path with a separate bounded deadline. Structure, role,
sensitivity, focus and current-policy failures still stop capture.
The current changes do not establish live success in every installed app.

## Processing and responsiveness

Each document round shares a deadline across validation, reading and staging.
OS events and cancellation run between rounds. Unfinished sources remain
unpublished; a failed final validation cannot expose a partial document as
complete. Capture status distinguishes reading from successfully saved content.

Routine capture notifications resume Library and semantic workers from their
capture cursors. They do not request a full scan of older observations. Manual
repair and periodic repair keep their separate behavior. Semantic startup also
retains its legacy-source repair. Foreground model requests retain priority over
background processing.

Apple source processing uses its supported content-transformation mode with
string generation and complete schema guidance. Strict JSON, source and
proposal validation still applies. General answers retain the default policy.
A declined source cannot block subsequent sources or raw Search and Ask.

## Derived-data erasure

Recaps record every source sent to generation, independently of the subset
cited in prose. Their reverse links support repeated visits without a growing
single-record source list. Erasure removes one recap per journal step. Generated
Today questions carry the same input dependencies; replacement and erasure
remove those generated questions while preserving authored answers and unrelated
Today content.

An input receipt distinguishes new evidence from already processed input,
allowing late indexed parts to refresh a recap without repeatedly generating
the same citation subset. Older automatic recaps without complete input
dependencies are handled by the [dependency migration](recap-dependency-migration.md).
Captured text and authored notes are preserved.

## Verification boundary

Regression tests use synthetic app trees, native metadata fixtures and actual
temporary encrypted stores. They cover large Unicode tails, redaction across
chunks, canonical duplicate identities, time-window paging, restart, privacy
changes, cancellation, indexed retrieval and derived-data deletion. Browser
tests exercise pre-acquisition region selection and chrome overlap rejection.

These tests are separate from live capture of Orion or other installed apps.
The repository requires explicit authorization for app lifecycle actions. A
build alone does not relaunch the running app or start a recording.

| Live app check | Current evidence |
| --- | --- |
| Orion | Pending live check; browser AX, privacy and pre-acquisition crop fixtures pass |
| Terminal | Pending live check; range-only Accessibility fixtures pass |
| Herdr | Pending live check; generic Accessibility and visual fallback fixtures pass |

## Packaged build, 2026-11-14

The canonical bundle is `~/Downloads/Blur.app`, identifier `com.hazcod.blur`.
Its main executable SHA-256 is
`b4d58a760d2bbfb6bb804616e1ae1b9bfa40684edef44e96248a65a8fe9685e0`.
The host, Foundation Models helper, meeting-audio library and screen-text library
all retain their exact existing certificate and designated signing requirements,
universal arm64/x86_64 architectures and Hardened Runtime. The approved direct
build remains unsandboxed with library validation enabled.

Both packaged native libraries passed signed Go ABI tests using invalid requests
only, without screen or audio acquisition. The packaged Apple helper passed
synthetic grounded answers, insufficient-evidence handling and supplied-source
transformation. The three synthetic answer generations took 931, 802 and 790 ms;
these measurements do not measure the full Ask or capture workflow.

The settled whole Go suite passed, including the encrypted-store package in
197.994 seconds. The combined historical-cleanup, source-link, part-recap and
follow-up race cohort passed three runs in 58.858 seconds. Lint reported zero
issues; vet, supported cross-build/test compilation and the no-cgo audit passed.
The app has not been launched or restarted by this task; live verification
remains pending authorization.

Full app captures currently stay on this Mac. The existing cross-Mac replica
format does not yet carry them; this is stated in Sync settings and the
[sync design](sync-design.md).
