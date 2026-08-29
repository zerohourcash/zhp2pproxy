#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO_BIN:-go}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhp2pproxy-ci.XXXXXX")"
trap 'find "$TMP_DIR" -type f -delete 2>/dev/null || true; find "$TMP_DIR" -depth -type d -empty -delete 2>/dev/null || true; rmdir "$TMP_DIR" 2>/dev/null || true' EXIT
cd "$ROOT"

unformatted="$(gofmt -l cmd internal pkg)"
if [[ -n "$unformatted" ]]; then
  printf 'gofmt required:\n%s\n' "$unformatted" >&2
  exit 1
fi

bash -n build.sh install.sh release.sh scripts/check-release-hygiene.sh \
  scripts/check-public-release-assets.sh scripts/ci.sh
if BUILD_SEQUENCE=2026082202 RELEASE_WINDOWS=0 ALLOW_DIRTY_LOCAL_RELEASE=1 ./release.sh >/dev/null 2>&1; then
  printf 'release.sh accepted a non-monotonic short BUILD_SEQUENCE\n' >&2
  exit 1
fi
"$ROOT/scripts/check-release-hygiene.sh"
"$GO_BIN" mod tidy -diff
"$GO_BIN" test -race ./...
"$GO_BIN" vet ./...

if command -v staticcheck >/dev/null 2>&1; then
  staticcheck ./...
fi
if command -v govulncheck >/dev/null 2>&1; then
  govulncheck ./...
fi

for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  suffix=""
  [[ "$goos" == "windows" ]] && suffix=".exe"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO_BIN" build \
    -trimpath -o "$TMP_DIR/zhp2pproxy-$goos-$goarch$suffix" ./cmd/proxy
done

printf 'all repository checks passed\n'
