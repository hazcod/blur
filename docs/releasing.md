# Releasing Blur

Releases are cut by pushing a semantic-version tag. The `Release` workflow
(`.github/workflows/release.yml`) then builds, signs and publishes everything.

## Versioning

GoReleaser requires semantic versions with a leading `v`: `vX.Y.Z`, or
`vX.Y.Z-rc.1` for pre-releases (which are flagged as pre-releases on GitHub).
Tags such as `release-1` or `1.2.3` are not valid for GoReleaser and `v*` tags
that are not valid semver will fail the `goreleaser` job.

The version is injected at link time through
`-X main.version=... -X main.commit=... -X main.date=...` and is shown by
`blur version` (also `blur --version`).

## Cutting a release

```sh
make version                      # preview the version derived from git
git tag -a v1.2.3 -m "Blur 1.2.3"
git push origin v1.2.3
```

The workflow runs two jobs:

1. `goreleaser` (Ubuntu): builds cgo-free Linux (amd64, arm64) and Windows
   (amd64 GUI plus the `blur-mcp` console companion) binaries with
   `-trimpath`, packages `tar.gz` and `zip` archives, writes
   `checksums.txt`, signs it keylessly with cosign (GitHub OIDC), and creates
   the GitHub release with a changelog.
2. `macos` (macOS runner, after the first job): builds the universal app with
   `make macos-app`, signs it with a Developer ID Application certificate
   (Hardened Runtime, secure timestamp, `platform/macos/Blur.sandbox.entitlements`),
   notarizes it with `notarytool`, staples the ticket, verifies with `spctl` and
   `codesign`, and uploads `Blur_<version>_macos_universal.zip` to the release.

Verify a download:

```sh
sha256sum -c checksums.txt
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/hazcod/blur/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

## Repository secrets

| Secret | Purpose |
| --- | --- |
| `MACOS_DEVELOPER_ID_P12_BASE64` | Base64 of the exported Developer ID Application certificate and private key (`.p12`). |
| `MACOS_DEVELOPER_ID_P12_PASSWORD` | Password chosen when exporting the `.p12`. |
| `APP_STORE_CONNECT_API_KEY_ID` | Key ID of the App Store Connect API key. |
| `APP_STORE_CONNECT_API_ISSUER_ID` | Issuer ID shown on the App Store Connect Keys page. |
| `APP_STORE_CONNECT_API_KEY_P8_BASE64` | Base64 of the downloaded `AuthKey_<id>.p8`. |

The cosign signature needs no secret; it uses the workflow's OIDC token.

### Creating them

- Certificate: in Keychain Access export your "Developer ID Application" identity
  as `.p12`, then `base64 -i cert.p12 | pbcopy`. Never commit it.
- API key: App Store Connect > Users and Access > Integrations > Team Keys, create
  a key with at least the Developer role, download the `.p8` once, then
  `base64 -i AuthKey_XXXX.p8 | pbcopy`.

Set each with `gh secret set NAME` (or in the repository settings).


## Local dry run

```sh
make release-snapshot
# equivalent to:
goreleaser release --snapshot --clean --skip=sign,publish
```

This builds all archives into `dist/` without signing or publishing. It does not
cover the macOS app; build that with `make macos-app` (see the README).

## Action pinning

Third-party actions are pinned to commit SHAs with the tag in a trailing
comment. To refresh one, resolve the tag with
`gh api repos/<owner>/<repo>/git/ref/tags/<tag>` (dereference annotated tags via
`git/tags/<sha>`).
