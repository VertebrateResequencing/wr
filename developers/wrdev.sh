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

# WR_JOBNAME_TOKEN for our ISOLATED prod-mode manager: its LSF jobs are named
# wrp<token>_* instead of wrp_*, so they can NEVER be confused with (or bkilled
# alongside) a REAL --deployment production manager's wrp_* jobs. See the naming
# hack in jobqueue/scheduler/{scheduler,lsf}.go and .docs/bugfixes/260727-1.md.
PROD_JOBTOKEN="${PROD_JOBTOKEN:-iso$PROD_PORT}"
PROD_JOB_PREFIX="wrp${PROD_JOBTOKEN}_"   # LSF job-name prefix of our isolated prod manager

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
  # optional: put the DB backup on a DIFFERENT filesystem than the DB (WRDEV_PROD_BKFILE),
  # to test whether a separate-volume backup avoids the NFS-write contention that starves
  # the committer (direction E). Absolute path => wr keeps it as-is (see internal/config.go).
  local bkline=""
  [ -n "${WRDEV_PROD_BKFILE:-}" ] && bkline="managerdbbkfile: \"$WRDEV_PROD_BKFILE\""
  cat > "$CONFIG_DIR/.wr_config.production.yml" <<EOF
managerport: "$PROD_PORT"
managerweb: "$PROD_WEB"
managerhost: "localhost"
managerdir: "$WRDEV_ROOT/.wr-prod"
$bkline
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
  # SAFE: our isolated manager's jobs are namespaced ${PROD_JOB_PREFIX}* (never a real wrp_*)
  timeout 60 bkill -J "${PROD_JOB_PREFIX}*" 0 >/dev/null 2>&1
  bjobs -o 'jobid job_name' -noheader 2>/dev/null | awk -v p="$PROD_JOB_PREFIX" 'index($2,p)==1{print $1}' | sort -u | while read -r j; do timeout 30 bkill "$j" >/dev/null 2>&1; done
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
    | grep -aE 'STALL|INFLATE|DUMP|BACKUP|relevant|goroutine|PASS|FAIL|panic|^ok |^---' | grep -avE 'no test files'
  rm -f "$work" "${work}_bk" "${work}_bk.tmp" 2>/dev/null
}

cmd_writestorm_freeze() {  # writestorm-freeze [N] [archivers] - reliable4 FULL prod-freeze repro (A/B)
  # The FAITHFUL freeze repro: on a big freelist-bloated DB, fire an N-job
  # updateJobAfterChange storm and TIME db.archiveJob (the prod freeze victim). The
  # freeze is CPU-bound freelist.Free/spill per commit, so it reproduces in-process
  # (no LSF, no manager, no real commands run - SAFE). PRE-FIX: goroutines explode
  # ~=N AND a synchronous archive is starved past the 60s client floor (freeze ->
  # falsely-lost -> churn) => the test FAILS. POST-FIX (single coalescing writer):
  # goroutines bounded AND archive stays well under 60s => PASS. Confirmed A/B on
  # pristine10 (~4.6GB freelist, N=100k): pre-fix maxArchiveLat 1m13s / 99k
  # goroutines; post-fix under the floor. Needs a big DB (WR_WSFREEZE_DB or
  # WRDEV_PRISTINE_DB) + RAM for N goroutines; it is COPIED (mutated) each run.
  # To A/B the fix itself, run this in a pre-fix `git worktree` vs the fixed tree.
  need_repo
  local n="${1:-100000}" archivers="${2:-8}"
  local db="${WR_WSFREEZE_DB:-${WRDEV_PRISTINE_DB:-}}"
  { [ -n "$db" ] && [ -f "$db" ]; } \
    || die "set WR_WSFREEZE_DB (or WRDEV_PRISTINE_DB) to a big freelist DB (see backup-stall-check / TestReliable4InflateDB)"
  osunset
  WR_WSFREEZE_DB="$db" WR_WSFREEZE_N="$n" WR_WSFREEZE_ARCHIVERS="$archivers" \
    timeout 2400 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4WriteStormFreeze -count=1 -v -timeout 39m 2>&1 \
    | grep -aE 'WSFREEZE|FREEZE|PASS|FAIL|panic|^ok |^---' | grep -avE 'no test files'
}

cmd_archive_rate() {  # archive-rate [archivers] [seconds] [thinkMs] - reliable4 FINDING 2 SCALE GATE
  # SCALE GATE for reliable4 FINDING 2 (.docs/reliable4/prod-run-20260817.md): a SUSTAINED
  # archive rate on a 10GB-class DB must not queue up on the single bbolt write lock. It
  # recreates the measured production regime in-process and SAFELY (no LSF, no manager, no
  # real job command ever executes): ARCHIVERS concurrent "runners", each doing think-then-
  # synchronously-archive on a COPY of a big freelist-bloated DB opened through the real
  # initDB, so every commit pays production's real freelist/page cost - measured at ~109ms
  # per single-archive transaction on pristine10, ie. ~9 archives/s if each archive commits
  # its own (production drained at ~12/s). The think time is JITTERED so the archivers do
  # not run in lockstep: 660 archives arriving in the same microsecond all land in ONE of
  # bbolt's 10ms batching windows and coalesce even WITHOUT the fix, which would make this
  # gate pass vacuously. Jittered arrivals reproduce production spacing, where bbolt's Batch
  # stops coalescing at all (it detaches its batch the instant one starts, so arrivals
  # further apart than MaxBatchDelay each get a transaction of their own).
  #
  # It reports what production reported: archive throughput, mean/p50/p99/max archive
  # latency and archive queue depth. PROD PRE-FIX NUMBERS TO BEAT (~660 runners): queue ~600
  # deep, ~12 archives/s, MEAN block 43.0s, tail over the 60s ClientMinRequestTimeout floor
  # (which is what put successfully exited compress jobs into `delayed`). GATE (the doc's
  # targets): mean < 5s and p99 < 60s, and zero archives over the client floor.
  #
  # A MISSING or INVALID measurement is a FAIL, never a PASS: no ARCHRATE-SUMMARY line, an
  # unparseable or zero archive count, zero depth samples, a non-zero go test exit and any
  # archive error all exit 1 (fast-failing archives would otherwise look like low latency).
  # Needs WR_ARCHRATE_DB (or WRDEV_PRISTINE_DB) = a big DB from the generator (see
  # backup-stall-check / TestReliable4InflateDB); it is COPIED (mutated) each run.
  # A/B the fix by running this in a pre-fix `git worktree` vs the fixed tree.
  need_repo
  local archivers="${1:-660}" secs="${2:-180}" thinkms="${3:-3800}"
  local maxmeanms="${WRDEV_ARCHRATE_MAX_MEAN_MS:-5000}" maxp99ms="${WRDEV_ARCHRATE_MAX_P99_MS:-60000}"
  local db="${WR_ARCHRATE_DB:-${WRDEV_PRISTINE_DB:-}}" gorc=0
  { [ -n "$db" ] && [ -f "$db" ]; } \
    || die "set WR_ARCHRATE_DB (or WRDEV_PRISTINE_DB) to a big freelist DB (see backup-stall-check / TestReliable4InflateDB)"
  local out="$WRDEV_ROOT/archive-rate.out"
  mkdir -p "$WRDEV_ROOT"
  echo "reliable4 FINDING 2 archive-rate gate: $archivers archivers, ${thinkms}ms think time, ${secs}s window"
  echo "  DB $db ($(ls -la "$db" | awk '{print $5}') bytes; COPIED to \$WRDEV_ROOT, never mutated in place)"
  echo "  prod pre-fix numbers to beat: queue ~600 deep, ~12 archives/s, mean block 43000ms, tail >60000ms"
  echo "  gate: mean <= ${maxmeanms}ms, p99 <= ${maxp99ms}ms, 0 archives over the 60s client floor"
  osunset
  WR_ARCHRATE_DB="$db" WR_ARCHRATE_ARCHIVERS="$archivers" WR_ARCHRATE_SECONDS="$secs" \
    WR_ARCHRATE_THINK_MS="$thinkms" \
    timeout $((secs + 1800)) go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4ArchiveRate -count=1 -v -timeout $((secs + 1700))s > "$out" 2>&1 || gorc=$?
  grep -aE 'ARCHRATE|PASS|FAIL|panic|^ok |^---' "$out" | grep -avE 'no test files'

  local sum archives mean p99 maxms rate meandepth maxdepth overfloor errs
  sum=$(grep -aoE 'ARCHRATE-SUMMARY .*' "$out" | tail -1)
  ar_num(){ echo "$sum" | grep -aoE "$1=[0-9]+" | grep -aoE '[0-9]+$' | head -1; }
  archives=$(ar_num archives); mean=$(ar_num meanMs); p99=$(ar_num p99Ms); maxms=$(ar_num maxMs)
  meandepth=$(ar_num meanDepth); maxdepth=$(ar_num maxDepth); overfloor=$(ar_num overFloor)
  errs=$(ar_num errors)
  rate=$(echo "$sum" | grep -aoE 'rate=[0-9.]+' | cut -d= -f2)
  for v in archives mean p99 maxms meandepth maxdepth overfloor errs; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done

  echo "## VERDICT: archives=$archives rate=${rate:-?}/s meanLatency=${mean}ms p99=${p99}ms max=${maxms}ms" \
       "queueDepth mean=$meandepth max=$maxdepth overClientFloor=$overfloor archiveErrors=$errs goExit=$gorc"
  # a gate that PASSES when the measurement is missing is worse than no gate, so an absent
  # summary line, an unreadable/zero archive count, and an unreadable depth sample are all
  # hard FAILURES rather than "0ms, PASS".
  local verdict=0 unmeasured=""
  [ -n "$sum" ] || unmeasured="the run produced no ARCHRATE-SUMMARY line (test crashed, timed out, skipped, or could not open the DB?)"
  [ -z "$unmeasured" ] && [ "$archives" -le 0 ] \
    && unmeasured="no archive completed, so no latency was measured (archives=$archives)"
  [ -z "$unmeasured" ] && { [ "$mean" -lt 0 ] || [ "$p99" -lt 0 ]; } \
    && unmeasured="could not read meanMs/p99Ms out of the summary line"
  [ -z "$unmeasured" ] && [ "$maxdepth" -lt 0 ] \
    && unmeasured="could not read a queue-depth sample, so the queue was never observed"
  if [ -n "$unmeasured" ]; then
    verdict=1
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $out"
  elif [ "$gorc" -ne 0 ] || [ "$errs" -ne 0 ]; then
    verdict=1
    echo "FAIL (TEST FAILED): the reproducer itself failed (go test exit $gorc, archive errors $errs), so the"
    echo "  latency numbers above are not a valid measurement of a healthy archive path; inspect $out"
  elif [ "$mean" -gt "$maxmeanms" ] || [ "$p99" -gt "$maxp99ms" ] || [ "$overfloor" -gt 0 ]; then
    verdict=1
    echo "FAIL: the archive path is queueing on the single bolt write lock again"
    echo "  => pending archives must be folded into ONE db.Update per commit, each waiter replied to"
    echo "     individually (see archiveWriter/applyArchives in jobqueue/db.go)"
  else
    echo "PASS: a sustained $archivers-archiver rate on a $(( $(ls -la "$db" | awk '{print $5}') / 1073741824 ))GB-class DB" \
         "stays at ${mean}ms mean / ${p99}ms p99 with the queue never deeper than $maxdepth"
  fi
  echo "## CLEANUP"
  rm -f "$WRDEV_ROOT/archrate_work_db" "$WRDEV_ROOT/archrate_work_db_bk" 2>/dev/null
  return "$verdict"
}

