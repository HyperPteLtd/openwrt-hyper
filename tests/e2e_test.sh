#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# End to end in a throwaway network namespace: the real hyperd (serve + guard), the real
# mihomo and the real network helper on nftables / iproute2, with a firewall4-like table.
#
#   start -> takeover installed -> core dies and stays down -> guard fails open
#   -> core comes back -> serve restores the takeover -> stop releases everything
#
#   HYPERD=/path/to/hyperd MIHOMO=/path/to/mihomo tests/e2e_test.sh
#   E2E_METHOD=tun ...    the TUN path
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
PKG="$ROOT/luci-app-hyper/root"

if [ "${HYPER_IN_NETNS:-}" != 1 ]; then
	: "${HYPERD:?set HYPERD}" "${MIHOMO:?set MIHOMO}"
	command -v nft >/dev/null || { echo "skip: needs nft"; exit 0; }
	# HYPER_E2E_ROOT=1 prefers real root (sudo), which also checks the core's identity and limits.
	if [ "${HYPER_E2E_ROOT:-}" != 1 ] && unshare -rn true 2>/dev/null; then exec env HYPER_IN_NETNS=1 unshare -rn sh "$0"
	# As real root the core runs as root in the core group (nogroup stands in for hyper).
	elif sudo -n unshare -n true 2>/dev/null; then exec sudo -n env HYPER_IN_NETNS=1 HYPERD_CORE_GROUP=nogroup E2E_METHOD="${E2E_METHOD:-tproxy}" HYPERD="$HYPERD" MIHOMO="$MIHOMO" PATH="$PATH" unshare -n sh "$0"
	else echo "skip: needs unprivileged user namespaces or sudo"; exit 0; fi
fi

umask 022  # an installed router layout
R="$(mktemp -d)"
chmod 0711 "$R"  # as /var/run/hyper
cleanup() {
	[ -z "${serve:-}" ] || kill "$serve" 2>/dev/null || :
	[ -z "${guard:-}" ] || kill "$guard" 2>/dev/null || :
	wait 2>/dev/null || :
	rm -rf "$R"
}
trap cleanup EXIT
mkdir -p "$R/bin" "$R/etc/config" "$R/etc/hyper" "$R/usr/libexec/hyper/bin" "$R/var/run" "$R/tmp/etc" "$R/tmp/dnsmasq.d" "$R/init"
cp "$PKG/etc/config/hyper" "$R/etc/config/hyper"
METHOD="${E2E_METHOD:-tproxy}"
sed -i "s/option enabled '0'/option enabled '1'/; s/option main_node 'nil'/option main_node 'pp_e2e'/; s/option proxy_mode 'tproxy'/option proxy_mode '$METHOD'/" "$R/etc/config/hyper"
tun_mode_before=$(stat -c %a /dev/net/tun 2>/dev/null || echo none)
if [ -n "${HYPERD_CORE_GROUP:-}" ]; then
	# What an earlier build left: the core directory and its cache under the core's own account.
	mkdir -p "$R/var/run/hyper/core" && echo stale >"$R/var/run/hyper/core/cache.db"
	chown -R nobody "$R/var/run/hyper/core" && chmod 0700 "$R/var/run/hyper/core"
fi
cp -R "$PKG/etc/hyper/rule-set" "$R/etc/hyper/"
cp "$PKG/usr/libexec/hyper/network" "$PKG/usr/libexec/hyper/proxy-conflicts" "$R/usr/libexec/hyper/"
cp "$HYPERD" "$R/usr/libexec/hyper/bin/hyperd"
cp "$MIHOMO" "$R/usr/libexec/hyper/bin/mihomo" && chmod 0755 "$R/usr/libexec/hyper/bin/mihomo"
printf '{"jwt":"e2e","email":"e2e@example.com"}\n' >"$R/etc/hyper/session"
cat >"$R/etc/hyper/nodes.json" <<'EOF'
{"version":1,"nodes":[{"id":"pp_e2e","label":"E2E","country":"JP","sort":1,
 "proxy":{"type":"anytls","server":"127.0.0.1","port":1,"password":"e2e","sni":"e2e.example.com"}}]}
EOF
printf 'conf-dir=%s/tmp/dnsmasq.d\n' "$R" >"$R/tmp/etc/dnsmasq.conf.cfg01411c"
cat >"$R/bin/ubus" <<'EOF'
#!/bin/sh
case "$*" in
	*network.interface.wan*) echo '{"dns-server":["192.0.2.53"]}' ;;
	*network.interface.lan*) echo '{"l3_device":"br-lan"}' ;;
	*'service list'*) echo '{}' ;;
	*) exit 4 ;;
