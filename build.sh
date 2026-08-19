#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

VERSION="${VERSION:-dev}"
GO_BIN="${GO_BIN:-go}"
if [[ -x /usr/local/go/bin/go ]]; then
  GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"
fi
if [[ "${GO_BIN}" == "go" && -x /usr/local/go/bin/go ]]; then
  GO_BIN="/usr/local/go/bin/go"
fi

CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o zhp2pproxy ./cmd/proxy