cmd_confirm_dead_leak() {  # confirm-dead-leak [checks] [host] - reliable4 Fix 5 confirm-dead SSH leak repro
  # Real-LSF, on-farm reproducer for the confirm-dead SSH connection LEAK (diagnosis Fix 5).
  # Drives Scheduler.ProcessNotRunningOnHost (the lost-job dead-confirmation ssh check) N
  # times against a reachable host and counts the ssh-client goroutines left alive. On LSF
  # each check does getHost -> cloud.NewServer (fresh) -> RunCmd -> dials an ssh client and
  # closes only the SESSION; the Host interface has no Close() and the throwaway server is
  # never Destroy()ed, so the client (its goroutines + socket) is NEVER closed - a per-check
  # leak (confirmJobDead does 2 checks/lost job; prod saw confirm-dead ssh conns 892->~5,300,
  # ~31,875 goroutines). Needs LSF present + PASSWORDLESS ssh to the host (default localhost;
  # the leak only forms on SUCCESSFUL dials - a failed dial errors out before caching the
  # client), and SKIPS otherwise. RED now; GREEN once the confirm-dead path closes its host
  # connection after use (add Host.Close(); group checks per host over one connection).
  need_repo
  local checks="${1:-40}" host="${2:-localhost}"
  osunset
  WR_CDLEAK_N="$checks" WR_CDLEAK_HOST="$host" \
    timeout 300 go -C "$REPO" test -tags reliability_repro ./jobqueue/scheduler/ \
      -run TestReliable4ConfirmDeadSSHLeak -count=1 -v -timeout 240s 2>&1 \
    | grep -aE 'CONFIRMDEAD-LEAK|confirm-dead SSH|PASS|FAIL|SKIP|panic|^ok |^---' | grep -avE 'no test files'
}

cmd_ttrmiss_check() {  # ttrmiss-check [jobs] [runners] [archiveDelayMs] - in-process TTR-miss archive-reject churn
  # Deterministic, in-process (no LSF/manager, build-tagged reliability_repro): a runner
  # pool does reserve -> Started(deadPid) -> [optionally keep touching] -> wait archiveDelay
  # -> Archive(success). The runner-pid liveness fix (record the runner's own pid, confirm a
  # lost job dead only if BOTH the command AND runner pids are gone) is now the DEFAULT, so a
  # live-but-starved runner (WR_TTRMISS_TOUCH=0, archiveDelay>TTR) is parked, not re-run, and
  # its late archive is accepted (no churn); use WR_TTRMISS_RUNNER_DEAD=1 to see a genuinely-
  # dead runner correctly re-run. Knobs (env):
  #   WR_TTRMISS_TOUCH=1        model a healthy touching runner (control: no churn)
  #   WR_TTRMISS_RUNNER_DEAD=1  model a genuinely-dead runner (both pids gone -> must re-run)
  #   WR_TTRMISS_SECONDS=N      run duration (default 60)
  # The wedged-runner backstop is a config value (ServerConfig.Timings.LostRunnerBackstop,
  # default 1h) validated by the dedicated TestReliable4TtrBackstopKill. The untagged
  # make-test regressions are TestReliable4RunnerPidLiveness / TestReliable4LostRunnerBackstop
  # (jobqueue) and TestKillProcessCommandContract (scheduler). See .docs/reliable4/ttrmiss.md.
  need_repo
  local jobs="${1:-60}" runners="${2:-20}" delay="${3:-1500}" secs="${WR_TTRMISS_SECONDS:-60}"
  echo "in-process TTR-miss churn: jobs=$jobs runners=$runners archiveDelayMs=$delay TTR=500ms seconds=$secs"
  echo "  knobs: TOUCH=${WR_TTRMISS_TOUCH:-0} RUNNER_DEAD=${WR_TTRMISS_RUNNER_DEAD:-0} (runner-pid fix is default-on)"
  osunset
  WR_TTRMISS_JOBS="$jobs" WR_TTRMISS_RUNNERS="$runners" WR_TTRMISS_ARCHIVE_DELAY_MS="$delay" WR_TTRMISS_TTR_MS=500 WR_TTRMISS_SECONDS="$secs" \
    timeout $((secs + 150)) go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4TtrMissChurn -count=1 -v -timeout $((secs + 120))s 2>&1 \
    | grep -aE 'TTRMISS|PASS|FAIL|panic|^ok ' | grep -avE 'no test files'
}

