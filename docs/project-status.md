# Project status

Detailed development status, security model, platform notes and packaging detail for Blur, moved here from the README. For the product overview, see the [README](../README.md).


Blur is a local-first memory and writing assistant in development. This
repository contains a Go desktop shell, encrypted local durable storage,
bounded capture/redaction pipelines, native macOS accessibility capture,
Windows UI Automation capture, and Linux AT-SPI capture for Linux amd64 and
arm64 desktops. Windows capture is wired to explicit start/pause but still
needs live Windows runtime and fixture-app validation. The Safari extension
builds as an unsigned universal `.appex`;
the repository now includes a profile-checked signing path for a configured
host app and extension. Registered App Group/Keychain identifiers, developer
provisioning profiles, notarization, and live Safari verification remain
release gates. Linux AT-SPI still needs live-desktop validation.
Treat this as development software, not a security-reviewed personal-data
product.

The implementation status is described in [architecture](architecture.md)
and [threat model](threat-model.md). The product direction is in the
[design](superpowers/specs/2026-11-04-blur-design.md); it includes proposed
capabilities that are not shipped here.

## Why not just let an AI read everything?

Blur is designed as the alternative to handing a model all of your data. Nothing leaves your Mac unless you turn on a model, and that model is a local Ollama model or an endpoint you configure. Questions are read by a deterministic local planner, not a model, so time questions and "what did you capture from WhatsApp" are answered with no model at all. When a model is used it receives only a handful of selected, size-limited items with their time and app, and Blur shows how many items were sent for each answer. Recaps are built from short hourly summaries Blur already wrote, and you decide what is captured, how long it is kept, and whether to delete all of it.

## Build and run

Requires Go 1.27.3 (module language version 1.27). Dependencies are listed in
`go.mod`; shipped targets are checked with cgo disabled.

```sh
go test ./...
go test -race ./...
go vet ./...
make build
make cross-build
make cross-test-compile
make no-cgo-audit
make macos-app
make safari-extension-test
make run-macos
go run ./cmd/blurctl status
go run ./cmd/blurctl capabilities
go run ./cmd/blur-core
# Read-only, synthetic fixture commands only:
go run ./cmd/blurctl fixture doctor --fixture --store "$TMPDIR/blur-fixture-demo.db"
go run ./cmd/blurctl fixture today --fixture --store "$TMPDIR/blur-fixture-demo.db"
go run ./cmd/blurctl fixture knowledge --fixture --store "$TMPDIR/blur-fixture-demo.db"
```

### Sync across Macs

Open Privacy → Sync across Macs, choose a folder that iCloud Drive (or any
file-sync service) carries, and set one passphrase on every Mac. Blur then
writes sealed, append-only change files (AES-256-GCM, key derived from your
passphrase with PBKDF2) into its own subfolder every 10 minutes and at pause or
quit, and imports the files your other Macs write. Captured observations, saved
notes, and deletions sync; deletions replay on the other Macs. Activity and
time recaps stay on each Mac in phase one, and derived views (Today,
knowledge, chronicles) rebuild locally from the imported observations. The
sync provider sees file sizes, timing, and the device count, and deleting a
file on the provider does not guarantee erasure there.

### Optional Claude MCP integration

Open Privacy → Claude integration and explicitly allow Claude access. This
starts a current-user local endpoint (AF_UNIX on macOS/Linux, a named pipe with
a current-SID-only DACL on Windows) and stores its expiring, read-only grant in
the OS secret store. It is off by default. The only tool is
`search_memory`; Blur does not expose mutation tools or open the database in the
Claude-launched process. Add this stdio server to Claude's MCP configuration
(move `Blur.app` to `/Applications` first on macOS; on Windows use the packaged
`blur-mcp.exe`, and on Linux use the packaged `blur` executable):

```json
{
  "mcpServers": {
    "blur": {
      "command": "/Applications/Blur.app/Contents/MacOS/blur",
      "args": ["mcp"]
    }
  }
}
```

Blur bounds each request to eight results, 2 KiB of text per result, and a 16
KiB response. Search is local and exact. Results are returned to Claude, so a
hosted Claude model may receive them under that provider's policies; Blur does
not send MCP results to a provider itself. Turning the setting off revokes the
grant, stops the socket, and removes the Keychain entry. Blur must be running
for searches to work.

