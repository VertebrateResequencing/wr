#!/usr/bin/env bash
# hook.sh <kind> <outdir> <rundir>: run.sh's WRDEV_PRODSIM_PRESTART_HOOK when
# USE_FUSE=1 (FUSE only), run between each restart's stop and start. Counts
# restarts; before start number FUSE_ON_AT the manager's db symlink is pointed
# through the fusestall mount (the same file, but its fsyncs can then be held),
# and before start number FUSE_OFF_AT it is pointed back at the file directly.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
kind=$1 d=$2 run=$3
n=$(( $(cat "$d/restart.count" 2>/dev/null || echo 0) + 1 )); echo $n > "$d/restart.count"
direct=$SOAK_DBDIR/db viafuse=$FUSE_MNT/db
if [ "$n" = "${FUSE_ON_AT:-3}" ]; then
  if mount | grep -qF " $FUSE_MNT type fuse.fusestall" && [ -e "$viafuse" ]; then
    # fusestall keeps its page cache across opens (see its -coherent), so drop
    # anything it cached before the direct-path manager's writes
    dd if="$viafuse" iflag=nocache count=0 status=none
    ln -sfn "$viafuse" "$run/db"; date +%s > "$d/fuse.on"
    echo "$(date +%s) restart $n ($kind): db now via fusestall: $(readlink "$run/db")"
  else
    echo "$(date +%s) restart $n ($kind): fusestall mount missing; db stays direct"
  fi
elif [ "$n" = "${FUSE_OFF_AT:-4}" ]; then
  ln -sfn "$direct" "$run/db"; date +%s > "$d/fuse.off"
  echo "$(date +%s) restart $n ($kind): db direct again: $(readlink "$run/db")"
else
  echo "$(date +%s) restart $n ($kind): db $(readlink "$run/db")"
fi
