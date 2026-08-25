#!/usr/bin/env bash
# RED for: dbUpgradeReporter's progress/phase lines are invisible in the manager
# log on a default `wr manager start`, because they go through clog.Info on the
# warn-filtered server context.
#
# Triggers the real index rebuild by deleting bucketDepGroups from an existing DB
# (initDB rebuilds it when openedExistingDB && !hadDepGroups), then restarts with
# DEFAULT logging and looks for the reporter's own phase line in the manager log.
set -u
ROOT="${RED_ROOT:?set RED_ROOT}"
DEL="${DELBUCKET:?set DELBUCKET to the delbucket binary}"
PORT=51982
WEB=51983
WR="$ROOT/wr"
export WR_CONFIG_DIR="$ROOT/config"
RUN="$ROOT/.wr-red_production"
DB="$ROOT/.wr-red_production/db"
NJOBS="${NJOBS:-2000}"
LIMITGRP=redhold
D=(--deployment production)
# what the two arms are counted with. It is the reporter's own message prefix,
# not a loose "rebuild|upgrade" match, because the counts are compared for
# equality: an unrelated debug line that merely mentions an upgrade would make
# the --debug arm look bigger than the default arm forever.
UPGRADE_MSG='msg="database upgrade'

mkdir -p "$WR_CONFIG_DIR" "$ROOT/cwd"
cat > "$WR_CONFIG_DIR/.wr_config.production.yml" <<CFG
managerport: "$PORT"
managerweb: "$WEB"
managerhost: "localhost"
managerdir: "$ROOT/.wr-red"
CFG

is_ours() { ps -o cmd= -p "$1" 2>/dev/null | grep -qF "$WR"; }
kill_manager() { local p; p=$(cat "$RUN/pid" 2>/dev/null || true); [ -n "$p" ] && is_ours "$p" && kill -9 "$p" 2>/dev/null; return 0; }

# descendants prints every descendant pid of the given pid. A job's command runs
# as a child of one of our runners and does NOT carry $WR in its cmdline, so
# killing only the processes is_ours matches orphans it (ppid 1) on this shared
# host until it exits on its own. LIMITGRP below stops jobs running at all, so
# there should be nothing here to kill; this is the backstop if one ever does.
descendants() {
  local kid
  for kid in $(pgrep -P "$1" 2>/dev/null); do
    echo "$kid"
    descendants "$kid"
  done
}

our_pids() { local p; for p in $(pgrep -f "$WR" 2>/dev/null); do [ "$p" = "$$" ] && continue; is_ours "$p" && echo "$p"; done; }

cleanup() {
  local p kids=""
  # collect children while their parents are still alive, then kill both
  for p in $(our_pids); do kids="$kids $(descendants "$p")"; done
  kill_manager
  for p in $(our_pids); do kill -9 "$p" 2>/dev/null; done
  for p in $kids; do kill -9 "$p" 2>/dev/null; done
  return 0
}
trap cleanup EXIT
wait_port_free() {
  for _ in $(seq 1 60); do
    ss -ltn 2>/dev/null | grep -q ":$PORT " || return 0
    sleep 1
  done
  echo "FAIL (NOT MEASURED): port $PORT still bound"; exit 3
}
start() { unset $(compgen -v | grep '^OS_' 2>/dev/null) 2>/dev/null || true
          timeout 180 "$WR" manager start "${D[@]}" -s local 2>&1 | grep -aE 'started on' | head -1; }

echo "== phase 1: build a db with $NJOBS jobs =="
cleanup; sleep 2; rm -rf "$RUN" "$ROOT/.wr-red"* 2>/dev/null; wait_port_free
out=$(start); echo "$out"
case "$out" in *"started on"*) ;; *) echo "FAIL (NOT MEASURED): manager did not start in phase 1"; exit 3;; esac
sleep 3
# the jobs only have to exist in the db as incomplete work, so they are added to
# a limit group capped at 0: nothing ever runs, so this leaves no job processes
# behind on a shared host, and every job stays live for the restart to recover.
seq 1 "$NJOBS" | sed 's|^|sleep 120 # up|' \
  | timeout 120 "$WR" add "${D[@]}" --cwd "$ROOT/cwd" -l "$LIMITGRP:0" >/dev/null \
  || { echo "FAIL (NOT MEASURED): add failed"; exit 3; }
