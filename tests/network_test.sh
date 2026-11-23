#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Runs luci-app-hyper/root/usr/libexec/hyper/network against real nftables and iproute2
# inside an unprivileged user + network namespace, with a firewall4-like table:
#
#   tests/network_test.sh
#
# Checks each plan variant installs exactly the owned objects and that down removes them
# without touching rules it does not own.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
HELPER="$HERE/../luci-app-hyper/root/usr/libexec/hyper/network"

if [ "${HYPER_IN_NETNS:-}" != 1 ]; then
	command -v nft >/dev/null && command -v unshare >/dev/null || { echo "skip: needs nft and unshare"; exit 0; }
	# Root needs only the network namespace: in a user namespace of its own it could no
	# longer pass directories other users own (the runner's home is 0750).
	ns="unshare -rn"
	[ "$(id -u)" = 0 ] && ns="unshare -n"
	exec env HYPER_IN_NETNS=1 $ns sh "$0" "$@"
fi

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/state" "$T/etc" "$T/init" "$T/dnsmasq.d"
printf 'conf-dir=%s/dnsmasq.d\n' "$T" >"$T/etc/dnsmasq.conf.cfg01411c"
cat >"$T/bin/ubus" <<'EOF'
#!/bin/sh
case "$2" in network.interface.lan) echo '{"l3_device":"br-lan"}' ;; network.interface.guest) echo '{"l3_device":"br-guest"}' ;; *) exit 4 ;; esac
EOF
cat >"$T/bin/jsonfilter" <<'EOF'
#!/bin/sh
sed -n 's/.*"l3_device":"\([^"]*\)".*/\1/p'
EOF
# dnsmasq 2.87+ lists --filter-AAAA in its help; DNSMASQ_OLD=1 plays an older build.
cat >"$T/bin/dnsmasq" <<'EOF'
#!/bin/sh
[ "$1" = --help ] || exit 1
echo '-R, --no-resolv           Do NOT read resolv.conf.'
[ -n "${DNSMASQ_OLD:-}" ] || echo '    --filter-AAAA          Filter all AAAA requests.'
EOF
cat >"$T/init/dnsmasq" <<EOF
#!/bin/sh
echo "dnsmasq \$1" >>"$T/calls"
EOF
cp "$HERE/lib/uci" "$T/bin/uci"
chmod +x "$T/bin/"* "$T/init/dnsmasq"
export UCI_STORE="$T/uci.store"
# DNS forwardings set in LuCI: two for every name, one for a single domain.
printf '%s\n' 'dhcp.cfg01411c=dnsmasq' 'dhcp.cfg01411c.cachesize=1000' 'dhcp.cfg01411c.server=8.8.8.8' \
	'dhcp.cfg01411c.server=/lan.example/192.168.1.53' 'dhcp.cfg01411c.server=/#/127.0.0.1#5335' >"$UCI_STORE"
cp "$UCI_STORE" "$T/uci.orig"
export PATH="$T/bin:$PATH" HYPER_NETWORK_STATE="$T/state" HYPER_DNS_CONFIG="$T/etc" HYPER_INIT="$T/init" \
	HYPER_LIBEXEC="$T/libexec" HYPER_FW4_LOCK="$T/fw4.lock" HYPER_UCI_STATE="$T/var-state"

ip link set lo up
for dev in br-lan br-guest hypertun0; do ip link add "$dev" type dummy && ip link set "$dev" up; done
ip addr add 192.168.1.1/24 dev br-lan
# A public LAN prefix, as some ISPs route to the customer.
ip addr add 203.0.113.1/28 dev br-guest
nft -f - <<'EOF'
table inet fw4 {
	chain input { type filter hook input priority filter; policy drop; iifname "lo" accept comment "foreign"; }
	chain forward { type filter hook forward priority filter; policy drop; }
}
EOF

fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
has() { printf '%s\n' "$1" | grep -Fq -- "$2"; }

