# Voice notes and cross-feature delivery status

Updated 2026-11-15. This records implemented behavior and remaining acceptance
work; it is not a claim that live recording has been verified.

## Voice notes

The interface uses **Voice notes** for personal thoughts, dictation and
conversations. Internal `meetings` package names and encrypted record domains
are retained. Opening the menu item or Control–Option–M shows a
compact overlay and starts recording automatically, following the user's
explicit preference. The overlay appears before audio setup starts. Reopening
an active or paused recording presents its current state without starting a
second recording or resuming a deliberate pause. Ask is the shared destination
for questions, search, typed notes and saved voice transcripts. Old Search and
Voice notes routes resolve there without starting recording. Settings contains
the voice preferences; there is no separate Saved notes destination.

- Typed notes save without starting audio. Saved text has its own timestamp.
- Opening Voice notes requests the selected microphone/system-audio permissions.
  Missing Apple speech files have an explicit Download & start action.
- Pause, Resume and Stop follow native acknowledgements. The menu icon's center
  dot turns red after the recorder acknowledges running and stays red until
  recording actually stops. The overlay meter displays measured PCM energy.
- The normal popup has Pause/Resume and Stop, plus an icon to pin it. Its clock sits beside the
  sound indicator and counts acknowledged recording time, excluding setup and
  pauses. One current phrase appears to the right of the buttons. A clipped
  single-line viewport follows the latest words without a scrollbar; pausing
  clears it. There is no Listening placeholder or recording subtitle.
  Preparation, download and finishing progress use that same inline phrase
  slot rather than adding another status row.
- Audio sources and language are saved in Settings > Capture > Voice notes.
  Setup checks beside Accessibility report speech-file readiness and notification
  permission. Explicit speech-file installation there does not start recording
  or request microphone/system-audio permission. Notification prompts are also
  explicit. Readiness checks are bounded, coalesced and cached while visible.
- The overlay hides after ten seconds of acknowledged recording unless pinned.
  Unpinning starts a fresh ten-second deadline. Escape hides it without stopping
  audio. Notch hover reveals an existing recording, including a paused one,
  without starting another recording. The menu has separate Pause/Resume and
  Stop recording actions. Its first row reports Recording - Capturing or
  Recording - Capture paused. Settings explicitly raises the
  dashboard. Quick Ask targets its own window before activating Blur.
- Native previews are temporary. Immutable final segments and manual notes
  become encrypted, searchable sources. Audio files are not saved or uploaded.
- Explicit Stop opens the completed voice note in Ask, with an on-device recap
  above the paged transcript. Recaps use bounded, progressively covered source
  ranges and disclose partial coverage or model failure. They do not create
  tasks or reminders. A background completion never steals focus.
  Transcripts start collapsed behind Show transcript. Saved notes also have an
  explicit Recap voice note action that uses the full local pipeline without
  starting the recorder; merely opening an archive does no inference.
- Recaps use a dedicated saved-transcript transformation, rather than the
  general Ask/refusal path. Consecutive fragments share compact metadata and
  retain up to fifty genuine source labels per automatic step (twenty for an
  explicitly requested visible-page summary). Runtime token admission can use
  fewer rows; the next step starts at the exact unread row. One strict format
  repair is allowed. Invalid or contentless prefixes never become recap prose;
  an explicit coverage gap lets later useful speech continue. Cancellation,
  source erasure and provider-policy changes still stop publication.
- Saved notes and transcripts have bounded pagination. The live tail is bounded;
  history does not reload on a periodic timer. Audio-only events repaint just
  the meter. Its freshness timer stops when the overlay is hidden or paused.
  DrawingArea explicitly regenerates the meter pixels; merely scheduling a
  paint reused the initial cached bitmap and previously made it appear frozen.
  Live phrase and clock updates use scoped UI state instead of rebuilding the
  app. Hidden transcript history does not cause recurring window updates.
- A saved note that has not been recorded can be explicitly started after
  restart using its original identity and settings. Setup failures remain
  retryable. The recording clock starts independently of note creation time.
- Summarize this page uses only saved sources from that page, the selected
  model and current AI policy. It states when coverage is limited.
- Deletion coordinates reminder cancellation, source erasure and the durable
  deletion journal. Retention removes individual old sources while preserving
  newer text in the same note.

## Related fixes

