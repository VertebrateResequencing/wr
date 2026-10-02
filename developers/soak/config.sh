# Settings for the soak scripts in this directory, sourced by each of them.
# Developer tooling, not part of wr. Every setting comes from the environment,
# with the default shown; see README.md.
#
# Required:
#   SOAK_ROOT      the soak's WRDEV_ROOT: the isolated binary, manager dir, DB and
#                  every output land under it. Put it on a filesystem with room
#                  for a production-sized DB and its backup, never a
#                  quota-limited home directory.
#
# Optional:
#   DEV_PORT DEV_WEB PROD_PORT PROD_WEB   isolated manager ports (51860-51863)
#   PPROF_PORT     the soak manager's WR_PPROF_ADDR port (6112)
#   PROD_JOBTOKEN  job name token (iso$PROD_PORT); LSF jobs are wrp<token>_*
#   SCHED          lsf|local, the soak manager's scheduler (lsf)
#   QUEUE          LSF queue for the soak's jobs (normal)
#   WRSRC          checkout wr is built from (this checkout)
#   WRDEV          wrdev.sh to drive (this checkout's developers/wrdev.sh)
#   SOAK_DBDIR     directory of the working DB ($SOAK_ROOT/dbdir)
#   GUARD_GB       stop when SOAK_ROOT's or SOAK_DBDIR's filesystem has less
#                  free (40)
#   TMP_GUARD_GB   stop when ${TMPDIR:-/tmp} has less free (20)
#   USE_FUSE       1 puts the DB behind fusestall to induce commit stalls (0)
#   FUSE_MNT       fusestall's mountpoint, on local disk
#                  (${TMPDIR:-/tmp}/wr-soak-fuse-$USER-$PROD_PORT)

SOAK_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

die() { echo "soak: $*" >&2; exit 1; }

