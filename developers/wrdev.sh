#!/usr/bin/env bash
#
# wrdev.sh - safe helper for wr reliability testing. See ../DEVELOPERS.md.
#
# Runs an ISOLATED wr manager (own config, ports, managerdir, wrd_* job names)
# that can never disturb a real --deployment production manager. Refuses to
# kill anything that is not its own isolated binary. Everything lives under
# $WRDEV_ROOT (default $HOME/wr-devtest).
#
# NOT part of the shipped binary or test suite.

set -u

# --- config (override via env) ----------------------------------------------
WRDEV_ROOT="${WRDEV_ROOT:-$HOME/wr-devtest}"
DEV_PORT="${DEV_PORT:-51780}"       # dev manager RPC / web
DEV_WEB="${DEV_WEB:-51781}"
PROD_PORT="${PROD_PORT:-51782}"     # isolated prod-mode manager RPC / web
PROD_WEB="${PROD_WEB:-51783}"
QUEUE="${QUEUE:-normal}"            # LSF queue to force jobs to
MEM_GROUPS="${MEM_GROUPS:-100}"     # spread jobs across this many memory groups

WR="$WRDEV_ROOT/wr"                 # isolated binary (all our managers use this)
WSPROBE="$WRDEV_ROOT/wsprobe"
CONFIG_DIR="$WRDEV_ROOT/config"
DEV_RUN="$WRDEV_ROOT/.wr_development"
PROD_RUN="$WRDEV_ROOT/.wr-prod_production"
export WR_CONFIG_DIR="$CONFIG_DIR"

REPO="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel 2>/dev/null)"

die() { echo "wrdev: $*" >&2; exit 1; }
osunset() { unset $(compgen -v | grep '^OS_' 2>/dev/null) 2>/dev/null; true; }  # OS_* unset

ensure_config() {
  mkdir -p "$CONFIG_DIR"
  cat > "$CONFIG_DIR/.wr_config.development.yml" <<EOF
managerport: "$DEV_PORT"
managerweb: "$DEV_WEB"
managerhost: "localhost"
managerdir: "$WRDEV_ROOT/.wr"
EOF
  cat > "$CONFIG_DIR/.wr_config.production.yml" <<EOF
managerport: "$PROD_PORT"
managerweb: "$PROD_WEB"
managerhost: "localhost"
managerdir: "$WRDEV_ROOT/.wr-prod"
EOF
}

# only ever kills a PID whose cmdline runs OUR isolated binary; never a real
# production manager or anything else.
is_ours() { ps -o cmd= -p "$1" 2>/dev/null | grep -qF "$WR"; }
safe_kill() {
  local pid="$1"
  [ -n "$pid" ] || return 0
  if ! ps -p "$pid" >/dev/null 2>&1; then echo "manager pid $pid already stopped"; return 0; fi
  if is_ours "$pid"; then kill -9 "$pid" && echo "killed our manager pid $pid"; else
    echo "refusing to kill pid $pid (running process is not our isolated binary)"; fi
}
mgr_pid() { cat "$1/pid" 2>/dev/null; }

bkill_dev() {  # dev jobs are wrd_* only; safe. (prod-mode wrp_* is NEVER pattern-killed here)
  for _ in 1 2 3; do
    timeout 120 bkill -J 'wrd_*' 0 >/dev/null 2>&1
    sleep 4
    local n; n=$(timeout 60 bjobs -o stat -noheader 2>/dev/null | grep -c RUN)
    [ "${n:-0}" -eq 0 ] && break
    # stuck RUN array elements: force-remove by exact jobid
    timeout 60 bjobs -o 'jobid job_name' -noheader 2>/dev/null | awk '$2 ~ /^wrd_/{print $1}' \
      | sort -u | while read -r j; do timeout 30 bkill -r "$j" >/dev/null 2>&1; done
  done
}

need_repo() { [ -n "$REPO" ] || die "run from inside the wr git checkout"; }
need_bin()  { [ -x "$WR" ] || die "no isolated binary; run: $0 build"; }

# ----------------------------------------------------------------------------
cmd_build() {
  need_repo; mkdir -p "$WRDEV_ROOT"
  echo "building wr from $(git -C "$REPO" rev-parse --short HEAD) -> $WR"
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags netgo -o "$WR" . ) || die "build failed"
  GOFLAGS=-mod=mod GOPROXY=off go build -C "$REPO/.docs/reliable2/phase2/wsprobe" -o "$WSPROBE" . \
    2>/dev/null && echo "built wsprobe -> $WSPROBE" || echo "wsprobe build skipped"
  echo "OK"
}

