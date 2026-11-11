# Safari Active-Page Capture Design

**Status:** Implementation in progress; Safari native packaging requires Xcode.
**Scope:** macOS Safari content capture only. Other browsers remain fail-closed.

## Intent

Safari web content is a first-class capture source, not a reason to crawl browser
history. When the user explicitly enables browser capture and grants Safari
website access, Blur may capture text already loaded in the currently active,
non-private page. This includes rendered off-screen content in that document,
but never content from another tab, a prior page, a search result, or a URL Blur
opens itself.

## Product and security requirements

- Capture is opt-in, shares the app's pause/resume state, and is visibly
  reported in Privacy settings. If capture is paused or a capability is
  unknown, no page text is accepted.
- The Safari native extension must obtain the originating `SFSafariPage` from
  the message event and independently require `isActive == true` and
  `usesPrivateBrowsing == false`. Missing properties, stale messages, or
  private mode fail closed. A page script cannot assert these facts on its own.
- The extension reads only the current document. It does not call tab/history
  enumeration, navigation, click, scroll, pagination, or search APIs.
- A bounded DOM text walk includes already-loaded off-screen rendered text,
  while excluding hidden subtrees, script/style/template content, editable
  control values, and password fields. Work is bounded by elapsed time, nodes,
  and bytes; unchanged content is not resent. Mutation-driven refresh is
  debounced and rate-limited, with no polling loop.
- Only HTTP(S) pages with a hostname are eligible. Paths, query strings,
  fragments, and credentials are discarded in the native extension before
  IPC. The Go sanitizer independently persists only the hostname and applies
  the existing redaction and validation pipeline.
- Raw text travels only in process memory across a bounded AF_UNIX socket
  inside a configured macOS App Group container. It is never queued to disk,
  logged, or sent to AI. The host requires same-user peer identity, a narrowly
  scoped `capture:ingest` HMAC grant, timestamp bounds, and one-use replay
  protection before accepting the payload.
- On explicit capture start, the Go host issues a `capture:ingest` grant and
  stores its credential in a Keychain access group shared through the
  configured App Group. The Safari extension reads that credential to
  authenticate each bounded socket message. The grant lifetime is at most 24
  hours. Pause/disable first closes the receiver gate, waits for any in-flight
  write, revokes the grant, and deletes the shared credential. The Go capture
  controller is the policy source of truth; Safari website permission is an
  additional gate, never a signal to start capture by itself.
- The receiving service rejects disabled policy, invalid proof, malformed or
  oversized payloads, unknown browser identity, inactive/private/unknown
  privacy state, unsupported URL schemes, and stale timestamps. It writes
  only a validated sanitized observation to an encrypted sink.
- App Group ID is a signing/build setting, not a hard-coded universal Apple
  developer identity. Both targets need the configured shared-container and
  Keychain-group entitlements. Builds lacking valid entitlements expose browser
  capture as unavailable rather than silently falling back to a weaker
  transport.

## Architecture

Use a Safari App Extension injected script to extract the bounded current DOM
and send one snapshot message through `safari.extension.dispatchMessage`. Its
`SFSafariExtensionHandler.messageReceived(withName:from:userInfo:)` callback
provides the originating `SFSafariPage`; the native handler obtains
authoritative URL, active, and private-mode properties from that exact page. A
Safari Web Extension with background native messaging is not the first
adapter, because its native message boundary does not provide this originating
page object directly.

The native handler discards URL path, query, fragment, and credentials, checks
message types and byte limits, and sends the approved envelope through the
app-group Unix socket.
The Go bridge authenticates the exact payload bytes with the existing
`localapi.GrantStore` proof format and `ScopeCaptureIngest`, then delegates to
`browsercapture.Sanitize`. The result uses the same `SanitizedObservation`
contract and encrypted sink as accessibility capture. No persistent browser
queue is introduced. The Safari app ID is `com.apple.safari`; the normalized
URL hostname is the observation domain; its source identity is the existing
sanitized observation content ID.

## Explicit limits

- The first adapter is Safari only. Browser mode/private state must be
  authoritative before another browser is enabled.
- Initial budgets: 10,000 visited nodes, 256 KiB extracted page text, 100 ms
  per extraction, 10 seconds minimum between mutation-triggered refreshes, a
  288 KiB authenticated wire frame, and a 24-hour maximum grant lifetime.
- Content extraction is bounded and may omit very large pages; omission is
  explicit in metrics/status and never worked around by a larger unbounded
  scan.
- Xcode, a signed App Group entitlement, Safari website permissions, and
  runtime tests are required to claim the native extension ships. A passing Go
  test or a source-only extension is not end-to-end completion.

## Evidence

Apple documents that Safari extensions and containing apps run in separate
sandboxed environments; native messaging and App Groups are their supported
communication paths. Apple also documents App Groups as supporting UNIX-domain
sockets inside the shared container, and Safari page properties expose
`isActive` and `usesPrivateBrowsing`.

- https://developer.apple.com/documentation/safariservices/messaging-between-the-app-and-javascript-in-a-safari-web-extension
- https://developer.apple.com/documentation/BundleResources/Entitlements/com.apple.security.application-groups
- https://developer.apple.com/documentation/safariservices/sfsafaripageproperties
- https://developer.apple.com/documentation/safariservices/adjusting-website-access-permissions
