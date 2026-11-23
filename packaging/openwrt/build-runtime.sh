#!/bin/sh
# Build one self-contained management executable; the router never runs Go or fetches it.
set -eu
abi=${1:?ABI}; version=${2:?version}; output=${3:?output directory}
case "$version" in ''|*[!0-9A-Za-z.+_~-]*) exit 1;; esac
GOARCH=$abi
case "$abi" in
 amd64) export GOAMD64=v1 ;;
 arm64) export GOARM64=v8.0 ;;
 386) export GO386=softfloat ;;
 arm5) GOARCH=arm; export GOARM=5,softfloat ;;
 arm6) GOARCH=arm; export GOARM=6,hardfloat ;;
 arm7) GOARCH=arm; export GOARM=7,hardfloat ;;
 mips|mipsle) export GOMIPS=softfloat ;;
 riscv64) export GORISCV64=rva20u64 ;;
 loong64) ;;
 *) echo "Unknown runtime ABI: $abi" >&2; exit 1 ;;
esac
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
mkdir -p "$output"
output=$(cd "$output" && pwd)
work=$(mktemp -d "$output/.runtime.XXXXXX")
trap 'rm -rf "$work"' EXIT
export GOARCH GOOS=linux CGO_ENABLED=0
flags="-s -w -buildid= -X github.com/HyperPteLtd/openwrt-hyper/internal/subscription.packageVersion=$version"
if [ -n "${HYPER_MIHOMO_PUBLIC_KEY:-}" ]; then
 case "$HYPER_MIHOMO_PUBLIC_KEY" in *[!A-Za-z0-9+/=]*) echo 'Invalid publisher public key' >&2; exit 1;; esac
 flags="$flags -X github.com/HyperPteLtd/openwrt-hyper/internal/kernel.PublicKey=$HYPER_MIHOMO_PUBLIC_KEY"
fi
(cd "$root" && go build -mod=readonly -trimpath -buildvcs=false -ldflags "$flags" -o "$work/manager" ./cmd/hyperd)
gzip -n -9 -c "$work/manager" > "$work/$abi.gz"
(cd "$work" && sha256sum "$abi.gz") > "$work/$abi.build"
printf '%s\n%s\n' "$version" "${HYPER_MIHOMO_PUBLIC_KEY:-}" >> "$work/$abi.build"
mv "$work/$abi.gz" "$work/$abi.build" "$output/"
