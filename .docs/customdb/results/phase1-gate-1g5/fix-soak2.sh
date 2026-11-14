#!/usr/bin/env bash
# 1g.5 bugfix soaks: the 1g.5 gate soak's shape (run.sh, SOAK_ROOT on NFS,
# USE_FUSE=0, RESTART_MIN=120, no injectors, run.sh's default ramp) from a
# given source tree, with the diagnostic trace on local disk.
# usage: soak-fix.sh <arm-name> <src dir> <hours> [ENV=VAL ...]
set -u
arm=${1:?arm}; src=${2:?src}; hours=${3:?hours}; shift 3
for kv in "$@"; do export "$kv"; done
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix
SP=/tmp/claude-11346/-nfs-users-nfs-s-sb10-src-go-github-com-VertebrateResequencing-wr/11ff1329-f08a-4839-8d20-e8b51f0456f7/scratchpad
export GOCACHE=$SP/gocache GOFLAGS="-p=2" GOPROXY=off
unset $(compgen -v | grep '^OS_') 2>/dev/null
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod GOTELEMETRY=off
export HOME=$W/home
export SCHED=lsf QUEUE=normal GUARD_GB=40
export USE_FUSE=0
unset FIXTURE RUNNER_FILELOG
H=$src/developers/soak
export SIMMIN=${SIMMIN:-6} SCALE=${SCALE:-1} RESTART_MIN=120 HOURS=$hours
export SOAK_ROOT=$W/soak/$arm
export DEV_PORT=53380 DEV_WEB=53381 PROD_PORT=53382 PROD_WEB=53383 PPROF_PORT=6338
export WR_DIAG_TRACE=$SP/g5fix-trace/$arm
mkdir -p "$SOAK_ROOT" "$WR_DIAG_TRACE"; rm -f "$SOAK_ROOT/current" "$SOAK_ROOT/ckptwatch.stop"
( for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
  out=$(cat "$SOAK_ROOT/current")
  python3 "$W/tools/ckptwatch.py" "$SOAK_ROOT/dbdir/db" "$out/ckptwatch.tsv" "$SOAK_ROOT/ckptwatch.stop" &
  wait ) &
echo "soak-fix: arm=$arm src=$src hours=$hours env: $* start $(date -u)"
nice "$H/run.sh"
rc=$?
touch "$SOAK_ROOT/ckptwatch.stop"
echo "soak-fix: run.sh exited $rc at $(date -u)"
exit $rc
