#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Render the core configs (internal/mihomo) and check them with the bundled core:
#   1. `mihomo -t` on every variant (smart, global, TUN, DNS only, fake-ip, listed_only),
#      with the packaged .mrs rule sets
#   2. start the core with the smart and TUN configs in a network namespace: the controller
#      answers, the rule providers load, the listeners bind
#
#   MIHOMO=/path/to/mihomo tests/render_test.sh      (default: mihomo on PATH)
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
MIHOMO="${MIHOMO:-$(command -v mihomo || :)}"
[ -x "$MIHOMO" ] || { echo "FAIL no mihomo binary (set MIHOMO)"; exit 1; }
MIHOMO="$(cd "$(dirname "$MIHOMO")" && pwd)/$(basename "$MIHOMO")"
BUILTIN="$ROOT/luci-app-hyper/root/etc/hyper/rule-set"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0

echo "== mihomo -t ($("$MIHOMO" -v | head -1))"
if (cd "$ROOT" && HYPER_TEST_MIHOMO="$MIHOMO" HYPER_TEST_BUILTIN="$BUILTIN" HYPER_TEST_DUMP="$TMP/dump" \
	go test -count=1 ./internal/mihomo); then
	echo "ok   every rendered variant passes mihomo -t"
else
	echo "FAIL rendered config rejected by mihomo"; fail=1
fi

# A network namespace lets the core bind its TProxy socket and TUN device without
# touching the host. Unprivileged user namespaces first, root otherwise (CI).
if unshare -rn true 2>/dev/null; then NS="unshare -rn"
elif sudo -n unshare -n true 2>/dev/null; then NS="sudo -n unshare -n"
else NS=""; fi

live() {
	name="$1"; dir="$TMP/dump/$name"
	[ -f "$dir/config.yaml" ] || { echo "FAIL $name: no rendered config"; fail=1; return; }
	ctl="$(sed -n 's/^external-controller: //p' "$dir/config.yaml")"
	secret="$(sed -n 's/^secret: //p' "$dir/config.yaml")"
	if $NS sh -s "$MIHOMO" "$dir" "$ctl" "$secret" <<'EOF'
set -u
ip link set lo up
"$1" -d "$2" -f "$2/config.yaml" >"$2/run.log" 2>&1 &
pid=$!
ok=1
for _ in $(seq 1 50); do
	if wget -q -O "$2/version" --header "Authorization: Bearer $4" "http://$3/version" 2>/dev/null ||
	   curl -fsS -o "$2/version" -H "Authorization: Bearer $4" "http://$3/version" 2>/dev/null; then ok=0; break; fi
	sleep 0.2
done
if [ "$ok" = 0 ]; then
	# The core loads rule providers after its controller answers: wait for geosite-cn.
	for _ in $(seq 1 25); do
		curl -fsS -H "Authorization: Bearer $4" "http://$3/providers/rules" >"$2/providers" 2>/dev/null ||
			wget -q -O "$2/providers" --header "Authorization: Bearer $4" "http://$3/providers/rules"
		grep -Eq '"geosite-cn":\{[^}]*"ruleCount":[1-9]' "$2/providers" && break
		sleep 0.2
	done
	cat /proc/net/tcp /proc/net/tcp6 /proc/net/udp /proc/net/udp6 >"$2/sockets" 2>/dev/null
fi
kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
exit "$ok"
EOF
	then
		echo "ok   $name: core started, controller answers"
		count="$(sed -n 's/.*"geosite-cn":{[^}]*"ruleCount":\([0-9]*\).*/\1/p' "$dir/providers")"
		if [ "${count:-0}" -gt 1000 ]; then echo "ok   $name: bundled geosite-cn loaded ($count rules)"
		else echo "FAIL $name: geosite-cn not loaded"; fail=1; fi
		# 5333 = 0x14D5 (DNS), 5332 = 0x14D4 (TProxy) in /proc/net listings
		if grep -q ':14D5 ' "$dir/sockets"; then echo "ok   $name: DNS listener"; else echo "FAIL $name: no DNS listener"; fail=1; fi
		if [ "$name" = TestRenderSmart ]; then
			if grep -q ':14D4 ' "$dir/sockets"; then echo "ok   $name: TProxy listener"; else echo "FAIL $name: no TProxy listener"; fail=1; fi
		fi
	else
		echo "FAIL $name: core did not start"; sed 's/^/     /' "$dir/run.log" | tail -20; fail=1
	fi
}

echo "== core startup"
if [ -z "$NS" ]; then
	echo "skip core startup (needs unprivileged user namespaces or sudo)"
else
	live TestRenderSmart
	live TestRenderTUN
fi

exit "$fail"
