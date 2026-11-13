#!/usr/bin/env bash
# Select release jobs without exposing credential values or using unsigned macOS builds.
set -euo pipefail

required=(
  MACOS_DEVELOPER_ID_P12_BASE64
  MACOS_DEVELOPER_ID_P12_PASSWORD
  APP_STORE_CONNECT_API_KEY_ID
  APP_STORE_CONNECT_API_ISSUER_ID
  APP_STORE_CONNECT_API_KEY_P8_BASE64
)
missing=()
configured=0
for secret_name in "${required[@]}"; do
  if [[ -n "${!secret_name:-}" ]]; then
    configured=$((configured + 1))
  else
    missing+=("$secret_name")
  fi
done

if [[ "$configured" -eq 0 ]]; then
  printf 'macos_enabled=false\n' >> "${GITHUB_OUTPUT:-/dev/stdout}"
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    printf '%s\n' 'Publishing Windows and Linux archives. Signed macOS assets are omitted because macOS signing and notarization secrets are not configured.' >> "$GITHUB_STEP_SUMMARY"
  fi
elif [[ "${#missing[@]}" -ne 0 ]]; then
  printf 'Incomplete macOS release configuration. Missing secrets: %s\n' "${missing[*]}" >&2
  exit 1
else
  printf 'macos_enabled=true\n' >> "${GITHUB_OUTPUT:-/dev/stdout}"
fi