cmd_start() {  # start [lsf|local]
  need_bin; ensure_config
  local sched="${1:-lsf}"
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s $sched) on :$DEV_PORT / web :$DEV_WEB"
  osunset ; timeout 90 "$WR" manager start --deployment development -s "$sched" 2>&1 \
    | grep -aE 'started on|token=' | head -2
  echo "pid $(mgr_pid "$DEV_RUN")   token $(cat "$DEV_RUN/client.token" 2>/dev/null)"
}

cmd_stop() {  # wr manager stop hangs under load; kill our verified pid + bkill wrd_
  safe_kill "$(mgr_pid "$DEV_RUN")"
  bkill_dev
}

cmd_churn() {  # churn [N]  (default 40000; ~half true half false, across memory groups)
  need_bin
  local n="${1:-40000}"; local half=$(( n / 2 ))
  echo "generating $n jobs ($half true + $half false) across $MEM_GROUPS memory groups"
  perl -e "for my \$i (1..$half){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"true #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/true.json"
  perl -e "for my \$i (1..$half){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"false #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/false.json"
  osunset
  timeout 180 "$WR" add -f "$WRDEV_ROOT/true.json"  --rep_grp rgtrue  --retries 0 --deployment development 2>&1 | tail -1
  timeout 180 "$WR" add -f "$WRDEV_ROOT/false.json" --rep_grp rgfalse --retries 0 --deployment development 2>&1 | tail -1
  cmd_monitor "$half"
}

cmd_monitor() {  # watch drain + churn counts + control-RPC latency until terminal/stall
  need_bin
  local half="${1:-40000}"; local t0; t0=$(date +%s); local prev=-1 stall=0
  num(){ echo "$1" | grep -oE "$2: [0-9]+" | grep -oE '[0-9]+' | head -1; }
  for _ in $(seq 1 80); do
    local ct cf tc tb fb fc run s e
    ct=$(osunset ; timeout 30 "$WR" status --deployment development -i rgtrue  -o counts 2>/dev/null)
    cf=$(timeout 30 "$WR" status --deployment development -i rgfalse -o counts 2>/dev/null)
    tc=$(num "$ct" complete); tb=$(num "$ct" buried); fb=$(num "$cf" buried); fc=$(num "$cf" complete)
    tc=${tc:-0}; tb=${tb:-0}; fb=${fb:-0}; fc=${fc:-0}
    local tterm=$((tc+tb)) fterm=$((fb+fc)) total=$((tc+tb+fb+fc))
    run=$(timeout 20 bjobs -o stat -noheader 2>/dev/null | grep -c RUN)
    local bj nr; bj=$(grep -c 'bad job' "$DEV_RUN/log" 2>/dev/null || true); nr=$(grep -ciE 'not running' "$DEV_RUN/log" 2>/dev/null || true)
    s=$(date +%s%3N); timeout 65 "$WR" status --deployment development -o counts >/dev/null 2>&1; e=$(date +%s%3N)
    echo "t+$(( $(date +%s)-t0 ))s RUN=$run terminal=$total/$((half*2)) rgtrue(c=$tc,b=$tb) rgfalse(b=$fb,c=$fc) badjob=${bj:-0} notrun=${nr:-0} status_rpc=$((e-s))ms"
    if [ "$tterm" -ge "$half" ] && [ "$fterm" -ge "$half" ]; then echo "FULLY DRAINED"; break; fi
    if [ "$total" -eq "$prev" ]; then stall=$((stall+1)); else stall=0; fi
    prev=$total
    [ "$stall" -ge 8 ] && { echo "NO PROGRESS ~6min at terminal=$total (investigate: $0 dump)"; break; }
    sleep 45
  done
}

cmd_probe() {  # probe [secs] [slowms]  - read the dev web /status_ws feed
  [ -x "$WSPROBE" ] || die "no wsprobe; run: $0 build"
  local secs="${1:-3}" slow="${2:-0}"
  local tok; tok=$(cat "$DEV_RUN/client.token" 2>/dev/null) || die "dev manager not running"
  "$WSPROBE" "localhost:$DEV_WEB" "$tok" "$secs" "$slow"
}

