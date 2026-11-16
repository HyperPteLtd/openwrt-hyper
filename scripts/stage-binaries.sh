#!/bin/sh
# Put the programs bundled with luci-app-hyper into the package tree for one
# OpenWrt package arch:
#   luci-app-hyper/root/usr/libexec/hyper/bin/sing-box     router build of pinned libbox sources
#   luci-app-hyper/root/usr/libexec/hyper/bin/hyper-crypt  built from hyper-crypt/ (static Go)
#
#   scripts/stage-binaries.sh <openwrt package arch>       e.g. aarch64_cortex-a53
#   OUT_DIR=/tmp/core scripts/stage-binaries.sh x86_64     elsewhere
#
# Requires Python 3, curl and tar. Builds both programs with the pinned Go
# toolchain, or in Docker when Go is unavailable. No prebuilt-core fallback.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/core.lock"
arch="${1:?usage: stage-binaries.sh <openwrt package arch>}"
out="${OUT_DIR:-$ROOT/luci-app-hyper/root/usr/libexec/hyper/bin}"
cache="${CACHE_DIR:-$ROOT/dist/.cache}"
GO_IMAGE="${GO_IMAGE:-golang:${GO_TOOLCHAIN#go}-bookworm}"

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
	*) echo "no libbox build for OpenWrt arch $arch" >&2; exit 1 ;;
esac

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p "$cache" "$out"
# Normalize relative overrides before building in other working directories.
cache="$(cd "$cache" && pwd)"
out="$(cd "$out" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' 0
trap 'exit 130' INT
trap 'exit 143' TERM

verify_source() {
	got="$(sha256 "$1")"
	[ "$got" = "$LIBBOX_SOURCE_SHA256" ] || {
		echo "checksum mismatch for libbox source: got $got, want $LIBBOX_SOURCE_SHA256" >&2
		exit 1
	}
}

name="libbox-$LIBBOX_COMMIT.tar.gz"
if [ ! -f "$cache/$name" ]; then
	echo "   download $name"
	curl -fsSL --retry 3 -o "$tmp/source.tar.gz" "https://codeload.github.com/$LIBBOX_REPO/tar.gz/$LIBBOX_COMMIT"
	verify_source "$tmp/source.tar.gz"
	mv "$tmp/source.tar.gz" "$cache/$name"
fi
verify_source "$cache/$name"

# Validate every member before extracting. The pinned archive contains only
# regular files/directories; links and paths outside its expected root fail shut.
python3 - "$cache/$name" "$tmp" "libbox-$LIBBOX_COMMIT" <<'PY'
import pathlib
import shutil
import sys
import tarfile

archive, destination, root = sys.argv[1:]
with tarfile.open(archive, "r:gz") as source:
    members = source.getmembers()
    for member in members:
        path = pathlib.PurePosixPath(member.name)
        if (path.is_absolute() or not path.parts or path.parts[0] != root
                or ".." in path.parts or "\\" in member.name
                or not (member.isfile() or member.isdir())):
            sys.exit(f"unsafe source archive member: {member.name}")
    for member in members:
        target = pathlib.Path(destination).joinpath(*pathlib.PurePosixPath(member.name).parts)
        if member.isdir():
            target.mkdir(parents=True, exist_ok=True)
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            with source.extractfile(member) as src, target.open("wb") as dst:
                shutil.copyfileobj(src, dst)
            target.chmod(member.mode & 0o777)
PY

runtime="$tmp/libbox-$LIBBOX_COMMIT/internal/singbox/runtime.go"
sdk_version="$(sed -n 's/^const sdkVersion = "\([^"]*\)"$/\1/p' "$runtime")"
core_version="$(sed -n 's/^const pinnedVersion = "v\([^"]*\)"$/\1/p' "$runtime")"
[ "$sdk_version" = "$LIBBOX_VERSION" ] && [ -n "$core_version" ] || {
	echo "libbox source version mismatch: SDK '$sdk_version' (expected $LIBBOX_VERSION), core '$core_version'" >&2
	exit 1
}

# Our build commands are shared by the host and Docker paths. Never run scripts
# from the downloaded source tree. Go validates dependencies against its go.sum.
mkdir -p "$tmp/bin"
cp -R "$ROOT/hyper-crypt" "$tmp/hyper-crypt"
cat > "$tmp/build.sh" <<'BUILD'
set -eu
cd "$(dirname "$0")"
cd "libbox-$LIBBOX_COMMIT"
go build -mod=readonly -trimpath -buildvcs=false -tags "$LIBBOX_ROUTER_TAGS" \
	-ldflags "-s -w -buildid= -checklinkname=0 -X runtime.godebugDefault=multipathtcp=0,tlssha1=1 -X github.com/sagernet/sing-box/constant.Version=$CORE_VERSION -X main.libboxRevision=$LIBBOX_COMMIT -X github.com/HyperPteLtd/libbox/internal/singbox.buildTags=$LIBBOX_ROUTER_TAGS" \
	-o ../bin/sing-box ./cmd/sing-box
cd ../hyper-crypt
go build -mod=readonly -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o ../bin/hyper-crypt .
BUILD

export CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" GOMIPS="$gomips" GO386="$go386"
export GOTOOLCHAIN="$GO_TOOLCHAIN" GOWORK=off GOFLAGS= GOEXPERIMENT=
export CORE_VERSION="$core_version" LIBBOX_COMMIT LIBBOX_ROUTER_TAGS
echo "   build core $core_version / libbox $sdk_version and hyper-crypt ($goarch${goarm:+ GOARM=$goarm}${gomips:+ GOMIPS=$gomips}${go386:+ GO386=$go386})"
if command -v go >/dev/null 2>&1; then
	# Keep Go's standard caches (including actions/setup-go) and any explicit
	# GOMODCACHE/GOCACHE overrides shared across architecture builds.
	sh "$tmp/build.sh"
elif command -v docker >/dev/null 2>&1; then
	# macOS (bsdtar) would add Apple extended attributes that GNU tar warns about
	case "$(tar --version 2>/dev/null)" in *bsdtar*) nometa="--no-xattrs --no-mac-metadata" ;; *) nometa="" ;; esac
	# shellcheck disable=SC2086
	COPYFILE_DISABLE=1 tar $nometa -C "$tmp" -cf "$tmp/context.tar" "libbox-$LIBBOX_COMMIT" hyper-crypt build.sh bin
	# Tar streams work when Docker cannot mount the checkout (e.g. macOS external
	# drives). One named volume reuses module/build caches across target arches.
	docker run --rm -i -v hyper-router-go-cache:/cache \
		-e CGO_ENABLED -e GOOS -e GOARCH -e GOARM -e GOMIPS -e GO386 \
		-e GOTOOLCHAIN -e GOWORK -e GOFLAGS -e GOEXPERIMENT \
		-e CORE_VERSION -e LIBBOX_COMMIT -e LIBBOX_ROUTER_TAGS \
		-e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build "$GO_IMAGE" \
		sh -c 'mkdir -p /b && tar -C /b -xf - && sh /b/build.sh 1>&2 && tar -C /b/bin -cf - sing-box hyper-crypt' \
		< "$tmp/context.tar" > "$tmp/binaries.tar"
	tar -xf "$tmp/binaries.tar" -C "$tmp/bin"
else
	echo "need Go or Docker to build the router core and hyper-crypt" >&2
	exit 1
fi
chmod 0755 "$tmp/bin/sing-box" "$tmp/bin/hyper-crypt"
cp "$tmp/bin/sing-box" "$out/sing-box"
cp "$tmp/bin/hyper-crypt" "$out/hyper-crypt"
ls -l "$out"
