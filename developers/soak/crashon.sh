#!/usr/bin/env bash
# crashon.sh <outdir> <nStarts> <trigger> <delaySecs>: an extra crash restart
# like crashafter.sh, timed to land while start reports are in flight. Once
# restarts.tsv has nStarts start lines, it waits for the next <trigger>,
# "burst" (a new portal burst line in events.tsv) or "stall" (a STALL START
# line in stall.log, so USE_FUSE=1 only), then delaySecs, then kills -9 our
# (verified) manager and starts it again on the same DB, recording both in
# restarts.tsv with manual=crashon-<trigger>.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 4 ] || die "usage: crashon.sh <outdir> <nStarts> burst|stall <delaySecs>"
d=$(soak_outdir "$1") || exit 1
n=$2 trig=$3 wait=$4
case $trig in
  burst) f=$d/events.tsv pat='	portal	burst ' ;;
  stall) [ "$USE_FUSE" = 1 ] || die "the stall trigger needs USE_FUSE=1 (stall.sh makes stall.log)"
         f=$d/stall.log pat=' STALL START' ;;
  *) die "unknown trigger $trig" ;;
esac
soak_enter "$d"
until [ "$(grep -c '	start	' "$d/restarts.tsv")" -ge "$n" ]; do soak_alive "$d" || exit 0; sleep 5; done
c0=$(grep -c "$pat" "$f" 2>/dev/null); c0=${c0:-0}
until c=$(grep -c "$pat" "$f" 2>/dev/null); [ "${c:-0}" -gt "$c0" ]; do soak_alive "$d" || exit 0; sleep 1; done
echo "$(date +%s) crashon: $trig seen: $(grep "$pat" "$f" | tail -1); crashing in ${wait}s" >> "$d/watcher.log"
sleep "$wait"
crash() {
  soak_isolated || die "wr does not resolve --deployment production to our manager on :$PROD_PORT"
  pid=$(soak_manager_pid) || die "the pid file does not name a process running $SOAK_WR"
  lsfrun=$(our_running_jobs)
  t0=$(date +%s%3N); kill -9 "$pid"
  echo "$(date +%s)	stop	rc=crash	pid=$pid	ms=$(( $(date +%s%3N) - t0 ))	kind=crash	manual=crashon-$trig	lsfRUN=$lsfrun	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
  cp -f "$SOAK_RUN/log" "$d/manager.log.$(date +%s)" 2>/dev/null
  soak_wait_gone "$pid" "$d" crashon; sleep 3; t0=$(date +%s%3N)
  soak_start_manager "$d"
  rc=$?
  echo "$(date +%s)	start	rc=$rc	pid=$(cat "$SOAK_RUN/pid")	ms=$(( $(date +%s%3N) - t0 ))	manual=crashon-$trig	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
}
# never collide with a scheduled restart part-way through, or it with this one
soak_restart_locked "$d" crash
