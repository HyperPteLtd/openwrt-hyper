#!/bin/sh
set -eu
mkdir -p /tmp/bin /var/run/ubus /var/etc /var/run/hyper /var/run/netns
/sbin/ubusd &
sleep 1
for pair in lan:50 guest:51 wan:18 unmanaged:52; do
 ns=${pair%:*}; num=${pair#*:}
 ip netns add "$ns"
 ip link add "$ns-r" type veth peer name "$ns-c" netns "$ns"
 ip link set "$ns-r" up
 ip netns exec "$ns" ip link set lo up
 ip netns exec "$ns" ip link set "$ns-c" up
 if [ "$ns" = wan ]; then prefix=198.18.0; v6=2001:db8:1; else prefix=192.168.$num; v6=fd00:$num; fi
 ip addr add "$prefix.1/24" dev "$ns-r"
 ip -6 addr add "$v6::1/64" dev "$ns-r" nodad
 ip netns exec "$ns" ip addr add "$prefix.10/24" dev "$ns-c"
 ip netns exec "$ns" ip -6 addr add "$v6::10/64" dev "$ns-c" nodad
 ip netns exec "$ns" ip route add default via "$prefix.1"
 ip netns exec "$ns" ip -6 route add default via "$v6::1"
done
ip route add default via 198.18.0.10 dev wan-r
ip -6 route add default via 2001:db8:1::10 dev wan-r
ip netns exec wan /tmp/peer server &
# Use different answers on the original WAN resolver and the real Mihomo.
# All targets are owned by this test, including hard-coded public/private DNS.
ip netns exec wan ip address add 10.66.0.53/32 dev wan-c
ip netns exec wan ip -6 address add fd00:66::53/128 dev wan-c nodad
ip netns exec wan dnsmasq --interface=wan-c --except-interface=lo --bind-interfaces \
 --conf-file=/dev/null --pid-file=/var/run/test-wan-dns.pid --no-resolv --no-hosts \
 --address=/wire.hyper.org/1.0.0.1
echo 'nameserver 198.18.0.10' >/tmp/original-resolvers
# netifd is represented only by its published interface status; all dataplane
# commands, DNS daemons and packets use the packaged Linux programs.
cat >/tmp/bin/ubus <<'SH'
#!/bin/sh
case "$2" in network.interface.lan) echo '{"l3_device":"lan-r"}';;
 network.interface.guest) echo '{"l3_device":"guest-r"}';;
 *) exec /bin/ubus "$@";; esac
