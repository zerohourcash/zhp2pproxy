#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
PUBLISH_DIR="${PUBLISH_DIR:-}"
GO_BIN="${GO_BIN:-go}"
VERSION="${VERSION:-$(date -u +%Y%m%d)-$(git -C "$ROOT" rev-parse --short=12 HEAD)}"
ARCHES=(amd64 arm64)
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhp2pproxy-release.XXXXXX")"

cleanup() {
  if [[ -d "$TMP_DIR" ]]; then
    find "$TMP_DIR" -type f -delete
    find "$TMP_DIR" -depth -type d -empty -delete
    rmdir "$TMP_DIR" 2>/dev/null || true
  fi
}
trap cleanup EXIT

for required in README.md install.sh install.ps1 proxy.install.json zhp2pproxy.service.in; do
  [[ -f "$ROOT/$required" ]] || {
    printf 'missing release file: %s\n' "$required" >&2
    exit 1
  }
done

need_release_command() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'required release command is missing: %s\n' "$1" >&2
    exit 1
  }
}

need_release_command zip

mkdir -p "$DIST_DIR"
for arch in "${ARCHES[@]}"; do
  package="zhp2pproxy-linux-$arch"
  package_dir="$TMP_DIR/$package"
  mkdir "$package_dir"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO_BIN" build \
      -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$package_dir/zhp2pproxy" ./cmd/proxy
  )
  install -m 0644 "$ROOT/README.md" "$package_dir/README.md"
  install -m 0600 "$ROOT/proxy.install.json" "$package_dir/proxy.install.json"
  install -m 0644 "$ROOT/zhp2pproxy.service.in" "$package_dir/zhp2pproxy.service.in"
  tar --create --gzip --file "$TMP_DIR/$package.tar.gz" \
    --directory "$TMP_DIR" "$package"
done

for arch in "${ARCHES[@]}"; do
  package="zhp2pproxy-windows-$arch"
  package_dir="$TMP_DIR/$package"
  mkdir "$package_dir"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=windows GOARCH="$arch" "$GO_BIN" build \
      -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$package_dir/zhp2pproxy.exe" ./cmd/proxy
  )
  install -m 0644 "$ROOT/README.md" "$package_dir/README.md"
  install -m 0600 "$ROOT/proxy.install.json" "$package_dir/proxy.install.json"
  install -m 0755 "$package_dir/zhp2pproxy.exe" "$TMP_DIR/$package.exe"
  (
    cd "$TMP_DIR"
    zip -q -r "$package.zip" "$package"
  )
done

(
  cd "$TMP_DIR"
  sha256sum \
    zhp2pproxy-linux-amd64.tar.gz \
    zhp2pproxy-linux-arm64.tar.gz \
    zhp2pproxy-windows-amd64.exe \
    zhp2pproxy-windows-arm64.exe \
    zhp2pproxy-windows-amd64.zip \
    zhp2pproxy-windows-arm64.zip >SHA256SUMS
)

for file in \
  zhp2pproxy-linux-amd64.tar.gz \
  zhp2pproxy-linux-arm64.tar.gz \
  zhp2pproxy-windows-amd64.exe \
  zhp2pproxy-windows-arm64.exe \
  zhp2pproxy-windows-amd64.zip \
  zhp2pproxy-windows-arm64.zip \
  SHA256SUMS; do
  install -m 0644 "$TMP_DIR/$file" "$DIST_DIR/$file"
done
install -m 0755 "$ROOT/install.sh" "$DIST_DIR/install.sh"
install -m 0644 "$ROOT/install.ps1" "$DIST_DIR/install.ps1"
install -m 0644 "$ROOT/README.md" "$DIST_DIR/README.md"

if [[ -n "$PUBLISH_DIR" ]]; then
  install -d -m 0755 "$PUBLISH_DIR"
  for file in \
    zhp2pproxy-linux-amd64.tar.gz \
    zhp2pproxy-linux-arm64.tar.gz \
    zhp2pproxy-windows-amd64.exe \
    zhp2pproxy-windows-arm64.exe \
    zhp2pproxy-windows-amd64.zip \
    zhp2pproxy-windows-arm64.zip \
    SHA256SUMS README.md install.ps1; do
    install -m 0644 "$DIST_DIR/$file" "$PUBLISH_DIR/$file"
  done
  install -m 0755 "$DIST_DIR/install.sh" "$PUBLISH_DIR/install.sh"
fi

printf 'release complete: version=%s\n' "$VERSION"
cat "$DIST_DIR/SHA256SUMS"
