#!/usr/bin/env bash
# 1g.6 review round 1 extra runs, one at a time in root $W/r1/root:
#  1. unsuspend-burst on a SYMLINK to the converted pristine10 store directory,
#     stamping the fixture before and after (finding 3) - also the s3-style gate
#  2. unsuspend-burst with WRDEV_UB_MAX_STATUS_MS=50, which must FAIL (finding 2)
#  3. crash-recovery with WRDEV_CR_START_WAIT=1, which must FAIL not-measured (finding 5)
#  4. crash-recovery three more times (finding 5)
#  5. prodsim for 0.1h with WRDEV_PROD_BKFILE set and planted backup-name siblings,
#     to show its cleanup removes only the backup (finding 9)
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
SRC=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr
WD=$SRC/developers/wrdev.sh
O=$W/r1
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOFLAGS=-p=2 GOPROXY=off GOTELEMETRY=off \
  GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
export WRDEV_ROOT=$O/root DEV_PORT=53480 DEV_WEB=53481 PROD_PORT=53482 PROD_WEB=53483
mkdir -p "$O/work" "$O/bk"; cd "$O/work" || exit 1
stamp() { find -L "$1" -type f -printf '%P %s %T@\n' | sort | md5sum | cut -d' ' -f1; }
echo "r1 extras: src $(git -C $SRC rev-parse --short HEAD) start $(date -u)"
"$WD" build > "$O/00-build.out" 2>&1 || { echo build failed; exit 1; }
ln -sfn "$W/fixtures/pristine10.store" "$O/pristine10.link"
b=$(stamp "$W/fixtures/pristine10.store")
WRDEV_PRISTINE_DB="$O/pristine10.link" "$WD" unsuspend-burst > "$O/01-unsuspend-burst-symlink.out" 2>&1; echo "1 unsuspend-burst symlink rc=$?"
a=$(stamp "$W/fixtures/pristine10.store")
echo "  fixture stamp before=$b after=$a $( [ "$b" = "$a" ] && echo UNCHANGED || echo CHANGED)" | tee -a "$O/01-unsuspend-burst-symlink.out"
WRDEV_PRISTINE_DB="$O/pristine10.link" WRDEV_UB_MAX_STATUS_MS=50 "$WD" unsuspend-burst > "$O/02-unsuspend-burst-lowbound.out" 2>&1; echo "2 unsuspend-burst lowbound rc=$? (must be 1)"
WRDEV_CR_START_WAIT=1 "$WD" crash-recovery > "$O/03-crash-recovery-nowait.out" 2>&1; echo "3 crash-recovery wait=1 rc=$? (must be 1)"
for i in 1 2 3; do "$WD" crash-recovery > "$O/04-crash-recovery-$i.out" 2>&1; echo "4.$i crash-recovery rc=$?"; done
mkdir -p "$O/bk/db_bk_r1x2" "$O/bk/db_bk_r1xB"; touch "$O/bk/db_bk_r1x_old"
echo "  bk before prodsim: $(ls "$O/bk" | tr '\n' ' ')" | tee "$O/05-prodsim-bk.out"
WRDEV_PROD_BKFILE="$O/bk/db_bk_r1x" "$WD" prodsim 0.1 > "$O/05-prodsim.out" 2>&1; echo "5 prodsim 0.1h rc=$?"
echo "  bk after prodsim:  $(ls "$O/bk" | tr '\n' ' ')" | tee -a "$O/05-prodsim-bk.out"
echo "r1 extras end $(date -u)"
