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
  # WRDEV_PRISTINE_DB) + RAM for N goroutines; each run COPIES it into $WRDEV_ROOT (which
  # needs room for it) and mutates only that copy, removed again below.
  # To A/B the fix itself, run this in a pre-fix `git worktree` vs the fixed tree.
  need_repo
  local n="${1:-100000}" archivers="${2:-8}"
  local db="${WR_WSFREEZE_DB:-${WRDEV_PRISTINE_DB:-}}"
  { [ -n "$db" ] && [ -f "$db" ]; } \
    || die "set WR_WSFREEZE_DB (or WRDEV_PRISTINE_DB) to a big freelist DB (see backup-stall-check / TestReliable4InflateDB)"
  local work="$WRDEV_ROOT/wsfreeze_work_db" rc=0
  mkdir -p "$WRDEV_ROOT"
  echo "in-process write-storm freeze: N=$n archivers=$archivers"
  echo "  DB $db ($(ls -la "$db" | awk '{print $5}') bytes; COPIED to $work, never mutated in place)"
  osunset
  WRDEV_ROOT="$WRDEV_ROOT" WR_WSFREEZE_DB="$db" WR_WSFREEZE_N="$n" WR_WSFREEZE_ARCHIVERS="$archivers" \
    timeout 2400 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4WriteStormFreeze -count=1 -v -timeout 39m 2>&1 \
    | grep -aE 'WSFREEZE|FREEZE|PASS|FAIL|panic|^ok |^---' | grep -avE 'no test files' || rc=$?
  # the Go test removes its own copy, but only if it lives to run its cleanup: a
  # timeout-killed or interrupted run would otherwise leave the whole DB behind.
  rm -f "$work" "${work}_bk" 2>/dev/null
  return "$rc"
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
  # backup-stall-check / TestReliable4InflateDB); each run COPIES it into $WRDEV_ROOT (which
  # needs room for it) and mutates only that copy, removed again below.
  # A/B the fix by running this in a pre-fix `git worktree` vs the fixed tree.
  need_repo
  local archivers="${1:-660}" secs="${2:-180}" thinkms="${3:-3800}"
  local maxmeanms="${WRDEV_ARCHRATE_MAX_MEAN_MS:-5000}" maxp99ms="${WRDEV_ARCHRATE_MAX_P99_MS:-60000}"
  local db="${WR_ARCHRATE_DB:-${WRDEV_PRISTINE_DB:-}}" gorc=0
  { [ -n "$db" ] && [ -f "$db" ]; } \
    || die "set WR_ARCHRATE_DB (or WRDEV_PRISTINE_DB) to a big freelist DB (see backup-stall-check / TestReliable4InflateDB)"
  local out="$WRDEV_ROOT/archive-rate.out" work="$WRDEV_ROOT/archrate_work_db"
  mkdir -p "$WRDEV_ROOT"
  echo "reliable4 FINDING 2 archive-rate gate: $archivers archivers, ${thinkms}ms think time, ${secs}s window"
  echo "  DB $db ($(ls -la "$db" | awk '{print $5}') bytes; COPIED to $work, never mutated in place)"
  echo "  prod pre-fix numbers to beat: queue ~600 deep, ~12 archives/s, mean block 43000ms, tail >60000ms"
  echo "  gate: mean <= ${maxmeanms}ms, p99 <= ${maxp99ms}ms, 0 archives over the 60s client floor"
  osunset
  WRDEV_ROOT="$WRDEV_ROOT" WR_ARCHRATE_DB="$db" WR_ARCHRATE_ARCHIVERS="$archivers" \
    WR_ARCHRATE_SECONDS="$secs" WR_ARCHRATE_THINK_MS="$thinkms" \
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
  rm -f "$work" "${work}_bk" 2>/dev/null
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
  #   WR_RS_DB=/nfs/hgi/wr/sb10-bigdb/pristine10  (serve() opens a mutable COPY under WRDEV_ROOT,
  #                                                 removed again below; needs ~2x its size there)
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
  local work="$WRDEV_ROOT/reliable4_reportstorm_work_db" rc=0
  echo "in-process report-storm: jobs=$jobs runners=$runners limit=$limit seconds=$secs bigDB=${WR_RS_DB:-none}"
  echo "  knobs: WR_RS_TTR_MS=${WR_RS_TTR_MS:-60000} WR_RS_CMD_MS=${WR_RS_CMD_MS:-0} WRDEV_ROOT=$WRDEV_ROOT"
  echo "  status_pollers=${WR_RS_STATUS:-0} (interval ${WR_RS_STATUS_MS:-500}ms) profile=${WR_RS_PROFILE_DIR:-off}"
  [ -n "${WR_RS_DB:-}" ] && echo "  big DB ${WR_RS_DB} COPIED to $work, never mutated in place"
  mkdir -p "$WRDEV_ROOT"
  osunset
  WRDEV_ROOT="$WRDEV_ROOT" \
  WR_RS_JOBS="$jobs" WR_RS_RUNNERS="$runners" WR_RS_LIMIT="$limit" WR_RS_SECONDS="$secs" \
  WR_RS_DB="${WR_RS_DB:-}" WR_RS_PROFILE_DIR="${WR_RS_PROFILE_DIR:-}" \
  WR_RS_STATUS="${WR_RS_STATUS:-0}" WR_RS_STATUS_MS="${WR_RS_STATUS_MS:-500}" \
    timeout $((secs + 600)) go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4ReportStorm -count=1 -v -timeout $((secs + 540))s 2>&1 \
    | grep -aE 'REPORTSTORM|PASS|FAIL|panic|^ok ' | grep -avE 'no test files' || rc=$?
  # the Go test removes its own copy (and its backups), but only if it lives to run
  # its cleanup: a timeout-killed or interrupted run would otherwise leave the whole
  # DB behind.
  rm -f "$work" "${work}_bk" "${work}_bk.tmp" 2>/dev/null
  return "$rc"
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

