#!/usr/bin/env bash
# mon.sh <outdir>: a status line every 10 min, plus new restart, ramp, watcher,
# stall, hook and spike lines as they appear, until the soak ends. Run it in a
# terminal beside run.sh.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: mon.sh <outdir>"
d=$(soak_outdir "$1") || exit 1
declare -A seen
emit_new() { local f=$1 tag=$2 n; n=$({ wc -l < "$f"; } 2>/dev/null || echo 0); if [ "$n" -gt "${seen[$f]:-0}" ]; then tail -n $(( n - ${seen[$f]:-0} )) "$f" | sed "s/^/$tag /" | cut -c1-400; seen[$f]=$n; fi; }
for f in "$d/restarts.tsv" "$d/ramp.log" "$d/watcher.log" "$d/stall.log" "$d/hook.log"; do seen[$f]=$({ wc -l < "$f"; } 2>/dev/null || echo 0); done
i=0
while soak_alive "$d" || pgrep -f 'wrdev.sh prodsim' >/dev/null; do
  emit_new "$d/restarts.tsv" RESTART; emit_new "$d/ramp.log" RAMP; emit_new "$d/watcher.log" WATCH; emit_new "$d/stall.log" STALL; emit_new "$d/hook.log" HOOK
  n=$(grep -c 'spike' "$d/events.tsv" 2>/dev/null); if [ "${n:-0}" -gt "${seen[spike]:-0}" ]; then grep spike "$d/events.tsv" | tail -n $(( n - ${seen[spike]:-0} )) | sed 's/^/SPIKE /'; seen[spike]=$n; fi
  if [ $(( i % 60 )) -eq 0 ]; then
    s=$(tail -1 "$d/samples.tsv" | awk -F'\t' '{print "rss="$3" gor="$7" heap="$8" fds="$6" db="$10" ping="$13" sub="$14" load="$15}')
    lg=$SOAK_RUN/log
    echo "STATUS $(date +%T) lsf[$(tail -1 "$d/lsf.tsv" 2>/dev/null | cut -f2)] $s errs=$(grep -ac 'lvl=eror' "$lg" 2>/dev/null) slow=$(grep -ac 'slow request' "$lg" 2>/dev/null) anyway=$(grep -ac 'anyway' "$lg" 2>/dev/null) rootfree=$(df -B1G --output=avail "$SOAK_ROOT" | tail -1 | tr -d ' ')G"
  fi
  i=$(( i + 1 )); sleep 10
done
echo "prodsim ended"
