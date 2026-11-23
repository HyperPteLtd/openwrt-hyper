#!/bin/sh
# Download the latest stable release's installer for one package format and OpenWrt
# architecture into an empty directory, beside its line of the release's SHA256SUMS. A
# release without that installer (an architecture or format added since) leaves the
# directory empty, and the install test skips the upgrade from it.
set -eu
format=${1:?ipk or apk}; arch=${2:?OpenWrt package architecture}; output=${3:?empty directory}
base=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
. "$base/retry.sh"
case "$format" in ipk|apk) ;; *) echo "Unknown package format: $format" >&2; exit 2;; esac
case "$arch" in ''|*[!a-z0-9_-]*) echo "Invalid package architecture: $arch" >&2; exit 2;; esac
[ -d "$output" ] && [ -z "$(ls -A "$output")" ] || { echo "Expected an empty directory: $output" >&2; exit 2; }
repo=${GITHUB_REPOSITORY:-HyperPteLtd/openwrt-hyper}
releases=https://github.com/$repo/releases
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
fetch() { curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 20 --max-time "$1" --max-filesize "$2" -o "$3" "$4"; }
# In CI the job's token reads the release through the API: dozens of install jobs fetching the
# public release pages at once get refused (403) and then time out. Elsewhere the public pages
# need no token. Both name the newest release that is neither a draft nor a pre-release.
if [ -n "${GH_TOKEN:-}" ] && command -v gh >/dev/null 2>&1; then
 latest() { bounded 60 gh api "repos/$repo/releases/latest" --jq .tag_name; }
 asset() { bounded "$1" gh release download "$tag" --repo "$repo" --pattern "$4" --output "$3" --clobber; }
else
 latest() {
  _latest=$(curl -fsSIL --proto '=https' --proto-redir '=https' --connect-timeout 20 --max-time 60 \
   -o /dev/null -w '%{url_effective}' "$releases/latest") || return 1
  printf '%s\n' "${_latest##*/}"
 }
 asset() { fetch "$1" "$2" "$3" "$releases/download/$tag/$4"; }
fi
# Reaching GitHub is the test's precondition, not its result: when the release can't be read,
# the upgrade from it is skipped with a warning instead of failing the whole matrix.
unreachable() {
 echo "::warning::Could not download the latest release ($1): skipping the upgrade from it" >&2
 exit 0
}
tag=$(retry latest) || unreachable 'release lookup failed'
if ! printf '%s\n' "$tag" | grep -Eqx 'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'; then
 echo "Unexpected latest release tag: $tag" >&2; exit 1
fi
retry asset 60 65536 "$work/SHA256SUMS" SHA256SUMS || unreachable "SHA256SUMS of $tag"
# The release names follow internal/release PackageName: the revision follows the last preview.
version=$(printf '%s' "${tag#v}" | sed 's/\./\\./g')
case "$format" in
 ipk) pattern="luci-app-hyper_$version-[1-9][0-9]*_$arch\\.ipk" ;;
 apk) pattern="luci-app-hyper-$version-r[1-9][0-9]*_$arch\\.apk" ;;
esac
count=$(grep -Ec "^[0-9a-f]{64}  $pattern\$" "$work/SHA256SUMS" || true)
case "$count" in
 0) echo "Release $tag has no $format package for $arch: skipping the upgrade from the latest release" >&2; exit 0 ;;
 1) ;;
 *) echo "Release $tag lists $count $format packages for $arch" >&2; exit 1 ;;
esac
line=$(grep -E "^[0-9a-f]{64}  $pattern\$" "$work/SHA256SUMS")
name=${line#*  }
# A truncated or corrupted copy is downloaded again. A copy that arrives whole but never matches
# the published checksum is a failure, not a skip.
download() {
 rm -f "$work/$name"
 asset 300 268435456 "$work/$name" "$name" || { rm -f "$work/$name"; return 1; }
 (cd "$work" && printf '%s\n' "$line" | sha256sum -c -)
}
if ! retry download; then
 [ -s "$work/$name" ] || unreachable "$name of $tag"
 echo "$name from $tag does not match the release's SHA256SUMS" >&2; exit 1
fi
mv "$work/$name" "$output/$name"
printf '%s\n' "$line" > "$output/SHA256SUMS"
echo "Downloaded $name from $tag, matching the release's SHA256SUMS" >&2
