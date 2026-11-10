#!/usr/bin/env bash
# statustime.sh <soakdir> <outdir>: 1g.3 evidence tooling. Watches restarts.tsv;
# on every stop line records the unfolded wal backlog of the (now dead or
# stopped) manager's store, and on every start line with rc=0 records the
# backlog the restarted manager opened with, then times, one after another:
#   B  wr status -i portal -z --limit 1   (history path: decodes overlay entries)
#   A  wr status --limit 1                (default selection, limit 1)
#   C  wr status -l 1                     (the gate's literal form: -l is --cmdline)
#   B2 B again, then the backlog again
# into <outdir>/statustime.tsv. Reads only; kills nothing.
set -u
H=$1/developers/soak
# shellcheck source=/dev/null
. "$H/config.sh"
d=$(soak_outdir "$2") || exit 1
soak_enter "$d"
T=$(cd "$(dirname "$0")" && pwd)
tsv=$d/statustime.tsv
bl() { python3 "$T/walbacklog.py" "$SOAK_DBDIR/db" 2>&1 | tr ' ' '\t'; }
st() { # label args...
  local label=$1 t0 rc ms bytes; shift
  t0=$(date +%s%3N)
  timeout 600 "$SOAK_WR" status --deployment production "$@" > "$d/statustime.last.out" 2>&1; rc=$?
  ms=$(( $(date +%s%3N) - t0 )); bytes=$(wc -c < "$d/statustime.last.out")
  printf '%s\tstatus\t%s\tms=%s\trc=%s\tbytes=%s\n' "$t0" "$label" "$ms" "$rc" "$bytes" >> "$tsv"
}
seen=0
while soak_alive "$d"; do
  n=$(wc -l < "$d/restarts.tsv")
  while [ "$seen" -lt "$n" ]; do
    seen=$(( seen + 1 ))
    line=$(sed -n "${seen}p" "$d/restarts.tsv")
    ev=$(printf '%s' "$line" | cut -f2)
    tag=$(printf '%s' "$line" | tr '\t' '\n' | grep -E '^(manual|kind|rc)=' | tr '\n' ' ')
    case "$ev" in
      stop) printf '%s\tstop\t%s\t%s\n' "$(date +%s%3N)" "$tag" "$(bl)" >> "$tsv" ;;
      start)
        printf '%s\tstart\t%s\t%s\n' "$(date +%s%3N)" "$tag" "$(bl)" >> "$tsv"
        case "$line" in (*"	rc=0	"*) ;; (*) continue ;; esac
        st B -i portal -z --limit 1
        st A --limit 1
        st C -l 1
        st B2 -i portal -z --limit 1
        printf '%s\tafter\t%s\t%s\n' "$(date +%s%3N)" "$tag" "$(bl)" >> "$tsv" ;;
    esac
  done
  sleep 0.5
done