[ -n "${SOAK_ROOT:-}" ] || die "set SOAK_ROOT to the soak's root directory (see $SOAK_DIR/README.md)"
SOAK_ROOT=${SOAK_ROOT%/}
# job commands embed paths under SOAK_ROOT unquoted
case "$SOAK_ROOT" in (/*) ;; (*) die "SOAK_ROOT must be an absolute path, not '$SOAK_ROOT'" ;; esac
case "$SOAK_ROOT" in (*[!A-Za-z0-9._/-]*) die "SOAK_ROOT may only contain letters, digits and ._/-" ;; esac

export SOAK_DIR SOAK_ROOT
export WRDEV_ROOT=$SOAK_ROOT
export DEV_PORT=${DEV_PORT:-51860} DEV_WEB=${DEV_WEB:-51861}
export PROD_PORT=${PROD_PORT:-51862} PROD_WEB=${PROD_WEB:-51863}
export PPROF_PORT=${PPROF_PORT:-6112}
# wrdev.sh names the isolated manager's LSF jobs wrp<PROD_JOBTOKEN>_*; the
# scripts here only ever count or kill jobs with this prefix
export PROD_JOBTOKEN=${PROD_JOBTOKEN:-iso$PROD_PORT}
export JOB_PREFIX=wrp${PROD_JOBTOKEN}_
export SCHED=${SCHED:-lsf} QUEUE=${QUEUE:-normal}
case "$SCHED" in (lsf|local) ;; (*) die "SCHED must be lsf or local, not '$SCHED'" ;; esac

SOAK_REPO=$(git -C "$SOAK_DIR" rev-parse --show-toplevel 2>/dev/null) || die "$SOAK_DIR is not in a git checkout"
export SOAK_REPO
export WRSRC=${WRSRC:-$SOAK_REPO}
export WRDEV=${WRDEV:-$SOAK_REPO/developers/wrdev.sh}

# the isolated binary wrdev.sh builds, and the manager dir it runs from
export SOAK_WR=$SOAK_ROOT/wr
export SOAK_RUN=$SOAK_ROOT/.wr-prod_production

export SOAK_DBDIR=${SOAK_DBDIR:-$SOAK_ROOT/dbdir}
export GUARD_GB=${GUARD_GB:-40} TMP_GUARD_GB=${TMP_GUARD_GB:-20}
export USE_FUSE=${USE_FUSE:-0}
export FUSE_MNT=${FUSE_MNT:-${TMPDIR:-/tmp}/wr-soak-fuse-${USER:-$(id -un)}-$PROD_PORT}
# fsyncs through FUSE_MNT block while this file exists
export STALL_CTL=$SOAK_ROOT/stall.on

have_lsf() { command -v bjobs >/dev/null 2>&1; }

need_lsf() {
  have_lsf || die "$(basename "$0") needs LSF (bjobs, bkill) on PATH"
}

# lsf_has_jobs succeeds if LSF has any job named <prefix>*, and dies when it
# cannot tell, so an in-use check fails closed. bjobs exits 0 with "Job <...>
# is not found" on stderr when no job matches, and non-zero (124 on timeout)
# when it could not ask LSF.
lsf_has_jobs() {
  local out rc
  out=$(timeout 60 bjobs -J "${1}*" -o jobid -noheader 2>/dev/null); rc=$?
  [ "$rc" -eq 0 ] || die "could not ask LSF for ${1}* jobs (bjobs exit $rc); refusing to continue"
  [ -n "$out" ]
}

# our_running_jobs prints how many of our LSF jobs are running, or - without LSF
our_running_jobs() {
  if have_lsf; then
    timeout 30 bjobs -J "${JOB_PREFIX}*" -r -o jobid -noheader 2>/dev/null | wc -l
  else
    echo -
  fi
}

# soak_outdir prints prodsim output dir $1 as an absolute path
soak_outdir() {
  [ -e "$1/restarts.tsv" ] || { echo "soak: $1 is not a prodsim output dir (no restarts.tsv)" >&2; return 1; }
  (cd "$1" && pwd)
}

# soak_alive succeeds while the prodsim of output dir $1 runs
soak_alive() { pgrep -f "$1/prodsim -wr" >/dev/null; }

# soak_conf_value prints field $2 of `wr conf` output $1 (a table with │
# separators), as wrdev.sh's conf_value does
soak_conf_value() {
  printf '%s\n' "$1" | awk -F'│' -v k="$2" \
    '{ f = $2; gsub(/ /, "", f); if (f == k) { v = $3; gsub(/^ +| +$/, "", v); print v; exit } }'
}

# soak_isolated succeeds only if wr resolves --deployment production to our
# isolated manager (localhost, exactly our port, our manager dir), so nothing
# here can reach another manager
soak_isolated() {
  local conf
  conf=$(timeout 60 "$SOAK_WR" conf --deployment production 2>/dev/null) || return 1
  [ "$(soak_conf_value "$conf" ManagerHost)" = localhost ] \
    && [ "$(soak_conf_value "$conf" ManagerPort)" = "$PROD_PORT" ] \
    && [ "$(soak_conf_value "$conf" ManagerDir)" = "$SOAK_RUN" ]
}

# soak_manager_pid prints our manager's pid, if the pid file names a process
# running our isolated binary
soak_manager_pid() {
  local pid
  pid=$(cat "$SOAK_RUN/pid" 2>/dev/null) || return 1
  [ -n "$pid" ] && ps -ww -o cmd= -p "$pid" 2>/dev/null | grep -qF "$SOAK_WR" && echo "$pid"
}

# soak_wr_running <subcommand ERE> prints the pids of our `wr manager
# <subcommand>` processes (and of a `timeout N` running one), and succeeds if
# there are any. The match is anchored at the start of the command line, so a
# pgrep for the same command in another of these scripts never counts as one.
soak_wr_running() {
  pgrep -u "$(id -u)" -f "^(timeout [0-9]+ )?${SOAK_WR//./\\.} manager $1( |\$)"
}

# soak_start_manager starts our manager on its existing DB, as wrdev.sh
# prodsim's restarts do, appending to manager-start.out in output dir $1
soak_start_manager() {
  env WR_JOBNAME_TOKEN="$PROD_JOBTOKEN" WR_PPROF_ADDR="localhost:$PPROF_PORT" timeout 1800 \
    "$SOAK_WR" manager start --deployment production -s "$SCHED" >> "$1/manager-start.out" 2>&1
}

# soak_enter puts the caller in output dir $1's private config and work dir
soak_enter() {
  export WR_CONFIG_DIR=$1/config
  cd "$1/work" || die "no $1/work"
  # shellcheck disable=SC2046 # one name per word
  unset $(compgen -v | grep '^OS_')
}
