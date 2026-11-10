#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-only
#
# Fail-open watchdog (fixes the scenario in homeproxy issue #402).
#
# The init script points dnsmasq at sing-box and installs nftables redirects
# before sing-box has actually come up. If sing-box then fails (bad node,
# rule-set download error, crash loop), the whole LAN loses DNS and internet.
#
# This loop asks sing-box's clash_api whether it is alive. After FAIL_LIMIT
# consecutive failures it "fails open": removes the dnsmasq hijack and the
# nftables rules, so the LAN keeps working without the proxy. procd keeps
# respawning sing-box; once it answers again, the service is restarted so the
# rules come back.
#
# Copyright (C) 2026 Hyper Pte. Ltd.

NAME="hyper"
# Overridable for tests (tests/watchdog_test.sh)
RUN_DIR="${RUN_DIR:-/var/run/$NAME}"
INIT_DIR="${INIT_DIR:-/etc/init.d}"
DNSMASQ_ETC="${DNSMASQ_ETC:-/tmp/etc}"
LOG_PATH="$RUN_DIR/$NAME.log"
FAILOPEN_FLAG="$RUN_DIR/failopen"
STATE_FILE="$RUN_DIR/health.json"

INTERVAL="${INTERVAL:-20}"
FAIL_LIMIT="${FAIL_LIMIT:-3}"

log() {
	echo -e "$(date "+%Y-%m-%d %H:%M:%S") [WATCHDOG] $*" >> "$LOG_PATH"
}

# Same lookup as the init script
dnsmasq_dir() {
	local cfg
	cfg="$(uci -q show "dhcp.@dnsmasq[0]" | awk 'NR==1 {split($0, conf, /[.=]/); print conf[2]}')"
	if [ -f "$DNSMASQ_ETC/dnsmasq.conf.$cfg" ]; then
		echo "$(awk -F '=' '/^conf-dir=/ {print $2}' "$DNSMASQ_ETC/dnsmasq.conf.$cfg")/dnsmasq-$NAME.d"
	else
		echo "/tmp/dnsmasq.d/dnsmasq-$NAME.d"
	fi
}

healthy() {
	local port secret
	port="$(uci -q get $NAME.infra.clash_api_port || echo 5334)"
	secret="$(uci -q get $NAME.infra.clash_api_secret)"
	curl -s -o /dev/null -m 3 -w '%{http_code}' -H "Authorization: Bearer $secret" \
		"http://127.0.0.1:$port/version" 2>/dev/null | grep -q '^200$'
}

write_state() {
	printf '{"state":"%s","failures":%s,"time":%s}\n' "$1" "$2" "$(date +%s)" > "$STATE_FILE"
}

fail_open() {
	local dir
	dir="$(dnsmasq_dir)"
	rm -rf "$dir/../dnsmasq-$NAME.conf" "$dir"
	"$INIT_DIR/dnsmasq" restart >/dev/null 2>&1

	for f in fw4_forward.nft fw4_input.nft fw4_post.nft; do
		: > "$RUN_DIR/$f"
	done
	fw4 reload >/dev/null 2>&1

	touch "$FAILOPEN_FLAG"
	log "sing-box is not answering after $FAIL_LIMIT checks: released DNS and firewall rules, LAN is now direct."
}

failures=0
# Give sing-box a moment to start before the first check
sleep "$INTERVAL"

while true; do
	if healthy; then
		if [ -e "$FAILOPEN_FLAG" ]; then
			rm -f "$FAILOPEN_FLAG"
			log "sing-box is answering again, restoring proxy rules."
			write_state recovering 0
			# Detach: the restart replaces this procd instance
			( "$INIT_DIR/$NAME" restart >/dev/null 2>&1 & )
			exit 0
		fi
		[ "$failures" -eq 0 ] || log "sing-box is answering again."
		failures=0
		write_state ok 0
	else
		failures=$((failures + 1))
		if [ ! -e "$FAILOPEN_FLAG" ] && [ "$failures" -ge "$FAIL_LIMIT" ]; then
			fail_open
		fi
		write_state "$([ -e "$FAILOPEN_FLAG" ] && echo failopen || echo degraded)" "$failures"
	fi
	sleep "$INTERVAL"
done
