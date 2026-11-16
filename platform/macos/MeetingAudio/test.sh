#!/bin/bash
set -euo pipefail
meeting_audio_root="$(cd "$(dirname "$0")" && pwd)"
meeting_audio_tmp="$(mktemp -d /tmp/blur-meeting-audio-test.XXXXXX)"
trap 'rm -rf "$meeting_audio_tmp"' EXIT
if [[ -z "${DEVELOPER_DIR:-}" && -d /Applications/Xcode.app/Contents/Developer ]]; then export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer; fi
xcrun swiftc -swift-version 6 -warnings-as-errors -parse-as-library -target "$(uname -m)-apple-macos13.0" \
  "$meeting_audio_root/MeetingAudioCore.swift" "$meeting_audio_root/MeetingAudioLevel.swift" "$meeting_audio_root/MeetingAudioSession.swift" "$meeting_audio_root/MeetingAudioSetup.swift" "$meeting_audio_root/MeetingAudioSpeech.swift" "$meeting_audio_root/MeetingAudioNative.swift" "$meeting_audio_root/MeetingAudioABI.swift" "$meeting_audio_root/Tests/MeetingAudioTests.swift" "$meeting_audio_root/Tests/MeetingAudioLevelTests.swift" \
  -o "$meeting_audio_tmp/meeting-audio-tests"
"$meeting_audio_tmp/meeting-audio-tests"
"$meeting_audio_root/build.sh" "$meeting_audio_tmp/libBlurMeetingAudio.dylib" "$(uname -m)"
cd "$meeting_audio_root/../../.."
BLUR_MEETING_AUDIO_TEST_LIBRARY="$meeting_audio_tmp/libBlurMeetingAudio.dylib" CGO_ENABLED=0 go test ./internal/platform/darwin -run '^TestMeetingAudio' -count=1
