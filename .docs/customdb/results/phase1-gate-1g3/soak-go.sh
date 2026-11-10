#!/usr/bin/env bash
# phase 1 gate item 1g.3: crash soak at production LSF concurrency on the log
# store (fe45846d), without FIXTURE. battery10's soak-go.sh shape, plus the
# statustime.sh watcher. usage: soak-go.sh main|smoke
set -u
mode=${1:?main|smoke}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g3
SP=/tmp/claude-11346/-nfs-users-nfs-s-sb10-src-go-github-com-VertebrateResequencing-wr/11ff1329-f08a-4839-8d20-e8b51f0456f7/scratchpad/1g3impl
export GOCACHE=$SP/gocache GOFLAGS="-p=2" GOPROXY=off
unset $(compgen -v | grep '^OS_') 2>/dev/null
# isolated HOME, as runstate-gate nohold-g3b: ~/.wr_config.yml names farm TLS
# certs, which wrdev.sh refuses for an isolated manager; .ssh linked in so
# confirm-dead ssh works. GOPATH/GOMODCACHE pinned since go env follows HOME.
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod GOTELEMETRY=off
export HOME=$W/home
export SCHED=lsf QUEUE=normal GUARD_GB=40
export USE_FUSE=1 RUNNER_FILELOG=1 WRDEV_PRODSIM_KEEP_DB=1
unset FIXTURE
SRC=$W/src-fe45846d
H=$SRC/developers/soak
case $mode in
  main)
    export SOAK_ROOT=$W/soak/run
    export DEV_PORT=53310 DEV_WEB=53311 PROD_PORT=53312 PROD_WEB=53313 PPROF_PORT=6331
    export HOURS=3 SIMMIN=6 SCALE=1
    export RESTART_MIN=15 RESTART_KINDS=crash,clean,crash,clean,crash,crash,clean,crash,clean,crash,clean
    export FUSE_ON_AT=3 FUSE_OFF_AT=4 STALL_AFTER_MIN=7 STALL_SECS=180
    export RAMP0=600 RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"
    inject() {
      "$H/crashon.sh" "$out" 4 stall 90 > "$out/crashon-stall.out" 2>&1 &
      "$H/crashon.sh" "$out" 6 burst 60 > "$out/crashon-burst1.out" 2>&1 &
      "$H/crashafter.sh" "$out" 8 300 > "$out/crashafter.out" 2>&1 &
      "$H/crashon.sh" "$out" 10 burst 45 > "$out/crashon-burst2.out" 2>&1 &
      "$H/relbury.sh" "$out" 2 r1 400 500 > "$out/relbury-r1.out" 2>&1 &
      "$H/relbury.sh" "$out" 7 r2 400 2000 > "$out/relbury-r2.out" 2>&1 &
      "$H/stopstate.sh" "$out" > "$out/stopstate.out" 2>&1 &
      "$H/rundep.sh" "$out" 240 9600 > "$out/rundep.out" 2>&1 &
    } ;;
  smoke)
    export SOAK_ROOT=$W/smoke/run
    export DEV_PORT=53300 DEV_WEB=53301 PROD_PORT=53302 PROD_WEB=53303 PPROF_PORT=6330
    export HOURS=0.3 SIMMIN=6 SCALE=1
    export RESTART_MIN=3 RESTART_KINDS=crash,clean,crash,clean
    export FUSE_ON_AT=2 FUSE_OFF_AT=4 STALL_AFTER_MIN=1 STALL_SECS=40
    export RAMP0=100 RAMP="0:100"
    inject() {
      "$H/crashon.sh" "$out" 3 stall 20 > "$out/crashon-stall.out" 2>&1 &
      "$H/relbury.sh" "$out" 2 r1 40 500 60 > "$out/relbury-r1.out" 2>&1 &
    } ;;
  *) echo "bad mode $mode"; exit 1 ;;
esac
mkdir -p "$SOAK_ROOT"; rm -f "$SOAK_ROOT/current"
( for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
  out=$(cat "$SOAK_ROOT/current")
  inject
  "$W/tools/statustime.sh" "$SRC" "$out" > "$out/statustime.out" 2>&1 &
  wait ) &
exec nice "$H/run.sh"
