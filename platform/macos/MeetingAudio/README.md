# Meeting audio bridge

This library powers the macOS **Voice notes** controls. The app supplies an
explicit Voice notes action, permission and language-installation handling, a compact
overlay, and encrypted final-transcript storage. Personal notes work without
starting audio. Internal package and ABI names retain `meeting` for compatibility.

Boundary, lifecycle and clock checks use synthetic audio and injected drivers.
They do not establish real-device, installed-speech-model or permission-flow
acceptance. See [delivery status](../../../docs/voice-notes-status.md).

## Platform and policy

- The library targets macOS 13. Speech transcription requires macOS 26 and an
  already-installed exact requested locale. New Speech symbols are weak linked.
- The read-only probe checks public microphone authorization status,
  `CGPreflightScreenCaptureAccess`, `SpeechTranscriber.isAvailable` and installed
  locales. It does not construct an analyzer/module, reserve or install assets,
  request a permission, start a device or run inference.
- Start is explicit and names one or both tracks: `microphone`, `system`.
  Every requested track must be available. Failure never silently removes a track.
- Microphone-only uses AVAudioEngine without voice processing or playback.
  System or dual-track capture uses ScreenCaptureKit, registering only requested
  `.audio` and `.microphone` outputs; it never registers screen/video output or
  accesses pixels. System audio excludes Blur's own process. The selected default
  microphone is fixed at start. Device/format failure ends the epoch visibly.
- ScreenCaptureKit requires an existing Screen Recording grant. That permission
  is broader than this bridge's audio consumers. Audio-only runtime operation
  remains an acceptance item; the source does not prove what the OS does internally.
- Probe and Start never request permissions or install/reserve assets. Explicit
  `RequestAccess` uses public microphone/Screen Recording permission APIs;
  explicit `InstallLocale` uses Apple's asset installation API. No private TCC
  API, recording file, audio log or audio upload exists. Apple SpeechAnalyzer
  runs on device; this code does not use SFSpeechRecognizer.
- Both tracks use one session-wide host-clock origin. SCK presentation timestamps
  convert from its synchronization clock to the host clock; microphone host times
  use the same clock. Resume retains that origin and increments the epoch.

## Crucial result contract

Apple documents that volatile results may be finalized without being emitted
again with `isFinal == true`. Waiting for that transition can lose durable words.
For this reason each requested track uses **two** SpeechTranscriber modules in
one SpeechAnalyzer with a shared input:

1. A volatile module emits `kind: "preview"`, `final: false`. One preview identity
   (`<track>-<epoch>-preview`) has increasing revisions. Its text and both range
   boundaries may change. Even a final result from this module remains a preview.
2. A module without volatile reporting emits `kind: "transcript"`, `final: true`.
   Each event has an independent immutable ID
   (`<track>-<epoch>-final-<sequence>`) and revision 1. An unexpected nonfinal result
   from this module is a visible transcription failure.

Preview IDs do not match final IDs. A preview is never a durable source. The first
engine integration should consume only `transcript` events. Preview UI needs its
own replacement/clearing behavior and live producer acceptance; consumers must
not assume preview start is fixed, preview end only grows, or final delivery is
synchronized with the preview stream. Ranges come from Apple unchanged apart from
rounding to the ABI's millisecond precision; no invented boundary normalization
is used to satisfy a downstream engine.

There are at most two analyzers, four modules, four result tasks and two feeder
tasks per physical session. Shared input/analyzer is supported by the public API;
shared inference cost or negligible overhead has **not** been established. If the
runtime rejects the two-module setup, start fails visibly. There is no fallback
that drops final-only processing.

## C ABI

```c
uint64_t blur_meeting_audio_open(const uint8_t *json, int32_t length);
int32_t  blur_meeting_audio_poll(uint64_t handle, uint8_t *out, int32_t capacity);
int32_t  blur_meeting_audio_control(uint64_t handle,
                                  const uint8_t *json, int32_t length);
void     blur_meeting_audio_cancel(uint64_t handle);
```

Open accepts at most 4 KiB of strict source-free JSON:

```json
{"version":1,"kind":"start","session_id":"meeting-1","locale":"en-US","tracks":["microphone","system"],"explicit_start":true,"startup_ms":8000}
```

A probe uses `kind: "probe"`, with neither `tracks` nor `explicit_start`. It emits
one availability batch. IDs are at most 64 ASCII letters/digits/underscore/hyphen;
locales are at most 64 ASCII letters/digits/hyphen. Unknown fields are rejected.
Open returns zero on admission failure. At most one physical meeting session,
two probes and four public handles may exist. Duplicate session IDs are rejected.