plan() {
	cat >"$T/state/plan.env" <<EOF
HYPER_METHOD='${METHOD:-tproxy}'
HYPER_IPV6='${IPV6:-0}'
HYPER_ROUTER_SELF='${SELF:-1}'
HYPER_FAKE_IP='${FAKE:-0}'
HYPER_TPROXY_PORT='5332'
HYPER_DNS_PORT='5333'
HYPER_SELF_MARK='100'
HYPER_MARK='${MARK:-101}'
HYPER_TABLE='100'
HYPER_TUN_NAME='hypertun0'
HYPER_DNS_REDIRECT='1'
HYPER_LAN_MODE='${LANMODE:-disabled}'
HYPER_LAN_NETWORKS='${LANS:-lan}'
HYPER_DIRECT_MACS='${DIRECT:-}'
HYPER_PROXY_MACS='${PROXY:-}'
HYPER_CORE_GID='${GID:-0}'
EOF
}

released() {
	! nft list table inet hyper >/dev/null 2>&1 &&
	[ -z "$(nft -a list chain inet fw4 input | grep hyper-owned)" ] &&
	[ -z "$(nft -a list chain inet fw4 forward | grep hyper-owned)" ] &&
	nft list chain inet fw4 input | grep -q foreign &&
	! ip rule show | grep -q 'lookup 100' && ! ip -6 rule show | grep -q 'lookup 100' &&
	[ -z "$(ip route show table 100 2>/dev/null)" ] &&
	[ ! -e "$T/dnsmasq.d/hyper-upstream.conf" ] && [ ! -e "$T/state/owned" ]
}

