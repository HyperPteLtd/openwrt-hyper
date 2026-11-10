#!/bin/sh
# Put the programs bundled with luci-app-hyper into the package tree for one
# OpenWrt package arch:
#   luci-app-hyper/root/usr/libexec/hyper/bin/sing-box     libbox release (scripts/core.lock, hashes in scripts/libbox.sha256)
#   luci-app-hyper/root/usr/libexec/hyper/bin/hyper-crypt  built from hyper-crypt/ (static Go)
#
#   scripts/stage-binaries.sh <openwrt package arch>       e.g. aarch64_cortex-a53
#   OUT_DIR=/tmp/core scripts/stage-binaries.sh x86_64     elsewhere
#
# Builds hyper-crypt with the local Go, or with Docker (golang image) when there is none.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/core.lock"
arch="${1:?usage: stage-binaries.sh <openwrt package arch>}"
out="${OUT_DIR:-$ROOT/luci-app-hyper/root/usr/libexec/hyper/bin}"
cache="${CACHE_DIR:-$ROOT/dist/.cache}"
GO_IMAGE="${GO_IMAGE:-golang:1.26.10-bookworm}"

goarm="" gomips="" go386=""
case "$arch" in
	x86_64) target=amd64 goarch=amd64 ;;
	aarch64_*) target=arm64 goarch=arm64 ;;
	arm_arm1176jzf-s_vfp) target=armv6 goarch=arm goarm=6 ;;
	arm_cortex-a5_vfpv4|arm_cortex-a7_vfpv4|arm_cortex-a7_neon-vfpv4|arm_cortex-a8_vfpv3|arm_cortex-a9_neon|arm_cortex-a9_vfpv3-d16|arm_cortex-a15_neon-vfpv4)
		target=armv7 goarch=arm goarm=7 ;;
	arm_*) target=armv5 goarch=arm goarm=5 ;;	# soft-float ARM cores
	mipsel_*) target=mipsle-softfloat goarch=mipsle gomips=softfloat ;;
	mips_*) target=mips-softfloat goarch=mips gomips=softfloat ;;
	i386_*) target=386-softfloat goarch=386 go386=softfloat ;;
	riscv64_*) target=riscv64 goarch=riscv64 ;;
	loongarch64_*) target=loong64 goarch=loong64 ;;
	*) echo "no libbox build for OpenWrt arch $arch" >&2; exit 1 ;;
esac

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

name="libbox-$LIBBOX_VERSION-linux-$target.tar.gz"
want="$(awk -v n="$name" '$2 == n { print $1 }' "$ROOT/scripts/libbox.sha256")"
[ -n "$want" ] || { echo "$name is not listed in scripts/libbox.sha256" >&2; exit 1; }

mkdir -p "$cache" "$out"
if [ ! -f "$cache/$name" ] || [ "$(sha256 "$cache/$name")" != "$want" ]; then
	echo "   download $name"
	curl -fsSL --retry 3 -o "$cache/$name.part" "https://github.com/$LIBBOX_REPO/releases/download/v$LIBBOX_VERSION/$name"
	mv "$cache/$name.part" "$cache/$name"
fi
got="$(sha256 "$cache/$name")"
[ "$got" = "$want" ] || { rm -f "$cache/$name"; echo "checksum mismatch for $name: got $got, want $want" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
tar -xzf "$cache/$name" -C "$tmp"
cp "$tmp"/libbox-*/sing-box "$out/sing-box"
chmod 0755 "$out/sing-box"

echo "   build hyper-crypt ($goarch${goarm:+ GOARM=$goarm}${gomips:+ GOMIPS=$gomips}${go386:+ GO386=$go386})"
if command -v go >/dev/null 2>&1; then
	(cd "$ROOT/hyper-crypt" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" GOMIPS="$gomips" GO386="$go386" \
		go build -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o "$out/hyper-crypt" .)
elif command -v docker >/dev/null 2>&1; then
	# macOS (bsdtar) would add Apple extended attributes that GNU tar warns about
	case "$(tar --version 2>/dev/null)" in *bsdtar*) nometa="--no-xattrs --no-mac-metadata" ;; *) nometa="" ;; esac
	# shellcheck disable=SC2086
	COPYFILE_DISABLE=1 tar $nometa -C "$ROOT/hyper-crypt" -cf - . | docker run --rm -i -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH="$goarch" \
		-e GOARM="$goarm" -e GOMIPS="$gomips" -e GO386="$go386" "$GO_IMAGE" \
		sh -c 'mkdir -p /b && tar -C /b -xf - && cd /b && go build -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o /tmp/hyper-crypt . 1>&2 && cat /tmp/hyper-crypt' \
		> "$out/hyper-crypt.part"
	mv "$out/hyper-crypt.part" "$out/hyper-crypt"
else
	echo "need Go or Docker to build hyper-crypt" >&2
	exit 1
fi
chmod 0755 "$out/hyper-crypt"
ls -l "$out"
