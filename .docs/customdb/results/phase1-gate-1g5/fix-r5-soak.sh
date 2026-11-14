#!/usr/bin/env bash
# 1g.5 bugfix round 5 soaks: the 1g.5 gate soak's shape (run.sh, SOAK_ROOT on
# NFS, USE_FUSE=0, RESTART_MIN=120, no injectors, run.sh's default ramp) from
# a given source tree. Unlike soak-fix.sh it depends on nothing in a session's
# /tmp scratchpad: its Go build cache is on NFS and there is no diagnostic
# trace (the final code has none).
# usage: soak-r5.sh <arm-name> <src dir> <hours> [ENV=VAL ...]
set -u
arm=${1:?arm}; src=${2:?src}; hours=${3:?hours}; shift 3
for kv in "$@"; do export "$kv"; done
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix
export GOCACHE=$W/r5/gocache GOFLAGS="-p=2" GOPROXY=off
unset $(compgen -v | grep '^OS_') 2>/dev/null
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod GOTELEMETRY=off
export HOME=$W/home
export SCHED=lsf QUEUE=normal GUARD_GB=40
export USE_FUSE=0
unset FIXTURE RUNNER_FILELOG WR_DIAG_TRACE
H=$src/developers/soak
export SIMMIN=${SIMMIN:-6} SCALE=${SCALE:-1} RESTART_MIN=120 HOURS=$hours
export SOAK_ROOT=$W/soak/$arm
export DEV_PORT=53380 DEV_WEB=53381 PROD_PORT=53382 PROD_WEB=53383 PPROF_PORT=6338
mkdir -p "$SOAK_ROOT" "$GOCACHE"; rm -f "$SOAK_ROOT/current" "$SOAK_ROOT/ckptwatch.stop"
( for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
  out=$(cat "$SOAK_ROOT/current")
  echo "soak-r5: $arm wr buildinfo:"; go version -m "$SOAK_ROOT/wr" | grep -E 'vcs\.(revision|modified)'
  python3 "$W/tools/ckptwatch.py" "$SOAK_ROOT/dbdir/db" "$out/ckptwatch.tsv" "$SOAK_ROOT/ckptwatch.stop" &
  wait ) &
echo "soak-r5: arm=$arm src=$src hours=$hours env: $* start $(date -u)"
nice "$H/run.sh"
rc=$?
touch "$SOAK_ROOT/ckptwatch.stop"
echo "soak-r5: run.sh exited $rc at $(date -u)"
exit $rc
