#!/bin/sh
# Public filename only: leave the SDK package and its internal version intact.
# Usage: package-name.sh <SDK filename> <firmware family> <package architecture>
set -eu

fail() { printf '%s\n' "$*" >&2; exit 1; }
[ "$#" -eq 3 ] || fail "usage: package-name.sh <SDK filename> <firmware family> <package architecture>"
name="${1##*/}"
family="$2"
arch="$3"
case "$arch" in ''|*[!A-Za-z0-9_-]*) fail "invalid package architecture: $arch" ;; esac

case "$name" in
	luci-app-hyper_*.ipk)
		[ "$family" = '23.05-24.10' ] || fail "IPK does not match firmware family: $family"
		version="${name#luci-app-hyper_}"
		suffix="_$arch.ipk"
		case "$version" in *"$suffix") version="${version%"$suffix"}" ;; *) fail "IPK architecture does not match $arch" ;; esac
		ext=ipk ;;
	luci-app-hyper-*.apk)
		[ "$family" = '25.12+' ] || fail "APK does not match firmware family: $family"
		version="${name#luci-app-hyper-}"
		version="${version%.apk}"
		ext=apk ;;
	*) fail "unsupported SDK package filename: $name" ;;
esac

# Strip only OpenWrt's trailing revision, preserving pre-release identifiers.
version="$(printf '%s\n' "$version" | sed 's/-r[0-9][0-9]*$//')"
case "$version" in ''|*[!A-Za-z0-9.+~-]*) fail "invalid package version: $version" ;; esac
case "$ext" in
	ipk) printf '%s_luci-app-hyper_%s_%s.ipk\n' "$family" "$version" "$arch" ;;
	apk) printf '%s_luci-app-hyper-%s-%s.apk\n' "$family" "$version" "$arch" ;;
esac
