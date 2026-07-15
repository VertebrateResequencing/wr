#!/usr/bin/env bash
set -uo pipefail
WR="$1"; LR="$2"; BASE="$3"; NADD="$4"; NRUN="$5"; PORT="$6"; SCHED="${7:-local}"; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
export WR_RELIABILITY_NOSCHED=1
cd "$BASE"
"$WR" --deployment development manager start -s "$SCHED" --max_cores 1 --timeout 60 >/dev/null 2>&1 || { echo start failed; exit 1; }
perl -e "for(1..$NADD){print \"true # wrk-\$_\\n\"}" > "$BASE/cmds.txt"
"$WR" --deployment development add -f "$BASE/cmds.txt" -i wrk -g wrk_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 300 >/dev/null 2>&1
sleep 4
GRP=$(grep RELNOSCHED "$BASE/manager_development/log" | sed -E 's/.*group=([^ ]+).*/\1/' | sort -u | head -1)
lat() { local tag="$1" mode="$2"; for i in 1 2 3 4 5; do local t0 t1; t0=$(date +%s.%N)
  if [ "$mode" = counts ]; then timeout 120 "$WR" --deployment development status -i wrk -o counts >/dev/null 2>&1
  else timeout 120 "$WR" --deployment development status -i wrk >/dev/null 2>&1; fi
  t1=$(date +%s.%N); printf "  %-16s #%d: %.3fs\n" "$tag" "$i" "$(echo "$t1-$t0"|bc)"; done; }
echo "== baseline =="; lat "base-counts" counts; lat "base-full" full
HPW=20; W=$(( (NRUN + HPW - 1) / HPW ))
"$LR" -mode hold -workers "$W" -holdper "$HPW" -group "$GRP" -deployment development >/dev/null 2>&1 &
LRPID=$!
for i in $(seq 1 40); do R=$("$WR" --deployment development status -i wrk -o counts 2>/dev/null | awk -F': *' '/^running/{print $2}'); [ "${R:-0}" -ge "$NRUN" ] 2>/dev/null && break; sleep 2; done
echo "== under touch load (running=$R) =="; lat "load-counts" counts; lat "load-full" full
# CPU snapshot of the manager
MPID=$(cat "$BASE/manager_development/pid"); echo "manager CPU%: $(ps -o %cpu= -p "$MPID" 2>/dev/null)"
kill -9 "$LRPID" 2>/dev/null; sleep 5
echo "== after kill =="; lat "kill-counts" counts; lat "kill-full" full
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo done
