#!/bin/sh
# Verifies every file of the bundle against SHA256SUMS, refuses files that
# SHA256SUMS does not list, and, with a public key, checks the signature of
# SHA256SUMS. Run it in the bundle folder after you unpack it (and check the
# archive's .sha256 and its .sig before that).
#
#   ./verify.sh                         checksums only (says so)
#   ./verify.sh --pubkey release.pub    checksums and the signature; fails when the
#                                       signature is missing or wrong
#   DAMSON_BUNDLE_PUBKEY=release.pub ./verify.sh    the same
#
# release.pub is the maintainers' public key (one line, "ssh-ed25519 AAAA..."),
# obtained from a channel you trust, not from this bundle. Needs ssh-keygen.
set -eu
pubkey="${DAMSON_BUNDLE_PUBKEY:-}"
while [ $# -gt 0 ]; do
	case "$1" in
		--pubkey) [ $# -ge 2 ] || { echo "verify: --pubkey needs a file" >&2; exit 2; }; pubkey="$2"; shift 2 ;;
		*) echo "usage: verify.sh [--pubkey FILE]" >&2; exit 2 ;;
	esac
done
# A relative key path is relative to where the command was run.
if [ -n "$pubkey" ]; then
	case "$pubkey" in /*) ;; *) pubkey="$(pwd)/$pubkey" ;; esac
fi
cd "$(dirname "$0")"

sha256sum --strict -c SHA256SUMS
extra="$(find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.sig | sed 's|^\./||' | sort | while read -r f; do grep -q "  $f\$" SHA256SUMS || echo "$f"; done)"
[ -z "$extra" ] || { echo "verify: files not listed in SHA256SUMS:" >&2; echo "$extra" >&2; exit 1; }

if [ -n "$pubkey" ]; then
	command -v ssh-keygen >/dev/null 2>&1 || { echo "verify: checking a signature needs ssh-keygen" >&2; exit 1; }
	[ -f "$pubkey" ] || { echo "verify: public key $pubkey does not exist" >&2; exit 1; }
	[ -f SHA256SUMS.sig ] || { echo "verify: SHA256SUMS.sig is missing; this bundle is not signed" >&2; exit 1; }
	signers="$(mktemp)"
	trap 'rm -f "$signers"' EXIT
	printf 'damson-bundle %s\n' "$(cut -d' ' -f1,2 "$pubkey")" > "$signers"
	ssh-keygen -q -Y verify -f "$signers" -I damson-bundle -n damson-bundle -s SHA256SUMS.sig < SHA256SUMS \
		|| { echo "verify: the signature of SHA256SUMS is not valid for $pubkey" >&2; exit 1; }
	echo "verify: signature ok"
else
	echo "verify: checksums ok; the signature was NOT checked (pass --pubkey to check it)" >&2
fi
