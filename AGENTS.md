<!-- CODEGRAPH_START -->
## CodeGraph

In repositories indexed by CodeGraph (a `.codegraph/` directory exists at the repo root), reach for it BEFORE grep/find or reading files when you need to understand or locate code:

- **MCP tool** (when available): `codegraph_explore` answers most code questions in one call — the relevant symbols' verbatim source plus the call paths between them, including dynamic-dispatch hops grep can't follow. Name a file or symbol in the query to read its current line-numbered source. If it's listed but deferred, load it by name via tool search.
- **Shell** (always works): `codegraph explore "<symbol names or question>"` prints the same output.

If there is no `.codegraph/` directory, skip CodeGraph entirely — indexing is the user's decision.
<!-- CODEGRAPH_END -->

## Local macOS builds

- The active macOS implementation is in `.worktrees/durable-memory-core`
  (`feature/durable-memory-core`). Until it is integrated, build from that
  worktree. The main checkout is older and cannot read its schema-5 store;
  do not overwrite the current app with a build of the main checkout.

- Build the user's app at `~/Downloads/Blur.app`, with bundle identifier
  `com.hazcod.blur`, including from worktrees. Use `make macos-app-stable` when
  available. Never create renamed, timestamped, acceptance, or alternate app
  bundles for the user.
- Preserve its existing signing identity, designated signing requirement, and
  exact signing certificate. If the identity is unavailable, stop and report the
  blocker; do not fall back to ad-hoc signing, create a certificate, change
  certificate trust, or reset Accessibility/TCC permissions.
- Direct-download macOS builds must be unsandboxed: the user explicitly approved
  this on 2026-11-13 because cross-app Accessibility capture is incompatible with
  App Sandbox. Preserve existing container memory, settings and Keychain identity
  in place; never silently open a second empty or older store. Do not enable
  acceptance behavior or change menu visibility for a normal local build.
- A build does not authorize launching, quitting, restarting, or opening the app.
