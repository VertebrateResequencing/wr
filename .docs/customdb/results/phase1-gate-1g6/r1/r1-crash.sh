#!/usr/bin/env bash
# 1g.6 review round 1: crash-recovery after its start signal moved to the manager's pid
# (06 = WRDEV_CR_START_WAIT=1, must FAIL not measured; 07-1..4 = default, must PASS)
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
WD=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr/developers/wrdev.sh
O=$W/r1
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOPROXY=off GOTELEMETRY=off
export WRDEV_ROOT=$O/root DEV_PORT=53480 DEV_WEB=53481 PROD_PORT=53482 PROD_WEB=53483
cd "$O/work" || exit 1
echo "r1 crash: start $(date -u)"
WRDEV_CR_START_WAIT=1 "$WD" crash-recovery > "$O/06-crash-recovery-nowait.out" 2>&1; echo "6 crash-recovery wait=1 rc=$? (must be 1)"
for i in 1 2 3 4; do "$WD" crash-recovery > "$O/07-crash-recovery-$i.out" 2>&1; echo "7.$i crash-recovery rc=$?"; done
echo "r1 crash end $(date -u)"
