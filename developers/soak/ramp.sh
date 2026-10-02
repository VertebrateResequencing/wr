#!/usr/bin/env bash
# ramp.sh <outdir> "<min>:<target> <min>:<target> ...": the portal concurrency
# ramp. At each stage's start (real minutes after launch) writes the target to
# $SOAK_ROOT/portal_target (prodsim sizes its bursts from it) and sets the
# results_portal limit group to it on OUR isolated manager only (checked with
# wr conf first). Logs to <outdir>/ramp.log.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 2 ] || die "usage: ramp.sh <outdir> \"<min>:<target> ...\""
d=$(soak_outdir "$1") || exit 1
stages=$2
soak_enter "$d"
t0=$(date +%s)
for st in $stages; do
  m=${st%%:*} c=${st##*:}
  while [ $(( $(date +%s) - t0 )) -lt $(( m * 60 )) ]; do
    soak_alive "$d" || exit 0
    sleep 10
  done
  echo "$c" > "$SOAK_ROOT/portal_target"
  lim=unset
  if soak_isolated; then
    for _ in 1 2 3 4 5; do timeout 60 "$SOAK_WR" limit -g "results_portal:$c" --deployment production >/dev/null 2>&1 && break; sleep 20; done
    lim=$(timeout 60 "$SOAK_WR" limit -g results_portal --deployment production 2>/dev/null | tail -1)
  fi
  echo "$(date +%s) stage t+${m}m target=$c results_portal=$lim load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/ramp.log"
done
