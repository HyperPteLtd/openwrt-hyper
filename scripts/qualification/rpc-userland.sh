#!/bin/sh
set -eu
work=$(mktemp -d)
cid=
cleanup(){ if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null 2>&1 || :; fi; rm -rf "$work"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
for bin in hyperd hyper-subscription hyper-kernel; do
 CGO_ENABLED=0 GOARCH=arm64 GOOS=linux go build -trimpath -o "$work/$bin" "./cmd/$bin"
done
for release in ${HYPER_TEST_RELEASES:-23.05.6 24.10.8 25.12.6}; do
 cid=$(docker create --network none --cap-add NET_ADMIN --entrypoint sh "hyper-test/network:$release-arm64" /tmp/check-rpc.sh)
 docker cp "$work/." "$cid:/usr/sbin/"
 docker cp files/. "$cid:/"
 docker cp scripts/qualification/rpc-guest.sh "$cid:/tmp/check-rpc.sh"
 docker start -a "$cid"
 [ "$(docker inspect -f '{{.State.ExitCode}}' "$cid")" = 0 ]
 docker rm "$cid" >/dev/null
 cid=
 echo "LuCI ubus backend passed on OpenWrt $release ARM64"
done