Setup requests use `kind: "request_access"` with explicit tracks or
`kind: "install_locale"` with a locale and no tracks. Both require the JSON boolean
`explicit_setup: true` and reject `explicit_start`/`startup_ms`. They emit one
`kind: "operation"`, epoch-zero event with state `completed`, or `failed` and a
closed error code. Only one setup operation may be outstanding; cancellation
keeps that slot occupied until actual native work exits. A five-minute timeout
closes result admission, cancels the task and reports `setup_timeout`; it cannot
dismiss an OS permission dialog or promise to stop an OS-managed asset download.
Completed operation handles should be canceled to discard their queue/release the
public handle (the Go methods do this automatically).

The explicit download action must disclose that Apple may reserve the locale and
retry a download later after a failed attempt. This is documented OS behavior;
canceling Blur's wait does not revoke the user's earlier download consent.

Poll returns zero while pending, -1 for an invalid handle/request, or the JSON
length required for the next batch. A nil output/capacity zero stages that batch
immutably. A sufficient output buffer copies and consumes it; a small buffer does
not consume it. Batches have a 1 MiB cap and at most 16 events:

```json
{"version":1,"session_id":"meeting-1","events":[{"version":1,"session_id":"meeting-1","epoch":1,"kind":"state","state":"running"}]}
```

Controls are at most 512 bytes, with exactly `version`, `session_id`, `epoch` and
`operation` (`pause`, `resume`, `stop`). The epoch is an exact UInt64, not a float.
Return 1 means admitted, 0 means state/epoch rejected, -1 means invalid input.
Admission does not confirm that devices have stopped: consume the acknowledged
`paused` or `completed` state. Start similarly returns a `starting` handle;
recording is confirmed only by `running`.

Pause invalidates the old epoch immediately and rejects its later callbacks.
Stop seals input immediately, allows accepted final output to drain, and emits
`completed` only after actual device/analyzer stop. An eight-second deadline emits
`stop_timeout`; it does not release the physical session slot while work remains.
Cancel invalidates the public handle immediately and discards its unread events,
but also keeps the slot reserved until actual cleanup exits. Failed actual stop
can deliberately leave the slot occupied rather than multiply orphan devices.

## Bounds and transport

- Transcript/preview text: 16 KiB per event. Timestamps use nonnegative
  milliseconds convertible to a signed nanosecond duration; there is no
  hour-based session cutoff. Sample-clock conversions and additions are checked.
- Queued events: 128 and 512 KiB of encoded data, including a staged batch.
- Preview updates replace unstaged revisions. Queue exhaustion preserves accepted
  finals and emits a separate emergency `event_overflow` error; it cannot prevent
  an explicit stop from stopping devices.
- Each track has a 128-packet owned PCM queue and a 128-input analyzer queue,
  allowing about 2.7 seconds of scheduling delay at 48 kHz/1024-frame capture.
  Source packets are at most 8192 frames, eight channels, 8–192 kHz.
  Conversion buffers are at most 65536 frames. Overflow fails visibly.
- PCM stays in memory. Framework-internal model memory and performance still
  require measurement; these application bounds are not a total RAM estimate.
- Errors are closed source-free codes, not localized backend descriptions.

The Go wrapper is in `internal/platform/darwin/meeting_audio*.go`:

```go
bridge, err := darwin.OpenMeetingAudioBridge(absoluteBundledLibraryPath)
availability, err := bridge.Probe(ctx, "en-US") // read-only; do not call in synthetic tests
err = bridge.RequestAccess(ctx, []string{"microphone", "system"}) // explicit user action only
err = bridge.InstallLocale(ctx, "en-US") // separate explicit download action only
session, err := bridge.Start(ctx, darwin.MeetingAudioStart{ /* explicit tracks */ })
events, err := session.Poll(ctx)
err = session.Control("stop", expectedEpoch)
```

Cancelling a Poll context stops waiting, not recording. If cancellation follows
native consumption, one bounded pending batch is retained for the next Poll so
accepted finals are not lost. `Close` explicitly cancels and discards unread
events. The dylib stays loaded because callbacks may still be unwinding. Normal
packaging must sign it with Blur's exact existing certificate so Hardened Runtime
library validation remains enabled.

## Verification and remaining acceptance