cmd_report_storm() {  # report-storm [jobs] [runners] [limit] [seconds] - reliable4 post-resume report storm
  # FAITHFUL in-process LOAD reproducer (build-tagged reliability_repro; NO LSF, NO manager process,
  # NO runner subprocess) for the reliable4 "post-resume report storm": N fast jobs behind ONE small
  # limit group across a few sibling memory groups, and M concurrent REAL client "runners" tight-
  # looping ReserveScheduled -> Started(os.Getpid()) -> [15s-style touch loop] -> [stop touching] ->
  # Archive(success), classifying every RPC exactly as the runner's reportFinalState retry loop. In
  # prod the manager could not service reports fast enough: reports hit err="receive time out" ->
  # reconnect -> retry -> rejected "bad job" -> "will need to be rerun", so successful fast commands
  # were re-run forever and `complete` never advanced. It measures per-RPC outcomes (accepted / bad
  # job / must-reserve / receive-time-out / other) for started+touch+archive, archive latency
  # max/p50/p99, reserves, reconnects, and the live queue breakdown; final VERDICT = drained or churned.
  # Optional big-DB confound (adds realistic archive-commit latency + periodic backups):
  #   WR_RS_DB=/nfs/hgi/wr/sb10-bigdb/pristine10  (serve() opens a mutable COPY under WRDEV_ROOT;
  #                                                 needs ~2x its size of scratch there)
  # Other env knobs: WR_RS_TTR_MS (server ItemTTR, default 60000 = real), WR_RS_CMD_MS (simulated
  # command runtime, default 0 = pure storm), WR_RS_STATUS=N (N concurrent `wr status`-style pollers
  # that drive the O(backlog) s.q.AllItems() scan + complete-jobs DB read - the prime-suspect
  # amplifier the runner storm alone does not exercise), WR_RS_STATUS_MS (poll interval, default 500).
  # NOTE: in-process the client's per-request receive
  # deadline is floored at 60s (ClientMinRequestTimeout) and all runners share ONE live pid, so on
  # the fixed branch this is expected to DRAIN cleanly (a negative control / regression guard). The
  # faithful churn needs real (NFS) storage + thousands of distinct dying runner processes; use the
  # LSF-scale limit-drain for that.
  need_repo
  local jobs="${1:-5000}" runners="${2:-200}" limit="${3:-2000}" secs="${4:-120}"
  echo "in-process report-storm: jobs=$jobs runners=$runners limit=$limit seconds=$secs bigDB=${WR_RS_DB:-none}"
  echo "  knobs: WR_RS_TTR_MS=${WR_RS_TTR_MS:-60000} WR_RS_CMD_MS=${WR_RS_CMD_MS:-0} WRDEV_ROOT=$WRDEV_ROOT"
  echo "  status_pollers=${WR_RS_STATUS:-0} (interval ${WR_RS_STATUS_MS:-500}ms) profile=${WR_RS_PROFILE_DIR:-off}"
  mkdir -p "$WRDEV_ROOT"
  osunset
  WR_RS_JOBS="$jobs" WR_RS_RUNNERS="$runners" WR_RS_LIMIT="$limit" WR_RS_SECONDS="$secs" \
  WR_RS_DB="${WR_RS_DB:-}" WR_RS_PROFILE_DIR="${WR_RS_PROFILE_DIR:-}" \
  WR_RS_STATUS="${WR_RS_STATUS:-0}" WR_RS_STATUS_MS="${WR_RS_STATUS_MS:-500}" \
    timeout $((secs + 600)) go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4ReportStorm -count=1 -v -timeout $((secs + 540))s 2>&1 \
    | grep -aE 'REPORTSTORM|PASS|FAIL|panic|^ok ' | grep -avE 'no test files'
}

cmd_report_storm_profile() {  # report-storm-profile [jobs] [runners] [limit] [seconds] - report-storm + pprof
  # As report-storm, but with the mutex+block+CPU profiler ON (writes
  # reportstorm_<config>.{cpu,mutex,block}.pprof to WRDEV_ROOT). Use this to PIN the
  # serialization point by symbol name, e.g. at the 50k-backlog + M=1000 config:
  #   wrdev.sh report-storm-profile 50000 1000 2000 240
  #   go tool pprof -top -nodecount=25 $WRDEV_ROOT/reportstorm_j50000_r1000_l2000.mutex.pprof
  #   go tool pprof -top -nodecount=25 $WRDEV_ROOT/reportstorm_j50000_r1000_l2000.block.pprof
  # (append _bigdb to the name when WR_RS_DB is set). Profiling perturbs timings slightly.
  WR_RS_PROFILE_DIR="$WRDEV_ROOT" cmd_report_storm "${1:-50000}" "${2:-1000}" "${3:-2000}" "${4:-240}"
}

