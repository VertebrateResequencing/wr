#!/bin/bash
cd /tmp/claude-11346/customdb
until grep -q "=== done" hp.out; do sleep 20; done
NFS=/nfs/hgi/wr/sb10-bigdb/customdb-proto
echo "### saturation $(date +%T)"
for d in wal keyfiles slots boltsmall sqlite; do
  rm -rf $NFS/sat/$d; mkdir -p $NFS/sat
  echo "=== sat $d $(date +%T)"
  timeout 900 nice -n 19 ./hotpath -design $d -dir $NFS/sat/$d -runners 6000 -preload 60000 -cmd 10000 -secs 60 -ramp 5 -dmin 0s -dmax 1s -addevery 5s -addn 1000 2>&1 | grep -v "^$"
  rm -rf $NFS/sat/$d
done
echo "### crash $(date +%T)"
for d in wal keyfiles slots sqlite; do
  timeout 1200 nice -n 19 ./crashtest -design $d -dir /tmp/claude-11346/customdb/crash -iters 10 2>&1 | tail -11
  timeout 1200 nice -n 19 ./crashtest -design $d -dir $NFS/crash -iters 6 2>&1 | tail -7
done
rm -rf /tmp/claude-11346/customdb/crash $NFS/crash
echo "### recovery $(date +%T)"
for d in wal keyfiles slots boltsmall sqlite; do
  timeout 3000 nice -n 19 ./recoverbench -design $d -dir $NFS/rec/$d-120k -n 120000 -cmd 10000 2>&1
  rm -rf $NFS/rec/$d-120k
done
for d in wal slots boltsmall; do
  timeout 5400 nice -n 19 ./recoverbench -design $d -dir $NFS/rec/$d-800k -n 800000 -cmd 10000 2>&1
  du -sh $NFS/rec/$d-800k
  rm -rf $NFS/rec/$d-800k
done
echo "### phase2 done $(date +%T)"
