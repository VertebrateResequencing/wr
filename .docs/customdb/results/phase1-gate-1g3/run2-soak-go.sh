#!/usr/bin/env bash
# phase 1 gate item 1g.3: crash soak at production LSF concurrency on the log
# store (097dcf8c), without FIXTURE: the rerun (run2) with a ramp to about 6,000
# portal runners held from t+24m. battery10's soak-go.sh shape, plus the
# statustime.sh and startprof.sh watchers. usage: soak-go-r2.sh main
set -u
mode=${1:?main}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g3
export GOCACHE=$W/gocache GOFLAGS="-p=2" GOPROXY=off
unset $(compgen -v | grep '^OS_') 2>/dev/null
# isolated HOME, as runstate-gate nohold-g3b: ~/.wr_config.yml names farm TLS
# certs, which wrdev.sh refuses for an isolated manager; .ssh linked in so
# confirm-dead ssh works. GOPATH/GOMODCACHE pinned since go env follows HOME.
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod GOTELEMETRY=off
export HOME=$W/home
export SCHED=lsf QUEUE=normal GUARD_GB=40
export USE_FUSE=1 RUNNER_FILELOG=1 WRDEV_PRODSIM_KEEP_DB=1
unset FIXTURE
SRC=$W/src-097dcf8c
H=$SRC/developers/soak
case $mode in
  main)
    export SOAK_ROOT=$W/soak/run2
    export DEV_PORT=53310 DEV_WEB=53311 PROD_PORT=53312 PROD_WEB=53313 PPROF_PORT=6331
    export HOURS=3 SIMMIN=6 SCALE=1
    export RESTART_MIN=15 RESTART_KINDS=crash,clean,crash,clean,crash,crash,clean,crash,clean,crash,clean
    export FUSE_ON_AT=3 FUSE_OFF_AT=4 STALL_AFTER_MIN=7 STALL_SECS=180
    export RAMP0=1500 RAMP="0:1500 4:2500 8:3500 13:4500 18:5300 24:6000"
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
  *) echo "bad mode $mode"; exit 1 ;;
esac
mkdir -p "$SOAK_ROOT"; rm -f "$SOAK_ROOT/current"
( for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
  out=$(cat "$SOAK_ROOT/current")
  inject
  "$W/tools/statustime.sh" "$SRC" "$out" > "$out/statustime.out" 2>&1 &
  "$W/tools/startprof.sh" "$out" "$PPROF_PORT" > "$out/startprof.out" 2>&1 &
  wait ) &
exec nice "$H/run.sh"
