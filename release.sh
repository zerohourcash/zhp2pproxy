#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
PUBLISH_DIR="${PUBLISH_DIR:-}"
GO_BIN="${GO_BIN:-go}"
SOURCE_COMMIT="$(git -C "$ROOT" rev-parse HEAD)"
SOURCE_TAG="$(git -C "$ROOT" describe --tags --exact-match HEAD 2>/dev/null || true)"
VERSION="${VERSION:-${SOURCE_TAG#v}}"
VERSION="${VERSION:-$(date -u +%Y%m%d)-$(git -C "$ROOT" rev-parse --short=12 HEAD)}"
RELEASE_TAG="${SOURCE_TAG:-v$VERSION}"
OFFICIAL_REPOSITORY="zerohourcash/zhp2pproxy"
RELEASE_ASSET_BASE="https://github.com/$OFFICIAL_REPOSITORY/releases/download/$RELEASE_TAG"
BUILD_SEQUENCE="${BUILD_SEQUENCE:-$(date -u +%Y%m%d%H%M%S)}"
UPDATE_SIGNING_KEY="${UPDATE_SIGNING_KEY:-}"
WINDOWS_SIGN_COMMAND="${WINDOWS_SIGN_COMMAND:-}"
WINDOWS_VERIFY_COMMAND="${WINDOWS_VERIFY_COMMAND:-}"
RELEASE_WINDOWS="${RELEASE_WINDOWS:-1}"
ALLOW_UNSIGNED_WINDOWS_RELEASE="${ALLOW_UNSIGNED_WINDOWS_RELEASE:-0}"
ALLOW_DIRTY_LOCAL_RELEASE="${ALLOW_DIRTY_LOCAL_RELEASE:-0}"
ARCHES=(amd64 arm64)
TMP_DIR=""

[[ "$VERSION" =~ ^[0-9A-Za-z._-]+$ ]] || {
  printf 'version contains unsupported characters: %s\n' "$VERSION" >&2
  exit 1
}
[[ "$BUILD_SEQUENCE" =~ ^[0-9]{14,18}$ ]] || {
  printf 'BUILD_SEQUENCE must contain 14..18 decimal digits; use a monotonic UTC value such as YYYYMMDDhhmmss\n' >&2
  exit 1
}
[[ "$RELEASE_WINDOWS" == "0" || "$RELEASE_WINDOWS" == "1" ]] || {
  printf 'RELEASE_WINDOWS must be 0 or 1\n' >&2
  exit 1
}
[[ "$ALLOW_UNSIGNED_WINDOWS_RELEASE" == "0" || "$ALLOW_UNSIGNED_WINDOWS_RELEASE" == "1" ]] || {
  printf 'ALLOW_UNSIGNED_WINDOWS_RELEASE must be 0 or 1\n' >&2
  exit 1
}

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhp2pproxy-release.XXXXXX")"

cleanup() {
  if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
    find "$TMP_DIR" -type f -delete
    find "$TMP_DIR" -depth -type d -empty -delete
    rmdir "$TMP_DIR" 2>/dev/null || true
  fi
}
trap cleanup EXIT

for required in README.md LICENSE NOTICE SECURITY.md CONTRIBUTING.md THREAT_MODEL.md \
  install.sh install.ps1 proxy.install.json zhp2pproxy.service.in; do
  [[ -f "$ROOT/$required" ]] || {
    printf 'missing release file: %s\n' "$required" >&2
    exit 1
  }
done

if [[ -n "$PUBLISH_DIR" ]]; then
  [[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]] || {
    printf 'refusing to publish from a dirty Git worktree\n' >&2
    exit 1
  }
  [[ -n "$UPDATE_SIGNING_KEY" ]] || {
    printf 'refusing to publish without UPDATE_SIGNING_KEY\n' >&2
    exit 1
  }
  if [[ "$RELEASE_WINDOWS" == "1" ]]; then
    if [[ "$ALLOW_UNSIGNED_WINDOWS_RELEASE" != "1" ]]; then
      [[ -n "$WINDOWS_SIGN_COMMAND" ]] || {
        printf 'refusing to publish Windows without WINDOWS_SIGN_COMMAND\n' >&2
        exit 1
      }
      [[ -n "$WINDOWS_VERIFY_COMMAND" ]] || {
        printf 'refusing to publish Windows without WINDOWS_VERIFY_COMMAND\n' >&2
        exit 1
      }
    fi
  fi
  [[ -n "$SOURCE_TAG" ]] || {
    printf 'refusing to publish a commit without an exact Git tag\n' >&2
    exit 1
  }
