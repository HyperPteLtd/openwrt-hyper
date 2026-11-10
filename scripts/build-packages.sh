#!/bin/sh
# Build test packages with the official OpenWrt SDKs inside Docker, one package
# per CPU and firmware family, named like passwall's releases:
#   23.05-24.10_luci-app-hyper_<ver>_<arch>.ipk   (24.10 SDK, opkg)
#   25.12+_luci-app-hyper-<ver>-<arch>.apk        (25.12 SDK, apk-tools 3)
# Each package bundles the libbox core and hyper-crypt (scripts/stage-binaries.sh).
#
#   scripts/build-packages.sh [target ...]          default target: x86/64
#   e.g. scripts/build-packages.sh mediatek/filogic rockchip/armv8
#
#   RELEASES="24.10.8 25.12.6"   SDK releases to build with
#
# Output: dist/<files> plus dist/sha256sums.
#
# The SDK needs a case-sensitive file system, so it lives on the Docker host
# (/var/tmp/hyper-sdk, inside the colima / Docker Desktop VM on macOS), not on
# the macOS disk. Sources go in and packages come out as tar streams, so no
# folder sharing is needed (colima shares only the home folder by default).
# Docker must run x86_64 containers: on Apple Silicon colima's default qemu 7.0
# crashes the SDK compiler, so enable Rosetta (colima start --vz-rosetta) first.
# Remove everything with:
#   docker rm -f hyper-sdk-builder && docker run --rm -v /var/tmp:/t debian:12-slim rm -rf /t/hyper-sdk

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELEASES="${RELEASES:-24.10.8 25.12.6}"
TARGETS="${*:-x86/64}"
NAME=hyper-sdk-builder
IMAGE=debian:12-slim
WORK=/var/tmp/hyper-sdk
OUT="$ROOT/dist"

echo "== web UI"
npm --prefix "$ROOT/web" run build >/dev/null
test -f "$ROOT/luci-app-hyper/htdocs/luci-static/hyper/index.html"

mkdir -p "$OUT"

if ! docker container inspect "$NAME" >/dev/null 2>&1; then
	echo "== builder container ($IMAGE, linux/amd64)"
	docker run -d --name "$NAME" --platform linux/amd64 \
		-v "$WORK:/work" "$IMAGE" sleep infinity >/dev/null
fi
docker start "$NAME" >/dev/null

docker exec "$NAME" sh -c '
	set -e
	if ! id build >/dev/null 2>&1; then
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends \
			build-essential ca-certificates file gawk gettext git libncurses-dev libssl-dev \
			python3 python3-setuptools rsync unzip wget xz-utils zstd bzip2 zlib1g-dev >/dev/null
		useradd -m build
	fi
	mkdir -p /work && chown build: /work
'

# macOS (bsdtar) would add Apple extended attributes that GNU tar warns about
case "$(tar --version 2>/dev/null)" in *bsdtar*) TAR_NOMETA="--no-xattrs --no-mac-metadata" ;; *) TAR_NOMETA="" ;; esac

for target in $TARGETS; do
	first_release="${RELEASES%% *}"
	arch="$(curl -fsSL "https://downloads.openwrt.org/releases/$first_release/targets/$target/profiles.json" |
		python3 -I -c 'import json, sys; print(json.load(sys.stdin)["arch_packages"])')"

	echo "== $target ($arch): bundle the core and hyper-crypt"
	sh "$ROOT/scripts/stage-binaries.sh" "$arch"

	echo "== copy sources into the container"
	# shellcheck disable=SC2086
	COPYFILE_DISABLE=1 tar $TAR_NOMETA -C "$ROOT" --exclude='._*' --exclude='.DS_Store' \
		-cf - luci-app-hyper scripts/sdk-build.sh |
		docker exec -i "$NAME" sh -c 'rm -rf /work/src && mkdir -p /work/src && tar -C /work/src -xf - && chown -R build: /work/src'

	for release in $RELEASES; do
		case "$release" in 2[0-4].*) family="23.05-24.10" ;; *) family="25.12+" ;; esac
		echo "== OpenWrt $release SDK, $target -> $family"
		dir="/work/$release-$(echo "$target" | tr / -)"
		if ! docker exec -u build "$NAME" sh /work/src/scripts/sdk-build.sh "$release" "$target"; then
			echo "build failed; log: docker exec $NAME tail -n 80 $dir/build.log" >&2
			exit 1
		fi
		pkg="$(docker exec "$NAME" sh -c "ls $dir/sdk/bin/packages/*/hyper/luci-app-hyper*.ipk $dir/sdk/bin/packages/*/hyper/luci-app-hyper*.apk 2>/dev/null | head -n1")"
		[ -n "$pkg" ] || { echo "no package built for $target $release" >&2; exit 1; }
		name="$(basename "$pkg")"
		case "$name" in
			*.ipk) out_name="${family}_${name}" ;;
			*.apk) out_name="${family}_${name%.apk}-${arch}.apk" ;;	# apk names carry no arch
		esac
		docker exec "$NAME" cat "$pkg" > "$OUT/$out_name"
		echo "   $out_name"
	done
done

(
	cd "$OUT"
	ls ./*.ipk ./*.apk 2>/dev/null | sed 's#^\./##' | while read -r f; do
		if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f"; else shasum -a 256 "$f"; fi
	done > sha256sums
)

echo "== done"
ls -l "$OUT"
