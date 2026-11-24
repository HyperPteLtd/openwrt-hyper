#!/bin/sh
# Download the core release pinned in scripts/core.lock for one runtime ABI
# (packaging/openwrt/runtime-abi) into an empty directory: its raw build, checked against the
# pin, for the tests that run the real core. Routers download their core on their own.
# HYPER_CORE_CACHE, if set, is a directory of release assets already downloaded; they are
# checked against the pins like a download.
set -eu
abi=${1:?runtime ABI}; output=${2:?empty output directory}
base=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
root=$(CDPATH= cd -- "$base/../.." && pwd)
. "$base/retry.sh"
. "$root/scripts/core.lock"
case "$abi" in
 amd64|arm64|riscv64|loong64) target=$abi ;;
 386) target=386-softfloat ;;
 arm5|arm6|arm7) target=armv${abi#arm} ;;
 mips|mipsle) target=$abi-softfloat ;;
 *) echo "Unknown runtime ABI: $abi" >&2; exit 2 ;;
esac
# The release names two builds after their instruction set level.
case "$target" in amd64) asset_target=amd64-v1 ;; loong64) asset_target=loong64-abi2 ;; *) asset_target=$target ;; esac
case "$CORE_TAG" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "Invalid CORE_TAG: $CORE_TAG" >&2; exit 1 ;; esac
case "$CORE_REPO/$CORE_RELEASE" in *[!A-Za-z0-9_./-]*) echo 'Invalid core release' >&2; exit 1 ;; esac
[ -d "$output" ] && [ -z "$(ls -A "$output")" ] || { echo "Expected an empty directory: $output" >&2; exit 2; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
# fetch ASSET SHA256: the release asset into $work, matching its pin.
fetch() {
 _asset=$1 _sum=$2
 printf '%s\n' "$_sum" | grep -Eqx '[0-9a-f]{64}' || { echo "Invalid pin for $_asset" >&2; return 1; }
 if [ -n "${HYPER_CORE_CACHE:-}" ] && [ -f "$HYPER_CORE_CACHE/$_asset" ] &&
    printf '%s  %s\n' "$_sum" "$HYPER_CORE_CACHE/$_asset" | sha256sum -c - >/dev/null 2>&1; then
  cp "$HYPER_CORE_CACHE/$_asset" "$work/$_asset"
  return 0
 fi
 if ! retry download "$_asset" "$_sum"; then
  echo "$_asset does not match the SHA-256 pinned in scripts/core.lock" >&2; return 1
 fi
 if [ -n "${HYPER_CORE_CACHE:-}" ]; then mkdir -p "$HYPER_CORE_CACHE" && cp "$work/$_asset" "$HYPER_CORE_CACHE/$_asset"; fi
}
download() {
 rm -f "$work/$1"
 bounded 300 curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 20 --max-filesize 268435456 \
  -o "$work/$1" "https://github.com/$CORE_REPO/releases/download/$CORE_RELEASE/$1" || return 1
 # A torn download is fetched again; a whole one that never matches fails in fetch.
 printf '%s  %s\n' "$2" "$work/$1" | sha256sum -c - >/dev/null 2>&1
}
expected=$(printf '%s\n' "$CORE_SHA256" | awk -v t="$target" '$1 == t { n++; h = $2 } END { if (n == 1) print h }')
[ -n "$expected" ] || { echo "scripts/core.lock pins no core for $target" >&2; exit 1; }
asset=mihomo-linux-$asset_target-$CORE_RELEASE.gz
fetch "$asset" "$expected"
gzip -t "$work/$asset"
chmod 644 "$work/$asset"
mv "$work/$asset" "$output/"
echo "Core $CORE_TAG for $target: $asset" >&2
