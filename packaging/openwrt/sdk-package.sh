#!/bin/sh
# Runs inside the official SDK selected by build.sh.
set -eu
cd /builder
export PATH="/builder/staging_dir/host/bin:$PATH" LC_ALL=C
export MKHASH=/builder/staging_dir/host/bin/mkhash
input=/tmp/input
pkg="$input/pkg"
feed=/tmp/feed
name=luci-app-hyper
# The dependencies 0.x declared, so a router upgrading from it (offline, by its own
# updater) already has every one: fw4 with TProxy, the TUN driver, full iproute2 for policy
# routing, jsonfilter and the CA bundle for HTTPS.
deps='libc luci-base procd busybox rpcd jsonfilter firewall4 kmod-nft-tproxy kmod-tun ip-full ca-bundle'
runtime=$pkg/usr/libexec/hyper
payload=$pkg/tmp/hyper-package-payload
install -d -m 1777 "$pkg/tmp"
if [ -d "$runtime/install-payload" ]; then mv "$runtime/install-payload" "$payload"; fi
chmod 0700 "$payload"
test -s "$payload/SHA256SUMS"
(cd "$payload" && sha256sum -c SHA256SUMS)
: > "$runtime/runtime.sha256"
: > "$input/runtime-space"
for archive in "$payload"/*.gz; do
 abi=${archive##*/}; abi=${abi%.gz}
 case "$abi" in ''|*[!a-zA-Z0-9_.-]*) echo 'Invalid runtime ABI filename' >&2; exit 1;; esac
 gzip -dc "$archive" > "$input/verify-runtime"
 test -s "$input/verify-runtime"
 hash=$(sha256sum "$input/verify-runtime")
 printf '%s  %s\n' "${hash%% *}" "$abi" >> "$runtime/runtime.sha256"
 bytes=$(wc -c < "$input/verify-runtime")
 printf ' %s) runtime_kb=%s;;\n' "$abi" "$(( (bytes+1023)/1024 + 1024 ))" >> "$input/runtime-space"
done
rm -f "$input/verify-runtime"
chown -R 0:0 "$pkg"
cat > "$input/preinst" <<'HOOK'
#!/bin/sh
set -eu
root=${IPKG_INSTROOT:-}
tmp=$root/tmp
if [ -z "$root" ]; then
 missing=""
 for tool in ubus uci jsonfilter flock sha256sum gzip tar; do
  command -v "$tool" >/dev/null || missing="$missing $tool"
 done
 if [ -n "$missing" ]; then
  echo "Hyper needs these missing firmware commands:$missing. Install the corresponding providers and retry." >&2
  exit 1
 fi
 [ "$(jsonfilter -s '{"ok":true}' -e '@.ok')" = true ] || { echo 'The firmware jsonfilter command is unusable' >&2; exit 1; }
fi
[ -n "$root" ] || [ ! -e /tmp/hyper-recovery/pending ] || {
 echo 'Previous Hyper removal is still restoring direct networking' >&2; exit 1;
}
if [ ! -e "$tmp" ]; then mkdir -m 1777 "$tmp"; fi
[ ! -L "$tmp" ] && [ -d "$tmp" ] || exit 1
owner=$(id -u)
ls -ldn "$tmp" | awk -v owner="$owner" '
 $3==owner && (substr($1,10,1) ~ /[tT]/ || (substr($1,6,1)!="w" && substr($1,9,1)!="w")) {ok=1}
 END {exit !ok}' || { echo 'Unsafe temporary directory for Hyper installation' >&2; exit 1; }
dir=$tmp/hyper-package-payload
if [ -e "$dir" ] || [ -L "$dir" ]; then
 [ ! -L "$dir" ] && [ -d "$dir" ] &&
  [ "$(ls -ldn "$dir" | awk '{print substr($1,1,10), $3}')" = "drwx------ $owner" ] || {
  echo 'Unsafe Hyper package staging directory' >&2; exit 1;
 }
 rm -rf "$dir"
fi
if command -v df >/dev/null; then
 . "$root/etc/openwrt_release"
 runtime_abi() (
HOOK
cat "$runtime/runtime-abi" >> "$input/preinst"
cat >> "$input/preinst" <<'HOOK'
 )
 abi=$(runtime_abi "$DISTRIB_ARCH") || exit 1
 case "$abi" in
