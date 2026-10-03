#!/usr/bin/env bash
# stall.sh <outdir>: induces one commit stall (USE_FUSE=1 only; run.sh starts
# it). STALL_AFTER_MIN minutes after hook.sh put the DB behind fusestall, holds
# every fsync for STALL_SECS by creating the control file, with goroutine dumps
# every 20s and a CPU profile during it, then releases. Logs to stall.log.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: stall.sh <outdir>"
[ "$USE_FUSE" = 1 ] || die "stall.sh needs USE_FUSE=1"
d=$(soak_outdir "$1") || exit 1
P=$d/profiles
mkdir -p "$P"
log() { echo "$(date +%s) $*" >> "$d/stall.log"; }
while [ ! -e "$d/fuse.on" ]; do soak_alive "$d" || exit 0; sleep 10; done
on=$(cat "$d/fuse.on"); at=$(( on + ${STALL_AFTER_MIN:-15} * 60 ))
log "db behind fusestall since $on; stall due at $at"
while [ "$(date +%s)" -lt $at ]; do soak_alive "$d" || exit 0; sleep 5; done
pid=$(cat "$SOAK_RUN/pid" 2>/dev/null)
case "$(readlink "$SOAK_RUN/db")" in ("$FUSE_MNT"/*) ;; (*) log "db is not behind fusestall; no stall"; exit 0 ;; esac
[ -e "$d/fuse.off" ] && { log "fuse segment already over; no stall"; exit 0; }
touch "$STALL_CTL"; t0=$(date +%s); log "STALL START pid=$pid lsfRUN=$(our_running_jobs) load=$(cut -d' ' -f1 /proc/loadavg)"
( curl -s -m 60 "http://localhost:$PPROF_PORT/debug/pprof/profile?seconds=45" > "$P/stall.$t0.cpu.pprof" ) &
while [ $(( $(date +%s) - t0 )) -lt "${STALL_SECS:-180}" ]; do
  ts=$(date +%s); curl -s -m 15 "http://localhost:$PPROF_PORT/debug/pprof/goroutine?debug=1" > "$P/stall.$ts.goroutine.txt"
  log "stalled $(( ts - t0 ))s goroutines=$(head -1 "$P/stall.$ts.goroutine.txt" | grep -oE 'total [0-9]+') held=$(grep -c holding "$d/fusestall.log") lsfRUN=$(our_running_jobs)"
  sleep 20
done
rm -f "$STALL_CTL"; log "STALL END after $(( $(date +%s) - t0 ))s"
for _ in 1 2 3 4 5 6; do sleep 20; ts=$(date +%s); curl -s -m 15 "http://localhost:$PPROF_PORT/debug/pprof/goroutine?debug=1" > "$P/poststall.$ts.goroutine.txt"; log "post-stall +$(( ts - t0 ))s goroutines=$(head -1 "$P/poststall.$ts.goroutine.txt" | grep -oE 'total [0-9]+')"; done
wait
