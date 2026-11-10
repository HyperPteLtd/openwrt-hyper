#!/bin/sh
# Run the PC-side tests.
#   UCODE=/path/to/ucode UCODE_LIB=/path/to/ucode/build tests/run.sh
# UCODE_LIB must contain fs.so / math.so (a host build of jow-/ucode).

set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
SCRIPTS="$ROOT/luci-app-hyper/root/etc/hyper/scripts"
UCODE="${UCODE:-ucode}"
UCODE_LIB="${UCODE_LIB:-/usr/lib/ucode}"
ECHO_PORT="${ECHO_PORT:-18765}"
export ECHO_PORT

run() {
	"$UCODE" -S -L "$HERE/stubs" -L "$HERE" -L "$SCRIPTS" -L "$UCODE_LIB" "$@"
}

fail=0

echo "== compile check"
for f in "$SCRIPTS"/*.uc "$ROOT"/luci-app-hyper/root/usr/share/rpcd/ucode/luci.hyper; do
	tmp="$(mktemp)"
	# absolute /etc/hyper/scripts imports -> repo paths
	sed "s#/etc/hyper/scripts/#$SCRIPTS/#g" "$f" > "$tmp"
	# Modules are checked by importing them: `-c module` exists only in newer ucode,
	# and the firmware's ucode (23.05 - 25.12) must accept the source too.
	case "$f" in
		*hyper.uc|*ppanel.uc|*snapshot.uc|*rules.uc|*checkin.uc|*md5.uc)
			mv "$tmp" "$tmp.uc"; tmp="$tmp.uc"
			check() { run -e "import * as m from '$tmp';"; } ;;
		*) check() { run -c -o /dev/null "$tmp"; } ;;
	esac
	if check; then echo "ok   $(basename "$f")"; else echo "FAIL $(basename "$f")"; fail=1; fi
	rm -f "$tmp"
done
tmp="$(mktemp)"
sed "s#/etc/hyper/scripts/#$SCRIPTS/#g" "$SCRIPTS/firewall_post.ut" > "$tmp"
if run -T -c -o /dev/null "$tmp"; then echo "ok   firewall_post.ut"; else echo "FAIL firewall_post.ut"; fail=1; fi
rm -f "$tmp"

echo "== regex limits"
# Regex literals compile when first used, so `ucode -c` misses this: musl (OpenWrt)
# and macOS refuse {n,m} counts above RE_DUP_MAX (255) at run time.
bad="$(grep -noE '\{[0-9]+(,[0-9]*)?\}' "$SCRIPTS"/*.uc "$SCRIPTS"/*.ut "$ROOT"/luci-app-hyper/root/usr/share/rpcd/ucode/luci.hyper |
	awk -F'{' '{ split($2, a, /[,}]/); if (a[1] + 0 > 255 || a[2] + 0 > 255) print }')"
if [ -n "$bad" ]; then echo "FAIL repetition count above 255:"; echo "$bad"; fail=1; else echo "ok   no repetition count above 255"; fi

echo "== rpcd load"
# Runs the top level of luci.hyper (method table, top-level regexes) with stubbed ubus / uci
tmp="$(mktemp)"
sed "s#/etc/hyper/scripts/#$SCRIPTS/#g" "$ROOT"/luci-app-hyper/root/usr/share/rpcd/ucode/luci.hyper > "$tmp"
if run -e "const m = loadfile('$tmp')()['luci.hyper']; for (let k in ['status', 'checkin', 'checkin_status', 'api', 'set_config']) if (type(m[k]?.call) !== 'function') die(k);" ; then
	echo "ok   luci.hyper loads"
else
	echo "FAIL luci.hyper does not load"; fail=1
fi
rm -f "$tmp"

echo "== unit tests"
run "$HERE/snapshot_test.uc" || fail=1
run "$HERE/ppanel_test.uc" || fail=1
run "$HERE/rules_test.uc" || fail=1
run "$HERE/checkin_test.uc" || fail=1
run "$HERE/md5_test.uc" || fail=1

python3 "$HERE/echo_server.py" "$ECHO_PORT" &
echo_pid=$!
sleep 1
run "$HERE/http_test.uc" || fail=1
kill "$echo_pid" 2>/dev/null

echo "== watchdog"
sh "$HERE/watchdog_test.sh" || fail=1

echo "== render (sing-box config + firewall)"
UCODE="$UCODE" UCODE_LIB="$UCODE_LIB" sh "$HERE/render_test.sh" || fail=1

exit $fail