HOOK
cat "$input/runtime-space" >> "$input/preinst"
payload_kb=$(du -sk "$payload" | awk '{print $1}')
printf ' *) echo "This Hyper package has no program for the $abi CPU ($DISTRIB_ARCH); download the package for $DISTRIB_ARCH" >&2; exit 1;;\n esac\n payload_kb=%s\n' "$(( payload_kb + 1024 ))" >> "$input/preinst"
cat >> "$input/preinst" <<'HOOK'
 dest=$root/usr/libexec/hyper
 while [ ! -d "$dest" ]; do dest=${dest%/*}; [ -n "$dest" ] || dest=/; done
 space() { df -Pk "$1" | awk 'NR==2 && $4 ~ /^[0-9]+$/ {print $4, $6}'; }
 set -- $(space "$tmp"); [ "$#" = 2 ] || exit 1
 tmp_kb=$1; tmp_mount=$2
 set -- $(space "$dest"); [ "$#" = 2 ] || exit 1
 if [ "$2" = "$tmp_mount" ]; then payload_kb=$((payload_kb+runtime_kb)); fi
 if [ "$tmp_kb" -lt "$payload_kb" ]; then
  echo "Hyper installation needs $payload_kb KiB free on $tmp_mount ($tmp_kb available)" >&2; exit 1
 fi
 if [ "$1" -lt "$runtime_kb" ]; then
  echo "Hyper $abi needs $runtime_kb KiB free on $2 ($1 available)" >&2; exit 1
 fi
fi
mkdir -m 700 "$dir"
if [ -z "$root" ]; then
 if ! (exec 9>"$dir/test.lock"; flock -n 9 && flock -u 9 && flock -x 9); then
  rm -f "$dir/test.lock"; rmdir "$dir"
  echo 'The firmware flock command must support file descriptor locks (-n, -u and -x)' >&2; exit 1
 fi
 rm -f "$dir/test.lock"
 # 0.x ran /usr/libexec/hyper/bin/hyperd, which its package manager never stops on an
 # upgrade. Stop it and release its network takeover while its own scripts are still here.
 if [ -x /usr/libexec/hyper/bin/hyperd ]; then
  /etc/init.d/hyper stop >/dev/null 2>&1 || :
  [ ! -x /usr/libexec/hyper/network ] || /usr/libexec/hyper/network down >/dev/null 2>&1 || :
 fi
fi
HOOK
cat > "$input/postinst" <<'HOOK'
#!/bin/sh
set -e
root="${IPKG_INSTROOT:-}"
# The core runs as root:nogroup, nogroup coming with OpenWrt's base-files: the package
# creates no account.
if [ -z "$root" ] && [ -e /tmp/hyper-recovery/pending ]; then
 echo 'Previous Hyper removal is still restoring direct networking' >&2
 exit 1
fi
# An earlier Hyper build sampled device traffic from cron into its own nft table.
# This package does neither, and the leftover job runs every minute.
if [ -z "$root" ]; then
 if [ -f /etc/crontabs/root ] && grep -q '/etc/hyper/scripts/' /etc/crontabs/root; then
  grep -v '/etc/hyper/scripts/' /etc/crontabs/root > /etc/crontabs/root.hyper || true
  cat /etc/crontabs/root.hyper > /etc/crontabs/root && rm -f /etc/crontabs/root.hyper
  /etc/init.d/cron restart >/dev/null 2>&1 || true
 fi
 rm -rf /etc/hyper/scripts
 nft delete table inet hyper_traffic 2>/dev/null || true
fi
# The package manager keeps an administrator's /etc/config/hyper and leaves the new default
# beside it. hyperd reads defaults for what is unset, and carries 0.x's sections over.
rm -f "$root/etc/config/hyper-opkg" "$root/etc/config/hyper.apk-new"
# 1.0.0 carried the core; opkg removes its files on the upgrade but leaves their directories.
rmdir "$root/usr/share/hyper/core" "$root/usr/share/licenses/luci-app-hyper/core" 2>/dev/null || :
exec sh "$root/usr/libexec/hyper/activate-runtime"
HOOK
cat > "$input/prerm" <<'HOOK'
#!/bin/sh
set -e
root=${IPKG_INSTROOT:-}
manager=$root/usr/libexec/hyper/manager
remove_manager() {
 dir=${manager%/*}
 rm -f "$manager" "$dir/.manager.next" "$dir/.manager.previous" "$dir/.activate.lock"
}
stop_service() {
 if [ "$(cat /proc/1/comm)" = procd ] || ubus -t 2 list service 2>/dev/null | grep -qx service; then
  /etc/init.d/hyper stop
 elif pidof hyperd >/dev/null; then
  echo 'Cannot stop the running Hyper service without procd' >&2
  return 1
 fi
}
case "${PKG_UPGRADE:-0}:${1:-remove}" in 1:*|*:upgrade) [ -n "$root" ] || stop_service; exit 0;; esac
if [ -n "$root" ]; then remove_manager; exit 0; fi
# The core's runtime directories go with the package, so a reinstall starts from a clean state.
clean_runtime() { rm -rf /var/run/hyper/core /var/run/hyper/mihomo; }
if [ ! -e "$manager" ] && [ ! -d /var/run/hyper/network ]; then clean_runtime; remove_manager; exit 0; fi
# Removal (also opkg --force-reinstall) restores direct networking only. Kernels, subscriptions,
# accounts and rule sets stay for the next installation; `hyperd purge` removes them by hand.
if ! /usr/sbin/hyperd uninstall || ! stop_service; then
 if command -v apk >/dev/null; then
  /usr/libexec/hyper/package-recovery arm
  echo 'Hyper removal recovery is active in /tmp/hyper-recovery' >&2
 fi
 exit 1
fi
/etc/init.d/hyper disable
clean_runtime
remove_manager
HOOK
chmod 0755 "$input/preinst" "$input/postinst" "$input/prerm"
# opkg names a package after its architecture; give the APK the same suffix.
arch_suffix=${PACKAGE_ARCH:+_$PACKAGE_ARCH}
emit_feed() (
 VERSION=$1; feed=$2; REVISION=${3:-$REVISION}
 mkdir "$feed"
case "$RELEASE" in
 25.*)
  apk=staging_dir/host/bin/apk
  mkdir -p "$pkg/lib/apk/packages"
  # apk stores protected configuration under /etc; register the package file list for OpenWrt tooling.
  (cd "$pkg" && find . \( -type f -o -type l \) | sed 's,^\.,,') > "$pkg/lib/apk/packages/$name.list"
  "$apk" mkpkg --info "name:$name" --info "version:$VERSION-r$REVISION" --info "arch:${PACKAGE_ARCH:-noarch}" \
   --info 'license:GPL-3.0' --info 'description:Hyper router management service' \
   --info "depends:$deps" --files "$pkg" --script "pre-install:$input/preinst" --script "pre-upgrade:$input/preinst" --script "post-install:$input/postinst" \
   --script "post-upgrade:$input/postinst" --script "pre-deinstall:$input/prerm" \
   --output "$feed/$name-$VERSION-r$REVISION$arch_suffix.apk"
  "$apk" --allow-untrusted adbdump --format json "$feed/$name-$VERSION-r$REVISION$arch_suffix.apk" > "$input/package.json"
  info=$(tr -d ' \n\t' < "$input/package.json")
  for field in "name\":\"$name\"" "version\":\"$VERSION-r$REVISION\"" "arch\":\"${PACKAGE_ARCH:-noarch}\""; do
   case "$info" in *"\"$field"*) ;; *) echo "unexpected APK metadata: $field" >&2; exit 1;; esac
  done
  ;;
 *)
  mkdir -p "$pkg/CONTROL"
  comma_deps=$(echo "$deps" | sed 's/ /, /g')
  # opkg prunes automatically installed dependencies removed in an upgrade,
  # even without --autoremove. It compares every alternative of Depends and
  # Recommends. The first alternative is already mandatory on every install.
  retained='luci-base | ubus | uci | jsonfilter | rpcd | rpcd-mod-ucode | ucode | ucode-mod-ubus | ucode-mod-fs | procd-ujail | firewall4 | nftables-json | kmod-nft-tproxy | kmod-tun | ip-full | flock | dnsmasq | ca-bundle | uclient-fetch'
  if [ "$FEED_MODE" = test ] && [ "$VERSION" = 0.0.0 ]; then
   # Give the upgrade fixture real orphanable dependencies. opkg must install
   # them as dependencies of this fixture, not as explicitly requested packages.
   comma_deps="$comma_deps, ip-full, kmod-tun"
  fi
  cat > "$pkg/CONTROL/control" <<EOF
Package: $name
Version: $VERSION-$REVISION
Architecture: ${PACKAGE_ARCH:-all}
Depends: $comma_deps
Recommends: $retained
Maintainer: Hyper
Description: Hyper router management service
Section: net
Priority: optional
License: GPL-3.0
EOF
  install -m755 "$input/preinst" "$pkg/CONTROL/preinst"
  install -m755 "$input/postinst" "$pkg/CONTROL/postinst"
  install -m755 "$input/prerm" "$pkg/CONTROL/prerm"
  [ ! -e "$pkg/etc/config/hyper" ] || echo /etc/config/hyper > "$pkg/CONTROL/conffiles"
  ./scripts/ipkg-build "$pkg" "$feed"
  ;;
esac
(cd "$feed" && sha256sum * > /tmp/feed-sums && mv /tmp/feed-sums SHA256SUMS)
)
emit_feed "$VERSION" /tmp/feed
if [ -n "${PRERELEASE:-}" ]; then
 emit_feed "$VERSION" /tmp/feed/preview "$PRERELEASE"
fi
if [ "$FEED_MODE" = test ] && [ "$VERSION" != 0.0.0 ]; then
 emit_feed 0.0.0 /tmp/feed/baseline
fi