- Saving a quick note acknowledges durable storage immediately. Optional model
  suggestions run on one bounded background worker. Remote processing requires
  its own note consent. Deletion and model/credential changes fence late output.
- Reminder notifications contain the saved reminder title. macOS preview policy
  can still hide notification text. Future legacy notices are upgraded once
  without alerting again for already-delivered reminders.
- Natural durations accept compact and spaced minute forms. Relative dates have
  local-time defaults; explicit 12/24-hour times take precedence. Conflicting or
  incomplete requests ask for the missing information.
- Apple grounded answers use a schema containing actual source labels. If the
  model returns invalid citations or malformed output, Ask shows genuine saved
  passages with an explanation. Voice passages are joined as readable text.
  Recorded-content questions prefer recent authenticated transcript sources;
  proximity to a browser visit does not establish which video was recorded.
  Browser controls are not substituted for a missing transcript. Ask never
  displays the unverified model prose.
- Safari capture grant renewal and commit-time preference checks are wired.
  The optional signed Safari host includes and verifies the audio library.
- MCP distinguishes a ready local bridge from a configured Claude host and
  provides configuration for the actual running executable.
- Capture substatus is hidden in the menu by default. Holding Command reveals
  it while the menu is open, including permission and read failures.
- Settings > Privacy > Diagnostics saves the runtime log level. It changes the
  existing persistent logger only after a successful preferences write.
- Search operations participate in Client shutdown: model work is canceled and
  joined, and requests made after close are rejected.

## Background work in Settings

Settings > AI starts with live Background work status for Automatic Library,
hourly recaps and note suggestions. It shows known queued jobs, actual worker
attempts, foreground deferral, power limits, completed work and safe error
messages with timestamps. Apple model activity separately distinguishes an
interactive request, automatic inference and retirement before yielding.
These are worker-local counters, not estimated totals or percent complete; they
may reset after AI settings change or Blur restarts. Library counts completed
chunks and only incomplete sources already discovered in its incremental queue,
including the active source. A skipped recap is not described as retained work.

While AI Settings is visible, a one-second timer reads bounded metadata under
existing locks. It does not read source content, scan storage, probe the system
or call a model. Only changed activity updates its own UI composition; the
Settings scroll container and editors are preserved. The timer stops when the
dashboard closes or another page/group is selected. Existing power controls are
labelled Background limits.

Fresh Apple settings enable Automatic Library and hourly recaps by default.
Explicit disabled choices are serialized and shown as Off even if an earlier
worker attempt failed. Existing populated settings keep their saved choices.
A reproduced multi-source Apple recap failure came from valid citations returned
in a different order than the persisted contract required. Per-request source
labels are validated and mapped before canonical sorting; unknown and duplicate
citations remain invalid. Worker failures now expose safe closed error codes.
A refused source was reproduced blocking later Library work. Refusal receipts
now preserve that source's incomplete state while allowing other sources to
continue. The exact content that caused the user's refusal remains unconfirmed.
The [full ingestion status](full-ingest-status.md) records the connected capture,
processing and deletion paths.

## Apple on-device mode

One selected Apple system model supplies request understanding, meaning search,
answers, note suggestions, Library derivation, recaps and voice-note summaries.
These paths do not require Ollama, an embedding server or a cloud account.
Task-list requests, calculations and exact retrieval can run without inference.
Interactive Apple requests take priority over optional background inference.
An interrupted background helper retires before another inference starts;
deferred note, recap and Library jobs retain their identities and recheck saved
sources, policy and deletion fences before retrying. Ordinary provider failures
are not retried by this mechanism. Sustained foreground traffic can still reach
the normal bounded request deadline.
Speech transcription uses Apple's SpeechAnalyzer; visual text recognition uses
Apple Vision. These are separate system capabilities with their own availability
and permission checks. Apple mode does not silently select a remote provider.

Native synthetic model checks on this Mac passed source attribution and unknown
evidence handling. The final packaged helper's three generation steps took
0.780–0.938 seconds; these are
model-step measurements, not end-to-end Ask latency. The helper reported an
available on-device model with an 8,192-token context. Other Macs and OS versions
can report different availability and capacity.

Apple note processing now counts the complete schema and payload against the
actual runtime token capacity. It does not inherit Ollama's byte estimate.
Oversized model requests leave the saved source intact and report the processing
budget. Note proposal extraction does not yet chunk oversized notes; Library
derivation processes observations in resumable overlapping chunks. Source text
or evidence omitted from an answer's model input triggers a coverage notice.

