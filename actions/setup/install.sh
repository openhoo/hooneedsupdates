#!/usr/bin/env bash
set -euo pipefail

version="${INPUT_VERSION#v}"
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "::error::version must be a stable semantic version" >&2
  exit 2
fi

runner_os_value="$(printf '%s' "$RUNNER_OS_VALUE" | tr '[:upper:]' '[:lower:]')"
case "$runner_os_value" in
  linux) os=linux ;;
  macos) os=darwin ;;
  windows) os=windows ;;
  *) echo "::error::unsupported runner OS: ${RUNNER_OS_VALUE}" >&2; exit 2 ;;
esac

runner_arch_value="$(printf '%s' "$RUNNER_ARCH_VALUE" | tr '[:upper:]' '[:lower:]')"
case "$runner_arch_value" in
  x64) arch=amd64 ;;
  arm64) arch=arm64 ;;
  *) echo "::error::unsupported runner architecture: ${RUNNER_ARCH_VALUE}" >&2; exit 2 ;;
esac

if [[ "$os" == windows && "$arch" != amd64 ]]; then
  echo "::error::Windows ARM64 release is not published" >&2
  exit 2
fi

archive="hooneedsupdates_${version}_${os}_${arch}.tar.gz"
base="https://github.com/openhoo/hooneedsupdates/releases/download/v${version}"
signature_identity="https://github.com/openhoo/hooneedsupdates/.github/workflows/release.yml@refs/tags/v${version}"
signature_issuer="https://token.actions.githubusercontent.com"
temporary="$(mktemp -d "${RUNNER_TEMP}/hooneedsupdates-download.XXXXXXXX")"
trap 'rm -rf "$temporary"' EXIT
archive_path="${temporary}/${archive}"
checksums_path="${temporary}/SHA256SUMS"
archive_bundle="${archive_path}.sigstore.json"
checksums_bundle="${checksums_path}.sigstore.json"

curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "$archive_path" "${base}/${archive}"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "$checksums_path" "${base}/SHA256SUMS"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "$archive_bundle" "${base}/${archive}.sigstore.json"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "$checksums_bundle" "${base}/SHA256SUMS.sigstore.json"

if ! command -v cosign >/dev/null 2>&1; then
  echo "::error::Pinned Cosign verifier is unavailable" >&2
  exit 1
fi
cosign verify-blob "$archive_path" --bundle "$archive_bundle" \
  --certificate-identity "$signature_identity" --certificate-oidc-issuer "$signature_issuer"
cosign verify-blob "$checksums_path" --bundle "$checksums_bundle" \
  --certificate-identity "$signature_identity" --certificate-oidc-issuer "$signature_issuer"

expected="$(awk -v file="$archive" '$2 == file { print $1 }' "$checksums_path")"
if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "::error::release checksum entry is missing or malformed" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$archive_path" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$archive_path" | awk '{ print $1 }')"
else
  echo "::error::no SHA-256 checksum utility is available" >&2
  exit 1
fi
if [[ "$actual" != "$expected" ]]; then
  echo "::error::release checksum mismatch" >&2
  exit 1
fi

destination="${RUNNER_TEMP}/hooneedsupdates-${version}"
mkdir -p "$destination"
tar -xzf "${temporary}/${archive}" -C "$destination"
extension=""
if [[ "$os" == windows ]]; then
  extension=".exe"
fi
executable="${destination}/hooneedsupdates${extension}"
chmod +x "$executable"
"$executable" version
echo "$destination" >> "$GITHUB_PATH"
echo "executable=$executable" >> "$GITHUB_OUTPUT"