echo "== tproxy, router self, direct MACs"
GID=990 FAKE=1 METHOD=tproxy LANMODE=except_listed DIRECT='00:d9:d1:8b:33:e2 aa:bb:cc:dd:ee:ff' plan
check "up" 'sh "$HELPER" up'
tbl="$(nft list table inet hyper 2>/dev/null || :)"
check "owned table" 'has "$tbl" "comment \"hyper-owned-v1\""'
check "tproxy capture" 'has "$tbl" "tproxy to :5332"'
check "direct MACs bypass" 'has "$tbl" "ether saddr @direct_macs return" && has "$tbl" "00:d9:d1:8b:33:e2"'
# Direct devices resolve through the core too; only it can serve the fake addresses they get.
check "fake addresses reach the core before the MAC bypass" 'printf "%s\n" "$tbl" | awk "/ip daddr 198.18.0.0\\/16 goto capture/{f=NR} /ether saddr @direct_macs return/{m=NR} END{exit !(f && m && f<m)}"'
check "every port intercepted" '! has "$tbl" "@ports"'
check "NTP from the LAN stays direct" '[ "$(printf "%s\n" "$tbl" | grep -c "udp dport 123 return")" = 1 ]'
# The router's own UDP (WireGuard, Tailscale, STUN, time sync) stays direct, except toward fake addresses.
check "router self: TCP and fake addresses only" 'has "$tbl" "meta l4proto tcp goto mark_local" && has "$tbl" "ip daddr 198.18.0.0/16 goto mark_local" && ! has "$tbl" "		goto mark_local"'
check "dnsmasq: cache off in uci state" '[ "$(uci -P x get dhcp.cfg01411c.cachesize)" = 0 ]'
check "dnsmasq: forwardings for every name set aside, the domain one kept" '[ "$(uci -P x -d "|" get dhcp.cfg01411c.server)" = /lan.example/192.168.1.53 ]'
check "dnsmasq: /etc/config untouched, nothing committed" 'cmp -s "$UCI_STORE" "$T/uci.orig" && [ ! -s "$UCI_STORE.commits" ]'
check "attached networks bypass" 'nft list set inet hyper local4 | grep -q "192.168.1.0/24" && nft list set inet hyper local4 | grep -q "203.0.113.0/28" && has "$tbl" "ip daddr @local4 return"'
ip addr add 198.51.100.1/24 dev br-guest
check "refresh picks up a new address" 'sh "$HELPER" refresh && nft list set inet hyper local4 | grep -q "198.51.100.0/24"'
check "refresh keeps one copy" 'sh "$HELPER" refresh && [ "$(nft list set inet hyper local4 | grep -o "198.51.100.0/24" | wc -l)" = 1 ]'
ip addr del 198.51.100.1/24 dev br-guest
check "router self chain" 'has "$tbl" "meta mark & 0x0000ffff == 0x00000064 return"'
check "core group never loops" 'has "$tbl" "meta skgid 990 return"'
check "lo loop back" 'has "$tbl" "iifname \"lo\""'
check "DNS hijack, both families" 'has "$tbl" "redirect to :53" && ! printf "%s\n" "$tbl" | grep "redirect to :53" | grep -q nfproto'
check "transparent listener guarded" 'has "$tbl" "chain listener_guard" && has "$tbl" "th dport 5332 meta mark & 0x0000ffff != 0x00000065 reject"'
check "fw4 input allowance" 'nft list chain inet fw4 input | grep -q "hyper-owned-v1-input"'
check "policy rule" 'ip rule show | grep -q "fwmark 0x65/0xffff lookup 100"'
check "local route" 'ip route show table 100 | grep -q "local default dev lo"'
check "dnsmasq upstream" 'grep -qx "server=127.0.0.1#5333" "$T/dnsmasq.d/hyper-upstream.conf" && grep -qx filter-AAAA "$T/dnsmasq.d/hyper-upstream.conf"'
check "inbound replies untouched" '[ "$(printf "%s\n" "$tbl" | grep -c "ct direction reply return")" = 2 ]'
check "owned marker" '[ -e "$T/state/owned" ]'
check "up again replaces" 'sh "$HELPER" up && [ "$(nft -a list chain inet fw4 input | grep -c hyper-owned)" = 1 ] && [ "$(ip rule show | grep -c "lookup 100")" = 1 ]'
replay="$T/state/replay/hyper.nft"
check "table copy kept for fw4, and it passes nft -c" '[ -s "$replay" ] && nft -c -f "$replay"'
ip addr add 198.51.100.1/24 dev br-guest
check "refresh updates the copy" 'sh "$HELPER" refresh && grep -q "198.51.100.0/24" "$replay"'
ip addr del 198.51.100.1/24 dev br-guest
sh "$HELPER" refresh
# fw4 includes the copy in its own transaction on every start, restart and reload.
check "fw4 reload replays the table in place" 'nft -f "$replay" && [ "$(nft list tables | grep -c "inet hyper")" = 1 ] && nft list table inet hyper | grep -q "comment \"hyper-owned-v1\""'
check "nothing to repair" '[ -z "$(sh "$HELPER" repair)" ]'
# A firewall restart by another tool: our table and one allowance gone.
nft delete table inet hyper
for h in $(nft -a list chain inet fw4 input | awk "/hyper-owned/ { print \$NF }"); do nft delete rule inet fw4 input handle "$h"; done
check "repair restores the table and the allowance" '[ "$(sh "$HELPER" repair)" = repaired ] && nft list table inet hyper | grep -q "comment \"hyper-owned-v1\"" && [ "$(nft list chain inet fw4 input | grep -c hyper-owned-v1-input)" = 1 ]'
check "attached networks restored with it" 'nft list set inet hyper local4 | grep -q "192.168.1.0/24"'
# fw4 stopped altogether (fw4 flush): the table runs on its own until fw4 replays it.
nft delete table inet hyper
nft list table inet fw4 >"$T/fw4.saved"
nft delete table inet fw4
check "repair with fw4 stopped restores the table alone" '[ "$(sh "$HELPER" repair)" = repaired ] && nft list table inet hyper >/dev/null && ! nft list table inet fw4 >/dev/null 2>&1'
grep -v hyper-owned "$T/fw4.saved" | nft -f -
check "repair puts the allowance back once fw4 runs again" '[ "$(sh "$HELPER" repair)" = repaired ] && [ "$(nft list chain inet fw4 input | grep -c hyper-owned-v1-input)" = 1 ]'
nft add table inet hyper-other
check "a table we do not own is never replaced" 'nft delete table inet hyper && nft add table inet hyper && ! sh "$HELPER" repair 2>/dev/null && nft delete table inet hyper && sh "$HELPER" repair >/dev/null'
nft delete table inet hyper-other
check "down" 'sh "$HELPER" down'
check "everything released" released
check "fw4 stops replaying the table" '[ ! -e "$T/state/replay" ]'
check "nothing to repair once released" '! sh "$HELPER" repair 2>/dev/null && ! nft list table inet hyper >/dev/null 2>&1'
check "dnsmasq: uci state reverted" '[ ! -s "$UCI_STORE.state" ] && [ "$(uci -P x get dhcp.cfg01411c.cachesize)" = 1000 ] && [ "$(uci -P x get dhcp.cfg01411c.server)" = "8.8.8.8 /lan.example/192.168.1.53 /#/127.0.0.1#5335" ]'

