#!/usr/bin/env bash
set -Eeuo pipefail

PROGRAM="zhp2pproxy"
SERVICE_NAME="zhp2pproxy.service"
BASE_URL="${ZHP2P_BASE_URL:-https://link.zeroscan.st/zhp2pproxy}"
TARGET_USER="${ZHP2P_USER:-}"
SKIP_SYSTEMD="${ZHP2P_SKIP_SYSTEMD:-0}"
UPDATE_PUBLIC_KEY_PEM='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAGnbiMTDyAI1NenxfEyJZihgFGsNUWmSVkke1Z6PweJA=
-----END PUBLIC KEY-----'

log() {
  printf '[zhp2pproxy-install] %s\n' "$*"
}

fail() {
  printf '[zhp2pproxy-install] ERROR: %s\n' "$*" >&2
  exit 1
}

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is missing: $1"
}

download() {
  local url="$1"
  local destination="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --retry 3 --retry-delay 1 --connect-timeout 10 --max-time 180 \
      --output "$destination" "$url"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget --https-only --tries=3 --timeout=30 --output-document="$destination" "$url"
    return
  fi
  fail "curl or wget is required"
}

escape_sed_replacement() {
  printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'
}

resolve_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) printf 'amd64' ;;
    aarch64 | arm64) printf 'arm64' ;;
    *) fail "unsupported CPU architecture: $(uname -m)" ;;
  esac
}

if [[ "$SKIP_SYSTEMD" != "1" && "$EUID" -ne 0 ]]; then
  fail "run as root to install and enable the systemd service"
fi

