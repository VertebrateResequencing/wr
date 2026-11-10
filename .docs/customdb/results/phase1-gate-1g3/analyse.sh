#!/usr/bin/env bash
# 1g.3 analysis of one soak tree with the export's (fe45846d) tools, as the
# soak README's "Analysis afterwards". usage: analyse.sh main|smoke
set -u
mode=${1:?main|smoke}
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g3
SP=/tmp/claude-11346/-nfs-users-nfs-s-sb10-src-go-github-com-VertebrateResequencing-wr/11ff1329-f08a-4839-8d20-e8b51f0456f7/scratchpad/1g3impl
export GOCACHE=$SP/gocache GOFLAGS="-p=2" GOPROXY=off GOTELEMETRY=off
export GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod HOME=$W/home
T=$W/src-fe45846d/developers/soak
case $mode in (main) R=$W/soak/run ;; (smoke) R=$W/smoke/run ;; (*) exit 1 ;; esac
out=$(cat "$R/current"); rl=$R/runnerlogs; A=$W/analysis/$mode
mkdir -p "$A" "$W/bin"; cd "$A" || exit 1
step() { echo "$(date +%T) $mode: $*"; }
[ -x "$W/bin/dbstart" ] || go -C "$W/src-fe45846d" build -o "$W/bin/dbstart" ./developers/soak/dbstart || exit 1
step markers; nice -n 19 python3 $T/markers.py "$out" > markers.txt 2>&1
step doubles; nice -n 19 python3 $T/doubles.py "$out" "$A/doubles.tsv" > doubles.txt 2>&1
step runnerlogs; nice -n 19 python3 $T/runnerlogs.py "$rl" "$A/unacked.tsv" > runnerlogs.txt 2>&1
step latency; nice -n 19 python3 $T/latency.py "$out" "$rl" > latency.txt 2>&1
step rundepcheck; nice -n 19 python3 $T/rundepcheck.py "$out" > rundepcheck.txt 2>&1
step dbstart; nice -n 19 $W/bin/dbstart "$R/dbdir/db" > dbstart.tsv 2> dbstart.err; echo "dbstart rc=$?" >> dbstart.err
step starttimes; nice -n 19 python3 $T/starttimes.py "$out" "$A/dbstart.tsv" "$A/unacked.tsv" > starttimes.txt 2>&1
step relburycheck; nice -n 19 python3 $T/relburycheck.py "$out" "$rl" "$A/dbstart.tsv" > relburycheck.txt 2>&1
step soakgate; nice -n 19 python3 $T/soakgate.py --source wait "$out" "$rl" "$A/doubles.tsv" "$A/dbstart.tsv" > soakgate.txt 2> soakgate.err; echo "soakgate rc=$?" >> soakgate.err
step done
