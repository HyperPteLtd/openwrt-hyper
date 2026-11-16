#!/bin/sh
# Share one synchronization lock between explicit refreshes, login and cron.
set -u
BASE=/var/run/hyper-sync
SCRIPT=/etc/hyper/scripts/sync_subscription.uc
umask 077
mkdir -p "$BASE"
exec 9>"$BASE/lock"
id="${1:-}"
if [ -z "$id" ]; then
  flock -n 9 || exit 0
  id="$(cat /proc/sys/kernel/random/uuid)"
  session="$(ucode -S -e "import { md5 } from '/etc/hyper/scripts/md5.uc'; import { readSession } from '/etc/hyper/scripts/hyper.uc'; print(md5(readSession()?.jwt || ''));")"
  printf '{"id":"%s","time":%s,"session":"%s"}\n' "$id" "$(date +%s)" "$session" > "$BASE/request.tmp"
  mv "$BASE/request.tmp" "$BASE/request.json"
else
  case "$id" in *[!A-Za-z0-9-]*) exit 1 ;; esac
  flock -x 9 || exit 1
fi
[ "$(jsonfilter -i "$BASE/request.json" -e '@.id' 2>/dev/null)" = "$id" ] || exit 0
previous="$(jsonfilter -i "$BASE/state.json" -e '@.id' 2>/dev/null)"
[ "$previous" != "$id" ] || exit 0
state() {
  printf '{"id":"%s","state":"%s","time":%s}\n' "$id" "$1" "$(date +%s)" > "$BASE/state.tmp"
  mv "$BASE/state.tmp" "$BASE/state.json"
}
state running
if "$SCRIPT" 9>&-; then state done; else state failed; fi
