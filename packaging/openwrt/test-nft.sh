#!/bin/sh
# QEMU linux-user rejects NETLINK_NETFILTER. Use the Docker host architecture's
# nft for that syscall in the target container's own network namespace.
set -eu
target=${1:?container}; release=${2:?release}; arch=${3:?package architecture}
base=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
. "$base/retry.sh"
in_target() { bounded 600 docker exec "$target" "$@"; }
if detail=$(in_target nft list tables 2>&1); then exit 0; fi
case "$detail" in
 *'Protocol not supported'*) ;;
 *) printf '%s\n' "$detail" >&2; exit 1 ;;
esac
printf 'Target nft under linux-user: %s\n' "$detail" >&2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
bundle=/tmp/hyper-nft-userland
docker image inspect alpine:3.23 >/dev/null 2>&1 || retry bounded 180 docker pull --quiet alpine:3.23 >&2
# apk fetches nftables from Alpine's mirrors; each attempt rewrites the whole bundle.
native_bundle() {
 bounded 300 docker run --rm alpine:3.23 sh -ec '
  apk add --no-cache nftables >&2
  mkdir /bundle
  nft=$(command -v nft)
  cp "$nft" /bundle/nft
  cp -L /lib/ld-musl-*.so.1 /bundle/ld
  ldd "$nft" > /tmp/libs
  awk '\''$2 == "=>" && $3 ~ /^\// {print $1, $3}'\'' /tmp/libs |
    while read -r name path; do cp -L "$path" "/bundle/$name"; done
  tar -C /bundle -cf - .
 ' > "$tmp/native.tar"
}
retry native_bundle
in_target mkdir -m 700 "$bundle"
docker cp - "$target:$bundle" < "$tmp/native.tar"

# A target-linked entry point can be copied and inspected by the ordinary
# OpenWrt uninstall hook, including when apk has removed the installed tools.
sdk=ghcr.io/openwrt/sdk:$arch-$release
docker image inspect "$sdk" >/dev/null 2>&1 || retry bounded 180 docker pull --quiet --platform linux/amd64 "$sdk" >&2
docker run --rm -i --platform linux/amd64 --entrypoint sh "$sdk" -ec '
 export STAGING_DIR=/builder/staging_dir
 set -- /builder/staging_dir/toolchain-*/bin/*-gcc
 "$1" -Os -s -Wall -Werror -x c -o /tmp/nft-launcher -
 cat /tmp/nft-launcher
' > "$tmp/nft" <<'C'
#include <stdio.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    char *args[argc + 4];
    args[0] = "/tmp/hyper-nft-userland/ld";
    args[1] = "--library-path";
    args[2] = "/tmp/hyper-nft-userland";
    args[3] = "/tmp/hyper-nft-userland/nft";
    memcpy(args + 4, argv + 1, argc * sizeof(*argv));
    execv(args[0], args);
    perror("exec nft in the test namespace");
    return 126;
}
C
chmod 755 "$tmp/nft"
guest_nft=$(in_target sh -ec 'command -v nft')
docker cp "$tmp/nft" "$target:$guest_nft"
in_target nft list tables
