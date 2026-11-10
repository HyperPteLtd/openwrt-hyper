#!/bin/sh
# Runs inside the builder container as a normal user (OpenWrt refuses to build
# as root); called by scripts/build-packages.sh.
#   sdk-build.sh <release> <target>       e.g. 24.10.8 x86/64

set -eu

release="$1"
target="$2"
base="https://downloads.openwrt.org/releases/$release/targets/$target"
dir="/work/$release-$(echo "$target" | tr / -)"
mkdir -p /work/dl "$dir"
log="$dir/build.log"
: > "$log"

# official SDK, checked against the release's sha256sums
line="$(wget -qO- "$base/sha256sums" | grep -E '[ *]openwrt-sdk-.*\.tar\.(zst|xz)$' | head -n 1)"
[ -n "$line" ] || { echo "no SDK listed in $base/sha256sums" >&2; exit 1; }
sum="${line%% *}"
file="${line##*[ *]}"
if [ ! -f "/work/dl/$file" ]; then
	echo "   download $file"
	wget -q -O "/work/dl/$file.part" "$base/$file"
	mv "/work/dl/$file.part" "/work/dl/$file"
fi
echo "$sum  /work/dl/$file" | sha256sum -c - >/dev/null

if [ ! -d "$dir/sdk" ]; then
	echo "   unpack SDK"
	rm -rf "$dir/unpack" && mkdir -p "$dir/unpack"
	case "$file" in
		*.zst) tar -C "$dir/unpack" --zstd -xf "/work/dl/$file" ;;
		*) tar -C "$dir/unpack" -xf "/work/dl/$file" ;;
	esac
	mv "$dir"/unpack/openwrt-sdk-* "$dir/sdk"
	rmdir "$dir/unpack"
fi

# fresh copy of the two packages as a local feed
rm -rf "$dir/feed" && mkdir -p "$dir/feed"
cp -a /work/src/luci-app-hyper "$dir/feed/"

cd "$dir/sdk"
grep -q '^src-link hyper ' feeds.conf.default || echo "src-link hyper $dir/feed" >> feeds.conf.default

echo "   feeds"
./scripts/feeds update base packages luci hyper >> "$log" 2>&1
./scripts/feeds install -p hyper luci-app-hyper >> "$log" 2>&1

# Minifiers are off for this package anyway (LUCI_MINIFY_*); skip building csstidy
cat >> .config <<EOF
# CONFIG_LUCI_CSSTIDY is not set
# CONFIG_LUCI_JSMIN is not set
EOF
make defconfig >> "$log" 2>&1

echo "   compile (log: $log)"
rm -rf bin/packages/*/hyper
make package/luci-app-hyper/clean >> "$log" 2>&1
make -j"$(nproc)" package/luci-app-hyper/compile V=s >> "$log" 2>&1

ls -1 bin/packages/*/hyper/
