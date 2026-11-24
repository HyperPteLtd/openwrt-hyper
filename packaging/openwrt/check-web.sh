#!/bin/sh
# Run only inside the disposable package-install userland.
set -eu
mkdir -p /var/run/ubus /var/lock
password=$(cat /proc/sys/kernel/random/uuid)
key=$(uhttpd -m "$password")
uci set rpcd.webtest=login
uci set rpcd.webtest.username=webtest
uci set rpcd.webtest.password="$key"
uci add_list rpcd.webtest.read=hyper
uci set rpcd.webprobe=login
uci set rpcd.webprobe.username=webprobe
uci set rpcd.webprobe.password="$key"
uci add_list rpcd.webprobe.read=hyper
uci add_list rpcd.webprobe.write=hyper
uci commit rpcd
ubusd >/tmp/web-ubus.log 2>&1 & bus=$!
rpc= daemon= web= peer=
stage=socket
cleanup() {
 code=$?
 if [ "$code" -ne 0 ]; then
  echo "LuCI test failed at $stage" >&2
  ubus list hyper >&2 || :
  for log in /tmp/web-ubus.log /tmp/web-manager.log /tmp/web-http.log /tmp/web-rpc.log /tmp/web-result /tmp/web-headers; do [ ! -f "$log" ] || tail -6 "$log" >&2; done
 fi
 # Let clients finish their shutdown on the bus they started on. Otherwise
 # their cleanup can unlink the socket used by the next installation test.
 for pid in "$peer" "$web" "$daemon" "$rpc" "$bus"; do
  [ -n "$pid" ] || continue
  kill "$pid" 2>/dev/null || :
  wait "$pid" 2>/dev/null || :
 done
 uci -q delete rpcd.webprobe
 uci -q delete rpcd.webtest && uci commit rpcd
}
trap cleanup EXIT
wait_ready() {
 for attempt in $(seq 1 15); do
  if "$@" >/dev/null 2>&1; then return 0; fi
  sleep 1
 done
 return 1
}
wait_ready ubus -t 2 list
rpcd >/tmp/web-rpc.log 2>&1 & rpc=$!
# A subscription read from a file (internal/cli/subscriptioncli HYPER_FIXTURE_DIR): one node at
# a local listener, with its own final rule, which keeps the routing template, and so the
# network, out of the test.
export HYPER_FIXTURE_DIR=/tmp/hyper-fixtures
mkdir -p "$HYPER_FIXTURE_DIR"
printf 'proxies:\n  - {name: loopback, type: http, server: 127.0.0.1, port: 18890}\nrules:\n  - MATCH,DIRECT\n' >"$HYPER_FIXTURE_DIR/package-probes.yaml"
hyperd serve >/tmp/web-manager.log 2>&1 & daemon=$!
uhttpd -f -h /www -p 127.0.0.1:18889 -u /ubus >/tmp/web-http.log 2>&1 & web=$!
stage=manager-ready
direct_ready() { ubus -t 2 call hyper status | jsonfilter -e '@.phase' | grep -qx direct; }
wait_ready direct_ready
origin=http://127.0.0.1:18889
for path in luci-static/resources/view/hyper/router.js luci-static/resources/hyper/app.js; do
 stage="static module $path"
 curl -fsS -D /tmp/web-headers "$origin/$path" -o /tmp/web-module.js
 grep -Eiq '^Content-Type: *(application|text)/(x-)?javascript' /tmp/web-headers
 cmp /tmp/web-module.js "/www/$path"
done
post() { curl -fsS --max-time 4 -H 'Content-Type: application/json' -d "$1" "$origin/ubus"; }
anon=00000000000000000000000000000000
stage=login
login() {
 for attempt in $(seq 1 15); do
  sid=$(post "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"call\",\"params\":[\"$anon\",\"session\",\"login\",{\"username\":\"$1\",\"password\":\"$password\"}]}" | jsonfilter -e '@.result[1].ubus_rpc_session') || sid=
  if [ -n "$sid" ] && [ "$sid" != "$anon" ]; then printf '%s\n' "$sid"; return 0; fi
  sleep 1
 done
 return 1
}
session=$(login webtest)
[ -n "$session" ] && [ "$session" != "$anon" ]
query() { post "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"call\",\"params\":[\"$1\",\"hyper\",\"status\",{}]}"; }
stage=authenticated-status
query "$session" > /tmp/web-result
[ "$(jsonfilter -i /tmp/web-result -e '@.result[1].phase')" = direct ]
denied() {
 [ "$(jsonfilter -i /tmp/web-result -e '@.error.code')" = -32002 ] ||
 [ "$(jsonfilter -i /tmp/web-result -e '@.result[0]')" = 6 ]
}
stage=anonymous-access
query "$anon" > /tmp/web-result
denied
stage=read-only-permission
post "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"call\",\"params\":[\"$session\",\"hyper\",\"stop\",{\"revision\":1}]}" > /tmp/web-result
denied
echo 'LuCI serves both JavaScript entry points and the authenticated Hyper ubus call succeeds'

