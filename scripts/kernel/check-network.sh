#!/bin/sh
# Run in an isolated Docker network namespace. Each image uses the matching
# OpenWrt ip-full/nft/dnsmasq packages and shares the Docker VM kernel.
set -eu
archive=${1:?pass the pinned Mihomo Linux ARM64 archive}
expected=${2:?pass its independently verified SHA-256}
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
cid=
cleanup() { if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null || :; fi; rm -rf "$work"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
[ "$(openssl dgst -sha256 < "$archive" | awk '{print $NF}')" = "$expected" ]
dir=$(basename "$archive" .tar.gz)
tar -xOzf "$archive" "$dir/mihomo" >"$work/core"
chmod 755 "$work/core"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$work/peer" "$root/scripts/kernel/network-fixture/echo.go"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$work/hyperd" ./cmd/hyperd
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$work/net" ./cmd/hyper-net
backend=${HYPER_TEST_FIREWALL:-fw4}
case "$backend" in fw4|fw3) ;; *) exit 2;; esac
for release in ${HYPER_TEST_RELEASES:-23.05.6 24.10.8 25.12.6}; do
 image=hyper-test/network:$release-arm64
 if ! docker image inspect "$image" >/dev/null 2>&1; then
  # The official rootfs uses OpenWrt's CPU name in its platform metadata.
  # Stage its filesystem into an ARM64 image the Docker VM can run natively.
  docker build --platform linux/arm64 --build-arg "RELEASE=$release" -t "$image" - <<'DOCKERFILE'
ARG RELEASE
ARG ROOTFS_PLATFORM=linux/aarch64_generic
FROM --platform=$ROOTFS_PLATFORM openwrt/rootfs:aarch64_generic-$RELEASE AS rootfs
FROM scratch
COPY --from=rootfs / /
RUN mkdir -p /var/lock && if command -v apk >/dev/null; then apk update && apk add ip-full; else opkg update && opkg install ip-full; fi
DOCKERFILE
 fi
 if [ "$backend" = fw3 ]; then
  legacy_image=hyper-test/fw3:$release-dns
  if ! docker image inspect "$legacy_image" >/dev/null 2>&1; then
   printf 'FROM %s\n' "$image" >"$work/Dockerfile"
   cat >>"$work/Dockerfile" <<'DOCKERFILE'
RUN set -eu; mkdir -p /var/lock; set -- firewall iptables-zz-legacy ip6tables-zz-legacy ip6tables-mod-nat iptables-mod-extra iptables-mod-tproxy; \
 if command -v apk >/dev/null; then apk update && apk del --rdepends firewall4 && apk add "$@"; \
 else opkg update && opkg remove --force-depends firewall4 && opkg install "$@"; fi
DOCKERFILE
   docker build -t "$legacy_image" - <"$work/Dockerfile"
  fi
  image=$legacy_image
 fi
 cid=$(docker create -e "TEST_METHODS=${HYPER_TEST_METHODS:-tproxy tun}" -e "TEST_TRACE=${HYPER_TEST_TRACE:-0}" -e "TEST_FIREWALL=$backend" --network none --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt seccomp=unconfined --security-opt apparmor=unconfined --device /dev/net/tun --sysctl net.ipv4.ip_forward=1 --sysctl net.ipv6.conf.all.disable_ipv6=0 --sysctl net.ipv6.conf.all.forwarding=1 "$image" sh /tmp/test)
 docker cp "$work/net" "$cid:/usr/sbin/hyper-net"
 for bin in core peer hyperd; do docker cp "$work/$bin" "$cid:/tmp/$bin"; done
 docker cp "$root/files/." "$cid:/"
 docker cp "$root/scripts/kernel/network-fixture/inside.sh" "$cid:/tmp/test"
 docker start -a "$cid"
 [ "$(docker inspect -f '{{.State.ExitCode}}' "$cid")" = 0 ]
 docker rm "$cid" >/dev/null
 cid=
 echo "OpenWrt $release ARM64 $backend network regression passed"
done
