#!/bin/sh
# Builds the offline bundle (ARCHITECTURE section 16): everything an air-gapped
# site needs to install Damson, with checksums.
#
#   deploy/bundle/build.sh [-o DIR]            binaries, chart, units, docs pointers
#   WITH_IMAGES=1 deploy/bundle/build.sh       also the three container images
#                                              (docker or podman, a few GB)
#
# Output: DIR/damson-bundle-VERSION.tar.gz and its .sha256. Needs go, helm, tar,
# sha256sum, and web/build (run `make web` first).
set -eu

root="$(cd "$(dirname "$0")/../.." && pwd)"
out="$root/dist"
while getopts o: opt; do
	case "$opt" in
		o) out="$OPTARG" ;;
		*) echo "usage: build.sh [-o DIR]" >&2; exit 2 ;;
	esac
done

VERSION="${VERSION:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)}"
# Chart versions are semver; a git describe string is made into one.
chart_version="$(printf '%s' "$VERSION" | sed -E 's/^v//')"
case "$chart_version" in
	[0-9]*.[0-9]*.[0-9]*) ;;
	*) chart_version="0.0.0-$(printf '%s' "$chart_version" | tr -c 'A-Za-z0-9.-' '-')" ;;
esac
name="damson-bundle-$VERSION"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
dir="$stage/$name"
mkdir -p "$dir/bin" "$dir/chart" "$dir/systemd" "$dir/images"

[ -d "$root/web/build" ] || { echo "build.sh: web/build is missing; run 'make web'" >&2; exit 1; }
command -v helm >/dev/null 2>&1 || { echo "build.sh: helm is required to package the chart" >&2; exit 1; }

echo "== binaries"
for arch in amd64 arm64; do
	( cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X github.com/denyszorinets/damson/internal/version.Version=$VERSION" \
		-o "$dir/bin/damson-linux-$arch" ./cmd/damson )
done

echo "== chart"
helm package "$root/deploy/helm/damson" --version "$chart_version" --app-version "$VERSION" -d "$dir/chart" >/dev/null

echo "== systemd"
cp "$root"/deploy/systemd/damson-controller.service "$root"/deploy/systemd/damson-agent.service \
	"$root"/deploy/systemd/damson.sysusers "$root"/deploy/systemd/*.example "$dir/systemd/"

cp "$root/LICENSE" "$dir/" 2>/dev/null || true
cp "$root/deploy/bundle/INSTALL.txt" "$dir/INSTALL.txt"
cp "$root/deploy/bundle/verify.sh" "$root/deploy/bundle/load-images.sh" "$dir/"
chmod 755 "$dir/verify.sh" "$dir/load-images.sh"

if [ "${WITH_IMAGES:-0}" = 1 ]; then
	echo "== images"
	engine=""
	for e in docker podman; do command -v "$e" >/dev/null 2>&1 && { engine="$e"; break; }; done
	[ -n "$engine" ] || { echo "build.sh: WITH_IMAGES=1 needs docker or podman" >&2; exit 1; }
	for target in damson damson-agent damson-agent-build; do
		"$engine" build -f "$root/deploy/docker/Dockerfile" --target "$target" --build-arg "VERSION=$VERSION" -t "$target:$VERSION" "$root"
		"$engine" save -o "$dir/images/$target-$VERSION.tar" "$target:$VERSION"
	done
else
	rmdir "$dir/images"
fi

echo "== checksums"
( cd "$dir" && find . -type f ! -name SHA256SUMS | sort | sed 's|^\./||' | xargs sha256sum > SHA256SUMS )
cat > "$dir/manifest.json" <<JSON
{"name":"damson-bundle","version":"$VERSION","chart_version":"$chart_version","images":$( [ "${WITH_IMAGES:-0}" = 1 ] && echo true || echo false )}
JSON
( cd "$dir" && sha256sum manifest.json >> SHA256SUMS )

mkdir -p "$out"
# A reproducible archive: sorted names, fixed owner and time.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='UTC 2020-01-01' -C "$stage" -czf "$out/$name.tar.gz" "$name"
( cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256" )
echo "bundle: $out/$name.tar.gz"