SH
chmod 755 /tmp/bin/ubus
export PATH=/tmp/bin:/usr/sbin:/usr/bin:/sbin:/bin
for pair in lan:50 guest:51 unmanaged:52; do
 ns=${pair%:*}; n=${pair#*:}
 dir=/tmp/instance-$ns
 mkdir -p "$dir"
 cat >/var/etc/dnsmasq.conf.$ns <<CFG
interface=$ns-r
except-interface=lo
bind-interfaces
no-hosts
resolv-file=/tmp/original-resolvers
cache-size=0
address=/localhost/127.0.0.1
domain-needed
server=/invalid/
stop-dns-rebind
conf-dir=$dir
pid-file=/var/run/dns-$ns.pid
CFG
done
cat >/etc/init.d/dnsmasq <<'SH'
#!/bin/sh
set -eu
for name in lan guest unmanaged; do
 file=/var/run/dns-$name.pid
 if [ -e "$file" ]; then kill "$(cat "$file")" || :; fi
done
sleep 1
for name in lan guest unmanaged; do dnsmasq -C "/var/etc/dnsmasq.conf.$name"; done
SH
chmod 755 /etc/init.d/dnsmasq
/etc/init.d/dnsmasq restart
uci -q delete hyper.main.network || :
uci add_list hyper.main.network=lan
uci add_list hyper.main.network=guest
uci set hyper.main.ipv6=1
uci set hyper.main.dns=1
uci set hyper.main.dns_intercept=1
uci commit hyper
mkdir -p /etc/iproute2
echo '21998 qualified_table' >>/etc/iproute2/rt_tables
cat >/tmp/fw4 <<'NFT'
table inet fw4 {
 chain input {
  type filter hook input priority filter; policy accept;
  include "/usr/share/nftables.d/chain-pre/input/90-hyper.nft"
  iifname "guest-r" meta mark & 0xffff == 0x4f57 drop
 }
 chain forward {
  type filter hook forward priority filter; policy accept;
  include "/usr/share/nftables.d/chain-pre/forward/90-hyper.nft"
  oifname "owtun0" drop
 }
}
NFT
helper=/usr/libexec/hyper/network
# As the manager does (internal/coregroup): the core runs as root in base-files' nogroup, and
# every apply is told that group (applyArgs in network.go).
core_gid=$(awk -F: '$1 == "nogroup" { print $3 }' /etc/group)
if [ "${TEST_FIREWALL:-fw4}" = fw3 ]; then
 uci set 'firewall.@defaults[0].input=ACCEPT'
 uci set 'firewall.@defaults[0].forward=ACCEPT'
 uci commit firewall
 fw3 start >/tmp/fw3.log 2>&1
 test "$("$helper" environment | cut -d' ' -f1)" = fw3
 nft -f /tmp/fw4
 test "$("$helper" environment)" = ambiguous
 nft delete table inet fw4
 for xt in iptables-legacy ip6tables-legacy; do
  "$xt" -I INPUT -i guest-r -m mark --mark 0x4f57/0xffff -j DROP
  "$xt" -I FORWARD -o owtun0 -j DROP
  "$xt" -N unrelated
  "$xt" -t nat -A PREROUTING -i unmanaged-r -p udp --dport 65053 -m comment --comment fixture-foreign -j RETURN
 done
else
 nft -f /tmp/fw4
 test "$("$helper" environment | cut -d' ' -f1)" = fw4
fi
"$helper" capabilities >/tmp/capabilities
for capability in 'dns_intercept 1' 'tproxy 1' 'tun 1'; do
 grep -Fqx "$capability" /tmp/capabilities || { cat /tmp/capabilities; exit 1; }
done
# Firmware may supply capabilities without a matching opkg/apk package. Probe
# selected mode before any takeover and verify that missing options stay local.
uci set hyper.main.method=tproxy
"$helper" prerequisites >/tmp/requirements
mount -t tmpfs -o size=16k tmpfs /dev/net
"$helper" prerequisites >/tmp/requirements
"$helper" capabilities >/tmp/requirements
grep -Fqx 'tproxy 1' /tmp/requirements && grep -Fqx 'tun 0' /tmp/requirements
! grep -q '^missing ' /tmp/requirements
uci set hyper.main.method=tun
if "$helper" prerequisites >/tmp/requirements; then exit 1; fi
grep -Fqx 'missing tun_device' /tmp/requirements
umount /dev/net
uci set hyper.main.method=tproxy
mv /etc/init.d/dnsmasq /etc/init.d/dnsmasq.saved
if "$helper" prerequisites >/tmp/requirements; then exit 1; fi
grep -Fqx 'missing dnsmasq' /tmp/requirements
uci set hyper.main.dns=0
"$helper" prerequisites >/tmp/requirements
mv /etc/init.d/dnsmasq.saved /etc/init.d/dnsmasq
uci set hyper.main.dns=1
ln -s /bin/busybox /tmp/bin/ip
if "$helper" prerequisites >/tmp/requirements; then exit 1; fi
grep -Fqx 'missing iproute' /tmp/requirements
rm /tmp/bin/ip
"$helper" prerequisites >/tmp/requirements
# Inspect the effective per-instance configuration, including nested files,
# without copying DNS values into diagnostics or touching foreign directives.
mkdir -p /tmp/instance-lan/nested
printf '%s\n' 'conf-dir=/tmp/instance-lan/nested,*.conf' >/tmp/instance-lan/custom.conf
printf '%s\n' 'server=/corp.example/10.0.0.53' >/tmp/instance-lan/nested/corp.conf
printf '%s\n' 'server=10.0.0.53' >/tmp/instance-lan/nested/ignored.disabled
"$helper" prerequisites >/tmp/requirements
for item in 'server=/#/10.0.0.53:dns_default_upstream' 'servers-file=/tmp/servers:dns_servers_file'; do
 printf '%s\n' "${item%:*}" >/tmp/instance-lan/nested/test.conf
 if "$helper" prerequisites >/tmp/requirements; then echo 'accepted conflicting nested upstream'; exit 1; fi
 grep -Fqx "missing ${item#*:} lan" /tmp/requirements
 if grep -Eq '10\.0\.0\.53|/tmp/servers' /tmp/requirements; then exit 1; fi
done
rm -f /tmp/instance-lan/nested/test.conf
echo 'Selected-mode prerequisites passed for TProxy without TUN, DNS off without dnsmasq, and detection of incomplete BusyBox ip'
# The helper looks up no group of its own: without the core's, or given root's, it takes nothing over.
for gid in '' 0 065534 nogroup; do
 if "$helper" apply "gid=$gid"; then echo "apply accepted core group '$gid'"; exit 1; fi
done
if "$helper" apply ipv4; then echo 'apply took over without the core group'; exit 1; fi
[ ! -e /var/run/hyper/network/owned ]
reload_firewall() {
 if [ "${TEST_FIREWALL:-fw4}" = fw3 ]; then fw3 reload >/tmp/fw3.log 2>&1
 else
  { flock -x 8; { echo 'delete table inet fw4'; cat /tmp/fw4; } | nft -f -; } 8>/var/run/fw4.lock
 fi
}
wire() {
 for ns in ${2:-lan guest}; do
  case "$ns" in lan) n=50;; guest) n=51;; unmanaged) n=52;; esac
  for family in 4 6; do
   case "$family:$1" in
    4:direct) expect=192.168.$n.10;; 6:direct) expect=fd00:$n::10;;
    4:proxy) expect=198.18.0.1;; 6:proxy) expect=2001:db8:1::1;;
   esac
   if [ "$family" = 4 ]; then target=198.18.0.10; else target=2001:db8:1::10; fi
   for protocol in tcp udp; do
    got=$(ip netns exec "$ns" /tmp/peer "$protocol$family" "$target")
    [ "$got" = "$expect" ] || { echo "$ns $protocol$family expected $expect got $got" >&2; return 1; }
   done
  done
 done
}
dns_wire() {
 phase=$1
 for ns in lan guest unmanaged; do
  case "$ns" in lan) subnet=50;; guest) subnet=51;; unmanaged) subnet=52;; esac
  for family in 4 6; do
   if [ "$family" = 4 ]; then gateway=192.168.$subnet.1; public=198.18.0.10; private=10.66.0.53
   else gateway=fd00:$subnet::1; public=2001:db8:1::10; private=fd00:66::53; fi
   for resolver in "$gateway" "$public" "$private"; do
    expect=1.0.0.1
    if [ "$ns" != unmanaged ]; then
     if [ "$phase" = intercept ] || { [ "$phase" = upstream ] && [ "$resolver" = "$gateway" ]; }; then expect=1.1.1.1; fi
    fi
    for protocol in tcp udp; do
     answer=$(ip netns exec "$ns" /tmp/peer dns "$protocol$family" "$resolver") || {
      echo "DNS $phase failed at $ns $protocol$family @$resolver" >&2
      ip netns exec "$ns" nslookup -type=A wire.hyper.org "$resolver" || :
      ip netns exec wan nslookup -type=A wire.hyper.org 198.18.0.10 || :
      return 1
     }
     [ "$answer" = "$expect" ] || { echo "$phase DNS $ns $protocol$family @$resolver expected $expect got $answer" >&2; return 1; }
    done
   done
  done
 done
 echo "DNS $phase passed on LAN, guest and unmanaged networks (IPv4/IPv6, TCP/UDP, gateway/public/private)"
}
start_core() {
 mkdir -p -m 0700 /tmp/mihomo-test
 # As the plugin does: root in the group every apply exempts alone, with every capability.
 /tmp/peer core "$core_gid" /tmp/core -d /tmp/mihomo-test -f /tmp/core.yaml >/tmp/core.log 2>&1 &
 core_pid=$!
 sleep 1
 for attempt in 1 2 3 4 5; do
  if ! kill -0 "$core_pid"; then cat /tmp/core.log; exit 1; fi
  if [ "$method" = tun ]; then [ -e /sys/class/net/owtun0 ] && break
  elif grep -q ':4D32 ' /proc/net/tcp6; then break; fi
  sleep 1
 done
}
# Only packets the takeover marked reach the transparent listener (fw4's listener_guard chain,
# fw3's INPUT guard): a device connecting to it straight is refused, never served by the core.
listener_refused() {
 for target in 192.168.50.1 fd00:50::1; do
  case "$target" in *:*) family=6 ;; *) family=4 ;; esac
  for protocol in tcp udp; do
   ip netns exec lan /tmp/peer refused "$protocol$family" "$target" 19762 ||
    { echo "direct $protocol$family connection to the TProxy listener was not refused" >&2; return 1; }
  done
 done
}
listener_guard() {
 "$1" -w 2 "$2" INPUT ! -i lo -p "$3" --dport 19762 -m mark ! --mark 0x4f57/0xffff -m comment --comment hyper-owned-v1 -j REJECT
}
blocked_queries() {
 pids=
 for family in 4 6; do
  if [ "$family" = 4 ]; then target=198.18.0.10; gateway=192.168.50.1
  else target=2001:db8:1::10; gateway=fd00:50::1; fi
  for protocol in tcp udp; do
   (if ip netns exec lan /tmp/peer "$protocol$family" "$target" >/dev/null 2>&1; then echo "$protocol$family bypassed protection" >&2; exit 1; fi) &
   pids="$pids $!"
   (if ip netns exec lan /tmp/peer dns "$protocol$family" "$gateway" >/dev/null 2>&1; then echo "$protocol$family leaked upstream DNS" >&2; exit 1; fi) &
   pids="$pids $!"
  done
 done
 result=0
 for pid in $pids; do wait "$pid" || result=1; done
 return "$result"
}
mkdir -p /etc/hyper/private
token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
printf '%s' "$token" >/etc/hyper/private/control.token
echo ok >/www/health.txt
/usr/sbin/uhttpd -f -p 0.0.0.0:80 -h /www >/tmp/uhttpd.log 2>&1 &
 sleep 1
 wire direct
 dns_wire direct
