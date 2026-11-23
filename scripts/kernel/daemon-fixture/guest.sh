#!/bin/sh
# Starts after the official firmware harness initializes the firewall, LAN and WAN.
set -eu
trap 'rc=$?; echo "HYPER_GUEST_EXIT=$rc"; echo HYPER_QUALIFICATION_END' EXIT
report() { printf 'HYPER_RESULT %s=%s\n' "$1" "$2"; }
call() { if [ "$#" -gt 1 ]; then printf '%s' "$2"; else printf '{}'; fi | /usr/sbin/hyperd call "$1"; }
wait_job() {
 local id i result stage
 id=$(jsonfilter -s "$1" -e '@.task')
 [ -n "$id" ] || { echo "missing task ID"; return 1; }
 for i in $(seq 1 240); do
  result=$(call task "{\"task\":\"$id\"}")
  stage=$(jsonfilter -s "$result" -e '@.stage')
  case "$stage" in done) return 0 ;; failed) echo "$result"; return 1 ;; esac
  sleep 1
 done
 return 1
}
install_bundle() {
 local tag dir
 tag=$1
 # Only the original archive, as copied to a router by hand: the installer checks it
 # against the release metadata it pinned for this tag. The manager then selects it.
 dir=/tmp/original-archive
 mkdir -m 0700 "$dir"
 cp /root/bundles/"$tag"/mihomo-"${tag#v}"-linux-arm64.tar.gz "$dir/"
 hyper-kernel install --tag "$tag" --mode double --bundle "$dir" --stage-only
 rm -r "$dir"
 wait_job "$(call kernel_write "{\"action\":\"select\",\"args\":{\"tag\":\"$tag\"}}")"
}
wait_phase() {
 phase=$1;limit=$2;prior=
 for i in $(seq 1 "$limit"); do
  state=$(call status)
  actual=$(jsonfilter -s "$state" -e '@.phase')
  if [ "$actual" != "$prior" ]; then
   echo "lifecycle phase=$actual"
   prior=$actual
   if [ "$actual" = fail_open ]; then
    printf '%s\n' "$state"
    hyper-kernel status
   fi
  fi
  [ "$actual" != "$phase" ] || return 0
  sleep 1
 done
 echo "$state"
 hyper-net conflicts || :
 logread -e hyper | tail -16
 return 1
}
uptime_s() { cut -d. -f1 /proc/uptime; }
stop_and_verify() {
 call stop
 for i in $(seq 1 25); do [ ! -f /var/run/hyper/network/owned ] && ! pidof mihomo >/dev/null && break; sleep 1; done
 [ ! -f /var/run/hyper/network/owned ] && ! pidof mihomo >/dev/null
 if [ -f /root/enable-fw3 ]; then
  for xt in iptables-legacy-save ip6tables-legacy-save; do
   "$xt" >/tmp/stop-firewall-check
   ! grep -q 'HYPER_\|hyper-owned-' /tmp/stop-firewall-check
  done
 else
  ! nft list table inet hyper >/dev/null 2>&1
 fi
 nslookup -retry=1 -timeout=1 -type=A localhost 192.168.41.1 >/dev/null
 report daemon_stop pass
}
if [ -f /root/original-boot ]; then
 [ "$(cat /root/original-boot)" != "$(cat /proc/sys/kernel/random/boot_id)" ]
 [ ! -e /tmp/hold-hyper-test-candidate ]
 for _n in $(seq 1 60); do
  [ -S /var/run/hyper/manager.sock ] && break
  sleep 1
 done
 [ -S /var/run/hyper/manager.sock ]
 wait_phase running 150
 state=$(hyper-kernel status)
 [ "$(jsonfilter -s "$state" -e '@.selected')" = v1.19.34-0.20261117082241-15fb245e9278 ]
 [ "$(jsonfilter -s "$state" -e '@.active')" = v1.19.34-0.20261117082241-15fb245e9278 ]
 [ "$(jsonfilter -s "$state" -e '@.pending')" = false ]
 sha256sum -c /root/confirmed-config.sha256 >/dev/null
 /usr/libexec/hyper/network verify
 report daemon_powercut_recovery pass
 stop_and_verify
 report daemon_lifecycle pass
 exit 0
fi
. /root/setup-network
[ ! -f /root/qualify-tun ] || uci set hyper.main.method=tun
uci set hyper.main.dns=1
uci set hyper.main.ipv6=1
uci commit hyper
/etc/init.d/hyper start
for i in $(seq 1 20); do [ -S /var/run/hyper/manager.sock ] && break; sleep 1; done
wait_job "$(call subscription_write '{"action":"configure","args":{"config":{"id":"qa","name":"Test","kind":"file"}}}')"
for attempt in 1 2 3; do
 # Its own final rule keeps the routing template, and so the network, out of the fixture.
 if wait_job "$(call subscription_write '{"action":"refresh","args":{"source":"qa","content":"proxies:\n  - {name: Fixture, type: socks5, server: 198.18.0.33, port: 1080}\nrules:\n  - MATCH,DIRECT\n"}}')" >/tmp/refresh-result; then
  break
 fi
 cat /tmp/refresh-result
 call status
 grep -q preflight_revision_changed /tmp/refresh-result && [ "$attempt" -lt 3 ] || exit 1
 sleep 2
done
call source '{"source":"qa"}'
call mode '{"mode":"global"}'
# Trust the isolated qualification key and verify the unselected candidate.
tag=v1.19.34-0.20261117082241-15fb245e9278
hyper-kernel install --tag "$tag" --stage-only --bundle /root/bundles/"$tag"
mkdir -p /var/run/hyper/mihomo
chmod 0755 /var/run/hyper/mihomo
printf '%s\n' "proxies: [{name: bad, type: nonexistent, password: private-test-token}]" >/tmp/bad.yaml
if hyper-kernel check --tag "$tag" --config /tmp/bad.yaml >/tmp/rejected-check 2>&1; then
 echo 'Incompatible configuration incorrectly accepted'; exit 1
