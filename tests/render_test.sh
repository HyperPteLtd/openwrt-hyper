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
		python3 -c "
import json, sys
c = json.load(open(sys.argv[1]))
def strip(o):
    if isinstance(o, dict):
        o.pop('routing_mark', None)
        for v in o.values(): strip(v)
    elif isinstance(o, list):
        for v in o: strip(v)
strip(c)
c['inbounds'] = [i for i in c['inbounds'] if i['type'] not in ('redirect', 'tproxy')]
json.dump(c, open(sys.argv[2], 'w'))
" "$conf" "$check_conf"
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
	q() { python3 -c "import json,sys; c=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))" "$CONF" "$1"; }
	check "selector exists"     "$(q "[o['type'] for o in c['outbounds'] if o['tag']=='main-out'][0]")" "selector"
	check "selector members"    "$(q "sorted([o for o in c['outbounds'] if o['tag']=='main-out'][0]['outbounds'])")" "['cfg-pp_anytls-out', 'cfg-pp_hk-out', 'cfg-pp_sg-out', 'cfg-pp_tokyo-out']"
	check "selector default"    "$(q "[o for o in c['outbounds'] if o['tag']=='main-out'][0]['default']")" "cfg-pp_tokyo-out"
	check "clash_api loopback"  "$(q "c['experimental']['clash_api']['external_controller']")" "127.0.0.1:5334"
	check "clash_api secret"    "$(q "len(c['experimental']['clash_api']['secret'])")" "48"
	check "cache_file enabled"  "$(q "c['experimental']['cache_file']['enabled']")" "True"
	check "reality pbk"         "$(q "[o for o in c['outbounds'] if o['tag']=='cfg-pp_tokyo-out'][0]['tls']['reality']['public_key']")" "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"
	check "anytls outbound"     "$(q "(lambda o: [o['type'], o['server_port'], o['tls']['server_name'], o['tls']['insecure'], o['idle_session_timeout'], o['min_idle_session']])([o for o in c['outbounds'] if o['tag']=='cfg-pp_anytls-out'][0])")" "['anytls', 8443, 'cdn.example.com', True, '30s', 5]"
	check "trojan ws path"      "$(q "[o for o in c['outbounds'] if o['tag']=='cfg-pp_sg-out'][0]['transport']['path']")" "/ws"
	check "final is main-out"   "$(q "c['route']['final']")" "main-out"
	check "server rule sets"    "$(q "sorted(r['tag'] for r in c['route']['rule_set'] if r['tag'].startswith('hyper-rule-'))")" "['hyper-rule-cn-direct-domain', 'hyper-rule-streaming-domain', 'hyper-rule-streaming-ip']"
	check "disabled rule left out" "$(q "any('off' in r['tag'] for r in c['route']['rule_set'])")" "False"
	check "direct rule + geoip" "$(q "[r for r in c['route']['rules'] if 'hyper-rule-cn-direct-domain' in r.get('rule_set', [])][0]")" "{'rule_set': ['hyper-rule-cn-direct-domain', 'geoip-cn'], 'action': 'route', 'outbound': 'direct-out'}"
	check "proxy rule"          "$(q "[r for r in c['route']['rules'] if 'hyper-rule-streaming-domain' in r.get('rule_set', [])][0]['outbound']")" "main-out"
	check "dns follows policy"  "$(q "[(r['rule_set'], r['server']) for r in c['dns']['rules'] if str(r.get('rule_set','')).startswith('hyper-rule')]")" "[('hyper-rule-cn-direct-domain', 'china-dns'), ('hyper-rule-streaming-domain', 'main-dns')]"
	check "rules before geosite" "$(q "[str(r.get('rule_set')) for r in c['dns']['rules']].index('hyper-rule-cn-direct-domain') < [str(r.get('rule_set')) for r in c['dns']['rules']].index('geosite-cn')")" "True"
	singbox_check "$CONF" "basic fixture"
else
	echo "FAIL generate_client.uc did not run"; fail=1
fi

echo "== share links of every protocol -> snapshot.uc -> generate_client.uc"
LINKS="$TMP/uci-links.json"
if summary="$(run "$FIXTURE" "$HERE/links_fixture.uc" "$HERE/fixtures/links.txt" "$FIXTURE" "$LINKS")"; then
	lq() { python3 -c "import json,sys; s=json.loads(sys.argv[1]); print(eval(sys.argv[2]))" "$summary" "$1"; }
	check "links parsed"      "$(lq "s['nodes']")" "20"
	check "protocols"         "$(lq "sorted(set(s['types']))")" "['anytls', 'http', 'hysteria', 'hysteria2', 'shadowsocks', 'socks', 'trojan', 'tuic', 'vless', 'vmess', 'wireguard']"
	check "unsupported kept out" "$(lq "s['unsupported']")" "['kcp transport', 'ssr://']"
	rm -f "$TMP/run/sing-box-c.json"
	if run "$LINKS" "$SCR/generate_client.uc"; then
		CONF="$TMP/run/sing-box-c.json"
		q() { python3 -c "import json,sys; c=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))" "$CONF" "$1"; }
		check "selector has all"  "$(q "len([o for o in c['outbounds'] if o['tag']=='main-out'][0]['outbounds'])")" "20"
		check "wireguard endpoint" "$(q "[e['type'] for e in c.get('endpoints', [])]")" "['wireguard']"
		check "vmess early data"  "$(q "[(o['transport']['path'], o['transport']['max_early_data']) for o in c['outbounds'] if o.get('type')=='vmess' and 'transport' in o][0]")" "('/vm', 2048)"
		singbox_check "$CONF" "every protocol"
	else
		echo "FAIL generate_client.uc with the share-link fixture"; fail=1
	fi
else
	echo "FAIL links_fixture.uc"; fail=1
fi

echo "== DNS protection only (config.dns_only=1)"
DNSONLY="$TMP/uci-dnsonly.json"
python3 -c "import json,sys; d=json.load(open(sys.argv[1])); d['hyper']['config']['dns_only']='1'; json.dump(d, open(sys.argv[2],'w'))" "$FIXTURE" "$DNSONLY"
rm -f "$TMP/run/sing-box-c.json"
if run "$DNSONLY" "$SCR/generate_client.uc"; then
	CONF="$TMP/run/sing-box-c.json"
	q() { python3 -c "import json,sys; c=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))" "$CONF" "$1"; }
	check "dns-only inbounds"   "$(q "sorted(i['tag'] for i in c['inbounds'])")" "['dns-in', 'mixed-in']"
	check "dns-only: dns via proxy" "$(q "[s.get('detour') for s in c['dns']['servers'] if s['tag']=='main-dns'][0]")" "main-out"
	check "dns-only: cn split kept" "$(q "any(r.get('server')=='china-dns' for r in c['dns']['rules'])")" "True"
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
python3 -c "import json,sys; d=json.load(open(sys.argv[1])); d['hyper']['control']['proxy_router_self']='0'; json.dump(d, open(sys.argv[2],'w'))" "$FIXTURE" "$NOSELF"

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

exit $fail