elif [[ "$ALLOW_DIRTY_LOCAL_RELEASE" != "1" && -n "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]]; then
  printf 'refusing release build from a dirty Git worktree; commit first or set ALLOW_DIRTY_LOCAL_RELEASE=1 for local testing\n' >&2
  exit 1
fi

need_release_command() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'required release command is missing: %s\n' "$1" >&2
    exit 1
  }
}

need_release_command zip

THIRD_PARTY_DIR="$TMP_DIR/THIRD_PARTY_LICENSES"
install -d -m 0755 "$THIRD_PARTY_DIR"
printf 'Module\tVersion\tLicense files\n' >"$THIRD_PARTY_DIR/INDEX.tsv"
while IFS='|' read -r module module_version module_dir; do
  [[ -n "$module" && -n "$module_dir" ]] || continue
  safe_module="$(printf '%s' "$module" | tr '/:' '__')"
  module_license_dir="$THIRD_PARTY_DIR/$safe_module"
  install -d -m 0755 "$module_license_dir"
  license_count=0
  license_names=()
  while IFS= read -r -d '' license_file; do
    license_name="$(basename "$license_file")"
    install -m 0644 "$license_file" "$module_license_dir/$license_name"
    license_names+=("$license_name")
    license_count=$((license_count + 1))
  done < <(find "$module_dir" -maxdepth 1 -type f \
    \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) -print0)
  if [[ "$license_count" == "0" ]]; then
    printf 'dependency has no discoverable root license: %s %s\n' "$module" "$module_version" >&2
    exit 1
  fi
  license_list="$(IFS=,; printf '%s' "${license_names[*]}")"
  printf '%s\t%s\t%s\n' "$module" "$module_version" "$safe_module/$license_list" \
    >>"$THIRD_PARTY_DIR/INDEX.tsv"
done < <("$GO_BIN" list -m -f '{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}' all)

sign_windows_binary() {
  local binary="$1"
  if [[ -n "$WINDOWS_SIGN_COMMAND" ]]; then
    [[ -x "$WINDOWS_SIGN_COMMAND" ]] || {
      printf 'WINDOWS_SIGN_COMMAND is not executable: %s\n' "$WINDOWS_SIGN_COMMAND" >&2
      exit 1
    }
    "$WINDOWS_SIGN_COMMAND" "$binary"
    [[ -n "$WINDOWS_VERIFY_COMMAND" && -x "$WINDOWS_VERIFY_COMMAND" ]] || {
      printf 'WINDOWS_VERIFY_COMMAND is required and must be executable when signing Windows binaries\n' >&2
      exit 1
    }
    "$WINDOWS_VERIFY_COMMAND" "$binary"
    return
  fi
  if [[ -n "$PUBLISH_DIR" ]]; then
    if [[ "$ALLOW_UNSIGNED_WINDOWS_RELEASE" == "1" ]]; then
      printf 'warning: publishing unsigned Windows binaries by explicit operator request\n' >&2
      return
    fi
    printf 'refusing to publish unsigned Windows binaries; set WINDOWS_SIGN_COMMAND or explicitly allow an unsigned release\n' >&2
    exit 1
  fi
  printf 'warning: local Windows build is unsigned and must not be publicly distributed\n' >&2
}

mkdir -p "$DIST_DIR"
for arch in "${ARCHES[@]}"; do
  package="zhp2pproxy-linux-$arch"
  package_dir="$TMP_DIR/$package"
  mkdir "$package_dir"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO_BIN" build \
      -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.buildSequence=$BUILD_SEQUENCE" \
      -o "$package_dir/zhp2pproxy" ./cmd/proxy
  )
  install -m 0644 "$ROOT/README.md" "$package_dir/README.md"
  install -m 0644 "$ROOT/LICENSE" "$package_dir/LICENSE"
  install -m 0644 "$ROOT/NOTICE" "$package_dir/NOTICE"
  install -m 0644 "$ROOT/SECURITY.md" "$package_dir/SECURITY.md"
  install -m 0644 "$ROOT/THREAT_MODEL.md" "$package_dir/THREAT_MODEL.md"
  cp -R "$THIRD_PARTY_DIR" "$package_dir/THIRD_PARTY_LICENSES"
  install -m 0600 "$ROOT/proxy.install.json" "$package_dir/proxy.install.json"
  install -m 0644 "$ROOT/zhp2pproxy.service.in" "$package_dir/zhp2pproxy.service.in"
  tar --create --gzip --file "$TMP_DIR/$package.tar.gz" \
    --directory "$TMP_DIR" "$package"
  install -m 0755 "$package_dir/zhp2pproxy" "$TMP_DIR/$package.bin"
