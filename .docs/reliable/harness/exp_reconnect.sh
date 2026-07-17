#!/usr/bin/env bash
set -uo pipefail
WR="$1"; LR="$2"; BASE="$3"; NADD="$4"; NCONN="$5"; PORT="$6"; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
export WR_RELIABILITY_NOSCHED=1
cd "$BASE"
"$WR" --deployment development manager start -s local --max_cores 1 --timeout 60 >/dev/null 2>&1 || { echo start failed; exit 1; }
perl -e "for(1..$NADD){print \"true # wrk-\$_\\n\"}" > "$BASE/cmds.txt"
"$WR" --deployment development add -f "$BASE/cmds.txt" -i wrk -g wrk_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 300 >/dev/null 2>&1
sleep 4
GRP=$(grep RELNOSCHED "$BASE/manager_development/log" | sed -E 's/.*group=([^ ]+).*/\1/' | sort -u | head -1)
echo "baseline status:"; for i in 1 2 3; do t0=$(date +%s.%N); timeout 60 "$WR" --deployment development status -i wrk -o counts >/dev/null 2>&1; echo "  #$i $(echo "$(date +%s.%N)-$t0"|bc)s"; done
echo "=== launch $NCONN simultaneous connections (holdper=1) = connect storm ==="
"$LR" -mode hold -workers "$NCONN" -holdper 1 -group "$GRP" -deployment development >/dev/null 2>&1 &
LRPID=$!
echo "measuring status latency immediately (captures storm), incl connect:"
for i in $(seq 1 25); do
  t0=$(date +%s.%N)
  if timeout 60 "$WR" --deployment development status -i wrk -o counts >/dev/null 2>&1; then st="ok"; else st="TIMEOUT/FAIL"; fi
  echo "  status #$i: $(echo "$(date +%s.%N)-$t0"|bc)s [$st]"
done
MPID=$(cat "$BASE/manager_development/pid"); echo "manager CPU%: $(ps -o %cpu= -p "$MPID" 2>/dev/null)"; echo "established conns to :$PORT: $(ss -tn 2>/dev/null | grep -c ":$PORT")"
kill -9 "$LRPID" 2>/dev/null
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo done
