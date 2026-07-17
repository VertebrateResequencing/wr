#!/usr/bin/env bash
set -uo pipefail
WR=/tmp/wr-reliable/bin/wr-head-safe; LR=/tmp/wr-reliable/bin/loadrunner-head
BASE=/tmp/wr-reliable/churn; PORT=51829; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost WR_RELIABILITY_NOSCHED=1
cd "$BASE"
"$WR" --deployment development manager start -s local --max_cores 1 --timeout 60 >/dev/null 2>&1
perl -e 'for(1..2000){print "true # w-$_\n"}' > c.txt
"$WR" --deployment development add -f c.txt -i wrk -g wrk_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check >/dev/null 2>&1
echo "baseline:"; for i in 1 2 3; do t0=$(date +%s.%N); timeout 60 "$WR" --deployment development status -i wrk -o counts >/dev/null 2>&1 && echo "  $(echo "$(date +%s.%N)-$t0"|bc)s" || echo "  FAIL"; done
echo "=== 1500 workers CHURNING (connect/ping/disconnect loop) for 45s ==="
"$LR" -mode churn -workers 1500 -duration 45s -deployment development >/dev/null 2>&1 &
LRPID=$!
for i in $(seq 1 20); do t0=$(date +%s.%N); if timeout 60 "$WR" --deployment development status -i wrk -o counts >/dev/null 2>&1; then st=ok; else st=TIMEOUT; fi; echo "  status #$i: $(echo "$(date +%s.%N)-$t0"|bc)s [$st]"; done
wait "$LRPID" 2>/dev/null
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo done