`blurctl` accepts only `status` and `capabilities`. Both commands start an
in-process ephemeral demonstration service; they do not contact a daemon or
open a local endpoint. `blur-core` prints the same one-shot development status.
Expect `mode: ephemeral_memory`, `encrypted_at_rest: false`,
`ai_enabled: false`, and `network_listening: false`. Memory exists only for the
process lifetime and is not suitable for real personal data.

The separate `blurctl fixture` commands open an existing store read-only with a
fixed, publicly known fixture key. They are only for synthetic offline test
fixtures, require the explicit `--fixture` marker and a
`blur-fixture-*.db` file under the operating system temporary directory, and
cannot open a store protected by an OS secret adapter.
They never accept keys through arguments, environment variables, or config.
`doctor` reports counters and fixed capability states; `today` and `knowledge`
print bounded fixture content. Do not place personal data in a fixture store.
The macOS desktop bundle is written to the stable, visible path
`~/Downloads/Blur.app` so it can be selected in macOS Accessibility settings.
`make macos-app` builds a universal arm64 + x86_64 executable with cgo disabled
and packages it into the same bundle.
Set `MACOS_APP` and `MACOS_BINARY` to choose explicit output paths. Rebuilding
replaces that generated bundle only after a staged build passes signature
verification. Finder-launched macOS builds write process and runtime
diagnostics to `~/Library/Logs/Blur/blur.log` (rotated around 4 MiB with one
backup; runtime stderr goes directly to the file so fatal diagnostics are not
left in a Go pipe) and a bounded startup trace to
`~/Library/Logs/Blur/startup.log`. If that directory is unavailable, both
use the per-user cache directory at
`~/Library/Caches/Blur/Logs` instead. Directories and log files are
user-private. Diagnostics are intended for troubleshooting, not as a memory
export, but should still be treated as private files.
App Sandbox (default): `make macos-app` signs the bundle with
`platform/macos/Blur.sandbox.entitlements` (app-sandbox, outbound network
client, user-selected read/write for the sync folder picker). Use
`make macos-app MACOS_SANDBOX=0` for an unsandboxed build (data then lives in
`~/Library/Application Support/Blur`). `make macos-app-sandbox-check` verifies
the built bundle carries the sandbox entitlement. When `BLUR_APP_GROUP_ID` /
`BLUR_KEYCHAIN_ACCESS_GROUP` are set, a merged entitlements plist is generated
next to the build binary. Under the sandbox `HOME` is the container, so data
lives in
`~/Library/Containers/com.hazcod.blur/Data/Library/Application Support/Blur`
and logs in `~/Library/Containers/com.hazcod.blur/Data/Library/Logs/Blur`
(same content-free `blur.log` / `startup.log`). The `blur mcp` bridge runs from
the same signed binary. Because the container path is too long for an AF_UNIX
socket, the local MCP socket lives in a user-private `blur-mcp` (0700)
directory under the per-user temp directory (`confstr(_CS_DARWIN_USER_TEMP_DIR)`,
`/var/folders/.../T/`), which the sandbox allows.
Migration, latest wins: on each sandboxed launch, if a store exists in
`~/Library/Application Support/Blur` and its `memory.db` is newer than the
container's (or the container has none), Blur copies it (store, preferences,
local AI settings) over the container's via a temporary directory and rename,
writes a `migrated-from-legacy` marker, and logs one content-free line. Legacy
data is copied, never moved or deleted; remove it yourself once you no longer
run unsandboxed builds. The macOS Accessibility grant is still required and is
tied to the bundle ID and signing identity.
The bundle identifier is `com.hazcod.blur`. The build reuses the existing
bundle's unambiguous signer, or the only valid local signing identity, to keep
Accessibility identity stable across rebuilds. If several identities are
available and there is no signed bundle to match, pass the identity explicitly:

```sh
make macos-app MACOS_CODESIGN_IDENTITY="Apple Development: Your Name (TEAMID)"
```

