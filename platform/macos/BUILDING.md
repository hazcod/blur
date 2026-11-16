# macOS build performance

Build the installed app from the active worktree with `make macos-app-stable`.
It replaces only `~/Downloads/Blur.app`; building does not open or restart it.
The existing bundle identifier, designated signing requirement, and exact
certificate must still match before replacement. Native caching does not skip
staging, signing, library validation, or signature checks.

## Repeated builds

The Go toolchain already caches compiled Go packages. The Makefile also builds
the existing goui CLI once per invocation and reuses it for its build and package
commands. This preserves its resource generation and Mach-O validation. The
signing identity is resolved once and passed through recursive Make calls.

The arm64 Go build, x86_64 Go build, and native build stage run independently,
with `MACOS_BUILD_JOBS=3` by default. Native Swift compilation uses a separate
pool limited to `MACOS_NATIVE_JOBS=2`. Either can be set to `1` to reduce peak
build CPU and memory use.

Foundation Models, MeetingAudio, and ScreenText cache their unsigned architecture slices in
`dist/macos-native-cache`. A cache entry includes source contents, architecture,
complete compile/link flags, the build helper, source path mapping, Swift/compiler
and linker hashes, selected developer directory, SDK path/version/build/settings,
and relevant compiler environment. Source bytes are snapshotted before compiling.
Toolchain identity follows installed SDK version/build metadata; it does not hash
every SDK header. Clear the cache after manually editing SDK contents in place.

Concurrent builds lock each entry, and reuse checks the artifact checksum.
Failures never publish a cache entry or replace an existing output. Both
architectures are verified before publishing a universal output. An unchanged
output retains its modification time. Fresh copies are always staged and signed
with the selected application certificate; signed binaries are never cached.

Full Xcode at `/Applications/Xcode.app` is selected automatically when the active
selection is Command Line Tools. An explicit `DEVELOPER_DIR` pointing to another
Xcode installation is respected. The deployment target remains macOS 13.

Use `BLUR_NATIVE_CACHE_DIR=/absolute/path` to select another cache or remove
`dist/macos-native-cache` to force recompilation. These commands only affect build
artifacts. Helpers can also be built independently:

```sh
platform/macos/FoundationModels/build.sh /tmp/blur-foundation-models
platform/macos/MeetingAudio/build.sh /tmp/libBlurMeetingAudio.dylib
platform/macos/ScreenText/build.sh /tmp/libBlurScreenText.dylib
make macos-build-cache-test
```

The tests use synthetic sources/compiler callbacks and dry-run Make commands.
They do not inspect signing identities or open any app. The separate release
suite also checks the signing and bundle contracts with temporary fixtures.

## Measured native timings

Measured before adding ScreenText, the previous serial universal native compile
steps for Foundation Models and MeetingAudio took 17.8 seconds. With the shared cache and two compiler workers, a cold compile
took 9.4 seconds and an unchanged invocation took 0.32 seconds, including native
toolchain discovery, checksum verification, and universal assembly. Measurements
used temporary unsigned outputs with both actual native components; no installed
app was replaced. These are native-stage timings, not complete application build
timings. A native source change rebuilds both slices of that component; unchanged
components remain cached. Application source changes still use the Go build cache
and require normal linking, packaging, and signing.

Including ScreenText, the three-component universal native build took 15.2
seconds from a cold cache and 0.35 seconds unchanged, including discovery. All
three temporary outputs contained both architectures with macOS 13 minimums.
