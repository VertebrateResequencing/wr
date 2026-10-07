#!/bin/bash
cd /tmp/claude-11346/customdb
NFS=/nfs/hgi/wr/sb10-bigdb/customdb-proto
echo "### crash slots (fixed) $(date +%T)"
timeout 1200 nice -n 19 ./crashtest2 -design slots -dir /tmp/claude-11346/customdb/crash -iters 10 2>&1 | tail -1
timeout 1200 nice -n 19 ./crashtest2 -design slots -dir $NFS/crash -iters 6 2>&1 | tail -1
rm -rf /tmp/claude-11346/customdb/crash $NFS/crash
echo "### D5 ship $(date +%T)"
rm -rf hp/wal-ship $NFS/hp/wal-ship-copy
timeout 900 nice -n 19 ./hotpath2 -design wal -dir /tmp/claude-11346/customdb/hp/wal-ship -ship $NFS/hp/wal-ship-copy -runners 6000 -preload 120000 -cmd 10000 -secs 180 -ramp 30 -dmin 20s -dmax 60s -addevery 5s -addn 1000 2>&1 | grep -v "^$"
rm -rf hp/wal-ship $NFS/hp/wal-ship-copy
echo "### ceiling $(date +%T)"
for d in wal keyfiles slots; do
  rm -rf $NFS/sat/$d
  echo "=== ceiling $d $(date +%T)"
  timeout 900 nice -n 19 ./hotpath2 -design $d -dir $NFS/sat/$d -runners 6000 -preload 250000 -cmd 10000 -secs 60 -ramp 5 -dmin 0s -dmax 0s -addevery 1s -addn 1000 2>&1 | grep -v "^$"
  rm -rf $NFS/sat/$d
done
echo "### recovery $(date +%T)"
for d in slots boltsmall; do
  timeout 3000 nice -n 19 ./recoverbench2 -design $d -dir $NFS/rec/$d-120k -n 120000 -cmd 10000 2>&1
  rm -rf $NFS/rec/$d-120k
done
for d in keyfiles slots boltsmall; do
  timeout 5400 nice -n 19 ./recoverbench2 -design $d -dir $NFS/rec/$d-800k -n 800000 -cmd 10000 2>&1
  rm -rf $NFS/rec/$d-800k
done
echo "### phase3 done $(date +%T)"
