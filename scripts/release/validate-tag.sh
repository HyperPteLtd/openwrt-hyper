#!/bin/sh
# Package versions and the automatic update channel support stable SemVer tags.
set -eu
if [ "$#" -ne 1 ] || ! printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
	echo 'Release tag must be vX.Y.Z (for example v1.2.3); prerelease tags are not supported' >&2
	exit 1
fi
case "$1" in
	*[!v0-9.]*) echo 'Release tag must be a single vX.Y.Z value' >&2; exit 1 ;;
esac