cmd_report_storm_lsf() {  # report-storm-lsf [jobs] [limit] [runsec] - LSF-scale FAITHFUL report-storm CHURN repro
  # The in-process report-storm CANNOT reproduce the prod churn: the client receive deadline
  # is floored at 60s (ClientMinRequestTimeout), so a report only times out if ONE server RPC
  # exceeds 60s, and in-process (fast storage, ONE shared runner pid) the worst was 16.5s and
  # a spuriously-lost job just parks (its owner's archive still accepted). Reproducing the churn
  # needs the two prod-only amplifiers, which THIS provides:
  #   (1) real (NFS) storage + a big DB so the periodic full-file backup starves the bbolt
  #       committer past 60s (raise WRDEV_PRISTINE_DB size / concurrency until a GAP > 60s shows);
  #   (2) real LSF runners = thousands of DISTINCT pids, so a job whose runner exited/was killed
  #       is confirmed dead and released -> its late archive is then rejected "bad job" -> the
  #       success is discarded and the job re-runs (retries 30) -> the spiral.
  # Shape (like prod's results_portal:2000): N >> limit SHORT jobs in ONE limit group across
  # MEM_GROUPS memory groups on an isolated PROD-mode manager (backups ON) opened on a COPY of a
  # big pre-generated DB. Its LSF jobs are namespaced ${PROD_JOB_PREFIX}* so cleanup is SAFE while
  # a real production manager runs. Healthy code drains; pre-fix, complete STALLS while badjob /
  # confirmed_dead / delayed climb and the manager log shows backup GAPs. Knobs: WRDEV_PRISTINE_DB
  # (big DB to copy; REQUIRED), WR_RS_PADKB (pad each cmd ~NKB to match prod's ~25KB debug lines),
  # WRDEV_DEBUG=1 (manager --debug, like prod). To amplify without more LSF slots, raise `limit`
  # (more concurrent archivers => longer backup stall) or use a bigger DB.
  need_bin
  local n="${1:-100000}" limit="${2:-2000}" runsec="${3:-1}"
  local pr="$PROD_RUN" plog="$PROD_RUN/log"
  # optional: back the DB up to a DIFFERENT filesystem (direction E). WR_RS_BKDIR=<dir on
  # another FS, e.g. Lustre> => the manager's db_bk lives there, so backup writes don't
  # contend for the DB filesystem's write bandwidth. Must be set before ensure_config.
  [ -n "${WR_RS_BKDIR:-}" ] && export WRDEV_PROD_BKFILE="$WR_RS_BKDIR/db_bk_$PROD_JOBTOKEN"
  ensure_config
  [ -n "${WR_RS_BKDIR:-}" ] && echo "backup filesystem: $WRDEV_PROD_BKFILE (separate from DB on $pr/db)"
  [ -n "${WRDEV_PRISTINE_DB:-}" ] && [ -f "${WRDEV_PRISTINE_DB}" ] \
    || die "set WRDEV_PRISTINE_DB to a big pre-generated DB (make one with: $0 backup-stall-check, or see its header)"
  cmd_prod_stop >/dev/null 2>&1; sleep 2
  mkdir -p "$pr"; rm -f "$pr/db" "$pr/db_bk"* "$pr/log" 2>/dev/null
  echo "copying pristine DB ${WRDEV_PRISTINE_DB} -> $pr/db (mutated by this run)"
  cp -f "${WRDEV_PRISTINE_DB}" "$pr/db" || die "could not copy pristine DB (room for ~2x its size under $WRDEV_ROOT?)"
  echo "db size: $(ls -la "$pr/db" 2>/dev/null | awk '{print $5}') bytes; LSF jobs will be ${PROD_JOB_PREFIX}*"
  local dbg=""; [ "${WRDEV_DEBUG:-0}" = "1" ] && dbg="--debug"
  local ppf=""; [ -n "${WR_RS_PPROF:-}" ] && ppf="WR_PPROF_ADDR=localhost:$WR_RS_PPROF"
  echo "starting isolated PROD-mode manager (backups ON) on the big DB; debug=${WRDEV_DEBUG:-0} pprof=${WR_RS_PPROF:-off}"
  # shellcheck disable=SC2086 # deliberate word-splitting of the optional env assignments
  osunset ; env WR_JOBNAME_TOKEN="$PROD_JOBTOKEN" $ppf timeout 90 "$WR" manager start --deployment production -s lsf $dbg 2>&1 \
    | grep -aE 'started on|token=' | head -2
  echo "pid $(mgr_pid "$PROD_RUN")"
  sleep 5
  # optional pprof sampler: dump all goroutine stacks every 5s so we CATCH a freeze in the
  # act (the pprof http server runs on its own goroutine, unaffected by the DB-committer
  # freeze, so it keeps answering). Correlate a dump's mtime with a log GAP to see who is
  # blocked where (expect: bbolt committer in fdatasync, backup in tx.WriteTo, archivers in
  # Batch sync.Cond.Wait). Enable with WR_RS_PPROF=<port>.
  local sampler_pid="" pdir=""
  if [ -n "${WR_RS_PPROF:-}" ]; then
    pdir="$WRDEV_ROOT/rsprof"; mkdir -p "$pdir"; rm -f "$pdir"/* 2>/dev/null
    ( while true; do timeout 8 curl -s "http://localhost:$WR_RS_PPROF/debug/pprof/goroutine?debug=2" -o "$pdir/g_$(date +%s).txt" 2>/dev/null; sleep 5; done ) &
    sampler_pid=$!
    echo "goroutine sampler pid $sampler_pid -> $pdir (every 5s)"
  fi
  local pad=""; local padkb="${WR_RS_PADKB:-0}"
  [ "$padkb" -gt 0 ] && pad=$(head -c $((padkb*1024)) /dev/zero | tr '\0' x)
  echo "adding $n sleep-$runsec jobs (limit reprolimit:$limit, pad ${padkb}KB) across $MEM_GROUPS mem groups"
  PAD="$pad" perl -e "for my \$i (1..$n){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"sleep $runsec #'.\$i.' '.\$ENV{PAD}.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/rsjobs.json"
  local out rc
  out=$(osunset; timeout 300 "$WR" add -f "$WRDEV_ROOT/rsjobs.json" --rep_grp rgrs --limit_grps "reprolimit:$limit" --retries 30 --deployment production 2>&1); rc=$?
  echo "$out" | tail -1
  echo "$out" | grep -qE 'Added [1-9][0-9]* new commands' || die "report-storm-lsf aborted - 0 jobs added (manager up?)"
  report_storm_lsf_monitor "$n" "$plog"
  if [ -n "${WR_RS_PPROF:-}" ]; then
    echo "## capturing block/mutex/heap profiles -> $pdir (analyse with: go tool pprof -top <file>)"
    timeout 20 curl -s "http://localhost:$WR_RS_PPROF/debug/pprof/block" -o "$pdir/block.pprof" 2>/dev/null
    timeout 20 curl -s "http://localhost:$WR_RS_PPROF/debug/pprof/mutex" -o "$pdir/mutex.pprof" 2>/dev/null
    timeout 20 curl -s "http://localhost:$WR_RS_PPROF/debug/pprof/heap"  -o "$pdir/heap.pprof"  2>/dev/null
    [ -n "$sampler_pid" ] && kill "$sampler_pid" 2>/dev/null
    echo "captured $(ls -1 "$pdir"/g_*.txt 2>/dev/null | wc -l) goroutine dumps + block/mutex/heap"
  fi
  echo "## CLEANUP"; cmd_prod_stop >/dev/null 2>&1
  # SAFE: only our namespaced isolated-manager jobs; NEVER a real wrp_*
  timeout 60 bkill -J "${PROD_JOB_PREFIX}*" 0 >/dev/null 2>&1
  bjobs -o 'jobid job_name' -noheader 2>/dev/null | awk -v p="$PROD_JOB_PREFIX" 'index($2,p)==1{print $1}' | sort -u | while read -r j; do timeout 30 bkill "$j" >/dev/null 2>&1; done
  rm -f "$WRDEV_ROOT/rsjobs.json" "$pr/db" "$pr/db_bk"* 2>/dev/null
  [ -n "${WRDEV_PROD_BKFILE:-}" ] && rm -f "$WRDEV_PROD_BKFILE"* 2>/dev/null
}

report_storm_lsf_monitor() {  # churn/stall monitor for report-storm-lsf (prod-mode manager)
  local n="${1:-100000}" plog="$2"; local t0; t0=$(date +%s); local prevc=-1 stall=0
  local basebad=-1 maxrpc=0 maxdelayed=0 maxlost=0 bj=0
  num(){ echo "$1" | grep -oE "$2: [0-9]+" | grep -oE '[0-9]+' | head -1; }
  for _ in $(seq 1 60); do
    local st cc cb cr cl cd run kd ar s e rpc
    st=$(osunset; timeout 30 "$WR" status --deployment production -i rgrs -o counts 2>/dev/null)
    cc=$(num "$st" complete); cb=$(num "$st" buried); cr=$(num "$st" running); cd=$(num "$st" delayed)
    cl=$(echo "$st"|grep -oE 'lost[^0-9]*[0-9]+'|grep -oE '[0-9]+'|head -1)
    cc=${cc:-0}; cb=${cb:-0}; cr=${cr:-0}; cd=${cd:-0}; cl=${cl:-0}
    run=$(timeout 20 bjobs -o stat -noheader 2>/dev/null | grep -c RUN)
    bj=$(grep -ac 'bad job' "$plog" 2>/dev/null); bj=${bj:-0}
    kd=$(grep -ac 'confirming it was dead' "$plog" 2>/dev/null); kd=${kd:-0}
    ar=$(grep -ac 'jarchive.*bad job\|jarchive.*must Reserve' "$plog" 2>/dev/null); ar=${ar:-0}
    [ "$basebad" -lt 0 ] && basebad=$bj
    s=$(date +%s%3N); timeout 65 "$WR" status --deployment production -i rgrs -o counts >/dev/null 2>&1; e=$(date +%s%3N); rpc=$((e-s))
    [ "$cd" -gt "$maxdelayed" ] && maxdelayed=$cd; [ "$rpc" -gt "$maxrpc" ] && maxrpc=$rpc; [ "$cl" -gt "$maxlost" ] && maxlost=$cl
    echo "t+$(( $(date +%s)-t0 ))s complete=$cc/$n running=$cr delayed=$cd lost=$cl LSF_RUN=$run badjob=$bj confirmed_dead=$kd archive_reject=$ar status_rpc=${rpc}ms"
    [ $((cc+cb)) -ge "$n" ] && { echo "FULLY DRAINED (complete=$cc buried=$cb)"; break; }
    if [ "$cc" -eq "$prevc" ]; then stall=$((stall+1)); else stall=0; fi
    prevc=$cc
    [ "$stall" -ge 6 ] && { echo "CHURN/STALL REPRODUCED: complete stuck at $cc/$n ~3min (badjob=$bj confirmed_dead=$kd archive_reject=$ar delayed=$cd lost=$cl)"; break; }
    sleep 30
  done
  echo "## manager log freezes (gaps >5s) - a gap > 60s crosses the client receive floor:"
  grep -oaP 'T\d\d:\d\d:\d\d' "$plog" 2>/dev/null | uniq | awk -F: '{t=$1*3600+$2*60+$3} NR==1{p=t} {if(t-p>5)print "  GAP "(t-p)"s ending "$0; p=t}' | tail -10
  echo "## VERDICT: badjobDelta=$(( bj - basebad )) maxDelayed=$maxdelayed maxLost=$maxlost maxStatusRPC=${maxrpc}ms"
}

cmd_unsuspend_burst() {  # unsuspend-burst [jobs] [pprofPort] - reliable4 PROD FREEZE repro (write-storm)
  # FAITHFUL scale reproducer of the CONFIRMED prod-freeze root cause (live pprof
  # 2026-07-28; see ../.docs/reliable4/prod-freeze-pprof-diagnosis.md). Un-suspending
  # a large batch flips ~100k jobs' state at once; each state change spawns ONE
  # unbounded `go db.bolt.Batch` (updateJobAfterChange), so on a big freelist-bloated
  # DB the bbolt committer collapses into thousands of tiny serialised fsync'd txns
  # (CPU-bound in freelist.Free/spill) and the whole manager freezes: control RPCs and
  # the synchronous archive path block past the client's 60s floor -> churn.
  #
  # Shape (exactly the prod trigger): N jobs in ONE limit group set to 0 (so they are
  # ready-but-blocked and NEVER run - zero LSF load, totally farm-safe) on an isolated
  # PROD-mode manager (backups ON, pprof ON) opened on a COPY of a big freelist-bloated
  # DB. We mass-SUSPEND them (stage), let that settle, then fire the BURST: a single
  # `wr resume` un-suspends all N at once => N simultaneous updateJobAfterChange.
  #
  # An embedded goroutine classifier (the prod _capture_load.sh logic) samples the
  # pprof endpoint every few seconds and reports the freeze signature:
  #   total   = total goroutines (prod: 119k -> 438k)
  #   bw      = goroutines blocked in bbolt.(*DB).Batch  (prod: 114,459; freeze if >3000)
  #   bwmax   = max minutes any Batch caller has been blocked (freeze if >=1)
  #   in_commit = goroutines in Tx.Commit/write/fdatasync (the stuck committer)
  # plus each tick a control-plane `wr status` RPC is timed (prod: blocked >60s).
  #
  # PASS/FAIL: pre-fix -> bw explodes to ~N, bwmax climbs >=1min, status RPC and log
  # GAPs cross 60s => FREEZE REPRODUCED. After the bounded single-writer fix -> bw
  # stays low and drains fast, no bwmax growth, status stays responsive => NO FREEZE.
  # This is the authoritative gate a /bugfix reviewer must re-run post-fix.
  #
  # REQUIRES WRDEV_PRISTINE_DB = a big freelist-bloated DB to copy: the real prod.db
  # copy (/nfs/hgi/wr/sb10-bigdb/prod.db - most faithful for the SUSTAINED >60s freeze,
  # but NB it recovers its own live jobs; use only with -s local) or a record-dense
  # pristine* DB (see backup-stall-check; all-complete, no live recovery - the clean
  # choice for the bw-explosion signature). WRDEV_ROOT needs room for ~2x the DB (copy
  # + backup); point it at a roomy filesystem (e.g. /nfs/hgi/wr/...), NOT a small home.
  # Goroutine dumps are classified then deleted so they cannot fill the disk.
  need_bin; ensure_config
  local n="${1:-100000}" pprof="${2:-6062}"
  local pr="$PROD_RUN" plog="$PROD_RUN/log"
  [ -n "${WRDEV_PRISTINE_DB:-}" ] && [ -f "${WRDEV_PRISTINE_DB}" ] \
    || die "set WRDEV_PRISTINE_DB to a big freelist-bloated DB (e.g. /nfs/hgi/wr/sb10-bigdb/pristine10 or .../prod.db)"
  cmd_prod_stop >/dev/null 2>&1; sleep 2
  mkdir -p "$pr"; rm -f "$pr/db" "$pr/db_bk"* "$pr/log" 2>/dev/null
  echo "copying pristine DB ${WRDEV_PRISTINE_DB} -> $pr/db (mutated by this run)"
  cp -f "${WRDEV_PRISTINE_DB}" "$pr/db" || die "could not copy pristine DB (room for ~2x its size under $WRDEV_ROOT?)"
  echo "db size: $(ls -la "$pr/db" 2>/dev/null | awk '{print $5}') bytes; scheduler=local (limit 0 => 0 LSF jobs)"
  echo "starting isolated PROD-mode manager (backups ON, pprof localhost:$pprof) on the big DB"
  osunset ; env WR_JOBNAME_TOKEN="$PROD_JOBTOKEN" WR_PPROF_ADDR="localhost:$pprof" timeout 90 "$WR" \
    manager start --deployment production -s local 2>&1 | grep -aE 'started on|token=' | head -2
  echo "pid $(mgr_pid "$PROD_RUN")"
  sleep 8

  echo "adding $n sleep-1 jobs in limit group burstlimit:0 (ready-but-blocked; NEVER run) rep_grp rgburst"
  perl -e "for my \$i (1..$n){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"sleep 1 #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" > "$WRDEV_ROOT/ubjobs.json"
  local out rc
  out=$(osunset; timeout 300 "$WR" add -f "$WRDEV_ROOT/ubjobs.json" --rep_grp rgburst --limit_grps "burstlimit:0" --retries 0 --deployment production 2>&1); rc=$?
  echo "$out" | tail -1
  echo "$out" | grep -qE 'Added [1-9][0-9]* new commands' || die "unsuspend-burst aborted - 0 jobs added (manager up?)"
  sleep 3

  echo "STAGING: mass-suspend all $n jobs (ready -> suspended), then let the write goroutines settle"
  osunset; timeout 180 "$WR" suspend --deployment production -i rgburst >/dev/null 2>&1
  ub_wait_settle "$pprof" "suspend-stage"

  # start the goroutine classifier BEFORE the burst so we catch its onset.
  local pdir="$WRDEV_ROOT/ubprof"; mkdir -p "$pdir"; rm -f "$pdir"/* 2>/dev/null
  ub_sampler "$pprof" "$pdir" &
  local sampler_pid=$!
  echo "goroutine classifier pid $sampler_pid -> $pdir/signals.tsv (every 3s)"
  sleep 3

  echo "=== BURST: un-suspend all $n jobs at once (the prod trigger) ==="
  local bs be
  bs=$(date +%s%3N)
  osunset; timeout 300 "$WR" resume --deployment production -i rgburst >/dev/null 2>&1
  be=$(date +%s%3N)
  echo "resume RPC returned in $((be-bs))ms (the storm is now in the background write goroutines)"

  ub_monitor "$pprof" "$plog" "$n"

  kill "$sampler_pid" 2>/dev/null
  echo "## goroutine classifier peak signals:"
  awk -F'\t' 'NR>1{if($2>mt)mt=$2; if($3>mb)mb=$3; if($4>mx)mx=$4; if($5>mc)mc=$5}
    END{printf "  peak total=%d  peak bw(Batch-blocked)=%d  peak bwmax=%dmin  peak in_commit=%d\n", mt,mb,mx,mc}' \
    "$pdir/signals.tsv" 2>/dev/null
  local peakbw peakbwmax
  peakbw=$(awk -F'\t' 'NR>1&&$3>m{m=$3}END{print m+0}' "$pdir/signals.tsv" 2>/dev/null)
  peakbwmax=$(awk -F'\t' 'NR>1&&$4>m{m=$4}END{print m+0}' "$pdir/signals.tsv" 2>/dev/null)
  echo "## VERDICT (write-storm): peak bw(Batch-blocked goroutines)=${peakbw:-0} peak bwmax=${peakbwmax:-0}min maxStatusRPC (see above)"
  if [ "${peakbw:-0}" -gt 5000 ]; then
    echo "WRITE-STORM REPRODUCED: the un-suspend burst spawned ${peakbw} concurrent bbolt.(*DB).Batch goroutines"
    echo "  (prod measured 114,459). The unbounded per-change 'go db.bolt.Batch' is the freeze's engine."
    if [ "${peakbwmax:-0}" -ge 1 ]; then
      echo "  + SUSTAINED FREEZE: a committer stayed blocked >=${peakbwmax}min (crosses the 60s client floor => churn)."
    else
      echo "  NB the storm here drained without any single committer blocking >=1min (this synthetic freelist"
      echo "  commits faster than prod's). For the sustained >60s freeze use the real freelist-bloated"
      echo "  WRDEV_PRISTINE_DB=/nfs/hgi/wr/sb10-bigdb/prod.db and/or a bigger burst; the bw explosion above"
      echo "  IS the DB-size-independent primary signature and the clean pre/post-fix A/B gate."
    fi
  else
    echo "NO WRITE-STORM: best-effort writes stayed bounded (peak bw=${peakbw:-0}) - the single-writer fix works."
  fi
  echo "## manager log freezes (gaps >5s; a gap >60s crosses the client receive floor):"
  grep -oaP 'T\d\d:\d\d:\d\d' "$plog" 2>/dev/null | uniq | awk -F: '{t=$1*3600+$2*60+$3} NR==1{p=t} {if(t-p>5)print "  GAP "(t-p)"s ending "$0; p=t}' | tail -10

  echo "## CLEANUP"; cmd_prod_stop >/dev/null 2>&1
  rm -f "$WRDEV_ROOT/ubjobs.json" "$pr/db" "$pr/db_bk"* 2>/dev/null
}

# ub_wait_settle waits until the total goroutine count stops moving (the staged
# suspend's write goroutines have drained), so the burst starts from a clean base.
ub_wait_settle() {  # <pprofPort> <label>
  local pprof="$1" label="$2" prev=-1 same=0
  for _ in $(seq 1 40); do
    local tot
    tot=$(timeout 8 curl -s "http://localhost:$pprof/debug/pprof/goroutine?debug=1" 2>/dev/null \
      | grep -oE '^goroutine profile: total [0-9]+' | grep -oE '[0-9]+$')
    tot=${tot:-0}
    if [ "$tot" -eq "$prev" ]; then same=$((same+1)); [ "$same" -ge 3 ] && break; else same=0; fi
    prev=$tot
    sleep 2
  done
  echo "  $label settled at total goroutines=$prev"
}

# ub_sampler is the embedded prod _capture_load.sh classifier: every 3s it grabs a
# full goroutine dump, classifies write-path involvement + max block minutes, appends
# a TSV row and emits sparse FREEZE-START/ONGOING/CLEARED lines.
ub_sampler() {  # <pprofPort> <outdir>
  local pprof="$1" pdir="$2"
  local ct="$pdir/signals.tsv"
  echo -e "epoch\ttotal\tbw\tbwmax\tin_commit\tin_backup\tssh" > "$ct"
  local frozen=0 fsince=0
  while true; do
    local now raw
    now=$(date +%s); raw="$pdir/goro2_${now}.txt"
    if ! timeout 30 curl -sS "http://localhost:$pprof/debug/pprof/goroutine?debug=2" -o "$raw" 2>/dev/null; then
      echo "  CURL-FAIL epoch=$now (pprof unreachable or >30s to answer - itself a strong freeze signal)"
      sleep 3; continue
    fi
    local total ssh
    total=$(grep -c '^goroutine ' "$raw")
    ssh=$(grep -c 'handleGlobalRequests' "$raw")
    read bw bwmax cw bkw < <(awk '
      function flush(){ if(!inb)return; if(isB){bw++; if(mins>bwm)bwm=mins} if(isC)cw++; if(isK)bkw++; inb=0 }
      /^goroutine /{ flush(); inb=1; isB=0; isC=0; isK=0; mins=0
        if($0 ~ /minutes\]/){ n=$0; sub(/ minutes\].*/,"",n); sub(/.*[^0-9]/,"",n); mins=n+0 } }
      /bbolt\.\(\*DB\)\.Batch/{isB=1}
      /fdatasync|Fdatasync|bbolt\.\(\*Tx\)\.Commit|bbolt\.\(\*Tx\)\.write/{isC=1}
      /backupToBackupFile|backgroundBackup|copyBackup|bbolt\.\(\*Tx\)\.WriteTo/{isK=1}
      /^$/{flush()}
      END{flush(); printf "%d %d %d %d", bw,bwm,cw,bkw}' "$raw")
    echo -e "${now}\t${total}\t${bw}\t${bwmax}\t${cw}\t${bkw}\t${ssh}" >> "$ct"
    rm -f "$raw" 2>/dev/null  # do NOT retain dumps (each is MBs x many => fills the disk); signals.tsv has what we need
    local isfz=0
    if [ "${bw:-0}" -gt 3000 ] || [ "${bwmax:-0}" -ge 1 ]; then isfz=1; fi
    local sig="total=$total bw=$bw bwmax=${bwmax}m in_commit=$cw in_backup=$bkw ssh=$ssh"
    if [ "$isfz" -eq 1 ] && [ "$frozen" -eq 0 ]; then frozen=1; fsince=$now; echo "  FREEZE-START epoch=$now $sig"
    elif [ "$isfz" -eq 1 ]; then echo "  FREEZE-ONGOING dur=$((now-fsince))s $sig"
    elif [ "$isfz" -eq 0 ] && [ "$frozen" -eq 1 ]; then frozen=0; echo "  FREEZE-CLEARED dur=$((now-fsince))s $sig"
    fi
    sleep 3
  done
}

