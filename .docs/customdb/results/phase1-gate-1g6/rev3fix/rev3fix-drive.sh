#!/usr/bin/env bash
# 1g.6 fix round 3, on the final code, in a fresh root $O/root with ports 53488-53491,
# one at a time:
#  01 crash-recovery x2 (manager stopped before cr_bkill_ours; exact exec_host match)
#  02 crash-recovery with WRDEV_CR_START_WAIT=1 (must FAIL not measured)
#  03 add-storm-lsf on s3's directory fixture (asl_cleanup counts only PEND/RUN/SUSP)
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
SRC=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr
WD=$SRC/developers/wrdev.sh
O=$W/rev3fix
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOFLAGS=-p=2 GOPROXY=off GOTELEMETRY=off \
  GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
export WRDEV_ROOT=$O/root DEV_PORT=53488 DEV_WEB=53489 PROD_PORT=53490 PROD_WEB=53491
mkdir -p "$O/work" "$WRDEV_ROOT"; cd "$O/work" || exit 1
sums() {
  echo "wrdev.sh md5 $(md5sum < "$WD" | cut -c1-12)" \
    "jobqueue diff md5 $(git -C $SRC diff -- jobqueue cmd internal | md5sum | cut -c1-12)" \
    "binary md5 $(md5sum < "$WRDEV_ROOT/wr" 2>/dev/null | cut -c1-12)"
}
echo "rev3fix: src $(git -C $SRC rev-parse --short HEAD) dirty: $(git -C $SRC status --porcelain | tr '\n' ' ')"
"$WD" build > "$O/00-build.out" 2>&1 || { echo build failed; exit 1; }
echo "rev3fix: $(sums) start $(date -u)"
for i in 1 2; do
  t0=$(date +%s); "$WD" crash-recovery > "$O/01-crash-recovery-$i.out" 2>&1
  echo "01.$i crash-recovery rc=$? in $(( $(date +%s) - t0 ))s"
done
WRDEV_CR_START_WAIT=1 "$WD" crash-recovery > "$O/02-crash-recovery-nowait.out" 2>&1
echo "02 crash-recovery wait=1 rc=$? (must be 1)"
t0=$(date +%s); WRDEV_PRISTINE_DB="$W/s3/aslfixture-sweep.db" "$WD" add-storm-lsf > "$O/03-add-storm-lsf.out" 2>&1
echo "03 add-storm-lsf (s3 directory fixture) rc=$? in $(( $(date +%s) - t0 ))s"
echo "rev3fix: $(sums) end $(date -u)"
echo "left in $WRDEV_ROOT/.wr-prod_production: $(ls "$WRDEV_ROOT/.wr-prod_production" 2>/dev/null | tr '\n' ' ')"
