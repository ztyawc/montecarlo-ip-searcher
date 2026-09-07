#!/usr/bin/env bash
set -euo pipefail
umask 077

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repository_root"
release_tag=${1:?Pass the release tag}
output_directory=${2:-dist}
python3 scripts/release_artifacts.py check-version --tag "$release_tag"
for name in ANDROID_KEYSTORE_BASE64 ANDROID_KEYSTORE_PASSWORD ANDROID_KEY_ALIAS ANDROID_KEY_PASSWORD ANDROID_SIGNING_CERT_SHA256; do
  if [[ -z "${!name:-}" ]]; then
    printf 'Required signing setting is missing: %s\n' "$name" >&2
    exit 1
  fi
done

android_root=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
build_tools="$android_root/build-tools/${ANDROID_BUILD_TOOLS:-36.0.0}"
unsigned_apk=flutter-app/build/app/outputs/apk/release/app-release-unsigned.apk
if [[ ! -f "$unsigned_apk" || ! -x "$build_tools/apksigner" || ! -x "$build_tools/zipalign" ]]; then
  echo 'The unsigned Flutter release APK and pinned Android build tools are required.' >&2
  exit 1
fi

signing_directory=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/mcis-signing.XXXXXX")
trap 'rm -rf "$signing_directory"' EXIT
export MCIS_SIGNING_DIRECTORY="$signing_directory"
python3 - <<'PY'
import base64
import os
from pathlib import Path

encoded = ''.join(os.environ['ANDROID_KEYSTORE_BASE64'].split())
try:
    decoded = base64.b64decode(encoded, validate=True)
except ValueError:
    raise SystemExit('ANDROID_KEYSTORE_BASE64 is not valid base64') from None
if not decoded:
    raise SystemExit('The signing keystore is empty')
(Path(os.environ['MCIS_SIGNING_DIRECTORY']) / 'release.jks').write_bytes(decoded)
PY
keytool -exportcert -keystore "$signing_directory/release.jks" \
  -alias "$ANDROID_KEY_ALIAS" -storepass:env ANDROID_KEYSTORE_PASSWORD \
  -file "$signing_directory/certificate.der"
certificate_sha256=$(sha256sum "$signing_directory/certificate.der" | cut -d ' ' -f 1)
if [[ "$certificate_sha256" != "$ANDROID_SIGNING_CERT_SHA256" ]]; then
  echo 'The signing certificate does not match the pinned release identity.' >&2
  exit 1
fi

mkdir -p "$output_directory"
signed_apk="$output_directory/mcis-$release_tag-android-arm64-v8a.apk"
"$build_tools/zipalign" -P 16 -f 4 "$unsigned_apk" "$signing_directory/aligned.apk"
"$build_tools/apksigner" sign --ks "$signing_directory/release.jks" \
  --ks-key-alias "$ANDROID_KEY_ALIAS" \
  --ks-pass env:ANDROID_KEYSTORE_PASSWORD --key-pass env:ANDROID_KEY_PASSWORD \
  --out "$signed_apk" "$signing_directory/aligned.apk"
"$build_tools/apksigner" verify --verbose --print-certs "$signed_apk" \
  > "$output_directory/ANDROID-SIGNING-CERT.txt"
"$build_tools/zipalign" -c -P 16 4 "$signed_apk"
"$build_tools/aapt2" dump badging "$signed_apk" > "$signing_directory/badging.txt"
python3 scripts/release_artifacts.py verify-apk --tag "$release_tag" \
  --output "$output_directory" --badging "$signing_directory/badging.txt"
# APK Signature Scheme v4 produces a local installation sidecar, not a release asset.
rm -f "$signed_apk.idsig"
printf 'Signed Android release verified: %s\n' "$signed_apk"