# ub_monitor watches control-plane responsiveness (status RPC latency) + queue state
# for a few minutes after the burst.
ub_monitor() {  # <pprofPort> <plog> <n>
  local pprof="$1" plog="$2" n="$3"; local t0; t0=$(date +%s); local maxrpc=0
  num(){ echo "$1" | grep -oE "$2: [0-9]+" | grep -oE '[0-9]+' | head -1; }
  for _ in $(seq 1 40); do
    local st cr csu s e rpc
    s=$(date +%s%3N); st=$(osunset; timeout 65 "$WR" status --deployment production -i rgburst -o counts 2>/dev/null); e=$(date +%s%3N); rpc=$((e-s))
    cr=$(num "$st" ready); csu=$(num "$st" suspended); cr=${cr:-0}; csu=${csu:-0}
    [ "$rpc" -gt "$maxrpc" ] && maxrpc=$rpc
    echo "t+$(( $(date +%s)-t0 ))s ready=$cr suspended=$csu status_rpc=${rpc}ms"
    sleep 3
  done
  echo "## VERDICT: maxStatusRPC=${maxrpc}ms (prod froze control RPCs past the 60000ms client floor)"
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

cmd_idle_backlog_cpu() {  # idle-backlog-cpu [jobs] [seconds] [pprofPort] - reliable4 FINDING 3 idle-backlog CPU burn
  # SCALE GATE for reliable4 FINDING 3 (.docs/reliable4/prod-run-20260817.md): an IDLE
  # manager with a big limit-blocked ready backlog must burn almost no CPU. It recreates
  # the live 245x A/B exactly: N jobs in ONE limit group set to 0, so they are all
  # ready-but-blocked, nothing is ever schedulable, NO runners are ever launched (so this
  # is farm-safe: -s local, zero LSF jobs) and there is zero real work to do. Pre-fix the
  # O(backlog) rac pre-pass recomputed 2 MD5s + a sort + several allocations for every one
  # of those jobs on every cycle: prod measured 19,640ms of CPU per 25s (0.79 cores) with
  # 41.8% of it in Job.schedulerGroupSnapshot, versus 80ms per 25s with an empty backlog.
  #
  # rac cycles only run when something enters ready (or once a minute on
  # CheckRunnerTime), so a background driver adds ONE job (into the same limit-0 group, so
  # it too is blocked and never runs) every WRDEV_IDLE_TRIGGER_S seconds for the whole
  # sample window; each add fires the ready-added callback, i.e. one full pre-pass over the
  # whole backlog. The rate is deliberately FIXED (and reported per cycle) rather than a
  # tight loop: a tight loop just saturates the manager whatever a cycle costs, whereas
  # production cycled at the rate its completions arrived, so a fixed rate is what makes
  # the CPU numbers comparable to the live A/B and sensitive to the per-cycle cost.
  #
  # It then takes a real CPU profile of the manager and reports the manager's total CPU,
  # the CPU inside Job.schedulerGroupSnapshot, and both again per rac cycle. PASS = the
  # memoised pre-pass keeps them near the empty-backlog floor; FAIL = the O(N)
  # MD5+sort+allocate recomputation is back.
  need_bin; ensure_config
  local n="${1:-50000}" secs="${2:-25}" pprof="${3:-6063}"
  local maxms="${WRDEV_IDLE_MAX_MS:-2500}" maxsnapms="${WRDEV_IDLE_MAX_SNAP_MS:-800}"
  local trig="${WRDEV_IDLE_TRIGGER_S:-2}"
  local ptxt="$WRDEV_ROOT/idle-backlog-cpu.top" pcount="$WRDEV_ROOT/idle-backlog-cpu.cycles"
  echo "reliable4 FINDING 3 idle-backlog CPU gate: $n ready-but-blocked jobs (limit group idlelimit:0),"
  echo "no runners, ${secs}s CPU profile of the manager while one rac cycle is driven every ${trig}s."
  echo "prod pre-fix numbers to beat: 19640ms per 25s (0.79 cores), 41.8% of it (8210ms) in"
  echo "schedulerGroupSnapshot; prod empty-backlog floor: 80ms per 25s."
  echo "Gate: total <= ${maxms}ms and schedulerGroupSnapshot <= ${maxsnapms}ms per ${secs}s. The gate is on"
  echo "ABSOLUTE cpu, not on the snapshot SHARE: post-fix the pre-pass is still O(backlog) CHEAP"
  echo "field reads (the memo is read under each job's read lock), so it stays a large share of a"
  echo "tiny total - a share threshold would flag a manager that is doing almost nothing."
  echo "A MISSING measurement (no profile, no samples, no rac cycles) is a FAIL, not a PASS."
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s local, pprof localhost:$pprof); dev mode wipes the DB"
  osunset ; env WR_PPROF_ADDR="localhost:$pprof" timeout 90 "$WR" manager start \
    --deployment development -s local 2>&1 | grep -aE 'started on|token=' | head -2
  local mpid; mpid=$(mgr_pid "$DEV_RUN")
  { [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; } || die "could not start dev manager"
  echo "pid $mpid"
  sleep 3

  echo "adding $n jobs in limit group idlelimit:0 (rep_grp rgidle); they can NEVER be scheduled"
  perl -e "for my \$i (1..$n){my \$m=500+((\$i%$MEM_GROUPS)*10); print '{\"cmd\":\"sleep 1 #'.\$i.'\",\"queue\":\"$QUEUE\",\"memory\":\"'.\$m.'M\"}'.\"\n\"}" \
    > "$WRDEV_ROOT/idlejobs.json"
  local out rc
  out=$(osunset; timeout 600 "$WR" add -f "$WRDEV_ROOT/idlejobs.json" --rep_grp rgidle \
    --limit_grps "idlelimit:0" --retries 0 --deployment development 2>&1); rc=$?
  echo "$out" | tail -1
  { [ "$rc" -eq 0 ] && echo "$out" | grep -qE 'Added [1-9][0-9]* new commands'; } \
    || die "idle-backlog-cpu aborted - jobs not added (manager up?)"
  echo "ready backlog: $(osunset; timeout 60 "$WR" status --deployment development -i rgidle -o counts 2>/dev/null | tr '\n' ' ')"
  sleep 5

  echo 0 > "$pcount"
  ib_rac_driver "$trig" "$pcount" &
  local driver_pid=$!
  echo "rac-cycle driver pid $driver_pid (one extra blocked job every ${trig}s => one rac cycle each)"

  echo "=== sampling ${secs}s CPU profile of the manager ==="
  timeout $((secs + 90)) go tool pprof -top -cum -nodecount=25 \
    "http://localhost:$pprof/debug/pprof/profile?seconds=$secs" > "$ptxt" 2>&1
  kill "$driver_pid" 2>/dev/null; wait "$driver_pid" 2>/dev/null

  local totms totpct snapline snapms snapshare
  totms=$(grep -aoE 'Total samples = [0-9.]+(ms|s|mins)' "$ptxt" | head -1 | awk '{print $4}' | ib_ms)
  totpct=$(grep -aoE 'Total samples =.*\( *[0-9.]+%\)' "$ptxt" | grep -aoE '[0-9.]+%' | head -1)
  snapline=$(grep -aE '\(\*Job\)\.schedulerGroupSnapshot$' "$ptxt" | head -1)
  snapms=$(echo "$snapline" | awk '{print $4}' | ib_ms)
  snapshare=$(echo "$snapline" | awk '{print $5}')
  echo "## profile top (also saved to $ptxt):"
  grep -aE 'Duration:|Total samples|schedulerGroupSnapshot|^ +[0-9]' "$ptxt" | head -12
  local cycles; cycles=$(cat "$pcount" 2>/dev/null); cycles=${cycles:-0}
  case "$cycles" in (*[!0-9]*|'') cycles=0 ;; esac  # unreadable count == not measured, see below
  echo "## VERDICT: totalCPU=${totms}ms per ${secs}s (${totpct:-?} of one core)"
  echo "##          schedulerGroupSnapshot=${snapms}ms (${snapshare:-0%} of that total)"
  if [ "$cycles" -gt 0 ]; then
    echo "##          $cycles rac cycles over $n ready jobs => $((totms / cycles))ms manager CPU per cycle," \
         "$((snapms / cycles))ms of it in the snapshot"
  fi
  # a gate that PASSES when the measurement is missing is worse than no gate, so a
  # profile without a Duration: line (pprof fetch failed, or the manager died), an
  # unparseable/zero total, or a driver that never fired a cycle (so the pre-pass was
  # never exercised) are all hard FAILURES rather than "0ms, PASS".
  local verdict=0 unmeasured=""
  grep -qaE '^Duration:' "$ptxt" \
    || unmeasured="the profile has no 'Duration:' line, so no CPU profile was taken (manager died, or no pprof on localhost:$pprof?)"
  [ -z "$unmeasured" ] && [ "$totms" -le 0 ] \
    && unmeasured="could not read a 'Total samples = <n><unit>' figure from the profile (truncated profile, or zero samples)"
  [ -z "$unmeasured" ] && [ "$cycles" -le 0 ] \
    && unmeasured="the rac-cycle driver fired 0 cycles, so the O(backlog) pre-pass was never exercised"
  if [ -n "$unmeasured" ]; then
    verdict=1
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $ptxt"
  elif [ "$totms" -gt "$maxms" ] || [ "$snapms" -gt "$maxsnapms" ]; then
    verdict=1
    echo "FAIL: the idle backlog is burning CPU again (total >${maxms}ms or snapshot >${maxsnapms}ms per ${secs}s)"
    echo "  => the per-ready-job scheduler-group derivation is no longer memoised (see jobDerived in job.go)"
  else
    echo "PASS: an idle $n-job limit-blocked backlog costs ~no CPU; the derivation is memoised"
  fi
  echo "## CLEANUP"
  cmd_stop >/dev/null 2>&1
  rm -f "$WRDEV_ROOT/idlejobs.json" "$WRDEV_ROOT/idledriver.json" 2>/dev/null
  return "$verdict"
}

# ib_ms reads a pprof duration (eg. 160ms, 1.20s, 2.5mins) on stdin and prints it as
# whole milliseconds, 0 if there was nothing to read (which its caller must treat as an
# unmeasured FAIL, never as a cheap PASS).
ib_ms() {
  awk '{v=$1
    if (v ~ /mins$/) {sub(/mins$/,"",v); printf "%d", v*60000}
    else if (v ~ /ms$/) {sub(/ms$/,"",v); printf "%d", v}
    else if (v ~ /s$/) {sub(/s$/,"",v); printf "%d", v*1000}
    else {printf "0"}
    exit}
    END{if (NR==0) printf "0"}'
}

