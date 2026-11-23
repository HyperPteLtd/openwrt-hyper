#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Install luci-app-hyper on a real OpenWrt userland and check it there: dependencies resolve,
# the bundled hyperd and mihomo run, first boot creates the core group and device id,
# rpcd serves luci.hyper through hyperd, and the network helper's rules load on the
# firmware's own nftables and iproute2.
#
#   tests/router-test.sh <openwrt rootfs image> <bin dir | package> [docker platform]
#
#   bin dir   hyperd and mihomo from scripts/stage-binaries.sh; the package files are copied
#             from the source tree and every LUCI_DEPENDS entry must resolve in the feeds
#   package   a built .ipk / .apk, installed with opkg / apk (dependencies included)
#
#   e.g. tests/router-test.sh openwrt-rootfs:23.05.6 dist/23.05-24.10_luci-app-hyper_0.1.0_x86_64.ipk
#
# Needs Docker and network access (installs the package's dependencies from the image's feeds).
set -eu

IMAGE="$1"
SRC="$2"
PLATFORM="${3:-}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT INT TERM
mkdir -p "$stage/src/luci-app-hyper" "$stage/src/bin" "$stage/src/pkg" "$stage/src/tests"
cp -R "$ROOT/tests/network_test.sh" "$ROOT/tests/lib" "$stage/src/tests/"
cp -R "$ROOT/luci-app-hyper/root" "$stage/src/luci-app-hyper/"
cp "$ROOT/luci-app-hyper/Makefile" "$stage/src/"
case "$SRC" in
	*.ipk|*.apk) cp "$SRC" "$stage/src/pkg/" ;;
	*) cp "$SRC"/* "$stage/src/bin/" ;;
esac

cat > "$stage/src/run-inner.sh" <<'INNER'
set -eu
mkdir -p /var/lock /var/run/ubus /tmp/lock /tmp/run
if command -v apk >/dev/null 2>&1; then
	pm_update() { apk update >/dev/null; }
	pm_add() { apk add "$@" >/dev/null; }
	pm_try() { apk add --simulate "$@"; }
	pm_file() { apk add --allow-untrusted "$1"; }
else
	pm_update() { opkg update >/dev/null; }
	pm_add() { opkg install "$@" >/dev/null; }
	pm_try() { opkg install --noaction "$@"; }
	pm_file() { opkg install "$1"; }
fi
# Feeds are retried (four attempts, 10, 20 and 40 s apart); hyper's own install never is.
n=0
until pm_update; do
	n=$((n + 1))
	[ "$n" -lt 4 ] || { echo "FAIL package feeds unreachable"; exit 1; }
	sleep $((10 << (n - 1)))
done
. /etc/openwrt_release
echo "== $DISTRIB_DESCRIPTION ($DISTRIB_ARCH)"
rc=0
ok() { echo "ok   $*"; }
bad() { echo "FAIL $*"; rc=1; }

echo "== install"
pkg="$(ls /src/pkg/* 2>/dev/null | head -n1 || true)"
if [ -n "$pkg" ]; then
	if pm_file "$pkg" >/tmp/install.log 2>&1; then ok "$(basename "$pkg") installs with its dependencies"
	else bad "$(basename "$pkg") does not install:"; tail -n 30 /tmp/install.log; exit 1; fi
	if command -v apk >/dev/null 2>&1; then
		parch="$(apk list -I luci-app-hyper 2>/dev/null | awk 'NR == 1 { print $2 }')"
	else
		parch="$(opkg status luci-app-hyper | sed -n 's/^Architecture: //p')"
	fi
	[ "$parch" = "$DISTRIB_ARCH" ] && ok "package architecture is $parch" || bad "package architecture is '$parch', not $DISTRIB_ARCH"
else
	deps="$(sed -n '/^LUCI_DEPENDS/,/^$/p' /src/Makefile | grep -o '+[A-Za-z0-9_.-]*' | tr -d +)"
	if out="$(pm_try $deps 2>&1)"; then ok "dependencies resolve: $(echo $deps)"
	else bad "dependencies do not resolve:"; echo "$out" | tail -n 20; fi
	pm_add $deps
	cp -R /src/luci-app-hyper/root/. /
	mkdir -p /usr/libexec/hyper/bin && cp /src/bin/* /usr/libexec/hyper/bin/ && chmod +x /usr/libexec/hyper/bin/*
	sh /etc/uci-defaults/luci-hyper
fi
BIN=/usr/libexec/hyper/bin
v="$("$BIN/mihomo" -v)" && ok "$(echo "$v" | head -n1)" || bad "bundled mihomo does not run"
v="$("$BIN/hyperd" version)" && ok "hyperd $v" || bad "bundled hyperd does not run"

echo "== first boot"
[ -n "$(uci -q get hyper.account.device_id)" ] && ok "device id generated" || bad "device id missing"
gid=$(awk -F: '$1 == "hyper" { print $3 }' /etc/group); [ -n "$gid" ] && [ "$gid" != 0 ] && ok "core group hyper exists" || bad "core group hyper missing"
[ "$(uci -q get hyper.config.proxy_mode)" = tproxy ] && [ "$(uci -q get hyper.config.routing_mode)" = smart ] &&
	ok "defaults: smart routing over TProxy" || bad "unexpected defaults"
[ -f /etc/hyper/rule-set/geoip-cn.mrs ] && [ -f /etc/hyper/rule-set/geosite-cn.mrs ] && ok "bundled rule sets" || bad "rule sets missing"

echo "== rpcd and hyperd"
/sbin/ubusd >/dev/null 2>&1 &
sleep 1
/sbin/rpcd >/dev/null 2>&1 &
"$BIN/hyperd" serve >/tmp/hyperd.log 2>&1 &
# Whole seconds: the firmware's busybox sleep takes no fractions.
for _ in $(seq 1 10); do [ -S /var/run/hyper/hyperd.sock ] && break; sleep 1; done
if ubus -t 10 wait_for luci.hyper; then
	n="$(ubus -v list luci.hyper | grep -c '^[[:space:]]*"')"
	[ "$n" = 34 ] && ok "rpcd serves luci.hyper through hyperd ($n methods)" || bad "luci.hyper has $n methods"
	res="$(ubus call luci.hyper checkin_status)" && echo "$res" | grep -q 'not signed in' &&
		ok "checkin_status: $(echo $res)" || bad "checkin_status: $res"
	base="$(ubus call luci.hyper get_config | jsonfilter -e '@.api_base')"
	[ "$base" = 'https://api.cloudcdnhyper.com' ] && ok "API address is selected automatically" || bad "api_base $base"
	[ "$(ubus call luci.hyper get_config | jsonfilter -e '@.config["config.proxy_mode"]')" = tproxy ] &&
		ok "get_config reports tproxy" || bad "get_config proxy_mode"

	# hyperd writes /etc/config/hyper itself: the firmware's uci must read it back.
	ubus call luci.hyper set_mode '{"mode":"dns"}' >/dev/null
	ubus call luci.hyper set_access_control '{"mode":"pick","direct_macs":["AA:BB:CC:DD:EE:01","00:d9:d1:8b:33:e2"]}' >/dev/null
	ubus call luci.hyper set_config '{"values":{"config.proxy_mode":"tun","subscription.auto_update_time":"6"}}' >/dev/null
	[ "$(uci -q get hyper.config.dns_only)" = 1 ] && [ "$(uci -q get hyper.config.proxy_mode)" = tun ] &&
	[ "$(uci -q get hyper.subscription.auto_update_time)" = 6 ] &&
	[ "$(uci -q get hyper.control.lan_direct_mac_addrs)" = 'aa:bb:cc:dd:ee:01 00:d9:d1:8b:33:e2' ] &&
		ok "uci reads what hyperd wrote" || { bad "uci disagrees with hyperd"; uci show hyper; }
	uci set hyper.config.log_level=info && uci commit hyper
	[ "$(ubus call luci.hyper get_config | jsonfilter -e '@.config["config.log_level"]')" = info ] &&
		ok "hyperd reads what uci wrote" || bad "hyperd misses a uci commit"
	[ "$(ubus call luci.hyper set_enabled '{"enabled":true}' | jsonfilter -e '@.error')" = 'not signed in' ] &&
		ok "start refused while signed out" || bad "set_enabled while signed out"

	# The UI polls status every few seconds: the core runs for its version only once,
	# and again only after the binary changes.
	mv "$BIN/mihomo" "$BIN/mihomo.real"
	printf '#!/bin/sh\necho run >> /tmp/core-runs\nexec %s/mihomo.real "$@"\n' "$BIN" > "$BIN/mihomo"
	chmod +x "$BIN/mihomo"
	for _ in 1 2 3; do ubus call luci.hyper status >/dev/null; done
	runs1=$(($(cat /tmp/core-runs 2>/dev/null | wc -l)))
	echo '# changed' >> "$BIN/mihomo"
	ubus call luci.hyper status >/dev/null
	runs2=$(($(cat /tmp/core-runs 2>/dev/null | wc -l)))
	mv "$BIN/mihomo.real" "$BIN/mihomo"
	[ "$runs1" = 1 ] && [ "$runs2" = 2 ] && ok "status runs the core once for its version" ||
		bad "status ran the core $runs1 time(s) for 3 calls, $runs2 after a change (want 1, 2)"

	# check_update runs as a task and records when it ran, even when GitHub is unreachable.
	task="$(ubus -t 5 call luci.hyper check_update | jsonfilter -e '@._hyper_task' || true)"
	[ -n "$task" ] || bad "check_update did not start a task"
	polls=0
	while [ -n "$task" ] && [ "$polls" -lt 185 ]; do
		state="$(ubus -t 5 call luci.hyper task_status "{\"id\":\"$task\"}" | jsonfilter -e '@.state' 2>/dev/null || echo done)"
		[ "$state" = running ] || break
		sleep 1
		polls=$((polls + 1))
	done
	[ -n "$(ubus call luci.hyper status | jsonfilter -e '@.update.checked' || true)" ] &&
		ok "check_update records when it ran" || bad "check_update did not record the check"

	installed="$(ubus call luci.hyper status | jsonfilter -e '@.version' || true)"
	echo "{\"time\": $(date +%s), \"latest\": \"9.9.9\"}" > /var/run/hyper-software-update/check.json
	newer="$(ubus call luci.hyper status | jsonfilter -e '@.update.available' || true)"
	same=false
	if [ -n "$installed" ]; then
		echo "{\"time\": $(date +%s), \"latest\": \"${installed%-r*}\"}" > /var/run/hyper-software-update/check.json
		same="$(ubus call luci.hyper status | jsonfilter -e '@.update.available' || true)"
	fi
	rm -f /var/run/hyper-software-update/check.json
	[ "$newer" = true ] && [ "$same" = false ] && ok "update hint: 9.9.9 is newer, ${installed:-(source tree)} is current" ||
		bad "update hint: newer=$newer, same version ($installed) available=$same"
else
	bad "rpcd does not serve luci.hyper"; cat /tmp/hyperd.log
fi

echo "== network helper on this firmware's nftables and iproute2"
HYPER_IN_NETNS=1 sh /src/tests/network_test.sh || rc=1
exit $rc
INNER

# Reap orphaned test children as the router's procd would.
# shellcheck disable=SC2086
tar -C "$stage" -cf - src | docker run --rm --init -i --cap-add NET_ADMIN ${PLATFORM:+--platform "$PLATFORM"} "$IMAGE" \
	sh -c 'tar -C / -xf - && sh /src/run-inner.sh'
