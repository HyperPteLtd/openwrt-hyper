#!/bin/sh
# Run in the official ghcr.io/openwrt/sdk image as its default build user.
# Mount the prepared checkout read-only at /feed and a writable output directory
# at /artifacts. SOURCE_DATE_EPOCH must be the checkout commit's timestamp.
# Keep the image's working directory: release images already contain the SDK.
set -eu

feed="${FEED_DIR:-/feed}"
artifacts="${ARTIFACTS_DIR:-/artifacts}"
sdk="${SDK_DIR:-$(pwd)}"

case "${SOURCE_DATE_EPOCH:-}" in
	''|*[!0-9]*) echo 'SOURCE_DATE_EPOCH must be the checkout commit timestamp' >&2; exit 1 ;;
esac
export SOURCE_DATE_EPOCH

[ "$(id -u)" -ne 0 ] || { echo 'Run with the SDK image default build user, not root' >&2; exit 1; }
[ -f "$feed/luci-app-hyper/Makefile" ] || { echo "Package sources missing from $feed" >&2; exit 1; }
[ -f "$feed/scripts/sdk-package.sh" ] || { echo "Package build script missing from $feed" >&2; exit 1; }
[ -f "$feed/luci-app-hyper/htdocs/luci-static/hyper/index.html" ] || {
	echo 'Stage the built web app before running the SDK' >&2; exit 1;
}
for binary in sing-box hyper-crypt; do
	[ -x "$feed/luci-app-hyper/root/usr/libexec/hyper/bin/$binary" ] || {
		echo "Stage an executable $binary before running the SDK" >&2; exit 1;
	}
done
mkdir -p "$artifacts/bin"

cd "$sdk"
# Snapshot-style images initialize their SDK lazily; release images skip this.
if [ ! -f scripts/feeds ] && [ -f setup.sh ]; then
	bash setup.sh
fi
test -f Makefile
test -x scripts/feeds
test -f feeds.conf.default
sdk="$(pwd)"

# Preserve each SDK's feed revisions, including the packages metadata needed
# by curl. Use the same GitHub mirrors as gh-action-sdk. Routing/telephony/video
# feeds are unrelated to this package.
awk '
	$1 ~ /^src-/ {
		field=2
		while (field < NF && $field ~ /^--/) field++
		name=$field
		if (name == "base" || name == "packages" || name == "luci") {
			gsub(/https:\/\/git\.openwrt\.org\/(feed|openwrt|project)\//, "https://github.com/openwrt/")
			print; seen[name]++
		}
	}
	END {
		if (seen["base"] != 1 || seen["packages"] != 1 || seen["luci"] != 1) {
			print "SDK must define exactly one base, packages and luci feed" > "/dev/stderr"
			exit 1
		}
	}
' feeds.conf.default > feeds.conf
printf 'src-link hyper %s\n' "$feed" >> feeds.conf

echo '== SDK feeds'
cat feeds.conf
./scripts/feeds update base packages luci hyper
./scripts/feeds install -p hyper luci-app-hyper

# The web assets are already built and minified. Preserve the package metadata
# and all runtime dependencies while selecting only the package to build.
cat >> .config <<'CONFIG'
CONFIG_PACKAGE_luci-app-hyper=m
# CONFIG_LUCI_CSSTIDY is not set
# CONFIG_LUCI_JSMIN is not set
# CONFIG_LUCI_SRCDIET is not set
CONFIG
make defconfig

echo '== Build Hyper package'
sh "$feed/scripts/sdk-package.sh" "$sdk"
test -d bin/packages
cp -a bin/packages "$artifacts/bin/"
echo "== Packages copied to $artifacts/bin/packages"