# ib_rac_driver keeps rac cycles firing for the whole profile window by adding one more
# job to the SAME limit-0 group every triggerSeconds: entering ready fires
# the ready-added callback (one full pre-pass over the backlog) while the job itself stays
# blocked, so it can never run and never launches a runner. It records how many triggers
# (== rac cycles) it has fired in cycleCountFile, so the CPU can be reported per cycle.
ib_rac_driver() {  # <triggerSeconds> <cycleCountFile>
  local trig="$1" pcount="$2" i=0
  while true; do
    i=$((i + 1))
    printf '{"cmd":"sleep 1 #driver-%d","queue":"%s","memory":"500M"}\n' "$i" "$QUEUE" \
      > "$WRDEV_ROOT/idledriver.json"
    timeout 60 "$WR" add -f "$WRDEV_ROOT/idledriver.json" --rep_grp rgidledriver \
      --limit_grps "idlelimit:0" --retries 0 --deployment development >/dev/null 2>&1
    echo "$i" > "$pcount"
    sleep "$trig"
  done
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
  echo "NOTE: our prod-mode LSF runners are ${PROD_JOB_PREFIX}* (WR_JOBNAME_TOKEN=$PROD_JOBTOKEN);"
  echo "      that prefix can NEVER match a real deployment's wrp_*, so it is safe to bkill by pattern."
  osunset ; WR_JOBNAME_TOKEN="$PROD_JOBTOKEN" timeout 90 "$WR" manager start --deployment production -s "$sched" 2>&1 \
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
  local jid; jid=$(timeout 40 bjobs -o 'jobid job_name stat' -noheader 2>/dev/null | awk -v p="$PROD_JOB_PREFIX" 'index($2,p)==1 && $3=="RUN"{print $1; exit}')
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
  idle-backlog-cpu [jobs] [seconds] [pprofPort]
                        reliable4 FINDING 3 SCALE GATE: N jobs in ONE limit group set to 0 (all
                        ready-but-blocked, nothing schedulable, NO runners => farm-safe) on an
                        isolated dev manager with pprof ON; a driver keeps rac cycles firing while
                        a real CPU profile is taken, and it reports the manager's total CPU and the
                        cumulative CPU in Job.schedulerGroupSnapshot (defaults 50000 25 6063).
                        Prod pre-fix: 19640ms per 25s (0.79 cores), 8210ms (41.8%) of it in the
                        snapshot; empty-backlog floor 80ms per 25s. PASS = both stay near that
                        floor (thresholds WRDEV_IDLE_MAX_MS / WRDEV_IDLE_MAX_SNAP_MS); FAIL = the
                        O(backlog) MD5+sort+allocate derivation is back, OR the run produced no
                        measurement to judge (no profile, no samples, no rac cycles).
  runner-started-timeout-check
                        reliable4 #3 reproducer: a transient post-exec Started() RPC timeout
                        kills a healthy running command; fails until Started() tolerates it
  ttrmiss-check [jobs] [runners] [archiveDelayMs]
                        reliable4 in-process TTR-miss archive-reject churn: a starved/dead runner's
                        late success is rejected + re-run (knobs WR_TTRMISS_TOUCH / _RUNNER_DEAD)
  writestorm-freeze [N] [archivers]
                        reliable4 FULL prod-freeze A/B repro (in-process, SAFE): fires an N-job
                        updateJobAfterChange storm on a big freelist DB (WR_WSFREEZE_DB / WRDEV_PRISTINE_DB)
                        and times db.archiveJob. Pre-fix a synchronous archive is starved past the 60s
                        client floor (freeze->churn) + goroutines explode ~=N; post-fix bounded + under
                        the floor (defaults 100000 8). Confirmed pre-fix 1m13s @ N=100k on pristine10.
  archive-rate [archivers] [seconds] [thinkMs]
                        reliable4 FINDING 2 SCALE GATE (in-process, SAFE - no LSF/manager/commands):
                        N concurrent "runners" think-then-synchronously-archive on a COPY of a big
                        freelist-bloated DB (WR_ARCHRATE_DB / WRDEV_PRISTINE_DB) opened via the real
                        initDB, so the arrival rate outruns the transaction rate exactly as production
                        did (defaults 660 180 3800). Reports archive throughput, mean/p50/p99/max
                        latency and queue depth. Prod pre-fix: queue ~600 deep, ~12/s, mean block
                        43000ms, tail over the 60s client floor. PASS = mean <= 5000ms, p99 <= 60000ms
                        and nothing over the floor (WRDEV_ARCHRATE_MAX_MEAN_MS / _MAX_P99_MS); FAIL =
                        the archives queue on the one write lock again, OR nothing was measured, OR
                        the reproducer itself failed (non-zero go test exit / any archive error).
  confirm-dead-leak [checks] [host]
                        reliable4 Fix 5 repro (real LSF + ssh): drives ProcessNotRunningOnHost N times
                        and counts leaked ssh-client goroutines; each check dials a client that is never
                        closed (defaults 40 localhost). Needs LSF + passwordless ssh to host; skips else.
  report-storm [jobs] [runners] [limit] [seconds]
                        reliable4 FAITHFUL in-process report-storm LOAD repro: N fast jobs behind one
                        limit group + M real runner goroutines; classifies every report RPC (defaults
                        5000 200 2000 120). Set WR_RS_DB=<bigdb> for the archive-commit/backup confound.
  report-storm-profile [jobs] [runners] [limit] [seconds]
                        as report-storm but with the CPU+mutex+block profiler ON (pprof files to
                        WRDEV_ROOT) to PIN the serialization point (defaults 50000 1000 2000 240)
  report-storm-lsf [jobs] [limit] [runsec]
                        reliable4 FAITHFUL LSF-scale report-storm CHURN repro: isolated PROD-mode
                        manager (backups ON) on a big DB copy + N fast jobs in one limit group; real
                        LSF runners (distinct pids) + backup stall crossing 60s => the discard+rerun
                        spiral (defaults 100000 2000 1). REQUIRES WRDEV_PRISTINE_DB=<big DB>. Safe:
                        its LSF jobs are namespaced (never a real wrp_*). WRDEV_DEBUG=1 / WR_RS_PADKB /
                        WR_RS_PPROF=<port> (profile the real manager) / WR_RS_BKDIR=<dir on another FS,
                        e.g. Lustre> (back up to a separate filesystem so it can't starve the DB's I/O).
  unsuspend-burst [jobs] [pprofPort]
                        reliable4 FAITHFUL PROD-FREEZE repro (write-storm root cause): N jobs in ONE
                        limit group set to 0 (ready-but-blocked; NEVER run => 0 LSF load) on an
                        isolated PROD-mode manager (backups+pprof ON) on a big freelist-bloated DB copy;
                        mass-suspend to stage, then a single `wr resume` un-suspends all N at once =>
                        the unbounded per-change `go db.bolt.Batch` storm. An embedded goroutine
                        classifier reports the freeze signature (bw Batch-blocked / bwmax / in_commit /
                        total) + control-RPC latency (defaults 100000 6062). REQUIRES
                        WRDEV_PRISTINE_DB=<big DB> (pristine10, or .../prod.db). Post-fix gate: bw stays
                        low, no bwmax growth, status stays responsive.
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
  idle-backlog-cpu) cmd_idle_backlog_cpu "${2:-50000}" "${3:-25}" "${4:-6063}" ;;
  runner-started-timeout-check) cmd_runner_started_timeout_check ;;
  ttrmiss-check) cmd_ttrmiss_check "${2:-60}" "${3:-20}" "${4:-1500}" ;;
  archive-rate) cmd_archive_rate "${2:-660}" "${3:-180}" "${4:-3800}" ;;
  confirm-dead-leak) cmd_confirm_dead_leak "${2:-40}" "${3:-localhost}" ;;
  writestorm-freeze) cmd_writestorm_freeze "${2:-100000}" "${3:-8}" ;;
  report-storm) cmd_report_storm "${2:-5000}" "${3:-200}" "${4:-2000}" "${5:-120}" ;;
  report-storm-profile) cmd_report_storm_profile "${2:-50000}" "${3:-1000}" "${4:-2000}" "${5:-240}" ;;
  report-storm-lsf) cmd_report_storm_lsf "${2:-100000}" "${3:-2000}" "${4:-1}" ;;
  unsuspend-burst) cmd_unsuspend_burst "${2:-100000}" "${3:-6062}" ;;
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
