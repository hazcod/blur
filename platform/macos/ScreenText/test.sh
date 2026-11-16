#!/bin/bash
set -euo pipefail
screen_text_root="$(cd "$(dirname "$0")" && pwd)"
screen_text_tmp="$(mktemp -d /tmp/blur-screen-text-test.XXXXXX)"
trap 'rm -rf "$screen_text_tmp"' EXIT
if [[ -z "${DEVELOPER_DIR:-}" && -d /Applications/Xcode.app/Contents/Developer ]]; then export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer; fi
xcrun swiftc -swift-version 6 -warnings-as-errors -parse-as-library -target "$(uname -m)-apple-macos13.0" \
  "$screen_text_root/ScreenText.swift" "$screen_text_root/ScreenTextRegion.swift" "$screen_text_root/ScreenTextEngine.swift" "$screen_text_root/ScreenTextNative.swift" "$screen_text_root/ScreenTextABI.swift" "$screen_text_root/Tests/ScreenTextTests.swift" "$screen_text_root/Tests/ScreenTextRegionTests.swift" \
  -o "$screen_text_tmp/screen-text-tests"
"$screen_text_tmp/screen-text-tests"

"$screen_text_root/build.sh" "$screen_text_tmp/libBlurScreenText.dylib" "$(uname -m)"
cd "$screen_text_root/../../.."
BLUR_SCREEN_TEXT_TEST_LIBRARY="$screen_text_tmp/libBlurScreenText.dylib" go test ./internal/platform/darwin -run '^TestScreenText' -count=1
