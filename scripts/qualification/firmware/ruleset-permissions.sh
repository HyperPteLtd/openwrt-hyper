#!/bin/sh
# Booted stock OpenWrt. The host stages the original IPK and its SHA256SUMS in
# /root/candidate and the matching upstream launchable archive, its tag and SHA256SUMS in /root/bundles.
set -eu
trap 'echo HYPER_GUEST_EXIT=$?; echo HYPER_QUALIFICATION_END' EXIT
report() { printf 'HYPER_RESULT %s=%s\n' "$1" "${2:-pass}"; }
call() { if [ "$#" -gt 1 ]; then printf '%s\n' "$2"; else echo '{}'; fi | hyperd call "$1"; }
wait_job() {
 local id result state i
 id=$(jsonfilter -s "$1" -e '@.task')
 [ -n "$id" ]
 for i in $(seq 1 100); do
  result=$(call task "{\"task\":\"$id\"}")
  state=$(jsonfilter -s "$result" -e '@.stage')
  case "$state" in done) return 0;; running) sleep 1;; *) echo "task ended: $result"; return 1;; esac
 done
 return 1
}
. /root/setup-network
opkg print-architecture > /tmp/guest-architectures
cat /tmp/guest-architectures >> /etc/opkg.conf
printf 'arch aarch64_cortex-a53 9\n' >> /etc/opkg.conf
cd /root/candidate
sha256sum -c SHA256SUMS
# The core comes from the archive in /root/bundles. Mark the first-run download as already
# tried, as on a router that has made it, so the manager the package starts does not hold
# the kernel installer's lock with a download of its own.
mkdir -p /etc/hyper/mihomo-kernel
chmod 0711 /etc/hyper /etc/hyper/mihomo-kernel
: >/etc/hyper/mihomo-kernel/.first-install-attempted
opkg install ./luci-app-hyper_*.ipk
report first_postinst
for i in $(seq 1 30); do [ -S /var/run/hyper/manager.sock ] && break; sleep 1; done
uci set hyper.main.method=tproxy
uci set hyper.main.dns=1
uci set hyper.main.ipv6=0
uci commit hyper
tag=$(cat /root/bundles/release-tag)
(cd /root/bundles && sha256sum -c SHA256SUMS)
set -- /root/bundles/*.gz
[ "$#" -eq 1 ] && [ -f "$1" ]
# The installer verifies the original archive against its GitHub release, as for a
# download, and the manager selects it.
hyper-kernel install --tag "$tag" --mode double --bundle /root/bundles --stage-only
wait_job "$(call kernel_write "{\"action\":\"select\",\"args\":{\"tag\":\"$tag\"}}")"
[ -z "$(hyper-kernel status | jsonfilter -e '@.active')" ]
report daemon_official_archive_install
dir=/etc/hyper/imports/rulesets/source-fixture
mkdir -p "$dir"
# Use the two binary fixtures shipped in the exact package being qualified.
cp /usr/share/hyper/rules/geosite-cn.mrs /usr/share/hyper/rules/geoip-cn.mrs "$dir/"
wait_job "$(call subscription_write '{"action":"configure","args":{"config":{"id":"fixture","name":"Rules","kind":"file"}}}')"
json='{"proxies":[{"name":"sample","type":"http","server":"198.18.0.33","port":1080}],"proxy-groups":[{"name":"Proxy","type":"select","proxies":["sample"]}],"rule-providers":{"domains":{"type":"file","behavior":"domain","format":"mrs","path":"./geosite-cn.mrs"},"networks":{"type":"file","behavior":"ipcidr","format":"mrs","path":"./geoip-cn.mrs"}},"rules":["RULE-SET,domains,DIRECT","RULE-SET,networks,DIRECT","MATCH,Proxy"]}'
body=$(CFG="$json" ucode -e 'printf("%J",{action:"refresh",args:{source:"fixture",content:getenv("CFG")}})')
wait_job "$(call subscription_write "$body")"
call source '{"source":"fixture"}'
call mode '{"mode":"smart"}'
began=$(cut -d. -f1 /proc/uptime)
prior=
call start
for i in $(seq 1 150); do
 state=$(call status)
 phase=$(jsonfilter -s "$state" -e '@.phase')
 if [ "$phase" != "$prior" ]; then echo "lifecycle phase=$phase"; prior=$phase; fi
 [ "$phase" != running ] || break
 if [ "$phase" = fail_open ] || [ "$phase" = release_failed ]; then echo "$state"; exit 1; fi
 sleep 1
done
[ "$phase" = running ] || { echo "$state"; exit 1; }
elapsed=$(( $(cut -d. -f1 /proc/uptime)-began ))
[ "$elapsed" -ge 60 ]
report daemon_first_confirm_s "$elapsed"
# Rule sets are private to root, which the core runs as.
[ "$(ls -ld /etc/hyper/rulesets | cut -c1-10)" = drwx------ ]
count=0
for f in /etc/hyper/rulesets/*.mrs; do
 [ -f "$f" ] && [ "$(ls -l "$f" | cut -c1-10)" = -rw------- ]
 count=$((count+1))
done
[ "$count" -eq 2 ]
report ruleset_watch_permissions
p=$(pidof mihomo)
[ -n "$p" ]
# The core runs as root:nogroup alone with every capability, like OpenClash's: nogroup is
# base-files' 65534, its bounding set is procd's, filled into the permitted and effective
# sets, and no_new_privs stays unset.
gid=$(awk -F: '$1 == "nogroup" { print $3 }' /etc/group)
[ "$gid" = 65534 ]
bounding=$(awk '$1 == "CapBnd:" { print $2 }' /proc/1/status)
[ -n "$bounding" ]
awk -v gid="$gid" -v bnd="$bounding" '
 $1 == "Uid:" { ok += $2 == 0 && $3 == 0 && $4 == 0 && $5 == 0 }
 $1 == "Gid:" { ok += $2 == gid && $3 == gid && $4 == gid && $5 == gid }
 $1 == "Groups:" { ok += NF == 1 }
 $1 ~ /^Cap(Prm|Eff|Bnd):$/ { ok += $2 == bnd }
 $1 == "NoNewPrivs:" { ok += $2 == 0 }
 END { exit ok != 7 }' "/proc/$p/status"
# And 1000000 open files, soft and hard, as OpenClash's core has.
awk '$1 == "Max" && $2 == "open" && $3 == "files" { ok = $4 == 1000000 && $5 == 1000000 } END { exit !ok }' "/proc/$p/limits"
report core_identity
# Confirm the running core, under its real UID, loaded both providers.
key=$(cat /etc/hyper/private/control.token)
uclient-fetch -qO /tmp/qualify-providers --header="Authorization: Bearer $key" http://127.0.0.1:19761/providers/rules
unset key
for provider in domains networks; do
 count=$(jsonfilter -i /tmp/qualify-providers -e "@.providers.$provider.ruleCount")
 [ "${count:-0}" -gt 0 ]
done
rm -f /tmp/qualify-providers
report live_rule_providers
call logs '{"limit":100,"source":"core"}' | ucode -lfs -e 'let e=json(fs.stdin.read("all"));for(let item in e.items)if(match(item.message,/permission_denied|_permission$/))exit(1);'
report core_permission_logs
nslookup -retry=1 -timeout=1 -type=A probe.hyper.invalid 192.168.41.1 | grep -q 192.0.2.1
report tproxy_dns_takeover
call stop
for i in $(seq 1 30); do [ ! -e /var/run/hyper/network/owned ] && ! pidof mihomo >/dev/null && break; sleep 1; done
[ ! -e /var/run/hyper/network/owned ] && ! pidof mihomo >/dev/null
nslookup -retry=1 -timeout=1 -type=A localhost 192.168.41.1 >/dev/null
report tproxy_dns_restored
report daemon_lifecycle