sleep 4
kill_manager; sleep 3
[ -s "$DB" ] || { echo "FAIL (NOT MEASURED): no db at $DB"; exit 3; }

echo "== phase 2: delete the dep-group index so the next open rebuilds it =="
"$DEL" "$DB" depgroups || { echo "FAIL (NOT MEASURED): could not delete bucket"; exit 3; }
mv -f "$RUN/log" "$RUN/log.phase1" 2>/dev/null || true
rm -f "$DB.upgrade" 2>/dev/null || true

echo "== phase 3: restart with DEFAULT logging =="
wait_port_free
out=$(start); echo "$out"
case "$out" in *"started on"*) ;; *) echo "FAIL (NOT MEASURED): manager did not start for the default-level run"; exit 3;; esac
sleep 8
grep -aq 'msg="recovering prior state"' "$RUN/log" || { echo "FAIL (NOT MEASURED): default-level manager never recovered, so it never opened the db"; exit 3; }

# non-vacuity: prove the rebuild actually ran, via the sidecar the reporter writes
if [ -e "$DB.upgrade" ]; then
  echo "sidecar written (rebuild ran): $(head -c 200 "$DB.upgrade")"
else
  echo "NOTE: no sidecar left behind (it is removed on success); checking the log for any upgrade trace"
fi

echo "--- manager log, distinct msgs ---"
grep -aoP 'msg="[^"]{0,60}' "$RUN/log" | sort | uniq -c | sort -rn | head
echo "--- lines about the rebuild ---"
grep -aiE "rebuild|upgrade" "$RUN/log" | cut -c1-180 | head -5
n=$(grep -acF "$UPGRADE_MSG" "$RUN/log" || true)
echo "manager-log lines mentioning the rebuild/upgrade: ${n:-0}"

# CONTROL: the same rebuild with --debug must show the lines, proving the rebuild
# really ran and that the only thing hiding it is the log level.
echo "== control: same rebuild, this time with --debug =="
kill_manager; sleep 2
"$DEL" "$DB" depgroups >/dev/null || { echo "FAIL (NOT MEASURED): control setup failed"; exit 3; }
mv -f "$RUN/log" "$RUN/log.default" 2>/dev/null || true
unset $(compgen -v | grep '^OS_' 2>/dev/null) 2>/dev/null || true
wait_port_free
cout=$(timeout 180 "$WR" manager start "${D[@]}" -s local --debug 2>&1 | grep -aE 'started on' | head -1); echo "$cout"
case "$cout" in *"started on"*) ;; *) echo "FAIL (NOT MEASURED): manager did not start for the --debug control"; exit 3;; esac
sleep 8
d=$(grep -acF "$UPGRADE_MSG" "$RUN/log" || true)
echo "--- with --debug, lines about the rebuild: ${d:-0} ---"
grep -aiE "rebuild|upgrade" "$RUN/log" | cut -c1-170 | head -3
[ "${d:-0}" -ge 1 ] || { echo "FAIL (NOT MEASURED): even --debug shows nothing, so the rebuild did not run"; exit 3; }

# the gate is equality, not "at least one": the fixture's jobs carry no dep
# groups, so the rebuild processes 0 entries and logs no progress lines at all,
# leaving both arms with exactly the same set of milestone lines. A fix that
# promoted only some of them - say the first - would pass an "at least one" gate
# while leaving an operator just as unable to tell a rebuild from a hang.
if [ "${n:-0}" -eq "${d:-0}" ]; then
  echo "PASS: all ${d} rebuild line(s) the --debug run logs are in the default manager log too"
  exit 0
fi
echo "RED: the rebuild logs ${d} line(s) under --debug and ${n:-0} at the default level"
exit 1