for method in ${TEST_METHODS:-tproxy tun}; do
 case "$method" in tproxy|tun) ;; *) exit 2 ;; esac
 echo "Testing $method"
 uci set hyper.main.method="$method"; uci commit hyper
 if [ "$method" = tproxy ]; then inbound='tproxy-port: 19762'; else inbound='tun: {enable: true, device: owtun0, stack: system, auto-route: false, auto-detect-interface: false, dns-hijack: [], inet4-address: [172.30.254.1/30], inet6-address: ["fd7e:5745:4952::1/126"]}'; fi
 cat >/tmp/core.yaml <<CFG
external-controller: 127.0.0.1:19761
secret: $token
allow-lan: true
bind-address: "*"
ipv6: true
interface-name: wan-r
log-level: warning
$inbound
rules: ['MATCH,DIRECT']
hosts:
  probe.hyper.invalid: 192.0.2.1
  wire.hyper.org: 1.1.1.1
dns:
  enable: true
  ipv6: true
  listen: 127.0.0.1:19764
  enhanced-mode: redir-host
  use-hosts: true
  nameserver: [198.18.0.10]
CFG
 start_core
 helper=/usr/libexec/hyper/network
 # Real fw4 serializes generation and nft commit with this same lock.
 # Reload repeatedly across the initial guest DNS restart and handover.
 touch /tmp/reload-fw4
 (
  while [ -f /tmp/reload-fw4 ]; do
   reload_firewall
   sleep 1
  done
 ) &
 reloader=$!
 "$helper" apply "gid=$core_gid" || { sh -x "$helper" apply "gid=$core_gid"; exit 1; }
 rm -f /tmp/reload-fw4; wait "$reloader"
 wire proxy
 if [ "$method" = tproxy ]; then listener_refused; fi
 grep -q '192.168.50.1:80' /var/run/hyper/network/management.json
 [ ! -e /tmp/instance-unmanaged/99-hyper-upstream.conf ]
 dns_wire intercept
 reload_firewall
 "$helper" verify
 if [ "$method" = tproxy ] && [ "${TEST_FIREWALL:-fw4}" = fw3 ]; then
  # A guard deleted by hand is a missing rule like any other: verify notices it in either
  # family and repair puts it back.
  for xt in iptables-legacy ip6tables-legacy; do
   listener_guard "$xt" -D udp
   if "$helper" verify; then echo "verify missed the listener guard deleted from $xt" >&2; exit 1; fi
   [ "$("$helper" repair)" = repaired ]
   listener_guard "$xt" -C udp
   listener_guard "$xt" -C tcp
  done
  listener_refused
 fi
 wire proxy
 dns_wire intercept
 # The ordinary stop path must return both dnsmasq and hand-set DNS to the
 # WAN, and enabling upstream protection alone must leave hand-set DNS alone.
 "$helper" release
 wire direct
 dns_wire direct
 uci set hyper.main.dns_intercept=0
 "$helper" apply "gid=$core_gid"
 dns_wire upstream
 "$helper" release
 uci set hyper.main.dns_intercept=1
 "$helper" apply "gid=$core_gid"
 # Isolate a DNS-only failure. The API and heartbeat keep answering while both
 # transports to Mihomo DNS are blocked. Measure from fault to an uncached WAN
 # answer on the LAN, independently of when the ownership marker disappears.
 (while :; do echo ok >/var/run/hyper/new-pulse; mv -f /var/run/hyper/new-pulse /var/run/hyper/pulse; sleep 1; done) &
 dns_writer=$!
 /tmp/hyperd guard &
 dns_guard=$!
 sleep 3
 [ -e /var/run/hyper/network/owned ]
 "$helper" prerequisites >/tmp/requirements
 dns_start=$(awk '{print $1}' /proc/uptime)
 nft -f - <<'NFT'
