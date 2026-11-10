#!/bin/sh
# Loads the container images of the bundle into the local engine, or, with a
# registry prefix, tags and pushes them for a private registry:
#   ./load-images.sh                       load into docker or podman
#   ./load-images.sh registry.local/damson load, tag and push
# Set DAMSON_BUNDLE_PUBKEY to a public key file to require a valid signature.
set -eu
cd "$(dirname "$0")"
[ -d images ] || { echo "load-images: this bundle has no images (built without WITH_IMAGES=1)" >&2; exit 1; }
engine=""
for e in docker podman; do command -v "$e" >/dev/null 2>&1 && { engine="$e"; break; }; done
[ -n "$engine" ] || { echo "load-images: docker or podman is required" >&2; exit 1; }
./verify.sh >/dev/null # DAMSON_BUNDLE_PUBKEY, when set, makes it check the signature too
prefix="${1:-}"
# verify.sh refused unlisted files, so every tar here is checksummed.
for tar in images/*.tar; do
	ref="$("$engine" load -i "$tar" | sed -n 's/^Loaded image: //p' | head -1)"
	[ -n "$ref" ] || { echo "load-images: $tar did not load an image" >&2; exit 1; }
	echo "loaded $ref"
	if [ -n "$prefix" ]; then
		short="${ref##*/}" # podman names images localhost/NAME:TAG
		"$engine" tag "$ref" "$prefix/$short"
		"$engine" push "$prefix/$short"
	fi
done
