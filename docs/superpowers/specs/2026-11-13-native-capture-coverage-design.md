# Native capture coverage from the Goldfish inspection

The user explicitly asked to continue fixing app capture and to reverse engineer
the installed Goldfish capture path. The installed 0.1.69 daemon's arm64 call
paths establish a generic AX reader, structured WebArea reader, attributed text,
visible ranges, and a text-marker fallback. Its capture walkers accept editable
text and exclude secure-field roles/subroles separately. Blur currently confuses
editability with secrecy, and accepts only CFString AXValue values. This loses
editor content and text exposed through other native Accessibility interfaces.

## Decisions

Use the same native reading strategies through Apple's Accessibility API in
Blur's own implementation. Capturing an app includes its visible edited text;
secure/password fields, private browsers, Blur itself and excluded apps/sites
remain excluded. The user has handed implementation decisions over and asked
for continued work; no new settings interview is needed.

Prefer AXValue, accepting CFString and CFAttributedString. If absent, read a
validated visible character range using AXStringForRange. Bound each request
and returned UTF-8 text, preserve Unicode, and report partial coverage. Keep
the existing source/window/domain checks, deadlines, streaming privacy work,
encrypted admission and deletion fences. Recheck sensitivity around deferred
reads. Do not turn a malformed or stale range into an unbounded native read.

WebArea marker reads must not bypass secure-field exclusion. The Goldfish bulk
helper does not itself prove that property; preserve Blur's structured path
until an input-safe marker contract is demonstrated. OCR is a separate fallback
for surfaces lacking useful AX text and is deferred while the native omissions
are fixed and measured. This avoids an additional screen permission as a
prerequisite for ordinary terminal/editor/browser text.

Read-only binary inspection is evidence of Goldfish's implementation, not
native acceptance of Blur or proof that every app works. The live CUA service
currently cannot start. Record that limitation and make capture failure states
honest. Keep the same signed Downloads bundle when the complete usable
capture/recall build is ready.

## Approved sandbox correction (2026-11-13)

Apple documents cross-app assistive Accessibility as incompatible with App
Sandbox, including the exact Terminal/trusted-permission/CannotComplete case.
The installed Goldfish main app and daemon do not enable App Sandbox. After
reviewing that evidence the user explicitly approved removing it from Blur.
Normal direct-download and release builds, including their Foundation Models
helper, must therefore be unsandboxed. Retain explicit sandbox build opt-in
for compatibility work without claiming its AX capture works.

An unsandboxed startup must keep using an existing com.hazcod.blur container
data directory in place. Prefer that directory over legacy normal-home data;
do not choose by mtime, move/copy stores, or reset preferences/keys. If the
container cannot be inspected or is not a real directory, fail closed rather
than creating a second empty store. Fresh installs with no container continue
using normal Application Support. Desktop and MCP must share the resolver.