echo "== TUN, IPv6, two LANs, listed_only"
FAKE=1 METHOD=tun MARK=102 IPV6=1 SELF=0 LANS='lan guest' LANMODE=listed_only PROXY='aa:bb:cc:dd:ee:01' plan
check "up" 'sh "$HELPER" up'
tbl="$(nft list table inet hyper 2>/dev/null || :)"
check "tun mark, no tproxy" 'has "$tbl" "| 0x00000066" && ! has "$tbl" tproxy'
check "both LANs" 'has "$tbl" "\"br-lan\", \"br-guest\""'
check "listed_only" 'has "$tbl" "ether saddr != @proxy_macs return"'
check "listed_only: unlisted devices still reach the core for fake addresses" 'printf "%s\n" "$tbl" | awk "/ip daddr 198.18.0.0\\/16 goto capture/{f=NR} /ether saddr != @proxy_macs return/{m=NR} END{exit !(f && m && f<m)}"'
check "router self off: only fake addresses captured" 'has "$tbl" "ip daddr 198.18.0.0/16 goto mark_local" && ! has "$tbl" "		goto mark_local"'
# Pings stay real ICMP over WAN: answered by the core they would hide blocked hosts.
check "TUN: only pings to fake addresses captured, no listener guard" '! printf "%s\n" "$tbl" | grep "echo-request" | grep -qv "198.18.0.0/16" && ! has "$tbl" listener_guard'
check "fw4 forward allowances" '[ "$(nft list chain inet fw4 forward | grep -c hyper-owned-v1-forward)" = 2 ]'
check "IPv6 policy rule" 'ip -6 rule show | grep -q "lookup 100"'
check "tun route" 'ip route show table 100 | grep -q "default dev hypertun0"'
check "no filter-AAAA with IPv6" '! grep -qi filter-aaaa "$T/dnsmasq.d/hyper-upstream.conf"'
check "fault releases" 'sh "$HELPER" fault'
check "everything released" released

echo "== a managed LAN comes up after the takeover"
LANS='lan guest' plan
mv "$T/bin/ubus" "$T/ubus.real"
printf '#!/bin/sh\ncase "$2" in network.interface.lan) echo %s ;; *) exit 4 ;; esac\n' "'{\"l3_device\":\"br-lan\"}'" >"$T/bin/ubus"
chmod +x "$T/bin/ubus"
check "up with the guest LAN down" 'sh "$HELPER" up && has "$(nft list chain inet hyper lan)" "iifname != \"br-lan\" return"'
check "nothing missing while it stays down" '[ -z "$(sh "$HELPER" missing-lans)" ]'
mv "$T/ubus.real" "$T/bin/ubus"
check "the guest LAN coming up is reported" '[ "$(sh "$HELPER" missing-lans)" = br-guest ]'
sh "$HELPER" down
check "nothing reported while released" '[ -z "$(sh "$HELPER" missing-lans)" ]'

