#!/bin/sh
# Serial harness runs inside an otherwise stock official ext4-root environment.
set -u
trap 'echo HYPER_QUALIFICATION_END' EXIT
report() { printf 'HYPER_RESULT %s=%s\n' "$1" "$2"; }
. /root/setup-network
# Two resolvers give different answers for the same name. This distinguishes
# restored upstream service from a still-running LAN listener or cached answer.
ip addr add 198.18.0.53/32 dev eth1 || exit 1
printf 'nameserver 198.18.0.53\n' >/tmp/qualification-resolv
uci add_list 'dhcp.@dnsmasq[0].interface=lan'
uci set 'dhcp.@dnsmasq[0].resolvfile=/tmp/qualification-resolv'
uci set 'dhcp.@dnsmasq[0].nonwildcard=1'
uci add_list 'dhcp.@dnsmasq[0].address=/printer.lan/192.168.41.200'
# A LuCI DNS forwarding. The takeover sets it aside in uci state only: dnsmasq's generated
# config drops it while /etc/config keeps it.
uci add_list 'dhcp.@dnsmasq[0].server=198.18.0.53'
uci commit dhcp
/etc/init.d/dnsmasq restart || exit 1
dnsmasq --interface=eth1 --except-interface=lo --bind-interfaces --conf-file=/dev/null --no-resolv --address=/wire-check.example.org/93.184.216.34 --pid-file=/tmp/wan-dns.pid || exit 1
dnsmasq --port=19764 --listen-address=127.0.0.1 --bind-interfaces --conf-file=/dev/null --no-resolv --address=/probe.hyper.invalid/192.0.2.1 --address=/wire-check.example.org/93.184.216.35 --pid-file=/tmp/core-dns.pid || exit 1
dns_answer() {
 nslookup -retry=1 -timeout=1 -type=A "$1" "$2" 2>/dev/null | awk -v expected="$3" '/^Address([[:space:]][0-9]+)?:/ && $NF == expected {found=1} END {exit !found}'
}
check_dns() {
 for gateway in 192.168.41.1 fd72:45::1; do
  dns_answer printer.lan "$gateway" 192.168.41.200 && dns_answer wire-check.example.org "$gateway" "$1" || return 1
 done
}
for attempt in 1 2 3 4 5; do check_dns 93.184.216.34 && break; sleep 1; done
check_dns 93.184.216.34 && report dns_baseline pass || { report dns_baseline failed; exit 1; }
for i in $(seq 1 6); do nslookup -timeout=1 -retry=1 localhost 192.168.41.1 >/tmp/baseline-dns 2>&1 && break;sleep 1;done
cat /tmp/baseline-dns; ps w | grep '[d]nsmasq'; netstat -ln | grep ':53 ';logread -e dnsmasq | tail -20
for c in /var/etc/dnsmasq.conf.*;do echo "Guest dnsmasq settings";grep -E 'interface=|port=|conf-dir|no-resolv|server=|bind-' "$c";done
net=/usr/libexec/hyper/network
# The manager passes the core's group to every apply (applyArgs in network.go): base-files' nogroup.
core_gid=$(awk -F: '$1 == "nogroup" { print $3 }' /etc/group)
check_clean() {
 if [ "$fw" = fw4 ]; then
  ! nft list table inet hyper >/dev/null 2>&1 && nft list table inet unrelated >/dev/null
 else
  for xt in iptables-legacy ip6tables-legacy; do
   # The TProxy listener guard and the allowances are owned INPUT rules.
   "$xt" -S unrelated >/dev/null && ! "$xt" -t mangle -S HYPER_LAN >/dev/null 2>&1 &&
    ! "$xt" -S INPUT | grep -q hyper-owned-v1 || return 1
  done
 fi
}
if [ "$fw" = fw4 ]; then nft add table inet unrelated
else for xt in iptables-legacy ip6tables-legacy; do "$xt" -N unrelated; "$xt" -A unrelated -j RETURN; done; fi
# Direct devices from the manager's handoff (directDevices in network.go) make every apply
# build the bypass sets and rules, so the firmware's own nft and xtables check them.
mkdir -p /var/run/hyper
printf '4 192.168.41.250/32\n6 fd72:45::250/128\nmac 02:00:00:00:41:fa\n' >/var/run/hyper/direct-devices
for method in tproxy tun; do
 [ "$method" != tun ] || { ip tuntap add dev owtun0 mode tun && ip link set owtun0 up || { report tun unsupported;continue; }; }
 uci set hyper.main.method="$method";uci set hyper.main.ipv6=1;uci set hyper.main.dns=1;uci commit hyper
 if [ "$method" = tun ]; then uci set hyper.main.dns_intercept=1; else uci set hyper.main.dns_intercept=0; fi
 uci commit hyper
 if /bin/sh -x "$net" apply "gid=$core_gid" > /tmp/qual-apply 2>&1; then
  report "${method}_apply" pass
  check_dns 93.184.216.35 && [ "$(uci -q get 'dhcp.@dnsmasq[0].server')" = 198.18.0.53 ] && ! grep -qx 'server=198.18.0.53' /var/etc/dnsmasq.conf.* && report "${method}_dns_takeover" pass || report "${method}_dns_takeover" failed
  "$net" verify && report "${method}_verify" pass || report "${method}_verify" failed
  "$fw" reload && "$net" verify && check_dns 93.184.216.35 && report "${method}_${fw}_reload" pass || report "${method}_${fw}_reload" failed
 else
  report "${method}_apply" failed;tail -180 /tmp/qual-apply;logread -e dnsmasq | tail -28;netstat -ln | grep -E "(:53 |:19764 )";nslookup -retry=1 -timeout=1 localhost 127.0.0.1 || :
 fi
 if /bin/sh -x "$net" release >/tmp/qual-release 2>&1 && check_clean && ! ip -4 rule show | grep -q '^21998:' && ! ip -6 rule show | grep -q '^21998:' && nslookup localhost 192.168.41.1 >/dev/null; then
  report "${method}_release" pass
  check_dns 93.184.216.34 && grep -qx 'server=198.18.0.53' /var/etc/dnsmasq.conf.* && report "${method}_dns_restored" pass || report "${method}_dns_restored" failed
 else report "${method}_release" failed;tail -65 /tmp/qual-release;nslookup localhost 192.168.41.1;fi
 [ "$method" != tun ] || ip link del owtun0 || true
done
