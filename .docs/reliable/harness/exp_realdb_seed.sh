#!/usr/bin/env bash
# Reproduce production LSF startup cost: real 1.9M-job DB + live jobs in big repgroups.
# Measures (a) add-time seeding cost, (b) restart loadPriorState (re-seed) cost.
# Usage: exp_realdb_seed.sh <base-dir> <port> <ntopgroups>
set -uo pipefail
WR=/tmp/wr-reliable/bin/wr-head-safe
BASE="$1"; PORT="$2"; NGRP="${3:-15}"; WEB=$((PORT+1))
rm -rf "$BASE"; mkdir -p "$BASE/manager_development" "$BASE/cwd"
echo "=== copy real DB into place ==="
cp /nfs/hgi/sb10/wr-reliable/db-pristine "$BASE/manager_development/db"
export WR_MANAGERDIR="$BASE/manager" WR_MANAGERPORT="$PORT" WR_MANAGERWEB="$WEB" WR_MANAGERHOST=localhost
export WR_RELIABILITY_NOSCHED=1 WR_RELIABILITY_KEEPDB=1 WR_RELIABILITY_TIMING=1
cd "$BASE"
echo "=== start manager on real DB (LSF, safe) ==="
/usr/bin/time -f "start1_to_responsive=%e s" "$WR" --deployment development manager start -s lsf --max_cores 1 --timeout 1800 2>&1 | grep -E "start1_to_responsive|EROR" | head -2
grep RELSTARTUP "$BASE/manager_development/log" | sed -E 's/.*msg=//;s/caller=.*//'

echo "=== add a few live jobs to top $NGRP big repgroups (times the add-time seeding) ==="
head -n "$NGRP" /tmp/wr-reliable/top-repgroups.txt | while read -r RG; do
  perl -e 'for(1..20){print "true # seedtest-$_\n"}' > "$BASE/c.txt"
  t0=$(date +%s.%N)
  "$WR" --deployment development add -f "$BASE/c.txt" -i "$RG" -g seedtest_req --cwd "$BASE/cwd" --memory 100M --time 5m --cpus 1 --disable_relative_check --timeout 600 >/dev/null 2>&1
  t1=$(date +%s.%N)
  printf "  add to %-40s took %ss\n" "$RG" "$(echo "$t1-$t0"|bc)"
done

MPID=$(cat "$BASE/manager_development/pid" 2>/dev/null)
echo "=== kill -9 manager $MPID ==="
kill -9 "$MPID" 2>/dev/null; sleep 3
: > "$BASE/manager_development/log"
echo "=== RESTART on real DB with live jobs in big repgroups: time to responsive ==="
/usr/bin/time -f "RESTART_to_responsive=%e s" "$WR" --deployment development manager start -s lsf --max_cores 1 --timeout 3600 2>&1 | grep -E "RESTART_to_responsive|EROR" | head -2
echo "--- sub-phase timing ---"
grep -E "RELSTARTUP|RELPRIOR|RELEQ" "$BASE/manager_development/log" | sed -E 's/.*msg=//;s/caller=.*//'
"$WR" --deployment development manager stop >/dev/null 2>&1 || pkill -9 -f "$PORT"
echo "=== leaked wrd_ jobs? ==="; timeout 30 bjobs -w 2>/dev/null | grep -c wrd_ || echo 0
echo "### done"