If there is no unambiguous identity, the build falls back to ad-hoc signing and
macOS may require Accessibility approval again after the executable changes.
Ad-hoc builds remain supported for contributors without a signing certificate.
Neither configuration is a notarized public release; Blur cannot grant or
migrate macOS-protected permissions itself. Add the final bundle in
`~/Downloads/Blur.app` to macOS Device Control & Data Access / Accessibility.
The app uses the existing platform Keychain namespace and refuses plaintext or
fixture-key fallback. If capture is enabled but Accessibility is missing,
Blur asks macOS once to display its native permission guidance and checks the
grant automatically; it never grants or edits TCC state. Capture is enabled
by default after the user grants macOS Accessibility permission; it can be
paused from the menu-bar item or Privacy settings. Privacy settings offer
“Capture all by default” (the default) or “Capture none by default,” with
allowlists and never-capture rules for apps and website domains; never-capture
rules win. Closing a window
hides it while capture continues; choose Quit Blur in the menu-bar menu to
stop the background process. The menu-bar item opens Interact, a compact
note/Ask panel; hovering the notch shows the local recap and composer. Choose
Open Today to show the full dashboard. There
is no global hotkey yet. Run only against data you are comfortable using with
development software; active browser pages are captured through their
foreground Accessibility tree with address-bar and tab chrome filtered.
Private-window title markers fail closed; browser history is never read or
navigated. Unconfigured stores automatically prune memory older than 90 days;
Privacy settings let users choose 30, 90, 180, or 365 days, or Off (retain
indefinitely). Deletion is logical and does not guarantee physical erasure
from backups, snapshots, or storage media. Privacy settings support up to 32
app identifiers/executable names and 32 website domains in both allow and
never-capture lists. These rules are applied before accessibility values are
read and affect future captures only; existing history is unchanged unless
explicitly deleted.
Privacy settings also expose Follow System, Balanced, Blur Saver, and
Performance power modes. Follow System reads the OS battery-saver signal on
demand (macOS ProcessInfo, Windows system power status, Linux Power Profiles
over the system bus); unknown state and battery power alone resolve to
Balanced. User-started capture, retention, note processing, and Ask are not
disabled by the power mode. Optional hourly recaps are separately opt-in,
local-Ollama-only, token/time bounded, and admitted through the current power
policy. Evidence is held only for the active hour, and a single boundary timer
or startup recovery of at most the previous 24 hours closes periods; no app or
browser history is navigated. Recaps are encrypted, source-linked, browseable
from Today, and cascade-delete when a source is removed. When the local model
finds a concrete meeting follow-up missing, it may suggest one source-linked
question in Today; it never creates tasks or sends messages from captured
activity. There is no claim of
measured battery savings yet.

An expanded hourly recap can also be reviewed with local Ollama for up to
three possible durable Library facts. Only the recap's bounded, cited source
text is sent, and only after selecting “Suggest Library facts.” Suggestions
remain temporary until accepted one by one; accepted facts retain their source
link. Remote providers are not used for this feature, and it never creates
tasks or modifies an existing Library entry automatically.
`make safari-extension-test` builds and validates
the unsigned universal Safari `.appex` in `dist/safari-extension-derived` in
addition to the bounded JavaScript/Swift source checks. An unsigned extension is
not installable: a release build must embed it in the app and sign both targets
with the same developer team and registered App Group/Keychain access-group
entitlements. `make macos-app-safari-signed` builds into a fresh staging path,
checks the exact bundle IDs, matching teams, the selected signing certificate,
and the restricted Keychain access group against both supplied provisioning
profiles. App Group and sandbox entitlements are validated in the bundle; they
are not incorrectly required to appear in the profiles. The target signs the
extension before the containing app, verifies both signatures, then publishes
the signed bundle by same-volume rename. It requires
`MACOS_CODESIGN_IDENTITY`, `BLUR_APP_GROUP_ID`, `BLUR_KEYCHAIN_ACCESS_GROUP`,
`MACOS_HOST_PROVISIONING_PROFILE`, and
`MACOS_EXTENSION_PROVISIONING_PROFILE`; it does not submit or notarize the app.
Safari capture still requires real registered identifiers and an end-to-end
Safari runtime test. Even in a configured build, Safari page capture has its
own Privacy setting, defaults off (including for upgraded preference files),
and follows the global capture pause. It requests only active, permitted,
non-private page snapshots; it does not read browser history. `make macos-app-safari-unsigned` assembles an inspectable host
bundle with the extension in `Contents/PlugIns`; it requires explicit App Group
and Keychain access-group identifiers but deliberately does not sign anything.
The committed Xcode project can be regenerated from
`platform/macos/project.yml` with XcodeGen; building it does not require
XcodeGen.

