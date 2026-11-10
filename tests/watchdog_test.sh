#!/bin/sh
# Drive watchdog.sh through: healthy -> 3 failures -> fail-open -> healthy -> restart.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
WATCHDOG="$HERE/../luci-app-hyper/root/etc/hyper/scripts/watchdog.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/init" "$T/run" "$T/dnsmasq.d/dnsmasq-hyper.d"
touch "$T/dnsmasq.d/dnsmasq-hyper.conf" "$T/dnsmasq.d/dnsmasq-hyper.d/redirect-dns.conf"
echo "rules" > "$T/run/fw4_post.nft"

# curl answers from a script of results, one per call: 200 = healthy
printf '200\n000\n000\n000\n000\n200\n' > "$T/curl.script"
cat > "$T/bin/curl" <<EOS
#!/bin/sh
code=\$(head -n1 "$T/curl.script"); sed -i.bak 1d "$T/curl.script"
echo "\${code:-200}"
EOS
cat > "$T/bin/uci" <<EOS
#!/bin/sh
case "\$*" in *dnsmasq*) echo "dhcp.cfg01=dnsmasq" ;; *clash_api_port*) echo 5334 ;; *) ;; esac
EOS
echo '#!/bin/sh' > "$T/bin/fw4"; echo "echo fw4 \$* >> $T/calls" >> "$T/bin/fw4"
for s in dnsmasq hyper; do printf '#!/bin/sh\necho %s $* >> %s/calls\n' "$s" "$T" > "$T/init/$s"; done
chmod +x "$T"/bin/* "$T"/init/*
# the watchdog derives the dnsmasq dir from $DNSMASQ_ETC/dnsmasq.conf.<cfg>
mkdir -p "$T/etc"; echo "conf-dir=$T/dnsmasq.d" > "$T/etc/dnsmasq.conf.cfg01"

PATH="$T/bin:$PATH" RUN_DIR="$T/run" INIT_DIR="$T/init" DNSMASQ_ETC="$T/etc" INTERVAL=0 FAIL_LIMIT=3 timeout 10 sh "$WATCHDOG"
rc=$?

fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got [$2] want [$3]"; fail=1; fi; }
check "exits after recovery"       "$rc" "0"
check "dnsmasq hijack removed"     "$([ -e "$T/dnsmasq.d/dnsmasq-hyper.d" ] && echo present || echo removed)" "removed"
check "firewall rules emptied"     "$(wc -c < "$T/run/fw4_post.nft" | tr -d ' ')" "0"
check "dnsmasq restarted once"     "$(grep -c '^dnsmasq restart' "$T/calls")" "1"
check "fw4 reloaded once"          "$(grep -c '^fw4 reload' "$T/calls")" "1"
sleep 1
check "service restarted on recovery" "$(grep -c '^hyper restart' "$T/calls")" "1"
check "failopen flag cleared"      "$([ -e "$T/run/failopen" ] && echo set || echo cleared)" "cleared"
check "log mentions fail-open"     "$(grep -c 'released DNS and firewall' "$T/run/hyper.log")" "1"
exit $fail