Production Accessibility capture uses bounded document rounds and encrypted
multipart staging, with final focus/privacy validation before publication.
Large exposed sources no longer stop at the former single-observation limit.
Model context and typed-note limits are separate. Content withheld by an app,
including offscreen content unavailable to Accessibility, is not invented;
see [capture coverage](full-ingest-status.md#acquisition-coverage).

## Runtime acceptance and known gaps

Recording holds a scoped macOS user-initiated activity until native pause/stop
cleanup finishes; resume obtains a fresh activity. The analyzer, audio feed and
result consumers also run at user-initiated priority. Idle system sleep remains
allowed. Native queues remain bounded at 128 packets, and failures distinguish
capture versus analyzer overflow from an unexpectedly closed stream.

Generated PCM tests cover ten minutes of virtual cadence, temporary consumer
stalls, sustained deficits, drain order and activity release on success, failure
and cancellation. A separate eight-minute real-time replay passed through the
production SpeechTrack and both transcribers, producing 155 final segments and
1,993 previews without stage errors. It used generated speech and existing
assets, with no device capture or asset installation. The replay test must use
the canonical signing identity and host identifier because Apple's installed
asset reservation is scoped to the host. The user's observed seven-minute
overflow was not proven to be caused by the previous smaller queue or App Nap;
long microphone and system-audio capture still need live confirmation.

The pin icon has a persistent mounted-widget regression: real pointer events
toggle its pixels at both display scales and unpin restores a fresh auto-hide
deadline. Replacing only the draw closure previously left the cached image
unchanged. State changes now explicitly regenerate the small icon.

Typed notes have a multiline Markdown editor, revision conflict handling,
current-version search and saved Markdown export. Deleting a note removes all
revisions without pausing passive capture. Today contains recaps and tasks;
suggested tasks can be dismissed, and proposed Library facts can be confirmed.
Quick history keeps up to twenty entries with stable navigation identities,
including repeated identical requests. Historical Today selections are excluded
from the current-day Quick preview.

Ask omits the duplicate Quick ask/New note actions, routine capture-on message
and Back to results button. The Ask sidebar returns from voice detail and
preserves a suspended Markdown draft. New input cancels an unfinished recap
before request understanding, keeping already valid recap text and rejecting
late receipts. Loading, finishing and download text occupy the live phrase
slot in the voice overlay.

Library voice-source provenance uses authentic, localized event times. Unknown
times show the source kind without an opaque recording key. Time hydration is
bounded and cached per visible page; rendering performs no storage reads.
Tasks places New task beside its filters and omits the Reminder delivery pane.
Per-task scheduling errors and explicit notification permission remain.

Current transcript labels describe the source as Recorded audio or Microphone
audio. They do not claim to identify people. Apple's public Speech API provides
time ranges and transcription confidence, but no speaker identity. A synthetic
comparison of two local diarizers found that both correctly separated the two
voices in the overlap fixture, but both merged Samantha and Moira in the
three-voice fixtures (6/9 clean turns and 12/18 rapid turns). This is not a
real-world accuracy measure. The classic model package is 13.7 MB and used
158–390 MiB peak process RSS across these runs; LS-EEND is 44.7 MB and used
67–126 MiB. LS-EEND ran at 0.35–0.40% real-time after warmup, versus 0.77–0.91%
for classic. On these synthetic clips LS-EEND was faster and used less memory,
but neither model distinguished all three generated voices. Keep the current
honest source labels for now; do not add automatic speaker attribution until
accuracy is measured on representative, consented recordings. Existing
recordings have no retained audio for later voice analysis.

A follow-on offline spike evaluated a separate [Qwen3 ECAPA-TDNN ONNX speaker
encoder](https://huggingface.co/marksverdhei/Qwen3-Voice-Embedding-12Hz-0.6B-onnx).
The 17 MB fp16 model ran on this Mac's CPU. On 30 non-overlapping, two-second
windows from three macOS-generated voices, a simple online centroid clusterer
at cosine threshold 0.96 produced exactly three pure clusters (9, 11 and 10
windows). Same-voice pair similarity had median 0.987; cross-voice pairs had
median 0.935. Warm inference took a median 6.09 ms per window; Python/librosa
feature extraction took 22.19 ms. Loading the model raised this Python process
from 30.7 MiB to 77.5 MiB RSS before feature extraction. The 8.9 MB int8 export
failed to initialize in ONNX Runtime on this Mac because its CPU provider lacks
`ConvInteger`; the 34 MB fp32 export worked and gave similar synthetic results.

This encoder provides embeddings, not speaker-turn detection. The spike used
known, fixed two-second windows, synthetic voices, no overlap, and no human
recordings; it cannot establish diarization accuracy or names in real meetings.
It therefore does not change the transcript labels. Product use still needs a
native, low-memory inference path, speech/turn segmentation, overlap handling,
and evaluation on representative consented audio before names or speaker IDs
are shown. The model card lists Apache-2.0 and describes the fp16 asset as 18.2
MB; local file sizes above use rounded binary units.

Request understanding carries memory/general scope in its first Apple
classification, avoiding a second generation for questions. Proven local notes
and task titles use their existing exact-input authority checks without
inference. Three native warm general-query trials improved from a median
2.213 seconds/two generations to 1.037 seconds/one generation. These measure
understanding alone under changing system load; the first after-change trial
still took 5.304 seconds. They do not establish whole-Ask latency.

A deterministic controller race was reproduced: memory invalidation could clear
the preview map between an admitted state event and a queued preview, causing a
nil-map panic. The guarded publication fix passes repeated race regressions.
The user's supplied startup log identified a separate whole-app exit: the Voice
window changed its creation-only Size when its content changed. The declaration
now keeps a constant initial size and resizes the mounted native window. The
regression compares the exact options field checked by GoUI and reproduced the
reported error before the fix. Another regression covers a failed native Stop
followed by a new recording; old Stop dispatch flags no longer suppress the
next Stop. No elevated-permission workaround was used.

Synthetic tests cover the native ABI, generated PCM, event queues, controller
lifecycle, encrypted-store reopen and erasure, source provenance, notification
content, model-policy cancellation and menu icon pixels. They do not replace
live microphone/system-audio, language installation, permission-dialog or
window-focus acceptance. No app launch/restart or real recording is performed
as part of building the canonical bundle.

The OCR bridge is packaged as a signed native library and attached as a fallback
when a verified foreground window exposes no useful Accessibility text. It uses
an existing Screen Recording grant and never prompts in the background. Private,
excluded, sensitive, structurally incomplete, stale or obscured contexts remain
unavailable. No screenshot files are retained. Live support for every app is not
established; protected or unverifiable windows can remain unreadable.
Standalone dictation into other apps, Copilot authentication and automatic cloud
model discovery are not complete. Claude currently uses an API key, while
ChatGPT has the implemented account flow. The Foundation Models answer provider
and Apple's SpeechAnalyzer transcription are separate system capabilities.

## Earlier voice-build verification

The current bundle and full-ingestion verification are recorded in
[macOS capture and memory processing](full-ingest-status.md#packaged-build-2026-11-14).

The 2026-11-13 canonical build was copied to `~/Downloads/Blur.app` with bundle
identifier `com.hazcod.blur`. The existing certificate and designated signing
requirements were preserved for the host, Foundation Models helper and audio
library. The new ScreenText library uses the same exact certificate. All four
contain arm64 and x86_64 code, retain Hardened Runtime, and pass strict signature
verification with library validation enabled. The full build with warm native
caches took 5.91 seconds on this Mac.

Verification passed on the settled source: the whole Go suite, the full client
and Library suites again after the final telemetry changes, repeated affected
race regressions, lint with zero issues, vet, cross-build/test compilation, the
no-cgo audit, 11 native-cache tests and 17 release-packaging Python tests.
A temporary test executable signed with the same certificate and Hardened
Runtime successfully loaded both final packaged native libraries and rejected
invalid ABI requests. The final packaged Apple helper passed synthetic source
attribution and unknown-evidence checks. These checks did not launch the host
app, probe devices, access real source content, capture pixels or record audio.

The 2026-11-14 canonical bundle includes the follow-up voice and Ask changes.
Its main executable SHA-256 is
`df055200cb12c5725fd9de427c80b51622d3fcf71dd7bf976dc0178c3947b5f6`.
The full Go suite, lint, vet, cross-build and packaged native helper checks pass.
The desktop-control host was unavailable for a final visual launch check.