`make cross-build` builds `blur`, `blur-core`, `blurctl`, and physical storage
with cgo disabled for Darwin arm64/amd64, Windows amd64, and Linux amd64/arm64;
it also compiles the native GoUI package for Linux arm64. The desktop entrypoint
selects the X11 shell on both Linux architectures. `make cross-test-compile`
compiles platform conformance tests for Darwin arm64, Windows amd64, and Linux
amd64/arm64, plus the Linux adapter tests on both Linux architectures. The
small vendored GoUI compatibility patch is documented in
[`docs/vendor-patches.md`](vendor-patches.md). `make no-cgo-audit` inspects
command dependency graphs for Cgo files and `import "C"` across the supported
target matrix.

`make portable-packages` creates unsigned, version-free portable artifacts in
`dist/packages/`: `blur-windows-amd64.zip`, `blur-linux-amd64.tar.gz`, and
`blur-linux-arm64.tar.gz`. The Windows archive contains a GUI-subsystem
`Blur.exe`, a separate console-subsystem `blur-mcp.exe` for inherited Claude
stdio, and the README, license, and security policy. The GUI app writes a
bounded rotating `blur.log` and runtime crash output under
`%APPDATA%\Blur\Logs`; one prior crash is retained as `crash.log.1`.
Linux archives contain the matching cgo-disabled desktop binary plus the
README, license, security policy, desktop launcher, and SVG icon. The Linux
launcher expects `blur` on `PATH`; install it under `~/.local/bin` and install the included desktop file
and icon in the standard per-user XDG application/icon directories. Linux
desktop capture requires an X11 session with AT-SPI2, D-Bus, and a Secret
Service implementation available. The Windows archive is unsigned and is not
an installer. CI exposes the archives as a
14-day workflow artifact but does not publish a GitHub release automatically.
macOS remains the separate signed `Blur.app` workflow above.

## Security status

The desktop can use an existing Ollama model or an explicitly configured
OpenAI-compatible provider for quick-note processing. Remote use requires
explicit opt-in; API credentials are kept in the OS secret store, never in
preferences. Sanitized user-authored note text is processed only when the user
adds it. The Ask screen is a separate, explicit action: it retrieves up to eight
matching local sources, bounds them to a small evidence context, and sends that
context to Ollama on loopback by default. Sending Ask evidence to the configured
OpenAI-compatible endpoint requires a separate, persisted opt-in in Privacy;
changing that endpoint or model clears consent. The provider may retain requests.
Background recaps and knowledge derivation remain local-only. Ollama is a
separate local trust boundary and may retain request logs or model state. Remote
endpoints must use HTTPS (loopback development endpoints may use HTTP); remote
providers may retain requests under their own policies. Both request paths bound response bytes, output tokens, and
context admission. Over-budget notes remain saved and explicit TODOs are still
processed without a model call. Model proposals stay unconfirmed until the
user accepts them. There is no screenshot/OCR path. macOS has an
event-driven AXUIElement capture adapter. Windows has a bounded UI Automation
reader, foreground identity leases, and event-driven WinEvent subscriptions;
Windows Pause and shutdown do not join a timed-out provider on the UI thread;
abandoned cleanup is serialized through one bounded UIA session slot. A truly
wedged provider can temporarily suppress further Windows capture, but cannot
cause unbounded per-focus automation sessions. Live Windows runtime validation
remains open. Linux has a private-bus,
event-driven AT-SPI2 adapter and bounded reader, wired to the Linux amd64/X11
and arm64/X11 desktops. Runtime validation against real AT-SPI desktops remains
open; this work was tested with synthetic/unit fixtures and cross-compilation
only.
Known macOS, Linux, and Windows browsers contribute only their active
foreground Accessibility tree; address-bar, tab, toolbar, and form-entry
roles are filtered, editable text/search controls and their descendants are
never read, and recognized private-window title markers fail closed.
Unknown, empty, or unreadable browser window titles also fail closed. This is
not a browser-history reader and does not navigate, click, scroll, paginate,
or search browser content. The conditional, owner-only AF_UNIX Safari ingest
bridge remains an optional, separately configured path. The Safari extension
builds and can be embedded by an explicit unsigned inspection target, but it
is not part of the default app and has not been signed or runtime-tested. The macOS Keychain,
Windows DPAPI CurrentUser, and Linux
Secret Service key adapters exist. Linux arm64 now builds the same X11 desktop
shell as Linux amd64; runtime validation on ARM64/X11 hardware or a full-system
emulator remains open.
Unavailable ports fail closed. No screenshot, OCR, clipboard, keystroke,
remote-provider, or other alternate capture path is wired in.

