#!/usr/bin/env bash
# startprof.sh <outdir> <pprofport>: on each new start line in restarts.tsv,
# a 30s CPU profile and goroutine dumps every 4s for 40s of the restarted
# manager (the window statustime.sh's first status runs in). Reads only.
d=$1; pp=$2; P=$d/profiles/startprof; mkdir -p $P
seen=$(grep -c '	start	' $d/restarts.tsv)
while pgrep -f "$d/prodsim -wr" >/dev/null; do
  n=$(grep -c '	start	' $d/restarts.tsv)
  if [ "$n" -gt "$seen" ]; then
    seen=$n; ts=$(date +%s)
    # CPU: prodsim takes profiles/poststart.<ts>.cpu.pprof itself (one CPU profile at a time)
    for i in $(seq 0 4 40); do curl -s -m 15 "http://localhost:$pp/debug/pprof/goroutine?debug=1" > $P/$ts.g$i.txt; sleep 4; done
    wait
  fi
  sleep 0.5
done
