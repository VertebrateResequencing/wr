#!/usr/bin/env bash
# crashafter.sh <outdir> <nStarts> <delaySecs>: an extra crash restart outside
# the regular schedule. Once restarts.tsv has nStarts start lines, waits
# delaySecs, kills -9 our (verified) manager and starts it again on the same
# DB, recording both in restarts.tsv as wrdev.sh's prodsim_restart does.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 3 ] || die "usage: crashafter.sh <outdir> <nStarts> <delaySecs>"
d=$(soak_outdir "$1") || exit 1
n=$2 wait=$3
soak_enter "$d"
until [ "$(grep -c '	start	' "$d/restarts.tsv")" -ge "$n" ]; do soak_alive "$d" || exit 0; sleep 5; done
sleep "$wait"
soak_isolated || die "wr does not resolve --deployment production to our manager on :$PROD_PORT"
pid=$(soak_manager_pid) || die "the pid file does not name a process running $SOAK_WR"
t0=$(date +%s%3N); kill -9 "$pid"
echo "$(date +%s)	stop	rc=crash	pid=$pid	ms=$(( $(date +%s%3N) - t0 ))	kind=crash	manual=crashafter	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
soak_wait_gone "$pid" "$d" crashafter; sleep 3; t0=$(date +%s%3N)
soak_start_manager "$d"
rc=$?
echo "$(date +%s)	start	rc=$rc	pid=$(cat "$SOAK_RUN/pid")	ms=$(( $(date +%s%3N) - t0 ))	manual=crashafter	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/restarts.tsv"
