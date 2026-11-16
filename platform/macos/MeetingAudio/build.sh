#!/bin/bash
set -euo pipefail
meeting_audio_root="$(cd "$(dirname "$0")" && pwd)"
if [[ $# -lt 1 || $# -gt 2 || "$1" != *.dylib ]]; then
  echo 'usage: build.sh /output/libBlurMeetingAudio.dylib [universal|arm64|x86_64]' >&2
  exit 2
fi
if [[ -z "${DEVELOPER_DIR:-}" && -d /Applications/Xcode.app/Contents/Developer ]]; then export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer; fi
exec python3 "$meeting_audio_root/../build_native.py" --meeting-out "$1" --arch "${2:-universal}"