writer=$(login webprobe)
[ -n "$writer" ] && [ "$writer" != "$anon" ]
rpc_call() {
 post "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"call\",\"params\":[\"$writer\",\"hyper\",\"$1\",$2]}" > /tmp/web-result
 [ "$(jsonfilter -i /tmp/web-result -e '@.result[0]')" = 0 ]
}
stage=signed-out
rpc_call status '{}'
[ "$(jsonfilter -i /tmp/web-result -e '@.result[1].saved.enabled')" = false ]
wait_task() {
 job=$(jsonfilter -i /tmp/web-result -e '@.result[1].task')
 [ -n "$job" ]
 for i in $(seq 1 25); do
  rpc_call task "{\"task\":\"$job\"}"
  case "$(jsonfilter -i /tmp/web-result -e '@.result[1].stage')" in
   done) break ;; running) sleep 1 ;; *) return 1 ;;
  esac
 done
 [ "$(jsonfilter -i /tmp/web-result -e '@.result[1].stage')" = done ]
}
stage=fail-open-preference
for toggle in false true; do
 case "$toggle" in false) bit=0;; true) bit=1;; esac
 rpc_call save_network "{\"args\":{\"enabled\":false,\"method\":\"tproxy\",\"dns\":true,\"networks\":[\"lan\"],\"update_hours\":6,\"fail_open\":$toggle}}"
 wait_task
 rpc_call network_preferences '{}'
 [ "$(jsonfilter -i /tmp/web-result -e '@.result[1].fail_open')" = "$toggle" ]
 [ "$(uci -q get hyper.main.fail_open)" = "$bit" ]
 grep -Eq "^[[:space:]]*option[[:space:]]+fail_open[[:space:]]+'$bit'[[:space:]]*$" /etc/config/hyper
done
echo 'Authenticated LuCI RPC persisted both fail-open settings to the UCI file'
# Under emulation (HYPER_EMULATED) the core starts too slowly for the kernel manager's checks:
# the probes and the core's installation are left to the native runs.
if [ "${HYPER_EMULATED:-0}" = 1 ]; then
 echo 'Emulated: skipped the TCP probes and the installation of the core'
 exit 0
fi
stage=bundled-core
# The daemon installs the package's core offline and selects it.
tag=$(jsonfilter -i /usr/share/hyper/core/pin.json -e '@.tag')
core_selected() { [ "$(hyper-kernel status | jsonfilter -e '@.selected')" = "$tag" ]; }
for i in $(seq 1 150); do core_selected && break; sleep 2; done
core_selected
echo "The daemon installed and selected the package's core $tag ($(hyper-kernel status | jsonfilter -e '@.mode'))"
stage=probe-source
# A second listener can be closed independently of uhttpd serving the authenticated RPC.
uhttpd -f -h /www -p 127.0.0.1:18890 >/tmp/web-peer.log 2>&1 & peer=$!
curl -fsS --retry 5 --retry-connrefused http://127.0.0.1:18890/luci-static/resources/hyper/app.js -o /dev/null
printf '%s' '{"config":{"id":"package-probes","name":"Package fixture","kind":"fixture"}}' |
 hyper-subscription configure | jsonfilter -e '@.ok' | grep -qx true
printf '%s' '{"source":"package-probes"}' | hyper-subscription refresh | jsonfilter -e '@.data.decision' | grep -qx ready
node=$(printf '%s' '{"source":"package-probes"}' | hyper-subscription nodes | jsonfilter -e '@.data.items[0].id')
[ -n "$node" ]
measure() {
 rpc_call test_nodes "{\"source\":\"package-probes\",\"method\":\"$1\",\"args\":{\"nodes\":[\"$node\"]}}"
}
tcp_probe() {
 measure tcp
 wait_task
 rpc_call latencies '{"method":"tcp"}'
 [ "$(jsonfilter -i /tmp/web-result -e '@.result[1].*.node')" = "$node" ]
}
stage=tcp-handshake
tcp_probe
measured=$(jsonfilter -i /tmp/web-result -e '@.result[1].*.latency.measured_at')
[ "$measured" -gt 0 ]
[ "$(jsonfilter -i /tmp/web-result -e '@.result[1].*.latency.delay_ms')" -gt 0 ]
stage=tcp-closed-peer
kill "$peer"
wait "$peer" || :
peer=
tcp_probe
[ "$(jsonfilter -i /tmp/web-result -e '@.result[1].*.error')" = 'no confirmed measurement' ]
[ -z "$(jsonfilter -i /tmp/web-result -e '@.result[1].*.latency.measured_at')" ]
echo 'Installed LuCI RPC measured a loopback TCP handshake and detected the closed peer'
