#!/bin/sh
# Serialize background service changes. State stays outside the core's writable directory.
set -u
CONTROL_DIR="${HYPER_CONTROL_DIR:-/var/run/hyper-control}"
INIT="${HYPER_INIT:-/etc/init.d/hyper}"
READY_LIMIT="${HYPER_READY_LIMIT:-60}"
INIT_LIMIT="${HYPER_INIT_LIMIT:-90}"
RUN_SESSION="$(dirname "$0")/run-session.sh"
umask 077
mkdir -p "$CONTROL_DIR"

# BusyBox flock is present in the supported OpenWrt releases. The kernel releases
# the lock even if a worker dies; queued workers cannot overlap init operations.
exec 9>"$CONTROL_DIR/lock"
flock -x 9 || exit 1

write_state() {
  # Never publish a completion belonging to an intent the user has replaced.
  [ "$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null)" = "$id" ] || return 0
  printf '{"id":%s,"action":"%s","state":"%s","error":"%s","time":%s}\n' \
    "$id" "$action" "$1" "$2" "$(date +%s)" > "$CONTROL_DIR/state.tmp"
  mv "$CONTROL_DIR/state.tmp" "$CONTROL_DIR/state.json"
}

terminate_init() {
  # Stop may arrive before the launcher has created its session. In that case
  # cancel the launcher itself so it cannot start the obsolete operation later.
  kill -TERM -- "-$init_pid" 2>/dev/null || kill -TERM "$init_pid" 2>/dev/null || true
  sleep 1
  kill -KILL -- "-$init_pid" 2>/dev/null || true
  kill -KILL "$init_pid" 2>/dev/null || true
  wait "$init_pid" 2>/dev/null || true
}

run_init() {
  local run_action elapsed latest next_action result
  run_action="$1"
  sh "$RUN_SESSION" "$INIT" "$run_action" >"$CONTROL_DIR/output.log" 2>&1 9>&- &
  init_pid=$!
  elapsed=0
  while kill -0 "$init_pid" 2>/dev/null; do
    latest="$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null || true)"
    next_action="$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.action' 2>/dev/null || true)"
    if [ "$run_action" != stop ] && [ "$latest" != "$id" ] && [ "$next_action" = stop ]; then
      terminate_init
      return 125
    fi
    if [ "$elapsed" -ge "$INIT_LIMIT" ]; then
      terminate_init
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  wait "$init_pid"
}

ready() {
  local port secret
  port="$(uci -q get hyper.infra.clash_api_port)"
  secret="$(uci -q get hyper.infra.clash_api_secret)"
  case "$port" in ''|*[!0-9]*) port=5334 ;; esac
  curl --noproxy '*' -fsS --max-time 1 -H "Authorization: Bearer $secret" \
    "http://127.0.0.1:$port/version" >/dev/null 2>&1
}

needs_cleanup=0
cleanup_failed_start() {
  [ "$action" != stop ] || return 0
  # A queued Stop can perform the cleanup; a queued Start needs cleanup first.
  if [ "$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null)" != "$id" ]; then
    needs_cleanup=1
  else
    # Includes timeouts before procd/watchdog registration, after DNS was changed.
    # Cleanup is bounded by run_init and never recursively cleans a failed Stop.
    run_init stop || true
  fi
}
while :; do
  request="$(cat "$CONTROL_DIR/request.json" 2>/dev/null || true)"
  id="$(jsonfilter -s "$request" -e '@.id' 2>/dev/null)"
  action="$(jsonfilter -s "$request" -e '@.action' 2>/dev/null)"
  case "$id" in ''|*[!0-9]*) exit 1 ;; esac
  case "$action" in start|stop|restart) ;; *) exit 1 ;; esac
  # Another worker may already have applied this request while we waited.
  done_id="$(jsonfilter -i "$CONTROL_DIR/state.json" -e '@.id' 2>/dev/null || true)"
  done_state="$(jsonfilter -i "$CONTROL_DIR/state.json" -e '@.state' 2>/dev/null || true)"
  if [ "$done_id" = "$id" ] && { [ "$done_state" = done ] || [ "$done_state" = failed ]; }; then break; fi

  write_state running ''
  if [ "$needs_cleanup" = 1 ] && [ "$action" != stop ]; then
    # A newer Start may replace the Stop that cancelled an earlier startup.
    run_init stop
    cleanup_result=$?
    needs_cleanup=0
    # Cleanup may take time; reread intent instead of starting an obsolete one.
    [ "$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null)" = "$id" ] || continue
    if [ "$cleanup_result" != 0 ]; then write_state failed service_failed; break; fi
  fi
  needs_cleanup=0
  run_init "$action"
  init_result=$?
  if [ "$init_result" = 125 ]; then
    # The latest Stop performs cleanup once; do not run a redundant Stop first.
    needs_cleanup=1
  elif [ "$init_result" = 0 ]; then
    error=''
    if [ "$action" != stop ]; then
      # A procd PID alone does not prove startup succeeded. Wait for the local API.
      error=not_ready
      stable=0
      for attempt in $(seq 1 "$READY_LIMIT"); do
        latest="$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null || true)"
        [ "$latest" = "$id" ] || break
        if ready; then stable=$((stable + 1)); else stable=0; fi
        if [ "$stable" -ge 2 ]; then error=''; break; fi
        sleep 1
      done
    fi
    if [ -z "$error" ]; then write_state done ''; else
      cleanup_failed_start
      write_state failed "$error"
    fi
  else
    cleanup_failed_start
    write_state failed service_failed
  fi
  # A newer request (e.g. Stop while starting) is applied next, never concurrently.
  latest="$(jsonfilter -i "$CONTROL_DIR/request.json" -e '@.id' 2>/dev/null || true)"
  [ "$latest" != "$id" ] || break
done
