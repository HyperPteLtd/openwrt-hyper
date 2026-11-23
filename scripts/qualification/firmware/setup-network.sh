# Shared preparation for tests in the official ext4 guest. Requires report().
. /etc/openwrt_release
report distribution "$DISTRIB_RELEASE"; report kernel "$(uname -r)"; report pid1 "$(cat /proc/1/comm)"
i=0;until ubus call system board >/dev/null 2>&1 && [ -e /etc/config/network ]; do i=$((i+1));[ "$i" -lt 40 ] || { report boot failed;exit 1; };sleep 1;done
fw=fw4
if [ -f /root/enable-fw3 ]; then
 fw=fw3
 /etc/init.d/firewall stop
 if command -v apk >/dev/null; then
  # The stock image has fw4-specific LuCI metapackages. Remove their reverse
  # dependencies with fw4 only in this disposable compatibility guest.
  apk --no-network del --rdepends firewall4 || { report prerequisites failed;exit 1; }
 else opkg remove --force-depends firewall4 || { report prerequisites failed;exit 1; }; fi
fi
if [ -d /root/packages ]; then
 if command -v apk >/dev/null; then
  mkdir -p /tmp/firmware-repos
  : >/tmp/firmware-repos/list
  for index in /root/packages/index-*; do
   repo=/tmp/firmware-repos/${index##*/};mkdir -p "$repo";cp "$index" "$repo/packages.adb"
   for package in /root/packages/*.apk; do ln -s "$package" "$repo/${package##*/}"; done
   echo "$repo/packages.adb" >>/tmp/firmware-repos/list
  done
  if [ "$fw" = fw3 ]; then
   set -- firewall iptables-zz-legacy ip6tables-zz-legacy iptables-mod-extra iptables-mod-tproxy ip6tables-mod-nat
  else set -- kmod-nft-tproxy; fi
  apk --repositories-file /tmp/firmware-repos/list --no-network add ip-full kmod-tun "$@" || { report prerequisites failed;exit 1; }
 else
  set --
  for package in /root/packages/*.ipk; do
   # Reuse old qualification caches made before the BusyBox flock check.
   case ${package##*/} in flock_*) command -v flock >/dev/null && continue ;; esac
   set -- "$@" "$package"
  done
  opkg install "$@" || { report prerequisites failed;exit 1; }
 fi
fi
uci -q batch <<'UCI'
set network.lan=interface
set network.lan.proto=static
set network.lan.device=eth0
set network.lan.ipaddr=192.168.41.1
set network.lan.netmask=255.255.255.0
set network.lan.ip6addr=fd72:45::1/64
set network.wan=interface
set network.wan.device=eth1
set network.wan.proto=dhcp
commit network
set dhcp.lan.force=1
commit dhcp
UCI
/etc/init.d/network restart
for i in $(seq 1 45); do ubus call network.interface.lan status | grep -q '"up": true' && break; sleep 1;done
/etc/init.d/firewall restart || { report "$fw" failed;exit 1; }
/etc/init.d/dnsmasq restart || { report dnsmasq failed;exit 1; }
if [ -d /root/network-candidate ]; then
 # The armsr guest and the local Cortex-A53 package share the ARM64 runtime.
 # Extend the architecture list only inside this disposable guest.
 (
  set -e
  cd /root/network-candidate
  sha256sum -c SHA256SUMS
  if command -v apk >/dev/null; then
   printf 'aarch64_cortex-a53\n' >>/etc/apk/arch
   apk --no-network --allow-untrusted add ./luci-app-hyper-*.apk
  else
   opkg print-architecture >/tmp/hyper-qualify-arches
   cat /tmp/hyper-qualify-arches >>/etc/opkg.conf
   printf 'arch aarch64_cortex-a53 9\n' >>/etc/opkg.conf
   opkg install ./luci-app-hyper_*.ipk
  fi
  test -x /usr/sbin/hyper-net
  /bin/sh /root/clock-fixture/test
  report clock_retry pass
  # The standard network fixture supplies its own DNS canary. The daemon and
  # its watchdog must relinquish ownership before the fixture drives the helper.
  /etc/init.d/hyper stop
  rm -f /var/run/hyper/stopping
  report installed_package_sha256 "$(cut -d' ' -f1 SHA256SUMS)"
 )
 [ "$?" -eq 0 ] || exit 1
fi
test "$(/usr/libexec/hyper/network environment | cut -d' ' -f1)" = "$fw" && report "$fw" running || { report "$fw" failed;exit 1; }
for i in $(seq 1 10);do nslookup -retry=1 -timeout=1 -type=A localhost 192.168.41.1 >/tmp/baseline-dns 2>&1 && break;sleep 1;done
if nslookup -retry=1 -timeout=1 -type=A localhost 192.168.41.1 >/dev/null;then report dnsmasq running;else logread|tail -65;ubus call service list '{"name":"dnsmasq","verbose":true}';ls -l /var/etc/dnsmasq*;netstat -ln;uci show dhcp;report dnsmasq unreachable;exit 1;fi
report management "$(ip route get 192.168.41.1 | head -1)"
command -v flock >/dev/null && ip -N -4 rule show >/dev/null || { report prerequisites missing;exit 1; }
if [ "$fw" = fw4 ]; then modprobe nft_tproxy || true; else modprobe xt_TPROXY || true; fi
modprobe tun || true
