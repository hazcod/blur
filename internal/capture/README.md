# macOS capture coverage

Capture uses generic Accessibility (AX) traversal for eligible foreground apps.
It does not need a model or an app-specific integration for ordinary exposed text.
The optional Safari page bridge supplies separately authorized structured browser
content. All accepted text passes the shared redactor and encrypted store.

## Local visible-text fallback

`NewDarwinPipelineWithVisualText` adds the bundled Apple Vision/ScreenCaptureKit
reader when a complete, privacy-validated AX structure contains no useful text,
or a deferred text/range value read fails. Readable AX text always wins; OCR
never replaces it or expands a partially read AX tree.
There is no cloud/Ollama call, provider selector, saved screenshot, or image IPC.
Only sanitized text can leave the capture package.

Admission requires all of these:

- An existing Accessibility grant, valid foreground target lease and focused AX
  window. No visual fallback for AX connection/window/structure failures.
- No excluded app/site, private/elevated context, secure AX role/subrole, unknown
  privacy metadata, truncated structural branch, or active secure event input.
- A fresh complete AX privacy walk before and after OCR, without reading text
  values. The native CG candidate must match AX PID, bundle, title and geometry.
  Fresh focus checks and a bounded public AXWindows list require that focused
  proxy to be present and no other same-geometry window to exist (including
  hidden/minimized windows). Missing/ambiguous metadata or more than 128 windows
  fails closed.
- Browser OCR requires a known domain and the same retained AX WebArea as text
  capture. Fresh ancestor geometry bounds the visible document viewport. A
  separate complete metadata walk proves visible chrome descendants do not
  overlap it, including descendants extending outside toolbar bounds. Unknown,
  incomplete, ambiguous or whole-window regions fail closed. The native display
  filter includes only the exact window and crops that region before sampling;
  tab titles and address-bar pixels are outside its authority.
- An existing macOS Screen Recording grant. Runtime capture never prompts for it.
- A fully visible, unobscured foreground window on one display. Large, occluded,
  spanning, off-screen or changed windows are skipped rather than broadened.
- Latest preference checks before reading/publication, plus a mutex-protected
  latest-policy check across final pixel acquisition and each encrypted
  observation, visit and activity write. Pixel admission follows fresh AX proof;
  a preference save may wait up to the three-second visual budget. Pause cancels
  the original context independently of this mutex.
  A successfully saved exclusion cannot be bypassed by an old admitted frame.

The ordinary AX budget remains 750 ms, 2048 nodes, depth 32 and 256 KiB of text.
The visual stage has a separate **at most 3 s** deadline derived from the original
caller context. Cancellation is retained throughout. Native images are bounded
at 4096×4096 and 8,388,608 pixels; recognition yields at most 512 lines and 256 KiB.
Capacity and cancellation suppress late native results. OCR is always marked
partial: it covers visible content and may omit/misread characters. It does not
scroll a document or promise a complete page transcript.

`screen_permission_required`, `screen_text_unavailable`, and `screen_not_visible`
are recoverable closed capture errors. Health and Command-menu detail distinguish
Screen Recording permission from Accessibility and empty text. Source/policy
changes produce `excluded`; cancellation and deadlines retain their existing
codes. These statuses contain no app text, titles or backend exception messages.

## What the checks establish

| App family | Implemented backend | Evidence and limits |
| --- | --- | --- |
| Finder, Terminal, editors | Generic AX; eligible empty AX window may use local OCR | Synthetic generic pipeline tests; app-specific live acceptance remains required. |
| Slack, Outlook, WhatsApp, Signal, Teams | Generic AX; same guarded OCR fallback | No per-app model/integration needed; current app versions and unusual windows are not proven by source tests. |
| Herdr and canvas/custom-rendered apps | OCR only after trustworthy AX window/privacy metadata | A successful opaque AX canvas can qualify. Unavailable or unverified AX metadata remains unsupported. |
| Orion, Safari, Chrome and supported browsers | Verified AX page, optional Safari bridge, or guarded document-viewport OCR | Private-window/domain/active-page and chrome exclusion fences stay mandatory. OCR sees only the visible verified viewport; unknown geometry or privacy state remains unavailable. |
| Passwords, private/protected/system UI, excluded apps/sites | No fallback | Intentionally rejected; Screen Recording is not authority to bypass privacy or OS protection. |

`visual_fallback_test.go` covers readable AX precedence across app IDs and
exclusions, incomplete/sensitive frames, cancellation, stale focus/policy, byte
limits and separate stage deadlines. `visual_darwin_test.go` exercises actual
native AX-binding code with synthetic CF metadata, without querying user windows.
Client tests cover preference/write serialization, encrypted reopen, immediate
searchability and late policy rejection. `platform/macos/ScreenText/test.sh`
runs real Vision recognition on an in-memory synthetic image plus synthetic
capture/permission/visibility/cancellation and Go ABI cases. Browser region tests
exercise tall-page clipping, negative display origins, 1x/2x output sizing,
overlapping chrome descendants, stale page/domain/private metadata, exact crop
echo, denied admission, and the real native AX-proof-to-pipeline path with fake
cross-process AX calls.

These checks prove backend contracts, not that every installed app currently
exposes safe metadata or readable text. No live app, screenshot, permission
change, user store or on-device model was used for this implementation's tests.
