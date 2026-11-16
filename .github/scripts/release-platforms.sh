#!/usr/bin/env bash
# Never skip the macOS app; distinguish development archives from trusted releases.
set -euo pipefail
required=(MACOS_DEVELOPER_ID_P12_BASE64 MACOS_DEVELOPER_ID_P12_PASSWORD APP_STORE_CONNECT_API_KEY_ID APP_STORE_CONNECT_API_ISSUER_ID APP_STORE_CONNECT_API_KEY_P8_BASE64)
missing=()
configured=0
for secret_name in "${required[@]}"; do
  if [[ -n "${!secret_name:-}" ]]; then configured=$((configured + 1)); else missing+=("$secret_name"); fi
done
case "${BLUR_REQUIRE_TRUSTED_MACOS:-false}" in true|false) ;; *) echo 'BLUR_REQUIRE_TRUSTED_MACOS must be true or false' >&2; exit 1 ;; esac
if [[ "$configured" -eq 0 && "${BLUR_REQUIRE_TRUSTED_MACOS:-false}" == false ]]; then
  mode=development
elif [[ "${#missing[@]}" -ne 0 ]]; then
  printf 'Trusted macOS release configuration is incomplete. Missing secrets: %s\n' "${missing[*]}" >&2
  exit 1
else
  mode=notarized
fi
printf 'macos_mode=%s\n' "$mode" >> "${GITHUB_OUTPUT:-/dev/stdout}"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf 'macOS app: **%s**. Development archives use ad-hoc signatures, are not Developer ID signed or notarized, and may be blocked by Gatekeeper.\n' "$mode" >> "$GITHUB_STEP_SUMMARY"
fi