The desktop stores data in a private-permission encrypted local database and
uses OS secret storage for its data key. Source deletion, derived-item
cascades, and an explicit two-step Delete All action are implemented; Delete
All is logical deletion, not guaranteed physical erasure. Backup/restore, key
rotation, secure erasure from filesystem snapshots, and rollback detection are
not implemented. Encryption at rest does not protect against a compromised process, an
unlocked account, or loss of the OS-protected key. The in-process capture
pipeline requires an available capability report,
validates a non-content probe, rejects secure, elevated, private, and excluded
targets before value reads, bounds work by bytes/nodes/depth/time, and redacts
heuristically before producing an opaque validated observation. This is a
foundation control, not a guarantee that all secrets are recognized. Do not
send real sensitive data through the demo or treat this repository as a
secure persistent memory product.

Local grants use scoped HMAC request proofs with expirations, one-use nonces,
replay limits, and revocation in memory. The store caps grants at 1,024 and
clears retained key/map state when grants are revoked or pruned after expiry.
Consumed nonces are retained through the full accepted clock-skew window, then
reclaimed so a live grant cannot be exhausted by old proofs.
The optional Safari bridge keeps its one-day bearer credential only in an
explicitly configured shared Keychain access group and deletes it on pause;
its AF_UNIX endpoint requires a private owner-only directory and validates the
peer UID on macOS/Linux. Without matching signed App Group/Keychain entitlements
it stays unavailable. This is not a general desktop IPC service or process
isolation boundary, and no persisted client identity is provided. Search binds
a per-request byte budget into its proof, scans at most
64 observations and 2 MiB (the omitted byte-limit field defaults to that cap),
fetches at most four rows per page, and the core allows at most two concurrent
searches. `securestore` defines a strict
key-state contract and OS secret adapters. Backup, key rotation, whole-store
rollback detection, and physical secure-erasure guarantees remain unimplemented.
See the [threat model](threat-model.md)
for limits and residual risks.

## Checks

The foundation verification commands are listed above. The latest local
`make vulncheck` run completed with no known vulnerabilities. Re-run it before
each release; the advisory database changes, and a clean scan does not replace
platform runtime validation, supply-chain/license review, or an independent
security assessment. Passing compile and unit checks do not establish that the
deferred persistence, platform, privacy, and security reviews are complete.

Developer-only UI snapshots (macOS): run the app binary with `BLUR_UI_SNAPSHOT_DIR=<dir>` to write
`<dir>/<page>.png` for each page plus `quick-panel.png`, then quit. It photographs the app's own windows (no Screen
Recording permission), uses a throwaway store and in-memory key, never touches real memory, and is inert when unset. A sandboxed build cannot write outside its container, and
a shell cannot read the container, so for review tooling build a dev-only app with a writable exception and point
the snapshot directory at the same path (never use or distribute that build):

```sh
make macos-app MACOS_SANDBOX_DEV_PATH=/tmp/blur-ui-snapshots
BLUR_UI_SNAPSHOT_DIR=/tmp/blur-ui-snapshots ~/Downloads/Blur.app/Contents/MacOS/blur
```

## Development

Common targets: `make test`, `make race`, `make vet`, `make no-cgo-audit`,
`make cross-build cross-test-compile`, and `make vulncheck`. See
[CONTRIBUTING](../CONTRIBUTING.md) for the full workflow, the cgo-free rule, and
vendored dependency patches.

The Go module is `github.com/hazcod/blur`. License: Apache-2.0; see
[LICENSE](../LICENSE).
