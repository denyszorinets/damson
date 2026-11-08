#!/bin/sh
# Loads the container images of the bundle into the local engine, or, with a
# registry prefix, tags and pushes them for a private registry:
#   ./load-images.sh                       load into docker or podman
#   ./load-images.sh registry.local/damson load, tag and push
set -eu
cd "$(dirname "$0")"
[ -d images ] || { echo "load-images: this bundle has no images (built without WITH_IMAGES=1)" >&2; exit 1; }
engine=""
for e in docker podman; do command -v "$e" >/dev/null 2>&1 && { engine="$e"; break; }; done
[ -n "$engine" ] || { echo "load-images: docker or podman is required" >&2; exit 1; }
./verify.sh >/dev/null
prefix="${1:-}"
for tar in images/*.tar; do
	ref="$("$engine" load -i "$tar" | sed -n 's/^Loaded image: //p' | head -1)"
	echo "loaded $ref"
	if [ -n "$prefix" ]; then
		"$engine" tag "$ref" "$prefix/$ref"
		"$engine" push "$prefix/$ref"
	fi
done
