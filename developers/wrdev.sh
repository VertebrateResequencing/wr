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

# ensure_dev_manager guarantees OUR isolated dev manager is up before jobs are
# added, so churn can never silently loop at terminal=0 just because nothing was
# running. If a dev manager we own is already alive it is REUSED (never
# restarted/wiped); otherwise a fresh one is started with cmd_start lsf.
ensure_dev_manager() {
  local pid; pid=$(mgr_pid "$DEV_RUN")
  if [ -n "$pid" ] && ps -p "$pid" >/dev/null 2>&1 && is_ours "$pid"; then
    echo "reusing running dev manager pid $pid"
    return 0
  fi
  echo "no dev manager running; starting one"
  cmd_start lsf
  pid=$(mgr_pid "$DEV_RUN")
  { [ -n "$pid" ] && ps -p "$pid" >/dev/null 2>&1; } || die "could not start dev manager"
}

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

cmd_start() {  # start [lsf|local]   (set WRDEV_DEBUG=1 to run the manager with --debug, like production)
  need_bin; ensure_config
  local sched="${1:-lsf}"
  local dbg=""; [ "${WRDEV_DEBUG:-0}" = "1" ] && dbg="--debug"
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s $sched${dbg:+ $dbg}) on :$DEV_PORT / web :$DEV_WEB"
  osunset ; timeout 90 "$WR" manager start --deployment development -s "$sched" $dbg 2>&1 \
    | grep -aE 'started on|token=' | head -2
  echo "pid $(mgr_pid "$DEV_RUN")   token $(cat "$DEV_RUN/client.token" 2>/dev/null)"
}

cmd_stop() {  # wr manager stop hangs under load; kill our verified pid + bkill wrd_
  safe_kill "$(mgr_pid "$DEV_RUN")"
  bkill_dev
}

cmd_churn() {  # churn [N]  (default 40000; ~half true half false, across memory groups)
  need_bin
  ensure_dev_manager
  local n="${1:-40000}"; local half=$(( n / 2 ))
  echo "generating $n jobs ($half true + $half false) across $MEM_GROUPS memory groups"
  perl -e "for my \$i (1..$half){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"true #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/true.json"
  perl -e "for my \$i (1..$half){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"false #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/false.json"
  churn_add "$WRDEV_ROOT/true.json"  rgtrue
  churn_add "$WRDEV_ROOT/false.json" rgfalse
  cmd_monitor "$half"
}