`test.sh` compiles the real native source, exercises only fake devices and
generated silence, then loads the compiled C ABI through purego using only invalid
requests/handles. It never invokes a live availability probe, model or device.
It covers strict schema, separate preview/final identities, moving raw ranges,
queue coalescing/overflow, pause epochs, final drain, late callbacks, permission
failures, device changes, cancellation slot ownership, the actual eight-second
deadline, generated PCM bounds, sample clocks, shared track origins, and exact
UInt64 transport. `build.sh OUTPUT.dylib` builds arm64+x86_64 at macOS 13 deployment
with Swift 6 and warnings as errors; it does not sign or modify an app.

Implemented in the app:

- signed app packaging, explicit controls and visible requested-track status;
- permission UI and installed-locale/asset setup policy;
- encrypted durable engine integration, final-only indexing and source deletion;
- separate typed notes, saved-history pagination, and model-backed page summaries.

Still required before claiming live recording is verified:

- real dual-source/audio-only SCK behavior and selected-device changes;
- actual SpeechAnalyzer two-module compatibility, resource cost, preview/final
  ranges and final drain under real speech and interruptions;
- macOS 13/older OS weak-link load acceptance and macOS 26 transcription hardware.

## Sources and related research

- [Apple: result finality](https://developer.apple.com/documentation/speech/speechmoduleresult/isfinal)
- [Apple: finalization watermark and companion final-only module](https://developer.apple.com/documentation/speech/speechmoduleresult/resultsfinalizationtime)
- [Apple: analyzer module list](https://developer.apple.com/documentation/speech/speechanalyzer/init(modules:options:))
- [Apple: SCK microphone output](https://developer.apple.com/documentation/screencapturekit/scstreamoutputtype/microphone)
- [Apple: SCK synchronization clock](https://developer.apple.com/documentation/screencapturekit/scstream/synchronizationclock)
- [Apple: speech permission distinction](https://developer.apple.com/documentation/speech/asking-permission-to-use-speech-recognition)
- [Apple: asset status](https://developer.apple.com/documentation/speech/assetinventory/status)
- [Apple: explicit asset request and reservation](https://developer.apple.com/documentation/speech/assetinventory/assetinstallationrequest(supporting:))
- [Apple: download attempts and later system retries](https://developer.apple.com/documentation/speech/assetinstallationrequest/downloadandinstall())
- [Apple: public screen permission request](https://developer.apple.com/documentation/coregraphics/cgrequestscreencaptureaccess())
- [Apple: audio-input Hardened Runtime resource access](https://developer.apple.com/documentation/security/hardened-runtime)
- [Apple: system-audio usage description](https://developer.apple.com/documentation/bundleresources/information-property-list/nsaudiocaptureusagedescription)
- [Yogurt](https://github.com/FrigadeHQ/yogurt), MIT, reviewed at
  `41c936ae83697b2894f52746dc9c24cb16bab474`: separate tracks, on-device live
  SpeechAnalyzer, no voice processing, sample clocks and explicit finalization.
  No Yogurt code or its private TCC calls were copied. Blur uses public SCK audio
  instead of Yogurt's CoreAudio process-tap/aggregate-device implementation.
- [Notion's public meeting-notes flow](https://www.notion.com/help/ai-meeting-notes)
  informed explicit controls and clear recording state; its internals are not
  inferred from the public UI.

## Live audio levels

A content-free `level` event carries only version/session/epoch, track and a
finite `level` number in `[0,1]`. The value is linear RMS from the current PCM
buffer, after normalized amplitude clipping; silence is zero. It is not speech
recognition, a speaking-person classifier, or synthetic animation. Float32,
Float64, Int16 and Int32 native-endian planar/interleaved buffers are supported,
bounded to 8,192 frames and eight channels. Unsupported/nonfinite samples omit
the meter update without failing transcription. The meter retains no PCM.

Each track emits at most once per 100ms. At most one pending value per track is
coalesced in a separate queue lane. Meter values do not consume the 128 reliable
transcript/state slots or their byte budget, including while a meter batch is
staged for the ABI size/read handshake. Reliable events have poll priority;
levels are expendable and never evict or fail a final. Pending levels are
cleared on pause, stop, failure and cancellation. Already-staged ABI batches
retain their immutable size/read contract; consumers discard obsolete epochs
and clear displayed activity on pausing/paused/stopping/completed/error states.

Synthetic tests exercise real generated PCM RMS, finite bounds, the ten-hertz
per-track throttle, 10,000 coalesced meter updates alongside 128 finals, staged
queue capacity, and pause/stop late-callback rejection. They access no device,
permission prompt, real microphone/system stream, model or audio file.