cmd_web_burst() {  # web-burst [N] - reproduce the status-bar freeze-under-burst (local, slow reader)
  need_bin; [ -x "$WSPROBE" ] || die "no wsprobe; run: $0 build"
  local n="${1:-10000}"
  cmd_start local
  local tok; tok=$(cat "$DEV_RUN/client.token")
  echo "starting SLOW wsprobe (models a slow browser) then adding $n fast echo jobs"
  "$WSPROBE" "localhost:$DEV_WEB" "$tok" 900 3 > "$WRDEV_ROOT/webprobe.out" 2>&1 &
  local wp=$!; sleep 2
  osunset ; perl -e "for (1..$n){print \"echo \$_\n\"}" | timeout 120 "$WR" add -i echo --deployment development 2>&1 | tail -1
  echo "waiting for CLI to reach complete=$n and the slow web feed to converge..."
  while kill -0 "$wp" 2>/dev/null; do
    local c; c=$(timeout 20 "$WR" status --deployment development -i echo -o counts 2>/dev/null | grep -oE 'complete: [0-9]+')
    echo "  CLI echo $c ; web feed still draining"; sleep 20
  done
  echo "=== web feed (must show echo complete=$n; pre-fix it froze short) ==="
  cat "$WRDEV_ROOT/webprobe.out"
  cmd_stop >/dev/null 2>&1
}

cmd_prod_start() {  # prod-start [lsf|local] - isolated PROD-mode manager (preserves DB across restart)
  need_bin; ensure_config
  local sched="${1:-local}"
  echo "starting ISOLATED prod-mode manager (-s $sched) on :$PROD_PORT / web :$PROD_WEB"
  echo "NOTE: prod-mode LSF runners are wrp_* - clean up by exact jobid, never 'wrp_*' pattern"
  osunset ; timeout 90 "$WR" manager start --deployment production -s "$sched" 2>&1 \
    | grep -aE 'started on|token=' | head -2
  echo "pid $(mgr_pid "$PROD_RUN")"
}

cmd_prod_stop() {  # stop the isolated prod-mode manager only (verified pid); does NOT bkill wrp_*
  safe_kill "$(mgr_pid "$PROD_RUN")"
  echo "if you launched LSF runners, bkill them by the exact jobid you recorded (never 'wrp_*')."
}

cmd_crash_recovery() {  # end-to-end Idea-1 crash-recovery on an isolated prod-mode LSF manager
  need_bin; ensure_config
  safe_kill "$(mgr_pid "$PROD_RUN")"; rm -rf "$PROD_RUN" 2>/dev/null; rm -f "$WRDEV_ROOT/cr_count"
  cmd_prod_start lsf
  printf '{"cmd":"bash -c \\"echo ran >> %s/cr_count; sleep 30\\"","queue":"%s","memory":"500M"}\n' \
    "$WRDEV_ROOT" "$QUEUE" > "$WRDEV_ROOT/cr.json"
  osunset; timeout 40 "$WR" add -f "$WRDEV_ROOT/cr.json" --rep_grp rgCR --retries 0 --deployment production 2>&1 | tail -1
  local r=0; for _ in $(seq 1 30); do sleep 5; r=$(timeout 20 "$WR" status --deployment production -i rgCR -o counts 2>/dev/null | grep -oE 'running: [0-9]+' | grep -oE '[0-9]+'); [ "${r:-0}" -ge 1 ] && break; done
  local jid; jid=$(timeout 40 bjobs -o 'jobid job_name stat' -noheader 2>/dev/null | awk '$2 ~ /^wrp_/ && $3=="RUN"{print $1; exit}')
  echo "job running; marker=$(wc -l < "$WRDEV_ROOT/cr_count" 2>/dev/null || echo 0) my wrp_ jobid=$jid"
  echo "--- killing prod manager mid-run (LSF runner $jid survives), then restarting (DB preserved) ---"
  safe_kill "$(mgr_pid "$PROD_RUN")"; sleep 12
  cmd_prod_start lsf
  local ok=0
  for _ in $(seq 1 20); do sleep 8; local c m; c=$(timeout 20 "$WR" status --deployment production -i rgCR -o counts 2>/dev/null | tr '\n' ' '); m=$(wc -l < "$WRDEV_ROOT/cr_count" 2>/dev/null || echo 0); echo "  rgCR[$c] marker=$m"; echo "$c" | grep -qE 'complete: 1' && { ok=1; break; }; done
  [ "$ok" = 1 ] && [ "$(wc -l < "$WRDEV_ROOT/cr_count" 2>/dev/null)" = 1 ] \
    && echo "PASS: re-sent archive accepted (complete=1), command ran exactly once" \
    || echo "FAIL: check rgCR / marker above"
  [ -n "$jid" ] && timeout 30 bkill "$jid" >/dev/null 2>&1  # exact jobid only, never 'wrp_*'
  safe_kill "$(mgr_pid "$PROD_RUN")"
}

