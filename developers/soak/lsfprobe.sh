#!/usr/bin/env bash
# lsfprobe.sh <outdir>: LSF-side responsiveness every 2 min (LSF only): bjobs
# and bqueues latency and our jobs' commonest pending reasons, to lsfprobe.tsv.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: lsfprobe.sh <outdir>"
need_lsf
d=$(soak_outdir "$1") || exit 1
while soak_alive "$d"; do
  t0=$(date +%s%3N); st=$(timeout 120 bjobs -J "${JOB_PREFIX}*" -o stat -noheader 2>/dev/null | sort | uniq -c | awk '{printf "%s=%s ", $2, $1}'); t1=$(date +%s%3N)
  timeout 120 bqueues "$QUEUE" >/dev/null 2>&1; t2=$(date +%s%3N)
  pr=$(timeout 120 bjobs -p -J "${JOB_PREFIX}*" -o 'pend_reason' -noheader 2>/dev/null | sed 's/;.*//' | sort | uniq -c | sort -rn | head -3 | awk '{c=$1; $1=""; printf "%s:%s| ", c, $0}'); t3=$(date +%s%3N)
  echo "$(date +%s)	$st	bjobs_ms=$((t1-t0))	bqueues_ms=$((t2-t1))	pendq_ms=$((t3-t2))	pend=[$pr]	load=$(cut -d' ' -f1 /proc/loadavg)" >> "$d/lsfprobe.tsv"
  sleep 120
done
