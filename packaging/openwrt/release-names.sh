#!/bin/sh
# Give the package of a built feed (build.sh output) its Release name (internal/release
# PackageName), and its baseline/ fixture too, each beside a SHA256SUMS of that name.
set -eu
feed=${1:?feed directory}; version=${2:?release version}; format=${3:?ipk or apk}; arch=${4:?OpenWrt package arch}
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
name() (cd "$root" && go run ./cmd/hyper-release package-name "$1" "$format" "$arch")
rename() {
 set -- "$1" "$2" "$1"/*."$format"
 [ "$#" = 3 ] && [ -f "$3" ] || { echo "Expected one .$format package in $1" >&2; exit 1; }
 (cd "$1" && sha256sum -c SHA256SUMS >/dev/null)
 release=$(name "$2")
 [ "${3##*/}" = "$release" ] || mv "$3" "$1/$release"
 (cd "$1" && sha256sum "$release" > SHA256SUMS)
}
rename "$feed" "$version"
[ ! -d "$feed/baseline" ] || rename "$feed/baseline" 0.0.0
[ ! -d "$feed/preview" ] || rm -r "$feed/preview"
