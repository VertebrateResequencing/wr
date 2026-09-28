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

secs=$(awk -v m="$mean" -v r="$RANDOM" 'BEGIN { printf "%.1f", m * (0.5 + r / 32767) }')

if [ "$mem" -gt 0 ] 2>/dev/null; then
  perl -e '$x = "a" x ($ARGV[0] * 1048576); select(undef, undef, undef, $ARGV[1]);' "$mem" "$secs"
else
  sleep "$secs"
fi

if [ "$kind" = walk ]; then
  # extra: wrBin deployment nStat statRG depGroup limitGroups statSecs cwd queue path
  wr=$1 dep=$2 n=$3 rg=$4 dg=$5 lg=$6 ssecs=$7 cwd=$8 queue=$9 path=${10}
  # jobs inherit prodsim's environment; without its private WR_CONFIG_DIR this
  # add could reach whatever manager the default config names
  [ -n "${WR_CONFIG_DIR:-}" ] || { echo "walk $id: WR_CONFIG_DIR is not set; refusing to add" >&2; exit 5; }
  script=$(readlink -f "$0")
  for i in $(seq 1 "$n"); do
    echo "$script stat $id.$i $ssecs 400 1 -p $path/dir$i"
  done | "$wr" add --deployment "$dep" -f - -i "$rg" -g wrstat-stat -e "$dg" -l "$lg" \
      -m 500M -t 30m -r 3 --cwd_matters -c "$cwd" --queue "$queue" --timeout 300 >/dev/null 2>&1 \
      || { echo "walk $id could not add its stat jobs" >&2; exit 4; }
fi

head -c $(( RANDOM % 4000 )) /dev/zero | tr '\0' 'o'
echo " $kind $id done after ${secs}s"

if [ $(( RANDOM % 100 )) -lt "$failpct" ]; then
  echo "psimjob: simulated failure of $kind $id" >&2
  exit 3
fi
exit 0
