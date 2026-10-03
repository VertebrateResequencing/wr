#!/usr/bin/env bash
#
# psimjob.sh - the only command prodsim's jobs run. Harmless by construction:
# it sleeps (optionally holding some memory), writes a little output, fails
# with a given probability, and - for a "walk" - adds its own child jobs to the
# isolated manager with the isolated wr binary, as `wrstat walk` does.
#
# usage: psimjob.sh <kind> <id> <meanSecs> <memMB> <failPct> [extra...]
# Anything after a '#' in the command line is a comment (production-sized
# padding) that bash never passes here.

kind=$1 id=$2 mean=$3 mem=$4 failpct=$5
shift 5

# Run markers: one S line when a run starts, and one E line when it ends,
# appended to markers/<host>.tsv beside this script, so a job that runs twice
# (eg. after a manager crash) can be counted afterwards. One file per host
# keeps each append a single-writer-host write on NFS.
#
# The E line's status field is the script's real exit code, written by finish
# just before the exit it names, or "sig<NAME>" when a signal ended the run.
# There is deliberately no EXIT trap: bash runs one after a fatal signal with $?
# from the last completed command (often 0), which made killed runs look like
# clean exits. A SIGKILLed run writes no E line at all.
mdir="$(dirname "$(readlink -f "$0")")/markers"
mhost=$(hostname -s)
mkdir -p "$mdir" 2>/dev/null
mark() { printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$(date +%s%3N)" "$kind" "$id" "$mhost" "$$" "${LSB_JOBID:-}" "$2" "$PWD" >> "$mdir/$mhost.tsv" 2>/dev/null; }
finish() { mark E "$1"; exit "$1"; }
child=""
onsig() {
  trap - "$1"
  [ -n "$child" ] && kill -TERM "$child" 2>/dev/null
  mark E "sig$1"
  kill -"$1" $$
  exit $(( 128 + $2 ))
}
trap 'onsig TERM 15' TERM
trap 'onsig INT 2' INT
trap 'onsig HUP 1' HUP
mark S ""

# a --cmd_deps dependent of a container job: record the time (secs) its
# dependency finished, read from the file the dependency wrote last. NFS may
# not show another host's new file at once, so look for up to a minute; the
# verdict is the recorded time against this run's start.
if [ "$kind" = ctrdep ]; then
  for _ in $(seq 1 30); do [ -s "$1" ] && break; ls "$(dirname "$1")" >/dev/null 2>&1; sleep 2; done
  if [ -s "$1" ]; then mark D "$(head -c 20 "$1")"; else mark D MISSING; fi
fi

secs=$(awk -v m="$mean" -v r="$RANDOM" 'BEGIN { printf "%.1f", m * (0.5 + r / 32767) }')

# the sleep runs in the background and is waited for, so that a signal's trap
# runs at once rather than after the sleep
if [ "$mem" -gt 0 ] 2>/dev/null; then
  perl -e '$x = "a" x ($ARGV[0] * 1048576); select(undef, undef, undef, $ARGV[1]);' "$mem" "$secs" &
else
  sleep "$secs" &
fi
child=$!
wait "$child"
child=""

if [ "$kind" = walk ]; then
  # extra: wrBin deployment nStat statRG depGroup limitGroups statSecs cwd queue path
  wr=$1 dep=$2 n=$3 rg=$4 dg=$5 lg=$6 ssecs=$7 cwd=$8 queue=$9 path=${10}
  # jobs inherit prodsim's environment; without its private WR_CONFIG_DIR this
  # add could reach whatever manager the default config names
  [ -n "${WR_CONFIG_DIR:-}" ] || { echo "walk $id: WR_CONFIG_DIR is not set; refusing to add" >&2; finish 5; }
  script=$(readlink -f "$0")
  for i in $(seq 1 "$n"); do
    echo "$script stat $id.$i $ssecs 400 1 -p $path/dir$i"
  done | "$wr" add --deployment "$dep" -f - -i "$rg" -g wrstat-stat -e "$dg" -l "$lg" \
      -m 500M -t 30m -r 3 --cwd_matters -c "$cwd" --queue "$queue" --timeout 300 >/dev/null 2>&1 \
      || { echo "walk $id could not add its stat jobs" >&2; finish 4; }
fi

head -c $(( RANDOM % 4000 )) /dev/zero | tr '\0' 'o'
echo " $kind $id done after ${secs}s"

if [ $(( RANDOM % 100 )) -lt "$failpct" ]; then
  echo "psimjob: simulated failure of $kind $id" >&2
  finish 3
fi
finish 0
