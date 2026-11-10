#!/bin/sh
# Run tests/run.sh on a real OpenWrt userland: the firmware's own ucode, nftables
# and curl, plus the bundled libbox core for `sing-box check`. Catches scripts
# that need a newer ucode than an older firmware (23.05) ships.
#
#   tests/router-test.sh <openwrt rootfs image> <bin dir | package> [docker platform]
#
#   bin dir   the staged sing-box (and hyper-crypt) from scripts/stage-binaries.sh;
#             the package files are copied from the source tree, and every
#             LUCI_DEPENDS entry must resolve in this firmware's feeds
#   package   a built .ipk / .apk, installed with opkg / apk (dependencies included)
#
#   e.g. tests/router-test.sh openwrt/rootfs:aarch64_generic-24.10.8 /tmp/core linux/aarch64_generic
#        tests/router-test.sh openwrt-rootfs:23.05.6 dist/23.05-24.10_luci-app-hyper_0.1.0-r1_x86_64.ipk
#
# Then checks the installed layout: the scripts find their modules from the real
# paths (no -L), the bundled programs run, and rpcd serves luci.hyper.
#
# Needs Docker and network access (installs the test tools and the package's
# dependencies from the image's feeds).
set -eu

IMAGE="$1"
SRC="$2"
PLATFORM="${3:-}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT INT TERM
mkdir -p "$stage/src/luci-app-hyper" "$stage/src/bin" "$stage/src/pkg"
cp -R "$ROOT/tests" "$stage/src/"
cp -R "$ROOT/luci-app-hyper/root" "$stage/src/luci-app-hyper/"
cp "$ROOT/luci-app-hyper/Makefile" "$stage/src/"
case "$SRC" in
	*.ipk|*.apk) cp "$SRC" "$stage/src/pkg/" ;;
	*) cp "$SRC"/* "$stage/src/bin/" ;;
esac

cat > "$stage/src/run-inner.sh" <<'INNER'
set -eu
mkdir -p /var/lock /var/run/ubus /tmp/lock /tmp/run
if command -v apk >/dev/null 2>&1; then
	pm_update() { apk update >/dev/null; }
	pm_add() { apk add "$@" >/dev/null; }
	pm_try() { apk add --simulate "$@"; }
	pm_file() { apk add --allow-untrusted "$1"; }
else
	pm_update() { opkg update >/dev/null; }
	pm_add() { opkg install "$@" >/dev/null; }
	pm_try() { opkg install --noaction "$@"; }
	pm_file() { opkg install "$1"; }
fi
pm_update
# python3 (echo server, JSON edits), coreutils-timeout (watchdog test)
pm_add python3 curl coreutils-timeout
. /etc/openwrt_release
echo "== $DISTRIB_DESCRIPTION ($DISTRIB_ARCH)"
rc=0

echo "== install"
pkg="$(ls /src/pkg/* 2>/dev/null | head -n1 || true)"
if [ -n "$pkg" ]; then
	if pm_file "$pkg" >/tmp/install.log 2>&1; then
		echo "ok   $(basename "$pkg") installs with its dependencies"
	else
		echo "FAIL $(basename "$pkg") does not install:"; tail -n 30 /tmp/install.log; exit 1
	fi
else
	deps="$(sed -n '/^LUCI_DEPENDS/,/^$/p' /src/Makefile | grep -o '+[A-Za-z0-9_.-]*' | tr -d +)"
	if out="$(pm_try $deps 2>&1)"; then
		echo "ok   dependencies resolve: $(echo $deps)"
	else
		echo "FAIL dependencies do not resolve:"; echo "$out" | tail -n 20; rc=1
	fi
	# the package's own files, laid out as opkg / apk would
	pm_add luci-base rpcd-mod-ucode
	cp -R /src/luci-app-hyper/root/. /
	mkdir -p /usr/libexec/hyper/bin && cp /src/bin/* /usr/libexec/hyper/bin/ && chmod +x /usr/libexec/hyper/bin/*
fi
BIN=/usr/libexec/hyper/bin
if v="$("$BIN/sing-box" version | head -n1)"; then echo "ok   $v"; else echo "FAIL bundled sing-box does not run"; rc=1; fi
if [ -e "$BIN/hyper-crypt" ]; then
	if "$BIN/hyper-crypt" keygen | grep -q '"private_key"'; then echo "ok   bundled hyper-crypt runs"; else echo "FAIL bundled hyper-crypt does not run"; rc=1; fi
fi

cd /src
PATH="$BIN:$PATH" UCODE=/usr/bin/ucode UCODE_LIB=/usr/lib/ucode sh tests/run.sh || rc=1

echo "== installed layout"
cd /tmp
for f in /etc/hyper/scripts/generate_client.uc /etc/hyper/scripts/firewall_pre.uc /etc/hyper/scripts/sync_subscription.uc; do
	# the init script and cron run these as plain `ucode -S <file>`, without -L
	if ucode -S -c -o /dev/null "$f"; then echo "ok   $f resolves its imports"; else echo "FAIL $f"; rc=1; fi
done
/sbin/ubusd >/dev/null 2>&1 &
sleep 1
/sbin/rpcd >/dev/null 2>&1 &
if ubus -t 10 wait_for luci.hyper && ubus -v list luci.hyper | grep -q '"checkin_status"'; then
	echo "ok   rpcd serves luci.hyper ($(ubus -v list luci.hyper | grep -c '^[[:space:]]*"') methods)"
	if res="$(ubus call luci.hyper checkin_status)"; then echo "ok   ubus call luci.hyper checkin_status: $(echo $res | head -c 120)"
	else echo "FAIL ubus call luci.hyper checkin_status"; rc=1; fi

	# The UI polls status every few seconds: it must start sing-box for the core
	# version only once, and again only after the binary changes
	mv "$BIN/sing-box" "$BIN/sing-box.real"
	printf '#!/bin/sh\necho run >> /tmp/sing-box-runs\nexec %s/sing-box.real "$@"\n' "$BIN" > "$BIN/sing-box"
	chmod +x "$BIN/sing-box"
	for i in 1 2 3; do ubus call luci.hyper status >/dev/null; done
	runs1=$(($(cat /tmp/sing-box-runs 2>/dev/null | wc -l)))
	echo '# changed' >> "$BIN/sing-box"
	ubus call luci.hyper status >/dev/null
	runs2=$(($(cat /tmp/sing-box-runs 2>/dev/null | wc -l)))
	mv "$BIN/sing-box.real" "$BIN/sing-box"
	if [ "$runs1" = 1 ] && [ "$runs2" = 2 ]; then echo "ok   status starts sing-box once for its version (cached until the binary changes)"
	else echo "FAIL status started sing-box $runs1 time(s) for 3 calls, $runs2 after the binary changed (want 1, 2)"; rc=1; fi

	# check_update records when it ran, also on a fresh install (no /var/run/hyper yet)
	# and when GitHub is unreachable, so the daily check does not repeat on every page load
	rm -rf /var/run/hyper
	ubus -t 60 call luci.hyper check_update >/dev/null 2>&1 || true
	checked="$(ubus call luci.hyper status | jsonfilter -e '@.update.checked' || true)"
	if [ -n "$checked" ]; then echo "ok   check_update records when it ran"
	else echo "FAIL check_update did not record the check"; rc=1; fi

	# Update hint from the last daily check: a newer release shows, the installed one does not
	mkdir -p /var/run/hyper
	installed="$(ubus call luci.hyper status | jsonfilter -e '@.version' || true)"
	echo "{\"time\": $(date +%s), \"latest\": \"9.9.9\"}" > /var/run/hyper/update_check.json
	newer="$(ubus call luci.hyper status | jsonfilter -e '@.update.available' || true)"
	same=false
	if [ -n "$installed" ]; then
		# the release tag has no -r<PKG_RELEASE>: v0.1.0 is the installed 0.1.0-r1
		echo "{\"time\": $(date +%s), \"latest\": \"${installed%-r*}\"}" > /var/run/hyper/update_check.json
		same="$(ubus call luci.hyper status | jsonfilter -e '@.update.available' || true)"
	fi
	rm -f /var/run/hyper/update_check.json
	if [ "$newer" = true ] && [ "$same" = false ]; then echo "ok   update hint: 9.9.9 is newer, ${installed:-(not installed)} is current"
	else echo "FAIL update hint: newer=$newer, same version ($installed) available=$same"; rc=1; fi
else
	echo "FAIL rpcd does not serve luci.hyper"; rc=1
fi
exit $rc
INNER

# shellcheck disable=SC2086
tar -C "$stage" -cf - src | docker run --rm -i --cap-add NET_ADMIN ${PLATFORM:+--platform "$PLATFORM"} "$IMAGE" \
	sh -c 'tar -C / -xf - && sh /src/run-inner.sh'