esac
EOF
cat >"$R/bin/jsonfilter" <<'EOF'
#!/bin/sh
# Enough of jsonfilter for the helpers: -e '@' echoes the input, l3_device is extracted.
in=; expr=
while [ $# -gt 0 ]; do case "$1" in -i) in=$2; shift ;; -e) expr=$2; shift ;; esac; shift; done
data=$(if [ -n "$in" ]; then cat "$in"; else cat; fi)
case "$expr" in
	@) printf '%s\n' "$data" ;;
	*l3_device*) printf '%s\n' "$data" | sed -n 's/.*"l3_device":"\([^"]*\)".*/\1/p' ;;
esac
EOF
cp "$HERE/lib/uci" "$R/bin/uci"
printf '%s\n' 'dhcp.cfg01411c=dnsmasq' 'dhcp.cfg01411c.cachesize=1000' >"$R/uci.store"
printf '#!/bin/sh\necho "dnsmasq $1" >>"%s/dnsmasq-calls"\n' "$R" >"$R/init/dnsmasq"
chmod +x "$R/bin/"* "$R/init/dnsmasq"
export UCI_STORE="$R/uci.store" PATH="$R/bin:$PATH" HYPERD_ROOT="$R" HYPER_NETWORK_STATE="$R/var/run/hyper/network" \
	HYPER_DNS_CONFIG="$R/tmp/etc" HYPER_INIT="$R/init" HYPER_FW4_LOCK="$R/fw4.lock" \
	HYPER_CONFLICT_PROC="$R/proc" HYPER_CONFLICT_TMP="$R/var/run" HYPER_CONFLICT_DNS_CONFIG="$R/tmp/etc" HYPER_RUN_SESSION="$R/usr/libexec/hyper/bin/hyperd"
mkdir -p "$R/proc"

ip link set lo up
ip link add br-lan type dummy && ip link set br-lan up && ip addr add 192.168.1.1/24 dev br-lan
nft -f - <<'EOF'
table inet fw4 {
	chain input { type filter hook input priority filter; policy drop; iifname "lo" accept; }
	chain forward { type filter hook forward priority filter; policy drop; }
}
EOF

H="$R/usr/libexec/hyper/bin/hyperd"
call() { printf '%s' "${2:-{\}}" | "$H" call "$1"; }
field() { sed -n "s/.*\"$1\":\([^,}]*\).*/\1/p" | head -1 | tr -d '"'; }
fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
wait_for() {
	n=0
	until eval "$2"; do
		n=$((n + 1))
		[ "$n" -lt "$3" ] || { echo "FAIL $1 (timed out)"; fail=1; return 1; }
		sleep 1
	done
	echo "ok   $1"
}
owned() { [ -e "$R/var/run/hyper/network/owned" ] && nft list table inet hyper >/dev/null 2>&1; }

set +e
"$H" serve >"$R/serve.log" 2>&1 & serve=$!
"$H" guard >"$R/guard.log" 2>&1 & guard=$!

wait_for "serve starts the core (enabled at boot)" '[ "$(call status | field running)" = true ]' 30
wait_for "takeover installed" owned 15
if [ "$METHOD" = tun ]; then
	check "TUN device and route" 'ip route show table 100 | grep -q "default dev hypertun0"'
	check "policy rule" 'ip rule show | grep -q "fwmark 0x66/0xffff lookup 100"'
	check "router's /dev/net/tun untouched" '[ "$(stat -c %a /dev/net/tun 2>/dev/null || echo none)" = "$tun_mode_before" ]'
else
	check "TProxy capture rule" 'nft list table inet hyper | grep -q "tproxy to :5332"'
	check "policy rule" 'ip rule show | grep -q "fwmark 0x65/0xffff lookup 100"'
