#!/usr/bin/env bash
# A/B scale drive: many workers archive many jobs; measure throughput decay + latency.
# Usage: exp_drive_ab.sh <wr-safe-bin> <loadrunner-bin> <base> <njobs> <workers> <port>
set -uo pipefail
WR="$1"; LR="$2"; BASE="$3"; NJOBS="$4"; WORKERS="$5"; PORT="$6"; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
export WR_RELIABILITY_NOSCHED=1
cd "$BASE"
echo "### $(basename "$WR") drive: njobs=$NJOBS workers=$WORKERS"
"$WR" --deployment development manager start -s local --max_cores 1 --timeout 60 >/dev/null 2>&1 || { echo start failed; exit 1; }
perl -e "for(1..$NJOBS){print \"true # uniq-\$_\\n\"}" > "$BASE/cmds.txt"
t0=$(date +%s.%N)
"$WR" --deployment development add -f "$BASE/cmds.txt" -i sd -g sd_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 300 >/dev/null 2>&1
t1=$(date +%s.%N); echo "add_time=$(echo "$t1-$t0"|bc)s"
sleep 4
GRP=$(grep RELNOSCHED "$BASE/manager_development/log" | sed -E 's/.*group=([^ ]+).*/\1/' | sort -u | head -1)
echo "group=[$GRP]"
timeout 600 "$LR" -mode drive -workers "$WORKERS" -touches 0 -group "$GRP" -deployment development 2>&1 | grep -E '^\[|throughput|latency|PING|RESULTS|elapsed'
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo "### done"