# churn_add adds one job file, failing FAST and LOUD if the manager is unreachable
# or nothing was added, so churn never silently drops into cmd_monitor looping at
# terminal=0 (which looks exactly like a scheduling stall but is really "no manager
# running"). Echoes wr's own output, then inspects its exit code and message.
churn_add() {
  local file="$1" rg="$2" out rc
  out=$(osunset ; timeout 180 "$WR" add -f "$file" --rep_grp "$rg" --retries 0 --deployment development 2>&1); rc=$?
  echo "$out" | tail -2
  if [ "$rc" -ne 0 ] || echo "$out" | grep -qiE 'could not reach the server|Connect\(\)|connection refused'; then
    die "churn aborted - could not add jobs (is the dev manager running? Connect error above)"
  fi
  if ! echo "$out" | grep -qE 'Added [1-9][0-9]* new commands'; then
    die "churn aborted - could not add jobs (is the dev manager running? Connect error above)"
  fi
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

cmd_limit_drain() {  # limit-drain [N] [limit] [runsec] - LSF-scale FAITHFUL repro of the production stall
  # Unlike churn (no limit group, drains freely), this recreates the production
  # shape: N >> limit SHORT-but-touch-needing jobs in ONE shared limit group, so
  # only `limit` run at once behind a huge ready backlog, across MEM_GROUPS memory
  # groups (=> many sibling scheduler groups sharing the one limit, as in prod).
  # Under saturation the touch-during-run TTR miss -> false-lost -> confirm-dead/
  # release -> archive-reject -> rerun loop makes `complete` stall while the backlog
  # stays put and confirmed_dead climbs. Set WRDEV_DEBUG=1 (manager --debug, like
  # production) to test the logging confound. It PASSES (drains) only once the stall
  # is actually fixed - so it is the gate for any fix.
  need_bin
  ensure_dev_manager
  local n="${1:-60000}" limit="${2:-2000}" runsec="${3:-30}" padkb="${4:-0}"
  # padkb pads each job's cmd (after a '#', so it stays a no-op comment) up to ~padkb KB,
  # so that WITH --debug the per-reserve "reserved job" log line matches production's ~25KB
  # portal_builder cmd lines - the debug-logging I/O is the suspected stall trigger, and short
  # cmds do NOT replicate it. The pad is passed via env (not the perl arg list) to stay safe.
  local pad=""
  [ "$padkb" -gt 0 ] && pad=$(head -c $((padkb*1024)) /dev/zero | tr '\0' x)
  echo "reliable4 STALL repro: $n jobs (sleep $runsec, cmd pad ${padkb}KB) in ONE limit group reprolimit:$limit across $MEM_GROUPS mem groups"
  echo "manager debug=${WRDEV_DEBUG:-0} (WRDEV_DEBUG=1 matches production's --debug; change requires a fresh 'start')"
  PAD="$pad" perl -e "for my \$i (1..$n){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"sleep $runsec #'.\$i.' '.\$ENV{PAD}.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/limit.json"
  local out rc
  # retries 30 so a false-lost job RE-RUNS (as in prod) instead of burying on the
  # first lost-release; this makes the stall show as FROZEN complete, not drain-to-buried.
  out=$(osunset ; timeout 300 "$WR" add -f "$WRDEV_ROOT/limit.json" --rep_grp rglimit \
    --limit_grps "reprolimit:$limit" --retries 30 --deployment development 2>&1); rc=$?
  echo "$out" | tail -2
  if [ "$rc" -ne 0 ] || echo "$out" | grep -qiE 'could not reach the server|Connect\(\)'; then
    die "limit-drain aborted - could not add jobs (is the dev manager running?)"
  fi
  echo "$out" | grep -qE 'Added [1-9][0-9]* new commands' || die "limit-drain aborted - 0 jobs added"
  cmd_limit_monitor "$n"
}

cmd_limit_monitor() {  # drain/stall monitor for the single limit-group workload
  need_bin
  local n="${1:-60000}"; local t0; t0=$(date +%s); local prevc=-1 stall=0
  num(){ echo "$1" | grep -oE "$2: [0-9]+" | grep -oE '[0-9]+' | head -1; }
  for _ in $(seq 1 80); do
    local st cc cb cr cl crun s e
    st=$(osunset ; timeout 30 "$WR" status --deployment development -i rglimit -o counts 2>/dev/null)
    cc=$(num "$st" complete); cb=$(num "$st" buried); cr=$(num "$st" running); cl=$(echo "$st" | grep -oE 'lost[^0-9]*[0-9]+' | grep -oE '[0-9]+' | head -1)
    cc=${cc:-0}; cb=${cb:-0}; cr=${cr:-0}; cl=${cl:-0}
    crun=$(timeout 20 bjobs -o stat -noheader 2>/dev/null | grep -c RUN)
    local bj kd ar
    bj=$(grep -ac 'bad job' "$DEV_RUN/log" 2>/dev/null); bj=${bj:-0}
    kd=$(grep -ac 'killed a job after confirming it was dead' "$DEV_RUN/log" 2>/dev/null); kd=${kd:-0}
    ar=$(grep -ac 'jarchive.*bad job\|jarchive.*must Reserve' "$DEV_RUN/log" 2>/dev/null); ar=${ar:-0}
    s=$(date +%s%3N); timeout 65 "$WR" status --deployment development -i rglimit -o counts >/dev/null 2>&1; e=$(date +%s%3N)
    echo "t+$(( $(date +%s)-t0 ))s complete=$cc/$n running=$cr lost=$cl LSF_RUN=$crun buried=$cb badjob=$bj confirmed_dead=$kd archive_reject=$ar status_rpc=$((e-s))ms"
    if [ $((cc+cb)) -ge "$n" ]; then echo "FULLY DRAINED (complete=$cc buried=$cb)"; break; fi
    if [ "$cc" -eq "$prevc" ]; then stall=$((stall+1)); else stall=0; fi
    prevc=$cc
    [ "$stall" -ge 6 ] && { echo "STALL REPRODUCED: complete stuck at $cc/~$n for ~4.5min while work remains (running=$cr lost=$cl badjob=$bj confirmed_dead=$kd archive_reject=$ar). goroutine dump: $0 dump"; break; }
    sleep 45
  done
}

cmd_backup_stall_check() {  # backup-stall-check [dbGB] [N] [limit] [runsec] - reliable4 DB-backup freeze/churn
  # FAITHFUL, PORTABLE repro of the CONFIRMED production stall root: on a LARGE DB the
  # periodic backup (db.backupToBackupFile does a full-file CopyFile every 30s) freezes
  # the manager - GBs of I/O + the bolt read-tx mmaplock held for the whole copy - so
  # archive/touch RPCs time out past the TTR -> jobs falsely lost -> confirmed-dead ->
  # rerun churn. It needs PROD mode (dev never backs up - which is exactly why the
  # dev-manager reproducers all drained cleanly) + a genuinely big DB. This inflates a
  # FRESH dbGB DB from scratch (no pre-existing/production DB needed - portable to any
  # machine), starts an isolated PROD-mode manager on it, runs N sleep jobs (which
  # CANNOT fail, so any delayed/lost/badjob is pure backup-induced churn), and watches
  # for churn + manager freezes coinciding with backups. Healthy code: drains with ~0
  # delayed and no freezes. Pre-fix: churns. Set WRDEV_ROOT to a disk with room for ~2x
  # dbGB (the DB + its backup); that env var is where all DB files are stored.
  need_bin; ensure_config
  local dbgb="${1:-8}" n="${2:-8000}" limit="${3:-2000}" runsec="${4:-30}" records="${5:-2100000}" flgb="${6:-2}"
  local pr="$PROD_RUN" plog="$PROD_RUN/log"
  cmd_prod_stop >/dev/null 2>&1; sleep 2
  # start from a clean slate: fresh inflated DB AND fresh log, so the churn metrics
  # (badjob delta, delayed, freezes) reflect only this run. dbGB must be big enough
  # that each backup CopyFile takes long enough to stall archives past the TTR - the
  # threshold depends on your storage speed, so raise dbGB (or lower it once fixed).
  mkdir -p "$pr"; rm -f "$pr/db" "$pr/db_bk"* "$pr/log" 2>/dev/null
  # A record-dense DB is what reproduces: ~$records real complete-job records + a
  # large persisted freelist (~${flgb}GB) so every archive commit rewrites a big
  # freelist AND the full-file backup copies GBs. Padding-only DBs (few big values,
  # ~empty freelist) do NOT reproduce even when larger - see the generator header.
  # Set WRDEV_PRISTINE_DB to a pre-generated big DB to COPY it (fast) instead of
  # regenerating (2.1M records takes ~20min); that pristine copy is never mutated.
  if [ -n "${WRDEV_PRISTINE_DB:-}" ] && [ -f "${WRDEV_PRISTINE_DB}" ]; then
    echo "copying pristine DB ${WRDEV_PRISTINE_DB} -> $pr/db"
    cp -f "${WRDEV_PRISTINE_DB}" "$pr/db" || die "could not copy pristine DB"
  else
    echo "inflating a fresh record-dense DB at $pr/db ($records records, ~${dbgb}GB, ~${flgb}GB freelist)"
    WR_INFLATE_DB="$pr/db" WR_INFLATE_RECORDS="$records" WR_INFLATE_GB="$dbgb" WR_INFLATE_FREELIST_GB="$flgb" \
      timeout "${WRDEV_INFLATE_TIMEOUT:-2400}" \
      go -C "$REPO" test -tags reliability_repro ./jobqueue/ -run TestReliable4InflateDB -count=1 \
        -timeout "${WRDEV_INFLATE_TIMEOUT:-2400}s" >/dev/null 2>&1 \
      || die "DB inflation failed (is $WRDEV_ROOT on a disk with room for ~2x ${dbgb}GB?)"
  fi
  echo "db size: $(ls -la "$pr/db" 2>/dev/null | awk '{print $5}') bytes"
  echo "starting isolated PROD-mode manager (backups ON) on the big DB"
  cmd_prod_start lsf 2>&1 | tail -1; sleep 5
  echo "adding $n sleep-$runsec jobs (limit $limit); they CANNOT fail, so delayed/lost/badjob == churn"
  perl -e "for my \$i (1..$n){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"sleep $runsec #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/bkjobs.json"
  osunset; timeout 180 "$WR" add -f "$WRDEV_ROOT/bkjobs.json" --rep_grp rgbk --limit_grps "bklimit:$limit" --retries 30 --deployment production 2>&1 | tail -1
  local t0; t0=$(date +%s); local maxdelayed=0 basebadjob=-1 maxbadjob=0 maxrpc=0
  num(){ echo "$1" | grep -oE "$2: [0-9]+" | grep -oE '[0-9]+' | head -1; }
  for _ in $(seq 1 40); do
    local st cc cd cl cr run bj kd s e rpc
    st=$(osunset; timeout 30 "$WR" status --deployment production -i rgbk -o counts 2>/dev/null)
    cc=$(num "$st" complete); cd=$(num "$st" delayed); cr=$(num "$st" running); cl=$(echo "$st"|grep -oE 'lost[^0-9]*[0-9]+'|grep -oE '[0-9]+'|head -1)
    cc=${cc:-0}; cd=${cd:-0}; cr=${cr:-0}; cl=${cl:-0}
    run=$(timeout 20 bjobs -o stat -noheader 2>/dev/null | grep -c RUN)
    bj=$(grep -ac 'bad job' "$plog" 2>/dev/null); bj=${bj:-0}; kd=$(grep -ac 'confirming it was dead' "$plog" 2>/dev/null); kd=${kd:-0}
    [ "$basebadjob" -lt 0 ] && basebadjob=$bj
    s=$(date +%s%3N); timeout 65 "$WR" status --deployment production -i rgbk -o counts >/dev/null 2>&1; e=$(date +%s%3N); rpc=$((e-s))
    [ "$cd" -gt "$maxdelayed" ] && maxdelayed=$cd
    [ "$bj" -gt "$maxbadjob" ] && maxbadjob=$bj
    [ "$rpc" -gt "$maxrpc" ] && maxrpc=$rpc
    echo "t+$(( $(date +%s)-t0 ))s complete=$cc/$n running=$cr delayed=$cd lost=$cl LSF_RUN=$run confirmed_dead=$kd badjob=$bj status_rpc=${rpc}ms"
    [ "$cc" -ge "$n" ] && break
    sleep 30
  done
  echo "## manager log freezes (gaps) during the run:"
  grep -oaP 'T\d\d:\d\d:\d\d' "$plog" 2>/dev/null | uniq | awk -F: '{t=$1*3600+$2*60+$3} NR==1{p=t} {if(t-p>5)print "  GAP "(t-p)"s ending "$0; p=t}' | tail -6
  local badjobdelta=$(( maxbadjob - basebadjob ))
  echo "## VERDICT: maxDelayed=$maxdelayed badjobDelta=$badjobdelta maxStatusRPC=${maxrpc}ms"
  if [ "$maxdelayed" -gt 50 ] || [ "$badjobdelta" -gt 200 ] || [ "$maxrpc" -gt 1500 ]; then
    echo "BACKUP-STALL REPRODUCED: sleep jobs churned and/or the manager froze during backups of the ${dbgb}GB DB (FAILS until the backup fix lands)"
  else
    echo "NO STALL: jobs drained cleanly despite backups (the fix works)"
  fi
  echo "## CLEANUP"; cmd_prod_stop 2>&1 | tail -1
  bjobs -o 'jobid job_name' -noheader 2>/dev/null | awk '$2 ~ /^wrp_/{print $1}' | sort -u | while read -r j; do timeout 30 bkill "$j" >/dev/null 2>&1; done
  rm -f "$WRDEV_ROOT/bkjobs.json" "$pr/db" "$pr/db_bk"* 2>/dev/null
}

cmd_backup_stall_fast() {  # backup-stall-fast [archivers] [seconds] [pauseMs] - FAST in-process repro/iterate
  # Deterministic, NO LSF and NO manager: opens a pre-generated record-dense big DB
  # via the REAL initDB (production, backups ON) and hammers db.archiveJob from many
  # goroutines - exactly what the server's archive RPC handler does. It measures each
  # archive's wall-clock latency; during each periodic full-file backup the archive
  # commits stall, and any archive over the TTR would be falsely lost -> churn. This is
  # the FAST iteration harness for backup-stall fixes (seconds to run, no cluster).
  # A/B a candidate fix via the WR_EXP_* env knobs (see db.go). Requires WRDEV_PRISTINE_DB
  # (a big DB from the generator); it is COPIED to a scratch path each run (the run
  # mutates it) so the pristine copy is reused across iterations.
  need_repo
  local archivers="${1:-50}" seconds="${2:-180}" pausems="${3:-100}"
  local pristine="${WRDEV_PRISTINE_DB:-}"
  { [ -n "$pristine" ] && [ -f "$pristine" ]; } \
    || die "set WRDEV_PRISTINE_DB to a generated big DB (see backup-stall-check / TestReliable4InflateDB)"
  local work="$WRDEV_ROOT/stall_work_db"
  mkdir -p "$WRDEV_ROOT"
  echo "copying pristine DB $pristine -> $work (mutated by the run)"
  cp -f "$pristine" "$work" || die "could not copy pristine DB"
  rm -f "${work}_bk" "${work}_bk.tmp" 2>/dev/null
  echo "in-process backup-stall: archivers=$archivers seconds=$seconds pauseMs=$pausems"
  echo "  EXP knobs: NOFREELISTSYNC=${WR_EXP_NOFREELISTSYNC:-} BACKUP_K=${WR_EXP_BACKUP_K:-} THROTTLE_MBPS=${WR_EXP_BACKUP_THROTTLE_MBPS:-} PREGROW_MB=${WR_EXP_PREGROW_MB:-} ARCHIVEDB=${WR_EXP_ARCHIVE_DB:-}"
  osunset
  WR_STALL_DB="$work" WR_STALL_ARCHIVERS="$archivers" WR_STALL_SECONDS="$seconds" WR_STALL_PAUSE_MS="$pausems" \
    timeout $((seconds + 360)) go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4BackupStall -count=1 -v -timeout $((seconds + 300))s 2>&1 \
    | grep -aE 'STALL|INFLATE|DUMP|relevant|goroutine|PASS|FAIL|panic|^ok |^---' | grep -avE 'no test files'
  rm -f "$work" "${work}_bk" "${work}_bk.tmp" 2>/dev/null
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

cmd_flicker_check() {  # flicker-check [handler.js] - reproduce/verify the web status-bar flicker/overcount family
  need_repo
  command -v node >/dev/null 2>&1 || die "node is required for flicker-check"
  local fixdir="$REPO/jobqueue/testdata/status-count-reconcile"
  local handler="${1:-$REPO/jobqueue/static/js/wr/websocket-handler.js}"
  local rc=0

  # 1) Deterministic count-level reproducer (no browser). This is the primary,
  #    non-flaky gate: it drives the REAL delta-application logic with
  #    out-of-order, seed-race and rerun-cycle streams and asserts the
  #    reconstructed counts stay coherent and converge exactly. FAILS on the
  #    pre-fix handler (overcount + permanent seed-race divergence), PASSES on a
  #    correct one. Pass an alternate handler path to A/B a candidate fix.
  echo "=== flicker count-level reproducer (deterministic) ==="
  timeout 120 node "$fixdir/reconcile-harness.mjs" "$handler" --verbose || rc=1

  # 2) Browser fixtures for the rendered bar. Uses the same persisted Playwright
  #    package/browser cache as `make browser-test`; skipped if absent.
  local pwroot="${WEBUI_TEST_PLAYWRIGHT_ROOT:-$HOME/.cache/wr-webui-playwright}"
  local pwpkg="${PLAYWRIGHT_PACKAGE_DIR:-$pwroot/node_modules/playwright}"
  local pwbrowsers="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"
  local art="$REPO/.tmp/agent/webui-test"; mkdir -p "$art"
  if [ -d "$pwpkg" ]; then
    echo "=== flicker browser fixtures (Playwright) ==="
    for fx in repgroup-bar-flicker status-count-reconcile; do
      echo "--- $fx ---"
      # artifact basename matches the make browser-test naming (the
      # status-count-reconcile fixture writes status-webui-count-reconcile.*)
      local base="status-webui-${fx#status-}"
      PLAYWRIGHT_PACKAGE_DIR="$pwpkg" PLAYWRIGHT_BROWSERS_PATH="$pwbrowsers" \
        timeout 180 node "$REPO/jobqueue/testdata/$fx/screenshot.mjs" \
          "$art/$base.png" "$art/$base-trace.json" \
        && echo "  $fx PASS" || { echo "  $fx FAIL"; rc=1; }
    done
  else
    echo "(Playwright package not found at $pwpkg; skipping browser fixtures."
    echo " Run 'make browser-test' once to install it, or set PLAYWRIGHT_PACKAGE_DIR.)"
  fi

  [ "$rc" -eq 0 ] && echo "flicker-check: PASS" || echo "flicker-check: FAIL (flicker/overcount/divergence present)"
  return "$rc"
}

cmd_overprovision_check() {  # overprovision-check [limit] [siblings] [ready] - runner over-provisioning invariant
  # Deterministic, in-process (no manager, no LSF): reproduces the reliable3 LSF-scale
  # bug where sibling scheduler groups sharing ONE limit group each get the limit
  # group's full remaining capacity, so the summed runner request is up to
  # siblings x limit (production saw 13,271 for a 2000 limit). Asserts the summed
  # request stays <= the limit. FAILS on the pre-fix per-scheduler-group accounting,
  # PASSES with the shared per-limit-group budget. A required gate for the fix.
  need_repo
  local limit="${1:-2000}" siblings="${2:-50}" ready="${3:-5000}"
  echo "over-provisioning invariant (deterministic, prod-scale): summed runner request across"
  echo "sibling scheduler groups sharing one limit group must be <= the limit."
  echo "scale: limit=$limit siblings=$siblings readyPerGroup=$ready (pre-fix code requests ~limit*siblings)"
  osunset
  local out rc
  out=$(WR_OP_LIMIT="$limit" WR_OP_SIBLINGS="$siblings" WR_OP_READY="$ready" \
    timeout 180 go -C "$REPO" test ./jobqueue/ -run TestReliable3LimitGroupOverProvision -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'over-provision check|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
}

cmd_overcount_check() {  # overcount-check [limit] [initialRunning] [windowReserves] - reliable3 2b over-count
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of
  # make test) for reliable3 ISSUE 2b: a single scheduler group's final scheduling
  # count exceeds its limit group's limit because countJobInGroup caps the ready count
  # against an EARLY capacity read while accountForRunningJobs later adds ALL running
  # jobs on top with no limit check. Reserves landing in that non-atomic window inflate
  # the count (production saw 3313 for a 2000 limit). PASSES on current (buggy) code,
  # showing finalCount = limit + windowReserves > limit.
  need_repo
  local limit="${1:-2000}" initial="${2:-300}" window="${3:-1500}"
  echo "reliable3 2b over-count (deterministic, in-process): a group's count exceeds its limit"
  echo "when reserves land between the early capacity read and the later running snapshot."
  echo "scale: limit=$limit initialRunning=$initial windowReserves=$window (finalCount should be limit+window)"
  osunset
  local out rc
  out=$(WR_OP_LIMIT="$limit" WR_RC_INITIAL="$initial" WR_RC_WINDOW="$window" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable3OverCountRunningSnapshot -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'OVERCOUNT-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
}

cmd_limit_stall_check() {  # limit-stall-check [limit] [ready] - reliable3 §1 silent-confirm stall
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of
  # make test) for reliable3 ISSUE 1: (1) scheduler.ProcessNotRunningOnHost and
  # lsf.initialize fail SILENTLY (no log) when death-confirmation cannot succeed, and
  # (2) the CONSEQUENCE - a limit group full of phantom slots (unconfirmable lost jobs)
  # skips every new ready job, so scheduling stalls. PASSES on current code. NOTE: a
  # "loud only" fix logs the failure but does NOT clear the phantom slots, so the stall
  # part still reproduces after it - that is the headline finding.
  need_repo
  local limit="${1:-2000}" ready="${2:-5000}"
  echo "reliable3 §1 silent-confirmation + limit-slot stall (deterministic, in-process)."
  echo "scale: limit=$limit phantomSlots=$limit newReady=$ready (all new ready jobs should be skipped)."
  echo "the SilentConfirm part shows ProcessNotRunningOnHost/lsf.initialize log NOTHING on failure."
  osunset
  local out rc
  out=$(WR_OP_LIMIT="$limit" WR_STALL_READY="$ready" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run 'TestReliable3LimitSlotStall|TestReliable3SilentConfirmFailure' -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'LIMIT-STALL-REPRO|SILENT-CONFIRM-REPRO|KEY-SWALLOW-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
}

cmd_priority_fairness_check() {  # priority-fairness-check [limit] [readyExtra] - reliable3 2a fairness
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of
  # make test) for reliable3 ISSUE 2a's refinement: the shared per-limit-group budget is
  # allocated FIRST-COME across sibling scheduler groups, so a low-priority sibling
  # scanned first consumes the whole budget and starves a higher-priority sibling.
  # PASSES on current code, showing the high-priority sibling gets count=0.
  need_repo
  local limit="${1:-2000}" extra="${2:-500}"
  echo "reliable3 2a priority fairness (deterministic, in-process): a low-priority sibling"
  echo "scanned first starves a higher-priority sibling of the shared limit-group budget."
  echo "scale: limit=$limit readyPerGroup=$((limit + extra)) (high-priority sibling should get 0)"
  osunset
  local out rc
  out=$(WR_OP_LIMIT="$limit" WR_PF_READY="$extra" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable3PriorityFairnessStarvation -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'PRIORITY-FAIRNESS-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
}

cmd_backlog_rescan_check() {  # backlog-rescan-check [limit] [backlog] - reliable4 #1 rac backlog rescan
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of
  # make test) for reliable4 ISSUE #1: the ready-added callback re-scans the WHOLE ready
  # backlog every cycle - buildSchedulerGroups runs prepareReadyJob for every ready job,
  # including the ones whose limit group is saturated and so cannot be scheduled. It adds
  # `backlog` ready jobs behind ONE limit group (limit L) on a real in-process server, then
  # drives one buildSchedulerGroups cycle and reads the inert racScanWork counter. Unlike
  # the reliable3 reproducers, this asserts the FIXED invariant, so it FAILS on current
  # (pre-fix) code: scanWork == backlog, far above the L + margin bound (want O(schedulable)).
  need_repo
  local limit="${1:-2000}" backlog="${2:-50000}"
  echo "reliable4 #1 rac backlog rescan (deterministic, in-process): a rac cycle's per-job"
  echo "scheduling work should be bounded by the schedulable count (~limit), not the ready backlog."
  echo "scale: limit=$limit backlog=$backlog (pre-fix scanWork == backlog; want <= limit+100)"
  osunset
  local out rc
  out=$(WR_OP_LIMIT="$limit" WR_BR_BACKLOG="$backlog" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable4BacklogRescan -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'BACKLOG-RESCAN-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
}

cmd_runner_started_timeout_check() {  # runner-started-timeout-check - reliable4 #3 Started() kills healthy cmd
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of make
  # test) for reliable4 ISSUE #3: after exec, the runner reports its PID via c.Started();
  # if that outbound RPC times out (server saturation), Execute KILLS the still-healthy
  # command instead of tolerating-and-retrying like the touch loop does. It runs a real
  # command (`sleep 1; echo ran > marker`) via an in-process capture client whose socket
  # fails the FIRST Started() RPC once with a "receive time out". Asserts the FIXED
  # invariant (the marker is written), so it FAILS on current code: the command is killed
  # mid-sleep, execErr = "started running, but I killed it due to a jobqueue server error".
  need_repo
  echo "reliable4 #3 Started() timeout kills a healthy command (deterministic, in-process):"
  echo "a transient post-exec Started() RPC failure must NOT destroy a healthy running command."
  echo "expect (pre-fix) markerWritten=false and 'I killed it due to a jobqueue server error'."
  osunset
  local out rc
  out=$(timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable4StartedTimeoutKillsHealthyCommand -count=1 -v 2>&1); rc=$?
  printf '%s\n' "$out" | grep -aE 'STARTED-TIMEOUT-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'
  return "$rc"
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
  churn [N]             ensure dev manager up, submit N true/false jobs (default 40000) then monitor
  limit-drain [N] [limit] [runsec] [padKB]
                        FAITHFUL LSF-scale stall repro: N>>limit jobs in ONE limit group
                        (defaults 60000 2000 30 0); must fully drain once the stall is fixed.
                        Set WRDEV_DEBUG=1 (manager --debug, like prod) + padKB~25 so per-reserve
                        log lines match production's ~25KB cmds (the suspected stall trigger).
  backup-stall-check [dbGB] [N] [limit] [runsec] [records] [freelistGB]
                        FAITHFUL LSF repro: inflates a fresh RECORD-DENSE DB from scratch
                        (records real complete-jobs + a large persisted freelist; portable),
                        runs an isolated PROD-mode manager (backups ON) + N sleep jobs, showing
                        the periodic full-file DB backup freeze the manager -> archive timeouts ->
                        churn (defaults 8 8000 2000 30 2100000 2). Fails until the backup fix lands.
                        WRDEV_ROOT holds the DB + backup (needs ~2x dbGB free). Set WRDEV_PRISTINE_DB
                        to COPY a pre-generated DB instead of regenerating. A/B a fix with WR_EXP_*.
  backup-stall-fast [archivers] [seconds] [pauseMs]
                        FAST in-process repro (no LSF/manager): opens WRDEV_PRISTINE_DB via the real
                        initDB (backups ON) and hammers db.archiveJob, timing each; archives over the
                        TTR would churn. Seconds to run - the iteration harness for fixes (WR_EXP_*).
  monitor [halfN]       watch drain / churn counts / control-RPC latency
  probe [secs] [slowms] read the dev web /status_ws feed via wsprobe
  web-burst [N]         reproduce the status-bar freeze-under-burst (local + slow reader)
  flicker-check [h.js]  reproduce/verify the web status-bar flicker/overcount family
                        (deterministic node harness + browser fixtures; no manager needed)
  overprovision-check [limit] [siblings] [ready]
                        deterministic prod-scale check: summed runners requested per limit
                        group stay <= the limit (fails on pre-fix per-group accounting; no manager)
  overcount-check [limit] [initialRunning] [windowReserves]
                        reliable3 2b reproducer: a group's count exceeds its limit when reserves
                        land between the early capacity read and the running snapshot (no manager)
  limit-stall-check [limit] [ready]
                        reliable3 §1 reproducer: silent death-confirmation failure + phantom-slot
                        limit-group stall (all new ready jobs skipped); loud-only won't clear it
  priority-fairness-check [limit] [readyExtra]
                        reliable3 2a reproducer: first-come budget allocation starves a higher-
                        priority sibling scanned after a low-priority one (no manager)
  backlog-rescan-check [limit] [backlog]
                        reliable4 #1 reproducer: a rac cycle scans the whole ready backlog
                        (racScanWork == backlog); fails until the scan is bounded to ~limit
  runner-started-timeout-check
                        reliable4 #3 reproducer: a transient post-exec Started() RPC timeout
                        kills a healthy running command; fails until Started() tolerates it
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
  flicker-check) cmd_flicker_check "${2:-}" ;;
  overprovision-check) cmd_overprovision_check "${2:-2000}" "${3:-50}" "${4:-5000}" ;;
  overcount-check) cmd_overcount_check "${2:-2000}" "${3:-300}" "${4:-1500}" ;;
  limit-stall-check) cmd_limit_stall_check "${2:-2000}" "${3:-5000}" ;;
  priority-fairness-check) cmd_priority_fairness_check "${2:-2000}" "${3:-500}" ;;
  backlog-rescan-check) cmd_backlog_rescan_check "${2:-2000}" "${3:-50000}" ;;
  runner-started-timeout-check) cmd_runner_started_timeout_check ;;
  limit-drain) cmd_limit_drain "${2:-60000}" "${3:-2000}" "${4:-30}" "${5:-0}" ;;
  backup-stall-check) cmd_backup_stall_check "${2:-8}" "${3:-8000}" "${4:-2000}" "${5:-30}" "${6:-2100000}" "${7:-2}" ;;
  backup-stall-fast) cmd_backup_stall_fast "${2:-50}" "${3:-180}" "${4:-100}" ;;
  prod-start) cmd_prod_start "${2:-local}" ;;
  prod-stop) cmd_prod_stop ;;
  crash-recovery) cmd_crash_recovery ;;
  dump) cmd_dump "${2:-lsf}" ;;
  clean) cmd_clean ;;
  status) cmd_status ;;
  help|-h|--help) usage ;;
  *) usage; exit 1 ;;
esac
