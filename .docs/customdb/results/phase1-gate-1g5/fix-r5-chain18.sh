#!/usr/bin/env bash
# Rerun of chain17's G7 gate soak and H2 on Wed 2026-11-18: chain17's G7 and
# H2 were stopped by prodsim's diskguard (local /tmp 14G free < GUARD_GB 40)
# within 2 minutes on Mon 2026-11-16. /tmp was freed first (58G free).
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix; G=$W/src-gate
echo "chain18: pid $$ $(date -u)"
[ "$(git -C $G rev-parse HEAD)" = 8dab632aa844fad73528e4172ba257ac90e7ab3f ] && [ -z "$(git -C $G status --porcelain)" ] || { echo "chain18: src-gate not clean at 8dab632"; exit 1; }
df -h /tmp | tail -1
$W/tools/soak-r5.sh G7b-gate "$G" 1 > $W/G7b-gate.out 2>&1 < /dev/null
echo "chain18: G7b done rc=$? $(date -u)"
if [ "$(date -u +%H%M)" -le 1552 ]; then
  $W/tools/soak-r5.sh H2b-scale2 "$G" 1 SCALE=2 > $W/H2b-scale2.out 2>&1 < /dev/null
  echo "chain18: H2b done rc=$? $(date -u)"
else echo "chain18: too late for H2b $(date -u)"; fi