echo "== DNS only"
METHOD=none plan
check "up" 'sh "$HELPER" up'
tbl="$(nft list table inet hyper 2>/dev/null || :)"
check "DNS hijack only" 'has "$tbl" "chain dns" && ! has "$tbl" "chain lan"'
check "no routes" '! ip rule show | grep -q "lookup 100"'
check "dnsmasq upstream" 'grep -qx "server=127.0.0.1#5333" "$T/dnsmasq.d/hyper-upstream.conf" && ! grep -qi filter-aaaa "$T/dnsmasq.d/hyper-upstream.conf"'
sh "$HELPER" down

echo "== the init script is stopping the service"
plan
sh "$HELPER" up >/dev/null 2>&1
touch "$T/stopping"
check "repair refuses" '! sh "$HELPER" repair 2>/dev/null'
check "release still works" 'sh "$HELPER" down && released'
check "up refuses and leaves nothing" '! sh "$HELPER" up 2>/dev/null && released'
rm -f "$T/stopping"

echo "== crash: the state is lost while the takeover is up"
plan
sh "$HELPER" up >/dev/null 2>&1
rm -f "$T/state/dns"
check "release reverts the uci state and removes the include anyway" 'sh "$HELPER" down && [ ! -e "$T/dnsmasq.d/hyper-upstream.conf" ] && [ ! -s "$UCI_STORE.state" ] && cmp -s "$UCI_STORE" "$T/uci.orig"'

echo "== a development build left the cache off in /etc/config"
sed -i 's/^dhcp.cfg01411c.cachesize=1000$/dhcp.cfg01411c.cachesize=0/' "$UCI_STORE"
printf '%s\n' 'dhcp.cfg01411c.hyper_cachesize=1000' >>"$UCI_STORE"
check "release puts the size back" 'sh "$HELPER" down && grep -qx "dhcp.cfg01411c.cachesize=1000" "$UCI_STORE" && ! grep -q hyper_cachesize "$UCI_STORE"'

echo "== fake-ip without router self"
METHOD=tproxy SELF=0 FAKE=1 plan
check "up" 'sh "$HELPER" up'
tbl="$(nft list table inet hyper 2>/dev/null || :)"
check "fake-ip captured" 'has "$tbl" "ip daddr 198.18.0.0/16 goto capture"'
check "pings to fake-ip addresses answered" 'has "$tbl" "ip daddr 198.18.0.0/16 icmp type echo-request"'
check "router: fake-ip only" 'has "$tbl" "ip daddr 198.18.0.0/16 goto mark_local" && ! has "$tbl" "		goto mark_local"'
sh "$HELPER" down

echo "== released"
check "refresh while released is a no-op" 'sh "$HELPER" refresh && ! nft list table inet hyper >/dev/null 2>&1'

echo "== dnsmasq older than 2.87"
plan
check "up" 'DNSMASQ_OLD=1 sh "$HELPER" up'
check "no filter-AAAA line dnsmasq would reject" '! grep -qi filter-aaaa "$T/dnsmasq.d/hyper-upstream.conf"'
sh "$HELPER" down

echo "== failures roll back"
LANS=wan plan
check "no LAN interface fails" '! sh "$HELPER" up 2>/dev/null'
check "nothing left behind" released
plan
mkdir -p "$T/libexec"
printf '#!/bin/sh\nexit 1\n' >"$T/libexec/proxy-conflicts"; chmod +x "$T/libexec/proxy-conflicts"
check "conflict handover failure aborts" '! sh "$HELPER" up 2>/dev/null && released'
rm -f "$T/libexec/proxy-conflicts"
ip rule add pref 100 fwmark 0x99 table 100
check "foreign policy rule refuses" '! sh "$HELPER" up 2>/dev/null && ! nft list table inet hyper >/dev/null 2>&1'
ip rule del pref 100 fwmark 0x99 table 100
check "usage" '! sh "$HELPER" bogus 2>/dev/null'

exit "$fail"