cmd_dump() {  # dump - start dev manager FOREGROUND, so you can SIGQUIT it for a goroutine dump
  need_bin; ensure_config
  cmd_stop >/dev/null 2>&1 || true
  local out="$WRDEV_ROOT/fg.out"
  echo "starting dev manager foreground (-f, pprof enabled), output -> $out"
  nohup "$WR" manager start --deployment development -s "${1:-lsf}" -f > "$out" 2>&1 &
  sleep 8
  local pid; pid=$(mgr_pid "$DEV_RUN")
  cat <<EOF
foreground manager pid: $pid
Reproduce the stall (e.g. '$0 churn'), then dump goroutines with:
    kill -3 $pid          # SIGQUIT -> full stacks appended to $out
    grep -aE '^goroutine' $out | sed -E 's/.*\[([^]]+)\].*/\1/' | sort | uniq -c | sort -rn
Look for many 'sync.RWMutex.RLock' waiters + a 'Lock' waiter (lock cycle), or
goroutines in os/exec (bsub/bjobs) holding a lock.
EOF
}

cmd_clean() {  # clean - stop everything of ours + bkill wrd_; verify production untouched
  safe_kill "$(mgr_pid "$DEV_RUN")"
  safe_kill "$(mgr_pid "$PROD_RUN")"
  bkill_dev
  echo "our managers: $(pgrep -af "$WR" | grep -c 'manager start' || echo 0)"
  echo "active wrd_ jobs: $(timeout 40 bjobs -o stat -noheader 2>/dev/null | grep -cE 'RUN|PEND' || echo 0)"
  echo "(any real --deployment production managers are left untouched)"
}

cmd_status() {
  echo "WRDEV_ROOT=$WRDEV_ROOT  binary=$([ -x "$WR" ] && echo present || echo MISSING)"
  echo "dev  mgr pid: $(mgr_pid "$DEV_RUN")  (ports $DEV_PORT/$DEV_WEB)"
  echo "prod mgr pid: $(mgr_pid "$PROD_RUN") (ports $PROD_PORT/$PROD_WEB)"
  echo "our wr manager processes:"; pgrep -af "$WR" | grep 'manager start' || echo "  (none)"
}

usage() {
  cat <<EOF
wrdev.sh - isolated wr reliability testing (see ../DEVELOPERS.md). NOT part of the build.

  build                 build wr + wsprobe from the current checkout into \$WRDEV_ROOT
  start [lsf|local]     start the isolated dev manager (default lsf)
  stop                  kill the (verified) dev manager + bkill wrd_ jobs
  churn [N]             submit N true/false jobs (default 40000) then monitor
  monitor [halfN]       watch drain / churn counts / control-RPC latency
  probe [secs] [slowms] read the dev web /status_ws feed via wsprobe
  web-burst [N]         reproduce the status-bar freeze-under-burst (local + slow reader)
  prod-start [lsf|local] start an isolated PROD-mode manager (DB survives restart)
  prod-stop             stop the isolated prod-mode manager (verified pid)
  crash-recovery        end-to-end Idea-1 crash-recovery test (isolated prod-mode LSF)
  dump [lsf|local]      run dev manager foreground for a SIGQUIT goroutine dump
  clean                 stop all our managers + bkill wrd_ (production untouched)
  status                show what is running

Env: WRDEV_ROOT (=$WRDEV_ROOT) DEV_PORT/DEV_WEB PROD_PORT/PROD_WEB QUEUE MEM_GROUPS
Safety: only kills processes running \$WRDEV_ROOT/wr; only pattern-bkills wrd_ (dev).
Never touches --deployment production managers or wrp_* jobs.
EOF
}

case "${1:-help}" in
  build) cmd_build ;;
  start) cmd_start "${2:-lsf}" ;;
  stop) cmd_stop ;;
  churn) cmd_churn "${2:-40000}" ;;
  monitor) cmd_monitor "${2:-20000}" ;;
  probe) cmd_probe "${2:-3}" "${3:-0}" ;;
  web-burst) cmd_web_burst "${2:-10000}" ;;
  prod-start) cmd_prod_start "${2:-local}" ;;
  prod-stop) cmd_prod_stop ;;
  crash-recovery) cmd_crash_recovery ;;
  dump) cmd_dump "${2:-lsf}" ;;
  clean) cmd_clean ;;
  status) cmd_status ;;
  help|-h|--help) usage ;;
  *) usage; exit 1 ;;
esac