cmd_status_seed_overlap() {  # status-seed-overlap [overlap] [natBacklog] - reliable4 FINDING 7 web count divergence
  # Deterministic, in-process reproducer/gate (build-tagged reliability_repro, NOT part
  # of make test) for reliable4 FINDING 7 (.docs/reliable4/prod-run-20260817.md): the web
  # status page showed 274 running when 4 were running, and only a page refresh fixed it.
  #
  # NOT a dropped delta and NOT a missing one. The scan-on-connect seed and the live
  # delta feed are not a consistent cut: setupUpdateListener joins the never-drop
  # statusCaster BEFORE the client's "current" request can arrive, and
  # sendCurrentStatusCounts then snapshots the queue, so every transition emitted in
  # that window is reported TWICE - once as its own from->to delta and once by the seed,
  # which already shows the job in its destination state. The client's occupancy model
  # cannot spot the duplicate (deltas are anonymous counts, not job identities), so one
  # unit of occupancy moves permanently from the source bucket to the destination one.
  # During a ramp-up that inflates `running`; the later limit->0 mass exit does not cause
  # it, it just makes it glaring because the true running count falls to a handful while
  # the offset stays. Only a reconnect (which re-seeds and clears the model) corrects it -
  # the operator's page refresh.
  #
  # The fix brackets the seed with jstatusSeedBoundary markers, written under the
  # connection's write mutex so nothing interleaves them, and the client resets to the
  # seed on the "begin" marker. Two shapes are run, both replaying the RECORDED wire
  # stream through the REAL websocket-handler.js
  # (jobqueue/testdata/status-count-reconcile/replay-stream.mjs), so "the web UI would
  # show N running" is the shipped client's own answer:
  #   forced  - the DISCRIMINATING shape. The interleaving is forced (dial, prove the
  #             caster member is live, run the transitions, let their deltas be
  #             delivered, THEN send "current"), so the magnitude is exact and the gate
  #             never flakes. Pre-fix it over-counts `running` by the whole overlap set.
  #   natural - the RESIDUAL MEASUREMENT. "current" is sent immediately on open exactly
  #             as the browser does, while jobs keep starting. In-process the
  #             pre-snapshot part of the window (the request hop, plus any delta the
  #             caster had not written yet) is microseconds, so what is left is the seed
  #             walk itself, which no boundary can close without locking the queue across
  #             it (DEVELOPERS.md rule 1). It replays the same recording twice - as the
  #             shipped client reads it and with the markers stripped, which is what an
  #             older status page sees - and asserts only that the bracket is present,
  #             that nothing interleaves it (both RED pre-fix) and that the boundary
  #             never makes the residual worse. It PRINTS the residual and the seed
  #             walk that bounds it.
  need_repo
  command -v node >/dev/null 2>&1 || die "node is required for status-seed-overlap"
  local overlap="${1:-120}" natbacklog="${2:-20000}"
  echo "reliable4 FINDING 7 web status count divergence (deterministic, in-process):"
  echo "the running bar a NEVER-RECONNECTING status client shows must equal the truth."
  echo "scale: forced overlap=$overlap, natural backlog=$natbacklog (defaults 120 / 20000)"
  osunset
  local out rc=0
  out=$(WR_SO_OVERLAP="$overlap" WR_SO_NAT_BACKLOG="$natbacklog" \
    timeout 600 go -C "$REPO" test -tags "netgo reliability_repro" ./jobqueue/ \
    -run 'TestReliable4StatusSeedOverlap' -count=1 -v 2>&1) || rc=$?
  printf '%s\n' "$out" | grep -aE 'SEED-OVERLAP-REPRO|--- (PASS|FAIL)|^(ok|FAIL)'

  # hard FAIL (NOT MEASURED) if either shape produced no measurement: a gate that
  # passes when the measurement is absent is worse than no gate.
  local forced natural bracket overcounts
  forced=$(printf '%s\n' "$out" | grep -aoE 'SEED-OVERLAP-REPRO forced true_running=[0-9]+ shown_running=[0-9]+' | tail -1)
  natural=$(printf '%s\n' "$out" | grep -aoE 'SEED-OVERLAP-REPRO natural ramp_started=[0-9]+ true_running=[0-9]+ shown_running=[0-9]+' | tail -1)
  bracket=$(printf '%s\n' "$out" | grep -aoE 'natural bracket=[0-9]+/[0-9]+ queue=[0-9]+ seedwalk_ms=[0-9.]+ jobswalk_ms=[0-9.]+ starts_per_s=[0-9]+ residual_predicted=[0-9.]+' | tail -1)
  overcounts=$(printf '%s\n' "$out" | grep -aoE 'overcount_boundary_aware=[0-9-]+ overcount_boundary_blind=[0-9-]+' | tail -1)
  if [ -z "$forced" ]; then
    echo "status-seed-overlap: FAIL (NOT MEASURED - the forced shape printed no comparison, so"
    echo "  it never reached it. Is node present? did the server start? see the output above)"
    return 1
  fi

  local ftrue fshown ntrue nshown nramp begins ends seedwalk starts predicted aware blind
  ftrue=$(printf '%s' "$forced" | sed -E 's/.*true_running=([0-9]+).*/\1/')
  fshown=$(printf '%s' "$forced" | sed -E 's/.*shown_running=([0-9]+).*/\1/')

  # the discriminating comparison, reported before anything else so a pre-fix run says
  # what actually went wrong rather than blaming the missing residual measurement (the
  # natural shape aborts at its own bracket assertion pre-fix, and prints nothing).
  if [ "$fshown" -ne "$ftrue" ]; then
    echo "status-seed-overlap: forced  true_running=$ftrue shown_running=$fshown"
    echo "status-seed-overlap: FAIL (seed/delta overlap double-counts: running over-counted by"
    echo "  $((fshown - ftrue)) in the forced shape, on a client that never reconnected)"
    return "${rc:-1}"
  fi

  if [ -z "$natural" ] || [ -z "$bracket" ] || [ -z "$overcounts" ]; then
    echo "status-seed-overlap: FAIL (NOT MEASURED - a natural, bracket or overcount measurement"
    echo "  line is missing, so the residual was never measured; see the output above)"
    return 1
  fi
  nramp=$(printf '%s' "$natural" | sed -E 's/.*ramp_started=([0-9]+).*/\1/')
  ntrue=$(printf '%s' "$natural" | sed -E 's/.*true_running=([0-9]+).*/\1/')
  nshown=$(printf '%s' "$natural" | sed -E 's/.*shown_running=([0-9]+).*/\1/')
  begins=$(printf '%s' "$bracket" | sed -E 's|.*bracket=([0-9]+)/[0-9]+.*|\1|')
  ends=$(printf '%s' "$bracket" | sed -E 's|.*bracket=[0-9]+/([0-9]+).*|\1|')
  seedwalk=$(printf '%s' "$bracket" | sed -E 's/.*seedwalk_ms=([0-9.]+).*/\1/')
  starts=$(printf '%s' "$bracket" | sed -E 's/.*starts_per_s=([0-9]+).*/\1/')
  predicted=$(printf '%s' "$bracket" | sed -E 's/.*residual_predicted=([0-9.]+).*/\1/')
  aware=$(printf '%s' "$overcounts" | sed -E 's/.*aware=([0-9-]+).*/\1/')
  blind=$(printf '%s' "$overcounts" | sed -E 's/.*blind=([0-9-]+).*/\1/')
  echo "status-seed-overlap: forced  true_running=$ftrue shown_running=$fshown (the discriminating shape)"
  echo "status-seed-overlap: natural true_running=$ntrue shown_running=$nshown (ramp started $nramp)"
  echo "status-seed-overlap: natural bracket=$begins/$ends seed_walk=${seedwalk}ms at ${starts} starts/s"
  echo "status-seed-overlap:   => ACCEPTED RESIDUAL: the seed walk is not a point in time, so up to"
  echo "status-seed-overlap:      ~$predicted transitions can be counted twice (measured: $aware; a"
  echo "status-seed-overlap:      boundary-blind client on the same recording: $blind)"

  # a natural run that started no jobs measured nothing either.
  if [ "$nramp" -le 0 ]; then
    echo "status-seed-overlap: FAIL (NOT MEASURED - the natural ramp started 0 jobs)"
    return 1
  fi

  if [ "$begins" -ne 1 ] || [ "$ends" -ne 1 ]; then
    echo "status-seed-overlap: FAIL (the seed is not bracketed: begin=$begins end=$ends, expected 1/1,"
    echo "  so the client has no way to tell what predates the seed)"
    return 1
  fi

  if [ "$aware" -gt "$blind" ]; then
    echo "status-seed-overlap: FAIL (the boundary made the residual WORSE: $aware vs $blind blind)"
    return 1
  fi

  # the go test itself must have passed too: it asserts more than this gate parses
  # (the bracket's ordering, that nothing was dropped, and the ready bar as well as
  # the running one).
  if [ "$rc" -ne 0 ]; then
    echo "status-seed-overlap: FAIL (the reproducer's own assertions failed; see above)"
    return "$rc"
  fi

  echo "status-seed-overlap: PASS (the seed no longer double-counts what predates it; the residual"
  echo "  above is the seed walk itself, which is bounded by the walk and not by the connect window)"
  return 0
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

cmd_overcount_check() {  # overcount-check [limit] [initialRunning] [windowReserves] - reliable3 2b over-count GATE
  # Deterministic, in-process REGRESSION GATE (build-tagged reliability_repro, NOT part of
  # make test) for reliable3 ISSUE 2b: a scheduler group's final scheduling count must NEVER
  # exceed its limit group's limit. countJobInGroup caps the READY count against an EARLY
  # capacity read, and accountForRunningJobs then adds ALL running jobs on top, so reserves
  # landing in that non-atomic window inflate the count (production saw 3313 for a 2000
  # limit); capGroupCountsToLimits trims the summed sibling counts back to the limit.
  #
  # It USED TO BE INVERTED - it asserted the over-count was PRESENT, so it passed on the buggy
  # code and started failing the moment the cap landed, ie. its exit code came to mean the
  # opposite of what any reader assumes. It now asserts the invariant, and asserts BOTH halves
  # so it cannot pass vacuously: that the arrangement really does over-count BEFORE the cap
  # (uncappedCount > limit; otherwise the cap is being credited for nothing) and that the
  # capped count is at or under the limit while STILL asking for runners (an upper bound alone
  # would be satisfied by a cap that trimmed everything to zero).
  #
  # Gate: uncappedCount > limit AND 0 < finalCount <= limit. FAIL = the cap is gone or has
  # started trimming to nothing, or the arrangement produced no over-count to cap. Deleting
  # the capGroupCountsToLimits call at the end of accountForRunningJobs turns it red.
  # A MISSING measurement (no OVERCOUNT-REPRO line) is a hard FAIL, never a cheap PASS.
  need_repo
  local limit="${1:-2000}" initial="${2:-300}" window="${3:-1500}"
  echo "reliable3 2b over-count gate (deterministic, in-process): a group's summed count must stay"
  echo "within its limit group's limit even when reserves land between the early capacity read and"
  echo "the later running snapshot."
  echo "scale: limit=$limit initialRunning=$initial windowReserves=$window"
  echo "Gate: uncappedCount > $limit (the arrangement really over-counts) AND 0 < finalCount <= $limit."
  echo "prod pre-fix: count=3313 for a 2000 limit. A MISSING measurement is a FAIL, not a PASS."
  osunset
  local out rc=0
  out=$(WR_OP_LIMIT="$limit" WR_RC_INITIAL="$initial" WR_RC_WINDOW="$window" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable3OverCountRunningSnapshot -count=1 -v 2>&1) || rc=$?
  printf '%s\n' "$out" | grep -aE 'OVERCOUNT-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'

  local repro uncapped final
  repro=$(printf '%s\n' "$out" | grep -aoE 'OVERCOUNT-REPRO: .*' | tail -1)
  uncapped=$(bk_num "$repro" uncappedCount); final=$(bk_num "$repro" finalCount)
  for v in uncapped final; do
    case "${!v}" in (*[!0-9-]*|'') eval "$v=-1" ;; esac
  done

  echo "## VERDICT: limit=$limit uncappedCount=$uncapped finalCount=$final"
  if [ -z "$repro" ] || [ "$uncapped" -lt 0 ] || [ "$final" -lt 0 ]; then
    echo "FAIL (NOT MEASURED): no usable OVERCOUNT-REPRO line, so neither count was measured"
    echo "  => this gate only reports PASS on a real measurement; see the test output above"
    return 1
  fi
  if [ "$uncapped" -le "$limit" ]; then
    echo "FAIL (NOT DISCRIMINATING): the uncapped count was $uncapped, not over the $limit limit, so"
    echo "  there was no over-count for the cap to remove and a low finalCount proves nothing"
    echo "  => raise WR_RC_WINDOW / lower WR_RC_INITIAL so reserves really do land in the window"
    return 1
  fi
  if [ "$final" -gt "$limit" ]; then
    echo "FAIL: the group's count is $final, over its $limit limit (uncapped it was $uncapped)"
    echo "  => the summed sibling counts per limit group must be trimmed back to the limit"
    echo "     (capGroupCountsToLimits / trimGroupsToLimit, called from accountForRunningJobs)"
    return 1
  fi
  if [ "$final" -le 0 ]; then
    echo "FAIL: the count was trimmed to $final, so no runners are requested for work that fits"
    echo "  => trimGroupsToLimit must remove only the overage, not the whole request"
    return 1
  fi
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the reproducer's own assertions failed (rc=$rc) even though the counts are in bounds"
    return "$rc"
  fi
  echo "PASS: an arrangement that reaches $uncapped uncapped is held at $final, within the $limit limit,"
  echo "      and still requests runners for the work that fits"
  return 0
}

cmd_limit_stall_check() {  # limit-stall-check [limit] [ready] - reliable3 §1 limit-slot + loud-confirm GATE
  # Deterministic, in-process REGRESSION GATE (build-tagged reliability_repro, NOT part of
  # make test) for the two halves of reliable3 ISSUE 1:
  #
  #   (1) LIMIT SLOTS: a limit group whose slots are ALL held schedules nothing more - every
  #       new ready job is limit-skipped, not scheduled. That is correct behaviour and always
  #       was, so this half's assertions are unchanged; what production suffered was where the
  #       held slots came from (phantom slots: reserved-then-lost jobs whose slots were never
  #       released because death confirmation never succeeded), which this half does not
  #       exercise - it holds the slots by incrementing the limiter directly.
  #   (2) LOUD CONFIRMATION: the confirmation path used to fail SILENTLY, so a manager whose
  #       reclaim was completely broken looked healthy and the stall in (1) had no visible
  #       cause. ProcessNotRunningOnHost must WARN when it cannot determine whether the process
  #       is alive (it still answers "maybe running", which is the safe answer - a job is only
  #       re-run once its runner is CONFIRMED dead), and lsf.initialize must WARN when the
  #       configured ssh private key cannot be read instead of swallowing the error.
  #
  # Half (2) WAS INVERTED - it asserted the ABSENCE of any log, so it passed on the silent code
  # and started failing once the warnings landed. It now asserts the warnings are present AND
  # that they name what an operator has to act on (the host and pid; the key path), since a bare
  # "something went wrong" leaves the operator where the silence did. Removing either warn turns
  # this red.
  #
  # Gate: scheduled == 0 AND skipped == ready, AND the cannot-confirm warning is present and
  # names the host and pid, AND (where LSF is usable) the key warning is present and names the
  # path. The key facet needs a working LSF, so it SKIPS elsewhere; the verdict says which
  # facets were measured rather than counting a skip as coverage.
  # A MISSING measurement (no LIMIT-STALL-REPRO or CONFIRM-LOUD-REPRO line) is a hard FAIL.
  need_repo
  local limit="${1:-2000}" ready="${2:-5000}"
  echo "reliable3 §1 limit-slot + loud-confirmation gate (deterministic, in-process)."
  echo "scale: limit=$limit heldSlots=$limit newReady=$ready (all new ready jobs must be skipped)."
  echo "Gate: scheduled == 0 AND skipped == $ready; the cannot-confirm warning present, naming the"
  echo "host and pid; and where LSF is usable, the unreadable-private-key warning naming the path."
  echo "A MISSING measurement is a FAIL, not a PASS; a skipped facet is reported as unmeasured."
  osunset
  local out rc=0
  out=$(WR_OP_LIMIT="$limit" WR_STALL_READY="$ready" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run 'TestReliable3LimitSlotStall|TestReliable3ConfirmFailureIsLoud' -count=1 -v 2>&1) || rc=$?
  printf '%s\n' "$out" \
    | grep -aE 'LIMIT-STALL-REPRO|CONFIRM-LOUD-REPRO|KEY-WARN-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'

  local stall confirm keyline scheduled skipped
  stall=$(printf '%s\n' "$out" | grep -aoE 'LIMIT-STALL-REPRO: .*' | tail -1)
  confirm=$(printf '%s\n' "$out" | grep -aoE 'CONFIRM-LOUD-REPRO: .*' | tail -1)
  keyline=$(printf '%s\n' "$out" | grep -aoE 'KEY-WARN-REPRO: .*' | tail -1)
  scheduled=$(bk_num "$stall" scheduled); skipped=$(bk_num "$stall" skipped)
  for v in scheduled skipped; do
    case "${!v}" in (*[!0-9-]*|'') eval "$v=-1" ;; esac
  done

  local keymeasured="no" keyverdict="not measured (no usable LSF on this host)"
  if printf '%s\n' "$keyline" | grep -aq 'keyWarnMeasured=true'; then
    keymeasured="yes"
    keyverdict="warned=$(ls_flag "$keyline" keyWarned) namesPath=$(ls_flag "$keyline" namesPath)"
  fi

  echo "## VERDICT: scheduled=$scheduled skipped=$skipped/$ready;" \
       "cannotConfirmWarned=$(ls_flag "$confirm" cannotConfirmWarned)" \
       "namesHost=$(ls_flag "$confirm" namesHost) namesPid=$(ls_flag "$confirm" namesPid);" \
       "keyFacetMeasured=$keymeasured ($keyverdict)"

  if [ -z "$stall" ] || [ "$scheduled" -lt 0 ] || [ "$skipped" -lt 0 ]; then
    echo "FAIL (NOT MEASURED): no usable LIMIT-STALL-REPRO line, so the limit-slot half never ran"
    echo "  => this gate only reports PASS on a real measurement; see the test output above"
    return 1
  fi
  if [ -z "$confirm" ]; then
    echo "FAIL (NOT MEASURED): no CONFIRM-LOUD-REPRO line, so the confirmation half never ran"
    echo "  => this gate only reports PASS on a real measurement; see the test output above"
    return 1
  fi
  if [ "$scheduled" -ne 0 ] || [ "$skipped" -ne "$ready" ]; then
    echo "FAIL: a limit group with every slot held scheduled $scheduled jobs and skipped $skipped"
    echo "  of $ready (want 0 and $ready): the limiter is no longer the source of truth for slot"
    echo "  occupancy, which is the over-provisioning family (see overprovision-check)"
    return 1
  fi
  local flag
  for flag in cannotConfirmWarned namesHost namesPid; do
    if [ "$(ls_flag "$confirm" "$flag")" != "true" ]; then
      echo "FAIL: the cannot-confirm outcome is silent again ($flag=false)"
      echo "  => a death confirmation that cannot succeed must WARN, naming the host and pid"
      echo "     (Scheduler.warnCannotConfirm, called from ProcessNotRunningOnHost)"
      return 1
    fi
  done
  if [ "$keymeasured" = "yes" ]; then
    for flag in keyWarned namesPath; do
      if [ "$(ls_flag "$keyline" "$flag")" != "true" ]; then
        echo "FAIL: an unreadable ssh private key is swallowed again ($flag=false)"
        echo "  => lsf.loadPrivateKey must WARN and name the path, or ssh (and so every death"
        echo "     confirmation) is permanently broken with nothing said"
        return 1
      fi
    done
  fi
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the reproducer's own assertions failed (rc=$rc) even though the parsed"
    echo "  measurements are within bounds; see the output above"
    return "$rc"
  fi
  echo "PASS: $ready ready jobs behind a fully-held $limit limit group were all skipped and none"
  echo "      scheduled, and the cannot-confirm warning named its host and pid"
  [ "$keymeasured" = "yes" ] \
    && echo "      (and the unreadable private key warned, naming its path)" \
    || echo "      (the private-key facet was NOT measured here: no usable LSF, so that warn is unproven)"
  return 0
}

# ls_flag prints the true/false value of a `key=true|false` field in one of the
# limit-stall-check reproducer lines, or "unmeasured" when the field is absent (which its
# caller must treat as a failure, never as a cheap PASS).
ls_flag() {  # <line> <key>
  local v
  v=$(printf '%s\n' "$1" | grep -aoE "$2=(true|false)" | tail -1 | cut -d= -f2)
  case "$v" in (true|false) echo "$v" ;; (*) echo "unmeasured" ;; esac
}

cmd_priority_fairness_check() {  # priority-fairness-check [limit] [readyExtra] - reliable3 2a fairness (INVERTED)
  # Deterministic, in-process reproducer (build-tagged reliability_repro, NOT part of make
  # test) for reliable3 ISSUE 2a's refinement, which is STILL AN OPEN DEFECT: the shared
  # per-limit-group budget is allocated FIRST-COME across sibling scheduler groups, so a
  # low-priority sibling scanned first consumes the whole budget and starves a higher-priority
  # sibling (which then gets count=0 and its whole ready backlog skipped).
  #
  # THIS MODE IS INVERTED, DELIBERATELY: it asserts the BUGGY behaviour, so exit 0 means THE
  # BUG REPRODUCED, not that an invariant holds. It is the dangerous shape of gate, so it
  # prints a banner saying so - a reader scanning exit codes across a sweep would otherwise
  # read its zero as good news, which is exactly what happened in the 2026-08-27 sweep. It was
  # NOT flipped to permanently red: the defect is real and outstanding, and a gate that can
  # only ever fail stops being read. The outstanding item is recorded in
  # .docs/reliable4/prod-validation-260827.md.
  #
  # Exit status means "was anything measured", not "is the invariant held": exit 1 only when
  # the reproducer produced no measurement. Both measured outcomes exit 0 and are told apart by
  # their banner - BUG STILL PRESENT (the starvation reproduced), or NO LONGER REPRODUCES, in
  # which case the banner asks for this mode to be converted into a proper regression gate
  # (the higher-priority sibling must get its share of the budget).
  need_repo
  local limit="${1:-2000}" extra="${2:-500}"
  echo "reliable3 2a priority fairness (deterministic, in-process): a low-priority sibling"
  echo "scanned first starves a higher-priority sibling of the shared limit-group budget."
  echo "scale: limit=$limit readyPerGroup=$((limit + extra))"
  echo "INVERTED MODE: it asserts the BUG, so exit 0 means the bug reproduced. Exit 1 means"
  echo "nothing was measured. Read the banner below, never the exit code alone."
  osunset
  local out rc=0
  out=$(WR_OP_LIMIT="$limit" WR_PF_READY="$extra" \
    timeout 180 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
    -run TestReliable3PriorityFairnessStarvation -count=1 -v 2>&1) || rc=$?
  printf '%s\n' "$out" | grep -aE 'PRIORITY-FAIRNESS-REPRO|Expected|--- (PASS|FAIL)|^(ok|FAIL)'

  local repro low high skipped
  repro=$(printf '%s\n' "$out" | grep -aoE 'PRIORITY-FAIRNESS-REPRO: .*' | tail -1)
  low=$(pf_num "$repro" 'low\(pri0\)\.count')
  high=$(pf_num "$repro" 'high\(pri250\)\.count')
  skipped=$(pf_num "$repro" 'high\.skipped')
  for v in low high skipped; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done

  echo "## VERDICT: limit=$limit low(pri0).count=$low high(pri250).count=$high high.skipped=$skipped"
  if [ -z "$repro" ] || [ "$low" -lt 0 ] || [ "$high" -lt 0 ] || [ "$skipped" -lt 0 ]; then
    echo "FAIL (NOT MEASURED): no usable PRIORITY-FAIRNESS-REPRO line, so neither sibling's count"
    echo "  was measured; this is the ONLY outcome this mode exits non-zero for. See the output above"
    return 1
  fi

  if [ "$high" -eq 0 ] && [ "$low" -eq "$limit" ] && [ "$skipped" -gt 0 ]; then
    echo "==============================================================================="
    echo "!! EXIT 0 HERE MEANS THE BUG IS STILL PRESENT, NOT THAT AN INVARIANT HOLDS. !!"
    echo "==============================================================================="
    echo "The reliable3 2a starvation REPRODUCED: the low-priority sibling took the whole"
    echo "$limit-slot budget and the priority-250 sibling got 0, with $skipped of its ready jobs"
    echo "skipped. This is an OPEN DEFECT, recorded in .docs/reliable4/prod-validation-260827.md."
    echo "Do not read this mode's zero exit as a passing invariant anywhere it is swept."
    echo "==============================================================================="
    if [ "$rc" -ne 0 ]; then
      echo "FAIL: the numbers say the starvation reproduced but the reproducer's own assertions"
      echo "  failed (rc=$rc), so one of them no longer matches the line above; see the output"
      return "$rc"
    fi
    return 0
  fi

  echo "==============================================================================="
  echo "!! THE 2a STARVATION NO LONGER REPRODUCES - good news, and this mode is now  !!"
  echo "!! OBSOLETE AS WRITTEN.                                                      !!"
  echo "==============================================================================="
  echo "It asserts the BUG, so from here on it will keep reporting failing assertions."
  echo "CONVERT IT into a regression gate: the higher-priority sibling must get its share"
  echo "of the shared limit-group budget (high(pri250).count > 0), then remove the open"
  echo "item from .docs/reliable4/prod-validation-260827.md."
  echo "==============================================================================="
  return 0
}

# pf_num prints the integer value of a `<key>=<int>` field in the priority-fairness
# reproducer line, printing nothing when the key is absent (which its caller must treat as an
# unmeasured FAIL). The keys contain regex metacharacters, so they are passed pre-escaped.
pf_num() {  # <line> <escapedKey>
  printf '%s\n' "$1" | grep -aoE "$2=[0-9]+" | tail -1 | cut -d= -f2
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

cmd_bkill_hygiene() {  # bkill-hygiene [elements] [hangSeconds] - reliable4 FINDING 4 SCALE GATE
  # SCALE GATE for reliable4 FINDING 4 (.docs/reliable4/prod-run-20260817.md): the excess-runner
  # kill path handed bkill ONE unbounded argv (~1,900 element ids measured live), with no timeout
  # and no context, re-issued the IDENTICAL failing kill on the next scheduling cycle (339390[7..13]
  # killed at 15:36:09 and again at 15:36:10), and logged the whole id list - 116 warnings and
  # ~75KB/min of pure `toKill=` text, which is what made the manager log unreadable during the
  # incident, and which hid whether those elements were already gone (benign) or live
  # over-provisioned runners that were never reclaimed (the reliable3 "lost slots" symptom).
  #
  # It is INDICATIVE, not faithful, and farm-safe by construction: the real bkill cannot be driven
  # at this scale without killing real LSF jobs, so bjobs and bkill are fake exes in a temp dir
  # (NO LSF job is ever submitted or killed, and no manager is started at all), while everything
  # on the wr side - the collector, the argv building, the exec, the back-off and the logging - is
  # the real killExcessCmds path, driven at the prod-measured element count.
  #
  # It measures the five things prod could not distinguish:
  #   maxArgv      largest single bkill argv (pre-fix: all $elements ids in one exec)
  #   invocations  bkills per cycle (pre-fix: 1, however many ids there are)
  #   cycle2*      whether the next cycle repeats the same ids (pre-fix: all of them, 1s later)
  #   killed/gone  the killed-vs-already-gone split (pre-fix: not reported at all, so -1)
  #   logBytes     one cycle's logging (pre-fix: ~104KB; the headline symptom)
  #   elapsedMs    how long a HUNG bkill blocks the kill path (pre-fix: forever)
  # A MISSING measurement (no summary line, unparseable number, no bkill invoked) is a hard FAIL,
  # never a cheap PASS.
  need_repo
  local elements="${1:-1900}" hang="${2:-120}"
  local cap=1000 logmax=4096 hangbound=90000
  local out="$WRDEV_ROOT/bkill-hygiene.out"
  echo "reliable4 FINDING 4 bkill hygiene gate: $elements excess LSF array elements, fake bjobs/bkill"
  echo "(farm-safe: no manager, no bsub, no real bkill), hanging-bkill case sleeps ${hang}s."
  echo "Gate: maxArgv <= $cap AND uniqueIDs == $elements AND logBytes <= $logmax AND cycle2RepeatedIDs == 0"
  echo "      AND killedReported/goneReported both >= 0 AND a hung bkill returns within ${hangbound}ms."
  echo "prod pre-fix: one ~1,900-id argv, ~104KB of log per cycle, the same ids re-killed every cycle."
  mkdir -p "$WRDEV_ROOT" 2>/dev/null
  osunset
  local rc=0
  WR_BK_ELEMENTS="$elements" WR_BK_HANG="$hang" \
    timeout 900 go -C "$REPO" test -tags reliability_repro ./jobqueue/scheduler/ \
    -run TestReliable4BkillHygieneScale -count=1 -v > "$out" 2>&1 || rc=$?
  grep -aE 'BKILL-HYGIENE|Expected|--- (PASS|FAIL)|^(ok|FAIL)' "$out" || true

  local chunk repeat outcome hangline
  chunk=$(grep -a 'BKILL-HYGIENE-CHUNK:' "$out" | tail -1)
  repeat=$(grep -a 'BKILL-HYGIENE-REPEAT:' "$out" | tail -1)
  outcome=$(grep -a 'BKILL-HYGIENE-OUTCOME:' "$out" | tail -1)
  hangline=$(grep -a 'BKILL-HYGIENE-HANG:' "$out" | tail -1)

  local invocations maxargv uniqueids logbytes repeated killed gone elapsed
  invocations=$(bk_num "$chunk" invocations); maxargv=$(bk_num "$chunk" maxArgv)
  uniqueids=$(bk_num "$chunk" uniqueIDs); logbytes=$(bk_num "$chunk" logBytes)
  repeated=$(bk_num "$repeat" cycle2RepeatedIDs)
  killed=$(bk_num "$outcome" killedReported); gone=$(bk_num "$outcome" goneReported)
  elapsed=$(bk_num "$hangline" elapsedMs)

  echo "## VERDICT: maxArgv=$maxargv (cap $cap)  invocations=$invocations  uniqueIDs=$uniqueids/$elements"
  echo "##          logBytes=$logbytes (max $logmax)  cycle2RepeatedIDs=$repeated"
  echo "##          killedReported=$killed goneReported=$gone  hungBkillElapsedMs=$elapsed (max $hangbound)"

  # a gate that PASSES when the measurement is missing is worse than no gate.
  local unmeasured=""
  [ -n "$chunk" ] || unmeasured="no BKILL-HYGIENE-CHUNK line: the scale test did not run to its first measurement"
  [ -z "$unmeasured" ] && [ -z "$repeat" ] && unmeasured="no BKILL-HYGIENE-REPEAT line: the second cycle was never measured"
  [ -z "$unmeasured" ] && [ -z "$outcome" ] && unmeasured="no BKILL-HYGIENE-OUTCOME line: the killed-vs-gone split was never measured"
  [ -z "$unmeasured" ] && [ -z "$hangline" ] && unmeasured="no BKILL-HYGIENE-HANG line: the hung-bkill case was never measured"
  if [ -z "$unmeasured" ]; then
    local v
    for v in "$invocations" "$maxargv" "$uniqueids" "$logbytes" "$repeated" "$killed" "$gone" "$elapsed"; do
      case "$v" in
        ''|*[!0-9-]*) unmeasured="a measurement was missing or unparseable in the lines above"; break ;;
      esac
    done
  fi
  [ -z "$unmeasured" ] && [ "$invocations" -le 0 ] \
    && unmeasured="0 bkill invocations, so the fake bkill was never run and nothing was measured"
  [ -z "$unmeasured" ] && [ "$logbytes" -le 0 ] \
    && unmeasured="0 log bytes, so no kill cycle was logged at all"
  local failed=""
  if [ -z "$unmeasured" ]; then
    [ "$maxargv" -le "$cap" ] || failed="a single bkill argv held $maxargv ids (cap $cap): rule 7 uncapped again"
    [ -z "$failed" ] && [ "$uniqueids" -ne "$elements" ] \
      && failed="the batches covered $uniqueids of $elements excess elements: chunking is dropping kills"
    [ -z "$failed" ] && [ "$logbytes" -gt "$logmax" ] \
      && failed="one cycle logged $logbytes bytes (max $logmax): the whole id list is being logged again"
    [ -z "$failed" ] && [ "$repeated" -ne 0 ] \
      && failed="the next cycle re-issued $repeated of the same ids: the kill back-off is gone"
    [ -z "$failed" ] && { [ "$killed" -lt 0 ] || [ "$gone" -lt 0 ]; } \
      && failed="the summary does not report killed vs alreadyGone, so an unreclaimed runner still hides"
    [ -z "$failed" ] && [ "$elapsed" -gt "$hangbound" ] \
      && failed="a hung bkill blocked the kill path for ${elapsed}ms (max $hangbound): the exec timeout is gone"
  fi

  local verdict=0
  if [ -n "$unmeasured" ]; then
    verdict=1
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; see the test output greped above"
  elif [ -n "$failed" ]; then
    verdict=1
    echo "FAIL: $failed"
  elif [ "$rc" -ne 0 ]; then
    verdict="$rc"
    echo "FAIL: the scale test itself failed (rc=$rc) even though the measurements are within bounds"
  else
    echo "PASS: the excess-runner kill is chunked to <= $cap ids per bkill, time-bounded, backed off"
    echo "      and summarised in $logbytes bytes with a killed-vs-already-gone split"
  fi
  echo "## CLEANUP"
  rm -f "$out" 2>/dev/null
  return "$verdict"
}

# bk_num extracts a `key=<int>` value (which may be negative) from one of the scale test's
# BKILL-HYGIENE lines, printing nothing when the key is absent (so the caller FAILs NOT MEASURED).
bk_num() {  # <line> <key>
  printf '%s\n' "$1" | grep -aoE "$2=-?[0-9]+" | tail -1 | cut -d= -f2
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

cmd_control_rpc_history() {  # control-rpc-history [archived] [groups] [live] - reliable4 FINDING 1 SCALE GATE
  # SCALE GATE for reliable4 FINDING 1 (.docs/reliable4/prod-run-20260817.md): a control
  # command that can only ever act on LIVE jobs must stay responsive no matter how much
  # ARCHIVED history the database holds. In production `wr resume -i portal -z` died with
  # `receive time out` after 120s while two handleGetByRepGroup goroutines kept
  # decodeArchivedJob-ing for 12+ minutes AFTER the client had given up, taking the manager's
  # heap 348MB -> 12,143MB with multi-second GC pauses; the operator could not un-suspend the
  # queue at all, and `-a` (the only history-free route) was the sole workaround.
  #
  # This is an end-to-end gate on the REAL binary: it seeds a database with `archived`
  # archived jobs spread over `groups` RepGroups (via TestReliable4SeedArchivedHistory -
  # bulk-inserted, because running that many jobs through the queue would take hours), points
  # an isolated PROD-mode manager at it (prod mode preserves the DB; dev mode wipes it), adds
  # `live` ready jobs in the FIRST of those RepGroups behind a limit group set to 0 - so they
  # are a real live backlog that can NEVER be scheduled, no runner is ever launched and no LSF
  # job is ever submitted (farm-safe) - and then TIMES THE ACTUAL CLI COMMANDS the doc names:
  # `wr limit`, `wr suspend -i <rg>` and `wr resume -i <substr> -z`.
  #
  # It also reports the manager's peak-RSS growth (VmHWM) across those commands, which is the
  # heap excursion prod suffered, and then takes a REFERENCE measurement: `wr status -i <rg>`
  # legitimately DOES want the history, so its timing and job count prove the seeded history
  # is real, reachable through this very RPC path, and expensive - which is what stops a PASS
  # here from being vacuous. The reference deliberately runs AFTER the timed commands: VmHWM
  # is a high-water MARK, so a reference scan taken first would make its own (legitimate)
  # excursion the baseline, and no growth the timed commands caused could ever be reported.
  #
  # It then times `wr status -i <substr> -z --limit 1` separately (reliable4 ITEM B), against its own
  # VmHWM baseline: that request legitimately wants history but can only ever RETURN one job per
  # status group, so it must cost O(limit), not O(history).
  #
  # GATE (the doc's batch targets): limit/suspend/resume all under WRDEV_HS_MAX_MS (5000ms),
  # peak-RSS growth under WRDEV_HS_MAX_RSS_MB, the status -z --limit 1 under WRDEV_HS_MAX_STATUS_MS
  # with its own peak-RSS growth under WRDEV_HS_MAX_STATUS_RSS_MB, and the commands must actually
  # have done their work (suspended == resumed == live, and the status command still standing in
  # for all `seeded - 1` other commands). A MISSING or INVALID measurement is a FAIL, never a
  # PASS: no seed line, a seeded history too small to gate on, a dead manager, jobs not added,
  # a reference scan that did not return the seeded history, a status command that did not return
  # the seeded history's one status group, or an unparseable timing/RSS all exit 1. The size
  # floors matter because the pre-fix cost is PROPORTIONAL to the history:
  # `wr suspend -i <rg>` decoded one group's records and `wr resume -i <substr> -z` decoded
  # every group's, so with a small history the pre-fix code passes too and the gate proves
  # nothing (WRDEV_HS_MIN_PERGROUP / WRDEV_HS_MIN_ARCHIVED).
  #
  # Disk: the seeded DB is ~5KB per archived job (production-sized records plus bolt page and
  # index overhead) and prod mode also keeps a backup copy, so the defaults need ~2GB free in
  # $PROD_RUN; both are removed in CLEANUP below. A/B the fix by running this in a pre-fix
  # `git worktree` vs the fixed tree - but note the pre-fix side DECODES the whole history
  # into RAM, so give the machine room for it.
  need_repo; need_bin; ensure_config
  local archived="${1:-200000}" groups="${2:-20}" live="${3:-5000}"
  local maxms="${WRDEV_HS_MAX_MS:-5000}" maxrssmb="${WRDEV_HS_MAX_RSS_MB:-512}"
  local maxstatusms="${WRDEV_HS_MAX_STATUS_MS:-5000}" maxstatusrssmb="${WRDEV_HS_MAX_STATUS_RSS_MB:-128}"
  local maxplainrssmb="${WRDEV_HS_MAX_PLAIN_RSS_MB:-128}"
  local minpergroup="${WRDEV_HS_MIN_PERGROUP:-1000}" minarchived="${WRDEV_HS_MIN_ARCHIVED:-100000}"
  local rgp="hsrg" rg1="hsrg0" lg="hslimit"
  local dbf="$PROD_RUN/db" jobs="$WRDEV_ROOT/hsjobs.json" out="$WRDEV_ROOT/control-rpc-history.out"
  mkdir -p "$WRDEV_ROOT"
  echo "reliable4 FINDING 1 control-RPC gate: $archived archived jobs over $groups rep groups,"
  echo "  $live live ready-but-blocked jobs ($lg:0, so no runner and no LSF job is ever created),"
  echo "  then the REAL 'wr limit', 'wr suspend -i $rg1' and 'wr resume -i $rgp -z' are timed."
  echo "  Then 'wr status -i $rgp -z --limit 1' is timed separately (reliable4 ITEM B): it wants the"
  echo "  history but can only return one job per status group, so it must cost O(limit), not O(history)."
  echo "  prod pre-fix: 'wr resume -i portal -z' timed out at 120s, kept scanning for 12+ min,"
  echo "  manager heap 348MB -> 12143MB. Gate: each control command <= ${maxms}ms, peak-RSS growth"
  echo "  <= ${maxrssmb}MB; status -z --limit 1 <= ${maxstatusms}ms and <= ${maxstatusrssmb}MB of its own peak-RSS growth."
  echo "  A MISSING measurement (no seed, dead manager, no reference scan) is a FAIL, not a PASS,"
  echo "  as is a history too small to gate on (>= $minpergroup per group and >= $minarchived in total:"
  echo "  the pre-fix cost was proportional to it, so a small history passes pre-fix as well)."
  osunset
  safe_kill "$(mgr_pid "$PROD_RUN")" >/dev/null 2>&1; sleep 2
  rm -rf "$PROD_RUN" 2>/dev/null; mkdir -p "$PROD_RUN"

  echo "=== seeding $dbf ==="
  local seed
  WRDEV_ROOT="$WRDEV_ROOT" WR_HS_DB="$dbf" WR_HS_ARCHIVED="$archived" WR_HS_GROUPS="$groups" \
    WR_HS_RG_PREFIX="$rgp" timeout 3600 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
      -run TestReliable4SeedArchivedHistory -count=1 -v -timeout 3500s > "$out" 2>&1 || true
  seed=$(grep -aoE 'HISTORY-SEED .*' "$out" | tail -1)
  echo "  ${seed:-<no HISTORY-SEED line; see $out>}"
  local pergroup; pergroup=$(echo "$seed" | grep -aoE 'perGroup=[0-9]+' | cut -d= -f2)
  local seeded; seeded=$(echo "$seed" | grep -aoE 'archived=[0-9]+' | cut -d= -f2)
  for v in pergroup seeded; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done

  echo "=== starting the isolated prod-mode manager on that DB ==="
  cmd_prod_start local 2>&1 | sed 's/^/  /'
  local mpid; mpid=$(mgr_pid "$PROD_RUN")
  sleep 3

  local added=""
  if [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; then
    perl -e "for my \$i (1..$live){print '{\"cmd\":\"sleep 1 #hs'.\$i.'\"}'.\"\n\"}" > "$jobs"
    added=$(timeout 900 "$WR" add -f "$jobs" --rep_grp "$rg1" --limit_grps "$lg:0" \
      --retries 0 --deployment production 2>&1 | grep -aoE 'Added [0-9]+ new commands' | grep -aoE '[0-9]+')
    echo "  added ${added:-0} live jobs to $rg1 (limit group $lg:0)"
  fi
  case "${added:-x}" in (*[!0-9]*|'') added=-1 ;; esac

  # the timed control commands, FIRST, so that the VmHWM baseline sampled here is the
  # manager's steady state with the live backlog in it: VmHWM is a high-water MARK, so
  # anything memory-hungry done before this sample (notably the reference history scan
  # below, which decodes a whole group's records on purpose) would raise the baseline and
  # hide exactly the excursion this metric exists to catch.
  local rss0=-1 rss1=-1 limms=-1 susms=-1 resms=-1 suspended=-1 resumed=-1 t0 t1
  if [ "$added" -gt 0 ]; then
    rss0=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
    t0=$(date +%s%3N); timeout 600 "$WR" limit --deployment production -g "$lg" >/dev/null 2>&1
    t1=$(date +%s%3N); limms=$((t1 - t0))
    t0=$(date +%s%3N)
    suspended=$(timeout 600 "$WR" suspend --deployment production -i "$rg1" 2>&1 \
      | grep -aoE 'Suspended [0-9]+' | grep -aoE '[0-9]+')
    t1=$(date +%s%3N); susms=$((t1 - t0))
    t0=$(date +%s%3N)
    resumed=$(timeout 600 "$WR" resume --deployment production -i "$rgp" -z 2>&1 \
      | grep -aoE 'Resumed [0-9]+' | grep -aoE '[0-9]+')
    t1=$(date +%s%3N); resms=$((t1 - t0))
    rss1=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
  fi
  for v in rss0 rss1 suspended resumed; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done
  local rssmb=-1
  { [ "$rss0" -ge 0 ] && [ "$rss1" -ge 0 ]; } && rssmb=$(((rss1 - rss0) / 1024))

  # SCALE GATE for reliable4 ITEM B, and a SEPARATE measurement from both the control
  # commands above and the reference scan below. `wr status -i <substr> -z --limit 1` is a
  # request that legitimately wants history but can only ever RETURN one job per status
  # group, and it used to decode every matching RepGroup's entire history first - so the
  # 12.1GB excursion FINDING 1 closed for suspend/resume stayed reachable from a status
  # command, with no way for an operator to know that -l 1 did not bound the work.
  #
  # It gets its OWN VmHWM baseline, sampled here rather than reused from rss0: rss1 is
  # already this run's high-water mark, and growth measured from a stale, lower baseline
  # would report the control commands' excursion as this one's. It also runs BEFORE the
  # reference scan for the same reason the control commands do - the reference decodes a
  # whole group ON PURPOSE, so letting it run first would make no growth here reportable.
  #
  # That baseline is sampled after a `-o counts -z` warm-up, ON PURPOSE. bbolt mmaps the
  # database, so ANY walk of the history makes its pages resident and grows VmHWM whether or
  # not one record is decoded, and at these sizes that page residency is comparable to the
  # decode itself: from a COLD baseline the fixed and unfixed managers are only 757MB vs
  # 1391MB apart at 200k records, most of it shared. `wr status -o counts` walks exactly the
  # same records through the count-only path (addCompleteJobStatusByRepGroup with
  # includeDetails false), which decodes NONE of them, so after it the baseline already holds
  # the mmap residency and what the timed command adds on top is the DECODE - the thing this
  # gate exists to catch. The warm-up's own reported complete count must equal the seeded
  # history, or it did not walk it and the baseline is not the one this gate assumes.
  #
  # Of the two cost metrics the RSS one is the DISCRIMINATING one: measured this way the same
  # 200k history costs 29MB fixed and 677MB unfixed. The time bound is only a ceiling against a
  # gross regression - warmed by the count-only pass above, the request takes ~1.0s fixed and
  # ~2.1s unfixed, so timing alone would not separate them and a gate resting on it would be
  # one of the false-PASS gates .docs/reliable4/next-steps-260819.md warns about.
  #
  # It is gated on its answer as well as its cost: with every seeded record identical they
  # all fall in one status group, so `-o details --limit 1` must print exactly one job for that
  # group standing in for the other `seeded - 1`. A pushdown that returned a different
  # count would be changing what `wr status` reports, which is not what this fix is.
  local statms=-1 statsim=-1 statrss0=-1 statrss1=-1 statrssmb=-1 statout="" warmms=-1 warmcomplete=-1
  if [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; then
    t0=$(date +%s%3N)
    warmcomplete=$(timeout 1800 "$WR" status --deployment production -i "$rgp" -z -o counts 2>/dev/null \
      | grep -aoE 'complete: [0-9]+' | grep -aoE '[0-9]+')
    t1=$(date +%s%3N); warmms=$((t1 - t0))
    case "${warmcomplete:-x}" in (*[!0-9]*|'') warmcomplete=-1 ;; esac
    echo "  count-only warm-up 'wr status -i $rgp -z -o counts' saw $warmcomplete complete jobs in ${warmms}ms"
    statrss0=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
    t0=$(date +%s%3N)
    statout=$(timeout 1800 "$WR" status --deployment production -i "$rgp" -z --limit 1 -o details 2>&1)
    t1=$(date +%s%3N); statms=$((t1 - t0))
    statrss1=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
    printf '=== wr status -i %s -z --limit 1 ===\n%s\n' "$rgp" "$statout" >> "$out"
    statsim=$(printf '%s\n' "$statout" | grep -aoE '\+ [0-9]+ other commands' \
      | grep -aoE '[0-9]+' | sort -n | tail -1)
    echo "  'wr status -i $rgp -z --limit 1' took ${statms}ms and stood in for ${statsim:-<none>} other commands"
  fi
  for v in statsim statrss0 statrss1; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done
  { [ "$statrss0" -ge 0 ] && [ "$statrss1" -ge 0 ]; } && statrssmb=$(((statrss1 - statrss0) / 1024))

  # SCALE GATE for reliable4 BUG D3, and again a SEPARATE measurement. cmd/status.go
  # zeroes the limit for the UNGROUPED output formats, so `wr status -i <substr> -z -o plain`
  # (and any explicit --limit 0) still asks for every matching archived record and gets the
  # unbounded decode that ITEM B removed from the default path. The 2026-08-20 validation
  # gate measured that at 6,975ms and +905MB of peak RSS on only 154,000 records, which
  # extrapolates to ~12.6GB on production's ~2.15M complete jobs - the same excursion
  # FINDING 1 closed for the control paths, one flag away.
  #
  # There is no result-preserving bound for this shape (-o plain prints one line per job
  # KEY, so the limitJobs grouping the grouped formats fold into is not available to it), so
  # the fix is a heap budget on the fetch plus a refusal that names the way out. This gate
  # therefore measures the COST, not the answer: whether the request is served or refused,
  # it must not take the manager's peak RSS up by more than WRDEV_HS_MAX_PLAIN_RSS_MB. If it
  # IS refused, the refusal must name a way out, or the operator is simply stuck.
  #
  # Its baseline is sampled here, after the -z --limit 1 measurement and before the
  # reference scan, for the same VmHWM high-water-mark reason as those: a baseline taken
  # any earlier would already include another measurement's excursion.
  local plainms=-1 plainrss0=-1 plainrss1=-1 plainrssmb=-1 plainlines=-1 plainrefused=0 plainout=""
  if [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; then
    plainrss0=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
    t0=$(date +%s%3N)
    plainout=$(timeout 1800 "$WR" status --deployment production -i "$rgp" -z -o plain 2>&1)
    t1=$(date +%s%3N); plainms=$((t1 - t0))
    plainrss1=$(awk '/^VmHWM:/{print $2}' "/proc/$mpid/status" 2>/dev/null)
    plainlines=$(printf '%s\n' "$plainout" | awk -F'\t' '$2=="complete"{n++} END{print n+0}')
    printf '=== wr status -i %s -z -o plain (first 5 lines) ===\n%s\n' "$rgp" \
      "$(printf '%s\n' "$plainout" | head -5)" >> "$out"
    printf '%s\n' "$plainout" | grep -aq 'too much completed-job history' && plainrefused=1
    echo "  'wr status -i $rgp -z -o plain' took ${plainms}ms, printed $plainlines complete lines" \
      "(refused=$plainrefused)"
  fi
  for v in plainrss0 plainrss1 plainlines; do
    case "${!v}" in (*[!0-9]*|'') eval "$v=-1" ;; esac
  done
  { [ "$plainrss0" -ge 0 ] && [ "$plainrss1" -ge 0 ]; } && plainrssmb=$(((plainrss1 - plainrss0) / 1024))

  # the reference, deliberately AFTER the measurements above: `wr status` with no limit DOES
  # want the whole history, so this both proves the seeded history is real and reachable
  # through the same getbr RPC the timed commands used, and shows what paying for it costs.
  # EVERY history-decoding request belongs here rather than earlier, the counts display
  # included, for the VmHWM baseline reason above.
  local refjobs=-1 refms=-1
  if [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; then
    t0=$(date +%s%3N)
    refjobs=$(timeout 1800 "$WR" status --deployment production -i "$rg1" -o plain 2>/dev/null \
      | awk -F'\t' '$2=="complete"{n++} END{print n+0}')
    t1=$(date +%s%3N); refms=$((t1 - t0))
    echo "  reference 'wr status -i $rg1' returned $refjobs complete jobs in ${refms}ms (it decodes them all)"
    echo "  counts after resuming: $(timeout 300 "$WR" status --deployment production -i "$rg1" \
      -o counts 2>/dev/null | tr '\n' ' ')"
  fi
  case "${refjobs:-x}" in (*[!0-9]*|'') refjobs=-1 ;; esac

  echo "## VERDICT: limit=${limms}ms suspend=${susms}ms (suspended=$suspended)" \
       "resume -z=${resms}ms (resumed=$resumed) peakRSSgrowth=${rssmb}MB;" \
       "status -z --limit 1=${statms}ms (similar=$statsim) peakRSSgrowth=${statrssmb}MB;" \
       "status -z -o plain=${plainms}ms (lines=$plainlines refused=$plainrefused) peakRSSgrowth=${plainrssmb}MB" \
       "[count-only warm-up: $warmcomplete jobs in ${warmms}ms]" \
       "[reference history scan: $refjobs jobs in ${refms}ms]"
  # a gate that PASSES when the measurement is missing is worse than no gate, so an absent
  # seed line, a history too small for the pre-fix code to have failed on either, a manager
  # that never came up, jobs that were not added, a reference scan that did not return the
  # seeded history and an unreadable timing/RSS are all hard FAILURES.
  local verdict=0 unmeasured=""
  [ -n "$seed" ] && [ "$pergroup" -gt 0 ] && [ "$seeded" -gt 0 ] \
    || unmeasured="the seeder produced no usable HISTORY-SEED line, so there is no archived history to gate on"
  [ -z "$unmeasured" ] && { [ "$pergroup" -lt "$minpergroup" ] || [ "$seeded" -lt "$minarchived" ]; } \
    && unmeasured="the seeded history is too small to gate on (perGroup=$pergroup needs >= $minpergroup, total=$seeded needs >= $minarchived): the pre-fix cost was proportional to the history, so at this size the unfixed code comes in under ${maxms}ms too and a PASS would prove nothing (raise the archived/groups arguments, or lower WRDEV_HS_MIN_PERGROUP/WRDEV_HS_MIN_ARCHIVED if you know what you are doing)"
  [ -z "$unmeasured" ] && { [ -z "$mpid" ] || ! ps -p "$mpid" >/dev/null 2>&1; } \
    && unmeasured="the isolated prod-mode manager is not running, so nothing served these requests"
  [ -z "$unmeasured" ] && [ "$added" -ne "$live" ] \
    && unmeasured="only $added of $live live jobs were added, so suspend/resume had nothing real to select"
  [ -z "$unmeasured" ] && [ "$refjobs" -ne "$pergroup" ] \
    && unmeasured="the reference 'wr status -i $rg1' returned $refjobs complete jobs, not the $pergroup seeded: the history is not reachable, so a fast suspend/resume proves nothing"
  [ -z "$unmeasured" ] && { [ "$limms" -lt 0 ] || [ "$susms" -lt 0 ] || [ "$resms" -lt 0 ] || [ "$rssmb" -lt 0 ]; } \
    && unmeasured="could not read a timing or the manager's VmHWM, so the commands were not measured"
  [ -z "$unmeasured" ] && { [ "$statms" -lt 0 ] || [ "$statrssmb" -lt 0 ]; } \
    && unmeasured="could not time 'wr status -i $rgp -z --limit 1' or read the manager's VmHWM around it, so the ITEM B pushdown was not measured"
  [ -z "$unmeasured" ] && [ "$warmcomplete" -ne "$seeded" ] \
    && unmeasured="the count-only warm-up saw $warmcomplete complete jobs, not the $seeded seeded, so the VmHWM baseline it exists to establish does not hold the whole history's mmap residency and the growth measured after it is not the decode"
  [ -z "$unmeasured" ] && { [ "$plainms" -lt 0 ] || [ "$plainrssmb" -lt 0 ] || [ -z "$plainout" ]; } \
    && unmeasured="could not time 'wr status -i $rgp -z -o plain' or read the manager's VmHWM around it, so the BUG D3 unbounded shape was not measured"
  [ -z "$unmeasured" ] && [ "$plainrefused" -eq 0 ] && [ "$plainlines" -ne "$seeded" ] \
    && unmeasured="'wr status -i $rgp -z -o plain' neither returned the $seeded seeded jobs ($plainlines lines) nor was refused, so whatever it did was not the shape this gate measures"
  [ -z "$unmeasured" ] && [ "$statsim" -ne "$((seeded - 1))" ] \
    && unmeasured="'wr status -i $rgp -z --limit 1' stood in for $statsim other commands, not the $((seeded - 1)) seeded: it did not return the history's one status group, so its cost proves nothing"
  if [ -n "$unmeasured" ]; then
    verdict=1
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $out and $PROD_RUN"
  elif [ "$suspended" -ne "$live" ] || [ "$resumed" -ne "$live" ]; then
    verdict=1
    echo "FAIL (WRONG RESULT): suspended=$suspended resumed=$resumed, expected $live each"
    echo "  => the commands returned quickly but no longer act on the jobs they used to, so the"
    echo "     state filter is excluding live jobs it must not (see JobStateIncomplete)"
  elif [ "$limms" -gt "$maxms" ] || [ "$susms" -gt "$maxms" ] || [ "$resms" -gt "$maxms" ] \
    || [ "$rssmb" -gt "$maxrssmb" ]; then
    verdict=1
    echo "FAIL: a control command is paying for the archived history again (>${maxms}ms or >${maxrssmb}MB)"
    echo "  => suspend/resume must send a state filter (cmd/suspend.go getSelectedJobs) and"
    echo "     getJobsByRepGroup must only fetch complete jobs when asked (repGroupOptions.IncludeComplete)"
  elif [ "$plainrssmb" -gt "$maxplainrssmb" ]; then
    verdict=1
    echo "FAIL: 'wr status -i $rgp -z -o plain' is still materialising the whole history"
    echo "  (${plainrssmb}MB > ${maxplainrssmb}MB of peak-RSS growth, in ${plainms}ms)"
    echo "  => cmd/status.go zeroes the limit for the ungrouped output formats, so this shape"
    echo "     gets the unbounded archived decode. It needs a heap budget on the fetch"
    echo "     (newArchivedBytesBudget) and a refusal that names the way out"
  elif [ "$plainrefused" -eq 1 ] && ! printf '%s\n' "$plainout" | grep -aq -- '--limit'; then
    verdict=1
    echo "FAIL: 'wr status -i $rgp -z -o plain' was refused without naming a way out"
    echo "  => a refusal an operator cannot act on is worse than a slow answer"
  elif [ "$statms" -gt "$maxstatusms" ] || [ "$statrssmb" -gt "$maxstatusrssmb" ]; then
    verdict=1
    echo "FAIL: 'wr status -i $rgp -z --limit 1' is still materialising the whole history"
    echo "  (${statms}ms > ${maxstatusms}ms or ${statrssmb}MB > ${maxstatusrssmb}MB of peak-RSS growth)"
    echo "  => the Limit/Offset must be pushed down into retrieveOldestCompleteJobsByRepGroup and"
    echo "     spent ACROSS getRepGroupsList's loop (newCompleteJobsBudget), so that a request that"
    echo "     can only return one job per status group decodes one job per status group"
  else
    echo "PASS: with $archived archived jobs over $groups rep groups (a $refms ms scan when a request"
    echo "  actually asks for it), limit/suspend/resume -z cost ${limms}/${susms}/${resms}ms and ${rssmb}MB,"
    echo "  and 'wr status -i $rgp -z --limit 1' cost ${statms}ms and ${statrssmb}MB while still standing in"
    echo "  for all $statsim other commands, and the ungrouped 'wr status -i $rgp -z -o plain' cost"
    echo "  ${plainms}ms and ${plainrssmb}MB (refused=$plainrefused, lines=$plainlines)"
  fi
  echo "## CLEANUP"
  safe_kill "$(mgr_pid "$PROD_RUN")"
  rm -f "$jobs" 2>/dev/null
  rm -rf "$PROD_RUN" 2>/dev/null
  return "$verdict"
}

cmd_dep_granularity_check() {  # dep-granularity-check [waiters] [members] [groups] - dep-granularity SCALE GATE
  # SCALE GATE for the dep-granularity spec (.docs/dep-granularity/spec.md F3). Production
  # was OOM-killed FIVE times at ~180GB recovering its prior state: an -inuse_space profile
  # taken mid-recovery put 97.55% of a 15.65GB live heap in recoveredItemDef, while the
  # 150,472 decoded jobs themselves were only 376MB (2.40%) of it. The bytes were the jobs'
  # dependency KEY LISTS - the pre-fix code expanded a dependency on a dep group into one
  # key per LIVE MEMBER of that group - so retention was waiters x members, ~19,000 keys per
  # job. Recovery of those 150,472 jobs took 42m56s; the manager served for four minutes and
  # died.
  #
  # This is an end-to-end gate on the REAL binary, and it covers the ADD path as well as
  # recovery. It builds a fixture database of `members` live jobs in one dep group, one live
  # job in each of the other `groups`-1 groups, and `waiters` live jobs depending on the big
  # group (via TestDepGranularityFixture, which stores them through db.storeNewJobs so
  # bucketRDTK and bucketDepGroups hold what the real add path puts there, and writes
  # bucketDTK itself as the pre-upgrade data it is - see that test for why BOTH of those
  # matter, and .docs/dep-granularity/scale-gate.md for the recorded numbers). It then points
  # an isolated PROD-mode manager at a COPY of it (prod mode preserves the DB; dev mode wipes
  # it), measures the recovery window between two log lines that exist in both trees, samples
  # `wr manager status` inside that window, and finally times `wr add` of one more member
  # into the big group.
  #
  # Nothing ever runs: every fixture job also depends on a dep group NO job is ever added
  # with, so the whole database is permanently `dependent`, no runner is launched and no LSF
  # job is submitted. The manager runs with -f, so it is the process this function started
  # (no pid file, no daemon) and /proc/<pid>/status can be sampled from t=0.
  #
  # A/B the fix by running this in a pre-fix `git worktree` (the branch point, 8b9ba00) vs
  # the fixed tree. Neither the mode nor the fixture generator exists at that commit, so
  # copy THIS script over there and hand the pre-fix run the fixture through
  # WRDEV_DEPGRAN_DB: pristine means the product code under test, not the harness, and REPO
  # is resolved from this script's own path so the pre-fix worktree builds and measures the
  # pre-fix binary. That only works because the fixture writes bucketDTK itself; leave those
  # entries to storeNewJobs and the pre-fix binary finds no member keys to expand, allocates
  # nothing quadratically and hands back a false PASS.
  #
  # A MISSING or INVALID measurement is a FAIL, never a PASS: no DEPGRAN-FIXTURE line from
  # the generator, an unreadable or zero peakRssMb, a manager that never reached recovery at
  # all, or (in a run that did finish recovering) an unreadable recoverySec/addSec or an add
  # that did not add its one job all exit 1. The DEPGRAN-SUMMARY line itself is printed from
  # the measurements rather than parsed back out of them, so unlike archive-rate there is no
  # case where it can go missing while the verdict is still reached. A run whose
  # start line appeared but whose finish line never did is a PLAIN FAIL rather than "not
  # measured": that is the pre-fix outcome this gate exists to catch, and peakRssMb is still
  # reported from the samples taken before the death.
  #
  # Disk: the fixture is ~110MB at the default shape and each run copies it, so $WRDEV_ROOT
  # needs room for two of them plus the manager's own backup; the copy is removed in CLEANUP
  # below and the fixture itself is kept, so a second run (the other side of an A/B) can be
  # handed it through WRDEV_DEPGRAN_DB. Memory: the PRE-FIX run is the one that needs headroom - it retains
  # waiters x members dependency keys - so it is capped by WRDEV_DEPGRAN_ABORT_RSS_MB, which
  # kills the manager and reports the FAIL rather than taking the host down with it.
  need_repo; need_bin; ensure_config
  # an empty positional (the dispatcher passes one when the argument is absent) has to fall
  # back too, which ${1:-...} alone does not do for an argument that is set but empty.
  local waiters="${1:-}" members="${2:-}" groups="${3:-}"
  waiters="${waiters:-${WRDEV_DEPGRAN_WAITERS:-30000}}"
  members="${members:-${WRDEV_DEPGRAN_MEMBERS:-3000}}"
  groups="${groups:-${WRDEV_DEPGRAN_GROUPS:-6300}}"
  # each threshold is 2x the post-fix figure at the default shape, rounded up to a round
  # number, and each is at most half the pre-fix figure - a threshold the pre-fix run would
  # pass is not a gate. The figures behind them, and the A/B they came from, are recorded in
  # .docs/dep-granularity/scale-gate.md.
  local maxrssmb="${WRDEV_DEPGRAN_MAX_RSS_MB:-700}"
  local maxrecsec="${WRDEV_DEPGRAN_MAX_RECOVERY_SEC:-1}"
  local maxaddsec="${WRDEV_DEPGRAN_MAX_ADD_SEC:-2}"
  local abortrssmb="${WRDEV_DEPGRAN_ABORT_RSS_MB:-16000}"
  local waitsec="${WRDEV_DEPGRAN_TIMEOUT_SEC:-1800}"
  local biggroup="${WRDEV_DEPGRAN_GROUP:-depgran-big}"
  local blocker="${WRDEV_DEPGRAN_BLOCKER:-depgran-blocker}"
  local supplied="${WRDEV_DEPGRAN_DB:-}"
  local out="$WRDEV_ROOT/dep-granularity-check.out"
  local mgrout="$WRDEV_ROOT/dep-granularity-manager.out"
  local fixture="$WRDEV_ROOT/depgran_fixture_db"
  local addrg="depgran-add"
  mkdir -p "$WRDEV_ROOT"
  echo "dep-granularity scale gate: $waiters waiters on a $members-member dep group, $groups groups in total"
  echo "  prod pre-fix: 150,472 live jobs, ~19,000 retained keys each, 97.55% of a 15.65GB heap in"
  echo "  recoveredItemDef, 42m56s to recover, five OOM kills at ~180GB."
  echo "  gate: peakRssMb <= ${maxrssmb}, recoverySec <= ${maxrecsec}, addSec <= ${maxaddsec},"
  echo "  statusInWindow must be 'starting' or 'up', never 'nonresponsive' and never '-' twice."

  # one run's diagnostics per file: the fixture step truncates it, but a run handed a
  # pre-built fixture skips that step and would otherwise append to the previous run's.
  : > "$out"

  local rc=0
  dep_granularity_run "$waiters" "$members" "$groups" "$maxrssmb" "$maxrecsec" "$maxaddsec" \
    "$abortrssmb" "$waitsec" "$biggroup" "$blocker" "$supplied" "$out" "$mgrout" "$fixture" "$addrg" || rc=$?

  # exit 2 means the run was clean except that nothing sampled `wr manager status` inside the
  # startup window, which is the one thing the gate can fix by trying again: doubling the
  # waiters lengthens recovery and so widens the window. Widen ONCE, and never a run that
  # already failed for another reason (rc 1), which a longer run would not teach anything
  # about. A supplied fixture cannot be widened, since generating one is what widening means.
  if [ "$rc" -eq 2 ]; then
    if [ -n "$supplied" ]; then
      rc=1
      echo "  (not widening: WRDEV_DEPGRAN_DB supplies the fixture, so its shape is fixed)"
    else
      waiters=$((waiters * 2))
      echo "=== widening the window once: rerunning with $waiters waiters ==="
      rc=0
      dep_granularity_run "$waiters" "$members" "$groups" "$maxrssmb" "$maxrecsec" "$maxaddsec" \
        "$abortrssmb" "$waitsec" "$biggroup" "$blocker" "$supplied" "$out" "$mgrout" "$fixture" \
        "$addrg" "widened-waiters-to-$waiters" || rc=$?
      [ "$rc" -eq 2 ] && rc=1
    fi
  fi

  echo "## CLEANUP"
  rm -f "$PROD_RUN/db" "$PROD_RUN/db_bk" 2>/dev/null
  rm -rf "$PROD_RUN" 2>/dev/null
  return "$rc"
}

# dep_granularity_run is cmd_dep_granularity_check's body, split out so that the cleanup
# above cannot swallow the verdict: the exit status is captured and returned, exactly as
# archive-rate does with its pipeline.
dep_granularity_run() {
  local waiters="$1" members="$2" groups="$3" maxrssmb="$4" maxrecsec="$5" maxaddsec="$6"
  local abortrssmb="$7" waitsec="$8" biggroup="$9" blocker="${10}" supplied="${11}"
  local out="${12}" mgrout="${13}" fixture="${14}" addrg="${15}"
  local errors="${16:-}"
  [ -n "$errors" ] && errors="${errors};"

  # the delimiter lines are in the manager's OWN log file, not in what -f prints: the
  # foreground output carries a handful of INFO lines in the terminal format, while
  # config.ManagerLogFile gets the structured lvl=/msg= records both trees write.
  local mgrlog="$PROD_RUN/log"

  osunset

  echo "=== fixture ==="
  if [ -n "$supplied" ]; then
    [ -f "$supplied" ] || { echo "FAIL (NOT MEASURED): WRDEV_DEPGRAN_DB=$supplied does not exist"; return 1; }
    fixture="$supplied"
    echo "  using the supplied fixture $fixture ($(stat -c %s "$fixture") bytes); generation skipped."
    echo "  its shape is whatever built it: the waiters/members/groups arguments only label the summary."
  else
    rm -f "$fixture" "${fixture}_bk"
    # `|| true` because `set -u` is on and a non-zero `go test` would end the run before the
    # verdict machinery below could report it. Nothing is lost by ignoring the exit status:
    # a generator that failed is caught by the missing DEPGRAN-FIXTURE line two lines down,
    # which is a FAIL (NOT MEASURED), never a PASS. That protection is implicit and worth
    # keeping in mind if either end moves - the Printf that produces the line sits AFTER the
    # generator's So()s, and GoConvey's FailureHalts abandons the block on the first failed
    # one, so a generator that asserted its way to a stop never prints it.
    WRDEV_ROOT="$WRDEV_ROOT" WR_DEPGRAN_DB="$fixture" WR_DEPGRAN_WAITERS="$waiters" \
      WR_DEPGRAN_MEMBERS="$members" WR_DEPGRAN_GROUPS="$groups" WR_DEPGRAN_GROUP="$biggroup" \
      WR_DEPGRAN_BLOCKER="$blocker" \
      timeout 3600 go -C "$REPO" test -tags reliability_repro ./jobqueue/ \
        -run TestDepGranularityFixture -count=1 -v -timeout 3500s >> "$out" 2>&1 || true
    rm -f "${fixture}_bk"
    local seed; seed=$(grep -aoE 'DEPGRAN-FIXTURE .*' "$out" | tail -1)
    echo "  ${seed:-<no DEPGRAN-FIXTURE line; see $out>}"
    [ -n "$seed" ] || { echo "FAIL (NOT MEASURED): the fixture generator produced no DEPGRAN-FIXTURE line"; return 1; }
  fi
  [ -f "$fixture" ] || { echo "FAIL (NOT MEASURED): no fixture database at $fixture"; return 1; }

  safe_kill "$(mgr_pid "$PROD_RUN")" >/dev/null 2>&1
  dep_granularity_sweep
  rm -rf "$PROD_RUN" 2>/dev/null; mkdir -p "$PROD_RUN"
  cp "$fixture" "$PROD_RUN/db" || { echo "FAIL (NOT MEASURED): could not copy the fixture"; return 1; }

  echo "=== starting the isolated prod-mode manager on a copy of it ==="
  : > "$mgrout"
  WR_JOBNAME_TOKEN="$PROD_JOBTOKEN" nohup "$WR" manager start --deployment production -s local -f \
    >> "$mgrout" 2>&1 &
  local mpid=$!
  echo "  manager pid $mpid, foreground output $mgrout, manager log $mgrlog"

  # an interrupted run would otherwise leave that manager holding $PROD_RUN, since -f writes
  # no pid file for the next run's safe_kill to find. The trap covers the signal case and
  # dep_granularity_sweep above the kill -9 case; both are cleared when the run ends normally,
  # so the ordinary teardown below stays the only one that speaks.
  trap 'safe_kill "$mpid"; trap - INT TERM; exit 130' INT TERM

  # the window, sampled from process start. The log is read through a held file descriptor
  # rather than re-grepped, so following it costs no process per poll. Every timestamp comes
  # from bash's own clock and every poll from a builtin, because a fork costs tens of
  # milliseconds on a host at load 120 and the whole startup window is a few hundred
  # milliseconds once the fix is in: an earlier version of this loop used date/awk/ps per
  # iteration and measured a 13ms recovery as 259ms of its own latency.
  #
  # VmHWM is read on EVERY poll rather than once a second, and once more after the add below.
  # It is a high-water mark, so the last reading of a live process is its peak; the polling is
  # for the process that dies, whose /proc entry goes with it. Sampled once a second, a
  # post-fix run that recovers in under a second got exactly one reading - taken at t=0, of a
  # process that had allocated nothing - and reported 10MB where the real peak was 212MB,
  # which is a measurement of nothing that would pass any threshold.
  local peakkb=0 startms=0 finishms=0 servingms=0 sampledms=0 firedms=0 statusin="-"
  local aborted=0 died=0 logopen=0 sampling=0 pollsec="0.02" line nowms deadlinems fifo
  local statusfile samplepid=""
  statusfile="$WRDEV_ROOT/depgran-status.$$"
  rm -f "$statusfile" "$statusfile.part"
  nowms=$(( ${EPOCHREALTIME/[.,]/} / 1000 ))
  deadlinems=$(( nowms + waitsec * 1000 ))

  # a fifo nothing writes to, so `read -t` is a sleep that costs no process. Failing to make
  # it is a hard stop rather than something to carry on without: fd 8 would then be unopened,
  # every `read -t -u 8` at the bottom of the loop would error instead of sleeping, and the
  # loop would spin flat out for up to waitsec. It fails here rather than through `die` so the
  # manager started above is torn down rather than leaked.
  fifo=$(mktemp -u "$WRDEV_ROOT/depgran-tick.XXXXXX")
  if ! { mkfifo "$fifo" && exec 8<>"$fifo"; }; then
    rm -f "$fifo"
    safe_kill "$mpid"; wait "$mpid" 2>/dev/null; trap - INT TERM
    echo "FAIL (NOT MEASURED): could not create the poll fifo $fifo"

    return 1
  fi
  rm -f "$fifo"

  while :; do
    nowms=$(( ${EPOCHREALTIME/[.,]/} / 1000 ))

    if [ "$logopen" -eq 0 ] && [ -f "$mgrlog" ]; then exec 9< "$mgrlog"; logopen=1; fi

    if [ "$logopen" -eq 1 ]; then
      while IFS= read -r line <&9; do
        case "$line" in
          # the start line first: the heartbeat's progress line contains its text, so the
          # window opens on the FIRST match and the msg= field keeps the two apart.
          *'msg="recovering prior state"'*)
            [ "$startms" -eq 0 ] && startms=$(( ${EPOCHREALTIME/[.,]/} / 1000 )) ;;
          *'msg="recovering: prior state recovered"'*)
            [ "$finishms" -eq 0 ] && finishms=$(( ${EPOCHREALTIME/[.,]/} / 1000 )) ;;
          *'started on '*)
            [ "$servingms" -eq 0 ] && servingms=$(( ${EPOCHREALTIME/[.,]/} / 1000 )) ;;
        esac
      done
    fi

    dep_granularity_proc "$mpid"
    case "$DEPGRAN_PROC_HWM" in (''|*[!0-9]*) ;; (*)
      [ "$DEPGRAN_PROC_HWM" -gt "$peakkb" ] && peakkb="$DEPGRAN_PROC_HWM" ;;
    esac
    if [ "$peakkb" -gt $(( abortrssmb * 1024 )) ]; then aborted=1; break; fi
    if [ "$DEPGRAN_PROC_ALIVE" -eq 0 ]; then died=1; break; fi

    # the ONE in-window `wr manager status`, fired as soon as there is a startup phase to
    # report: the sidecar's existence means one is in progress, and it appears before the
    # recovery line does. The pre-fix tree writes no sidecar, so there the start line is what
    # fires it, and the answer is `up` - which is not a FAIL and which acceptance test 1 does
    # not rest on. Whether it landed inside the window is decided afterwards, by comparing
    # when it answered with when the finish line appeared.
    #
    # It runs in the BACKGROUND. Run inline it takes ~25ms, during which this loop reads no
    # log lines - and 25ms is a third of the whole post-fix recovery, so both delimiter lines
    # then arrived in the same drain and recoverySec measured 0.000.
    if [ "$sampling" -eq 0 ] && [ "$finishms" -eq 0 ] \
      && { [ -f "$PROD_RUN/db.upgrade" ] || [ "$startms" -ne 0 ]; }; then
      firedms=$(( ${EPOCHREALTIME/[.,]/} / 1000 ))
      sampling=1
      ( dep_granularity_status_sample "$out" > "$statusfile.part"; mv "$statusfile.part" "$statusfile" ) &
      samplepid=$!
    fi

    if [ "$sampling" -eq 1 ] && [ -f "$statusfile" ]; then
      sampledms=$(( ${EPOCHREALTIME/[.,]/} / 1000 ))
      statusin=$(cat "$statusfile")
      sampling=2
    fi

    # full resolution until recovery has been going for a while, then a tenth of it: a
    # post-fix window is a few tens of milliseconds and a 0.2s poll quantises it to nothing,
    # while a pre-fix recovery of this shape runs for many minutes and does not need it.
    [ "$startms" -ne 0 ] && [ $(( nowms - startms )) -gt 5000 ] && pollsec="0.2"

    [ "$finishms" -ne 0 ] && break
    [ "$nowms" -gt "$deadlinems" ] && break

    read -t "$pollsec" -u 8 line
  done
  [ "$logopen" -eq 1 ] && exec 9<&-
  exec 8<&-

  # a sample still in flight when the window closed did not answer inside it, whatever it
  # goes on to say. It is waited for by pid, never with a bare wait, which would wait for the
  # manager too and never return.
  [ -n "$samplepid" ] && wait "$samplepid" 2>/dev/null
  rm -f "$statusfile" "$statusfile.part"

  [ "$sampledms" -ne 0 ] && echo "  'wr manager status' was fired inside the window and took" \
    "$(( sampledms - firedms ))ms to answer '$statusin'"

  # a sample that only came back after the window had closed measured the wrong thing.
  if [ "$sampledms" -eq 0 ]; then
    statusin="-"
  elif [ "$finishms" -ne 0 ] && [ "$finishms" -lt "$sampledms" ]; then
    statusin="-"
  fi

  local recoveryms=-1
  { [ "$startms" -ne 0 ] && [ "$finishms" -ne 0 ]; } && recoveryms=$((finishms - startms))

  [ "$aborted" -eq 1 ] && errors="${errors}aborted-at-rss-ceiling;"
  [ "$died" -eq 1 ] && errors="${errors}manager-died-during-recovery;"
  { [ "$startms" -ne 0 ] && [ "$finishms" -eq 0 ] && [ "$aborted" -eq 0 ] && [ "$died" -eq 0 ]; } \
    && errors="${errors}recovery-did-not-finish-in-${waitsec}s;"
  [ "$statusin" = "-" ] && errors="${errors}status-sample-missed-the-window;"

  # the add path, which is the other half of the quadratic: adding one more member to a dep
  # group re-resolves every job waiting on it.
  local addms=-1 added=-1
  if [ "$finishms" -ne 0 ] && dep_granularity_wait_up 300; then
    local t0 t1 addout
    t0=$(( ${EPOCHREALTIME/[.,]/} / 1000 ))
    addout=$(echo "true #$addrg $$" | timeout 3600 "$WR" add --deployment production \
      -e "$biggroup" -d "$blocker" --rep_grp "$addrg" --retries 0 2>&1)
    t1=$(( ${EPOCHREALTIME/[.,]/} / 1000 )); addms=$((t1 - t0))
    added=$(printf '%s\n' "$addout" | grep -aoE 'Added [0-9]+ new commands' | grep -aoE '[0-9]+' | head -1)
    case "${added:-x}" in (''|*[!0-9]*) added=-1 ;; esac
    printf '=== wr add ===\n%s\n' "$addout" >> "$out"
  elif [ "$finishms" -ne 0 ]; then
    errors="${errors}manager-never-answered-after-recovery;"
  fi

  # the last high-water reading, covering the add as well: VmHWM only ever grows, so this is
  # the whole run's peak whenever the manager is still alive to be read.
  dep_granularity_proc "$mpid"
  case "$DEPGRAN_PROC_HWM" in (''|*[!0-9]*) ;; (*)
    [ "$DEPGRAN_PROC_HWM" -gt "$peakkb" ] && peakkb="$DEPGRAN_PROC_HWM" ;;
  esac

  safe_kill "$mpid"
  wait "$mpid" 2>/dev/null
  trap - INT TERM

  # E2 acceptance test 4: `wr manager start -f` prints "wr manager ... started on" only after
  # the listener is bound, which is after recovery ends. The wall-clock gap the poll loop
  # measured is printed as evidence, but the VERDICT is taken from the order the two lines sit
  # in the manager's own log, because that is not a race: the loop breaks on the finish line,
  # so it can miss a "started on" written a millisecond later, while both lines come from the
  # same logger and so reach the file in the order they were emitted. The check itself is
  # deliberately LAST, after every metric threshold (see below).
  local finishline startedline
  finishline=$(grep -an 'msg="recovering: prior state recovered"' "$mgrlog" 2>/dev/null \
    | head -1 | cut -d: -f1)
  startedline=$(grep -anE 'msg="wr manager .* started on ' "$mgrlog" 2>/dev/null \
    | head -1 | cut -d: -f1)
  if [ "$servingms" -ne 0 ] && [ "$finishms" -ne 0 ]; then
    echo "  'started on' was logged $((servingms - finishms))ms after 'prior state recovered'" \
         "(negative means it announced itself before recovery finished, as the pre-fix tree does)"
  fi

  # the manager's own log, kept where the cleanup cannot reach it: its per-phase elapsed
  # fields are how a slow run is attributed to a phase.
  printf '=== manager log ===\n' >> "$out"
  cat "$mgrlog" >> "$out" 2>/dev/null

  local peakrssmb=-1
  [ "$peakkb" -gt 0 ] && peakrssmb=$((peakkb / 1024))
  printf 'DEPGRAN-SUMMARY waiters=%s members=%s peakRssMb=%s recoverySec=%s statusInWindow=%s addSec=%s errors=%s\n' \
    "$waiters" "$members" "$peakrssmb" "$(dep_granularity_secs "$recoveryms")" "$statusin" \
    "$(dep_granularity_secs "$addms")" "${errors:--}"

  # the verdict. A gate that passes when the measurement is missing is worse than no gate, so
  # an absent or unreadable metric is a hard failure; the one exception is a run that started
  # recovering and never finished, which is a plain FAIL because it is the pre-fix outcome
  # this gate exists to catch.
  if [ "$startms" -eq 0 ]; then
    echo "FAIL (NOT MEASURED): the manager never logged 'recovering prior state', so it never"
    echo "  reached recovery and nothing was measured; inspect $out"
    return 1
  fi

  if [ "$peakrssmb" -le 0 ]; then
    echo "FAIL (NOT MEASURED): no usable VmHWM sample, so the manager's peak memory was never"
    echo "  measured; inspect $out"
    return 1
  fi

  if [ "$finishms" -eq 0 ]; then
    echo "FAIL: recovery started and never finished (peakRssMb=$peakrssmb, errors=$errors)"
    echo "  => this is the pre-fix outcome: expanding a dep-group dependency into one key per"
    echo "     live member retains waiters x members keys, and the manager dies or crawls long"
    echo "     before it can publish (spec A1 and A2)"
    return 1
  fi

  if [ "$recoveryms" -lt 0 ] || [ "$addms" -lt 0 ]; then
    echo "FAIL (NOT MEASURED): recovery finished but recoverySec or addSec is absent (errors=$errors)"
    echo "  => a run whose 'wr add' never happened would otherwise sail past every threshold"
    return 1
  fi

  if [ "$added" -ne 1 ]; then
    echo "FAIL (WRONG RESULT): 'wr add' of one member into $biggroup reported $added new commands"
    echo "  => the add was timed but did not do the work the timing is supposed to cover"
    return 1
  fi

  if [ "$statusin" = "nonresponsive" ]; then
    echo "FAIL: 'wr manager status' died against a manager that was still starting"
    echo "  => that is exactly what spec E5 exists to prevent: the startup sidecar must be read"
    echo "     before the command decides the manager is non-responsive (cmd/manager.go)"
    return 1
  fi

  if [ "$peakrssmb" -gt "$maxrssmb" ] \
    || [ "$(awk -v a="$recoveryms" -v b="$maxrecsec" 'BEGIN{print (a>b*1000)?1:0}')" -eq 1 ] \
    || [ "$(awk -v a="$addms" -v b="$maxaddsec" 'BEGIN{print (a>b*1000)?1:0}')" -eq 1 ]; then
    echo "FAIL: a threshold was exceeded (peakRssMb=$peakrssmb/$maxrssmb," \
         "recoveryMs=$recoveryms/$((maxrecsec * 1000)), addMs=$addms/$((maxaddsec * 1000)))"
    echo "  => a dep-group dependency must stay one opaque key for the whole group, resolved"
    echo "     against the in-memory per-group member state (jobqueue/depgroups.go)"
    return 1
  fi

  # E2 acceptance test 4, and deliberately AFTER every metric threshold above. A pre-fix
  # binary, or a post-fix one measured against a blunt fixture, fails on memory or on the
  # recovery time first and never reaches this line - which is what keeps an ordering check
  # from masking the memory gate this whole function exists to be. Placed any earlier it would
  # fail the pre-fix arm of the A/B on ordering before the two sides' memory figures had been
  # compared at all, voiding the comparison.
  if [ -z "$startedline" ] || [ -z "$finishline" ]; then
    echo "FAIL (NOT MEASURED): the manager log has no 'started on' and/or 'prior state"
    echo "  recovered' line, so the startup ordering was never observed; inspect $mgrlog"
    return 1
  fi

  if [ "$startedline" -lt "$finishline" ]; then
    echo "FAIL (WRONG ORDER): 'wr manager ... started on' was logged at line $startedline of"
    echo "  the manager log, before 'prior state recovered' at line $finishline"
    echo "  => 'wr manager start -f' must announce the manager only once the listener is bound,"
    echo "     which is after recovery ends: startJQ waits on server.Serving() before"
    echo "     logStarted (cmd/manager.go). Announcing at t=0 advertises a manager that will"
    echo "     not answer for as long as recovery takes"
    return 1
  fi

  # last, so that a run failing for any other reason is never widened: a longer run of a run
  # that already FAILs teaches nothing. 2 asks the caller to widen the window once.
  if [ "$statusin" = "-" ]; then
    echo "FAIL: nothing sampled 'wr manager status' inside the startup window"
    echo "  => a run that never samples leaves E5's command wiring with no automated coverage"
    return 2
  fi

  echo "PASS: peakRssMb=$peakrssmb, recovery in ${recoveryms}ms, 'wr manager status' answered" \
       "'$statusin' inside the window, and one more member added in ${addms}ms"
  return 0
}

# dep_granularity_sweep reaps a prod-mode manager left behind by an interrupted run of this
# gate. The gate starts its manager with -f, so there is no pid file and mgr_pid/safe_kill
# have nothing to find; a leftover manager would still hold $PROD_RUN when the next run
# rm -rf'd it. Candidates are found by cmdline, and safe_kill still refuses any pid whose
# running process is not our isolated $WR binary, so a real production manager (a different
# binary path entirely) can never be a candidate here.
dep_granularity_sweep() {
  local pid
  for pid in $(pgrep -u "$(id -u)" -f "^$WR manager start --deployment production" 2>/dev/null); do
    [ "$pid" = "$$" ] && continue
    safe_kill "$pid"
  done
}

# dep_granularity_proc reads the manager's peak RSS and whether it is still a live process,
# out of /proc with shell builtins only. It reports through globals because a command
# substitution would fork, which is what this loop exists to avoid; a zombie counts as dead,
# since this function's caller is the process's parent and never reaps it until the end.
dep_granularity_proc() {
  local pid="$1" key value rest
  DEPGRAN_PROC_HWM=""
  DEPGRAN_PROC_ALIVE=0

  [ -r "/proc/$pid/status" ] || return 0

  while read -r key value rest; do
    case "$key" in
      VmHWM:) DEPGRAN_PROC_HWM="$value" ;;
      State:) [ "$value" != "Z" ] && DEPGRAN_PROC_ALIVE=1 ;;
    esac
  done < "/proc/$pid/status"
}

# dep_granularity_secs renders a millisecond measurement as seconds, and a missing one as the
# "-" step 7 asks for: no numeric threshold can evaluate a metric the run never reached, so
# it must be visibly absent rather than zero.
dep_granularity_secs() {
  [ "$1" -lt 0 ] && { echo "-"; return 0; }
  awk -v ms="$1" 'BEGIN{printf "%.3f", ms/1000}'
}

# dep_granularity_status_sample runs `wr manager status` once and classifies its answer.
dep_granularity_status_sample() {
  local out="$1" sout srce=0
  sout=$(timeout 120 "$WR" manager status --deployment production 2>&1) || srce=$?
  printf '=== wr manager status (in window) rc=%s ===\n%s\n' "$srce" "$sout" >> "$out"
  if [ "$srce" -ne 0 ]; then echo "nonresponsive"; return 0; fi
  case "$sout" in
    *"starting:"*) echo "starting" ;;
    *"Status website:"*) echo "up" ;;
    # anything else is "stopped", which the command prints when it finds no pid file, no
    # listener and no startup sidecar. The sample was fired because the sidecar existed, so
    # that answer means the sidecar went away before the command read it - which is the
    # window closing, not an answer about the window.
    *) echo "-" ;;
  esac
}

