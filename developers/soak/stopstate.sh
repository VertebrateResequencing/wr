#!/usr/bin/env bash
# stopstate.sh <outdir>: while a `wr manager stop` of our manager runs, polls
# the manager pid's /proc state every 0.2s and logs each change, then, when the
# stop command exits, the pid's state at that moment (Z = exited, awaiting
# reaping) and whether the client token is still there, to stopstate.log.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -eq 1 ] || die "usage: stopstate.sh <outdir>"
d=$(soak_outdir "$1") || exit 1
st() { awk '{print $3}' "/proc/$1/stat" 2>/dev/null || echo gone; }
while soak_alive "$d"; do
  sp=$(pgrep -f "$SOAK_WR manager stop" | head -1)
  if [ -n "$sp" ]; then
    pid=$(cat "$SOAK_RUN/pid" 2>/dev/null); last=""
    while [ -e "/proc/$sp" ]; do
      s=$(st "$pid"); [ "$s" != "$last" ] && { echo "$(date +%s.%N | cut -c1-14) pid=$pid state=$s" >> "$d/stopstate.log"; last=$s; }
      sleep 0.2
    done
    echo "$(date +%s.%N | cut -c1-14) stop command exited; pid=$pid state=$(st "$pid") token=$([ -e "$SOAK_RUN/client.token" ] && echo present || echo absent)" >> "$d/stopstate.log"
    for _ in $(seq 1 50); do s=$(st "$pid"); [ "$s" != "$last" ] && { echo "$(date +%s.%N | cut -c1-14) pid=$pid state=$s" >> "$d/stopstate.log"; last=$s; }; [ "$s" = gone ] && break; sleep 0.2; done
  fi
  sleep 1
done
