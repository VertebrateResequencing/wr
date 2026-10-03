#!/usr/bin/env bash
# diskguard.sh <outdir> <wrdevpid>: stops the soak (its wrdev.sh cleanup runs)
# when the filesystem holding SOAK_ROOT or SOAK_DBDIR has under GUARD_GB free,
# or ${TMPDIR:-/tmp} under TMP_GUARD_GB. Logs free space to disk.tsv every 30s.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 2 ] || die "usage: diskguard.sh <outdir> <wrdevpid>"
d=$(soak_outdir "$1") || exit 1
w=$2 tmp=${TMPDIR:-/tmp}
free_gb() { df -B1G --output=avail "$1" | tail -1 | tr -d ' '; }
while soak_alive "$d"; do
  t=$(free_gb "$tmp") r=$(free_gb "$SOAK_ROOT") b=$(free_gb "$SOAK_DBDIR")
  echo "$(date +%s) tmp=${t}G root=${r}G dbdir=${b}G db_mb=$(( $(stat -L -c %s "$SOAK_RUN/db" 2>/dev/null || echo 0) >> 20 )) bk_mb=$(( $(stat -L -c %s "$SOAK_RUN/db_bk" 2>/dev/null || echo 0) >> 20 ))" >> "$d/disk.tsv"
  if [ "$t" -lt "$TMP_GUARD_GB" ] || [ "$r" -lt "$GUARD_GB" ] || [ "$b" -lt "$GUARD_GB" ]; then
    echo "$(date +%s) diskguard: tmp=${t}G root=${r}G dbdir=${b}G, stopping the soak" >> "$d/watcher.log"
    # only ever signal the wrdev.sh prodsim we were given
    [ "$(ps -o args= -p "$w" 2>/dev/null | grep -c 'wrdev.sh prodsim')" = 1 ] && kill -TERM "$w"
    exit 0
  fi
  sleep 30
done
