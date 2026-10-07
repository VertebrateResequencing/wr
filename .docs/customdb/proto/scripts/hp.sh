#!/bin/bash
# production-shaped hot-path matrix; one design at a time.
cd /tmp/claude-11346/customdb
NFS=/nfs/hgi/wr/sb10-bigdb/customdb-proto/hp
LOC=/tmp/claude-11346/customdb/hp
COMMON="-runners 6000 -preload 120000 -cmd 10000 -secs 180 -ramp 30 -dmin 20s -dmax 60s -addevery 5s -addn 1000"
one() { # base name design extra...
  base=$1; name=$2; d=$3; shift 3
  rm -rf $base/$name
  echo "=== $(date +%T) $base $name $d $*"
  timeout 900 nice -n 19 ./hotpath -design $d -dir $base/$name $COMMON "$@" 2>&1 | grep -v "^$"
  du -sh $base/$name 2>/dev/null
  rm -rf $base/$name
}
mkdir -p $NFS $LOC
for d in wal slots boltsmall boltfull sqlite; do one $LOC $d $d; done
for d in wal keyfiles slots boltsmall boltfull sqlite; do one $NFS $d $d; done
one $NFS wal-nosync wal -nosync
one $NFS boltsmall-nosync boltsmall -nosync
one $NFS wal-lease wal -lease
one $LOC wal-ship wal -ship $NFS/wal-ship-copy
rm -rf $NFS/wal-ship-copy
echo "=== done $(date +%T)"
