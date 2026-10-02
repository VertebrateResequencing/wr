#!/usr/bin/env bash
# relburyiso.sh <killDelayMs>...: relbury.sh's #654 batches on a quiet
# isolated LSF manager (LSF only: bjobs, bsub; WRDEV_ROOT
# $REPRO_ROOT/relburyiso; REPRO_ROOT, REPRO_WR, REPRO_PORTS and WRDEV as for
# readdcrash.sh; QUEUE the LSF queue, default normal), where acks are quick, so
# that a kill -9 lands with some releases and buries already acknowledged.
# One batch per delay (tags k<delay>): 100 jobs --retries 1 (relb.<tag>.rel)
# and 100 --retries 0 (relb.<tag>.bur), all exiting 3 at a shared deadline D;
# at D+delay the manager is killed -9 and started again on the same DB, with
# status snapshots at up, +180s and +600s. Output (relbury/ marks, driver.log,
# snapshots, restarts.tsv, runner logs) is laid out for
# relburycheck.py <root> <root>/runnerlogs.
set -u
[ $# -ge 1 ] || { echo "usage: relburyiso.sh <killDelayMs>..." >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
W=${WRDEV:-$(git -C "$here" rev-parse --show-toplevel)/developers/wrdev.sh}
read -r DEV_PORT DEV_WEB PROD_PORT PROD_WEB <<< "${REPRO_PORTS:-51876 51877 51878 51879}"
export WRDEV_ROOT=${REPRO_ROOT:?set REPRO_ROOT to a scratch directory}/relburyiso DEV_PORT DEV_WEB PROD_PORT PROD_WEB
command -v bjobs >/dev/null || { echo "relburyiso.sh needs LSF (bjobs) on PATH" >&2; exit 1; }
# the root is wiped below: never while a manager or runner, here or on an LSF
# exec node, still runs its binary
pgrep -f "$WRDEV_ROOT/wr" >/dev/null && { echo "a process is running $WRDEV_ROOT/wr*; not wiping it" >&2; exit 1; }
JOBP="wrp${PROD_JOBTOKEN:-iso$PROD_PORT}_"
# bjobs exits 0 with "Job <...> is not found" on stderr when nothing matches,
# and non-zero (124 on timeout) when it could not ask LSF: fail closed then
out=$(timeout 60 bjobs -J "${JOBP}*" -o jobid -noheader 2>/dev/null); rc=$?
[ "$rc" -eq 0 ] || { echo "could not ask LSF for ${JOBP}* jobs (bjobs exit $rc); not wiping $WRDEV_ROOT" >&2; exit 1; }
[ -z "$out" ] || { echo "LSF still has ${JOBP}* jobs, whose runners may use $WRDEV_ROOT/wr; not wiping it" >&2; exit 1; }
unset $(compgen -v | grep '^OS_')
QUEUE=${QUEUE:-normal}
rm -rf "$WRDEV_ROOT"; mkdir -p "$WRDEV_ROOT/work" "$WRDEV_ROOT/runnerlogs" "$WRDEV_ROOT/relbury"
cp "${REPRO_WR:?set REPRO_WR to a wr binary}" "$WRDEV_ROOT/wr.real"
printf '%s\n' '#!/bin/bash' \
  "if [ \"\$1\" = manager ] && [ \"\$2\" = start ]; then exec $WRDEV_ROOT/wr.real \"\$@\" --runner_filelog $WRDEV_ROOT/runnerlogs; fi" \
  "exec $WRDEV_ROOT/wr.real \"\$@\"" > $WRDEV_ROOT/wr
chmod 755 "$WRDEV_ROOT/wr"
cd "$WRDEV_ROOT/work" || exit 1; export WR_CONFIG_DIR=$WRDEV_ROOT/config
WR=$WRDEV_ROOT/wr R=$WRDEV_ROOT/relbury D=$WRDEV_ROOT
log() { echo "$(date +%s%3N) $tag $*" >> $R/driver.log; }
start() { local t0; t0=$(date +%s%3N); $W prod-start lsf | tail -1
  echo "$(date +%s)	start	rc=0	pid=$(cat $D/.wr-prod_production/pid)	ms=$(( $(date +%s%3N) - t0 ))	$1" >> $D/restarts.tsv; }
start initial
for kd in "$@"; do
  tag=k$kd dms=$(( ($(date +%s) + 150) * 1000 ))
  job() {
    echo ": relb $1; h=\$(hostname -s); printf 'S\\t$1\\t%s\\t%s\\t%s\\t%s\\t\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" >> $R/\$h.tsv; n=\$(date +%s%3N); s=\$(( $dms - n )); [ \$s -gt 0 ] && sleep \$(printf '%d.%03d' \$(( s / 1000 )) \$(( s % 1000 ))); printf 'E\\t$1\\t%s\\t%s\\t%s\\t%s\\t3\\n' \$(date +%s%3N) \$h \$\$ \"\${LSB_JOBID:-}\" >> $R/\$h.tsv; exit 3"
  }
  for g in rel:1 bur:0; do
    out=$(for j in $(seq 1 100); do job "relb.$tag.${g%%:*}.$j"; done \
      | timeout 120 $WR add --deployment production -f - -i "relb.$tag.${g%%:*}" -r "${g##*:}" -m 100M -t 10m --queue "$QUEUE" 2>&1 | tail -1)
    log "add ${g%%:*} (-r ${g##*:}): $out"
  done
  log "deadline D=$dms; kill at D+${kd}ms"
  until [ "$(date +%s%3N)" -ge $(( dms + kd )) ]; do sleep 0.02; done
  pid=$(cat $D/.wr-prod_production/pid); ps -ww -o cmd= -p $pid | grep -qF "$WRDEV_ROOT/wr" || { log "pid $pid is not ours"; exit 1; }
  t0=$(date +%s%3N); kill -9 $pid
  log "killed pid $pid at $t0 (D+$(( t0 - dms ))ms); marks so far S=$(cat $R/*.tsv | awk -F'\t' -v p="relb.$tag." '$1=="S" && index($2,p)==1' | wc -l) E=$(cat $R/*.tsv | awk -F'\t' -v p="relb.$tag." '$1=="E" && index($2,p)==1' | wc -l)"
  echo "$(date +%s)	stop	rc=crash	pid=$pid	ms=1	kind=crash	manual=relbury-$tag	killms=$t0" >> $D/restarts.tsv
  sleep 3; start "manual=relbury-$tag"; up=$(date +%s)
  for w in 0 180 600; do
    while [ $(( $(date +%s) - up )) -lt $w ]; do sleep 5; done
    l=up; [ $w -gt 0 ] && l=+$w
    for g in rel bur; do
      timeout 300 $WR status --deployment production -i "relb.$tag.$g" -o json --limit 0 > $R/$tag.$l.$g.json 2> $R/$tag.$l.$g.err
      log "snapshot $l $g at $(date +%s%3N): $(grep -o '"State":"[a-z]*"' $R/$tag.$l.$g.json | sort | uniq -c | tr '\n' ' ')"
    done
  done
done
cp -f $D/.wr-prod_production/log $D/manager.log
# a clean stop kills and buries anything left, and bkills our runners; it
# stops whichever manager wr conf resolves, so check it is ours
(SOAK_ROOT=$WRDEV_ROOT; . "$here/../config.sh"; soak_isolated) \
  || { echo "the isolation check failed (see above, or wr conf); not stopping the manager with wr manager stop" >&2; $W prod-stop | head -1; exit 1; }
timeout 900 $WR manager stop --deployment production 2>&1 | tail -1
left=$(bjobs -J "${JOBP}*" -o jobid -noheader 2>/dev/null | wc -l); echo "LSF jobs left: $left"
