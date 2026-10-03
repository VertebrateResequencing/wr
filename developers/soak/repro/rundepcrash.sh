#!/usr/bin/env bash
# rundepcrash.sh <crash:0|1> [bSecs=30] [crashDelay=3]: a job D that --deps
# dep group G is RUNNING when a new job B is added to G (#649). D must finish
# once (its completion accepted), then run again exactly once, after B
# completes, with no "bad job" rejections. With crash=1 the manager is killed
# -9 a few seconds after B's add (by default D is still running; bSecs 70 and
# crashDelay 40 crash after D's first run ended but before B did) and started
# again on the same DB. Local scheduler, runner logs kept. REPRO_ROOT,
# REPRO_WR, REPRO_PORTS and WRDEV as for readdcrash.sh.
set -u
crash=${1:?usage: rundepcrash.sh <crash:0|1> [bSecs] [crashDelay]} bsecs=${2:-30} cdelay=${3:-3}
here=$(cd "$(dirname "$0")" && pwd)
W=${WRDEV:-$(git -C "$here" rev-parse --show-toplevel)/developers/wrdev.sh}
# pstart <sched> <lines> runs wrdev.sh prod-start, showing its last lines, and
# stops the repro if it failed (such as a refused isolation check)
pstart() { $W prod-start "$1" | tail -"$2"; [ "${PIPESTATUS[0]}" = 0 ] || { echo "wrdev.sh prod-start failed; stopping" >&2; exit 1; }; }
read -r DEV_PORT DEV_WEB PROD_PORT PROD_WEB <<< "${REPRO_PORTS:-51876 51877 51878 51879}"
export WRDEV_ROOT=${REPRO_ROOT:?set REPRO_ROOT to a scratch directory}/rundep$crash-$bsecs-$cdelay DEV_PORT DEV_WEB PROD_PORT PROD_WEB
# the root is wiped below: never while a manager or runner still runs its binary
pgrep -f "$WRDEV_ROOT/wr" >/dev/null && { echo "a process is running $WRDEV_ROOT/wr*; not wiping it" >&2; exit 1; }
unset $(compgen -v | grep '^OS_')
rm -rf "$WRDEV_ROOT"; mkdir -p "$WRDEV_ROOT/work" "$WRDEV_ROOT/runnerlogs"
cp "${REPRO_WR:?set REPRO_WR to a wr binary}" "$WRDEV_ROOT/wr.real"
printf '%s\n' '#!/bin/bash' \
  "if [ \"\$1\" = manager ] && [ \"\$2\" = start ]; then exec $WRDEV_ROOT/wr.real \"\$@\" --runner_filelog $WRDEV_ROOT/runnerlogs; fi" \
  "exec $WRDEV_ROOT/wr.real \"\$@\"" > $WRDEV_ROOT/wr
chmod 755 "$WRDEV_ROOT/wr"
cd "$WRDEV_ROOT/work" || exit 1
export WR_CONFIG_DIR=$WRDEV_ROOT/config
pstart local 2
WR=$WRDEV_ROOT/wr M=$WRDEV_ROOT/marks G=rdg$$
job() { echo "echo S $1 \$(date +%s%3N) \$\$ >> $M; sleep $2; echo E $1 \$(date +%s%3N) \$\$ >> $M"; }
add() { timeout 60 $WR add --deployment production --retries 0 -m 100M -t 2m "$@" 2>&1 | tail -1; }
job A 1 | add -i rdA -e $G
for i in $(seq 1 30); do grep -q '^E A' $M 2>/dev/null && break; sleep 1; done
job D 40 | add -i rdD -d $G
for i in $(seq 1 60); do grep -q '^S D' $M 2>/dev/null && break; sleep 1; done
sleep 8
echo "adding B to $G while D runs"; job B $bsecs | add -i rdB -e $G
timeout 30 $WR status --deployment production -i rdD -o c 2>&1 | tail -2
if [ "$crash" = 1 ]; then
  sleep $cdelay; echo "crash: D ends so far $(grep -c '^E D' $M), B ends $(grep -c '^E B' $M)"; $W prod-stop | head -1; sleep 3; pstart local 1
fi
# D's first run ends ~32s after B's add, B's ~30s after it; D's second run then takes 40s
for i in $(seq 1 300); do [ "$(grep -c '^E D' $M)" -ge 2 ] && break; sleep 1; done
sleep 5
echo "marks:"; cat $M
nd=$(grep -c '^S D' $M); eb=$(awk '$1=="E"&&$2=="B"{print $3}' $M); s2=$(awk '$1=="S"&&$2=="D"{n++; if(n==2)print $3}' $M)
echo "D runs=$nd (want 2); B ends=$eb; D run2 starts=$s2 (want > B end)"
timeout 30 $WR status --deployment production -i rdD 2>&1 | grep -E 'Status|Attempts' | head -4
echo "bad job in manager log: $(grep -ac 'bad job' $WRDEV_ROOT/.wr-prod_production/log)"
echo "bad job in runner logs: $(grep -rac 'bad job' $WRDEV_ROOT/runnerlogs | awk -F: '{s+=$NF} END{print s+0}')"
grep -a 'lvl=eror\|lvl=crit' $WRDEV_ROOT/.wr-prod_production/log | cut -c1-200 | tail -5
$W prod-stop | head -1
pkill -9 -f "$WRDEV_ROOT/wr.real runner"; true
