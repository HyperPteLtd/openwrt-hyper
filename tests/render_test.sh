#!/bin/sh
# Render the router-side generators from a UCI fixture on a PC:
#   - generate_client.uc -> sing-box config, then `sing-box check` (if sing-box is installed)
#   - firewall_post.ut   -> nftables rules, with and without proxy_router_self
#
#   UCODE=... UCODE_LIB=... tests/render_test.sh

set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
SCRIPTS="$ROOT/luci-app-hyper/root/etc/hyper/scripts"
UCODE="${UCODE:-ucode}"
UCODE_LIB="${UCODE_LIB:-/usr/lib/ucode}"
FIXTURE="$HERE/fixtures/uci-basic.json"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
SCR="$TMP/scripts"
mkdir -p "$SCR" "$TMP/run" "$TMP/etc-hyper"
cp -R "$HERE/fixtures/rules" "$TMP/etc-hyper/rules"
cp -R "$ROOT/luci-app-hyper/root/etc/hyper/rule-set" "$TMP/etc-hyper/rule-set"
cp -R "$ROOT/luci-app-hyper/root/etc/hyper/resources" "$TMP/etc-hyper/resources"
for f in "$SCRIPTS"/*.uc "$SCRIPTS"/*.ut; do
	sed -e "s#/var/run/hyper#$TMP/run#g" -e "s#/etc/hyper/scripts/#$SCR/#g" \
	    -e "s#HP_DIR = '/etc/hyper'#HP_DIR = '$TMP/etc-hyper'#" \
	    -e "s#/sbin/validate_data#$HERE/stubs/validate_data#g" "$f" > "$SCR/$(basename "$f")"
done

run() {
	HYPER_UCI_FIXTURE="$1"; shift
	HYPER_UCI_FIXTURE="$HYPER_UCI_FIXTURE" "$UCODE" -S -L "$HERE/stubs" -L "$SCR" -L "$UCODE_LIB" "$@"
}

fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got [$2] want [$3]"; fail=1; fi; }

# sing-box check of a rendered config; on macOS the Linux-only fields are stripped first
singbox_check() {
	conf="$1"; what="$2"
	if ! command -v sing-box >/dev/null 2>&1; then
		echo "skip sing-box check, $what (sing-box not installed)"
		return
	fi
	check_conf="$conf"
	if [ "$(uname -s)" != "Linux" ]; then
		# routing_mark and redirect/tproxy inbounds only exist on Linux
		check_conf="$TMP/sing-box-host.json"
		node "$HERE/render_config.mjs" host "$conf" "$check_conf" || fail=1
	fi
	if out="$(sing-box check -c "$check_conf" 2>&1)"; then
		echo "ok   sing-box check, $what ($(sing-box version | head -1)$( [ "$check_conf" = "$conf" ] || echo ', Linux-only fields stripped'))"
	else
		echo "FAIL sing-box check, $what:"; echo "$out" | sed 's/^/     /'; fail=1
	fi
}

echo "== generate_client.uc"
if run "$FIXTURE" "$SCR/generate_client.uc"; then
	CONF="$TMP/run/sing-box-c.json"
	node "$HERE/render_config.mjs" basic "$CONF" || fail=1
	singbox_check "$CONF" "basic fixture"
	if command -v sing-box >/dev/null 2>&1; then
		node "$HERE/core_start_test.mjs" "$CONF" || fail=1
	fi
else
	echo "FAIL generate_client.uc did not run"; fail=1
fi

echo "== TUN configuration (current core stack)"
TUN="$TMP/uci-tun.json"
node "$HERE/render_config.mjs" fixture-tun "$FIXTURE" "$TUN" || fail=1
rm -f "$TMP/run/sing-box-c.json"
if run "$TUN" "$SCR/generate_client.uc"; then
	CONF="$TMP/run/sing-box-c.json"
	node "$HERE/render_config.mjs" tun "$CONF" || fail=1
	singbox_check "$CONF" "TUN"
else
	echo "FAIL generate_client.uc with TUN"; fail=1
fi
if run "$TUN" -T "$SCR/firewall_post.ut" > "$TMP/fw-tun.nft"; then
	check "TUN: core connections bypass before interception" "$(sed -n '/^chain hyper_mangle_tun {/{n;p;}' "$TMP/fw-tun.nft" | xargs)" "meta mark 100 counter return"
else
	echo "FAIL firewall_post.ut render (TUN)"; fail=1
fi

echo "== custom DHCP DNS (router build feature)"
DHCP="$TMP/uci-dhcp.json"
node "$HERE/render_config.mjs" fixture-dhcp "$FIXTURE" "$DHCP" || fail=1
rm -f "$TMP/run/sing-box-c.json"
if run "$DHCP" "$SCR/generate_client.uc"; then
	CONF="$TMP/run/sing-box-c.json"
	node "$HERE/render_config.mjs" dhcp "$CONF" || fail=1
	singbox_check "$CONF" "custom DHCP DNS"
else
	echo "FAIL generate_client.uc with custom DHCP DNS"; fail=1
fi

echo "== share links of every protocol -> snapshot.uc -> generate_client.uc"
LINKS="$TMP/uci-links.json"
if summary="$(run "$FIXTURE" "$HERE/links_fixture.uc" "$HERE/fixtures/links.txt" "$FIXTURE" "$LINKS")"; then
	node "$HERE/render_config.mjs" links-summary "$summary" || fail=1
	rm -f "$TMP/run/sing-box-c.json"
	if run "$LINKS" "$SCR/generate_client.uc"; then
		CONF="$TMP/run/sing-box-c.json"
		node "$HERE/render_config.mjs" links "$CONF" || fail=1
		singbox_check "$CONF" "every protocol"
	else
		echo "FAIL generate_client.uc with the share-link fixture"; fail=1
	fi
else
	echo "FAIL links_fixture.uc"; fail=1
fi

echo "== DNS protection only (config.dns_only=1)"
DNSONLY="$TMP/uci-dnsonly.json"
node "$HERE/render_config.mjs" fixture-dnsonly "$FIXTURE" "$DNSONLY" || fail=1
rm -f "$TMP/run/sing-box-c.json"
if run "$DNSONLY" "$SCR/generate_client.uc"; then
	CONF="$TMP/run/sing-box-c.json"
	node "$HERE/render_config.mjs" dnsonly "$CONF" || fail=1
	singbox_check "$CONF" "dns only"
else
	echo "FAIL generate_client.uc with dns_only"; fail=1
fi
if run "$DNSONLY" -T "$SCR/firewall_post.ut" > "$TMP/fw-dnsonly.nft"; then
	check "dns-only: no nft rules" "$(grep -c 'chain\|set ' "$TMP/fw-dnsonly.nft")" "0"
else
	echo "FAIL firewall_post.ut render (dns_only)"; fail=1
fi

echo "== firewall_post.ut"
NOSELF="$TMP/uci-noself.json"
node "$HERE/render_config.mjs" fixture-noself "$FIXTURE" "$NOSELF" || fail=1

if run "$FIXTURE" -T "$SCR/firewall_post.ut" > "$TMP/fw-self.nft"; then
	check "self: output redirect present" "$(grep -c 'chain hyper_output_redir' "$TMP/fw-self.nft")" "1"
	check "self: mangle_output present"   "$(grep -c 'chain mangle_output' "$TMP/fw-self.nft")" "1"
	check "direct mac excluded"           "$(grep -c 'ether saddr { 00:d9:d1:8b:33:e2 } counter return' "$TMP/fw-self.nft")" "2"
else
	echo "FAIL firewall_post.ut render (proxy_router_self=1)"; fail=1
fi
if run "$NOSELF" -T "$SCR/firewall_post.ut" > "$TMP/fw-noself.nft"; then
	check "noself: output redirect gone"  "$(grep -c 'chain hyper_output_redir' "$TMP/fw-noself.nft")" "0"
	check "noself: mangle_output gone"    "$(grep -c 'chain mangle_output' "$TMP/fw-noself.nft")" "0"
	check "noself: LAN redirect kept"     "$(grep -c 'chain hyper_redirect {' "$TMP/fw-noself.nft")" "1"
else
	echo "FAIL firewall_post.ut render (proxy_router_self=0)"; fail=1
fi
# Missing china_ip lists (failed resource update) must still give valid rules
mv "$TMP/etc-hyper/resources" "$TMP/etc-hyper/resources.off"
if run "$FIXTURE" -T "$SCR/firewall_post.ut" > "$TMP/fw-nolists.nft"; then
	check "no lists: mainland set kept, empty" "$(grep -c 'set hyper_mainland_addr_v4 {' "$TMP/fw-nolists.nft")" "1"
	check "no lists: no element list"          "$(grep -A4 'set hyper_mainland_addr_v4 {' "$TMP/fw-nolists.nft" | grep -c 'elements')" "0"
else
	echo "FAIL firewall_post.ut render (no resource lists)"; fail=1
fi
mv "$TMP/etc-hyper/resources.off" "$TMP/etc-hyper/resources"

# fw4 includes the file at "table-post", i.e. inside its table: check it the same way.
# nft -c talks to the kernel, so it needs root (CI runners have passwordless sudo).
NFT=nft
[ "$(id -u)" = 0 ] || NFT="sudo -n nft"
nft_check() {
	if ! command -v nft >/dev/null 2>&1 || ! $NFT --version >/dev/null 2>&1; then
		echo "skip nft -c, $2 (nft not installed or no root)"
		return
	fi
	{ echo 'table inet fw4 {'; cat "$1"; echo '}'; } > "$1.table"
	if $NFT -c -f "$1.table"; then echo "ok   nft -c, $2"; else echo "FAIL nft -c, $2"; fail=1; fi
}
nft_check "$TMP/fw-self.nft" "proxy_router_self=1"
nft_check "$TMP/fw-noself.nft" "proxy_router_self=0"
nft_check "$TMP/fw-nolists.nft" "no resource lists"
nft_check "$TMP/fw-tun.nft" "TUN"

exit $fail
