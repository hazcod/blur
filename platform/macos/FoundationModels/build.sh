#!/bin/bash
set -euo pipefail
foundation_build_root="$(cd "$(dirname "$0")" && pwd)"
if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo 'usage: build.sh /output/blur-foundation-models [universal|arm64|x86_64]' >&2
  exit 2
fi
exec python3 "$foundation_build_root/../build_native.py" --foundation-out "$1" --arch "${2:-universal}"
