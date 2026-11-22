#!/bin/sh
# Put the programs bundled with luci-app-hyper into the package tree for one
# OpenWrt package arch:
#   luci-app-hyper/root/usr/libexec/hyper/bin/mihomo       pinned mihomo source (core.lock), trimmed
#   luci-app-hyper/root/usr/libexec/hyper/bin/hyperd       built from cmd/hyperd (static Go)
#
#   scripts/stage-binaries.sh <openwrt package arch>       e.g. aarch64_cortex-a53
#   OUT_DIR=/tmp/core scripts/stage-binaries.sh x86_64     elsewhere
#   scripts/stage-binaries.sh --target-id <arch>          print the shared binary target
#
# Requires curl, tar, awk, node and Go (GOTOOLCHAIN fetches the pinned toolchain).
# No prebuilt-core fallback.
set -eu

target_id_only=false
if [ "${1:-}" = --target-id ]; then
	target_id_only=true
	shift
fi
arch="${1:?usage: stage-binaries.sh [--target-id] <openwrt package arch>}"

goarm="" gomips="" go386=""
case "$arch" in
	x86_64) goarch=amd64 ;;
	aarch64_*) goarch=arm64 ;;
	arm_arm1176jzf-s_vfp) goarch=arm goarm=6 ;;
	arm_cortex-a5_vfpv4|arm_cortex-a7_vfpv4|arm_cortex-a7_neon-vfpv4|arm_cortex-a8_vfpv3|arm_cortex-a9_neon|arm_cortex-a9_vfpv3-d16|arm_cortex-a15_neon-vfpv4)
		goarch=arm goarm=7 ;;
	arm_*) goarch=arm goarm=5 ;;	# soft-float ARM cores
	mipsel_*) goarch=mipsle gomips=softfloat ;;
	mips_*) goarch=mips gomips=softfloat ;;
	i386_*) goarch=386 go386=softfloat ;;
	riscv64_*) goarch=riscv64 ;;
	loongarch64_*) goarch=loong64 ;;
	*) echo "no mihomo build for OpenWrt arch $arch" >&2; exit 1 ;;
esac

# Artifact identity for shared CI caches; needs only the shell.
if [ "$target_id_only" = true ]; then
	printf '%s\n' "$goarch${goarm:+v$goarm}${gomips:+-$gomips}${go386:+-$go386}"
	exit 0
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/core.lock"
out="${OUT_DIR:-$ROOT/luci-app-hyper/root/usr/libexec/hyper/bin}"
cache="${CACHE_DIR:-$ROOT/dist/.cache}"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p "$cache" "$out"
cache="$(cd "$cache" && pwd)"
out="$(cd "$out" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' 0
trap 'exit 130' INT
trap 'exit 143' TERM

verify_source() {
	got="$(sha256 "$1")"
	[ "$got" = "$MIHOMO_SOURCE_SHA256" ] || {
		echo "checksum mismatch for mihomo source: got $got, want $MIHOMO_SOURCE_SHA256" >&2
		exit 1
	}
}

name="mihomo-$MIHOMO_COMMIT.tar.gz"
if [ ! -f "$cache/$name" ]; then
	echo "   download $name"
	curl -fsSL --retry 3 --retry-delay 10 --retry-all-errors --max-time 300 -o "$tmp/source.tar.gz" "https://codeload.github.com/$MIHOMO_REPO/tar.gz/$MIHOMO_COMMIT"
	verify_source "$tmp/source.tar.gz"
	mv "$tmp/source.tar.gz" "$cache/$name"
fi
verify_source "$cache/$name"

# Inspect the whole verified archive before extracting: only regular files and
# directories inside the pinned root pass (GNU and BSD tar listings).
src="mihomo-$MIHOMO_COMMIT"
LC_ALL=C tar -tzf "$cache/$name" > "$tmp/source.paths"
LC_ALL=C tar -tvzf "$cache/$name" > "$tmp/source.types"
awk -v root="$src" '
	function reject() { print "unsafe source archive member: " $0 > "/dev/stderr"; bad=1 }
	{
		path=$0
		if (index(path, "\\") || path ~ /[[:cntrl:]]/) { reject(); next }
		sub(/\/+$/, "", path)
		if (path != root && index(path, root "/") != 1) { reject(); next }
		n=split(path, parts, "/")
		for (i=1; i<=n; i++) if (parts[i] == "..") { reject(); break }
	}
	END { exit bad || NR == 0 }
' "$tmp/source.paths"
awk '
	substr($0, 1, 1) != "-" && substr($0, 1, 1) != "d" {
		print "unsafe source archive member: " $0 > "/dev/stderr"; bad=1
	}
	END { exit bad || NR == 0 }
' "$tmp/source.types"
tar -xzf "$cache/$name" -C "$tmp" --no-same-owner --no-same-permissions

# Never run scripts from the downloaded tree; only our trim step edits it.
node "$ROOT/scripts/mihomo-trim.mjs" "$tmp/$src"

command -v go >/dev/null 2>&1 || { echo "need Go to build mihomo and hyperd" >&2; exit 1; }
export CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" GOMIPS="$gomips" GO386="$go386"
export GOTOOLCHAIN="$GO_TOOLCHAIN" GOWORK=off GOFLAGS= GOEXPERIMENT=
# The core reports its release (v1.0.0) without the source tag's prefix: the Logs page and
# the controller show it.
core_version=${MIHOMO_TAG##*-}
echo "   build mihomo $core_version and hyperd ($goarch${goarm:+ GOARM=$goarm}${gomips:+ GOMIPS=$gomips}${go386:+ GO386=$go386})"
mkdir -p "$tmp/bin"
(cd "$tmp/$src" && go build -mod=readonly -trimpath -buildvcs=false -tags "$MIHOMO_TAGS" \
	-ldflags "-s -w -buildid= -X github.com/metacubex/mihomo/constant.Version=$core_version" \
	-o "$tmp/bin/mihomo" .)
version="$(awk -F':=' '$1 == "PKG_VERSION" { print $2 }' "$ROOT/luci-app-hyper/Makefile")"
(cd "$ROOT" && go build -mod=readonly -trimpath -buildvcs=false \
	-ldflags "-s -w -buildid= -X main.Version=$version" -o "$tmp/bin/hyperd" ./cmd/hyperd)

cp "$tmp/bin/mihomo" "$out/mihomo"
cp "$tmp/bin/hyperd" "$out/hyperd"
chmod 0755 "$out/mihomo" "$out/hyperd"
ls -l "$out"
