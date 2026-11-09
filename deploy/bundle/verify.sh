#!/bin/sh
# Verifies every file of the bundle against SHA256SUMS, and refuses files that
# SHA256SUMS does not list. Run it in the bundle folder after you unpack it (and
# check the archive's .sha256 before that).
set -eu
cd "$(dirname "$0")"
sha256sum --strict -c SHA256SUMS
# verify.sh and SHA256SUMS themselves are not listed (verify.sh is listed; SHA256SUMS cannot be).
extra="$(find . -type f ! -name SHA256SUMS | sed 's|^\./||' | sort | while read -r f; do grep -q "  $f\$" SHA256SUMS || echo "$f"; done)"
[ -z "$extra" ] || { echo "verify: files not listed in SHA256SUMS:" >&2; echo "$extra" >&2; exit 1; }
