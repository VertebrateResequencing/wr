#!/usr/bin/env bash
# RED command for: prior-state recovery is invisible on a default `wr manager start`.
#
# Drives the REAL binary with DEFAULT logging (no --debug) in a fully isolated
# production-mode deployment: adds incomplete jobs, kill -9s the manager (so the
# live bucket keeps them), restarts it, and asserts the new log says recovery
# started (with a total) and finished. Nothing here touches a real manager.
set -u

ROOT="${RED_ROOT:?set RED_ROOT}"
PORT=51982
WEB=51983
WR="$ROOT/wr"
export WR_CONFIG_DIR="$ROOT/config"
RUN="$ROOT/.wr-red_production"
NJOBS="${NJOBS:-12}"

mkdir -p "$WR_CONFIG_DIR" "$ROOT/cwd"
cat > "$WR_CONFIG_DIR/.wr_config.production.yml" <<CFG
managerport: "$PORT"
managerweb: "$WEB"
managerhost: "localhost"
managerdir: "$ROOT/.wr-red"
CFG

is_ours() { ps -o cmd= -p "$1" 2>/dev/null | grep -qF "$WR"; }

kill_manager() {
  local pid; pid=$(cat "$RUN/pid" 2>/dev/null || true)
  [ -n "$pid" ] || return 0
  if is_ours "$pid"; then kill -9 "$pid" 2>/dev/null; echo "killed our manager pid $pid"; fi
}

cleanup() {
  kill_manager
  # reap anything still running our isolated binary (runners), never anything else
  for p in $(pgrep -f "$WR" 2>/dev/null); do
    [ "$p" = "$$" ] && continue
    is_ours "$p" && kill -9 "$p" 2>/dev/null
  done
}
trap cleanup EXIT

start_manager() {
  unset $(compgen -v | grep '^OS_' 2>/dev/null) 2>/dev/null || true
  timeout 120 "$WR" manager start --deployment production -s local 2>&1 \
    | grep -aE 'started on' | head -1
}

echo "== phase 1: fresh manager, add $NJOBS incomplete jobs =="
cleanup; sleep 1
rm -rf "$RUN" "$ROOT/.wr-red"* 2>/dev/null
start_manager || { echo "FAIL (NOT MEASURED): manager would not start"; exit 3; }
sleep 3
for i in $(seq 1 "$NJOBS"); do echo "sleep 600 # red$i"; done \
  | timeout 60 "$WR" add --deployment production --cwd "$ROOT/cwd" >/dev/null \
  || { echo "FAIL (NOT MEASURED): could not add jobs"; exit 3; }
sleep 6
live=$(timeout 60 "$WR" status --deployment production -o counts 2>/dev/null | tr -d ' ')
echo "counts before kill: ${live:-<none>}"

echo "== phase 2: kill -9 (unclean, so the live jobs stay in the db) =="
kill_manager; sleep 3

echo "== phase 3: restart with DEFAULT logging and read the NEW log =="
start_manager || { echo "FAIL (NOT MEASURED): manager would not restart"; exit 3; }
sleep 20
LOG="$RUN/log"
[ -s "$LOG" ] || { echo "FAIL (NOT MEASURED): no log at $LOG"; exit 3; }

echo "--- new log, all distinct msgs ---"
grep -oP 'msg="[^"]{0,60}' "$LOG" | sort | uniq -c | sort -rn
echo "--- lines mentioning recover ---"
grep -i "recover" "$LOG" | grep -v 'err="jobqueue' | sed 's/^/  /' | head -10

started=$(grep -c 'msg="recovering prior state"' "$LOG" 2>/dev/null || true)
done_=$(grep -c 'prior state recovered' "$LOG" 2>/dev/null || true)
echo
echo "recovery-start lines:    ${started:-0}"
echo "recovery-finish lines:   ${done_:-0}"

if [ "${started:-0}" -ge 1 ] && [ "${done_:-0}" -ge 1 ]; then
  echo "PASS: a default start reports recovery start and finish"
  exit 0
fi

echo "RED: a default \`wr manager start\` never says it is recovering prior state"
exit 1
