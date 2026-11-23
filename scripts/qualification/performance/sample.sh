#!/bin/sh
# Run on the test router while an external LAN client drives a fixed workload.
# Stream to the controller over SSH; keep the router's flash out of the test.
set -eu
interval=${1:-5}
case "$interval" in ''|*[!0-9]*|0) exit 2;; esac
printf 'uptime_seconds,process_kib,rss_fallback,cpu_ticks,total_cpu_ticks,available_kib,tmp_used_kib\n'
while :; do
 used=0; ticks=0; rss=0
 for entry in /proc/[0-9]*/comm; do
  [ -r "$entry" ] || continue
  name=$(cat "$entry" 2>/dev/null) || continue
  case "$name" in hyperd|hyper-*|mihomo) ;; *) continue;; esac
  proc=${entry%/comm}
  if [ -r "$proc/smaps_rollup" ]; then kb=$(awk '$1=="Pss:" {print $2; exit}' "$proc/smaps_rollup" 2>/dev/null) || continue
  else kb=$(awk '$1=="VmRSS:" {print $2; exit}' "$proc/status" 2>/dev/null) || continue;rss=1;fi
  cpu=$(awk '{print $14+$15}' "$proc/stat" 2>/dev/null) || continue
  [ -n "$kb" ] && [ -n "$cpu" ] || continue
  used=$((used+kb));ticks=$((ticks+cpu))
 done
 total=$(awk '$1=="cpu" {for(i=2;i<=9;i++) v+=$i; printf "%.0f\n",v;exit}' /proc/stat)
 avail=$(awk '$1=="MemAvailable:" {print $2;exit}' /proc/meminfo)
 tmp=$(df -Pk /tmp | awk 'END {print $3}')
 up=$(cut -d' ' -f1 /proc/uptime)
 printf '%s,%s,%s,%s,%s,%s,%s\n' "$up" "$used" "$rss" "$ticks" "$total" "${avail:-0}" "$tmp"
 sleep "$interval"
done
