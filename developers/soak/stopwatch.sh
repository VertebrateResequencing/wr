#!/usr/bin/env bash
# stopwatch.sh <outdir>: while a `wr manager stop` of our manager runs, dumps
# its goroutines every 10s and notes our running LSF job count and which stop
# phases the dump shows, to stopwatch.log.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: stopwatch.sh <outdir>"
d=$(soak_outdir "$1") || exit 1
P=$d/profiles
mkdir -p "$P"
while soak_alive "$d"; do
  if pgrep -f "$SOAK_WR manager stop" >/dev/null; then
    ts=$(date +%s); pid=$(cat "$SOAK_RUN/pid" 2>/dev/null)
    curl -s -m 8 "http://localhost:$PPROF_PORT/debug/pprof/goroutine?debug=2" > "$P/stop.$ts.goroutine2.txt"
    r=$(our_running_jobs)
    echo "$ts stop in progress pid=$pid alive=$(ps -p "$pid" >/dev/null 2>&1 && echo y || echo n) lsfRUN=$r dump=$(wc -c < "$P/stop.$ts.goroutine2.txt") phase=$(grep -oE '(waitForRunnersToDie|scheduler\.\(\*lsf\)\.cleanup|closeServerCommsAndDB|waitForClientHandling|waitForDeletes|finaliseBackup|stopArchiveWriter|stopNewJobsWriter|stopBestEffortWriter|syncFreelist|backupToBackupFile|closeBolt|waitForPortsClosed)\(' "$P/stop.$ts.goroutine2.txt" | sort | uniq -c | awk '{printf "%s:%s,", $2, $1}')" >> "$d/stopwatch.log"
    sleep 10; continue
  fi
  sleep 2
done
