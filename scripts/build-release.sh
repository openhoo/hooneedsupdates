#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:?VERSION is required}"
commit="${COMMIT:?COMMIT is required}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="${root}/dist"

mkdir -p "$dist"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os="${target%/*}"
  arch="${target#*/}"
  extension=""
  if [[ "$os" == windows ]]; then
    extension=".exe"
  fi
  name="hooneedsupdates_${version}_${os}_${arch}"
  stage="$(mktemp -d "${TMPDIR:-/tmp}/hooneedsupdates-release.XXXXXXXX")"
  trap 'rm -rf "$stage"' EXIT
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=${version}" \
    -o "${stage}/hooneedsupdates${extension}" ./cmd/hooneedsupdates
  install -m 0644 LICENSE "${stage}/LICENSE"
  tar -czf "${dist}/${name}.tar.gz" -C "$stage" "hooneedsupdates${extension}" LICENSE
  rm -rf "$stage"
  trap - EXIT
done

printf '%s\n' "$commit" > "${dist}/COMMIT"
