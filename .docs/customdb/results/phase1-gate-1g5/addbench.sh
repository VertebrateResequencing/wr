#!/usr/bin/env bash
# phase 1 gate item 1g.5: one TestAddLatency1000 run (spec A3) on the log
# store, its database under the NFS work directory. usage: addbench.sh <n>
# Runs the jobqueue test binary built at f7006373 with a scratch HOME and every
# OS_ variable unset; WR_ADDBENCH_DIR is a fresh directory per run.
set -u
n=${1:?run number}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5
SP=/tmp/claude-11346/-nfs-users-nfs-s-sb10-src-go-github-com-VertebrateResequencing-wr/11ff1329-f08a-4839-8d20-e8b51f0456f7/scratchpad/1g5
REPO=/nfs/users/nfs_s/sb10/src/go/github.com/VertebrateResequencing/wr
dir=$W/addbench/run$n
[ -e "$dir" ] && { echo "$dir exists"; exit 1; }
mkdir -p "$dir"
out=$W/addbench/run$n.out
unset $(compgen -v | grep '^OS_') 2>/dev/null
home=$(mktemp -d -p "$SP")
echo "start $(date -u +%FT%T.%3NZ) $(date +%s.%3N) home=$home dir=$dir" > "$out"
cd "$REPO/jobqueue" || exit 1
HOME=$home WR_ADDBENCH_DIR=$dir WR_TEST_STORE=log \
  "$SP/jobqueue.test" -test.run '^TestAddLatency1000$' -test.v -test.count 1 -test.timeout 90m >> "$out" 2>&1
rc=$?
echo "end $(date -u +%FT%T.%3NZ) $(date +%s.%3N) rc=$rc" >> "$out"
echo "home $home"
exit $rc