done

if [[ "$RELEASE_WINDOWS" == "1" ]]; then
  for arch in "${ARCHES[@]}"; do
    package="zhp2pproxy-windows-$arch"
    package_dir="$TMP_DIR/$package"
    mkdir "$package_dir"
    (
      cd "$ROOT"
      CGO_ENABLED=0 GOOS=windows GOARCH="$arch" "$GO_BIN" build \
        -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.buildSequence=$BUILD_SEQUENCE" \
        -o "$package_dir/zhp2pproxy.exe" ./cmd/proxy
    )
    sign_windows_binary "$package_dir/zhp2pproxy.exe"
    install -m 0644 "$ROOT/README.md" "$package_dir/README.md"
    install -m 0644 "$ROOT/LICENSE" "$package_dir/LICENSE"
    install -m 0644 "$ROOT/NOTICE" "$package_dir/NOTICE"
    install -m 0644 "$ROOT/SECURITY.md" "$package_dir/SECURITY.md"
    install -m 0644 "$ROOT/THREAT_MODEL.md" "$package_dir/THREAT_MODEL.md"
    cp -R "$THIRD_PARTY_DIR" "$package_dir/THIRD_PARTY_LICENSES"
    install -m 0600 "$ROOT/proxy.install.json" "$package_dir/proxy.install.json"
    install -m 0755 "$package_dir/zhp2pproxy.exe" "$TMP_DIR/$package.exe"
    (
      cd "$TMP_DIR"
      zip -q -r "$package.zip" "$package"
    )
  done
fi

tar --create --gzip --file "$TMP_DIR/THIRD_PARTY_LICENSES.tar.gz" \
  --directory "$TMP_DIR" THIRD_PARTY_LICENSES

printf '%s\n' "$SOURCE_COMMIT" >"$TMP_DIR/SOURCE_COMMIT"
(
  cd "$ROOT"
  "$GO_BIN" list -m -json all >"$TMP_DIR/SBOM-go-modules.json"
)
for release_document in README.md LICENSE NOTICE SECURITY.md THREAT_MODEL.md CONTRIBUTING.md install.sh install.ps1; do
  install -m 0644 "$ROOT/$release_document" "$TMP_DIR/$release_document"
done

release_files=(
  zhp2pproxy-linux-amd64.bin
  zhp2pproxy-linux-arm64.bin
  zhp2pproxy-linux-amd64.tar.gz
  zhp2pproxy-linux-arm64.tar.gz
  THIRD_PARTY_LICENSES.tar.gz
  SOURCE_COMMIT SBOM-go-modules.json README.md LICENSE NOTICE SECURITY.md
  THREAT_MODEL.md CONTRIBUTING.md install.sh
)
if [[ "$RELEASE_WINDOWS" == "1" ]]; then
  release_files+=(
    zhp2pproxy-windows-amd64.exe
    zhp2pproxy-windows-arm64.exe
    zhp2pproxy-windows-amd64.zip
    zhp2pproxy-windows-arm64.zip
    install.ps1
  )
fi
(
  cd "$TMP_DIR"
  sha256sum "${release_files[@]}" >SHA256SUMS
)

