#!/usr/bin/env bash
# 1g.3 run2 analysis of soak/run2 with the export's (097dcf8c) tools, as the
# soak README's "Analysis afterwards". usage: analyse-r2.sh build|main
# build: build dbstart (run on the soak host); main: the analysis (run as an
# LSF job, since dbstart decodes the store's 2.66M live jobs)
set -u
mode=${1:?build|main}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g3
export GOCACHE=$W/gocache GOFLAGS="-p=2" GOPROXY=off GOTELEMETRY=off
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod HOME=$W/home
T=$W/src-097dcf8c/developers/soak
B=$W/bin-097dcf8c
R=$W/soak/run2
out=$(cat "$R/current"); rl=$R/runnerlogs; A=$W/analysis/run2
step() { echo "$(date +%T) run2: $*"; }
if [ "$mode" = build ]; then
  mkdir -p "$B" && go -C "$W/src-097dcf8c" build -o "$B/dbstart" ./developers/soak/dbstart; exit $?
fi
[ "$mode" = main ] || exit 1
[ -x "$B/dbstart" ] || { echo "no $B/dbstart; run build first"; exit 1; }
mkdir -p "$A"; cd "$A" || exit 1
step markers; nice -n 19 python3 $T/markers.py "$out" > markers.txt 2>&1
step doubles; nice -n 19 python3 $T/doubles.py "$out" "$A/doubles.tsv" > doubles.txt 2>&1
step runnerlogs; nice -n 19 python3 $T/runnerlogs.py "$rl" "$A/unacked.tsv" > runnerlogs.txt 2>&1
step latency; nice -n 19 python3 $T/latency.py "$out" "$rl" > latency.txt 2>&1
step rundepcheck; nice -n 19 python3 $T/rundepcheck.py "$out" > rundepcheck.txt 2>&1
step dbstart; /usr/bin/time -v $B/dbstart "$R/dbdir/db" > dbstart.tsv 2> dbstart.err; echo "dbstart rc=$?" >> dbstart.err
step starttimes; nice -n 19 python3 $T/starttimes.py "$out" "$A/dbstart.tsv" "$A/unacked.tsv" > starttimes.txt 2>&1
step relburycheck; nice -n 19 python3 $T/relburycheck.py "$out" "$rl" "$A/dbstart.tsv" > relburycheck.txt 2>&1
step soakgate; nice -n 19 python3 $T/soakgate.py --source wait "$out" "$rl" "$A/doubles.tsv" "$A/dbstart.tsv" > soakgate.txt 2> soakgate.err; echo "soakgate rc=$?" >> soakgate.err
step done
