# Releasing Blur

Each push to `main` creates a numbered preview, `v0.1.0-alpha.<workflow-run-number>`.
The `Release` workflow builds the complete macOS app before GoReleaser creates
the GitHub release. A macOS build, helper, signing, notarization, or archive
validation failure stops release creation. The subsequent attachment job uploads
the verified macOS assets; an attachment failure leaves the workflow failed and
can be retried without replacing the tag's source commit.

## Downloads and signing status

Every successful release includes a universal **`Blur.app`** for arm64 and
x86_64, including `Contents/Helpers/blur-foundation-models`, the app icon,
and version metadata. The app and helper are unsandboxed so macOS Accessibility
can read other applications; each still has a verified code signature.

| Apple CI configuration | macOS archive | Trust status |
| --- | --- | --- |
| All five secrets below configured | `Blur_<version>_macos_universal.zip` | Developer ID signed, Hardened Runtime, timestamped, notarized, stapled, and verified with Gatekeeper |
| No Apple secrets configured | `Blur_<version>_macos_universal_development.zip` | Ad-hoc development signatures; **not** Developer ID signed or notarized; Gatekeeper may block the download |
| Some secrets configured, or trusted build required without all secrets | No release | Actionable error naming missing secret keys, without their values |

Extract the archive and move `Blur.app` to Applications. Prefer a notarized
archive for regular use. Development status appears in its filename, the
adjacent `.json` asset, and `Blur.app/Contents/Resources/BlurRelease.json`; the
workflow does not describe development archives as trusted downloads. Set the
repository variable `BLUR_REQUIRE_TRUSTED_MACOS=true` to forbid the development
fallback entirely. Its default is `false` for the automatic preview releases.

Each macOS archive has an adjacent `.zip.sha256` file containing its basename,
so verification works in any download directory:

```sh
shasum -a 256 -c Blur_<version>_macos_universal_development.zip.sha256
```

The adjacent `.json` contains source commit, full release version, numeric build,
architectures, minimum macOS version, signing/notarization status, and the archive
SHA-256. It is generated from the checked bundle and signing mode. Both manifests
declare `sandbox: false`. The bundled manifest is finalized before signing;
packaging verifies signatures and rejects App Sandbox or sandbox inheritance
entitlements on either executable, including after notarization when applicable.

Windows amd64 GUI/MCP and Linux amd64/arm64 archives are built by GoReleaser from
`.github/goreleaser.yaml`. Their `checksums.txt` is signed keylessly with cosign
using GitHub OIDC. The macOS checksum is a separate asset, not an entry in that
GoReleaser checksum file.

## Repository secrets and runner

| Secret | Purpose |
| --- | --- |
| `MACOS_DEVELOPER_ID_P12_BASE64` | Base64 of the CI Developer ID Application certificate and private key (`.p12`) |
| `MACOS_DEVELOPER_ID_P12_PASSWORD` | Export password for that `.p12` |
| `APP_STORE_CONNECT_API_KEY_ID` | App Store Connect API key ID |
| `APP_STORE_CONNECT_API_ISSUER_ID` | App Store Connect issuer ID |
| `APP_STORE_CONNECT_API_KEY_P8_BASE64` | Base64 of the corresponding `.p8` key |

Provision these through repository secrets. The pipeline imports exactly one
Developer ID Application identity into a disposable runner keychain and deletes
the keychain and private-key files on exit, including failures. It does not
discover, export, upload, or reuse the local user's signing identity. A configured
certificate/notary failure never falls back to development signing.

The current Foundation Models helper requires a macOS 27+ SDK to compile, while
retaining its macOS 13 deployment target and weak framework link. The release
job defaults to GitHub's [`xcode-27` runner](https://github.blog/changelog/2026-10-15-xcode-27-runner-image-generally-available/),
generally available since October 15; `macos-15` does not include the required SDK. Repository
variables `MACOS_RELEASE_RUNNER` and `MACOS_RELEASE_DEVELOPER_DIR` can select a
capable runner label and Xcode developer directory. The SDK preflight fails
explicitly when it is too old. Apple Intelligence remains dependent on eligible
hardware, OS and system-model availability; packaging does not claim inference
works on every supported OS or Intel Mac.

The ordinary archive does not include the Safari extension: its App Group and
Keychain provisioning remain a separate signing path. Explicit local
`MACOS_SANDBOX=1` compatibility builds retain their sandbox entitlements, but
cross-app Accessibility capture is unsupported in that configuration. No development sandbox
absolute-path exceptions or acceptance build flags are used in release builds.

## Local checks

```sh
python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v
actionlint .github/workflows/release.yml
make release-snapshot
```

The Python tests exercise policy routing, missing/invalid bundle components,
metadata, archive contents and executable permissions. On macOS they also
compile synthetic universal executables into a temporary fixture, apply real
ad-hoc unsandboxed signatures, and verify them after an archive round trip. They
reject sandboxed manifests, sandbox/inherit entitlements and ad-hoc replacement
of an existing app. They never launch the fixtures or touch the user's installed
app.

`make release-snapshot` builds portable archives without signing/publishing; it
does not run the CI-only macOS release script. Normal local user builds continue
through `make macos-app-stable` with App Sandbox disabled, preserving the exact
existing certificate and signing requirement at `~/Downloads/Blur.app`. An
unavailable signing identity stops replacement; ad-hoc signing is available only
for a fresh development bundle. A build does not launch the app.

Third-party actions are pinned to commit SHAs with the tag in a trailing comment.
