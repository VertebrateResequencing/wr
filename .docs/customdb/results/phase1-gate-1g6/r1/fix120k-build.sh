#!/usr/bin/env bash
# 1g.6 review round 1, finding 4: build a 120,000-job add-storm-lsf fixture in
# bbolt form with d1a4b6d7's own wrdev.sh (bbolt was that release's default
# store), under this item's NFS area, from a read-only copy of pristine6.
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
F=$W/fix120k
S=$W/src-d1a4b6d7/developers/wrdev.sh
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOFLAGS=-p=2 GOPROXY=off GOTELEMETRY=off \
  GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
export WRDEV_ROOT=$F/root DEV_PORT=53490 DEV_WEB=53491 PROD_PORT=53492 PROD_WEB=53493
cd "$F/work" || exit 1
echo "fix120k build: $S ($(git -C $W/src-d1a4b6d7 log --oneline -1)) start $(date -u)"
"$S" build || exit 1
"$WRDEV_ROOT/wr" version
WRDEV_PRISTINE_DB=/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/pristine6 \
  WRDEV_ASL_FIXTURE=$F/fix120k.db "$S" add-storm-fixture 120000
rc=$?
echo "fix120k build: rc=$rc end $(date -u)"
exit $rc
