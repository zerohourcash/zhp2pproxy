#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG="${1:-$ROOT/catalog/chains.json}"
SIGNING_KEY="${UPDATE_SIGNING_KEY:-}"
PINNED_PUBLIC_KEY="GnbiMTDyAI1NenxfEyJZihgFGsNUWmSVkke1Z6PweJA="

[[ -f "$CATALOG" ]] || { printf 'catalog not found: %s\n' "$CATALOG" >&2; exit 1; }
[[ -n "$SIGNING_KEY" && -f "$SIGNING_KEY" ]] || {
  printf 'set UPDATE_SIGNING_KEY to the official offline Ed25519 PEM path\n' >&2
  exit 1
}
jq -e '.sequence > 0 and (.chains | length > 0 and length <= 64)' "$CATALOG" >/dev/null
actual_public_key="$(openssl pkey -in "$SIGNING_KEY" -pubout -outform DER 2>/dev/null | tail -c 32 | base64 -w0)"
[[ "$actual_public_key" == "$PINNED_PUBLIC_KEY" ]] || {
  printf 'signing key does not match the public key pinned by clients\n' >&2
  exit 1
}
temporary_signature="$(mktemp "${TMPDIR:-/tmp}/zhp2pproxy-catalog.XXXXXX")"
trap 'rm -f "$temporary_signature"' EXIT
openssl pkeyutl -sign -rawin -inkey "$SIGNING_KEY" -in "$CATALOG" -out "$temporary_signature"
base64 -w0 "$temporary_signature" >"$CATALOG.sig"
printf '\n' >>"$CATALOG.sig"
printf 'signed %s\n' "$CATALOG"
