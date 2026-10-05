# Developer tooling: manager pids and restart overlap (2026-10-05)

- Branch: `devtool-pids-14defb3a`
- Base: `origin/develop` at `545e67d4` (#679)
- Queue owner: this branch and this checklist
- Source: battery10 findings F2 and F3. Evidence, read only, is in
  `/nfs/hgi/wr/sb10-bigdb/battery10/RESULTS.md` and the paths it cites.

Developer tooling only, so there are no Go tests or CHANGELOG entry. Each
item is proved by running the tool, with the local scheduler, an isolated
`WRDEV_ROOT` and ports 51970-51977 (free per `ss -ltn` first), and with
`bjobs`, `bkill`, `bsub` and `bqueues` replaced on `PATH` by stubs that exit 0,
since `wrdev.sh start` and `stop` bkill every `wrd_*` job of this user. Gates:
`bash -n` on each edited script (shellcheck is not installed on this host).

- [x] F2: `developers/wrdev.sh dump` reads the manager pid from
  $WRDEV_ROOT/.wr_development/pid, but a foreground `wr manager start -f` never
  writes a pid file (only daemonize() does, cmd/manager.go ~251), so dump
  prints a previous daemon's stale pid; developers/soak/sweep.sh's dump
  handling then SIGQUITs/kill -9s that stale pid (dangerous: it could be an
  unrelated process if the pid was reused) and leaves the real foreground
  manager RUNNING (the battery found it still up holding ports after "sweep
  done"). Fix so dump/sweep identify the actual manager they started and never
  signal a pid whose cmdline isn't ours; make sweep.sh verify before kill and
  fail loudly rather than leave a manager running.
  - Source: battery10 F2.
  - Red: a harness running `wrdev.sh start local; wrdev.sh stop; wrdev.sh dump
    local`, comparing the pid dump prints with `pgrep -f "^$WRDEV_ROOT/wr
    manager start --deployment development -s local -f"`, then running
    `wrdev.sh stop` and checking that the foreground manager has gone. It
    exited 1 before the fix:

    ```text
    dump printed pid: '791220'  actual foreground manager: '791830'
    after wrdev.sh stop, foreground manager still running: '791830'
    FAIL: dump printed the wrong pid
    FAIL: wrdev.sh stop left the foreground manager running
    ```

    The base `sweep.sh <dir> dump` (LSF stubbed) exited 0 with `goroutines in
    SIGQUIT dump: 0`, and its root's foreground manager was still running
    afterwards, as in battery10.
  - Fix: `cmd_dump` records the foreground manager's own pid (`$!` of the
    nohup, which execs wr) in `$DEV_RUN/pid`, where wr's daemon would have
    written it, and dies unless that pid is alive and `is_ours` 8s later. So
    `dump`'s printout, `stop`, `clean`, `ensure_dev_manager` and `sweep.sh`
    all find the manager dump started. `sweep.sh` signals the pid only if its
    command line is exactly this root's `$WRDEV_ROOT/wr manager start
    --deployment development ... -f` (`our_fg_manager`); otherwise it logs a
    `SWEEP ERROR` and takes no dump. Afterwards it kills any foreground manager
    of this root still running, verified the same way, logs a `SWEEP ERROR`
    for it, adds `nodump` or `leftover` to the mode's rc, and the sweep exits
    1.
  - After: the harness prints `PASS` (dump printed 798673, the actual
    foreground manager was 798673, and none was running after `stop`).
    `sweep.sh <dir> prod-start prod-stop dump` exited 0 with all three
    `rc=0`, `goroutines in SIGQUIT dump: 38`, and no manager of its root left.
    The fixed `sweep.sh` driving the base `wrdev.sh` (`SWEEP_WRDEV`), with the
    pid file pre-seeded with the pid of an unrelated `sleep 300`, logged both
    `SWEEP ERROR` lines, recorded `rc=0,nodump,leftover`, exited 1, left no
    manager running and did not signal the `sleep`.
  - Other places checked: the soak scripts read
    `.wr-prod_production/pid`, which the daemonised prodsim manager writes;
    those that signal it (`crashon.sh`, `crashafter.sh`, `relbury.sh`) go
    through `soak_manager_pid`, which checks the command line, and the rest
    (`stall.sh`, `stopstate.sh`, `stopwatch.sh`) only log the pid. wrdev.sh's
    other foreground manager (`dep_granularity_run`) keeps `$!` and kills it
    by that, and `dep_granularity_sweep` finds leftovers by command line. Every
    other `mgr_pid` reader goes through `safe_kill`/`is_ours`.
  - Files: `developers/wrdev.sh`, `developers/soak/sweep.sh`.

- [x] F3: wrdev.sh prodsim's restart scheduler doesn't wait for an injector's
  restart: injectors (developers/soak/crashon.sh, crashafter.sh, relbury.sh)
  wait for the scheduler, but not the reverse, so a scheduled restart can hit
  a manager an injector has just started mid-startup (seen twice in
  soak9/battery10: a clean stop got rc=killed; a scheduled crash killed a
  manager before it was ready; the injector recorded `start rc=1` and the
  watcher a spurious failure). Fix with a shared lock taken by both the
  scheduler and every injector around their kill+start (and the watcher's
  restart), so restarts never overlap; keep their timing otherwise unchanged.
  - Source: battery10 F3 (`soak/run/prodsim-1791171218/restarts.tsv`,
    `watcher.log`).
  - Cause: the injectors only poll `soak_wr_running "(start|stop)"` before
    their kill, which is check-then-act and misses the scheduler's restart
    between `wr` commands (its pre-stop profiles, and the whole of a crash
    restart, which runs no `wr manager` command before the start). The
    scheduler checks nothing.
  - Red: a harness ran `wrdev.sh prodsim 0.04 6 0.02` (local scheduler,
    `WRDEV_PRODSIM_RESTART_MIN=1`, `WRDEV_PRODSIM_RESTART_KINDS=crash`,
    `-portal-jobs 10 -web-clients 1`) with every `wr manager start` slowed by
    20s through a wrapper around the isolated binary (as `run.sh`'s
    `RUNNER_FILELOG` wrapper does), plus `crashafter.sh <out> 1 50`, timed so
    its kill and start straddle the first scheduled restart. Verdict from
    `restarts.tsv`, whose lines are appended in real-time order: FAIL if a
    stop or start line of one restart falls between the other's stop and
    start lines. Before the fix it exited 1:

    ```text
    1791185079 stop rc=crash pid=886730 ms=5 manual=crashafter
    1791185088 stop rc=skipped pid=886730 ms=41 scheduled
    1791185102 start rc=0 pid=892798 ms=20342 manual=crashafter
    1791185109 start rc=1 pid=892798 ms=20045 scheduled
    FAIL: line 3 (scheduled stop) came inside the crashafter restart begun at line 2
    FAIL: line 4 (crashafter start) came inside the scheduled restart begun at line 3
    ```

  - Fix: every restart of the prodsim manager holds `<outdir>/restart.lock`
    with `flock`: wrdev.sh's scheduled `prodsim_restart` and the cleanup's
    final stop and kill loop (`prodsim_restart_locked`), and in
    developers/soak (`soak_restart_locked` in `config.sh`) `crashafter.sh`'s
    and `crashon.sh`'s kill and start, `relbury.sh`'s, and `watcher.sh`'s
    decision and start after a failed start. The injectors and the watcher
    now look up the pid inside the lock, so they act on the manager that is
    up once the lock is theirs. `relbury.sh`'s kill must land at
    D+killDelayMs, so it uses `-n` and skips its crash, as it already did for
    a `wr` stop or start in progress, when another restart holds the lock.
    The locked command runs with the lock's fd closed, so a manager it
    starts never inherits the lock, and the lock is released with `flock -u`
    because subshells the command forks (the post-start profiler) share it.
    Timing is otherwise unchanged; a scheduled restart that comes due during
    an injector's restart now starts when that one ends.
  - After: the same harness passed (scheduled restart 1's stop line came
    after crashafter's start line, and every start had `rc=0`):

    ```text
    1791186069 stop rc=crash pid=964725 ms=6 manual=crashafter
    1791186093 start rc=0 pid=971201 ms=20813 manual=crashafter
    1791186093 stop rc=crash pid=971201 ms=78 scheduled
    1791186114 start rc=0 pid=973222 ms=20334 scheduled
    PASS: no restart began or ended inside another
    ```

    Also run: `soak_restart_locked -n` returned 75 without running its
    command while another process held the lock, the blocking form waited
    the 4s until it was released, and the command's exit status passed
    through. A child the command left running did not hold the lock, nor did
    a daemonised manager started under it through `soak_start_manager`
    (`/proc/<pid>/fd` has no `restart.lock`). `watcher.sh` given a failed
    start line, with the lock held from 30s to 95s, started the manager at
    115s (its 65s wait, then the lock, then the 20s slowed start).
    `relbury.sh <out> 0 r1 4 500 15` on a local manager killed it at D+600ms
    and restarted it (`start rc=0`); `r2`, with the lock held across its kill
    time, logged `another restart holds the restart lock at D+500ms; no
    relbury crash` and added no line to `restarts.tsv`.
  - Not covered: `crashon.sh` was not run end to end; its change is the same
    move into `crash()` under `soak_restart_locked` as `crashafter.sh`'s.
  - Files: `developers/wrdev.sh`, `developers/soak/config.sh`,
    `developers/soak/crashafter.sh`, `developers/soak/crashon.sh`,
    `developers/soak/relbury.sh`, `developers/soak/watcher.sh`,
    `developers/soak/README.md`.
