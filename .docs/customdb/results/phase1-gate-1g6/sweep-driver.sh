#!/usr/bin/env bash
# 1g.6: run developers/soak/sweep.sh from the main clone into $W/<name>, with an
# isolated HOME (so ~/.wr_config*.yml cannot point wr elsewhere), an NFS Go
# cache, and ports 53480-53483. usage: sweep.sh <name> [mode ...]
set -u
name=${1:?name}; shift
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
SRC=${SRC:-/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr}
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home GOCACHE=$W/gocache GOFLAGS=-p=2 GOPROXY=off GOTELEMETRY=off \
  GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
export SWEEP_PORTS="${SWEEP_PORTS:-53480 53481 53482 53483}" \
  SWEEP_DB_DIR=/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures GUARD_GB=${GUARD_GB:-30}
echo "1g6 sweep $name: src $SRC at $(git -C "$SRC" rev-parse --short HEAD), dirty: $(git -C "$SRC" status --porcelain | tr '\n' ' ') start $(date -u)"
"$SRC/developers/soak/sweep.sh" "$W/$name" "$@"
rc=$?
echo "1g6 sweep $name: rc=$rc end $(date -u)"
exit $rc
