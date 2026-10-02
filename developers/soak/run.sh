#!/usr/bin/env bash
# Soak launcher (developer tooling, not part of wr). Builds the isolated binary
# from WRSRC, optionally mounts fusestall over the working DB's directory, then
# runs `wrdev.sh prodsim` with the helper scripts (disk guard, ramp, stop
# watcher, restart watcher, LSF probe, stall inducer) pointed at its output
# directory. Settings come from config.sh plus these:
#
#   HOURS SIMMIN SCALE          wrdev.sh prodsim's arguments (3 6 1)
#   RESTART_MIN RESTART_KINDS   restart schedule (30, clean,crash,clean,crash,clean)
#   RAMP0                       portal concurrency at the start (600)
#   RAMP                        ramp.sh stages ("0:600 10:1200 20:2100 35:3000 100:3300")
#   FIXTURE                     DB to start from (none: an empty DB)
#   CTR_IMAGE                   singularity image for prodsim's container jobs,
#                               readable from every exec node (none: no container jobs)
#   USE_FUSE=1                  put the DB behind fusestall from restart
#                               FUSE_ON_AT to FUSE_OFF_AT (3, 4), with one
#                               induced commit stall of STALL_SECS (180)
#                               STALL_AFTER_MIN (15) minutes in. Needs FUSE.
#   RUNNER_FILELOG=1            runners keep logs in $SOAK_ROOT/runnerlogs
#   EXTRA_ARGS                  more prodsim flags
#
# LSF: SCHED=lsf (the default) needs bjobs and bkill on PATH; SCHED=local runs
# everything on this host, without the LSF probe.
set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=config.sh
. "$here/config.sh"

if [ "$SCHED" = lsf ]; then need_lsf; fi
if [ "$USE_FUSE" = 1 ]; then
  { [ -e /dev/fuse ] && command -v fusermount >/dev/null; } || die "USE_FUSE=1 needs /dev/fuse and fusermount"
fi
if [ -n "${CTR_IMAGE:-}" ]; then
  [ -r "$CTR_IMAGE" ] || die "CTR_IMAGE $CTR_IMAGE is not readable"
  command -v singularity >/dev/null || die "CTR_IMAGE needs singularity on PATH"
fi
if [ -n "${FIXTURE:-}" ]; then [ -r "$FIXTURE" ] || die "FIXTURE $FIXTURE is not readable"; fi

# Rebuilding writes $SOAK_WR in place. A runner still executing that file
# (here, or on an exec node over NFS) dies when it is rewritten, and its job
# then looks like it ran twice, so refuse while anything may be using it.
if pgrep -f "$SOAK_WR" >/dev/null; then
  die "a process is running $SOAK_WR*; let it finish or use another SOAK_ROOT"
fi
if have_lsf && lsf_has_jobs "$JOB_PREFIX"; then
  die "LSF still has ${JOB_PREFIX}* jobs, whose runners may use $SOAK_WR; wait for them or use other ports"
fi

mkdir -p "$SOAK_ROOT" "$SOAK_DBDIR" || die "could not make $SOAK_ROOT and $SOAK_DBDIR"
rm -f "$SOAK_ROOT/current"
# shellcheck disable=SC2046 # one name per word
unset $(compgen -v | grep '^OS_') 2>/dev/null
# a previous RUNNER_FILELOG=1 run left $SOAK_WR as a wrapper script, which go
# build will not overwrite; nothing is running it (checked above)
if [ -e "$SOAK_WR.real" ]; then mv -f "$SOAK_WR.real" "$SOAK_WR" || die "could not restore $SOAK_WR"; fi
WRDEV_REPO="$WRSRC" "$WRDEV" build || exit 1
if [ "${RUNNER_FILELOG:-0}" = 1 ]; then
  # runners keep logs under $SOAK_ROOT/runnerlogs: wrap the isolated binary so
  # every manager start (including the helpers') adds --runner_filelog
  mv -f "$SOAK_WR" "$SOAK_WR.real"; mkdir -p "$SOAK_ROOT/runnerlogs"
  printf '%s\n' '#!/bin/bash' \
    "if [ \"\$1\" = manager ] && [ \"\$2\" = start ]; then exec $SOAK_WR.real \"\$@\" --runner_filelog $SOAK_ROOT/runnerlogs; fi" \
    "exec $SOAK_WR.real \"\$@\"" > "$SOAK_WR"
  chmod 755 "$SOAK_WR"
