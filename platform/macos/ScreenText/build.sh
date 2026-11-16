#!/bin/bash
set -euo pipefail
screen_text_root="$(cd "$(dirname "$0")" && pwd)"
if [[ $# -lt 1 || $# -gt 2 || "$1" != *.dylib ]]; then
  echo 'usage: build.sh /output/libBlurScreenText.dylib [universal|arm64|x86_64]' >&2
  exit 2
fi
exec python3 "$screen_text_root/../build_native.py" --screen-out "$1" --arch "${2:-universal}"
