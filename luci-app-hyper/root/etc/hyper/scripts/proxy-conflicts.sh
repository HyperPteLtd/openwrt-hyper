#!/bin/sh
# Stop only recognized, active proxy services before Hyper takes over networking.
# Sources for service names / owned paths:
# vernesong/OpenClash: luci-app-openclash/root/etc/init.d/openclash
# Openwrt-Passwall/openwrt-passwall{,2}: root/etc/init.d/passwall{,2}
# fw876/helloworld: luci-app-ssr-plus/root/etc/init.d/shadowsocksr
# immortalwrt/homeproxy: root/etc/init.d/homeproxy
# nikkinikki-org/OpenWrt-nikki: nikki/files/nikki.init (v1.0.1: mihomo/files/mihomo.init)
# nikkinikki-org/OpenWrt-momo: momo/files/momo.init
# openwrt/packages: net/v2raya/files/v2raya.init

umask 077
[ -n "$HYPER_CONFLICT_INIT" ] || HYPER_CONFLICT_INIT=/etc/init.d
[ -n "$HYPER_CONFLICT_PROC" ] || HYPER_CONFLICT_PROC=/proc
[ -n "$HYPER_CONFLICT_TMP" ] || HYPER_CONFLICT_TMP=/var/run
[ -n "$HYPER_CONFLICT_DNS_CONFIG" ] || HYPER_CONFLICT_DNS_CONFIG=/tmp/etc
[ -n "$HYPER_CONFLICT_LIMIT" ] || HYPER_CONFLICT_LIMIT=30
case "$HYPER_CONFLICT_LIMIT" in ''|*[!0-9]*) exit 1 ;; esac
[ "$HYPER_CONFLICT_LIMIT" -ge 1 ] && [ "$HYPER_CONFLICT_LIMIT" -le 120 ] || exit 1
SERVICES="openclash passwall passwall2 shadowsocksr homeproxy nikki mihomo momo v2raya"
RUN_SESSION="$(dirname "$0")/run-session.sh"

log() {
	printf '%s [CONFLICT] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >&2
}

work=$(mktemp -d "$HYPER_CONFLICT_TMP/hyper-conflicts.XXXXXX") || exit 1
job=
group=
cleanup_job() {
	# This group contains only the stop command we launched, not the service's
	# pre-existing processes. Do not leave a blocked stop to run after takeover.
	if [ -n "$group" ]; then
		kill -TERM "-$group" 2>/dev/null || :
		sleep 1
		kill -KILL "-$group" 2>/dev/null || :
	fi
	[ -z "$job" ] || wait "$job" 2>/dev/null || :
	job=
	group=
}
cleanup() {
	cleanup_job
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

still_enabled() {
	[ "$(uci -q get hyper.config.enabled 2>/dev/null)" = 1 ]
}

snapshot() {
	# A failed state query must not be mistaken for an idle proxy.
	if ! ubus -t 2 call service list >"$work/services" 2>/dev/null ||
		! jsonfilter -i "$work/services" -e '@' >/dev/null 2>&1; then
		log "Cannot inspect running services; Hyper was not started"
		return 1
	fi
	if ! nft -t list ruleset >"$work/nft" 2>/dev/null; then
		log "Cannot inspect firewall ownership; Hyper was not started"
		return 1
	fi
	: >"$work/iptables"
	for tool in iptables-save ip6tables-save; do
		# Legacy tables may be unavailable even when the optional binary exists.
		command -v "$tool" >/dev/null 2>&1 || continue
		"$tool" >>"$work/iptables" 2>/dev/null || :
	done
	: >"$work/processes"
	for cmdline in "$HYPER_CONFLICT_PROC"/[0-9]*/cmdline; do
		[ -r "$cmdline" ] || continue
		tr '\000' '\n' <"$cmdline" >>"$work/processes" 2>/dev/null || :
		printf '\n' >>"$work/processes"
	done
	# Inspect only directories referenced by dnsmasq's generated runtime config.
	# Files in an installed plugin's config directory are not evidence of use.
	: >"$work/dns"
	for config in "$HYPER_CONFLICT_DNS_CONFIG"/dnsmasq.conf.*; do
		[ -r "$config" ] || continue
		awk -F= '/^conf-dir=/ { sub(/,.*/, "", $2); print $2 }' "$config"
	done >"$work/dns-dirs"
	while IFS= read -r dir; do
		[ -d "$dir" ] || continue
		for owner in homeproxy passwall passwall2; do
			if [ -s "$dir/dnsmasq-$owner.conf" ] &&
				grep -Eq '^[[:space:]]*(conf-dir|server|no-resolv|no-poll)($|=)' "$dir/dnsmasq-$owner.conf"; then
				printf '%s\n' "$owner" >>"$work/dns"
			fi
		done
	done <"$work/dns-dirs"
}

active() {
	local service="$1" path chain table instances
	case "$service" in
		openclash)
			path='/(etc/openclash/(clash|core/clash[^/[:space:]]*)|usr/share/openclash/openclash_watchdog.sh)([[:space:]]|$)'
			chain='openclash([_[:space:]"]|$)'; table='openclash' ;;
		passwall|passwall2)
			path="/(tmp|var)/etc/$service/bin/|/usr/share/$service/monitor.sh([[:space:]]|$)"
			chain=PSW; [ "$service" != passwall2 ] || chain=PSW2
			chain="$chain([_[:space:]\"]|$)"; table="$service" ;;
		shadowsocksr)
			path='/(var|tmp)/etc/ssrplus/|/usr/bin/ssr-(monitor|switch)'
			chain='(SSR|SS_SPEC)([_[:space:]"-]|$)'; table='ss_spec(_mangle)?' ;;
		homeproxy)
			# Server-only HomeProxy and its log cleaner do not proxy LAN traffic.
			path='/(var|tmp)/run/homeproxy/sing-box-c.json'
			chain='homeproxy_'; table='homeproxy' ;;
		nikki|mihomo|momo)
			path="/etc/$service/run([/[:space:]]|$)"
			chain="$service([_[:space:]\"]|$)"; table="$service" ;;
		v2raya)
			path='/usr/bin/v2raya([[:space:]]|$)|/etc/v2raya/'
			chain='V2RAYA([_[:space:"]-]|$)'; table='v2raya' ;;
	esac
	if [ "$service" = homeproxy ]; then
		instances=$(jsonfilter -i "$work/services" -e '@.homeproxy.instances["sing-box-c"].running' 2>/dev/null)
	else
		instances=$(jsonfilter -i "$work/services" -e "@[\"$service\"].instances[*].running" 2>/dev/null)
	fi
	[ "$service" != openclash ] || instances="$instances
