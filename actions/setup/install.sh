#!/usr/bin/env bash
set -euo pipefail

version="${INPUT_VERSION#v}"
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "::error::version must be a stable semantic version" >&2
  exit 2
fi

case "${RUNNER_OS_VALUE,,}" in
  linux) os=linux ;;
  macos) os=darwin ;;
  windows) os=windows ;;
  *) echo "::error::unsupported runner OS: ${RUNNER_OS_VALUE}" >&2; exit 2 ;;
esac

case "${RUNNER_ARCH_VALUE,,}" in
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
temporary="$(mktemp -d "${RUNNER_TEMP}/hooneedsupdates-download.XXXXXXXX")"
trap 'rm -rf "$temporary"' EXIT

curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "${temporary}/${archive}" "${base}/${archive}"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
  --output "${temporary}/SHA256SUMS" "${base}/SHA256SUMS"

expected="$(awk -v file="$archive" '$2 == file { print $1 }' "${temporary}/SHA256SUMS")"
if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "::error::release checksum entry is missing or malformed" >&2
  exit 1
fi
actual="$(sha256sum "${temporary}/${archive}" | awk '{ print $1 }')"
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
