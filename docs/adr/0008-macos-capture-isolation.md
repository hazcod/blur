# macOS capture isolation

Status: researched direction; not implemented or accepted with real AX/TCC.

## Finding

There is a supported architecture for keeping Blur's UI, encrypted storage and
models sandboxed while moving cross-app Accessibility into a minimal unsandboxed
embedded XPC service. Apple's DTS explicitly recommends this split for directly
distributed apps with sandbox-incompatible operations. The collector itself
would remain unsandboxed. This is not an all-process Sandbox solution.

- [DTS: The Case for Sandboxing a Directly Distributed App](https://developer.apple.com/forums/thread/776609)
- [App Sandbox restrictions, including assistive Accessibility](https://developer.apple.com/documentation/security/protecting-user-data-with-app-sandbox.md)

Current Blur remains the user-approved unsandboxed direct-download app. Do not
re-enable its Sandbox until the collector path is functional and accepted.

## Supported boundary

The host owns encrypted storage, search, models, settings and UI. The collector
owns only foreground-target observation and bounded text capture. It receives
no encryption keys, arbitrary file paths, shell commands or model requests.
Authenticate the IPC peer, bind requests to policy/capture epochs, enforce app
and site exclusions on both sides, cancel on pause, and reject stale callbacks.
The host's menu-bar process must stay alive for an embedded XPC service to keep
working; ordinary XPC lifetime does not promise capture after host termination.

Enable Hardened Runtime independently on each executable. It protects code
loading and runtime behavior; it does not confine filesystem or network access
like App Sandbox. Keep library validation enabled and sign embedded code with
the same certificate.

## Approaches ruled out

- A normal `Process`/`posix_spawn` child inherits its sandbox. Removing an
  inheritance entitlement is not a supported escape.
- Apple DTS says a sandboxed app installing an unsandboxed launch agent or
  daemon with SMAppService is unsupported; newer macOS versions block this.
- There is no supported `com.apple.security.accessibility` entitlement that
  grants general cross-app AX capture inside App Sandbox.
- ScreenCaptureKit supports sandboxed apps, but OCR cannot supply AX password,
  private-browser or domain semantics. It is a separately consented fallback,
  not a reason to weaken those privacy rules.

References: [child-process inheritance](https://developer.apple.com/forums/thread/123873),
[SMAppService sandbox matrix](https://developer.apple.com/forums/thread/802443),
[AX entitlement clarification](https://developer.apple.com/forums/thread/799000),
[ScreenCaptureKit sample](https://developer.apple.com/documentation/screencapturekit/capturing-screen-content-in-macos.md).

## Acceptance before switching the host

The documented general XPC split does not prove its exact AX/TCC attribution.
Apple warns that helper permission attribution can be subtle. A signed runtime
acceptance must establish the visible Accessibility identity, successful
capture, revoke/regrant behavior, service interruption, upgrades preserving
identity and exclusion/pause enforcement. Do not promise that a separate helper
will automatically get a separate permission entry.

- [TCC attribution and helpers](https://developer.apple.com/forums/thread/718728)
- [Constraints for embedded code](https://developer.apple.com/documentation/security/applying-launch-environment-and-library-constraints.md)

Use the existing canonical Blur bundle and exact signing identity throughout;
do not create an alternate acceptance app or reset TCC. No app lifecycle or
permission operation was performed during this research.