fi
! grep -q private-test-token /tmp/rejected-check
[ -z "$(hyper-kernel status | jsonfilter -e '@.selected')" ]
[ ! -f /var/run/hyper/network/owned ]
report daemon_incompatible_config_rejected pass
install_bundle v1.19.34-0.20261117082241-15fb245e9278
wait_phase direct 10
[ ! -f /var/run/hyper/network/owned ] && ! pidof mihomo >/dev/null
[ "$(hyper-kernel status | jsonfilter -e '@.selected')" = v1.19.34-0.20261117082241-15fb245e9278 ]
report daemon_official_archive_install pass
hyper-net conflicts || { report conflict_scan failed; exit 1; }
report conflict_scan pass
tun_device() { ls -lidn /dev/net/tun | awk '{print $1, $2, $4, $5, $6, $7}'; }
tun_before=$(tun_device)
before=$(uptime_s)
call start
wait_phase running 160
elapsed=$(( $(uptime_s)-before ))
[ "$elapsed" -ge 60 ]
[ "$(hyper-kernel status | jsonfilter -e '@.active')" = v1.19.34-0.20261117082241-15fb245e9278 ]
nslookup -retry=1 -timeout=1 -type=A probe.hyper.invalid 192.168.41.1 | grep -q 192.0.2.1
report daemon_first_confirm_s "$elapsed"
[ "$(tun_device)" = "$tun_before" ]
hyper-net conflicts
report running_conflict_scan pass
ip -4 rule add pref 21998 from 192.0.2.41/32 lookup 123
if hyper-net conflicts >/tmp/conflicts; then echo 'foreign rule was ignored'; exit 1; fi
jsonfilter -i /tmp/conflicts -e '@.resources[*].kind' | grep -Fxq route
ip -4 rule del pref 21998 from 192.0.2.41/32 lookup 123
dnsdir=$(head -1 /var/run/hyper/network/dns)
# A default upstream that another plugin wrote, even to a local resolver, does not stop a start.
# DNS protection cannot set it aside, so its prerequisites refuse it instead.
printf '%s\n' 'server=127.0.0.1#5335' >"$dnsdir/98-other.conf"
hyper-net conflicts
if /usr/libexec/hyper/network prerequisites >/tmp/requirements; then echo 'foreign DNS upstream was ignored'; exit 1; fi
grep -q '^missing dns_default_upstream ' /tmp/requirements
rm "$dnsdir/98-other.conf"
hyper-net conflicts
report concurrent_conflict_scan pass
# Only the VM's private fixture key can authorize this synthetic fault.
hyper-kernel install --tag v0.0.0-qa --stage-only --bundle /root/bundles/v0.0.0-qa
trial=$(call kernel_write '{"action":"select","args":{"tag":"v0.0.0-qa"}}')
if wait_job "$trial" >/tmp/rejected-trial; then echo 'Fault incorrectly reported success'; exit 1; fi
cat /tmp/rejected-trial
[ "$(jsonfilter -i /tmp/rejected-trial -e '@.stage')" = failed ]
grep -q 'previous kernel v1.19.34-0.20261117082241-15fb245e9278 is running' /tmp/rejected-trial || { call status; hyper-kernel status; exit 1; }
for i in $(seq 1 100); do
 state=$(hyper-kernel status)
 if [ "$(jsonfilter -s "$state" -e '@.selected')" = v1.19.34-0.20261117082241-15fb245e9278 ] && [ "$(call status | jsonfilter -e '@.phase')" = running ]; then break; fi
 sleep 1
done
[ "$(hyper-kernel status | jsonfilter -e '@.selected')" = v1.19.34-0.20261117082241-15fb245e9278 ]
wait_phase running 30
/usr/libexec/hyper/network verify
call logs '{"source":"service","query":"core exited during startup","limit":20}' | grep -q 'core exited during startup'
report daemon_failed_candidate_rollback pass
if [ -f /root/enable-powercut ]; then
 # Persist the reference before staging the fault. The actual trial's own
 # transaction must make it to disk without a guest sync or shutdown.
 cat /proc/sys/kernel/random/boot_id >/root/original-boot
 cfg=$(hyper-kernel active | jsonfilter -e '@.config')
 sha256sum "$cfg" >/root/confirmed-config.sha256
 uci set hyper.main.enabled=1
 uci commit hyper
 /etc/init.d/hyper enable
 sync
 hyper-kernel install --tag v0.0.0-qa --stage-only --bundle /root/bundles/v0.0.0-qa
 touch /tmp/hold-hyper-test-candidate
 call kernel_write '{"action":"select","args":{"tag":"v0.0.0-qa"}}' | jsonfilter -e '@.task' | grep -q .
 for _n in $(seq 1 45); do
  state=$(hyper-kernel status)
  [ "$(jsonfilter -s "$state" -e '@.pending')" = true ] && break
  sleep 1
 done
 [ "$(jsonfilter -s "$state" -e '@.pending')" = true ]
 [ "$(jsonfilter -s "$state" -e '@.active')" = v1.19.34-0.20261117082241-15fb245e9278 ]
 [ "$(jsonfilter -s "$state" -e '@.selected')" = v0.0.0-qa ]
 report daemon_candidate_pending pass
 echo HYPER_POWER_CUT
 sleep 60
 exit 1
fi
stop_and_verify
report daemon_lifecycle pass
