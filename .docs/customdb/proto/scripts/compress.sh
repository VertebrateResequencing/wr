#!/bin/bash
# Does compressing the log keep or improve D2's speed while cutting its size?
# And with compression, are lazy specs (Phase 3) still needed for recovery?
# Every run uses real production commands (corpus files made by compbench
# -corpusout and bigcorpus), on NFS, one at a time.
# usage: compress.sh <workdir with bin/, corpus.all, corpus.big> <nfs dir>
set -u
W=$1
NFS=$2
mkdir -p "$NFS"
cd "$W" || exit 1

echo "### hot path, production shape, 10KB real-command jobs $(date +%T)"
HP="-runners 6000 -preload 120000 -cmd 10000 -secs 180 -ramp 30 -dmin 20s -dmax 60s -addevery 5s -addn 1000 -corpus corpus.big"
for bc in none s2 zstd; do
  rm -rf "$NFS/hp-$bc"
  echo "=== hp batchcodec=$bc $(date +%T)"
  timeout 1200 nice -n 19 bin/hotpath -design wal -dir "$NFS/hp-$bc" $HP -batchcodec $bc 2>&1 | grep -v '^$'
  rm -rf "$NFS/hp-$bc"
done

echo "### saturation, 10KB real-command jobs $(date +%T)"
SAT="-runners 6000 -preload 60000 -cmd 10000 -secs 60 -ramp 5 -dmin 0s -dmax 1s -addevery 5s -addn 1000 -corpus corpus.big"
for bc in none s2 zstd; do
  rm -rf "$NFS/sat-$bc"
  echo "=== sat batchcodec=$bc $(date +%T)"
  timeout 1200 nice -n 19 bin/hotpath -design wal -dir "$NFS/sat-$bc" $SAT -batchcodec $bc 2>&1 | grep -v '^$'
  rm -rf "$NFS/sat-$bc"
done

echo "### recovery, production shape: 118k jobs with real command sizes $(date +%T)"
for bc in none s2 zstd; do
  timeout 3000 nice -n 19 bin/recoverbench -design wal -dir "$NFS/rec-prod-$bc" -n 118213 -cmd 0 -corpus corpus.all -batchcodec $bc -heap 2>&1
  du -sh "$NFS/rec-prod-$bc"
  rm -rf "$NFS/rec-prod-$bc"
done

echo "### recovery, portal worst case: 800k jobs of 10KB real commands $(date +%T)"
for bc in none s2 zstd; do
  timeout 5400 nice -n 19 bin/recoverbench -design wal -dir "$NFS/rec-800k-$bc" -n 800000 -cmd 10000 -corpus corpus.big -batchcodec $bc -heap 2>&1
  du -sh "$NFS/rec-800k-$bc"
  rm -rf "$NFS/rec-800k-$bc"
done

echo "### D3 lazy specs for comparison, 800k $(date +%T)"
timeout 5400 nice -n 19 bin/recoverbench -design slots -dir "$NFS/rec-800k-slots" -n 800000 -cmd 10000 -corpus corpus.big 2>&1
du -sh "$NFS/rec-800k-slots"
rm -rf "$NFS/rec-800k-slots"
echo "=== done $(date +%T)"
