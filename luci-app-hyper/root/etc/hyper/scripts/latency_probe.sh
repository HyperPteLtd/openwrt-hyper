#!/bin/sh
# Isolated, cancellable node probes. Never touch the service config or firewall.
set -u
BASE="${HYPER_PROBE_DIR:-/var/run/hyper-latency}"
CORE="${HYPER_PROBE_CORE:-/usr/libexec/hyper/bin/sing-box}"
GENERATOR="${HYPER_PROBE_GENERATOR:-/etc/hyper/scripts/generate_client.uc}"
URL="${HYPER_PROBE_URL:-https://www.gstatic.com/generate_204}"
id="${1:-}"
case "$id" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
umask 077
mkdir -p "$BASE"
# Only probes share this lock. User actions only replace/cancel request.json.
exec 9>"$BASE/lock"
flock -x 9 || exit 1
pause() { ucode -e 'sleep(100);'; }
current() {
  [ "$(jsonfilter -i "$BASE/request.json" -e '@.id' 2>/dev/null)" = "$id" ] &&
  [ "$(jsonfilter -i "$BASE/request.json" -e '@.cancelled' 2>/dev/null)" != true ]
}
current || exit 0
# The previous probe has exited before this lock is acquired.
for old in "$BASE"/job-*; do [ ! -d "$old" ] || rm -rf "$old"; done
job="$BASE/job-$id"
mkdir -m 700 "$job"
cp "$BASE/request.json" "$job/request.json"
core_pid=''
pending=''
write_state() {
  printf '{"state":"%s","error":"%s"}\n' "$1" "${2:-}" > "$job/state.tmp"
  mv "$job/state.tmp" "$job/state.json"
}
terminate_core() {
  [ -n "$core_pid" ] || return
  kill "$core_pid" 2>/dev/null || true
  for tick in $(seq 1 10); do
    kill -0 "$core_pid" 2>/dev/null || break
    pause
  done
  kill -KILL "$core_pid" 2>/dev/null || true
  wait "$core_pid" 2>/dev/null || true
  core_pid=''
}
cleanup() {
  for item in $pending; do kill "${item%%:*}" 2>/dev/null || true; done
  for item in $pending; do wait "${item%%:*}" 2>/dev/null || true; done
  terminate_core
  rm -f "$job/config.json" "$job"/*.response
}
trap cleanup EXIT
trap 'write_state cancelled; exit 0' INT TERM
write_state running
secret="$(cat /proc/sys/kernel/random/uuid)"
ready=0
for attempt in 1 2 3; do
  current || { write_state cancelled; exit 0; }
  port=$((20000 + ($$ + attempt * 997) % 30000))
  HYPER_PROBE_REQUEST="$job/request.json" HYPER_PROBE_CONFIG="$job/config.json" \
    HYPER_PROBE_PORT="$port" HYPER_PROBE_SECRET="$secret" \
    ucode -S "$GENERATOR" >"$job/generate.log" 2>&1 || { write_state failed probe_config_failed; exit 1; }
  current || { write_state cancelled; exit 0; }
  "$CORE" run -c "$job/config.json" >"$job/core.log" 2>&1 9>&- &
  core_pid=$!
  for tick in $(seq 1 50); do
    current || { write_state cancelled; exit 0; }
    kill -0 "$core_pid" 2>/dev/null || break
    if curl --noproxy '*' -fsS --max-time 1 -H "Authorization: Bearer $secret" \
      "http://127.0.0.1:$port/version" >/dev/null 2>&1; then ready=1; break; fi
    pause
  done
  [ "$ready" = 0 ] || break
  terminate_core
done
[ "$ready" = 1 ] || { write_state failed probe_start_failed; exit 1; }
names="$(jsonfilter -i "$job/request.json" -e '@.nodes[*]')"
# Each curl corresponds to one independent outbound, at most six in flight.
set -- $names
started="$(date +%s)"
while [ "$#" -gt 0 ] || [ -n "$pending" ]; do
  current || { write_state cancelled; exit 0; }
  [ "$(( $(date +%s) - started ))" -lt 120 ] || { write_state failed probe_timeout; exit 1; }
  kill -0 "$core_pid" 2>/dev/null || { write_state failed probe_stopped; exit 1; }
  next=''
  count=0
  for item in $pending; do
    pid="${item%%:*}"; name="${item#*:}"
    if kill -0 "$pid" 2>/dev/null; then
      next="$next $item"; count=$((count + 1)); continue
    fi
    wait "$pid" 2>/dev/null || true
    delay="$(jsonfilter -i "$job/$name.response" -e '@.delay' 2>/dev/null)"
    case "$delay" in ''|*[!0-9]*) delay=null ;; *) [ "$delay" -gt 0 ] || delay=1 ;; esac
    printf '{"ms":%s}\n' "$delay" > "$job/$name.tmp"
    mv "$job/$name.tmp" "$job/$name.json"
    rm -f "$job/$name.response"
  done
  pending="$next"
  while [ "$#" -gt 0 ] && [ "$count" -lt 6 ]; do
    name="$1"; shift
    case "$name" in ''|*[!A-Za-z0-9_]*) continue ;; esac
    curl --noproxy '*' -fsS --max-time 4 -G -H "Authorization: Bearer $secret" \
      --data-urlencode "url=$URL" --data-urlencode 'timeout=3000' \
      "http://127.0.0.1:$port/proxies/cfg-$name-out/delay" -o "$job/$name.response" >/dev/null 2>&1 9>&- &
    pending="$pending $!:$name"
    count=$((count + 1))
  done
  pause
done
current || { write_state cancelled; exit 0; }
write_state done