fi
check "fw4 allowance" 'nft list chain inet fw4 input forward 2>/dev/null | grep -q hyper-owned-v1 || nft list ruleset | grep -q hyper-owned-v1'
check "dnsmasq points at the core" 'grep -qx "server=127.0.0.1#5333" "$R/tmp/dnsmasq.d/hyper-upstream.conf"'
check "core answers DNS port" 'grep -q ":14D5 " /proc/net/udp'
# A start counts once it held four healthy checks a second apart.
wait_for "start confirmed healthy" '[ "$(call status | sed -n "s/.*\"health\":{\"state\":\"\([a-z]*\)\".*/\1/p")" = ok ] && call status | grep -q "\"state\":\"done\""' 10
check "selected node" 'call status | grep -q "\"main_node_label\":\"E2E\""'
# The core itself.
corepid() { pgrep -f "^$R/usr/libexec/hyper/bin/mihomo -d" | head -1; }
pid1=$(corepid)
if [ -n "${HYPERD_CORE_GROUP:-}" ]; then
	gid=$(getent group "$HYPERD_CORE_GROUP" | cut -d: -f3)
	st() { awk -v k="$1:" '$1 == k { $1 = ""; sub(/^ /, ""); print }' "/proc/$pid1/status"; }
	check "core keeps root's user ID" '[ "$(st Uid)" = "0 0 0 0" ]'
	check "core runs in the core group only" '[ "$(st Gid)" = "$gid $gid $gid $gid" ] && [ -z "$(st Groups)" ]'
	# As OpenClash runs its core: every capability root holds, no bound, no no_new_privs.
	own() { awk -v k="$1:" '$1 == k { print $2 }' /proc/self/status; }
	check "core holds every capability root holds" '[ "$(st CapEff)" = "$(own CapBnd)" ] && [ "$(st CapBnd)" = "$(own CapBnd)" ]'
	check "core runs without no_new_privs" '[ "$(st NoNewPrivs)" = 0 ]'
	files=$(awk '{ print ($1 < 1000000) ? $1 : 1000000 }' /proc/sys/fs/nr_open)
	check "core may open $files files, soft and hard" '[ "$(awk "/^Max open files/ { print \$4, \$5 }" /proc/$pid1/limits)" = "$files $files" ]'
	check "a core directory another account left is rebuilt as root's" '[ "$(stat -c "%u %a" "$R/var/run/hyper/core")" = "0 700" ] && ! grep -qs stale "$R/var/run/hyper/core/cache.db"'
	check "router-self chain skips the core group" 'nft list table inet hyper | grep -q "meta skgid $gid return"'
	check "core shares the router's mount namespace (opens its /dev/net/tun)" '[ "$(readlink /proc/$pid1/ns/mnt)" = "$(readlink /proc/self/ns/mnt)" ]'
fi
check "interface notice keeps the core (config unchanged)" '"$H" notify-network | grep -q "\"restarted\":false" && [ "$(corepid)" = "$pid1" ]'
check "refresh keeps the takeover" 'sh "$R/usr/libexec/hyper/network" refresh && owned'

echo "== the core dies and cannot restart"
ln -sf /bin/false "$R/usr/libexec/hyper/bin/mihomo.new" && mv -f "$R/usr/libexec/hyper/bin/mihomo.new" "$R/usr/libexec/hyper/bin/mihomo"
pkill -9 -f "$R/var/run/hyper/core" || kill -9 "$(pgrep -f "mihomo -d $R" | head -1)"
# The owned marker goes last, after the firewall, routes and dnsmasq are restored.
wait_for "guard fails open: takeover released" '[ ! -e "$R/var/run/hyper/network/owned" ]' 20
check "LAN direct: no capture rule, dnsmasq restored" '! nft list tables | grep -q "inet hyper" && [ ! -e "$R/tmp/dnsmasq.d/hyper-upstream.conf" ]'
wait_for "status shows failopen" '[ "$(call status | sed -n "s/.*\"health\":{\"state\":\"\([a-z]*\)\".*/\1/p")" = failopen ]' 15

echo "== the core comes back"
cp "$MIHOMO" "$R/usr/libexec/hyper/bin/mihomo.new" && chmod 0755 "$R/usr/libexec/hyper/bin/mihomo.new" && mv -f "$R/usr/libexec/hyper/bin/mihomo.new" "$R/usr/libexec/hyper/bin/mihomo"
wait_for "serve restores the takeover" owned 60
wait_for "status health ok again" '[ "$(call status | sed -n "s/.*\"health\":{\"state\":\"\([a-z]*\)\".*/\1/p")" = ok ]' 15

echo "== stop"
call set_enabled '{"enabled":false}' >/dev/null
wait_for "stop releases everything" '! owned && ! ip rule show | grep -q "lookup 100" && [ "$(call status | field running)" = false ]' 20
check "dnsmasq cache restored" 'grep -qx "dhcp.cfg01411c.cachesize=1000" "$R/uci.store" && ! grep -q hyper_cachesize "$R/uci.store"'
check "fw4 left as it was" '! nft list chain inet fw4 input | grep -q hyper-owned && nft list chain inet fw4 input | grep -q "iifname \"lo\" accept"'

if [ "$fail" != 0 ]; then
	echo "--- hyper.log"; cat "$R/var/run/hyper/hyper.log" 2>/dev/null
	echo "--- serve"; cat "$R/serve.log"; echo "--- guard"; cat "$R/guard.log"
fi
exit "$fail"
