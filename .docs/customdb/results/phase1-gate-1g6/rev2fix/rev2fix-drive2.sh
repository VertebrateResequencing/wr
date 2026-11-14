#!/usr/bin/env bash
# 1g.6 fix round 2, part 2: the four rows rev2fix left on s3's code, rerun on the
# final code (the rev2fix binary, not rebuilt) in root $O/root, ports 53484-53487,
# one at a time, each under the sweep's timeout:
#  08 add-storm-fixture on the compacted pristine6 bbolt fixture -> $O/aslfixture-rev2fix.db
#  09 add-storm-lsf-fix120k on the 1g6 fix120k bbolt fixture
#  10 backup-stall-check and 11 report-storm-lsf on the compacted pristine10 bbolt fixture
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
cd "$O/work" || exit 1
echo "rev2fix2: src $(git -C $SRC rev-parse --short HEAD) dirty: $(git -C $SRC status --porcelain | tr '\n' ' ')"
echo "rev2fix2: wrdev.sh md5 $(md5sum < "$WD" | cut -c1-12) jobqueue diff md5 $(git -C $SRC diff -- jobqueue cmd internal | md5sum | cut -c1-12) binary md5 $(md5sum < "$WRDEV_ROOT/wr" | cut -c1-12) start $(date -u)"
run() {  # <n> <label> <timeoutMin> <mode> [env assignments...]
  local n=$1 label=$2 tmo=$3 mode=$4; shift 4
  local t0; t0=$(date +%s)
  env "$@" timeout --signal=TERM --kill-after=120 $(( tmo * 60 )) "$WD" "$mode" > "$O/$n-$label.out" 2>&1
  echo "$n $label rc=$? in $(( $(date +%s) - t0 ))s"
}
run 08 add-storm-fixture 60 add-storm-fixture WRDEV_PRISTINE_DB=$F/pristine6 WRDEV_ASL_FIXTURE=$O/aslfixture-rev2fix.db
run 09 add-storm-lsf-fix120k 60 add-storm-lsf WRDEV_PRISTINE_DB=$W/fix120k/fix120k.db
run 10 backup-stall-check 120 backup-stall-check WRDEV_PRISTINE_DB=$F/pristine10
run 11 report-storm-lsf 120 report-storm-lsf WRDEV_PRISTINE_DB=$F/pristine10
echo "rev2fix2: wrdev.sh md5 $(md5sum < "$WD" | cut -c1-12) binary md5 $(md5sum < "$WRDEV_ROOT/wr" | cut -c1-12) end $(date -u)"
echo "left in $WRDEV_ROOT/.wr-prod_production: $(ls "$WRDEV_ROOT/.wr-prod_production" 2>/dev/null | tr '\n' ' ')"
