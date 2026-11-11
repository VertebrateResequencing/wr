#!/usr/bin/env bash
# phase 1 gate item 1g.5: daytime NFS targets on the log store (f7006373).
# usage: soak-go.sh gate|ckpt|smoke (smoke: a 12-minute tooling check)
#   gate: the stall-free soak: run.sh with SOAK_ROOT on NFS, USE_FUSE=0,
#         HOURS=1, RESTART_MIN=120, no injectors; run.sh's default ramp.
#   ckpt: the same shape for 45 minutes, plus seedlive.sh adding 250,000
#         live jobs that never run, so checkpoints of a production-sized
#         live set (.ckpt of 40MB or more) are written while runners reserve.
# Both run ckptwatch.py on the store directory.
set -u
mode=${1:?gate|ckpt}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5
SP=/tmp/claude-11346/-nfs-users-nfs-s-sb10-src-go-github-com-VertebrateResequencing-wr/11ff1329-f08a-4839-8d20-e8b51f0456f7/scratchpad/1g5
export GOCACHE=$SP/gocache GOFLAGS="-p=2" GOPROXY=off
unset $(compgen -v | grep '^OS_') 2>/dev/null
# isolated HOME, as 1g.3: ~/.wr_config.yml names farm TLS certs, which
# wrdev.sh refuses for an isolated manager; .ssh linked in for confirm-dead.
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod GOTELEMETRY=off
export HOME=$W/home
export SCHED=lsf QUEUE=normal GUARD_GB=40
export USE_FUSE=0
unset FIXTURE RUNNER_FILELOG
SRC=$W/src-f7006373
H=$SRC/developers/soak
export SIMMIN=6 SCALE=1 RESTART_MIN=120
case $mode in
  gate)
    export SOAK_ROOT=$W/soak/run
    export DEV_PORT=53350 DEV_WEB=53351 PROD_PORT=53352 PROD_WEB=53353 PPROF_PORT=6335
    export HOURS=1
    inject() { :; } ;;
  ckpt)
    export SOAK_ROOT=$W/ckpt/run
    export DEV_PORT=53360 DEV_WEB=53361 PROD_PORT=53362 PROD_WEB=53363 PPROF_PORT=6336
    # portal bursts every 7m, not 5m, so they drift against the 5m checkpoints
    # (in the gate soak each checkpoint ran ~3s before a burst, the same minute)
    export HOURS=0.75 EXTRA_ARGS="-portal-gap 7m"
    inject() {
      "$W/tools/seedlive.sh" "$out" "$H" 250000 10000 240 > "$out/seedlive.out" 2>&1 &
    } ;;
  smoke)
    export SOAK_ROOT=$W/smoke/run
    export DEV_PORT=53370 DEV_WEB=53371 PROD_PORT=53372 PROD_WEB=53373 PPROF_PORT=6337
    export HOURS=0.2 RAMP0=100 RAMP="0:100"
    inject() {
      "$W/tools/seedlive.sh" "$out" "$H" 20000 10000 30 > "$out/seedlive.out" 2>&1 &
    } ;;
  *) echo "bad mode $mode"; exit 1 ;;
esac
mkdir -p "$SOAK_ROOT"; rm -f "$SOAK_ROOT/current" "$SOAK_ROOT/ckptwatch.stop"
( for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
  out=$(cat "$SOAK_ROOT/current")
  inject
  python3 "$W/tools/ckptwatch.py" "$SOAK_ROOT/dbdir/db" "$out/ckptwatch.tsv" "$SOAK_ROOT/ckptwatch.stop" &
  wait ) &
nice "$H/run.sh"
rc=$?
touch "$SOAK_ROOT/ckptwatch.stop"
echo "soak-go: run.sh exited $rc at $(date -u)"
exit $rc
