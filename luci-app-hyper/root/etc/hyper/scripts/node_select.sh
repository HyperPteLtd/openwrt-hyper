#!/bin/sh
# Apply the latest persisted selection without blocking rpcd or the service lock.
set -u
BASE=/var/run/hyper-node-select
umask 077
mkdir -p "$BASE"
exec 9>"$BASE/lock"
flock -x 9 || exit 1
attempts=0
while :; do
  name="$(uci -q get hyper.config.main_node)"
  case "$name" in ''|*[!A-Za-z0-9_]*) exit 1 ;; esac
  # A stopped service keeps the choice for its next start.
  [ "$(uci -q get hyper.config.enabled)" = 1 ] || exit 0
  running="$(ubus call service list '{"name":"hyper"}' 2>/dev/null | jsonfilter -e '@.hyper.instances["sing-box-c"].running' 2>/dev/null)"
  if [ "$running" != true ]; then
    attempts=$((attempts + 1))
    [ "$attempts" -lt 240 ] || exit 0
    ucode -e 'sleep(250);'
    continue
  fi
  port="$(uci -q get hyper.infra.clash_api_port)"
  secret="$(uci -q get hyper.infra.clash_api_secret)"
  case "$port" in ''|*[!0-9]*) port=5334 ;; esac
  if ! curl --noproxy '*' -fsS --max-time 2 -X PUT -H "Authorization: Bearer $secret" \
    -H 'Content-Type: application/json' --data "{\"name\":\"cfg-$name-out\"}" \
    "http://127.0.0.1:$port/proxies/main-out" >/dev/null 2>&1; then
    # Fall back through the guarded service queue, which ignores restart after Stop.
    if [ "$(uci -q get hyper.config.main_node)" = "$name" ] && [ "$(uci -q get hyper.config.enabled)" = 1 ]; then
      ubus call luci.hyper restart >/dev/null 2>&1 || true
    fi
  fi
  [ "$(uci -q get hyper.config.main_node)" != "$name" ] || exit 0
done
