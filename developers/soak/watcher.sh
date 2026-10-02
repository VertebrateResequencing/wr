#!/usr/bin/env bash
# watcher.sh <outdir>: restarts our isolated manager if a scheduled start
# failed (rc != 0 on a start line of restarts.tsv), after logging which
# sockets hold our ports, to watcher.log.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: watcher.sh <outdir>"
d=$(soak_outdir "$1") || exit 1
soak_enter "$d"
seen=$(wc -l < "$d/restarts.tsv")
while soak_alive "$d"; do
  n=$(wc -l < "$d/restarts.tsv")
  if [ "$n" -gt "$seen" ]; then
    seen=$n
    last=$(tail -1 "$d/restarts.tsv")
    case "$last" in
      *start*rc=[1-9]*)
        echo "$(date +%s) watcher: scheduled start failed: $last; bind errors: $(grep -c 'address already in use' "$d/manager-start.out")" >> "$d/watcher.log"
        ss -tan | grep -E ":$PROD_PORT |:$PROD_WEB " >> "$d/watcher.log"
        sleep 65
        if soak_isolated && ! soak_wr_running start >/dev/null; then
          t0=$(date +%s%3N)
          soak_start_manager "$d"
          rc=$?
          echo "$(date +%s)	start	rc=$rc	pid=$(cat "$SOAK_RUN/pid")	ms=$(( $(date +%s%3N) - t0 ))	watcher-after-failure	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
          seen=$(wc -l < "$d/restarts.tsv")
        fi ;;
    esac
  fi
  sleep 10
done
echo "$(date +%s) watcher: prodsim gone, exiting" >> "$d/watcher.log"
