#!/bin/sh
# Package-manager and uninstall checks inside fresh, disposable containers.
set -eu
release=${1:?firmware}; arch=${2:?package arch}; feed=${3:?directory with the package for this architecture}
rootfs=${HYPER_TEST_ROOTFS:-openwrt/rootfs:$arch-$release}
base=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
. "$base/retry.sh"
response=$(mktemp)
release_tool=$(mktemp)
published=$(mktemp -d)
network= server= target= generated=
# Release packages declare their OpenWrt architecture (build.sh with HYPER_PACKAGE_ARCH).
package_arch=$arch
cleanup() {
 rm -f "$response" "$release_tool"
 rm -rf "$published"
 [ -z "$target" ] || docker rm -f "$target" >/dev/null
 [ -z "$server" ] || docker rm -f "$server" >/dev/null
 [ -z "$network" ] || docker network rm "$network" >/dev/null
 [ -z "$generated" ] || docker image rm "$generated" >/dev/null
}
trap cleanup EXIT
trap 'exit 1' TERM INT
# Every command run in a container is bounded: a stuck package manager or service fails
# the job instead of holding the runner.
in_target() { bounded 600 docker exec "$target" "$@"; }
in_server() { bounded 600 docker exec "$server" "$@"; }
# The build job compiled this tool already; what can fail here is the module download.
(cd "$base/../.." && retry go build -o "$release_tool" ./cmd/hyper-release)
# Official rootfs manifests use the OpenWrt package architecture as the OCI platform.
target_platform=${DOCKER_DEFAULT_PLATFORM:-}
if [ -z "${HYPER_TEST_ROOTFS:-}" ]; then target_platform=linux/$arch; fi
# Many architectures have no rootfs image. That answer is final; other pull failures are
# retried, and the ImageBuilder remains the fallback once they run out.
rootfs_missing=
pull_rootfs() {
 _pull_status=0
 _pull=$(export DOCKER_DEFAULT_PLATFORM="$target_platform"; bounded 180 docker pull "$rootfs" 2>&1) || _pull_status=$?
 printf '%s\n' "$_pull" >&2
 case "$_pull_status:$_pull" in
  0:*) return 0 ;;
  *'manifest unknown'*|*'no matching manifest'*|*': not found'*) rootfs_missing=1; return 0 ;;
 esac
 return "$_pull_status"
}
if ! docker image inspect "$rootfs" >/dev/null 2>&1 && { ! retry pull_rootfs || [ -n "$rootfs_missing" ]; }; then
 [ -z "${HYPER_TEST_ROOTFS:-}" ] || exit 1
 generated=$(sh "$base/test-rootfs.sh" "$release" "$arch")
 rootfs=$generated
