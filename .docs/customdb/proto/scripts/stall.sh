#!/bin/bash
# Phase 4 question: what does a 60s filesystem commit stall do to hand-outs and
# completions under D2, D6 (strict leases) and D5 (local primary shipping to
# the stalled NFS)? Every store, or D5's replica, sits on NFS behind fusestall.
# usage: stall.sh <workdir with bin/> <nfs dir>
set -u
W=$1
NFS=$2
MNT=$W/fusemnt
CTL=$W/stall.ctl
OUT=$W/out/stall
mkdir -p "$MNT" "$NFS" "$OUT" "$W/local"
rm -f "$CTL"

"$W/bin/fusestall" -dir "$NFS" -mnt "$MNT" -ctl "$CTL" -max 5m > "$OUT/fusestall.log" 2>&1 &
FPID=$!
cleanup() {
  rm -f "$CTL"
  fusermount -u "$MNT" 2>/dev/null
  kill "$FPID" 2>/dev/null
}
trap cleanup EXIT
for _ in $(seq 50); do mountpoint -q "$MNT" && break; sleep 0.2; done
mountpoint -q "$MNT" || { echo "fusestall did not mount"; exit 1; }

COMMON="-runners 6000 -preload 120000 -cmd 10000 -secs 240 -ramp 30 -dmin 20s -dmax 60s -addevery 5s -addn 1000 -stallctl $CTL -stallat 90s -stallfor 60s"
one() { # name dir extra...
  name=$1; dir=$2; shift 2
  rm -rf "$dir"
  echo "=== $(date +%T) $name"
  timeout 1200 nice -n 19 "$W/bin/hotpath" -design wal -dir "$dir" $COMMON -timeline "$OUT/$name.tl" "$@" 2>&1 | grep -v '^$'
  rm -rf "$dir"
}
one d2 "$MNT/d2"
one d6 "$MNT/d6" -lease
one d5 "$W/local/d5" -ship "$MNT/d5-copy"
rm -rf "$MNT/d5-copy"
echo "=== done $(date +%T)"
