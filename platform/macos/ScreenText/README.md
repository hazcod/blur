# Local visual text bridge

This native bridge uses public ScreenCaptureKit and Vision APIs. The macOS
capture controller loads the bundled reader lazily for eligible verified windows
with empty Accessibility text. It never requests permission or replaces readable
Accessibility capture. The host enforces AX window/privacy proof, app/domain
scope and the existing Screen Recording grant before recognizing a real window.
See `internal/capture/README.md` for the actual host coverage and limitations.

## Build and synthetic verification

```sh
platform/macos/ScreenText/build.sh /tmp/libBlurScreenText.dylib
platform/macos/ScreenText/test.sh
```

The build produces an unsigned universal dylib targeting macOS 13. The normal
Blur bundle build must place it in `Contents/Frameworks/libBlurScreenText.dylib`
and sign it with Blur's existing exact certificate before signing the app.
There is no alternate app bundle or app launch in either script.

The test script generates text into an in-memory image and runs actual Vision
OCR. It injects synthetic window metadata and image acquisition into the real
engine to exercise permission, target, visibility, deadline and cancellation
checks. It also loads the dylib through the real Go/purego loader and sends only
malformed source-free requests, which are rejected before any desktop API.
Tests never query actual windows, capture the screen, prompt for permission,
or read application/user memory.

## C ABI

```c
uint64_t blur_screen_text_start(const uint8_t *json, int32_t length);
int32_t blur_screen_text_poll(uint64_t handle, uint8_t *output, int32_t capacity);
void blur_screen_text_cancel(uint64_t handle);
```

`start` accepts at most 4 KiB of UTF-8 JSON and returns zero for invalid/busy
requests. `poll` returns zero while pending, -1 for invalid handles, or the exact
reply byte count. A null/too-small output buffer leaves the reply available;
a sufficiently large non-null buffer consumes it. Replies are at most 2 MiB.
Cancellation invalidates the handle immediately. Native callbacks may finish
later, but cannot publish a cancelled/expired result. Do not unload the dylib:
its framework callbacks can outlive a Go wrapper's `Close` operation.

Request schema (no file paths or source text):

```json
{"version":1,"kind":"topmost","request_id":"unique-id","timeout_ms":1000}
```

```json
{"version":1,"kind":"recognize","request_id":"unique-id","pid":123,"window_id":456,"lease_token":"64-lowercase-hex-characters","max_text_bytes":262144,"timeout_ms":8000}
```

Recognition optionally accepts `"region":{"x":20,"y":100,"width":780,"height":500}`.
These are global AX/CG top-left logical points, wholly inside the bound window.
The host supplies a verified document viewport; omission preserves exact-window
capture. The reply must echo the exact region, including on failure. Unknown
fields, nonfinite numbers, outside-window rectangles and region/window changes
are rejected. Region coordinates are never inferred from OCR text.

Topmost metadata is a **candidate on-screen window**, not an AX-focused window.
It includes PID, window ID, bundle ID, bounded title and availability/truncation,
frame, full-on-screen/unobscured state, permission preflight and an opaque lease.
The lease binds all these properties. Pass it unchanged to recognition; apply
privacy policy before that call. Recognition revalidates it before native
capture, after obtaining pixels, and after OCR. Unknown private/browser/domain
state must not be treated as safe merely because pixels are available.

Successful recognition replies bind the request ID/PID/window ID and contain
UTC Unix milliseconds at frame acquisition, the window lease, bounded text,
at most 512 recognized lines with confidence and normalized Vision rectangles
(origin bottom-left), and a truncation flag. Error replies contain only closed
status codes, never framework errors, paths or pixels. The Go decoder verifies
binding, bounds, status and text/line consistency.

## Runtime limits and privacy

- Screen Recording is preflighted without prompting. No capture is attempted
  without an existing grant. Metadata without permission omits titles.
- Only the requested frontmost app's topmost normal window is eligible. It must
  fit fully on one active display and have no overlapping window above it.
  Spanning displays, occlusion and uncertain native metadata fail closed.
- Without a region, a desktop-independent **exact-window** filter excludes other
  app/window pixels. With a region, an exact-window-included **display** filter
  uses `sourceRect` before acquisition: independent-window filters ignore that
  property. Global top-left points are translated to the bound display's local
  points without a Y flip or source scaling. Retina scale changes only bounded
  output dimensions. The display identity/frame/scale must remain unchanged;
  no whole-display fallback or post-acquisition crop exists. Off-screen/occluded
  content is rejected in both modes. Audio, cursor, child windows and menu-bar
  inclusion are disabled where the public SDK exposes those controls.
- macOS 14+ uses a single screenshot; macOS 13 stops a stream after one complete
  frame. Each image is at most 4096 by 4096 and 8,388,608 total pixels. Frames
  remain native and in memory; no image file, recording or pixel IPC exists.
- Vision runs locally with language correction disabled to preserve technical
  text. OCR may misread or omit content; confidence and partial coverage remain
  explicit. It has no secure-field, password, private-window, URL or DOM semantics.
- Two registry entries, two outstanding framework callbacks, one active stream
  and one active OCR request are allowed. Expired/unconsumed entries are reclaimed after 30 seconds.
  Timeout is 100–8000 ms; Go also enforces context cancellation and a backstop.
  Cancellation stops publication immediately, invokes Vision cancellation and
  requests stream shutdown. Late callbacks keep their native capacity reservation
  until they finish. A stream shutdown failure consumes its slot for process
  lifetime to prevent retries multiplying a stream that did not stop.
- The Go host still owns app/site exclusions, private-window rules, secure-input
  policy, redaction, encrypted persistence, retention and capture-epoch checks.
  This source does not enable a provider, change entitlements, or restore the
  host App Sandbox. ScreenCaptureKit itself supports sandboxed apps; cross-app
  AX requires a different architecture.

Primary API references:

- [ScreenCaptureKit sample](https://developer.apple.com/documentation/screencapturekit/capturing-screen-content-in-macos.md)
- [SCStream, macOS 12.3+](https://developer.apple.com/documentation/screencapturekit/scstream.md)
- [SCScreenshotManager, macOS 14+](https://developer.apple.com/documentation/screencapturekit/scscreenshotmanager.md)
- [Display filter including exact windows](https://developer.apple.com/documentation/screencapturekit/sccontentfilter/init(display:including:))
- [SCStreamConfiguration sourceRect](https://developer.apple.com/documentation/screencapturekit/scstreamconfiguration/sourcerect.md)
- [Vision text recognition](https://developer.apple.com/documentation/vision/vnrecognizetextrequest.md)
