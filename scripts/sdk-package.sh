#!/bin/sh
# Package the prebuilt Hyper app with the official SDK rules, without compiling
# its runtime dependencies. Feeds installation and make defconfig must be done
# first. Run as the SDK's normal build user: sdk-package.sh [SDK directory]
set -eu

fail() {
	echo "sdk-package: $*" >&2
	exit 1
}

[ "$#" -le 1 ] || fail "usage: sdk-package.sh [SDK directory]"
TOPDIR="$(cd "${1:-.}" && pwd -P)"
case "$TOPDIR" in *[[:space:]]*) fail "SDK directory must not contain whitespace" ;; esac
[ "$(id -u)" -ne 0 ] || fail "run as the SDK's normal, non-root build user"
package_dir="$TOPDIR/package/feeds/hyper/luci-app-hyper"
[ -f "$TOPDIR/rules.mk" ] && [ -f "$TOPDIR/feeds/luci/luci.mk" ] &&
	[ -f "$package_dir/Makefile" ] || fail "prepare the official SDK and install the hyper/luci feeds first"
grep -q '^CONFIG_HAVE_DOT_CONFIG=y$' "$TOPDIR/.config" &&
	grep -Eq '^CONFIG_PACKAGE_luci-app-hyper=(m|y)$' "$TOPDIR/.config" ||
	fail "select CONFIG_PACKAGE_luci-app-hyper=m and run make defconfig first"
[ -s "$TOPDIR/tmp/.packageinfo" ] && [ -f "$TOPDIR/tmp/.packageauxvars" ] ||
	fail "missing package metadata; run make defconfig first"

# This optimization is intentionally limited to our static root/htdocs package.
# An added source tree, translation compiler, make include, hook, or build
# dependency must be reviewed before bypassing the SDK dependency scheduler.
for entry in "$package_dir"/* "$package_dir"/.[!.]* "$package_dir"/..?*; do
	[ -e "$entry" ] || [ -L "$entry" ] || continue
	case "${entry##*/}" in Makefile|root|htdocs) ;; *)
		fail "unsupported package input ${entry##*/}; use the full SDK build and review the prebuilt-package contract" ;;
	esac