if [[ -n "$UPDATE_SIGNING_KEY" ]]; then
	need_release_command openssl
	need_release_command base64
  [[ -f "$UPDATE_SIGNING_KEY" ]] || {
    printf 'update signing key does not exist: %s\n' "$UPDATE_SIGNING_KEY" >&2
    exit 1
  }
  signing_public_key="$(openssl pkey -in "$UPDATE_SIGNING_KEY" -pubout -outform DER 2>/dev/null | tail -c 32 | base64 -w0)"
  [[ "$signing_public_key" == "GnbiMTDyAI1NenxfEyJZihgFGsNUWmSVkke1Z6PweJA=" ]] || {
    printf 'UPDATE_SIGNING_KEY does not match the public key pinned by released clients\n' >&2
    exit 1
  }
  linux_amd64_sha="$(awk '$2 == "zhp2pproxy-linux-amd64.bin" {print $1}' "$TMP_DIR/SHA256SUMS")"
  linux_arm64_sha="$(awk '$2 == "zhp2pproxy-linux-arm64.bin" {print $1}' "$TMP_DIR/SHA256SUMS")"
  if [[ "$RELEASE_WINDOWS" == "1" ]]; then
    windows_amd64_sha="$(awk '$2 == "zhp2pproxy-windows-amd64.exe" {print $1}' "$TMP_DIR/SHA256SUMS")"
    windows_arm64_sha="$(awk '$2 == "zhp2pproxy-windows-arm64.exe" {print $1}' "$TMP_DIR/SHA256SUMS")"
    printf '{"version":"%s","sequence":%s,"published_at":"%s","downloads":{"linux-amd64":{"url":"%s/zhp2pproxy-linux-amd64.bin","sha256":"%s"},"linux-arm64":{"url":"%s/zhp2pproxy-linux-arm64.bin","sha256":"%s"},"windows-amd64":{"url":"%s/zhp2pproxy-windows-amd64.exe","sha256":"%s"},"windows-arm64":{"url":"%s/zhp2pproxy-windows-arm64.exe","sha256":"%s"}}}\n' \
      "$VERSION" "$BUILD_SEQUENCE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "$RELEASE_ASSET_BASE" "$linux_amd64_sha" \
      "$RELEASE_ASSET_BASE" "$linux_arm64_sha" \
      "$RELEASE_ASSET_BASE" "$windows_amd64_sha" \
      "$RELEASE_ASSET_BASE" "$windows_arm64_sha" \
      >"$TMP_DIR/update.json"
  else
    printf '{"version":"%s","sequence":%s,"published_at":"%s","downloads":{"linux-amd64":{"url":"%s/zhp2pproxy-linux-amd64.bin","sha256":"%s"},"linux-arm64":{"url":"%s/zhp2pproxy-linux-arm64.bin","sha256":"%s"}}}\n' \
      "$VERSION" "$BUILD_SEQUENCE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "$RELEASE_ASSET_BASE" "$linux_amd64_sha" \
      "$RELEASE_ASSET_BASE" "$linux_arm64_sha" \
      >"$TMP_DIR/update.json"
  fi
  openssl pkeyutl -sign -rawin -inkey "$UPDATE_SIGNING_KEY" \
    -in "$TMP_DIR/update.json" -out "$TMP_DIR/update.json.sig.raw"
  base64 -w0 "$TMP_DIR/update.json.sig.raw" >"$TMP_DIR/update.json.sig"
  printf '\n' >>"$TMP_DIR/update.json.sig"
  openssl pkeyutl -sign -rawin -inkey "$UPDATE_SIGNING_KEY" \
    -in "$TMP_DIR/SHA256SUMS" -out "$TMP_DIR/SHA256SUMS.sig.raw"
  base64 -w0 "$TMP_DIR/SHA256SUMS.sig.raw" >"$TMP_DIR/SHA256SUMS.sig"
  printf '\n' >>"$TMP_DIR/SHA256SUMS.sig"
elif [[ -n "$PUBLISH_DIR" ]]; then
  printf 'refusing to publish without UPDATE_SIGNING_KEY\n' >&2
  exit 1
fi

for file in "${release_files[@]}" SHA256SUMS; do
  install -m 0644 "$TMP_DIR/$file" "$DIST_DIR/$file"
done
if [[ -f "$TMP_DIR/update.json" ]]; then
  install -m 0644 "$TMP_DIR/update.json" "$DIST_DIR/update.json"
  install -m 0644 "$TMP_DIR/update.json.sig" "$DIST_DIR/update.json.sig"
  install -m 0644 "$TMP_DIR/SHA256SUMS.sig" "$DIST_DIR/SHA256SUMS.sig"
fi
chmod 0755 "$DIST_DIR/install.sh"

if [[ -n "$PUBLISH_DIR" ]]; then
  install -d -m 0755 "$PUBLISH_DIR"
  public_release_files=(
    zhp2pproxy-linux-amd64.bin
    zhp2pproxy-linux-arm64.bin
  )
  if [[ "$RELEASE_WINDOWS" == "1" ]]; then
    public_release_files=(
      zhp2pproxy-windows-amd64.exe
      zhp2pproxy-windows-arm64.exe
      "${public_release_files[@]}"
    )
  fi
  for file in "${public_release_files[@]}"; do
    install -m 0755 "$DIST_DIR/$file" "$PUBLISH_DIR/$file"
  done
  install -m 0644 "$DIST_DIR/update.json" "$PUBLISH_DIR/update.json"
  install -m 0644 "$DIST_DIR/update.json.sig" "$PUBLISH_DIR/update.json.sig"
fi

printf 'release complete: version=%s\n' "$VERSION"
cat "$DIST_DIR/SHA256SUMS"