[[ "$BASE_URL" == https://* ]] || fail "download base URL must use https://"

need_command tar
need_command sha256sum
need_command openssl
need_command base64
need_command install
need_command sed
need_command getent
need_command awk
need_command cut
need_command find
need_command mktemp

CREATED_SERVICE_USER=0
if [[ -z "$TARGET_USER" ]]; then
  if [[ "$EUID" -eq 0 && "$SKIP_SYSTEMD" != "1" ]]; then
    if [[ -f /root/zhp2pproxy/proxy.json && -f /etc/systemd/system/zhp2pproxy.service ]]; then
      TARGET_USER="root"
      log "preserving the existing root-owned installation; use ZHP2P_USER=zhp2pproxy for an explicit privilege migration"
    else
      TARGET_USER="zhp2pproxy"
      if ! getent passwd "$TARGET_USER" >/dev/null; then
        need_command useradd
        NOLOGIN_SHELL="$(command -v nologin || true)"
        [[ -n "$NOLOGIN_SHELL" ]] || NOLOGIN_SHELL="/usr/sbin/nologin"
        useradd --system --user-group --create-home \
          --home-dir /var/lib/zhp2pproxy --shell "$NOLOGIN_SHELL" "$TARGET_USER"
        CREATED_SERVICE_USER=1
        log "created unprivileged system user $TARGET_USER"
      fi
    fi
  else
    TARGET_USER="$(id -un)"
  fi
fi

TARGET_PASSWD="$(getent passwd "$TARGET_USER" || true)"
[[ -n "$TARGET_PASSWD" ]] || fail "system user does not exist: $TARGET_USER"
TARGET_GROUP="${ZHP2P_GROUP:-$(id -gn "$TARGET_USER")}"
TARGET_HOME="${ZHP2P_HOME:-$(printf '%s' "$TARGET_PASSWD" | cut -d: -f6)}"
[[ -n "$TARGET_HOME" && "$TARGET_HOME" == /* ]] || fail "invalid home directory for $TARGET_USER"
if [[ "$TARGET_USER" == "zhp2pproxy" && -z "${ZHP2P_INSTALL_DIR:-}" ]]; then
  INSTALL_DIR="$TARGET_HOME"
else
  INSTALL_DIR="${ZHP2P_INSTALL_DIR:-$TARGET_HOME/zhp2pproxy}"
fi
[[ "$INSTALL_DIR" == /* ]] || fail "installation directory must be absolute"
[[ "$INSTALL_DIR" != "/" ]] || fail "refusing to use / as installation directory"

ARCH="$(resolve_arch)"
ARCHIVE="zhp2pproxy-linux-${ARCH}.tar.gz"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhp2pproxy-install.XXXXXX")"
cleanup() {
  rm -f "$TMP_DIR/$ARCHIVE" "$TMP_DIR/SHA256SUMS" \
    "$TMP_DIR/SHA256SUMS.sig" "$TMP_DIR/SHA256SUMS.sig.raw" \
    "$TMP_DIR/update-public.pem" "$TMP_DIR/rendered.service"
  if [[ -d "$TMP_DIR/unpacked" ]]; then
    find "$TMP_DIR/unpacked" -type f -delete
    find "$TMP_DIR/unpacked" -depth -type d -empty -delete
  fi
  rmdir "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

log "downloading $ARCHIVE"
download "$BASE_URL/$ARCHIVE" "$TMP_DIR/$ARCHIVE"
download "$BASE_URL/SHA256SUMS" "$TMP_DIR/SHA256SUMS"
download "$BASE_URL/SHA256SUMS.sig" "$TMP_DIR/SHA256SUMS.sig"

printf '%s\n' "$UPDATE_PUBLIC_KEY_PEM" >"$TMP_DIR/update-public.pem"
base64 --decode "$TMP_DIR/SHA256SUMS.sig" >"$TMP_DIR/SHA256SUMS.sig.raw" \
  || fail "release checksum signature is not valid base64"
openssl pkeyutl -verify -pubin -rawin \
  -inkey "$TMP_DIR/update-public.pem" \
  -in "$TMP_DIR/SHA256SUMS" \
  -sigfile "$TMP_DIR/SHA256SUMS.sig.raw" >/dev/null \
  || fail "release checksum signature verification failed"
log "verified official Ed25519 release signature"

EXPECTED_LINE="$(awk -v file="$ARCHIVE" '$2 == file || $2 == "*" file { print $1 "  " file; exit }' "$TMP_DIR/SHA256SUMS")"
[[ -n "$EXPECTED_LINE" ]] || fail "checksum for $ARCHIVE is missing"
(
  cd "$TMP_DIR"
  printf '%s\n' "$EXPECTED_LINE" | sha256sum --check --status -
) || fail "checksum validation failed for $ARCHIVE"

mkdir "$TMP_DIR/unpacked"
tar --extract --gzip --file "$TMP_DIR/$ARCHIVE" \
  --directory "$TMP_DIR/unpacked" --no-same-owner --no-same-permissions
PACKAGE_DIR="$TMP_DIR/unpacked/zhp2pproxy-linux-${ARCH}"
[[ -x "$PACKAGE_DIR/zhp2pproxy" ]] || fail "archive does not contain the executable"
[[ -f "$PACKAGE_DIR/proxy.install.json" ]] || fail "archive does not contain the safe config template"
[[ -f "$PACKAGE_DIR/zhp2pproxy.service.in" ]] || fail "archive does not contain the systemd template"

install -d -m 0750 -o "$TARGET_USER" -g "$TARGET_GROUP" "$INSTALL_DIR"
if [[ -x "$INSTALL_DIR/zhp2pproxy" ]]; then
  install -m 0755 -o "$TARGET_USER" -g "$TARGET_GROUP" \
    "$INSTALL_DIR/zhp2pproxy" "$INSTALL_DIR/zhp2pproxy.previous"
fi
install -m 0755 -o "$TARGET_USER" -g "$TARGET_GROUP" \
  "$PACKAGE_DIR/zhp2pproxy" "$INSTALL_DIR/zhp2pproxy"
install -m 0644 -o "$TARGET_USER" -g "$TARGET_GROUP" \
  "$PACKAGE_DIR/README.md" "$INSTALL_DIR/README.md"
install -m 0600 -o "$TARGET_USER" -g "$TARGET_GROUP" \
  "$PACKAGE_DIR/proxy.install.json" "$INSTALL_DIR/proxy.example.json"

if [[ ! -f "$INSTALL_DIR/proxy.json" ]]; then
  LEGACY_RUNTIME_DIR="${ZHP2P_LEGACY_RUNTIME_DIR:-/root/zhp2pproxy}"
  if [[ "$TARGET_USER" == "zhp2pproxy" && "$LEGACY_RUNTIME_DIR" != "$INSTALL_DIR" && -f "$LEGACY_RUNTIME_DIR/proxy.json" ]]; then
    install -m 0600 -o "$TARGET_USER" -g "$TARGET_GROUP" \
      "$LEGACY_RUNTIME_DIR/proxy.json" "$INSTALL_DIR/proxy.json"
    for runtime_file in identity.key peers.json; do
      if [[ -f "$LEGACY_RUNTIME_DIR/$runtime_file" ]]; then
        install -m 0600 -o "$TARGET_USER" -g "$TARGET_GROUP" \
          "$LEGACY_RUNTIME_DIR/$runtime_file" "$INSTALL_DIR/$runtime_file"
      fi
    done
    log "migrated existing runtime state from $LEGACY_RUNTIME_DIR"
  else
    install -m 0600 -o "$TARGET_USER" -g "$TARGET_GROUP" \
      "$PACKAGE_DIR/proxy.install.json" "$INSTALL_DIR/proxy.json"
    log "created $INSTALL_DIR/proxy.json"
  fi
else
  log "kept existing proxy.json, identity.key and peers.json"
fi

"$INSTALL_DIR/zhp2pproxy" -version

if [[ "$SKIP_SYSTEMD" == "1" ]]; then
  log "systemd installation skipped by ZHP2P_SKIP_SYSTEMD=1"
  log "installed in $INSTALL_DIR"
  exit 0
fi

need_command systemctl
need_command seq
UNIT_TEMPLATE="$PACKAGE_DIR/zhp2pproxy.service.in"
USER_REPL="$(escape_sed_replacement "$TARGET_USER")"
GROUP_REPL="$(escape_sed_replacement "$TARGET_GROUP")"
HOME_REPL="$(escape_sed_replacement "$TARGET_HOME")"
DIR_REPL="$(escape_sed_replacement "$INSTALL_DIR")"
sed \
  -e "s|@USER@|$USER_REPL|g" \
  -e "s|@GROUP@|$GROUP_REPL|g" \
  -e "s|@HOME@|$HOME_REPL|g" \
  -e "s|@INSTALL_DIR@|$DIR_REPL|g" \
  "$UNIT_TEMPLATE" >"$TMP_DIR/rendered.service"

install -m 0644 "$TMP_DIR/rendered.service" "/etc/systemd/system/$SERVICE_NAME"
systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"

for _attempt in $(seq 1 20); do
  if systemctl is-active --quiet "$SERVICE_NAME"; then
    break
  fi
  sleep 0.5
done
systemctl is-active --quiet "$SERVICE_NAME" || {
  systemctl status "$SERVICE_NAME" --no-pager -l || true
  fail "$SERVICE_NAME did not become active"
}

log "service is active and enabled at boot"
if [[ "$CREATED_SERVICE_USER" == "1" ]]; then
  log "service runs as the unprivileged user $TARGET_USER"
fi
log "installation directory: $INSTALL_DIR"
log "config: $INSTALL_DIR/proxy.json"
log "status: systemctl status $SERVICE_NAME --no-pager -l"
log "logs: journalctl -u $SERVICE_NAME -f"
log "local API: http://127.0.0.1:8081/info"
log "for a public relay, allow TCP/UDP 10001 in the server firewall"