table inet dns_fault_fixture {
 chain output {
  type filter hook output priority filter; policy accept;
  ip daddr 127.0.0.1 meta l4proto { tcp, udp } th dport 19764 drop
 }
}
NFT
 missed=0; recovered=0
 for seq in $(seq 1 25); do
  if answer=$(ip netns exec lan /tmp/peer dns udp4 192.168.50.1 "recovery-$seq.wire.hyper.org." 2>/dev/null); then
   if [ "$answer" = 1.0.0.1 ]; then recovered=1; break; fi
  else missed=1; fi
  elapsed=$(awk -v start="$dns_start" '{print int($1-start)}' /proc/uptime)
  [ "$elapsed" -lt 18 ] || break
  sleep 1
 done
 awk -v start="$dns_start" -v outcome="$recovered" -v fw="${TEST_FIREWALL:-fw4}" -v mode="$method" '{
  elapsed=$1-start; printf "%s %s DNS-only fault: LAN returned an uncached upstream answer after %.2f seconds (recovered=%d)\n",fw,mode,elapsed,outcome;
  exit !(outcome == 1 && elapsed <= 18)
 }' /proc/uptime
 [ "$missed" = 1 ]
 for _ in $(seq 1 8); do [ ! -e /var/run/hyper/network/owned ] && break; sleep 1; done
 [ ! -e /var/run/hyper/network/owned ] && kill -0 "$core_pid"
 nft delete table inet dns_fault_fixture
 kill "$dns_guard" "$dns_writer"; wait "$dns_guard" || :; wait "$dns_writer" || :
 wire direct
 dns_wire direct
 "$helper" apply "gid=$core_gid"
 # Kill one side at a time. A live heartbeat writer cannot mask a dead core.
 (while :; do echo ok >/var/run/hyper/new-pulse; mv -f /var/run/hyper/new-pulse /var/run/hyper/pulse; sleep 1; done) &
 writer=$!
 /tmp/hyperd guard &
 guard=$!
 sleep 8
 [ -e /var/run/hyper/network/owned ] || { echo 'healthy network was released'; exit 1; }
 if [ "$method" = tun ]; then kill "$core_pid"; wait "$core_pid" || :; else kill "$writer"; wait "$writer" || :; fi
 for i in 1 2 3 4 5 6 7 8 9 10 11 12; do [ -e /var/run/hyper/network/owned ] || break; sleep 1; done
 [ ! -e /var/run/hyper/network/owned ]
 kill "$guard"; wait "$guard" || :
 wire direct
 dns_wire direct
 if [ "${TEST_FIREWALL:-fw4}" = fw3 ]; then
  for xt in iptables-legacy ip6tables-legacy; do
   "$xt" -S unrelated >/dev/null
   "$xt" -t nat -C PREROUTING -i unmanaged-r -p udp --dport 65053 -m comment --comment fixture-foreign -j RETURN
   if "$xt" -t nat -S | grep -q hyper-owned-v1; then echo 'owned NAT rule remained after release' >&2; exit 1; fi
   if "$xt" -S INPUT | grep -q hyper-owned-v1; then echo 'owned INPUT rule remained after release' >&2; exit 1; fi
   "$xt" -C INPUT -i guest-r -m mark --mark 0x4f57/0xffff -j DROP
   if "$xt" -t mangle -S HYPER_LAN >/dev/null 2>&1; then exit 1; fi
  done
 fi
 [ ! -e /tmp/instance-lan/99-hyper-upstream.conf ]
 if [ "$method" = tun ]; then kill "$writer"; wait "$writer" || :; else kill "$core_pid"; wait "$core_pid" || :; fi

 [ "${TEST_TRACE:-0}" != 1 ] || set -x
 # Disable automatic direct fallback for the next confirmed takeover.
 uci set hyper.main.fail_open=0
 guest_mac=$(ip netns exec guest cat /sys/class/net/guest-c/address)
 printf 'mac %s direct\n4 192.168.50.10/32 block\n6 fd00:50::10/128 block\ndefault block\n' "$guest_mac" >/var/run/hyper/device-guard.policy
 [ "$("$helper" fault)" = direct ]
 nft -f - <<'NFT'
