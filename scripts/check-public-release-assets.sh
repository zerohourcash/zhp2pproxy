#!/usr/bin/env bash
set -Eeuo pipefail

release_dir="${1:-}"
[[ -n "$release_dir" && -d "$release_dir" ]] || {
  printf 'usage: %s RELEASE_DIRECTORY\n' "$0" >&2
  exit 1
}

expected="$({
  printf '%s\n' \
    update.json \
    update.json.sig \
    zhp2pproxy-linux-amd64.bin \
    zhp2pproxy-linux-arm64.bin \
    zhp2pproxy-windows-amd64.exe \
    zhp2pproxy-windows-arm64.exe
} | sort)"
actual="$(find "$release_dir" -maxdepth 1 -type f -printf '%f\n' | sort)"

if [[ "$actual" != "$expected" ]]; then
  printf 'unexpected public release assets\nexpected:\n%s\nactual:\n%s\n' \
    "$expected" "$actual" >&2
  exit 1
fi

printf 'public release assets are clean: 6 required files\n'
