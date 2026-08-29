#!/usr/bin/env bash
set -Eeuo pipefail

PROGRAM="zhp2pproxy"
SERVICE_NAME="zhp2pproxy.service"
RELEASE_API_URL="${ZHP2P_RELEASE_API_URL:-https://api.github.com/repos/zerohourcash/zhp2pproxy/releases/latest}"
RESOURCE_BASE_URL="${ZHP2P_RESOURCE_BASE_URL:-https://link.zeroscan.st/zhp2pproxy}"
TARGET_USER="${ZHP2P_USER:-}"
SKIP_SYSTEMD="${ZHP2P_SKIP_SYSTEMD:-0}"

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

[[ "$RELEASE_API_URL" == https://api.github.com/repos/zerohourcash/zhp2pproxy/releases/* ]] || fail "release metadata URL must use the official GitHub API"
[[ "$RESOURCE_BASE_URL" == https://* ]] || fail "installer resource URL must use https://"

need_command sha256sum
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
BINARY_NAME="zhp2pproxy-linux-${ARCH}.bin"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhp2pproxy-install.XXXXXX")"
cleanup() {
  rm -f "$TMP_DIR/$BINARY_NAME" "$TMP_DIR/release.json" \
    "$TMP_DIR/proxy.install.json" "$TMP_DIR/zhp2pproxy.service.in" \
    "$TMP_DIR/rendered.service"
  rmdir "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

download "$RELEASE_API_URL" "$TMP_DIR/release.json"
ASSET_URL="$(
  tr ',' '\n' <"$TMP_DIR/release.json" | awk -F'"' -v asset="$BINARY_NAME" '
    $0 ~ "\"name\"[[:space:]]*:[[:space:]]*\"" asset "\"" { wanted = 1 }
    wanted && $2 == "browser_download_url" { print $4; exit }
  '
)"
EXPECTED_SHA256="$(
  tr ',' '\n' <"$TMP_DIR/release.json" | awk -F'"' -v asset="$BINARY_NAME" '
    $0 ~ "\"name\"[[:space:]]*:[[:space:]]*\"" asset "\"" { wanted = 1 }
    wanted && $2 == "digest" && $4 ~ /^sha256:[0-9a-fA-F]{64}$/ {
      sub(/^sha256:/, "", $4)
      print $4
      exit
    }
  '
)"
[[ "$ASSET_URL" =~ ^https://github\.com/zerohourcash/zhp2pproxy/releases/download/[^/]+/${BINARY_NAME}$ ]] || fail "official release metadata has no valid URL for $BINARY_NAME"
[[ "$EXPECTED_SHA256" =~ ^[0-9a-fA-F]{64}$ ]] || fail "official release metadata has no SHA-256 digest for $BINARY_NAME"

log "downloading $BINARY_NAME"
download "$ASSET_URL" "$TMP_DIR/$BINARY_NAME"
ACTUAL_SHA256="$(sha256sum "$TMP_DIR/$BINARY_NAME" | awk '{print $1}')"
[[ "$ACTUAL_SHA256" == "$EXPECTED_SHA256" ]] || fail "SHA-256 validation failed for $BINARY_NAME"
log "verified official GitHub asset SHA-256: $ACTUAL_SHA256"

download "$RESOURCE_BASE_URL/proxy.install.json" "$TMP_DIR/proxy.install.json"
download "$RESOURCE_BASE_URL/zhp2pproxy.service.in" "$TMP_DIR/zhp2pproxy.service.in"
[[ -s "$TMP_DIR/proxy.install.json" ]] || fail "safe config template is unavailable"
[[ -s "$TMP_DIR/zhp2pproxy.service.in" ]] || fail "systemd template is unavailable"

install -d -m 0750 -o "$TARGET_USER" -g "$TARGET_GROUP" "$INSTALL_DIR"
if [[ -x "$INSTALL_DIR/zhp2pproxy" ]]; then
  install -m 0755 -o "$TARGET_USER" -g "$TARGET_GROUP" \
    "$INSTALL_DIR/zhp2pproxy" "$INSTALL_DIR/zhp2pproxy.previous"
fi
install -m 0755 -o "$TARGET_USER" -g "$TARGET_GROUP" \
  "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/zhp2pproxy"
install -m 0600 -o "$TARGET_USER" -g "$TARGET_GROUP" \
  "$TMP_DIR/proxy.install.json" "$INSTALL_DIR/proxy.example.json"

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
      "$TMP_DIR/proxy.install.json" "$INSTALL_DIR/proxy.json"
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
UNIT_TEMPLATE="$TMP_DIR/zhp2pproxy.service.in"
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