done
if ! awk '
	function reject() { print "sdk-package: unsupported package Makefile line " NR ": " $0 > "/dev/stderr"; bad=1; exit 1 }
	{
		line=$0; sub(/[[:space:]]*#.*/, "", line)
		gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
		if (line == "") next
		if (conffiles) {
			if (line == "endef") { conffiles=0; next }
			if (line ~ /^\/[A-Za-z0-9_.\/-]+$/) next
			reject()
		}
		if (deps) {
			deps=(line ~ /\\$/); sub(/[[:space:]]*\\$/, "", line)
			if (line ~ /^\+[A-Za-z0-9_.+-]+([[:space:]]+\+[A-Za-z0-9_.+-]+)*$/) next
			reject()
		}
		if (line == "include $(TOPDIR)/rules.mk") { rules++; next }
		if (line == "include $(TOPDIR)/feeds/luci/luci.mk") { luci++; next }
		if (line == "LUCI_PKGARCH:=$(ARCH_PACKAGES)") { arch++; next }
		if (line == "PKG_NAME:=luci-app-hyper") { name++; next }
		if (line == "LUCI_MINIFY_JS:=0") { js++; next }
		if (line == "LUCI_MINIFY_CSS:=0") { css++; next }
		if (line == "define Package/luci-app-hyper/conffiles") { conffiles=1; next }
		if (line ~ /^(PKG_VERSION|PKG_RELEASE|PKG_LICENSE):=[A-Za-z0-9_.+~-]+$/) next
		if (line ~ /^LUCI_TITLE:=[A-Za-z0-9_. +()-]+$/) next
		if (line ~ /^LUCI_DEPENDS:=[[:space:]]*/) {
			sub(/^LUCI_DEPENDS:=[[:space:]]*/, "", line)
			deps=(line ~ /\\$/); sub(/[[:space:]]*\\$/, "", line)
			if (line == "" || line ~ /^\+[A-Za-z0-9_.+-]+([[:space:]]+\+[A-Za-z0-9_.+-]+)*$/) next
		}
		reject()
	}
	END { if (bad || conffiles || deps || rules!=1 || luci!=1 || arch!=1 || name!=1 || js!=1 || css!=1) exit 1 }
' "$package_dir/Makefile"; then
	fail "package is no longer the reviewed prebuilt app; use the full SDK build"
fi
for binary in mihomo hyperd; do
	[ -x "$package_dir/root/usr/libexec/hyper/bin/$binary" ] || fail "stage the prebuilt $binary first"
done
[ -s "$package_dir/htdocs/luci-static/hyper/index.html" ] || fail "stage the built web app first"

# Match the SDK top-level environment. In particular, the SDK fakeroot must use
# the SDK tar, not a host tar linked against a different libc (which can silently
# archive uid/gid 1000). Keep all strip, dependency checks and pack rules intact.
SDK=1
LC_ALL=C
LANG=C
ORIG_PATH="${ORIG_PATH:-$PATH}"
STAGING_DIR_HOST="$TOPDIR/staging_dir/host"
PATH="$STAGING_DIR_HOST/bin:$PATH"
HOST_OS="$(uname)"
HOST_ARCH="$(uname -m)"
GNU_HOST_NAME="$("$TOPDIR/scripts/config.guess")"
export TOPDIR SDK LC_ALL LANG ORIG_PATH STAGING_DIR_HOST PATH HOST_OS HOST_ARCH GNU_HOST_NAME
unset TAR TAR_OPTIONS STAGING_DIR C_INCLUDE_PATH CROSS_COMPILE ARCH LPATH
unset P4PORT P4USER P4CONFIG P4CLIENT QUILT_PATCHES QUILT_PATCH_OPTS MAKEFLAGS MFLAGS
CFLAGS= LDFLAGS=
export CFLAGS LDFLAGS
umask 022
for tool in tar fakeroot; do
	[ -x "$STAGING_DIR_HOST/bin/$tool" ] || fail "official SDK is missing host tool $tool"
done
probe="$(mktemp -d "$TOPDIR/tmp/hyper-package.XXXXXX")"
trap 'rm -rf "$probe"' EXIT HUP INT TERM
: > "$probe/owner"
"$STAGING_DIR_HOST/bin/fakeroot" "$STAGING_DIR_HOST/bin/tar" --format=gnu --numeric-owner \
	-cf "$probe/owner.tar" -C "$probe" owner
"$STAGING_DIR_HOST/bin/tar" --numeric-owner -tvf "$probe/owner.tar" |
	awk 'NR == 1 && $2 == "0/0" { ok=1 } END { exit !ok }' ||
	fail "SDK fakeroot/tar could not produce root-owned files"

set -- "TOPDIR=$TOPDIR" SDK=1
if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
	case "$SOURCE_DATE_EPOCH" in *[!0-9]*) fail "SOURCE_DATE_EPOCH must be an integer" ;; esac
	# A copied feed has no .git; package.mk otherwise falls back to the SDK date.
	set -- "$@" "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" "PKG_SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"
fi
if grep -q '^CONFIG_USE_APK=y$' "$TOPDIR/.config"; then
	for tool in apk openssl; do
		[ -x "$STAGING_DIR_HOST/bin/$tool" ] || fail "official SDK is missing host tool $tool"
	done
	# These official targets are normally dependencies of top-level package/compile.
	# Existing keys are reused; package/index signing stays with the SDK rules.
	make -C "$TOPDIR" "$@" "$TOPDIR/private-key.pem" "$TOPDIR/public-key.pem"
	[ -s "$TOPDIR/private-key.pem" ] && [ -s "$TOPDIR/public-key.pem" ] || fail "APK signing keys were not generated"
fi

make -C "$package_dir" "$@" clean
make -C "$package_dir" "$@" compile V=s
