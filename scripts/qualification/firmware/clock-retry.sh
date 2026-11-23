#!/bin/sh
# Executed only in the disposable QEMU guest with a throwaway local CA.
set -eu
cd /root/clock-fixture
epoch=$(cat epoch)
call() { printf '%s\n' "$2" | hyperd call "$1"; }
status() { call subscription_read '{"action":"status","args":{"source":"clock-test"}}'; }
await_stage() {
 local id="$1" expected="$2" stage _try
 for _try in $(seq 1 30); do
  stage=$(call task "{\"task\":\"$id\"}" | jsonfilter -e '@.stage')
  case "$stage" in done|failed) [ "$stage" = "$expected" ]; return;; esac
  sleep 1
 done
 return 1
}
for _try in $(seq 1 20); do [ -S /var/run/hyper/manager.sock ] && break; sleep 1; done
uci set hyper.main.update_hours=0
/etc/init.d/sysntpd stop
cp /etc/ssl/certs/ca-certificates.crt roots
cat server.crt >>/etc/ssl/certs/ca-certificates.crt
mkdir www
printf 'socks5://198.18.0.33:1080#Fixture\n' >www/source
uhttpd -f -h "$PWD/www" -s 127.0.0.1:19443 -C "$PWD/server.crt" -K "$PWD/server.key" &
server=$!
step=listener
cleanup() {
 if [ "$1" -ne 0 ]; then echo "Clock regression failed at $step"; status || :; fi
 kill "$server" 2>/dev/null || :
 cat roots >/etc/ssl/certs/ca-certificates.crt
 date -s "@$epoch" >/dev/null
 uci revert hyper.main.update_hours || :
}
trap 'cleanup $?' EXIT
for _try in $(seq 1 10); do netstat -lnt | grep -q '127.0.0.1:19443 ' && break; sleep 1; done
netstat -lnt | grep -q '127.0.0.1:19443 ' || { ip -4 addr show dev lo; exit 1; }
step=configure
cfg='{"action":"configure","args":{"config":{"id":"clock-test","kind":"external","name":"Clock test","url":"https://127.0.0.1:19443/source"}}}'
await_stage "$(call subscription_write "$cfg" | jsonfilter -e '@.task')" "done"
step=certificate_failure
date -s "@$((epoch-86400))" >/dev/null
await_stage "$(call subscription_write '{"action":"refresh","args":{"source":"clock-test"}}' | jsonfilter -e '@.task')" failed
failure=$(status)
[ "$(jsonfilter -s "$failure" -e '@.data.last_tls.reason')" = not_yet_valid ]
[ -n "$(jsonfilter -s "$failure" -e '@.data.last_tls.not_before')" ]
step=clock_event
date -s "@$epoch" >/dev/null
ACTION=step /sbin/hotplug-call ntp
for _try in $(seq 1 20); do
 [ "$(status | jsonfilter -e '@.data.ready')" = true ] && break
 sleep 1
done
[ "$(status | jsonfilter -e '@.data.ready')" = true ]
[ "$(call status '{}' | jsonfilter -e '@.saved.enabled')" = false ]
