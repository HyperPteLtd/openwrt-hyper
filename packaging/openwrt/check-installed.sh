#!/bin/sh
set -eu
name=luci-app-hyper
# install-test.sh copies the shared retry helper next to this script.
. /tmp/hyper-retry
# The installed Hyper version, as the package manager records it.
installed_version() {
 if command -v apk >/dev/null; then
  apk query --installed --format json --fields version "$name" | jsonfilter -e '@[0].version'
 else
  opkg status "$name" | awk '$1=="Version:" {print $2}'
 fi
}
# install-test.sh stages the latest release's package for this router in /tmp/hyper-release,
# with its line of the release's SHA256SUMS, when the release has one. Keep it only when it is
# older than the candidate, and name its dependencies to install from the online feeds.
release_deps=
stage_release() {
 [ -d /tmp/hyper-release ] || return 0
 (cd /tmp/hyper-release && sha256sum -c SHA256SUMS)
 candidate=$(cat /tmp/test-candidate-version)
 if command -v apk >/dev/null; then
  apk --allow-untrusted adbdump --format json /tmp/hyper-release/*.apk > /tmp/release-info.json
  published=$(jsonfilter -i /tmp/release-info.json -e '@.info.version')
  relation=$(apk version -t "$published" "$candidate")
  deps=$(jsonfilter -i /tmp/release-info.json -e '@.info.depends[*]')
 else
  tar -xzOf /tmp/hyper-release/*.ipk ./control.tar.gz | tar -xzO ./control > /tmp/release-control
  published=$(sed -n 's/^Version: //p' /tmp/release-control)
  # opkg reads a single < as <=.
  relation='>='
  if opkg compare-versions "$published" '<<' "$candidate"; then relation='<'; fi
  deps=$(sed -n 's/^Depends: //p' /tmp/release-control | tr ',' ' ')
 fi
 if [ "$relation" != '<' ]; then
  echo "Skipping the upgrade from the latest release: $published is not older than this build, $candidate" >&2
  rm -r /tmp/hyper-release
  return 0
 fi
 echo "$published" > /tmp/test-release-version
 release_deps=$deps
}
# What an upgraded router holds, whichever version it came from.
check_upgraded() {
 version=$(installed_version)
 [ "$version" = "$(cat /tmp/test-candidate-version)" ] || { echo "The upgrade left $version installed" >&2; exit 1; }
 test "$(cat /etc/hyper/kernel/package-upgrade-test)" = upgrade-state-preserved
 test -s /usr/share/licenses/luci-app-hyper/LICENSE
 for cli in hyperd hyper-net hyper-kernel hyper-subscription; do test -x "/usr/sbin/$cli"; done
 test -x /etc/init.d/hyper && test -x /usr/libexec/hyper/network
 # The core runs as root:nogroup; base-files brings the group, the package no account.
 test "$(awk -F: '$1 == "nogroup" { print $3 }' /etc/group)" = 65534
 test ! -e /usr/sbin/mihomo
 check_core
 # 0.x's programs and core went with its package.
 test ! -e /usr/libexec/hyper/bin/hyperd && test ! -e /usr/libexec/hyper/bin/mihomo
}
# The package's core (packaging/openwrt/fetch-core.sh) and its source notice.
check_core() {
 pin=/usr/share/hyper/core/pin.json
 name=$(jsonfilter -i "$pin" -e '@.name')
 case "$name" in mihomo-linux-*-"$(jsonfilter -i "$pin" -e '@.tag')".gz) ;; *) echo "Unexpected bundled core $name" >&2; exit 1;; esac
 printf '%s  %s\n' "$(jsonfilter -i "$pin" -e '@.sha256')" "/usr/share/hyper/core/$name" | sha256sum -c -
 test -s /usr/share/licenses/luci-app-hyper/core/SOURCE
}
case "$1" in reject-*|upgrade) PATH=/tmp/hyper-test-bin:$PATH; export PATH;; esac
case "$1" in
 prepare)
  . /etc/openwrt_release
  test "$DISTRIB_RELEASE" = "$2"
  test "$DISTRIB_ARCH" = "$3"
  mkdir -p /var/lock /var/run
  # Test doubles are outside the package and used only by the updater cases.
  mkdir -p /tmp/hyper-test-bin
  real_fetch=$(command -v uclient-fetch)
  cat > /tmp/hyper-test-bin/uclient-fetch <<'FETCH'
#!/bin/sh
set -eu
[ "$#" = 8 ] && [ "$1" = -T ] && [ "$3" = -U ] && [ "$4" = Hyper-OpenWrt ] && [ "$6" = -O ] || exit 1
case "$8" in
 https://api.github.com/repos/HyperPteLtd/openwrt-hyper/releases/latest) path=releases/latest ;;
 https://github.com/HyperPteLtd/openwrt-hyper/releases/download/v*/*) path=${8#https://github.com/HyperPteLtd/openwrt-hyper/} ;;
 *) echo 'Unexpected release request' >&2; exit 1 ;;
