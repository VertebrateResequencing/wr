#!/usr/bin/env bash
# A/B startup: kill-9 with N running jobs, one restart, measure recovery time.
# Usage: exp_startup_ab.sh <wr-safe-bin> <loadrunner-bin> <base> <nadd> <nrun> <port> [sched]
set -uo pipefail
WR="$1"; LR="$2"; BASE="$3"; NADD="$4"; NRUN="$5"; PORT="$6"; SCHED="${7:-local}"; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
export WR_RELIABILITY_NOSCHED=1 WR_RELIABILITY_KEEPDB=1 WR_RELIABILITY_TIMING=1
cd "$BASE"
echo "### $(basename "$WR") : nadd=$NADD nrun=$NRUN"
"$WR" --deployment development manager start -s "$SCHED" --max_cores 1 --timeout 60 >/dev/null 2>&1 || { echo start1 failed; exit 1; }
perl -e "for(1..$NADD){print \"true # wrk-\$_\\n\"}" > "$BASE/cmds.txt"
"$WR" --deployment development add -f "$BASE/cmds.txt" -i wrk -g wrk_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 300 >/dev/null 2>&1
sleep 4
GRP=$(grep RELNOSCHED "$BASE/manager_development/log" | sed -E 's/.*group=([^ ]+).*/\1/' | sort -u | head -1)
HPW=20; W=$(( (NRUN + HPW - 1) / HPW ))
"$LR" -mode hold -workers "$W" -holdper "$HPW" -group "$GRP" -deployment development >/dev/null 2>&1 &
LRPID=$!
for i in $(seq 1 60); do
  R=$("$WR" --deployment development status -i wrk -o counts 2>/dev/null | awk -F': *' '/^running/{print $2}')
  if [ "${R:-0}" -ge "$NRUN" ] 2>/dev/null; then break; fi
  sleep 2
done
MPID=$(cat "$BASE/manager_development/pid" 2>/dev/null)
echo "running=$R; kill -9 manager $MPID"
kill -9 "$MPID" 2>/dev/null; kill -9 "$LRPID" 2>/dev/null; sleep 3
: > "$BASE/manager_development/log"
echo "=== RESTART: time to responsive ==="
/usr/bin/time -f "WALL_TO_RESPONSIVE=%e s" "$WR" --deployment development manager start -s "$SCHED" --max_cores 1 --timeout 1800 2>&1 | grep -E "WALL_TO_RESPONSIVE|EROR" | head -2
echo "--- sub-phase timing (HEAD only) ---"
grep -E "RELSTARTUP|RELPRIOR|RELEQ" "$BASE/manager_development/log" 2>/dev/null | sed -E 's/.*msg=//; s/caller=.*//'
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo "### done"
