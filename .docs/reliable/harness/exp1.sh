#!/usr/bin/env bash
# EXP-1: end-to-end churn throughput + responsiveness A/B.
# Real runners (local scheduler), no code hacks -> identical CLI for HEAD and v0.36.5.
# Usage: exp1.sh <wr-binary> <label> <base-dir> <njobs> <runners> <jobtype:true|sleepN> <port>
set -uo pipefail
WR="$1"; LABEL="$2"; BASE="$3"; NJOBS="$4"; RUNNERS="$5"; JOBTYPE="$6"; PORT="$7"
WEB=$((PORT+1))
LR=/tmp/wr-reliable/bin/loadrunner-head
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
cd "$BASE"

case "$JOBTYPE" in
  true) CMD='true' ;;
  sleep*) CMD="${JOBTYPE/sleep/sleep }" ;;
  *) CMD='true' ;;
esac
perl -e "for(1..$NJOBS){print \"$CMD # ${LABEL}-\$_\\n\"}" > "$BASE/cmds.txt"

echo "### $LABEL : njobs=$NJOBS runners=$RUNNERS jobtype=$JOBTYPE dir=$BASE"
"$WR" --deployment development manager start -s local --max_cores "$RUNNERS" --timeout 60 >/dev/null 2>&1 || { echo "start failed"; exit 1; }

t0=$(date +%s.%N)
"$WR" --deployment development add -f "$BASE/cmds.txt" -i "${LABEL}_rg" -g "${LABEL}_req" \
  --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 300 >/dev/null 2>&1
t1=$(date +%s.%N)
ADD=$(echo "$t1 - $t0" | bc)
echo "add_time=${ADD}s"

# poll to completion, sampling ping responsiveness
PINGLOG="$BASE/ping.log"; : > "$PINGLOG"
deadline=$(( $(date +%s) + 1800 ))
last=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  ST=$(timeout 30 "$WR" --deployment development status -i "${LABEL}_rg" -o counts 2>/dev/null)
  comp=$(echo "$ST" | awk -F': *' '/^complete/{print $2}')
  run=$(echo "$ST" | awk -F': *' '/^running/{print $2}')
  rdy=$(echo "$ST" | awk -F': *' '/^ready/{print $2}')
  bur=$(echo "$ST" | awk -F': *' '/^buried/{print $2}')
  # responsiveness sample (persistent-connection ping, 2s window)
  P=$(timeout 15 "$LR" -mode ping -duration 2s -deployment development 2>/dev/null | grep '^PING')
  echo "$(date +%s) comp=$comp run=$run rdy=$rdy bur=$bur | $P" >> "$PINGLOG"
  last="comp=$comp run=$run rdy=$rdy bur=$bur"
  if [ "${comp:-0}" -ge "$NJOBS" ] 2>/dev/null; then break; fi
  if [ "${comp:-0}" -gt 0 ] && [ "${run:-0}" = "0" ] && [ "${rdy:-0}" = "0" ]; then break; fi
done
t2=$(date +%s.%N)
RUN=$(echo "$t2 - $t1" | bc)
echo "run_time=${RUN}s  final: $last"
THRU=$(echo "scale=1; ${comp:-0} / $RUN" | bc 2>/dev/null)
echo "throughput=${THRU} complete/s"
echo "--- ping samples (p50/p95/max under load) ---"
grep -o 'p50=[^ ]* p95=[^ ]* p99=[^ ]* max=[^ ]*' "$PINGLOG" | tail -8
echo "--- worst ping p95 across run ---"
grep -o 'p95=[^ ]*' "$PINGLOG" | sed 's/p95=//;s/ms/*1000/;s/µs//;s/s$/*1000000/' 2>/dev/null | head
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo "### done $LABEL"
