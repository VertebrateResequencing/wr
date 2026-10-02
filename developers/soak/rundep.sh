#!/usr/bin/env bash
# rundep.sh <outdir> [intervalSecs=240] [lastStartSecs=0]: the #649 case (a
# job whose dependency group gains a member while it runs) at soak scale.
# Every interval, while prodsim runs, one instance i: job A.i (dep group
# rdg.i) runs and ends; D.i (--deps rdg.i, 90s) starts; 15s after D.i starts,
# B.i joins rdg.i (30s on odd i, so it ends before D.i's first run; 150s on
# even i, so D.i waits dependent for it). D.i must run exactly twice, its
# second run starting after B.i ended. Marks go to <outdir>/rundep/<host>.tsv
# (S|E, name, epoch ms, host, pid, LSF job, rc), so <outdir> must be visible
# from every exec node; no instance starts after lastStartSecs from launch (0:
# no limit); the driver's own log is <outdir>/rundep/driver.log.
# rundepcheck.py gives the verdicts.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -ge 1 ] || die "usage: rundep.sh <outdir> [intervalSecs] [lastStartSecs]"
d=$(soak_outdir "$1") || exit 1
gap=${2:-240} last=${3:-0} T0=$(date +%s)
soak_enter "$d"
R=$d/rundep; mkdir -p "$R"
alive() { soak_alive "$d"; }
log() { echo "$(date +%s%3N) $*" >> "$R/driver.log"; }
job() { # name secs (no literal tabs: wr add -f splits its input lines on them)
  echo "h=\$(hostname -s); printf 'S\\t$1\\t%s\\t%s\\t%s\\t%s\\t\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" >> $R/\$h.tsv; sleep $2; rc=\$?; printf 'E\\t$1\\t%s\\t%s\\t%s\\t%s\\t%s\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" \$rc >> $R/\$h.tsv; exit \$rc"
}
add() { # name secs repgrp args... ; retried while the manager is down, for up to 10 minutes
  local n=$1 s=$2 rg=$3 t0; shift 3; t0=$(date +%s)
  while [ $(( $(date +%s) - t0 )) -lt 600 ]; do
    out=$(job "$n" "$s" | timeout 90 "$SOAK_WR" add --deployment production -i "$rg" -r 0 -m 100M -t 10m --queue "$QUEUE" "$@" 2>&1 | tail -1)
    log "add $n: $out"
    case "$out" in (*Added*|*duplicates*) return 0 ;; esac
    alive || return 1; sleep 15
  done
  return 1
}
seen() { cat "$R"/*.tsv 2>/dev/null | awk -F'\t' -v k="$1" -v n="$2" '$1==k && $2==n' | wc -l; }
waitfor() { # kind name count timeoutSecs
  local t0; t0=$(date +%s)
  until [ "$(seen "$1" "$2")" -ge "$3" ]; do
    alive || return 1; [ $(( $(date +%s) - t0 )) -ge "$4" ] && return 1; sleep 3
  done
}
instance() {
  local i=$1 g=rdg.$1 bs=150; [ $(( i % 2 )) = 1 ] && bs=30
  add A.$i 5 rundep.A.$i -e $g || { log "instance $i: A not added"; return; }
  waitfor E A.$i 1 1800 || { log "instance $i: A never ended"; return; }
  add D.$i 90 rundep.D.$i -d $g || { log "instance $i: D not added"; return; }
  waitfor S D.$i 1 1800 || { log "instance $i: D never started"; return; }
  sleep 15
  log "instance $i: D running; adding B.$i (${bs}s) to $g"
  add B.$i $bs rundep.B.$i -e $g || { log "instance $i: B not added"; return; }
  log "instance $i: B added"
  waitfor E D.$i 2 1800 || log "instance $i: D did not end twice within 30m"
  sleep 5
  log "instance $i: D status: $(timeout 90 "$SOAK_WR" status --deployment production -i rundep.D.$i 2>&1 | grep -E '^(Status|Id):' | tr '\n' ' ')"
}
i=0
while alive; do
  [ "$last" -gt 0 ] && [ $(( $(date +%s) - T0 )) -gt "$last" ] && break
  i=$(( i + 1 )); instance $i &
  for _ in $(seq 1 $(( gap / 5 ))); do alive || break; sleep 5; done
done
wait
log "prodsim gone; $i instances"
