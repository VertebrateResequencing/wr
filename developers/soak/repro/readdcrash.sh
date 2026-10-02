#!/usr/bin/env bash
# readdcrash.sh <readd:0|1>: does re-adding (--rerun, ignoreComplete=false) a
# RUNNING job, then a kill -9 of the manager, make it run twice? Local
# scheduler. REPRO_ROOT (required) is a scratch directory, wiped per run;
# REPRO_WR (required) is the wr binary to test; REPRO_PORTS is the isolated
# "devPort devWeb prodPort prodWeb" (51876 51877 51878 51879); WRDEV is the
# wrdev.sh to drive (this checkout's).
set -u
readd=${1:?usage: readdcrash.sh <readd:0|1>}
here=$(cd "$(dirname "$0")" && pwd)
W=${WRDEV:-$(git -C "$here" rev-parse --show-toplevel)/developers/wrdev.sh}
read -r DEV_PORT DEV_WEB PROD_PORT PROD_WEB <<< "${REPRO_PORTS:-51876 51877 51878 51879}"
export WRDEV_ROOT=${REPRO_ROOT:?set REPRO_ROOT to a scratch directory}/repro$readd DEV_PORT DEV_WEB PROD_PORT PROD_WEB
# the root is wiped below: never while a manager or runner still runs its binary
pgrep -f "$WRDEV_ROOT/wr" >/dev/null && { echo "a process is running $WRDEV_ROOT/wr*; not wiping it" >&2; exit 1; }
unset $(compgen -v | grep '^OS_')
rm -rf "$WRDEV_ROOT"; mkdir -p "$WRDEV_ROOT/work"; cp "${REPRO_WR:?set REPRO_WR to a wr binary}" "$WRDEV_ROOT/wr"
cd "$WRDEV_ROOT/work" || exit 1
export WR_CONFIG_DIR=$WRDEV_ROOT/config
$W prod-start local | tail -2
WR=$WRDEV_ROOT/wr
cmd="echo run \$\$ >> $WRDEV_ROOT/marks; sleep 45; echo end \$\$ >> $WRDEV_ROOT/marks"
add() { echo "$cmd" | timeout 60 $WR add --deployment production -i rgrepro --retries 0 -m 100M -t 2m "$@" 2>&1 | tail -1; }
add
for i in $(seq 1 30); do grep -q run $WRDEV_ROOT/marks 2>/dev/null && break; sleep 1; done
sleep 8
[ "$readd" = 1 ] && { add --rerun; add --rerun; }
sleep 3
timeout 30 $WR status --deployment production -i rgrepro -o c 2>&1 | tail -3
$W prod-stop | head -1
sleep 3
$W prod-start local | tail -1
sleep 70
echo "marks:"; cat $WRDEV_ROOT/marks
timeout 30 $WR status --deployment production -i rgrepro 2>&1 | grep -E 'Status|Attempts|# ' | head -10
grep -a 'lvl=eror' $WRDEV_ROOT/.wr-prod_production/log | cut -c1-200 | tail -3
$W prod-stop | head -1
pkill -9 -f "$WRDEV_ROOT/wr runner" ; true
