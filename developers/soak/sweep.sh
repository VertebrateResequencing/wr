#!/usr/bin/env bash
# sweep.sh <sweepdir> [mode ...]: runs every wrdev.sh mode in turn (or just the
# modes named), each with its recommended fixture, recording rc, wall time,
# load and free space per mode. Developer tooling, not part of wr. Many modes
# use real LSF (bjobs, bsub, bkill), so run it on an LSF submission host.
#
# Each mode's output goes to <sweepdir>/logs/<n>-<mode>.out and one line per
# mode to <sweepdir>/results.tsv. A disk guard stops the running mode (its
# wrdev.sh cleanup trap runs) if <sweepdir>'s filesystem drops under GUARD_GB
# free, and the sweep then stops. Modes run from <sweepdir>/work, never the
# checkout: a `wr add` without --cwd makes its jobs' wr_cwd dirs under the
# directory it was run from.
#
#   SWEEP_WRDEV    wrdev.sh to run (this checkout's); another checkout's builds
#                  from that checkout
#   SWEEP_PORTS    "devPort devWeb prodPort prodWeb" (51850 51851 51852 51853)
#   SWEEP_DB_DIR   directory of the big fixture DBs pristine10, pristine6 and
#                  prod.db (see wrdev.sh help); modes whose fixture is missing
#                  are recorded as SKIPPED-NOFIXTURE
#   SWEEP_ASL_FIXTURE  an add-storm-fixture DB for add-storm-lsf (the one the
#                  sweep's own add-storm-fixture mode writes)
#   FIX120K        a 120k-job add-storm-fixture DB for add-storm-lsf-fix120k
#   SWEEP_BOLTBUCKETS  boltbuckets binary for add-storm-lsf's offline check
#   GUARD_GB       free-space floor in GB (40)
set -u
[ $# -ge 1 ] || { echo "usage: sweep.sh <sweepdir> [mode ...]" >&2; exit 2; }
SW=$(mkdir -p "$1" && cd "$1" && pwd) || exit 1
shift
REPO=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
W="${SWEEP_WRDEV:-$REPO/developers/wrdev.sh}"
B=${SWEEP_DB_DIR:-}
FIX120K=${FIX120K:-}
ASLFIX=${SWEEP_ASL_FIXTURE:-$SW/aslfixture-sweep.db}
BB=${SWEEP_BOLTBUCKETS:+WRDEV_ASL_BOLTBUCKETS=$SWEEP_BOLTBUCKETS}
GUARD_GB=${GUARD_GB:-40}
read -r DEV_PORT DEV_WEB PROD_PORT PROD_WEB <<< "${SWEEP_PORTS:-51850 51851 51852 51853}"
export WRDEV_ROOT="$SW/root" DEV_PORT DEV_WEB PROD_PORT PROD_WEB
mkdir -p "$WRDEV_ROOT" "$SW/logs" "$SW/work"
# shellcheck disable=SC2046 # one name per word
unset $(compgen -v | grep '^OS_') 2>/dev/null
sw_free() { df -B1G --output=avail "$SW" | tail -1 | tr -d ' '; }
# our_fg_manager <pid> succeeds only if pid runs this root's foreground dev
# manager, as wrdev.sh dump starts it, so a stale or reused pid is never signalled
our_fg_manager() {
  : "${WRDEV_ROOT:?}"
  case "$(ps -ww -o args= -p "$1" 2>/dev/null)" in
    ("$WRDEV_ROOT/wr manager start --deployment development "*" -f") return 0 ;;
  esac
  return 1
}
# sweep_error logs a problem the sweep must exit non-zero for
sweep_rc=0
sweep_error() { echo "SWEEP ERROR: $*" | tee -a "$SW/sweep.log" "$log" >&2; sweep_rc=1; }
# The build mode rewrites $WRDEV_ROOT/wr in place. A runner still executing it
# (here, or on an exec node over NFS) dies when it is rewritten, so refuse while
# anything may be using it: a local process, or an LSF job named as this
# root's prod and dev managers name them (wrp<token>_*, wrd<token>_*), or as a
# dev manager from before wrdev.sh namespaced them did (wrd_*).
if pgrep -f "$WRDEV_ROOT/wr" >/dev/null; then
  echo "sweep: a process is running $WRDEV_ROOT/wr*; let it finish or use another sweepdir" >&2; exit 1
fi
# bjobs exits 0 with "Job <...> is not found" on stderr when nothing matches,
# and non-zero (124 on timeout) when it could not ask LSF: fail closed then
if command -v bjobs >/dev/null; then
  for p in "wrp${PROD_JOBTOKEN:-iso$PROD_PORT}_" "wrd${DEV_JOBTOKEN:-iso$DEV_PORT}_" wrd_; do
    out=$(timeout 60 bjobs -J "${p}*" -o jobid -noheader 2>/dev/null); rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "sweep: could not ask LSF for ${p}* jobs (bjobs exit $rc); refusing to start" >&2; exit 1
    fi
    if [ -n "$out" ]; then
      echo "sweep: LSF still has ${p}* jobs, whose runners may use $WRDEV_ROOT/wr; wait for them" >&2; exit 1
    fi
  done
fi
# mode|timeoutMin|env assignments (space separated)|args
MODES=(
  "build|20||"
  "status|2||"
  "flicker-check|20||"
  "status-seed-overlap|30||"
  "overprovision-check|20||"
  "overcount-check|20||"
  "limit-stall-check|30||"
  "priority-fairness-check|20||"
  "backlog-rescan-check|20||"
  "bkill-hygiene|20||"
  "runner-started-timeout-check|20||"
  "ttrmiss-check|30||"
  "confirm-dead-leak|10||"
  "report-storm|30||"
  "report-storm-profile|40||"
  "remap-stall-check|20||"
  "freelist-check|40||"
  "selfconnect-check|20||"
  "idle-backlog-cpu|30||"
  "control-rpc-history|60||"
  "dep-granularity-check|60||"
  "exec-impossible-retries|20||"
  "transient-start-retries|20||"
  "runner-log-bytes|20||"
  "retention-check|30||"
  "web-burst|30||"
  "backup-stall-fast|30|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "writestorm-freeze|40|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "archive-rate|30|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "archive-ceiling|40|WRDEV_PRISTINE_DB=${B:+$B/pristine6} WRDEV_AC_WORK=$SW/root|"
  "add-storm|40|WRDEV_PRISTINE_DB=${B:+$B/prod.db} WRDEV_AS_WORK=$SW/root|"
  "add-storm-fixture|60|WRDEV_PRISTINE_DB=${B:+$B/pristine6} WRDEV_ASL_FIXTURE=$SW/aslfixture-sweep.db|"
  "add-storm-lsf|60|WRDEV_PRISTINE_DB=$ASLFIX $BB|"
  "add-storm-lsf-fix120k|60|WRDEV_PRISTINE_DB=$FIX120K $BB|"
  "unsuspend-burst|60|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "start|5||lsf"
  "probe|5||"
  "churn|90||"
  "monitor|30||"
  "stop|10||"
  "limit-drain|90||"
  "backup-stall-check|120|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "report-storm-lsf|120|WRDEV_PRISTINE_DB=${B:+$B/pristine10}|"
  "crash-recovery|20||"
  "prodsim|180||"
  "prod-start|5||local"
  "prod-stop|5||"
  "dump|3||local"
  "clean|10||"
)

want=" $* "
n=0
for spec in "${MODES[@]}"; do
  IFS='|' read -r mode tmo envs args <<< "$spec"
  n=$(( n + 1 ))
  [ $# -gt 0 ] && [[ "$want" != *" $mode "* ]] && continue
  f=$(sw_free)
  if [ "$f" -lt $(( GUARD_GB + 25 )) ]; then
    echo -e "$(date +%s)\t$mode\tSKIPPED-DISK\tfree=${f}G" >> "$SW/results.tsv"; echo "stopping: $SW has ${f}G free"; break
  fi
  # a mode that names a fixture DB needs it to exist
  fix=$(sed -n 's/.*WRDEV_PRISTINE_DB=\([^ ]*\).*/\1/p' <<< "$envs")
  if [[ "$envs" == *WRDEV_PRISTINE_DB=* ]] && { [ -z "$fix" ] || [ ! -f "$fix" ]; }; then
    echo -e "$(date +%s)\t$mode\tSKIPPED-NOFIXTURE\t${fix:-unset}" >> "$SW/results.tsv"; echo "=== $mode skipped: no fixture DB"
    continue
  fi
  wmode=${mode%-fix120k}
  log="$SW/logs/$(printf %02d $n)-$mode.out"
  l0=$(cut -d' ' -f1 /proc/loadavg); t0=$(date +%s)
  echo "=== $mode $args ($envs) start $(date +%T) load=$l0 free=${f}G" | tee -a "$SW/sweep.log"
  # shellcheck disable=SC2086 # envs and args are word lists
  ( cd "$SW/work" && exec env $envs setsid timeout --signal=TERM --kill-after=120 $(( tmo * 60 )) "$W" $wmode $args ) > "$log" 2>&1 &
  mp=$!
  guard=0 minf=$f
  while kill -0 $mp 2>/dev/null; do
    sleep 15
    f=$(sw_free); [ "$f" -lt "$minf" ] && minf=$f
    if [ "$f" -lt "$GUARD_GB" ]; then
      echo "$(date +%T) DISK GUARD: $SW has ${f}G < ${GUARD_GB}G, stopping $mode" | tee -a "$SW/sweep.log" "$log"
      pkill -TERM -f "$W $wmode" ; guard=1; sleep 150; pkill -KILL -f "$W $wmode"
    fi
  done
  wait $mp; rc=$?
  if [ "$mode" = dump ]; then  # dump leaves a foreground dev manager behind by design; take a dump and stop it
    p=$(cat "$WRDEV_ROOT/.wr_development/pid" 2>/dev/null)
    if [ -n "$p" ] && our_fg_manager "$p"; then
      kill -3 "$p"; sleep 3; kill -9 "$p" 2>/dev/null
    else
      sweep_error "dump's pid file names '${p:-nothing}', not this root's foreground manager; no dump taken"
      rc="$rc,nodump"
    fi
    echo "goroutines in SIGQUIT dump: $(grep -ac '^goroutine ' "$WRDEV_ROOT/fg.out")" >> "$log"
    # none may outlive the sweep: kill any still up, each pid verified by its
    # whole command line (this root's binary path), never matched by pattern
    sleep 1
    for q in $(ps -u "$(id -u)" -o pid=); do
      our_fg_manager "$q" || continue
      sweep_error "this root's foreground manager pid $q was still running after dump; killing it"
      kill -9 "$q"; rc="$rc,leftover"
    done
  fi
  t1=$(date +%s)
  echo -e "$t0\t$mode\trc=$rc\tsecs=$(( t1 - t0 ))\tload0=$l0\tload1=$(cut -d' ' -f1 /proc/loadavg)\tminfree=${minf}G\tguard=$guard" >> "$SW/results.tsv"
  echo "=== $mode rc=$rc in $(( t1 - t0 ))s; tail:" | tee -a "$SW/sweep.log"; tail -4 "$log" | sed 's/^/    /' | tee -a "$SW/sweep.log"
  [ "$guard" = 1 ] && { echo "stopping the sweep after the disk guard fired"; break; }
done
echo "sweep done $(date +%T)" | tee -a "$SW/sweep.log"
[ "$sweep_rc" = 0 ] || echo "sweep had errors; see SWEEP ERROR lines in $SW/sweep.log" >&2
exit "$sweep_rc"
