#!/usr/bin/env bash
# 1g.6 fix round 2: on the final code, in root $O/root with ports 53484-53487:
#  01 a real first start converting a copy of the compacted pristine10 bbolt fixture
#     (3,000,000 jobs), sampling the status sidecar every 5s (checklist item 8 evidence)
#  then the scenarios affected by changes since s1, one at a time:
#  02 dep-granularity-check  03 control-rpc-history  04 crash-recovery x2
#  05 crash-recovery with WRDEV_CR_START_WAIT=1 (must FAIL not measured)
#  06 unsuspend-burst on the compacted pristine10 bbolt fixture (conversion inside the start)
#  07 add-storm-lsf on s3's add-storm-fixture log store directory fixture
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
SRC=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr
WD=$SRC/developers/wrdev.sh
F=/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures
O=$W/rev2fix
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOFLAGS=-p=2 GOPROXY=off GOTELEMETRY=off \
  GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
export WRDEV_ROOT=$O/root DEV_PORT=53484 DEV_WEB=53485 PROD_PORT=53486 PROD_WEB=53487
mkdir -p "$O/work" "$WRDEV_ROOT"; cd "$O/work" || exit 1
sums() { md5sum < "$SRC/developers/wrdev.sh" | cut -c1-12; }
echo "rev2fix: src $(git -C $SRC rev-parse --short HEAD) dirty: $(git -C $SRC status --porcelain | tr '\n' ' ')"
echo "rev2fix: wrdev.sh md5 $(sums) jobqueue diff md5 $(git -C $SRC diff -- jobqueue cmd internal | md5sum | cut -c1-12) start $(date -u)"
"$WD" build > "$O/00-build.out" 2>&1 || { echo build failed; exit 1; }
echo "binary md5 $(md5sum < "$WRDEV_ROOT/wr" | cut -c1-12)"

# 01: conversion evidence
pr=$WRDEV_ROOT/.wr-prod_production
mkdir -p "$pr"
cp -f "$F/pristine10" "$pr/db" || { echo copy failed; exit 1; }
( while :; do
    if [ -f "$pr/db.upgrade" ]; then echo "$(date -u +%T) $(tr -d '\n' < "$pr/db.upgrade")"; fi
    sleep 5
  done ) > "$O/01-convert-sidecar.txt" 2>&1 &
sampler=$!
t0=$(date +%s)
PROD_START_TIMEOUT=3600 "$WD" prod-start local > "$O/01-convert-start.out" 2>&1; echo "01 convert prod-start rc=$? in $(( $(date +%s) - t0 ))s"
[ -n "$sampler" ] && kill "$sampler"
sed -E 's/token=[^ "]*/token=[REDACTED]/g' "$pr/log" > "$O/01-convert-manager.log"
"$WD" prod-stop > /dev/null 2>&1
echo "01 left beside db: $(ls "$pr" | tr '\n' ' ')"
du -sh "$pr/db" "$pr"/db.bolt-pre-* 2>/dev/null | sed 's/^/01 /'
# the converted store and the kept bbolt original (several GB) go before the scenarios
rm -rf /nfs/hgi/wr/sb10-bigdb/customdb/1g6/rev2fix/root/.wr-prod_production

for m in dep-granularity-check control-rpc-history; do
  n=$([ $m = dep-granularity-check ] && echo 02 || echo 03)
  t0=$(date +%s); "$WD" $m > "$O/$n-$m.out" 2>&1; echo "$n $m rc=$? in $(( $(date +%s) - t0 ))s"
done
for i in 1 2; do t0=$(date +%s); "$WD" crash-recovery > "$O/04-crash-recovery-$i.out" 2>&1; echo "04.$i crash-recovery rc=$? in $(( $(date +%s) - t0 ))s"; done
WRDEV_CR_START_WAIT=1 "$WD" crash-recovery > "$O/05-crash-recovery-nowait.out" 2>&1; echo "05 crash-recovery wait=1 rc=$? (must be 1)"
t0=$(date +%s); WRDEV_PRISTINE_DB="$F/pristine10" "$WD" unsuspend-burst > "$O/06-unsuspend-burst.out" 2>&1; echo "06 unsuspend-burst (bbolt pristine10) rc=$? in $(( $(date +%s) - t0 ))s"
t0=$(date +%s); WRDEV_PRISTINE_DB="$W/s3/aslfixture-sweep.db" "$WD" add-storm-lsf > "$O/07-add-storm-lsf.out" 2>&1; echo "07 add-storm-lsf (s3 directory fixture) rc=$? in $(( $(date +%s) - t0 ))s"
echo "rev2fix: wrdev.sh md5 $(sums) end $(date -u)"
echo "left in $pr: $(ls "$pr" 2>/dev/null | tr '\n' ' ')"