esac
FETCH
  printf 'exec "%s" "$1" "$2" "$3" "$4" "$5" "$6" "$7" "http://feed:8080/$path"\n' "$real_fetch" >> /tmp/hyper-test-bin/uclient-fetch
  chmod 755 /tmp/hyper-test-bin/uclient-fetch
  (cd /tmp/verified-feed && sha256sum -c SHA256SUMS)
  rm /tmp/verified-feed/SHA256SUMS
  if command -v apk >/dev/null; then
   apk --allow-untrusted adbdump --format json /tmp/verified-feed/*.apk > /tmp/package-info.json
   test "$(jsonfilter -i /tmp/package-info.json -e '@.info.arch')" = "${HYPER_PACKAGE_ARCH:-noarch}"
   jsonfilter -i /tmp/package-info.json -e '@.info.version' > /tmp/test-candidate-version
   depends=$(jsonfilter -i /tmp/package-info.json -e '@.info.depends[*]')
   if apk verify /tmp/verified-feed/*.apk >/dev/null 2>&1; then echo 'Expected an unsigned release APK' >&2; exit 1; fi
   test -n "$depends"
   # Stage dependencies declared by the old offline-upgrade fixture as well.
   baseline_deps=$(apk --allow-untrusted adbdump --format json /tmp/hyper-baseline/*.apk | jsonfilter -e '@.info.depends[*]')
   # And those of the latest published release.
   stage_release
   # Fetches from the firmware's own feeds; Hyper's packages are never retried.
   retry apk update
   retry apk add $depends $baseline_deps $release_deps firewall4 nftables-json kmod-nft-tproxy uhttpd uhttpd-mod-ubus curl
  else
   tar -xzOf /tmp/verified-feed/*.ipk ./control.tar.gz | tar -xzO ./control > /tmp/package-control
   test "$(sed -n 's/^Architecture: //p' /tmp/package-control)" = "${HYPER_PACKAGE_ARCH:-all}"
   sed -n 's/^Version: //p' /tmp/package-control > /tmp/test-candidate-version
   depends=$(sed -n 's/^Depends: //p' /tmp/package-control | tr ',' ' ')
   test -n "$depends"
   stage_release
   # Fetches from the firmware's own feeds; Hyper's packages are never retried.
   retry opkg update
   retry opkg install $depends firewall4 nftables-json kmod-nft-tproxy uhttpd uhttpd-mod-ubus curl
   # Verify against an online feed so a mistaken recommendation would pull the
   # package. Compare names and versions, as a dependency could replace a fork.
   opkg list-installed | sort >/tmp/stock-packages
   opkg install /tmp/verified-feed/*.ipk
   opkg list-installed | awk -v pkg="$name" '$1!=pkg' | sort >/tmp/other-packages
   cmp /tmp/stock-packages /tmp/other-packages
   opkg remove "$name"
   # The old package must own its optional dependencies for this test. An
   # explicit opkg install of all old dependencies hides the orphan removal.
   opkg install /tmp/hyper-baseline/*.ipk
   opkg list-installed | awk -v pkg="$name" '$1!=pkg' | sort >/tmp/baseline-packages
   opkg install /tmp/verified-feed/*.ipk
   opkg list-installed | awk -v pkg="$name" '$1!=pkg' | sort >/tmp/other-packages
   cmp /tmp/baseline-packages /tmp/other-packages
   opkg remove "$name"
   echo 'Native opkg installed only Hyper and retained the previous dependency set across upgrade'
   # Only now the published release's dependencies, which must not hide the orphan check above.
   [ -z "$release_deps" ] || retry opkg install $release_deps
  fi
  # Dependencies have been installed from the firmware's trusted repositories.
  for upstream in /etc/opkg/distfeeds.conf /etc/apk/repositories.d/distfeeds.list; do
   [ ! -f "$upstream" ] || mv "$upstream" "$upstream.disabled"
  done
  ;;
 offline-install)
  test -z "$(ip -4 route show default)" && test -z "$(ip -6 route show default)"
  test ! -e /usr/sbin/hyperd && test ! -e /usr/libexec/hyper/manager
  set -- /tmp/verified-feed/*
  test "$#" = 1 && test -f "$1"
  case "${1##*/}" in
   25.12+_luci-app-hyper-*.apk) apk --no-network --repositories-file /dev/null add --allow-untrusted "$1" ;;
   23.05-24.10_luci-app-hyper_*.ipk) opkg install "$1" ;;
   *) echo 'Expected a single installation file' >&2; exit 1 ;;
  esac
  rm "$1"
  test -x /usr/libexec/hyper/manager && test ! -e /tmp/hyper-package-payload
  check_core
  (cd /usr/share/hyper/rules && sha256sum -c SHA256SUMS)
  awk '$3 == "geoip-cn.mrs" { ip++ } $3 == "geosite-cn.mrs" { site++ }
       END { exit !(ip == 1 && site == 1) }' /usr/share/hyper/rules/CATALOG
  test -s /usr/libexec/hyper/fault-policy && test -x /usr/libexec/hyper/procd-delete
  echo "Offline runtime size: $(wc -c < /usr/libexec/hyper/manager) bytes; bundled CPU archives removed"
  for cli in hyperd hyper-kernel hyper-net hyper-subscription; do
   test "$(readlink -f "/usr/sbin/$cli")" = /usr/libexec/hyper/manager
  done
  test "$(hyper-kernel status | jsonfilter -e '@.schema_version')" = 1
  test "$(hyper-subscription list </dev/null | jsonfilter -e '@.ok')" = true
  # A minimal rootfs has no procd service object. The checker reports an
  # explicit blocked result when it cannot inspect a required service.
  if hyper-net conflicts > /tmp/offline-conflicts; then
   test "$(jsonfilter -i /tmp/offline-conflicts -e '@.blocked')" = false
  else
   test "$(jsonfilter -i /tmp/offline-conflicts -e '@.blocked')" = true
   test -n "$(jsonfilter -i /tmp/offline-conflicts -e '@.resources[0].kind')"
  fi
  # check-web starts hyperd and calls its authenticated RPC over loopback.
  ;;
 install-baseline)
  (cd /tmp/hyper-baseline && sha256sum -c SHA256SUMS)
  if command -v apk >/dev/null; then
   apk --no-network --repositories-file /dev/null add --allow-untrusted /tmp/hyper-baseline/*.apk
  else
   opkg install /tmp/hyper-baseline/*.ipk
  fi
  rm -r /tmp/hyper-baseline
  cat /etc/openwrt_release
  mkdir -p /etc/hyper/kernel
  echo 'upgrade-state-preserved' > /etc/hyper/kernel/package-upgrade-test
  ;;
 reject-tampered-upgrade)
  updater=/usr/libexec/hyper/package-update
  # Only the fixture's GitHub copy: no download mirrors outside the test network.
  uci set hyper.main.download_mirrors_saved=1
  uci -q delete hyper.main.download_mirror || true
  uci commit hyper
  test "$("$updater" check | jsonfilter -e '@.available')" = true
  if "$updater" apply; then echo 'Updater accepted modified package bytes' >&2; exit 1; fi
  installed=$("$updater" check | jsonfilter -e '@.installed')
  case "$installed" in 0.0.0-*) ;; *) echo 'Corrupt release changed the installed package' >&2; exit 1;; esac
  ;;
 reject-invalid-release)
  updater=/usr/libexec/hyper/package-update
  if "$updater" check || "$updater" apply; then echo 'Updater accepted a invalid official release metadata' >&2; exit 1; fi
  ;;
 reject-wrong-version)
  if detail=$(/usr/libexec/hyper/package-update apply 2>&1); then echo 'Updater accepted an incorrect embedded package version' >&2; exit 1; fi
  case "$detail" in *'identity does not match release'*) ;; *) echo "$detail" >&2; exit 1;; esac
  ;;
 upgrade)
  test "$(/usr/libexec/hyper/package-update check | jsonfilter -e '@.available')" = true
  /usr/libexec/hyper/package-update apply
  check_upgraded
  ;;
 install-release)
  # The latest published release, as a router has it before the upgrade.
  (cd /tmp/hyper-release && sha256sum -c SHA256SUMS)
  rm /tmp/hyper-release/SHA256SUMS
  set -- /tmp/hyper-release/*
  test "$#" = 1 && test -f "$1"
  test ! -e /usr/sbin/hyperd && test ! -e /usr/libexec/hyper/manager
  rm -f /etc/hyper/kernel/package-upgrade-test
  if command -v apk >/dev/null; then
   apk --no-network --repositories-file /dev/null add --allow-untrusted "$1"
  else
   opkg install "$1"
  fi
  rm -r /tmp/hyper-release
  test "$(installed_version)" = "$(cat /tmp/test-release-version)"
  mkdir -p /etc/hyper/kernel
  echo 'upgrade-state-preserved' > /etc/hyper/kernel/package-upgrade-test
  # Up to 1.3.0 the core ran in a jail as hyper-core, which the package created, and a
  # started core left its runtime directories to that account. Leave them as it did.
  if id hyper-core >/dev/null 2>&1; then
   mkdir -p /var/run/hyper
   rm -rf /var/run/hyper/core /var/run/hyper/mihomo
   mkdir -m 700 /var/run/hyper/core
   chown hyper-core:hyper-core /var/run/hyper/core
   mkdir -m 750 /var/run/hyper/mihomo /var/run/hyper/mihomo/devices
   chgrp hyper-core /var/run/hyper/mihomo /var/run/hyper/mihomo/devices
   (umask 077 && : > /var/run/hyper/mihomo/cache.db)
   chown hyper-core:hyper-core /var/run/hyper/mihomo/cache.db
   echo 'Left the core runtime directories to hyper-core, as a jail-era release does'
  fi
  ;;
 upgrade-from-release)
  # Users upgrade a published release with the package manager, as the release notes say.
  (cd /tmp/hyper-candidate && sha256sum -c SHA256SUMS)
  rm /tmp/hyper-candidate/SHA256SUMS
  set -- /tmp/hyper-candidate/*
  test "$#" = 1 && test -f "$1"
  if command -v apk >/dev/null; then
   apk --no-network --repositories-file /dev/null add --allow-untrusted "$1"
  else
   opkg install "$1"
  fi
  rm -r /tmp/hyper-candidate
  check_upgraded
  # Files only the release shipped go with it, such as the jail helper and apk's account request.
  test ! -e /usr/libexec/hyper/tun-jail && test ! -e /lib/apk/packages/luci-app-hyper.rusers
  ;;
 reject-downgrade)
  candidate=$(/usr/libexec/hyper/package-update check | jsonfilter -e '@.candidate')
  case "$candidate" in 0.0.0-*) ;; *) echo 'Expected the earlier release' >&2; exit 1;; esac
  if detail=$(/usr/libexec/hyper/package-update apply 2>&1); then echo 'Updater accepted a downgrade' >&2; exit 1; fi
  case "$detail" in *'Refusing package downgrade'*) ;; *) echo "$detail" >&2; exit 1;; esac
  ;;
 prepare-recovery)
  # Create observable owned resources only in the disposable test namespace.
  mkdir -p /var/run/hyper/network /tmp/test-dns
  echo /tmp/test-dns > /var/run/hyper/network/dns
  printf '%s\n' '# Hyper managed upstream' 'server=127.0.0.1#19764' > /tmp/test-dns/99-hyper-upstream.conf
  echo 'preserve' >/tmp/test-dns/foreign.conf
  nft 'add table inet hyper { comment "hyper-owned-v1"; }'
  nft add table inet foreign_test
  ;;
 uninstall-with-recovery)
  for _ in 1 2 3 4 5; do [ ! -e /tmp/block-release ] || break; sleep 1; done
  test -f /tmp/block-release
  apk del "$name"
  test ! -e /usr/sbin/hyperd && test ! -e /usr/libexec/hyper/network
  test -f /tmp/hyper-recovery/pending
  rm -f /tmp/block-release
  for _ in $(seq 1 65); do [ -e /tmp/hyper-recovery/pending ] || break; sleep 1; done
  test ! -e /tmp/hyper-recovery/pending
  test ! -e /tmp/test-dns/99-hyper-upstream.conf && test -f /tmp/test-dns/foreign.conf
  # Removal keeps the user's kernels and subscriptions.
  test "$(cat /etc/hyper/kernel/package-upgrade-test)" = upgrade-state-preserved
  nft list table inet foreign_test >/dev/null
  if nft list table inet hyper >/dev/null 2>&1; then echo 'Owned firewall table survived recovery' >&2; exit 1; fi
  echo 'Verified detached recovery removed the owned DNS upstream and nft table after APK removed the package'
  ;;
 uninstall)
  if command -v apk >/dev/null; then apk del "$name"; else opkg remove "$name"; fi
  test ! -e /usr/sbin/hyperd && test ! -e /usr/libexec/hyper/network
  test ! -e /usr/libexec/hyper/manager
  # The core's runtime directories go with the package, whichever account an older core left them to.
  test ! -e /var/run/hyper/core && test ! -e /var/run/hyper/mihomo
  # Removal keeps the user's kernels and subscriptions; only a manual purge deletes them.
  [ "${2:-}" != preserved ] || test "$(cat /etc/hyper/kernel/package-upgrade-test)" = upgrade-state-preserved
  ;;
 *) exit 2 ;;
esac