$(jsonfilter -i "$work/services" -e '@["openclash-watchdog"].instances[*].running' 2>/dev/null)"
	printf '%s\n' "$instances" | grep -qx true && return 0
	grep -qx "$service" "$work/dns" && return 0
	grep -Eq "(^|[[:space:]])($path)" "$work/processes" && return 0
	# Runtime rules count even after a core has crashed. Saved config files,
	# UCI enabled flags and boot symlinks alone never count as active.
	grep -Eq "^[[:space:]]*table (inet|ip|ip6) \"?($table)\"? \{|(jump|goto) \"?$chain" "$work/nft" && return 0
	grep -Eq -- "(-j|-g) $chain" "$work/iptables"
}

stop_service() {
	local service="$1" elapsed=0 result
	if [ ! -x "$HYPER_CONFLICT_INIT/$service" ]; then
		log "$service still owns proxy processes or rules but has no stop script; Hyper was not started"
		return 1
	fi
	log "Stopping active proxy service: $service"
	rm -f "$work/group" "$work/result"
	# Close service_control's inherited lock FD. The isolated session lets us
	# cancel an init script blocked on a plugin's own flock without killing
	# unrelated sing-box / mihomo / DNS processes.
	sh "$RUN_SESSION" sh -c '
		printf "%s\n" "$$" >"$1/group"
		"$2" stop
		result=$?
		printf "%s\n" "$result" >"$1/result"
		exit "$result"
	' sh "$work" "$HYPER_CONFLICT_INIT/$service" 9>&- >"$work/stop.log" 2>&1 &
	job=$!
	while :; do
		if [ -s "$work/group" ]; then
			read -r group <"$work/group"
			case "$group" in ''|*[!0-9]*) group=; return 1 ;; esac
			[ "$group" -gt 1 ] || { group=; return 1; }
		fi
		[ ! -f "$work/result" ] || break
		if ! kill -0 "$job" 2>/dev/null || [ "$elapsed" -ge "$HYPER_CONFLICT_LIMIT" ]; then
			log "Timed out or failed stopping $service; Hyper was not started"
			cleanup_job
			return 1
		fi
		sleep 1
		elapsed=$((elapsed + 1))
	done
	read -r result <"$work/result"
	wait "$job" 2>/dev/null || :
	job=
	if [ "$result" != 0 ]; then
		if [ "$service" = homeproxy ]; then
			# Upstream service_stopped unconditionally deletes the TUN device.
			# In TProxy mode that returns nonzero after successful cleanup.
			# Accept this known quirk only after all runtime ownership is gone.
			log "homeproxy stop returned $result; verifying process, DNS and firewall cleanup"
		else
			log "$service stop failed (exit $result); Hyper was not started"
			cleanup_job
			return 1
		fi
	fi
	# Some init scripts finish before their managed processes and cleanup jobs.
	while :; do
		snapshot || return 1
		if ! active "$service" && ! kill -0 "-$group" 2>/dev/null; then
			# Recheck once to catch a surviving watchdog / immediate respawn.
			sleep 1
			snapshot || return 1
			if ! active "$service" && ! kill -0 "-$group" 2>/dev/null; then
				group=
				log "Stopped proxy service: $service"
				return 0
			fi
		fi
		if [ "$elapsed" -ge "$HYPER_CONFLICT_LIMIT" ]; then
			log "$service still owns proxy processes or rules after stop; Hyper was not started"
			cleanup_job
			return 1
		fi
		sleep 1
		elapsed=$((elapsed + 1))
	done
}

still_enabled || exit 1
snapshot || exit 1
for service in $SERVICES; do
	still_enabled || { log "Start cancelled"; exit 1; }
	active "$service" || continue
	stop_service "$service" || exit 1
done
# A stop request or another plugin's watchdog may have arrived while waiting.
still_enabled || { log "Start cancelled"; exit 1; }
snapshot || exit 1
for service in $SERVICES; do
	if active "$service"; then
		log "$service became active again; Hyper was not started"
		exit 1
	fi
done
exit 0
