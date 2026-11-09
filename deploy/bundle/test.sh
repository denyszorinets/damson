#!/bin/sh
# Tests verify.sh and the signing step of build.sh's contract on a small fake
# bundle: valid, tampered, extra file, wrong key, unsigned. Needs ssh-keygen.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "bundle test: $*" >&2; exit 1; }

ssh-keygen -q -t ed25519 -N '' -f "$tmp/release" -C test
ssh-keygen -q -t ed25519 -N '' -f "$tmp/other" -C other

mk() { # mk NAME: a signed bundle folder
	d="$tmp/$1"; mkdir -p "$d/bin"
	cp "$here/verify.sh" "$d/verify.sh"; chmod 755 "$d/verify.sh"
	echo binary > "$d/bin/damson"
	( cd "$d" && find . -type f ! -name SHA256SUMS | sort | sed 's|^\./||' | xargs sha256sum > SHA256SUMS )
}
sign() { ssh-keygen -q -Y sign -f "$1" -n damson-bundle "$2/SHA256SUMS"; }

mk good; sign "$tmp/release" "$tmp/good"
( cd "$tmp/good" && ./verify.sh --pubkey "$tmp/release.pub" ) 2>&1 | grep -q 'signature ok' || fail "a valid signature was refused"
( cd "$tmp/good" && DAMSON_BUNDLE_PUBKEY="$tmp/release.pub" ./verify.sh ) >/dev/null 2>&1 || fail "the environment variable is ignored"
( cd "$tmp/good" && ./verify.sh ) 2>&1 | grep -q 'NOT checked' || fail "an unchecked signature is not announced"

if ( cd "$tmp/good" && ./verify.sh --pubkey "$tmp/other.pub" ) >/dev/null 2>&1; then fail "another key's check passed"; fi

mk unsigned
if ( cd "$tmp/unsigned" && ./verify.sh --pubkey "$tmp/release.pub" ) >/dev/null 2>&1; then fail "an unsigned bundle passed with a key"; fi
( cd "$tmp/unsigned" && ./verify.sh ) >/dev/null 2>&1 || fail "an unsigned bundle fails without a key"

mk tampered; sign "$tmp/release" "$tmp/tampered"; echo evil > "$tmp/tampered/bin/damson"
if ( cd "$tmp/tampered" && ./verify.sh --pubkey "$tmp/release.pub" ) >/dev/null 2>&1; then fail "a changed file passed"; fi

mk swapped; sign "$tmp/release" "$tmp/swapped"
# An attacker replaces the file and rewrites SHA256SUMS: the signature breaks.
echo evil > "$tmp/swapped/bin/damson"
( cd "$tmp/swapped" && find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.sig | sort | sed 's|^\./||' | xargs sha256sum > SHA256SUMS )
if ( cd "$tmp/swapped" && ./verify.sh --pubkey "$tmp/release.pub" ) >/dev/null 2>&1; then fail "rewritten checksums passed the signature check"; fi
( cd "$tmp/swapped" && ./verify.sh ) >/dev/null 2>&1 || fail "rewritten checksums fail the plain check (it does not claim authenticity)"

mk extra; sign "$tmp/release" "$tmp/extra"; echo x > "$tmp/extra/extra.file"
if ( cd "$tmp/extra" && ./verify.sh --pubkey "$tmp/release.pub" ) >/dev/null 2>&1; then fail "an unlisted file passed"; fi

# A relative key path works from another directory.
( cd "$tmp" && good/verify.sh --pubkey release.pub ) >/dev/null 2>&1 || fail "a relative key path is not resolved against the caller's directory"
echo "bundle test: ok"
