#!/bin/sh
# Build a native OpenWrt package in a disposable, version-matched official SDK.
set -eu
release=${1:?OpenWrt version}; arch=${2:?OpenWrt package arch}; version=${3:?plugin version}; output=${4:?output directory}
case "$release" in 23.05.6|24.10.8|25.12.6) ;; *) echo "unsupported OpenWrt: $release" >&2; exit 1;; esac
base=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# The selected SDK supplies the native packaging tools.
# HYPER_PACKAGE_ARCH builds the package for one OpenWrt architecture, as released;
# without it the output is a universal test package with every supported CPU.
"$base/runtime-abi" "$arch" >/dev/null
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
(cd "$root" && go run ./cmd/hyper-release version "$version")
mode=${HYPER_FEED_MODE:-production}
case "$mode" in test|production) ;; *) exit 1;; esac
tmp=$(mktemp -d)
clean() { rm -rf "$tmp"; }
trap clean EXIT
trap 'exit 1' INT TERM
mkdir -p "$tmp/pkg/usr/sbin" "$output"
[ -z "$(ls -A "$output")" ] || { echo 'Package output directory must be empty' >&2; exit 1; }
cp -R "$root/files/." "$tmp/pkg"
# Browsers cache LuCI's ES modules by URL, so each build versions them to load the new interface.
stamp="$version-${HYPER_PACKAGE_REVISION:-1}${HYPER_PACKAGE_PRERELEASE:+-p$HYPER_PACKAGE_PRERELEASE}"
luci="$tmp/pkg/www/luci-static/resources"
find "$luci/hyper" -name '*.js' -exec sed -i -E "s#((from|import\\() *')(\\.\\.?/[^'?]+\\.js)'#\\1\\3?v=$stamp'#g" {} +
find "$luci/hyper" -name '*.css' -exec sed -i -E "s#url\\('(\\.\\.?/[^'?]+\\.css)'\\)#url('\\1?v=$stamp')#g" {} +
# L.resource() drops any path part with characters outside its whitelist, such as ? and =,
# so the version goes after the resolved URL.
sed -i -E "s#L\\.resource\\('hyper/(app\\.js|style\\.css)'\\)#L.resource('hyper/\\1') + '?v=$stamp'#g" "$luci/view/hyper/router.js"
grep -q "app.js') + '?v=" "$luci/view/hyper/router.js" && grep -q "style.css') + '?v=" "$luci/view/hyper/router.js"
(cd "$tmp/pkg/usr/share/hyper/rules" && sha256sum -c SHA256SUMS)
(cd "$root" && go run ./cmd/hyper-release rules --check-package "$tmp/pkg/usr/share/hyper/rules")
mkdir -p "$tmp/pkg/usr/share/licenses/luci-app-hyper"
install -m 644 "$root/LICENSE" "$tmp/pkg/usr/share/licenses/luci-app-hyper/LICENSE"
mkdir -p "$tmp/pkg/usr/libexec/hyper"
install -m 755 "$root/packaging/openwrt/package-update" "$tmp/pkg/usr/libexec/hyper/package-update"
install -m 755 "$root/packaging/openwrt/package-recovery" "$tmp/pkg/usr/libexec/hyper/package-recovery"
install -m 755 "$base/runtime-abi" "$tmp/pkg/usr/libexec/hyper/runtime-abi"
install -m 755 "$base/activate-runtime" "$tmp/pkg/usr/libexec/hyper/activate-runtime"
for cli in hyperd hyper-kernel hyper-net hyper-subscription; do
 ln -s ../libexec/hyper/manager "$tmp/pkg/usr/sbin/$cli"
done
mkdir -m 1777 "$tmp/pkg/tmp"
payload="$tmp/pkg/tmp/hyper-package-payload"
mkdir -m 700 "$payload"
package_arch=${HYPER_PACKAGE_ARCH:-}
revision=${HYPER_PACKAGE_REVISION:-1}
# A develop build also emits preview/ with package revision <N> (version-rN) for the pre-release.
prerelease=${HYPER_PACKAGE_PRERELEASE:-}
case "$prerelease" in ''|[1-9]|[1-9][0-9]|[1-9][0-9][0-9]|[1-9][0-9][0-9][0-9]) ;; *) echo 'Expected a preview number' >&2; exit 1;; esac
case "$revision" in 0*|*[!0-9]*|'') echo 'Expected a positive package revision' >&2; exit 1;; esac
[ -n "$package_arch" ] || [ "$revision" = 1 ] || { echo 'Release assets use revision 1' >&2; exit 1; }
if [ -n "$package_arch" ]; then
 abis=$("$base/runtime-abi" "$package_arch")
 # The core the package carries for its CPU (fetch-core.sh): from HYPER_CORE_DIR/<ABI>, as
 # CI's core jobs fetch it, or downloaded here. A universal test package carries none.
 if [ -n "${HYPER_CORE_DIR:-}" ]; then
  core=$HYPER_CORE_DIR/$abis
 else
  core=$tmp/core
  mkdir "$core"
  "$base/fetch-core.sh" "$abis" "$core"
 fi
 test -s "$core/core/pin.json"
 mkdir -p "$tmp/pkg/usr/share/hyper/core" "$tmp/pkg/usr/share/licenses/luci-app-hyper/core"
 cp "$core/core/"* "$tmp/pkg/usr/share/hyper/core/"
 cp "$core/licenses/"* "$tmp/pkg/usr/share/licenses/luci-app-hyper/core/"
 chmod 644 "$tmp/pkg/usr/share/hyper/core/"* "$tmp/pkg/usr/share/licenses/luci-app-hyper/core/"*
else
 abis=$("$base/runtime-abi" --list)
fi
for abi in $abis; do
 if [ -n "${HYPER_RUNTIME_DIR:-}" ]; then
  metadata="$HYPER_RUNTIME_DIR/$abi.build"
  test "$(sed -n '2p' "$metadata")" = "$version"
  test "$(sed -n '3p' "$metadata")" = "${HYPER_MIHOMO_PUBLIC_KEY:-}"
  (cd "$HYPER_RUNTIME_DIR" && head -1 "$abi.build" | sha256sum -c -)
  cp "$HYPER_RUNTIME_DIR/$abi.gz" "$payload/$abi.gz"
 else
  "$base/build-runtime.sh" "$abi" "$version" "$payload"
  rm "$payload/$abi.build"
 fi
done
(cd "$payload" && sha256sum *.gz > SHA256SUMS)
(cd "$root" && go run ./cmd/hyper-release licenses "$tmp/pkg/usr/share/licenses/luci-app-hyper")
cp "$root/packaging/openwrt/sdk-package.sh" "$tmp/package.sh"
image="ghcr.io/openwrt/sdk:$arch-$release"
# A container would copy the entire SDK into an anonymous /builder volume.
# BuildKit reads the SDK layers in place and exports only the package feed.
docker buildx build --platform linux/amd64 --file "$base/package.Dockerfile" \
 --build-arg "SDK=$image" --build-arg "RELEASE=$release" --build-arg "ARCH=$arch" \
 --build-arg "VERSION=$version" --build-arg "REVISION=$revision" \
 --build-arg "PACKAGE_ARCH=$package_arch" --build-arg "FEED_MODE=$mode" --build-arg "PRERELEASE=$prerelease" \
 --output "type=local,dest=$output" "$tmp"
