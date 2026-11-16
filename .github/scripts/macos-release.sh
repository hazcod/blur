#!/usr/bin/env bash
# CI-only packaging. Never discovers a local signing identity or touches Downloads.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && -n "${RUNNER_TEMP:-}" ]] || { echo 'Run macos-release.sh only on a disposable GitHub Actions macOS runner.' >&2; exit 1; }
[[ "$(uname -s)" == Darwin ]] || { echo 'macOS release packaging requires a macOS runner.' >&2; exit 1; }
release_work=$(mktemp -d "$RUNNER_TEMP/blur-release.XXXXXX")
keychain=
cleanup() {
  if [[ -n "$keychain" ]]; then security delete-keychain "$keychain" >/dev/null 2>&1 || true; fi
  rm -rf "$release_work"
}
trap cleanup EXIT
GITHUB_OUTPUT="$release_work/policy" bash .github/scripts/release-platforms.sh
mode=$(sed -n 's/^macos_mode=//p' "$release_work/policy")
[[ "$mode" == "${MACOS_RELEASE_MODE:-}" ]] || { echo 'macOS signing policy changed between release jobs.' >&2; exit 1; }
release_version=${RELEASE_TAG#v}
[[ "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'RELEASE_TAG must be a semantic version tag.' >&2; exit 1; }
[[ "${GITHUB_RUN_NUMBER:-}" =~ ^[1-9][0-9]*$ && "${GITHUB_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || { echo 'Release build/commit metadata is missing.' >&2; exit 1; }
# The current helper uses SDK 27 FoundationModels APIs with availability guards.
# Fail visibly when the runner lacks them; never ship a helper-free app.
if [[ -n "${BLUR_MACOS_DEVELOPER_DIR:-}" ]]; then export DEVELOPER_DIR="$BLUR_MACOS_DEVELOPER_DIR"; fi
sdk_version=$(xcrun --sdk macosx --show-sdk-version)
[[ "${sdk_version%%.*}" -ge 27 ]] || { echo 'The bundled Foundation Models helper requires a macOS 27+ SDK. Select an Xcode 27+ installation with MACOS_RELEASE_DEVELOPER_DIR or a capable MACOS_RELEASE_RUNNER repository variable.' >&2; exit 1; }
identity=-
sign_flags="--options runtime --timestamp=none"
if [[ "$mode" == notarized ]]; then
  keychain="$release_work/release.keychain-db"
  keychain_password=$(uuidgen)
  printf '::add-mask::%s\n' "$keychain_password"
  printf '%s' "$MACOS_DEVELOPER_ID_P12_BASE64" | base64 --decode > "$release_work/developer-id.p12"
  security create-keychain -p "$keychain_password" "$keychain"
  security set-keychain-settings -lut 21600 "$keychain"
  security unlock-keychain -p "$keychain_password" "$keychain"
  security import "$release_work/developer-id.p12" -k "$keychain" -P "$MACOS_DEVELOPER_ID_P12_PASSWORD" -T /usr/bin/codesign
  rm -f "$release_work/developer-id.p12"
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null
  identities=$(security find-identity -v -p codesigning "$keychain" | sed -n '/"Developer ID Application: /s/.*) \([0-9A-F]*\) .*/\1/p')
  [[ "$identities" =~ ^[0-9A-F]{40}$ ]] || { echo 'Import exactly one valid Developer ID Application identity into the CI certificate secret.' >&2; exit 1; }
  identity=$identities
  sign_flags="--options runtime --timestamp --keychain \"$keychain\""
fi
app="$release_work/Blur.app"
# Explicit identity (including '-' in development mode) bypasses local discovery.
make macos-app MACOS_APP="$app" MACOS_BINARY="$release_work/blur" \
  MACOS_BUNDLE_ID=com.hazcod.blur MACOS_MINIMUM_SYSTEM_VERSION=13.0 \
  MACOS_CODESIGN_IDENTITY="$identity" MACOS_CODESIGN_EXTRA_FLAGS="$sign_flags" \
  MACOS_SANDBOX=0 MACOS_SANDBOX_DEV_PATH= BLUR_APP_GROUP_ID= BLUR_KEYCHAIN_ACCESS_GROUP= \
  VERSION="$release_version" COMMIT="$GITHUB_SHA"
python3 .github/scripts/macos-release.py prepare --app "$app" --mode "$mode" \
  --version "$release_version" --build "$GITHUB_RUN_NUMBER" --commit "$GITHUB_SHA"
# Bundle metadata must be finalized before the final inside-out signature pass.
extra_flags=(--options runtime --timestamp=none)
if [[ "$mode" == notarized ]]; then extra_flags=(--options runtime --timestamp --keychain "$keychain"); fi
codesign --force --sign "$identity" "${extra_flags[@]}" \
  --identifier com.hazcod.blur.foundation-models "$app/Contents/Helpers/blur-foundation-models"
codesign --force --sign "$identity" "${extra_flags[@]}" \
  --identifier com.hazcod.blur.meeting-audio "$app/Contents/Frameworks/libBlurMeetingAudio.dylib"
codesign --force --sign "$identity" "${extra_flags[@]}" \
  --identifier com.hazcod.blur.screen-text "$app/Contents/Frameworks/libBlurScreenText.dylib"
codesign --force --sign "$identity" "${extra_flags[@]}" --entitlements platform/macos/Blur.runtime.entitlements \
  --identifier com.hazcod.blur "$app"
if [[ "$mode" == notarized ]]; then
  printf '%s' "$APP_STORE_CONNECT_API_KEY_P8_BASE64" | base64 --decode > "$release_work/AuthKey.p8"
  ditto -c -k --sequesterRsrc --keepParent "$app" "$release_work/notarize.zip"
  xcrun notarytool submit "$release_work/notarize.zip" --key "$release_work/AuthKey.p8" \
    --key-id "$APP_STORE_CONNECT_API_KEY_ID" --issuer "$APP_STORE_CONNECT_API_ISSUER_ID" --wait --output-format json > "$release_work/notary-result.json"
  python3 - "$release_work/notary-result.json" <<'PY'
import json, sys
if json.load(open(sys.argv[1])).get("status") != "Accepted":
    sys.exit("Apple did not accept the notarization submission; no release archive will be published.")
PY
  rm -f "$release_work/AuthKey.p8" "$release_work/notarize.zip"
  xcrun stapler staple "$app"
fi
python3 .github/scripts/macos-release.py archive --app "$app" --mode "$mode" --output "$RUNNER_TEMP/macos-release-assets"
