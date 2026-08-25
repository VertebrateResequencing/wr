#!/usr/bin/env bash
# RED for: wr status / wr manager status cannot tell "recovering" from "empty".
# Adds N jobs to an isolated production-mode manager, kill -9s it so they stay
# live, restarts it, and polls both status commands hard through the recovery
# window, recording whether ANY sample tells the operator recovery is in progress.
set -u
ROOT="${RED_ROOT:?set RED_ROOT}"
PORT=51982
WEB=51983
WR="$ROOT/wr"
export WR_CONFIG_DIR="$ROOT/config"
RUN="$ROOT/.wr-red_production"
NJOBS="${NJOBS:-30000}"
D=(--deployment production)

mkdir -p "$WR_CONFIG_DIR" "$ROOT/cwd"
cat > "$WR_CONFIG_DIR/.wr_config.production.yml" <<CFG
managerport: "$PORT"
managerweb: "$WEB"
managerhost: "localhost"
managerdir: "$ROOT/.wr-red"
CFG

is_ours() { ps -o cmd= -p "$1" 2>/dev/null | grep -qF "$WR"; }
kill_manager() { local p; p=$(cat "$RUN/pid" 2>/dev/null || true); [ -n "$p" ] && is_ours "$p" && kill -9 "$p" 2>/dev/null; return 0; }
cleanup() { kill_manager; for p in $(pgrep -f "$WR" 2>/dev/null); do [ "$p" = "$$" ] && continue; is_ours "$p" && kill -9 "$p" 2>/dev/null; done; }
trap cleanup EXIT
start() { unset $(compgen -v | grep '^OS_' 2>/dev/null) 2>/dev/null || true
          timeout 180 "$WR" manager start "${D[@]}" -s local 2>&1 | grep -aE 'started on' | head -1; }

echo "== phase 1: $NJOBS live jobs =="
cleanup; sleep 1; rm -rf "$RUN" "$ROOT/.wr-red"* 2>/dev/null
start || { echo "FAIL (NOT MEASURED): no manager"; exit 3; }
sleep 3
seq 1 "$NJOBS" | sed 's|^|sleep 600 # red|' \
  | timeout 300 "$WR" add "${D[@]}" --cwd "$ROOT/cwd" >/dev/null \
  || { echo "FAIL (NOT MEASURED): add failed"; exit 3; }
sleep 5
before=$(timeout 60 "$WR" status "${D[@]}" -o counts 2>/dev/null | tr -d ' \n')
echo "counts before kill: ${before:-<none>}"
case "$before" in *ready:*) ;; *) echo "FAIL (NOT MEASURED): jobs not added"; exit 3;; esac
kill_manager; sleep 3
# start the restart from a FRESH log, so nothing below reads the first manager's lines
mv -f "$RUN/log" "$RUN/log.phase1" 2>/dev/null || true

echo "== phase 2: restart, poll both status commands through the window =="
start &
SAMPLES="$ROOT/samples.txt"; : > "$SAMPLES"
for i in $(seq 1 400); do
  {
    echo "--- mgrstatus $i @$(date +%s.%N)"; timeout 5 "$WR" manager status "${D[@]}" 2>&1
    echo "--- status $i @$(date +%s.%N)";    timeout 5 "$WR" status "${D[@]}" -o counts 2>&1
  } >> "$SAMPLES" 2>&1
  grep -aq 'prior state recovered' "$RUN/log" 2>/dev/null && { echo "recovery finished at sample $i"; break; }
done
wait 2>/dev/null

W=$(grep -a 'msg="recovering prior state"' "$RUN/log" | grep -oP '^t=\K\S+' | head -1)
F=$(grep -a 'prior state recovered' "$RUN/log" | grep -oP '^t=\K\S+' | head -1)
echo "recovery window: ${W:-?} -> ${F:-?}"
[ -n "$W" ] && [ -n "$F" ] || { echo "FAIL (NOT MEASURED): no recovery window in the log"; exit 3; }
ws=$(date -d "$W" +%s); fs=$(date -d "$F" +%s)
inside=$(grep -oP '^--- \w+ \d+ @\K[0-9.]+' "$SAMPLES" \
  | awk -v a="$ws" -v b="$fs" '{t=int($1); if (t>=a && t<=b) n++} END{print n+0}')
echo "samples taken: $(grep -c -- '--- mgrstatus' "$SAMPLES"), of which inside the window: $inside"
[ "${inside:-0}" -ge 2 ] || { echo "FAIL (NOT MEASURED): fewer than 2 samples landed inside the recovery window"; exit 3; }

echo "--- any sample mentioning recovery ---"
grep -ain "recover" "$SAMPLES" | head -5
n=$(grep -aic "recover" "$SAMPLES" || true)
echo "samples mentioning recovery: ${n:-0}"
if [ "${n:-0}" -ge 1 ]; then echo "PASS: an operator can see recovery in progress"; exit 0; fi
echo "RED: neither 'wr manager status' nor 'wr status' ever says the manager is recovering"
exit 1
