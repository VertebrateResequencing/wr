#!/usr/bin/env bash
# 1g.6: rerun wrdev.sh crash-recovery N times on the s2 root (built by sweep s2)
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g6
SRC=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr
unset $(compgen -v | grep '^OS_') 2>/dev/null
export HOME=$W/home WRDEV_ROOT=$W/s2/root DEV_PORT=53480 DEV_WEB=53481 PROD_PORT=53482 PROD_WEB=53483
cd "$W/s2/work" || exit 1
for i in $(seq 1 "${1:-3}"); do
  echo "=== crash-recovery repeat $i start $(date -u +%T)"
  timeout 1200 "$SRC/developers/wrdev.sh" crash-recovery 2>&1 | grep -aE 'jobid|LSF runner|rgCR|PASS|FAIL|aborted'
  echo "=== crash-recovery repeat $i rc=${PIPESTATUS[0]} end $(date -u +%T)"
done