table inet flow_fixture {
 flowtable test_ft { hook ingress priority 0; devices = { lan-r, wan-r }; }
 chain test_forward { type filter hook forward priority filter; policy accept; meta l4proto udp flow add @test_ft; }
}
NFT
 if "$helper" apply "gid=$core_gid"; then echo 'active offload was accepted for fault protection'; exit 1; fi
 [ "$(cat /var/run/hyper/network/error)" = fault_flow_offload ]
 [ ! -e /var/run/hyper/network/owned ]
 nft delete table inet flow_fixture
 start_core
 "$helper" apply "gid=$core_gid"
 wire proxy
 (while :; do echo ok >/var/run/hyper/new-pulse; mv -f /var/run/hyper/new-pulse /var/run/hyper/pulse; sleep 1; done) &
 writer=$!
 /tmp/hyperd guard &
 guard=$!
 sleep 3
 if [ "$method" = tun ]; then
  kill "$core_pid"; wait "$core_pid" || :
  for fam in 4 6; do
   ip "-$fam" route show table 21998 | grep -q '^prohibit default'
   if [ "$fam" = 4 ]; then dst=198.18.0.10; else dst=2001:db8:1::10; fi
   if ip "-$fam" route get "$dst" mark 0x4f57 >/dev/null 2>&1; then echo 'dead TUN routed through WAN'; exit 1; fi
  done
 else kill "$writer"; wait "$writer" || :; fi
 for i in $(seq 1 20); do
  if [ -f /var/run/hyper/network/held ] && [ ! -f /var/run/hyper/network/owned ]; then break; fi
  sleep 1
 done
 [ -f /var/run/hyper/network/held ] && [ ! -f /var/run/hyper/network/owned ] || { echo 'guard did not hold'; cat /var/run/hyper/network/error; exit 1; }
 kill "$guard"; wait "$guard" || :
 if [ "$method" = tun ]; then kill "$writer"; wait "$writer" || :; fi
 [ "$("$helper" fault)" = protected ]
 reload_firewall
 blocked_queries
 wire direct 'guest unmanaged'
 for ns in guest unmanaged; do
  if [ "$ns" = guest ]; then n=51; else n=52; fi
  [ "$(ip netns exec "$ns" /tmp/peer dns udp4 "192.168.$n.1")" = 1.0.0.1 ]
  [ "$(ip netns exec "$ns" /tmp/peer dns tcp6 "fd00:$n::1")" = 1.0.0.1 ]
 done
 ip netns exec lan wget -q -O /dev/null http://192.168.50.1/health.txt
 # A failed candidate retains the protection until a healthy takeover or stop.
 uci set hyper.main.method=invalid
 if "$helper" apply "gid=$core_gid"; then echo 'invalid candidate was accepted'; exit 1; fi
 [ -f /var/run/hyper/network/held ]
 uci set hyper.main.method="$method"
 # Smart default-direct with explicit proxy exceptions replaces the old MAC
 # exemption only upon this next confirmed takeover.
 printf '4 192.168.50.10/32 block\n6 fd00:50::10/128 block\ndefault direct\n' >/var/run/hyper/device-guard.policy
 if [ "$method" = tun ]; then start_core; fi
 "$helper" apply "gid=$core_gid"
 [ ! -f /var/run/hyper/network/held ]
 wire proxy
 [ "$("$helper" fault)" = protected ]
 blocked_queries
 wire direct 'guest unmanaged'
 "$helper" release
 [ ! -e /var/run/hyper/network/held ]
 # Simulate init stop while the guard has already noticed a missing pulse
 # and serve has queued a retry. Both share the network lock with release.
 "$helper" apply "gid=$core_gid"
 touch /var/run/hyper/stopping
 "$helper" fault >/tmp/stopping-disposition &
 stopping_fault=$!
 "$helper" release &
 stopping_release=$!
 if "$helper" apply "gid=$core_gid"; then echo 'queued apply retook LAN during stop'; exit 1; fi
 wait "$stopping_release"; wait "$stopping_fault"
 [ "$(cat /tmp/stopping-disposition)" = direct ]
 [ ! -f /var/run/hyper/network/owned ] && [ ! -e /var/run/hyper/network/held ]
 rm -f /var/run/hyper/stopping
 wire direct
 dns_wire direct
 kill "$core_pid"; wait "$core_pid" || :
 uci set hyper.main.fail_open=1
 echo "$method hold, retry and manual release passed (IPv4/IPv6 TCP/UDP, managed/direct/unmanaged)"
