#!/bin/sh
# Build a one-VM trust domain. Neither the public nor the private fixture key
# is present in ordinary OpenWrt builds.
set -eu
archive=${1:?provide the pinned Mihomo ARM64 archive};shift
expected=${1:?provide its independently verified SHA-256};shift
root=$(cd "$(dirname "$0")/../.." && pwd)
overlay=$(mktemp -d)
work=$(mktemp -d)
trap 'rm -rf "$overlay" "$work"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$overlay/root/bundles" "$overlay/usr/sbin"
cd "$root"
case "${HYPER_QUALIFY_METHOD:-tproxy}" in
 tproxy) ;;
 tun) touch "$overlay/root/qualify-tun" ;;
 *) echo 'Expected tproxy or tun' >&2; exit 2 ;;
esac
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o "$work/fault" scripts/kernel/daemon-fixture/failure.go
pub=$(go run scripts/kernel/daemon-fixture/sign.go "$overlay/root/bundles" "$archive" "$work/fault" "$expected")
for cli in hyper-kernel hyperd hyper-subscription; do
 linker='-s -w'
 [ "$cli" != hyper-kernel ] || linker="$linker -X github.com/HyperPteLtd/openwrt-hyper/internal/kernel.PublicKey=$pub"
 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$linker" -o "$overlay/usr/sbin/$cli" "./cmd/$cli"
done
cp -R files/. "$overlay"
go run ./scripts/qualification/firmware/run "$@" --guest-overlay "$overlay" --guest-script scripts/kernel/daemon-fixture/guest.sh --disk-mib 768
