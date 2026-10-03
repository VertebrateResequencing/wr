#!/usr/bin/env bash
# relbury.sh <outdir> <nStarts> <tag> <nJobs> <killDelayMs> [leadSecs=300]:
# the #654 case (releases and buries arriving together just before a kill -9
# of the manager) at soak scale. Once restarts.tsv has nStarts start lines
# (and no stop/start is in progress), wait 90s, then add nJobs/2 jobs with
# --retries 1 (rep group relb.<tag>.rel: the first failure is a release, the
# second a bury) and nJobs/2 with --retries 0 (relb.<tag>.bur: the first
# failure is a bury). Every job sleeps until one shared deadline D
# (now+leadSecs), then exits 3, so all the releases and buries arrive
# together. killDelayMs after D, our (verified) manager is killed -9 and
# started again on the same DB (restarts.tsv, manual=relbury-<tag>). Status
# snapshots of both groups (wr status -o json --limit 0) are taken as soon as
# the manager is back, and at +3m, +10m and +25m, into
# <outdir>/relbury/<tag>.<label>.json. Marks (S|E, name, epoch ms, host, pid,
# LSF job, rc) go to <outdir>/relbury/<host>.tsv, so <outdir> must be visible
# from every exec node. relburycheck.py gives the verdicts.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -ge 5 ] || die "usage: relbury.sh <outdir> <nStarts> <tag> <nJobs> <killDelayMs> [leadSecs]"
d=$(soak_outdir "$1") || exit 1
n=$2 tag=$3 nj=$4 kdms=$5 lead=${6:-300}
soak_enter "$d"
R=$d/relbury; mkdir -p "$R"
alive() { soak_alive "$d"; }
log() { echo "$(date +%s%3N) $tag $*" >> "$R/driver.log"; }
busy() { soak_wr_running "(start|stop)" >/dev/null; }
until [ "$(grep -c '	start	' "$d/restarts.tsv")" -ge "$n" ]; do alive || exit 0; sleep 5; done
sleep 90; while busy; do sleep 5; done
dms=$(( ($(date +%s) + lead) * 1000 ))
job() { # name: no literal tabs, since wr add -f splits its input lines on them
  echo ": relb $1; h=\$(hostname -s); printf 'S\\t$1\\t%s\\t%s\\t%s\\t%s\\t\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" >> $R/\$h.tsv; n=\$(date +%s%3N); s=\$(( $dms - n )); [ \$s -gt 0 ] && sleep \$(printf '%d.%03d' \$(( s / 1000 )) \$(( s % 1000 ))); printf 'E\\t$1\\t%s\\t%s\\t%s\\t%s\\t3\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" >> $R/\$h.tsv; exit 3"
}
add() { # kind retries
  local t0 out; t0=$(date +%s)
  while [ $(( $(date +%s) - t0 )) -lt 120 ]; do
    out=$(for j in $(seq 1 $(( nj / 2 ))); do job "relb.$tag.$1.$j"; done \
      | timeout 120 "$SOAK_WR" add --deployment production -f - -i "relb.$tag.$1" -r "$2" -m 100M -t 10m --queue "$QUEUE" 2>&1 | tail -1)
    log "add $1 (-r $2): $out"
    case "$out" in (*Added*|*duplicates*) return 0 ;; esac
    alive || return 1; sleep 10
  done
  return 1
}
add rel 1; add bur 0
log "deadline D=$dms; kill at D+${kdms}ms"
until [ "$(date +%s%3N)" -ge $(( dms + kdms )) ]; do sleep 0.05; done
if busy; then log "a manager stop/start is in progress at D+${kdms}ms; no relbury crash"; exit 0; fi
soak_isolated || { log "not our manager config"; exit 1; }
pid=$(soak_manager_pid) || { log "pid $(cat "$SOAK_RUN/pid" 2>/dev/null) is not ours"; exit 1; }
t0=$(date +%s%3N); kill -9 "$pid"
started=$(cat "$R"/*.tsv 2>/dev/null | awk -F'\t' -v p="relb.$tag." '$1=="S" && index($2,p)==1' | wc -l)
ended=$(cat "$R"/*.tsv 2>/dev/null | awk -F'\t' -v p="relb.$tag." '$1=="E" && index($2,p)==1' | wc -l)
log "killed pid $pid at $t0 (D+$(( t0 - dms ))ms); marks so far S=$started E=$ended"
echo "$(date +%s)	stop	rc=crash	pid=$pid	ms=$(( $(date +%s%3N) - t0 ))	kind=crash	manual=relbury-$tag	killms=$t0	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
cp -f "$SOAK_RUN/log" "$d/manager.log.$(date +%s)" 2>/dev/null
sleep 3; t0=$(date +%s%3N)
soak_start_manager "$d"
rc=$?
echo "$(date +%s)	start	rc=$rc	pid=$(cat "$SOAK_RUN/pid")	ms=$(( $(date +%s%3N) - t0 ))	manual=relbury-$tag	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
up=$(date +%s)
snap() { # label
  local g
  for g in rel bur; do
    timeout 300 "$SOAK_WR" status --deployment production -i "relb.$tag.$g" -o json --limit 0 > "$R/$tag.$1.$g.json" 2> "$R/$tag.$1.$g.err"
    log "snapshot $1 $g rc=$? at $(date +%s%3N): $(grep -o '"State":"[a-z]*"' "$R/$tag.$1.$g.json" | sort | uniq -c | tr '\n' ' ')"
  done
}
snap up
for w in 180 600 1500; do
  while [ $(( $(date +%s) - up )) -lt $w ]; do alive || exit 0; sleep 5; done
  while busy; do sleep 5; done
  snap "+$w"
done