fi
go -C "$SOAK_REPO" build -o "$SOAK_ROOT/psinspect" ./developers/soak/psinspect || exit 1
if [ "$USE_FUSE" = 1 ]; then
  go -C "$SOAK_REPO" build -o "$SOAK_ROOT/fusestall" ./developers/soak/fusestall || exit 1
  mkdir -p "$FUSE_MNT" || die "could not make $FUSE_MNT"
  # a clean mount: nothing of ours may still be using an old one
  if mount | grep -qF " $FUSE_MNT type fuse"; then fusermount -u "$FUSE_MNT" || exit 1; fi
  rm -f "$STALL_CTL"
  export WRDEV_PRODSIM_PRESTART_HOOK="$here/hook.sh"
  export FUSE_ON_AT="${FUSE_ON_AT:-3}" FUSE_OFF_AT="${FUSE_OFF_AT:-4}"
  export STALL_AFTER_MIN="${STALL_AFTER_MIN:-15}" STALL_SECS="${STALL_SECS:-180}"
fi
export WRDEV_PRODSIM_DB="${FIXTURE:-}"
export WRDEV_PRODSIM_DBDIR="$SOAK_DBDIR"
export WRDEV_PRODSIM_SCHED="$SCHED" WRDEV_PRODSIM_PPROF="$PPROF_PORT"
export WRDEV_PRODSIM_RESTART_MIN="${RESTART_MIN:-30}"
export WRDEV_PRODSIM_RESTART_KINDS="${RESTART_KINDS:-clean,crash,clean,crash,clean}"
export WRDEV_PRODSIM_FINAL_STOP=1
ramp0=${RAMP0:-600}
echo "$ramp0" > "$SOAK_ROOT/portal_target"
ctr=""
[ -n "${CTR_IMAGE:-}" ] && ctr="-ctr-image $CTR_IMAGE -ctr-jobs 25"
export WRDEV_PRODSIM_ARGS="-portal-target-file $SOAK_ROOT/portal_target -portal-gap 5m -portal-median-mins 4 \
-portal-cmd-kb 10 -portal-limit $ramp0 -operator-limits=false $ctr \
-spike-call 15s -spike-gap 10m ${EXTRA_ARGS:-}"
t0=$(date +%s)
"$WRDEV" prodsim "${HOURS:-3}" "${SIMMIN:-6}" "${SCALE:-1}" &
wp=$!
# a background job of a non-interactive shell ignores SIGINT, so wrdev.sh's own
# INT trap never fires: pass Ctrl-C (and TERM) on as a TERM, which runs its
# cleanup (stop prodsim and our manager, bkill only our jobs). Only once: a
# second TERM would abort that cleanup part-way (its final clean stop can take
# many minutes), leaving our manager and jobs behind.
trap 'trap "echo \"run: already stopping; wait for wrdev.sh cleanup to finish\"" INT TERM
  echo "run: stopping wrdev.sh prodsim"; kill -TERM $wp 2>/dev/null' INT TERM
# prodsim's output dir is the newest prodsim-* made after we started
out=""
for _ in $(seq 1 600); do
  kill -0 $wp 2>/dev/null || break
  for c in "$SOAK_ROOT"/prodsim-*; do [ -d "$c" ] && [ "${c##*-}" -ge "$t0" ] 2>/dev/null && out=$c; done
  [ -n "$out" ] && [ -e "$out/samples.tsv" ] && break
  sleep 2
done
[ -n "$out" ] || { echo "run: no prodsim output dir"; kill -TERM $wp; wait $wp; exit 1; }
echo "run: output $out"
# the injectors started beside this script (crashon.sh, rundep.sh, relbury.sh)
# wait for this file to name the output dir
echo "$out" > "$SOAK_ROOT/current"
fp=""
if [ "$USE_FUSE" = 1 ]; then
  "$SOAK_ROOT/fusestall" -dir "$SOAK_DBDIR" -mnt "$FUSE_MNT" -ctl "$STALL_CTL" -max 10m > "$out/fusestall.log" 2>&1 &
  fp=$!
  "$here/stall.sh" "$out" &
fi
"$here/diskguard.sh" "$out" $wp &
"$here/ramp.sh" "$out" "${RAMP:-0:600 10:1200 20:2100 35:3000 100:3300}" &
"$here/stopwatch.sh" "$out" &
"$here/watcher.sh" "$out" &
if [ "$SCHED" = lsf ]; then "$here/lsfprobe.sh" "$out" & fi
# a trapped signal interrupts wait (status 128+n) before wrdev.sh's cleanup
# has finished, so wait until it has gone; bash keeps its real exit status for
# a final wait, which only gives 127 if that status was already collected
wait $wp; rc=$?
while kill -0 $wp 2>/dev/null; do wait $wp; rc=$?; done
wait $wp 2>/dev/null; r=$?; [ "$r" -ne 127 ] && rc=$r
echo "run: wrdev.sh prodsim exited $rc"
sleep 5
if [ -n "$fp" ]; then
  kill -TERM "$fp" 2>/dev/null; wait "$fp" 2>/dev/null
  mount | grep -qF " $FUSE_MNT type fuse" && fusermount -u "$FUSE_MNT"
fi
python3 "$here/markers.py" "$out" > "$out/markers-analysis.txt" 2>&1
echo "run: done; markers analysis in $out/markers-analysis.txt"