done
# A pre-existing chain blocks takeover. Rules added by another owner while we
# run survive cleanup, even when they reside in or refer to our own chain.
if [ "${TEST_FIREWALL:-fw4}" = fw3 ]; then
 . /usr/libexec/hyper/firewall-backend
 state=/var/run/hyper/network
 legacy_families=4; note=hyper-owned-v1; port=19762; mark=0x4f57; method=tproxy; interfaces=lan-r; v6=0
 xt=iptables-legacy
 "$xt" -t mangle -N HYPER_LAN
 if legacy_prepare; then echo 'foreign chain collision was accepted' >&2; exit 1; fi
 "$xt" -t mangle -X HYPER_LAN
 legacy_prepare
 legacy_apply
 for protocol in tcp udp; do listener_guard "$xt" -C "$protocol"; done
 # Without the IPv6 takeover the core listens on IPv4 alone; IPv6 is left untouched.
 if ip6tables-legacy -S INPUT | grep -q hyper-owned-v1; then echo 'IPv6 guard without the IPv6 takeover' >&2; exit 1; fi
 "$xt" -t mangle -A HYPER_LAN -m comment --comment fixture-foreign -j RETURN
 "$xt" -t mangle -N fixture_ref
 "$xt" -t mangle -A fixture_ref -j HYPER_LAN
 legacy_remove 4
 "$xt" -t mangle -C HYPER_LAN -m comment --comment fixture-foreign -j RETURN
 "$xt" -t mangle -C fixture_ref -j HYPER_LAN
 if "$xt" -t mangle -S PREROUTING | grep -q HYPER_LAN; then exit 1; fi
 if "$xt" -S INPUT | grep -q hyper-owned-v1; then echo 'listener guard remained after removal' >&2; exit 1; fi
fi
