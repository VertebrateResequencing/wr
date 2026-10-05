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