# dep_granularity_wait_up waits up to the given seconds for the manager to answer as a
# running manager, which is what step 6 needs before it can time an add.
dep_granularity_wait_up() {
  local secs="$1" waited=0
  while [ "$waited" -lt "$secs" ]; do
    timeout 60 "$WR" manager status --deployment production 2>&1 | grep -aq 'Status website:' && return 0
    sleep 2
    waited=$((waited + 2))
  done
  return 1
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

cmd_exec_impossible_retries() {  # exec-impossible-retries [jobs] [seconds] [cores] - reliable4 FINDING 5 SCALE GATE
  # SCALE GATE for reliable4 FINDING 5 (.docs/reliable4/prod-run-20260817.md): a command that
  # can NEVER exec must cost exactly ONE runner slot and ONE attempt, not N. In production a
  # Cmd over Linux's MAX_ARG_STRLEN (128KB for a SINGLE argv element, whatever the much larger
  # ARG_MAX is) failed `fork/exec ...: argument list too long`, was treated as a transient
  # failure and released for a retry: 608 such events across 150 runner logs in 25 minutes,
  # 109 of them from ONE runner. Every retry spends a scheduled runner, a reservation, a copy
  # of the (enormous) command over RPC and a bolt write, to learn the same answer again - and
  # the retries were UNBOUNDED, because the server only decremented UntilBuried for a job whose
  # StartTime was set and an exec that never started never reported a start, so not even
  # --retries 0 could stop it. ITEM A has since bounded them at --retries+1 (see the sibling
  # transient-start-retries gate), but Retries+1 runner slots spent relearning a permanent answer
  # is Retries+1 too many, so this gate still demands a bury on attempt ONE. That is why it uses
  # --retries 2: a job that merely obeys its retry budget would take 3 attempts and FAIL here.
  #
  # End-to-end on the REAL binary, and farm-safe: -s local, so no LSF job is ever submitted
  # and the only processes are our own dev manager plus at most `cores` local runners. It runs
  # the isolated dev manager with --runner_filelog (the capability that made this failure
  # visible in production at all), adds `jobs` jobs whose Cmd is over MAX_ARG_STRLEN, waits up
  # to `seconds` for them to reach a terminal state, then measures THREE independent things:
  #   attempts = `argument list too long` lines across ALL runner logs (the runner-side truth)
  #   buried   = the rep group's buried count from `wr status` (the manager-side truth)
  #   reasoned = how many buried jobs give `command line too long` as their problem (the CAUSE)
  # PASS = all three == jobs: one slot, one attempt, a permanent verdict, for the right reason.
  # Pre-fix: buried == 0 and attempts >> jobs and still climbing when the window ends. The
  # `reasoned` measurement is what stops an over-wide "bury every start failure" change - which
  # would bury healthy transiently-failing work - from passing this gate on the counts alone.
  # A MISSING measurement (dead manager, nothing added, no runner logs, zero attempts,
  # unparseable counts, no jobs reported by `wr status`) is a hard FAIL, never a cheap PASS.
  need_bin; ensure_config
  local jobs="${1:-20}" secs="${2:-120}" cores="${3:-2}"
  local rg=rgexecfail
  local logdir="$WRDEV_ROOT/execfail-runnerlogs" work="$WRDEV_ROOT/execfail.json"
  # 128KB is MAX_ARG_STRLEN; go over it so exec fails immediately and deterministically.
  local padkb="${WRDEV_EXECFAIL_PADKB:-130}"
  echo "reliable4 FINDING 5 exec-impossible gate: $jobs jobs whose Cmd is ~${padkb}KB (over the 128KB"
  echo "MAX_ARG_STRLEN single-argv cap), on an isolated dev manager (-s local, --max_cores $cores,"
  echo "--runner_filelog). Such a command can never exec on any host at any time."
  echo "prod pre-fix: 608 'argument list too long' events / 150 runner logs / 25min, 109 from one runner."
  echo "Gate: buried == $jobs AND attempts == $jobs (exactly one runner slot and one attempt per job),"
  echo "and all $jobs give 'command line too long' as their problem (buried for the RIGHT reason)."
  echo "A MISSING measurement (no runner logs, 0 attempts, dead manager) is a FAIL, not a PASS."
  rm -rf "$logdir" 2>/dev/null; mkdir -p "$logdir"
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s local); dev mode wipes the DB"
  osunset ; timeout 90 "$WR" manager start --deployment development -s local \
    --max_cores "$cores" --runner_filelog "$logdir" 2>&1 | grep -aE 'started on|token=' | head -2
  local mpid; mpid=$(mgr_pid "$DEV_RUN")
  { [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; } || die "could not start dev manager"
  echo "pid $mpid"
  sleep 3

  local rc=0
  ef_run "$jobs" "$secs" "$rg" "$logdir" "$work" "$padkb" "$mpid" || rc=$?
  echo "## CLEANUP"
  cmd_stop >/dev/null 2>&1
  ef_reap_runners "$logdir"
  rm -rf "$logdir" 2>/dev/null; rm -f "$work" 2>/dev/null
  return "$rc"
}

# ef_reap_runners kills any of OUR local runners still alive from this gate. cmd_stop only kills
# the manager (and bkills LSF jobs), and a -s local runner that is mid-reserve outlives it; on a
# pre-fix/FAIL run there is always one, since the retry storm never ends. Three gates stand
# between this and someone else's production manager on the same shared host: only this user's
# processes are considered (ps -u), the argv must contain a `--logdir` whose NEXT argument is
# EXACTLY this gate's log dir (an argv-aware awk match, not a substring one - a substring match
# would also reap a process whose logdir merely has ours as a prefix), and each surviving
# candidate is re-checked with is_ours so it must be our isolated binary. ps output is captured
# BEFORE it is filtered, so the filter's own command line can never self-match.
ef_reap_runners() {  # <logdir>
  local list; list=$(ps -u "$(id -un)" -o pid=,args= 2>/dev/null)
  printf '%s\n' "$list" \
    | awk -v d="$1" '{for(i=2;i<NF;i++) if ($i=="--logdir" && $(i+1)==d) {print $1; break}}' | while read -r p; do
    [ -n "$p" ] || continue
    is_ours "$p" && kill -9 "$p" 2>/dev/null && echo "killed our leftover local runner pid $p"
  done
}

# ef_run does the measured part of exec-impossible-retries, so the caller can always clean up
# the runner-log dir and the (large) job file without its `rm`s swallowing the FAIL exit code.
ef_run() {  # <jobs> <seconds> <repGroup> <logdir> <jobfile> <padKB> <managerPid>
  local jobs="$1" secs="$2" rg="$3" logdir="$4" work="$5" padkb="$6" mpid="$7"
  echo "generating $jobs jobs with a ~${padkb}KB Cmd -> $work"
  perl -e "my \$pad='x' x ($padkb*1024); for my \$i (1..$jobs){ print '{\"cmd\":\"echo '.\$pad.' #'.\$i.'\",\"memory\":\"100M\",\"cpus\":1}'.\"\n\" }" \
    > "$work"
  local out arc
  out=$(osunset; timeout 300 "$WR" add -f "$work" --rep_grp "$rg" --retries 2 \
    --deployment development 2>&1); arc=$?
  echo "$out" | tail -1
  if [ "$arc" -ne 0 ] || ! echo "$out" | grep -qE "Added $jobs new commands"; then
    echo "FAIL (NOT MEASURED): $jobs jobs were not added (manager up? see the wr add output above)"
    return 1
  fi

  echo "waiting up to ${secs}s for all $jobs to reach a terminal state (post-fix: buried on attempt 1)"
  local counts="" buried=0 waited=0
  while [ "$waited" -lt "$secs" ]; do
    sleep 5; waited=$((waited + 5))
    counts=$(osunset; timeout 60 "$WR" status --deployment development -i "$rg" -o counts 2>/dev/null | tr '\n' ' ')
    buried=$(ef_num "$counts" buried)
    echo "  t=${waited}s $rg[$counts] attempts=$(ef_attempts "$logdir")"
    [ "$buried" -ge "$jobs" ] && break
  done

  local attempts; attempts=$(ef_attempts "$logdir")
  local logfiles; logfiles=$(find "$logdir" -type f 2>/dev/null | wc -l)
  # the CAUSE is measured too, not just the count: buried==N alone would also be satisfied by an
  # over-wide "bury every start failure" change, which would bury healthy transiently-failing work.
  local reason="command line too long" stanzas=0 reasoned=0 sr
  sr=$(ef_status_reasons "$rg" "$reason")
  stanzas=${sr%% *}; reasoned=${sr##* }
  case "$stanzas" in (*[!0-9]*|'') stanzas=0 ;; esac
  case "$reasoned" in (*[!0-9]*|'') reasoned=0 ;; esac
  echo "## VERDICT: buried=$buried/$jobs  execAttempts=$attempts  runnerLogFiles=$logfiles"
  echo "##          failReason '$reason' on $reasoned/$stanzas reported jobs"
  echo "##          counts: $rg[$counts]"

  # a gate that PASSES when the measurement is missing is worse than no gate.
  local unmeasured=""
  [ "$jobs" -gt 0 ] || unmeasured="asked for $jobs jobs, so there was nothing to measure"
  [ -z "$unmeasured" ] && { ps -p "$mpid" >/dev/null 2>&1 || unmeasured="the dev manager (pid $mpid) died during the run"; }
  [ -z "$unmeasured" ] && [ -z "$counts" ] \
    && unmeasured="'wr status -i $rg -o counts' returned nothing, so no manager-side state was read"
  [ -z "$unmeasured" ] && [ "$logfiles" -le 0 ] \
    && unmeasured="no runner log files under $logdir, so no runner ever started (--runner_filelog broken?)"
  [ -z "$unmeasured" ] && [ "$attempts" -le 0 ] \
    && unmeasured="0 'argument list too long' lines in the runner logs, so the exec failure never happened"
  [ -z "$unmeasured" ] && [ "$stanzas" -le 0 ] \
    && unmeasured="'wr status -i $rg --limit 0' reported no jobs, so no FailReason could be read"
  if [ -n "$unmeasured" ]; then
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $logdir and the counts above"
    return 1
  fi
  if [ "$buried" -ne "$jobs" ]; then
    echo "FAIL: only $buried/$jobs unrunnable jobs were buried after ${waited}s ($attempts exec attempts so far)"
    echo "  => an exec-impossible command is being released for a retry instead of buried;"
    echo "     ITEM A caps those retries at --retries+1, so this now ends - but every one of them"
    echo "     still burns a scheduled runner, a reservation, a copy of the command over RPC and a"
    echo "     bolt write to relearn an answer the FIRST attempt already gave"
    return 1
  fi
  if [ "$attempts" -ne "$jobs" ]; then
    echo "FAIL: $jobs unrunnable jobs cost $attempts exec attempts (want exactly $jobs, one runner slot each)"
    echo "  => something is re-attempting a command that can never exec"
    return 1
  fi
  if [ "$reasoned" -ne "$jobs" ]; then
    echo "FAIL: only $reasoned/$jobs buried jobs give '$reason' as their problem ($stanzas jobs reported)"
    echo "  => the jobs were buried for the wrong reason, so the operator cannot see what to fix;"
    echo "     a blanket 'bury every start failure' would look like this, and would bury healthy work"
    return 1
  fi
  echo "PASS: $jobs unrunnable jobs cost exactly $attempts exec attempts, all $buried are buried,"
  echo "      and all $reasoned give '$reason' as their problem"
  return 0
}

# ef_status_reasons prints "<jobs reported> <jobs whose problem is the given FailReason>" for a
# rep group, so the gate can assert the CAUSE of the burying and still FAIL loudly when nothing
# was reported. --limit 0 turns off `wr status`'s same-status grouping (its default --limit 1
# would collapse all N jobs into one stanza plus a "+ N other commands" line), and the output is
# streamed through awk rather than captured, because every stanza quotes the whole ~130KB Cmd.
ef_status_reasons() {  # <repGroup> <failReason>
  osunset; timeout 300 "$WR" status --deployment development -i "$1" --limit 0 2>/dev/null \
    | awk -v r="Previous problem: $2" '/^Cwd: /{s++} $0==r{m++} END{printf "%d %d\n", s+0, m+0}'
}

# ef_attempts counts exec failures matching a pattern (default the exec-impossible gate's
# `argument list too long`) across every runner log. Runner log lines can be ~130KB here (the
# failing command is quoted), so -o keeps the output small.
ef_attempts() {  # <logdir> [pattern]
  local n
  n=$(grep -rhao "${2:-argument list too long}" "$1" 2>/dev/null | wc -l)
  case "$n" in (*[!0-9]*|'') echo 0 ;; (*) echo "$n" ;; esac
}

# ef_num pulls "<name>: <n>" out of a `wr status -o counts` blob, printing 0 if it is absent or
# unparseable (which its caller must treat as an unmeasured FAIL, never as a cheap PASS).
ef_num() {  # <countsBlob> <name>
  local n
  n=$(printf '%s' "$1" | grep -aoE "$2: [0-9]+" | grep -aoE '[0-9]+' | head -1)
  case "$n" in (*[!0-9]*|'') echo 0 ;; (*) echo "$n" ;; esac
}

cmd_transient_start_retries() {  # transient-start-retries [jobs] [seconds] [cores] [retries] - reliable4 ITEM A SCALE GATE
  # SCALE GATE for reliable4 ITEM A (.docs/reliable4/next-steps-260819.md): a release that happens
  # BEFORE the job reported a start must still spend one of the job's --retries. The server sets
  # StartTime only from a landed start report (applyJobStart needs a real pid+host) and
  # resetJobForReservation re-zeroes it at every reservation, so a command that fails inside
  # cmd.Start() used to be released with UntilBuried untouched: an UNBOUNDED retry loop that
  # ignores --retries entirely and burns a scheduled runner, a reservation, a copy of the command
  # over RPC and a bolt write every time round. A pre-fix scale run sat at `delayed: 20, buried: 0`
  # for 90s while exec attempts climbed 20 -> 47 across 22 runner logs.
  #
  # 0d22eda closed this only for the three PERMANENT errnos it buries (E2BIG/ENOENT/EACCES; see the
  # exec-impossible-retries gate). This gate covers the half it deliberately left alone: a
  # TRANSIENT-classified start failure, which must be retried - but only `retries` times.
  #
  # How the transient start failure is induced, end-to-end on the REAL binary and farm-safe
  # (-s local, so no LSF job is ever submitted): jobs are added with --group, which makes
  # buildExecCmd exec `newgrp` (a BARE name, resolved on the runner's own PATH) instead of the
  # shell. A directory holding a `newgrp` that is executable but is not a valid executable format
  # is prepended to the manager's PATH, so every one of these jobs fails cmd.Start() with ENOEXEC
  # ("exec format error") - an errno permanentStartFailReason deliberately classifies as TRANSIENT
  # (it is the "a node with a broken loader" case), so the job is released, not buried on attempt
  # one. Nothing else in wr ever execs `newgrp`, and the shell is untouched, so the manager still
  # launches its runners normally. This is why the gate cannot simply make the shell unusable: the
  # local scheduler launches runners with that same shell.
  #
  # It then measures THREE independent things:
  #   attempts = `exec format error` lines across ALL runner logs (the runner-side truth)
  #   buried   = the rep group's buried count from `wr status` (the manager-side truth)
  #   reasoned = how many jobs give `command failed to start` as their problem (the CAUSE)
  # PASS = buried == jobs AND attempts == jobs*(retries+1) AND reasoned == jobs: the retries are
  # spent, the ceiling is reached, and the operator is told why. Pre-fix: buried == 0 and attempts
  # keeps climbing past jobs*(retries+1) for as long as the window lasts.
  # A MISSING measurement (dead manager, nothing added, no runner logs, zero attempts, unparseable
  # counts, no jobs reported by `wr status`) is a hard FAIL, never a cheap PASS.
  need_bin; ensure_config
  local jobs="${1:-20}" secs="${2:-300}" cores="${3:-2}" retries="${4:-2}"
  local rg=rgtransientstart
  local logdir="$WRDEV_ROOT/tsfail-runnerlogs" work="$WRDEV_ROOT/tsfail.json" badpath="$WRDEV_ROOT/tsfail-path"
  local want=$((jobs * (retries + 1)))
  echo "reliable4 ITEM A transient-start gate: $jobs jobs with --retries $retries whose cmd.Start()"
  echo "fails with ENOEXEC every time (a bare-name 'newgrp' on the runner's PATH that is executable"
  echo "but not a valid executable format), on an isolated dev manager (-s local, --max_cores $cores,"
  echo "--runner_filelog). ENOEXEC is deliberately TRANSIENT, so these are released, not buried at once."
  echo "Gate: buried == $jobs AND execAttempts == $want (jobs*(retries+1)) AND all $jobs give"
  echo "'command failed to start' as their problem."
  echo "Pre-fix: buried == 0 and attempts climb past $want without bound (UntilBuried never decrements"
  echo "for a job that never reported a start, so the retry ceiling is never reached)."
  echo "A MISSING measurement (no runner logs, 0 attempts, dead manager) is a FAIL, not a PASS."
  rm -rf "$logdir" "$badpath" 2>/dev/null; mkdir -p "$logdir" "$badpath"
  # executable, but not a valid executable format => execve gives ENOEXEC. Go's os/exec does NOT
  # fall back to a shell the way a shell would, so cmd.Start() fails outright.
  printf '\0\1\2\3 not a valid executable\n' > "$badpath/newgrp"
  chmod 755 "$badpath/newgrp"
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s local) with $badpath prepended to PATH; dev mode wipes the DB"
  osunset ; PATH="$badpath:$PATH" timeout 90 "$WR" manager start --deployment development -s local \
    --max_cores "$cores" --runner_filelog "$logdir" 2>&1 | grep -aE 'started on|token=' | head -2
  local mpid; mpid=$(mgr_pid "$DEV_RUN")
  { [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; } || die "could not start dev manager"
  echo "pid $mpid"
  sleep 3

  local rc=0
  ts_run "$jobs" "$secs" "$rg" "$logdir" "$work" "$retries" "$want" "$mpid" || rc=$?
  echo "## CLEANUP"
  cmd_stop >/dev/null 2>&1
  ef_reap_runners "$logdir"
  rm -rf "$logdir" "$badpath" 2>/dev/null; rm -f "$work" 2>/dev/null
  return "$rc"
}

# ts_run does the measured part of transient-start-retries, so the caller can always clean up the
# runner-log dir, the fake-newgrp dir and the job file without its `rm`s swallowing the FAIL exit
# code.
ts_run() {  # <jobs> <seconds> <repGroup> <logdir> <jobfile> <retries> <wantAttempts> <managerPid>
  local jobs="$1" secs="$2" rg="$3" logdir="$4" work="$5" retries="$6" want="$7" mpid="$8"
  local pat="exec format error" reason="command failed to start"
  echo "generating $jobs jobs -> $work"
  perl -e "for my \$i (1..$jobs){ print '{\"cmd\":\"echo transient-start '.\$i.'\",\"memory\":\"100M\",\"cpus\":1}'.\"\n\" }" \
    > "$work"
  local out arc
  out=$(osunset; timeout 300 "$WR" add -f "$work" --rep_grp "$rg" --retries "$retries" \
    --group "$(id -gn)" --deployment development 2>&1); arc=$?
  echo "$out" | tail -1
  if [ "$arc" -ne 0 ] || ! echo "$out" | grep -qE "Added $jobs new commands"; then
    echo "FAIL (NOT MEASURED): $jobs jobs were not added (manager up? see the wr add output above)"
    return 1
  fi

  echo "waiting up to ${secs}s for all $jobs to be buried (post-fix: after exactly $((retries + 1)) attempts each)."
  echo "NB: the release backoff starts at ClientReleaseDelayMin (30s) and doubles, so the expected"
  echo "    post-fix wall time is ~2 backoffs (~90-150s); a longer window only helps a pre-fix run climb."
  local counts="" buried=0 waited=0 attempts=0
  while [ "$waited" -lt "$secs" ]; do
    sleep 10; waited=$((waited + 10))
    counts=$(osunset; timeout 60 "$WR" status --deployment development -i "$rg" -o counts 2>/dev/null | tr '\n' ' ')
    buried=$(ef_num "$counts" buried)
    attempts=$(ef_attempts "$logdir" "$pat")
    echo "  t=${waited}s $rg[$counts] attempts=$attempts (want $want)"
    [ "$buried" -ge "$jobs" ] && break
  done

  # give any in-flight retry a moment to land, so an over-shooting run is caught rather than
  # measured mid-flight and reported as an exact hit.
  sleep 10
  attempts=$(ef_attempts "$logdir" "$pat")
  local logfiles; logfiles=$(find "$logdir" -type f 2>/dev/null | wc -l)
  local stanzas=0 reasoned=0 sr
  sr=$(ef_status_reasons "$rg" "$reason")
  stanzas=${sr%% *}; reasoned=${sr##* }
  case "$stanzas" in (*[!0-9]*|'') stanzas=0 ;; esac
  case "$reasoned" in (*[!0-9]*|'') reasoned=0 ;; esac
  echo "## VERDICT: buried=$buried/$jobs  execAttempts=$attempts (want $want)  runnerLogFiles=$logfiles"
  echo "##          failReason '$reason' on $reasoned/$stanzas reported jobs"
  echo "##          counts: $rg[$counts]"

  # a gate that PASSES when the measurement is missing is worse than no gate.
  local unmeasured=""
  [ "$jobs" -gt 0 ] || unmeasured="asked for $jobs jobs, so there was nothing to measure"
  [ -z "$unmeasured" ] && { ps -p "$mpid" >/dev/null 2>&1 || unmeasured="the dev manager (pid $mpid) died during the run"; }
  [ -z "$unmeasured" ] && [ -z "$counts" ] \
    && unmeasured="'wr status -i $rg -o counts' returned nothing, so no manager-side state was read"
  [ -z "$unmeasured" ] && [ "$logfiles" -le 0 ] \
    && unmeasured="no runner log files under $logdir, so no runner ever started (--runner_filelog broken?)"
  [ -z "$unmeasured" ] && [ "$attempts" -le 0 ] \
    && unmeasured="0 '$pat' lines in the runner logs, so the transient start failure never happened"
  [ -z "$unmeasured" ] && [ "$stanzas" -le 0 ] \
    && unmeasured="'wr status -i $rg --limit 0' reported no jobs, so no FailReason could be read"
  if [ -n "$unmeasured" ]; then
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $logdir and the counts above"
    return 1
  fi
  if [ "$buried" -ne "$jobs" ]; then
    echo "FAIL: only $buried/$jobs jobs were buried after ${waited}s ($attempts exec attempts, want $want)"
    echo "  => a pre-start release is not spending a retry, so --retries is ignored and this never ends"
    return 1
  fi
  if [ "$attempts" -ne "$want" ]; then
    echo "FAIL: $jobs jobs with --retries $retries cost $attempts exec attempts (want exactly $want)"
    echo "  => the retry budget is not being spent exactly once per attempt"
    return 1
  fi
  if [ "$reasoned" -ne "$jobs" ]; then
    echo "FAIL: only $reasoned/$jobs jobs give '$reason' as their problem ($stanzas jobs reported)"
    echo "  => the jobs ended for the wrong reason, so the operator cannot see what to fix"
    return 1
  fi
  echo "PASS: $jobs jobs with --retries $retries cost exactly $attempts exec attempts (== $want),"
  echo "      all $buried are buried, and all $reasoned give '$reason' as their problem"
  return 0
}

cmd_runner_log_bytes() {  # runner-log-bytes [jobs] [seconds] [cores] [padKB] - reliable4 ITEM C2 SCALE GATE
  # SCALE GATE for reliable4 ITEM C2 (.docs/reliable4/next-steps-260819.md): a runner must not
  # write its job's whole command line to its log. The command line is entirely user-supplied and
  # production's was routinely tens of KB (the 2026-08-17 profiling run measured a p99 of 24,261
  # bytes and a MAXIMUM of 1,345,498 bytes for a single `reserved a job` line), and every job was
  # logged that way FOUR times: `reserved a job`, `will start executing`, `started executing`
  # (client-side, inside Execute) and `command ... ran OK`. 0d22eda cut the multiplier - an
  # unrunnable job is attempted once rather than forever - but not the per-line size, so a run of
  # ordinary, SUCCESSFUL jobs still produced ~4x the command line per job in runner logs. That is
  # what made the production logs unreadable at exactly the moment they were needed.
  #
  # This is the measurement the handoff doc names as the honest one: log BYTES PER COMPLETED JOB
  # over a real run, using bkill-hygiene's log-bytes technique (11f1537). It is the only thing
  # that pins the three cmd/runner.go call sites, which no unit test can reach (they live inside a
  # cobra Run closure). End-to-end on the REAL binary and farm-safe: -s local, so no LSF job is
  # ever submitted and the only processes are our own dev manager plus at most `cores` local
  # runners, running `true` with a padded shell COMMENT so nothing is printed and nothing is slow.
  #
  # It measures FOUR things over `jobs` completed jobs, in two independent logs:
  #   bytesPerJob     = total size of every RUNNER log file / jobs      (runner volume)
  #   tailSentinel    = runner-log lines carrying the Cmd's distinctive TAIL (runner boundedness)
  #   mgrBytesPerJob  = size of the MANAGER's own log / jobs            (manager volume)
  #   mgrTailSentinel = manager-log lines carrying that same tail       (manager boundedness)
  # The sentinel is the sharp half: it sits at the very END of each Cmd, so it can only appear in
  # a log line that copied the command line WHOLE. A bytes threshold alone could be satisfied by
  # dropping a log line instead of bounding it; a sentinel count alone would not notice the volume
  # creeping back through some other line. Pre-fix all four fail.
  #
  # The manager is deliberately started with --debug, because that is production's own state (it
  # was turned on during the 260725 investigation, ~12MB/min, and the handoff doc's operational
  # notes say to confirm whether it is still on) and it is the regime the NEXT profiling round
  # will read. The manager's per-job lines - `reserved job`, `completed job`, `released job` /
  # `buried job`, `unburied job`, `removed job` - are clog.Debug, so they are invisible without it.
  # --debug is not propagated to runners (buildRunnerCmd, cmd/manager.go), so it does not perturb
  # the runner-log half of the measurement.
  #
  # A MISSING measurement (dead manager, nothing added, jobs never completed, no runner logs, zero
  # bytes in either log, or a padKB too small for the Cmd to dominate the fixed per-job log
  # overhead) is a hard FAIL, never a cheap PASS.
  need_bin; ensure_config
  local jobs="${1:-30}" secs="${2:-180}" cores="${3:-2}" padkb="${4:-20}"
  local rg=rgrunnerlog
  local logdir="$WRDEV_ROOT/runnerlog-runnerlogs" work="$WRDEV_ROOT/runnerlog.json"
  local maxper="${WRDEV_RUNLOG_MAX_BYTES_PER_JOB:-4096}" minpad="${WRDEV_RUNLOG_MIN_PADKB:-4}"
  # 8192 not 4096: the MEASURED post-fix figure on this host is 2,250 bytes/job at jobs=30, and the
  # manager log also carries fixed startup/scheduler debug lines that get divided by `jobs`, so a
  # small `jobs` would otherwise false-FAIL. Pre-fix is ~43,000 bytes/job (2 whole copies of a 20KB
  # Cmd on top of the same 2,250), so 8192 still discriminates by more than 5x - and the sentinel
  # check below is the sharp half regardless of padKB.
  local mgrmaxper="${WRDEV_MGRLOG_MAX_BYTES_PER_JOB:-8192}"
  echo "reliable4 ITEM C2 runner-log-bytes gate: $jobs SUCCESSFUL jobs each with a ~${padkb}KB Cmd,"
  echo "on an isolated dev manager (-s local, --max_cores $cores, --runner_filelog, --debug)."
  echo "prod pre-fix: 'reserved a job' p99 24,261 bytes, max 1,345,498 bytes, and 4 such lines per job."
  echo "Gate: runner-log bytes/completed job <= $maxper AND manager-log bytes/completed job <= $mgrmaxper"
  echo "      AND 0 lines in EITHER log carrying the Cmd's tail sentinel."
  echo "A MISSING measurement (no runner logs, 0 bytes, jobs never completed, dead manager) is a FAIL."
  rm -rf "$logdir" 2>/dev/null; mkdir -p "$logdir"
  cmd_stop >/dev/null 2>&1 || true
  rm -rf "$DEV_RUN.bak" 2>/dev/null; [ -d "$DEV_RUN" ] && mv "$DEV_RUN" "$DEV_RUN.bak"
  echo "starting isolated dev manager (-s local, --debug); dev mode wipes the DB"
  osunset ; timeout 90 "$WR" manager start --deployment development -s local --debug \
    --max_cores "$cores" --runner_filelog "$logdir" 2>&1 | grep -aE 'started on|token=' | head -2
  local mpid; mpid=$(mgr_pid "$DEV_RUN")
  { [ -n "$mpid" ] && ps -p "$mpid" >/dev/null 2>&1; } || die "could not start dev manager"
  echo "pid $mpid"
  sleep 3

  local rc=0
  rl_run "$jobs" "$secs" "$rg" "$logdir" "$work" "$padkb" "$mpid" "$maxper" "$minpad" \
    "$DEV_RUN/log" "$mgrmaxper" || rc=$?
  echo "## CLEANUP"
  cmd_stop >/dev/null 2>&1
  ef_reap_runners "$logdir"
  rm -rf "$logdir" 2>/dev/null; rm -f "$work" 2>/dev/null
  return "$rc"
}

# rl_run does the measured part of runner-log-bytes, so the caller can always clean up the
# runner-log dir and the (large) job file without its `rm`s swallowing the FAIL exit code.
rl_run() {  # <jobs> <secs> <repGroup> <logdir> <jobfile> <padKB> <mgrPid> <maxPerJob> <minPadKB> <mgrLog> <mgrMaxPerJob>
  local jobs="$1" secs="$2" rg="$3" logdir="$4" work="$5" padkb="$6" mpid="$7" maxper="$8" minpad="$9"
  local mgrlog="${10}" mgrmaxper="${11}"
  local sent=WRDEVCMDTAILSENTINEL
  # the index goes at the FRONT of the padding so every Cmd is unique (same Cmd+Cwd is the same
  # job key, so identical commands would be deduplicated down to one), leaving the sentinel at the
  # very end where only a whole-command-line copy can reach it.
  echo "generating $jobs unique jobs with a ~${padkb}KB padded Cmd -> $work"
  perl -e "my \$pad='c' x ($padkb*1024); for my \$i (1..$jobs){ print '{\"cmd\":\"true #'.\$i.' '.\$pad.'$sent\",\"memory\":\"100M\",\"cpus\":1}'.\"\n\" }" \
    > "$work"
  local out arc
  out=$(osunset; timeout 300 "$WR" add -f "$work" --rep_grp "$rg" \
    --deployment development 2>&1); arc=$?
  echo "$out" | tail -1
  if [ "$arc" -ne 0 ] || ! echo "$out" | grep -qE "Added $jobs new commands"; then
    echo "FAIL (NOT MEASURED): $jobs jobs were not added (manager up? see the wr add output above)"
    return 1
  fi

  echo "waiting up to ${secs}s for all $jobs to complete"
  local counts="" complete=0 waited=0
  while [ "$waited" -lt "$secs" ]; do
    sleep 5; waited=$((waited + 5))
    counts=$(osunset; timeout 60 "$WR" status --deployment development -i "$rg" -o counts 2>/dev/null | tr '\n' ' ')
    complete=$(ef_num "$counts" complete)
    echo "  t=${waited}s $rg[$counts] runnerLogBytes=$(rl_bytes "$logdir")"
    [ "$complete" -ge "$jobs" ] && break
  done

  local logbytes; logbytes=$(rl_bytes "$logdir")
  local logfiles; logfiles=$(find "$logdir" -type f 2>/dev/null | wc -l)
  local hits; hits=$(ef_attempts "$logdir" "$sent")
  local mgrbytes; mgrbytes=$(rl_bytes "$mgrlog")
  local mgrhits; mgrhits=$(ef_attempts "$mgrlog" "$sent")
  local perjob=0 mgrperjob=0
  [ "$jobs" -gt 0 ] && perjob=$((logbytes / jobs)) && mgrperjob=$((mgrbytes / jobs))
  echo "## VERDICT: complete=$complete/$jobs  runnerLogBytes=$logbytes  bytesPerJob=$perjob (max $maxper)"
  echo "##          tailSentinelLines=$hits (want 0)  runnerLogFiles=$logfiles  cmdBytes~$((padkb * 1024))"
  echo "##          managerLogBytes=$mgrbytes  mgrBytesPerJob=$mgrperjob (max $mgrmaxper)"
  echo "##          mgrTailSentinelLines=$mgrhits (want 0)  managerLog=$mgrlog"
  echo "##          counts: $rg[$counts]"

  # a gate that PASSES when the measurement is missing is worse than no gate.
  local unmeasured=""
  [ "$jobs" -gt 0 ] || unmeasured="asked for $jobs jobs, so there was nothing to measure"
  [ -z "$unmeasured" ] && { ps -p "$mpid" >/dev/null 2>&1 || unmeasured="the dev manager (pid $mpid) died during the run"; }
  [ -z "$unmeasured" ] && [ "$padkb" -lt "$minpad" ] \
    && unmeasured="padKB $padkb is under WRDEV_RUNLOG_MIN_PADKB $minpad: too small for the Cmd to dominate the fixed per-job log overhead (timestamps, keys, the other per-job lines), so bytes-per-job would stop measuring command-line volume. Note the pre-fix code still FAILS well below $minpad (the sentinel check catches it at any padKB, and the bytes check at ~2KB too), so this bound is conservative, not load-bearing"
  [ -z "$unmeasured" ] && [ -z "$counts" ] \
    && unmeasured="'wr status -i $rg -o counts' returned nothing, so no manager-side state was read"
  [ -z "$unmeasured" ] && [ "$complete" -ne "$jobs" ] \
    && unmeasured="only $complete/$jobs jobs completed in ${waited}s, so bytes-per-COMPLETED-job is not comparable"
  [ -z "$unmeasured" ] && [ "$logfiles" -le 0 ] \
    && unmeasured="no runner log files under $logdir, so no runner ever started (--runner_filelog broken?)"
  [ -z "$unmeasured" ] && [ "$logbytes" -le 0 ] \
    && unmeasured="the runner logs are empty, so nothing was logged to measure"
  [ -z "$unmeasured" ] && [ ! -f "$mgrlog" ] \
    && unmeasured="there is no manager log at $mgrlog, so the manager-log half was never measured"
  [ -z "$unmeasured" ] && [ "$mgrbytes" -le 0 ] \
    && unmeasured="the manager log $mgrlog is empty, so --debug is not writing the per-job lines this measures"
  if [ -n "$unmeasured" ]; then
    echo "FAIL (NOT MEASURED): $unmeasured"
    echo "  => this gate only reports PASS on a real measurement; inspect $logdir and the counts above"
    return 1
  fi
  if [ "$hits" -ne 0 ]; then
    echo "FAIL: $hits runner-log lines carry the END of the command line, so a whole ~$((padkb * 1024))-byte"
    echo "      Cmd is being copied into the log ($perjob bytes per completed job)"
    echo "  => bound it with internal.Abbreviate and keep the job key, so 'wr status' still yields the whole thing"
    return 1
  fi
  if [ "$mgrhits" -ne 0 ]; then
    echo "FAIL: $mgrhits MANAGER-log lines carry the END of the command line, so a whole"
    echo "      ~$((padkb * 1024))-byte Cmd is being copied into the manager's own log ($mgrperjob bytes per job)"
    echo "  => bound the per-job clog.Debug sites (reserved/completed/released/buried/unburied/removed job)"
    echo "     with job.loggableCmd() and keep the job key; that log is what the next prod profile reads"
    return 1
  fi
  if [ "$perjob" -gt "$maxper" ]; then
    echo "FAIL: $perjob runner-log bytes per completed job (want <= $maxper) for a ~$((padkb * 1024))-byte Cmd"
    echo "  => something on the per-job path is logging unbounded, user-supplied content"
    return 1
  fi
  if [ "$mgrperjob" -gt "$mgrmaxper" ]; then
    echo "FAIL: $mgrperjob MANAGER-log bytes per completed job (want <= $mgrmaxper) for a"
    echo "      ~$((padkb * 1024))-byte Cmd, with --debug on as production had it"
    echo "  => something on the manager's per-job path is logging unbounded, user-supplied content"
    return 1
  fi
  echo "PASS: $jobs jobs with a ~$((padkb * 1024))-byte Cmd each cost $perjob runner-log bytes and"
  echo "      $mgrperjob manager-log bytes per job (<= $maxper / $mgrmaxper), and no line in either log"
  echo "      carried the command line's tail"
  return 0
}

# rl_bytes prints the total size of every file under a runner-log dir, or 0 when there is nothing
# there (which its caller must treat as an unmeasured FAIL, never as a cheap PASS).
rl_bytes() {  # <logdir>
  local n
  n=$(find "$1" -type f -printf '%s\n' 2>/dev/null | awk '{t+=$1} END{printf "%d\n", t+0}')
  case "$n" in (*[!0-9]*|'') echo 0 ;; (*) echo "$n" ;; esac
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
  status-seed-overlap [overlap] [natBacklog]
                        reliable4 FINDING 7 gate: the scan-on-connect seed and the live delta
                        feed are not a consistent cut, so a transition straddling the seed is
                        counted TWICE and a never-reconnecting status page over-counts
                        `running` for the rest of the run (prod: 274 shown vs 4 real). Runs a
                        forced-interleaving shape (exact, non-flaky, the discriminating one)
                        and a natural one (the browser's own connect sequence) that measures
                        the accepted residual - the seed walk itself - by replaying one
                        recording both with and without the seed boundary, through the REAL
                        websocket-handler.js. No manager, no LSF (defaults 120 20000)
  overprovision-check [limit] [siblings] [ready]
                        deterministic prod-scale check: summed runners requested per limit
                        group stay <= the limit (fails on pre-fix per-group accounting; no manager)
  overcount-check [limit] [initialRunning] [windowReserves]
                        reliable3 2b GATE (deterministic, in-process, no manager): a group's
                        summed count must stay within its limit group's limit even when reserves
                        land between the early capacity read and the later running snapshot
                        (prod pre-fix: count=3313 for a 2000 limit). PASS = the arrangement
                        really over-counts before the cap (uncappedCount > limit) AND
                        0 < finalCount <= limit; FAIL = capGroupCountsToLimits is gone or has
                        started trimming to nothing, OR the arrangement produced no over-count
                        to cap, OR nothing was measured. Was an INVERTED reproducer (it asserted
                        the over-count was present, so it passed on the buggy code); converted
                        (defaults 2000 300 1500)
  limit-stall-check [limit] [ready]
                        reliable3 §1 GATE (deterministic, in-process, no manager), two halves:
                        (1) a limit group with every slot held schedules nothing - scheduled == 0
                        and skipped == ready, which is correct behaviour and unchanged; and
                        (2) a death confirmation that cannot succeed must WARN naming the host
                        and pid, and an unreadable ssh private key must WARN naming the path.
                        Half (2) was INVERTED (it asserted the ABSENCE of any log, so it passed
                        on the silent code); converted. FAIL = a held-slot group schedules work,
                        or either warning is gone or stops naming what to act on, OR nothing was
                        measured. The key facet needs a usable LSF and is reported as UNMEASURED
                        where there is none, never counted as coverage (defaults 2000 5000)
  priority-fairness-check [limit] [readyExtra]
                        reliable3 2a reproducer, still INVERTED ON PURPOSE because the defect is
                        OPEN: first-come budget allocation starves a higher-priority sibling
                        scanned after a low-priority one (no manager). It asserts the BUG, so
                        EXIT 0 MEANS THE BUG IS STILL PRESENT, not that an invariant holds - it
                        prints a banner saying so, since a zero exit read as good news in the
                        2026-08-27 sweep. Exit status here means "was anything measured": exit 1
                        ONLY when no measurement was produced. If the starvation ever stops
                        reproducing, the banner says so and asks for this mode to be converted
                        into a regression gate. Open item recorded in
                        .docs/reliable4/prod-validation-260827.md (defaults 2000 500)
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
  control-rpc-history [archived] [groups] [live]
                        reliable4 FINDING 1 SCALE GATE (real binary, farm-safe - no LSF job and no
                        runner is ever created): seeds a DB with N archived jobs over G rep groups,
                        points an isolated PROD-mode manager at it, adds L live ready-but-blocked
                        jobs (hslimit:0) in the first group, then TIMES the real 'wr limit',
                        'wr suspend -i <rg>' and 'wr resume -i <substr> -z' and reports the
                        manager's peak-RSS growth (defaults 200000 20 5000; needs ~2GB free in
                        \$WRDEV_ROOT/.wr-prod_production, removed again afterwards). Prod pre-fix:
                        'wr resume -i portal -z' timed out at 120s, kept scanning 12+ minutes, heap
                        348MB -> 12143MB. PASS = each command <= WRDEV_HS_MAX_MS (5000) with
                        <= WRDEV_HS_MAX_RSS_MB growth AND suspended == resumed == L; FAIL = a
                        command pays for the history again, OR it stopped acting on live jobs, OR
                        nothing was measured (no seed, dead manager, no reference history scan),
                        OR the seeded history is under WRDEV_HS_MIN_PERGROUP (1000) per group or
                        WRDEV_HS_MIN_ARCHIVED (100000) in total, which the pre-fix code passes too.
  dep-granularity-check [waiters] [members] [groups]
                        dep-granularity SCALE GATE (real binary, farm-safe - every fixture job waits
                        on a never-seen dep group, so nothing is ever scheduled and no runner or LSF
                        job is created): builds a DB of W jobs waiting on one M-member dep group
                        among G groups (TestDepGranularityFixture), points an isolated PROD-mode
                        manager at a copy of it, and reports peak RSS, the recovery window between
                        the two 'recovering' log lines, what 'wr manager status' says INSIDE that
                        window, and how long 'wr add' of one more member takes (defaults 30000 3000
                        6300; a ~110MB fixture, copied per run and the copy removed again). Prod
                        pre-fix: 150,472 live jobs, 97.55% of a 15.65GB heap in recoveredItemDef,
                        42m56s to recover, five OOM kills at ~180GB. PASS = peakRssMb <=
                        WRDEV_DEPGRAN_MAX_RSS_MB, recoverySec <= WRDEV_DEPGRAN_MAX_RECOVERY_SEC,
                        addSec <= WRDEV_DEPGRAN_MAX_ADD_SEC and statusInWindow starting/up;
                        FAIL = a threshold exceeded, recovery started and never finished (the
                        pre-fix outcome), 'wr manager status' non-responsive or never sampled, OR
                        nothing was measured.
                        WRDEV_DEPGRAN_DB=<file> measures a pre-built fixture instead of generating
                        one, which is how the pre-fix worktree (8b9ba00) gets a fixture at all;
                        WRDEV_DEPGRAN_ABORT_RSS_MB (16000) caps the pre-fix run's memory.
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
  exec-impossible-retries [jobs] [seconds] [cores]
                        reliable4 FINDING 5 SCALE GATE (real binary, farm-safe - -s local, no LSF job
                        ever submitted): N jobs whose Cmd is over Linux's 128KB MAX_ARG_STRLEN, so
                        fork/exec can NEVER succeed. Runs the dev manager with --runner_filelog and
                        asserts each such job costs exactly ONE runner slot and ONE attempt: buried
                        == N (manager side) AND 'argument list too long' lines == N (runner side)
                        AND 'command line too long' as the problem of all N (the right CAUSE, so a
                        blanket bury-every-start-failure cannot pass on the counts alone)
                        (defaults 20 120 2; WRDEV_EXECFAIL_PADKB overrides the 130KB Cmd padding).
                        Pre-fix: buried == 0 (the retry ceiling is never reached because StartTime
                        is never set) and attempts >> N and still climbing.
  transient-start-retries [jobs] [seconds] [cores] [retries]
                        reliable4 ITEM A SCALE GATE (real binary, farm-safe - -s local, no LSF job
                        ever submitted): N jobs with --retries R whose cmd.Start() fails with a
                        TRANSIENT errno every time (they are added with --group, so buildExecCmd
                        execs the bare name `newgrp`, and a `newgrp` that is executable but not a
                        valid executable format is prepended to the manager's PATH => ENOEXEC, which
                        permanentStartFailReason deliberately keeps retryable). Asserts the retries
                        are actually SPENT: buried == N (manager side) AND 'exec format error' lines
                        == N*(R+1) (runner side) AND 'command failed to start' as the problem of all
                        N (defaults 20 300 2 2). Pre-fix: buried == 0 and attempts climb past
                        N*(R+1) without bound, because UntilBuried only ever decremented for a job
                        whose StartTime was set and a start that never happened never sets it.
  bkill-hygiene [elements] [hangSeconds]
                        reliable4 FINDING 4 SCALE GATE (fake bjobs/bkill exes, so farm-safe - no
                        manager, no bsub, no real bkill - but the REAL killExcessCmds path, driven at
                        the prod-measured element count): asserts no single bkill argv exceeds the
                        1000-id cap, the batches cover every excess element, one cycle logs <= 4096
                        bytes, the next cycle re-issues NONE of the same ids, the summary reports the
                        killed-vs-alreadyGone split, and a HUNG bkill returns within 90s
                        (defaults 1900 120). Pre-fix: one ~1,900-id argv, ~104KB of log per cycle,
                        all 1,900 ids re-killed on the next cycle, no killed/gone split, and a hung
                        bkill blocks the kill path for as long as it hangs.
  runner-log-bytes [jobs] [seconds] [cores] [padKB]
                        reliable4 ITEM C2 SCALE GATE (real binary, farm-safe - -s local, no LSF job
                        ever submitted): N SUCCESSFUL jobs whose Cmd carries a ~padKB padded shell
                        comment, run under --runner_filelog and --debug, then measures BYTES PER
                        COMPLETED JOB in the runner logs AND in the manager's own log, and counts
                        lines in each carrying the Cmd's tail sentinel. PASS = runner bytes/job <=
                        WRDEV_RUNLOG_MAX_BYTES_PER_JOB (4096), manager bytes/job <=
                        WRDEV_MGRLOG_MAX_BYTES_PER_JOB (8192) and 0 sentinel lines in either
                        (defaults 30 180 2 20). Pre-fix: ~4 copies of the whole Cmd per job in the
                        runner log (`reserved a job`, `will start executing`, `started executing`,
                        `command ... ran OK`) and 2 more in the manager log (`reserved job`,
                        `completed job`), so the sentinel is present in both. It is the only thing
                        that pins the cmd/runner.go call sites, which no unit test can reach, and
                        the only end-to-end check of the manager log with --debug on, as prod had it.
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
  status-seed-overlap) cmd_status_seed_overlap "${2:-120}" "${3:-20000}" ;;
  overprovision-check) cmd_overprovision_check "${2:-2000}" "${3:-50}" "${4:-5000}" ;;
  overcount-check) cmd_overcount_check "${2:-2000}" "${3:-300}" "${4:-1500}" ;;
  limit-stall-check) cmd_limit_stall_check "${2:-2000}" "${3:-5000}" ;;
  priority-fairness-check) cmd_priority_fairness_check "${2:-2000}" "${3:-500}" ;;
  backlog-rescan-check) cmd_backlog_rescan_check "${2:-2000}" "${3:-50000}" ;;
  idle-backlog-cpu) cmd_idle_backlog_cpu "${2:-50000}" "${3:-25}" "${4:-6063}" ;;
  bkill-hygiene) cmd_bkill_hygiene "${2:-1900}" "${3:-120}" ;;
  control-rpc-history) cmd_control_rpc_history "${2:-200000}" "${3:-20}" "${4:-5000}" ;;
  dep-granularity-check) cmd_dep_granularity_check "${2:-}" "${3:-}" "${4:-}" ;;
  runner-started-timeout-check) cmd_runner_started_timeout_check ;;
  exec-impossible-retries) cmd_exec_impossible_retries "${2:-20}" "${3:-120}" "${4:-2}" ;;
  transient-start-retries) cmd_transient_start_retries "${2:-20}" "${3:-300}" "${4:-2}" "${5:-2}" ;;
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
  runner-log-bytes) cmd_runner_log_bytes "${2:-30}" "${3:-180}" "${4:-2}" "${5:-20}" ;;
  prod-start) cmd_prod_start "${2:-local}" ;;
  prod-stop) cmd_prod_stop ;;
  crash-recovery) cmd_crash_recovery ;;
  dump) cmd_dump "${2:-lsf}" ;;
  clean) cmd_clean ;;
  status) cmd_status ;;
  help|-h|--help) usage ;;
  *) usage; exit 1 ;;
esac