fi
docker image inspect busybox:1.37 >/dev/null 2>&1 || retry bounded 180 docker pull --quiet busybox:1.37
case "$release" in 25.*) ext=apk;; *) ext=ipk;; esac
# The upgrade users make: the latest published release's package for this router, if it has one.
sh "$base/fetch-release.sh" "$ext" "$arch" "$published"
network=$(docker network create hyper-pkg-test-$$)
server=$(docker create --network "$network" --network-alias feed busybox:1.37 httpd -f -p 8080 -h /srv)
docker cp "$feed/." "$server:/srv"
docker start "$server" >/dev/null
# The candidate package embeds the official URLs. Only the isolated router's
# test transport maps those exact URLs to the local server.
serve_release() {
 subtree=$1; shift
 asset=$("$release_tool" fake-latest "$feed$subtree" "$response" "$@")
 in_server sh -ec '
  directory=/srv/releases/download/${1%/*}
  mkdir -p "$directory"
  ln -sf "/srv$2/${1##*/}" "$directory/${1##*/}"
 ' sh "$asset" "$subtree"
 chmod 644 "$response"
 docker cp "$response" "$server:/srv/releases/latest"
}
serve_release ""
# The shell stays PID 1 and reaps orphans while it waits. The router lives an hour at
# most, should this script die before its cleanup.
target=$(DOCKER_DEFAULT_PLATFORM="$target_platform" docker create --network "$network" --cap-add NET_ADMIN -e "HYPER_PACKAGE_ARCH=$package_arch" --entrypoint sh "$rootfs" -c 'for _ in 1 2 3 4 5 6; do sleep 600; done')
docker start "$target" >/dev/null
docker cp "$base/retry.sh" "$target:/tmp/hyper-retry"
docker cp "$base/check-web.sh" "$target:/tmp/check-web"
docker cp "$base/check-installed.sh" "$target:/tmp/check-installed"
in_target mkdir /tmp/verified-feed
case "$release" in
 25.*) set -- "$feed"/SHA256SUMS "$feed"/*.apk ;;
 *) set -- "$feed"/SHA256SUMS "$feed"/*.ipk ;;
esac
for item do docker cp "$item" "$target:/tmp/verified-feed/"; done
docker cp "$feed/baseline" "$target:/tmp/hyper-baseline"
if [ -n "$(ls -A "$published")" ]; then docker cp "$published" "$target:/tmp/hyper-release"; fi
in_target sh /tmp/check-installed prepare "$release" "$arch"
sh "$base/test-nft.sh" "$target" "$release" "$arch"
docker network disconnect "$network" "$target"
test "$(docker inspect -f '{{len .NetworkSettings.Networks}}' "$target")" = 0
in_target sh /tmp/check-installed offline-install
in_target sh /tmp/check-web
in_target sh /tmp/check-installed uninstall
docker network connect "$network" "$target"
echo "Verified offline single-package installation, all four commands and LuCI RPC on $release ($arch)"
# Keep the original release digest while changing the served download.
in_server sh -c 'set -e; for f in /srv/*.ipk /srv/*.apk; do [ -f "$f" ] || continue; cp "$f" "$f.clean"; dd if=/dev/zero of="$f" bs=1 count=16 conv=notrunc 2>/dev/null; done'
in_target sh /tmp/check-installed install-baseline
in_target sh /tmp/check-installed reject-tampered-upgrade
in_server sh -c 'set -e; for f in /srv/*.clean; do mv "$f" "${f%.clean}"; done'
for invalid in digest tag origin url duplicate draft prerelease missing; do
 serve_release "" --invalid "$invalid"
 in_target sh /tmp/check-installed reject-invalid-release
done
set -- "$feed/baseline/"*."$ext"
serve_release "" --contents "$1"
in_server sh -ec 'for f in /srv/releases/download/v*/*; do ln -sf "/srv/baseline/$1" "$f"; done' sh "${1##*/}"
in_target sh /tmp/check-installed reject-wrong-version
serve_release ""
in_target sh /tmp/check-installed upgrade
in_target sh /tmp/check-web
serve_release /baseline
in_target sh /tmp/check-installed reject-downgrade
if [ "$release" = 25.12.6 ]; then
 in_target sh /tmp/check-installed prepare-recovery
 docker exec -d "$target" sh -ec 'exec 8>/var/run/hyper/network/lock; flock -x 8; touch /tmp/block-release; while [ -f /tmp/block-release ]; do sleep 1; done'
 in_target sh /tmp/check-installed uninstall-with-recovery
else
 in_target sh /tmp/check-installed uninstall preserved
fi
echo "Verified native package installation, GitHub digest/identity checks, upgrade and removal on OpenWrt $release ($arch)"
# prepare keeps the published release only when it is older than this build.
if [ "$(in_target sh -c 'if [ -d /tmp/hyper-release ]; then echo staged; fi')" = staged ]; then
 in_target mkdir /tmp/hyper-candidate
 set -- "$feed"/SHA256SUMS "$feed"/*."$ext"
 for item do docker cp "$item" "$target:/tmp/hyper-candidate/"; done
 in_target sh /tmp/check-installed install-release
 in_target sh /tmp/check-installed upgrade-from-release
 in_target sh /tmp/check-web
 in_target sh /tmp/check-installed uninstall preserved
 echo "Verified the upgrade from the latest published release to this build and its removal on OpenWrt $release ($arch)"
else
 echo "Skipped the upgrade from the latest published release on OpenWrt $release ($arch); the reason is above"
fi
