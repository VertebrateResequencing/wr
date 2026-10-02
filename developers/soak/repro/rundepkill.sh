#!/usr/bin/env bash
# rundepkill.sh <restart:0|1|2|3>: like rundepcrash.sh, but D (--deps G, running when B joined G) is
# killed with `wr kill` while B still runs, as a clean manager stop kills running jobs. The spec
# (.docs/dep-granularity/spec.md, running waiter): "a bury leaves it buried with the new
# dependencies ... a kick makes it dependent". Prints D's and B's status after the kill, after B
# ends, and (restart=1) after a clean manager stop and start. restart=2 also kills B (as a stop
# kills every running job) before the restart, so D's new dep group member is buried. restart=3
# is 2 with D killed (buried) BEFORE B joins G, the case that predates #649 (spec B2).
# Local scheduler. REPRO_ROOT, REPRO_WR, REPRO_PORTS and WRDEV as for readdcrash.sh.
set -u
rs=${1:?usage: rundepkill.sh <restart:0|1|2|3>}
here=$(cd "$(dirname "$0")" && pwd)
W=${WRDEV:-$(git -C "$here" rev-parse --show-toplevel)/developers/wrdev.sh}
read -r DEV_PORT DEV_WEB PROD_PORT PROD_WEB <<< "${REPRO_PORTS:-51876 51877 51878 51879}"
export WRDEV_ROOT=${REPRO_ROOT:?set REPRO_ROOT to a scratch directory}/rundepkill$rs DEV_PORT DEV_WEB PROD_PORT PROD_WEB
# the root is wiped below: never while a manager or runner still runs its binary
pgrep -f "$WRDEV_ROOT/wr" >/dev/null && { echo "a process is running $WRDEV_ROOT/wr*; not wiping it" >&2; exit 1; }
unset $(compgen -v | grep '^OS_')
rm -rf "$WRDEV_ROOT"; mkdir -p "$WRDEV_ROOT/work"; cp "${REPRO_WR:?set REPRO_WR to a wr binary}" "$WRDEV_ROOT/wr"
cd "$WRDEV_ROOT/work" || exit 1; export WR_CONFIG_DIR=$WRDEV_ROOT/config
$W prod-start local | tail -1
WR=$WRDEV_ROOT/wr M=$WRDEV_ROOT/marks G=rdg$$
job() { echo "echo S $1 \$(date +%s%3N) \$\$ >> $M; sleep $2; echo E $1 \$(date +%s%3N) \$\$ >> $M"; }
add() { timeout 60 $WR add --deployment production --retries 0 -m 100M -t 2m "$@" 2>&1 | tail -1; }
st() { for g in rdD rdB rdC; do echo "  $g: $(timeout 30 $WR status --deployment production -i $g 2>&1 | grep -E '^Status:' | cut -c1-90)"; done; }
job A 1 | add -i rdA -e $G
for i in $(seq 1 30); do grep -q '^E A' $M 2>/dev/null && break; sleep 1; done
job D 60 | add -i rdD -d $G
job C 60 | add -i rdC
for i in $(seq 1 60); do grep -q '^S D' $M 2>/dev/null && grep -q '^S C' $M && break; sleep 1; done
killd() { echo "killing D (and C, a job with no deps, for comparison)"; timeout 30 $WR kill --deployment production -i rdD 2>&1 | tail -1; timeout 30 $WR kill --deployment production -i rdC 2>&1 | tail -1; }
if [ "$rs" = 3 ]; then sleep 5; killd; for i in $(seq 1 60); do timeout 30 $WR status --deployment production -i rdD 2>&1 | grep -q "^Status: buried" && break; sleep 1; done; echo "D buried before B joins G:"; st; job B 40 | add -i rdB -e $G; sleep 5
else sleep 5; job B 40 | add -i rdB -e $G; sleep 3; killd; fi
sleep 10; echo "after the kill:"; st
[ "$rs" -ge 2 ] && { timeout 30 $WR kill --deployment production -i rdB 2>&1 | tail -1; sleep 8; echo "after killing B:"; st; }
if [ "$rs" -lt 2 ]; then
  for i in $(seq 1 60); do grep -q '^E B' $M && break; sleep 1; done; sleep 5
  echo "after B ended:"; st
fi
if [ "$rs" != 0 ]; then
  timeout 300 $WR manager stop --deployment production 2>&1 | tail -1; sleep 2; $W prod-start local | tail -1; sleep 5
  echo "after a clean restart:"; st
fi
sleep 30; echo "marks:"; cat $M
$W prod-stop | head -1; pkill -9 -f "$WRDEV_ROOT/wr runner"; true
