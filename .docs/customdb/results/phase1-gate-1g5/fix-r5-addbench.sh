#!/usr/bin/env bash
# phase 1 gate item 1g.5, round 5: one TestAddLatency1000 run (spec A3) on the
# log store, its database under the NFS work directory, from the jobqueue test
# binary chain17.sh built from src-gate ($W/r5/jq-gate.test), with a scratch
# HOME on NFS and every OS_ variable unset. usage: addbench-r5.sh <n>
set -u
n=${1:?run number}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix
dir=$W/addbench/run$n
[ -e "$dir" ] && { echo "$dir exists"; exit 1; }
mkdir -p "$dir" "$W/r5/homes"
out=$W/addbench/run$n.out
unset $(compgen -v | grep '^OS_') 2>/dev/null
home=$(mktemp -d -p "$W/r5/homes")
echo "start $(date -u +%FT%T.%3NZ) $(date +%s.%3N) home=$home dir=$dir test=$(sha256sum < $W/r5/jq-gate.test | cut -c1-16)" > "$out"
cd "$W/src-gate/jobqueue" || exit 1
HOME=$home WR_ADDBENCH_DIR=$dir WR_TEST_STORE=log \
  "$W/r5/jq-gate.test" -test.run '^TestAddLatency1000$' -test.v -test.count 1 -test.timeout 90m >> "$out" 2>&1
rc=$?
echo "end $(date -u +%FT%T.%3NZ) $(date +%s.%3N) rc=$rc" >> "$out"
echo "home $home"
exit $rc
