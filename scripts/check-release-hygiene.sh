#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

failed=0
while IFS= read -r -d '' path; do
  base="${path##*/}"
  case "$base" in
    proxy.json*|identity.key*|peers.json*|zerohour.conf*|*.pem|*.key|*.p12|*.pfx|*.bak|*.log|.env|.env.*)
      # The pinned public verification key is intentionally public.
      if [[ "$path" != "release/update-public.pem" ]]; then
        printf 'forbidden runtime/secret-like tracked file: %s\n' "$path" >&2
        failed=1
      fi
      ;;
  esac
done < <(git ls-files -z)

# Public Ed25519 verification key; this is intentionally distributed and is
# not signing material. gitleaks:allow
expected_key="GnbiMTDyAI1NenxfEyJZihgFGsNUWmSVkke1Z6PweJA=" # gitleaks:allow
grep -Fq "$expected_key" cmd/proxy/main.go || {
  printf 'embedded updater public key does not match the release key\n' >&2
  failed=1
}
derived_key="$(openssl pkey -pubin -in release/update-public.pem -outform DER 2>/dev/null | tail -c 32 | base64 | tr -d '\n')"
[[ "$derived_key" == "$expected_key" ]] || {
  printf 'release/update-public.pem does not match the pinned updater key\n' >&2
  failed=1
}

if [[ "$failed" != "0" ]]; then
  exit 1
fi

if command -v gitleaks >/dev/null 2>&1; then
  gitleaks git --redact --no-banner
else
  printf 'note: gitleaks is not installed; filename and key checks completed\n' >&2
fi
