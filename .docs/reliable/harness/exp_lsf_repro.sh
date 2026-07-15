#!/usr/bin/env bash
# REAL LSF reproduction (real runners, real code paths). Fresh DB, SAFE sleep jobs
# only (never .tmp/db). Monitors: false lost, count-accounting anomalies, status
# responsiveness, and kill-9 restart with real runner reconnection.
# Usage: exp_lsf_repro.sh <wr-binary> <base-dir> <port> <njobs> <sleep> <limit> <killat>
set -uo pipefail
WR="$1"; BASE="$2"; PORT="$3"; NJOBS="$4"; SLEEP="$5"; LIMIT="$6"; KILLAT="${7:-0}"; WEB=$((PORT+1))
LSF_PREFIX="wrd_"
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd" "$BASE/runnerlogs"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
cd "$BASE"
EV="$BASE/evidence.log"; : > "$EV"
log(){ printf '%s %s\n' "$(date -Is)" "$*" | tee -a "$EV"; }
CLEANED=0
cleanup(){ [ "$CLEANED" = 1 ] && return; CLEANED=1; set +e
  log "cleanup: manager stop"; timeout 120 "$WR" --deployment development manager stop >>"$EV" 2>&1
  local ids; ids=$(timeout 60 bjobs -w 2>/dev/null | awk -v p="$LSF_PREFIX" 'NF>6 && index($7,p)==1{id=$1; sub(/\[.*/,"",id); print id}' | sort -u)
  [ -n "$ids" ] && { log "bkill leftover runners"; echo "$ids" | xargs -r -n 500 bkill -b >>"$EV" 2>&1; }
  log "final bjobs wrd_: $(timeout 60 bjobs -w 2>/dev/null | grep -c "$LSF_PREFIX" || echo 0)"
}
trap cleanup EXIT

pre=$(timeout 30 bjobs -w 2>/dev/null | grep -c "$LSF_PREFIX" || true)
[ "${pre:-0}" != "0" ] && { log "ABORT: $pre existing $LSF_PREFIX jobs"; exit 75; }

log "=== $(basename "$WR"): manager start -s lsf (FRESH db, safe jobs), NFS dir ==="
timeout 150 "$WR" --deployment development manager start -s lsf --timeout 120 --runner_filelog "$BASE/runnerlogs" >>"$EV" 2>&1 || { log "start failed"; exit 1; }
timeout 30 "$WR" --deployment development limit -g "results_portal=$LIMIT" >>"$EV" 2>&1 || log "limit set failed (continuing)"

log "add $NJOBS jobs: sleep $SLEEP (limit results_portal=$LIMIT, retries 30, cwd_matters)"
perl -e "for(1..$NJOBS){print \"sleep $SLEEP && echo ok-\$_\\n\"}" > "$BASE/cmds.txt"
timeout 300 "$WR" --deployment development add -f "$BASE/cmds.txt" -i portal_rg -g portal_req \
  --cwd "$BASE/cwd" --memory 100M --time 10m --cpus 1 --queue normal --limit_grps results_portal --retries 30 --cwd_matters \
  --disable_relative_check --timeout 240 >>"$EV" 2>&1 || log "add failed"

MAXRUN=0; MAXLOST=0; ANOM=0; KILLED=0
for i in $(seq 1 300); do
  t0=$(date +%s.%N)
  ST=$(timeout 90 "$WR" --deployment development status -i portal_rg -o counts 2>/dev/null); rc=$?
  lat=$(echo "$(date +%s.%N)-$t0"|bc)
  if [ $rc -ne 0 ]; then log "poll $i: STATUS TIMEOUT/FAIL after ${lat}s (rc=$rc) <-- NON-RESPONSIVE"; ANOM=1; sleep 5; continue; fi
  g(){ echo "$ST"|awk -F': *' -v k="$1" '$1==k{print $2}'; }
  comp=$(g complete); run=$(g running); rdy=$(g ready); dep=$(g dependent); del=$(g delayed); bur=$(g buried); lost=$(g "lost contact")
  bjc=$(timeout 60 bjobs -w 2>/dev/null | grep -c "$LSF_PREFIX" || echo "?")
  tot=$(( ${comp:-0}+${run:-0}+${rdy:-0}+${dep:-0}+${del:-0}+${bur:-0}+${lost:-0} ))
  log "poll $i: comp=$comp run=$run rdy=$rdy dep=$dep del=$del bur=$bur lost=$lost | acct=$tot/$NJOBS bjobs=$bjc statuslat=${lat}s"
  [ "${run:-0}" -gt "$MAXRUN" ] && MAXRUN=${run:-0}
  [ "${lost:-0}" -gt "$MAXLOST" ] && MAXLOST=${lost:-0}
  [ "${lost:-0}" -gt 0 ] && { log "ANOMALY: lost contact=$lost"; ANOM=1; }
  [ "$(echo "$lat > 5"|bc)" = 1 ] && { log "ANOMALY: status latency ${lat}s > 5s"; ANOM=1; }
  awk "BEGIN{exit !($tot < $NJOBS && ${comp:-0} > 0)}" && { log "ANOMALY: unaccounted jobs ($tot<$NJOBS) -> vanished/removed"; ANOM=1; }
  # kill-9 test when running reaches KILLAT
  if [ "$KILLAT" != 0 ] && [ "$KILLED" = 0 ] && [ "${run:-0}" -ge "$KILLAT" ]; then
    MPID=$(cat "$BASE/manager_development/pid" 2>/dev/null)
    log "=== KILL -9 manager $MPID with run=$run (runners stay alive in LSF, will reconnect) ==="
    kill -9 "$MPID" 2>/dev/null; KILLED=1; sleep 5
    log "restart manager (real runners will reconnect):"
    rt0=$(date +%s.%N)
    timeout 1800 "$WR" --deployment development manager start -s lsf --timeout 1500 --runner_filelog "$BASE/runnerlogs" >>"$EV" 2>&1 && log "restart returned to-responsive=$(echo "$(date +%s.%N)-$rt0"|bc)s" || log "restart FAILED/timeout after $(echo "$(date +%s.%N)-$rt0"|bc)s"
  fi
  [ "${comp:-0}" -ge "$NJOBS" ] && { log "all complete"; break; }
  sleep 5
done
log "=== SUMMARY peak_running=$MAXRUN max_lost=$MAXLOST anomaly=$ANOM ==="
log "manager log lost/zombie/error grep:"; timeout 60 grep -HnEi "lost|zombie|panic|eror|err=" "$BASE/manager_development"/log* 2>/dev/null | head -40 >>"$EV"
cleanup; trap - EXIT
echo "### done anomaly=$ANOM"
