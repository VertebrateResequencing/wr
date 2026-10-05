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

- [x] wrdev.sh's bkill_dev runs `bkill -J 'wrd_*' 0` for every dev job of
  this user, whichever dev manager owns it, even with `-s local`, so it can
  kill other sessions' jobs. Scope it to jobs belonging to this WRDEV_ROOT's
  manager, and skip it entirely when the manager was started with the local
  scheduler.
  - Source: incidental, found while verifying F2 (every `wrdev.sh start`,
    `stop`, `dump` and `clean` in those checks reached `bkill -J wrd_* 0`,
    absorbed by the LSF stubs).
  - Cause: dev managers were started without `WR_JOBNAME_TOKEN`, so every
    dev manager of every session names its LSF jobs `wrd_<hash>_<rand>`
    (`jobNamePrefix` in jobqueue/scheduler/scheduler.go), and `bkill_dev`
    could only pattern-kill all of them.
  - Red: with fake `bjobs` (a fixed list of jobs 101 `wrdiso51970_...`, 102
    `wrd_...`, 103 `wrdiso51780_...`, 104 `wrpiso51972_...`, 105 `wrp_...`,
    filtered by `-J` as LSF does) and a fake `bkill` that only logs, the base
    `wrdev.sh stop` (DEV_PORT=51970) logged `bkill -J wrd_* 0` and `bkill -r
    102`, another dev manager's job; a dev manager from `wrdev.sh start local`
    had no `WR_JOBNAME_TOKEN`, and stopping it still made 6 bkill calls.
  - Fix: `DEV_JOBTOKEN` (default `iso$DEV_PORT`, like `PROD_JOBTOKEN`) and
    `DEV_JOB_PREFIX=wrd<token>_`. Every dev manager start in wrdev.sh
    (`start`, `dump`, idle-backlog-cpu, exec-impossible-retries,
    transient-start-retries, runner-log-bytes, retention-check) sets
    `WR_JOBNAME_TOKEN=$DEV_JOBTOKEN`, so its jobs are `$DEV_JOB_PREFIX*`.
    `bkill_dev` refuses a prefix that is not `wrd<letters/digits>_`, and
    kills, counts and force-removes only `$DEV_JOB_PREFIX*` jobs, the last
    by exact job id after an exact prefix match. `stop` and `clean` skip
    `bkill_dev` when the dev manager they stop runs `-s local` (`dev_local`,
    from its verified command line), and `clean` counts only this root's
    dev jobs. `sweep.sh`'s preflight also refuses while this root's
    `wrd<token>_*` jobs exist (it still refuses on plain `wrd_*`, which
    managers from before this change may have left). Help text and
    developers/README.md updated.
  - After: the same check logged only `bkill -J wrdiso51970_* 0` and `bkill
    -r 101`; the `start local` manager's environment had
    `WR_JOBNAME_TOKEN=iso51970`, and stopping it made 0 bkill calls (the
    manager was stopped). Real `bkill` was never run.
  - Not changed: the churn, limit-drain and similar monitors still count
    every RUN job of this user (`bjobs -o stat`) for their progress lines;
    that only skews a reading and kills nothing.
  - Files: `developers/wrdev.sh`, `developers/soak/sweep.sh`,
    `developers/README.md`.

- [x] prodsim_reap_local only matches runners whose path is exactly $WR, so
  runners started through run.sh's RUNNER_FILELOG wrapper as $WR.real are
  left behind under SCHED=local. Make it match both (by exact path, verified,
  not a broad pattern).
  - Source: incidental, found while verifying F3 (its harness wraps the
    binary the way `run.sh` does).
  - Cause: with `RUNNER_FILELOG=1`, `$WR` is a script that execs
    `$WR.real`, so the manager, and every runner the local scheduler starts,
    runs as `$WR.real`; `prodsim_reap_local` compared argv[0] with `$WR`
    only.
  - Red: the F3 harness (now also failing if any process whose argv[0] is a
    file in its root is left after prodsim's cleanup), run at `f57ae4d1`:
    `killed 2 leftover local runner/job processes`, then `FAIL: processes of
    the root still running after prodsim's cleanup`, two `ROOT/wr.real
    runner ... --deployment production` processes.
  - Fix: argv[0] must equal `$WR` or `$WR.real` exactly, with the existing
    `runner` and `--server ...:$PROD_PORT` checks; `${WR:?}` and
    `${PROD_PORT:?}` guard against an empty value.
  - After: `killed 18 leftover local runner/job processes`, `PASS: nothing
    of the root left running`, and the F3 verdict still `PASS: no restart
    began or ended inside another`.
  - Files: `developers/wrdev.sh`.

## Incident: a verification harness killed another run's processes

- What happened: at about 08:43, while checking F3, I re-ran the tail of my
  scratch harness (its verdict and leftover cleanup) by piping it into a new
  `bash`, outside the script that set its variables. `$root` was empty, so
  its cleanup, `pgrep -u <me> -f -- "$root/wr"` and then `kill -9` of each
  pid whose command line contained `$root/wr`, matched every process of mine
  with `/wr` in its command line: about 10.
- Impact: the battery10 phase 5 driver
  (`/nfs/hgi/wr/sb10-bigdb/battery10/p5/go.sh`) and its `rundepcrash-0`
  repro (manager on ports 51940-51943) were killed. No LSF jobs or ports
  were left; the battery agent re-ran phase 5. Other processes of mine with
  `/wr` in their command line at that moment may also have been killed; I
  could not list them afterwards.
- Guard, from then on: no killing by pattern (`pkill -f`, `pgrep -f | kill`,
  `killall`). Only pids recorded from `$!`, or pids whose whole command line
  is verified and whose argv[0] is a file inside the scratch dir. Every
  harness starts with `set -u` and uses `${var:?}` guards, the scratch
  helper `ours_under <root>` refuses a root outside the scratch dir, and no
  fragment of a script is run outside the script. The same applied to the
  tooling here: `sweep.sh`'s leftover loop walks `ps -u` pids and checks each
  with `our_fg_manager` (which needs `${WRDEV_ROOT:?}`) instead of `pgrep -f`,
  and `prodsim_reap_local` matches exact paths with `${WR:?}` guards.
- Re-run with the guarded harnesses after all four items: F2's harness
  PASSes (printed and actual foreground pid 1270415, none left after
  `stop`); `sweep.sh` on `prod-start prod-stop dump` exits 0 with 38
  goroutines dumped and nothing of its root left; `sweep.sh` driving the
  base `wrdev.sh` with a stale pid file exits 1 with both `SWEEP ERROR`
  lines, leaves nothing running and does not signal the unrelated `sleep`;
  the F3 harness PASSes both verdicts (above).

## Safety review of 545e67d4..471cec3a

- [x] R1 (high, introduced by f57ae4d1). `bkill_dev`'s check `case
  "$DEV_JOB_PREFIX" in (wrd[A-Za-z0-9]*_)` is a glob, so it accepts
  `wrdi*_` or `wrda-b_`: `DEV_JOBTOKEN='i*'` would bkill every session's
  `wrdiso*` jobs. wr's `jobNameToken()` strips non-alphanumerics, so
  `DEV_JOBTOKEN=iso-1` names jobs `wrdiso1_` while `bkill_dev` targets
  `wrdiso-1_*`, a silent leak. Validate the tokens once where they are
  defined, fix the `wrp` guard in prodsim's cleanup the same way, and
  validate before the older `bkill -J "${PROD_JOB_PREFIX}*" 0` calls.
  - Red: with a fake `bjobs` (a fixed job list, filtered by `-J` as LSF
    does) and a fake `bkill` that only logs, `wrdev.sh stop` at `471cec3a`
    with `DEV_JOBTOKEN='i*'` ran `bkill -J wrdi*_* 0`, and with
    `DEV_JOBTOKEN=iso-1` ran `bkill -J wrdiso-1_* 0`. `PROD_JOBTOKEN='i*'`
    was accepted too.
  - Fix: `job_token_ok` accepts only a non-empty token of ASCII letters and
    digits (`case` with `''|*[!A-Za-z0-9]*`; bash's `globasciiranges` is on,
    and `isö` is rejected). wrdev.sh dies at startup unless both
    `PROD_JOBTOKEN` and `DEV_JOBTOKEN` pass. `bkill_dev` and prodsim's
    cleanup require the token to pass and the prefix to equal exactly
    `wrd<token>_` or `wrp<token>_`. The three older prod bkills (the
    backup-stall, add-storm and report-storm cleanups) call
    `prod_bkill_ok` first, which dies on the same test.
  - After: `DEV_JOBTOKEN='i*'` and `iso-1` make `wrdev.sh stop` die with
    `DEV_JOBTOKEN '...' must be letters and digits only` and 0 bkill calls;
    `PROD_JOBTOKEN='i*'` and `iso-1` make any mode die the same way; the
    default token still bkills only `wrdiso51970_*` and job 101.
  - Files: `developers/wrdev.sh`.

- [x] R6 (medium). The token is not unique per WRDEV_ROOT: the default
  `iso$DEV_PORT` collides across same-user sessions on different hosts (LSF
  job names are seen cluster-wide) and with a later root reusing the port.
  PROD_JOBTOKEN has the same flaw. R7: DEVELOPERS.md still said wrdev.sh
  uses `wrd_*` and told readers to `bkill -J 'wrd_*' 0`.
  - Fix: the default `DEV_JOBTOKEN` and `PROD_JOBTOKEN` are
    `iso<port>h<cksum of "hostname:WRDEV_ROOT">`, letters and digits only.
    `wrdev.sh job-token dev|prod` prints them, and the tools that need the
    prod or dev prefix ask it rather than repeating the formula: soak
    `config.sh` (its `JOB_PREFIX`, validated the same way), `sweep.sh`
    (which exports both, and whose preflight checks this root's `wrd<token>_`
    and `wrp<token>_` jobs) and `repro/relburyiso.sh`. A root's modes and its
    cleanup must run on one host; the help, developers/README.md, the soak
    README and DEVELOPERS.md say so. DEVELOPERS.md now describes the tokened
    names and says never to `bkill -J 'wrd_*'`.
  - Verified (LSF stubbed): two roots give `iso51970h780126228` and
    `iso51970h2971687645`; the same root with `hostname` faked to
    `otherhost` gives `iso51970h679774962`; soak `config.sh` for that root
    gives `JOB_PREFIX=wrpiso51972h780126228_`, matching `wrdev.sh job-token
    prod`; dev (`start local`) and prod-mode (`prod-start local`) managers
    ran with `WR_JOBNAME_TOKEN` set to those tokens, and stopped. `sweep.sh`
    with a fake `bjobs` listing a job of its root's dev token refused to
    start (`LSF still has wrdiso51974h491614040_* jobs`), and with only other
    tokens' jobs ran its mode.
  - Files: `developers/wrdev.sh`, `developers/soak/config.sh`,
    `developers/soak/sweep.sh`, `developers/soak/repro/relburyiso.sh`,
    `developers/README.md`, `developers/soak/README.md`, `DEVELOPERS.md`.
- [x] R8 (note, no code). Managers started by an older wrdev.sh named their
  LSF jobs `wrd_*` (or `wrpiso<port>_*` for prod-mode) without the new
  token. `stop`, `clean` and prodsim's cleanup no longer bkill those; kill
  them by exact job id. `sweep.sh`'s preflight still refuses to start while
  any `wrd_*` job of this user exists, which is safe.
- [x] R2 (medium, older). `sweep.sh`'s disk guard ran `pkill -TERM/-KILL
  -f "$W $wmode"`, which matches other sessions running the same wrdev.sh
  and longer mode names (`add-storm` matches `add-storm-lsf`). Stop the
  mode by its own pid or process group.
  - Red: with a fake `df` (200G free on the first call, then 1G) and
    `SWEEP_WRDEV` set to a fake wrdev.sh that starts a child and a
    grandchild and logs its TERM trap, plus a decoy running the same
    `<fake wrdev.sh> status` command line outside the sweep, the guard at
    `471cec3a` stopped the mode and also killed the decoy.
  - Fix: `stop_mode` signals only the pid the sweep recorded with `$!`.
    The launch execs env, setsid and timeout in turn, so that pid is
    timeout, and setsid made it a process group leader (a background job of
    a non-interactive shell leads no group, so setsid does not fork); it is
    checked by its command line (`timeout --signal=TERM ... $W $wmode`) and
    its pgid before anything is sent. A TERM to timeout reaches wrdev.sh,
    whose cleanup trap runs, and the rest of its group; if the mode is still
    there 150s later its group is killed. The loop then stops instead of
    firing the guard again.
  - After: the same check stopped the mode, its child and its grandchild
    (the TERM trap ran) and left the decoy running.
  - Files: `developers/soak/sweep.sh`.
- [x] R3 (medium, older). `soak/repro/readdcrash.sh`, `rundepkill.sh` and
  `rundepcrash.sh` ran `pkill -9 -f "$WRDEV_ROOT/wr[.real] runner"`, which
  is unanchored, so a process whose path merely ends with this root's
  matches. `.docs/reliable/harness/exp_realdb_seed.sh` ran `pkill -9 -f
  "$PORT"`, as do exp1.sh, exp_churn.sh, exp_drive_ab.sh,
  exp_reconnect.sh, exp_startup_ab.sh and exp_status_load2.sh.
  - Red: fake runners (`exec -a` to set argv, the process is `sleep`) for
    `$WRDEV_ROOT/wr runner`, `$WRDEV_ROOT/wr.real runner`, a decoy
    `<scratch>/pre$WRDEV_ROOT/wr runner`, another root's runner and a
    non-runner of this root: the old pattern, listed with `pgrep -f` (never
    used to kill), matched the decoy whose path ends with this root's, and
    missed `$WR.real`.
  - Fix: `wrdev.sh reap-runners [port]` kills this user's processes whose
    argv[0] is exactly `$WR` or `$WR.real` and argv[1] is `runner` (and,
    given a port, whose `--server` is on it), through `our_runner_pids`,
    which `prodsim_reap_local` now uses too. The three repros call it. The
    seven harness scripts' fallback is `kill_our_manager`: kill -9 the pid
    in `$BASE/manager_development/pid` only if its command line is `$WR
    ... manager start ...`.
  - After: `reap-runners` killed exactly the two runners of this root and
    left the three decoys. `kill_our_manager` left an unrelated pid named by
    the pid file alone and killed a fake `$WR --deployment development
    manager start` pid. The F3 harness's leftover check (which runs
    `prodsim_reap_local`) passes again below. The repros and the harness
    scripts were not run end to end: the harness scripts need
    `/tmp/wr-reliable` binaries, a real DB and LSF, and the repros run
    under battery10, which is not to be touched.
  - Files: `developers/wrdev.sh`, `developers/soak/repro/readdcrash.sh`,
    `developers/soak/repro/rundepcrash.sh`,
    `developers/soak/repro/rundepkill.sh`, `.docs/reliable/harness/exp*.sh`.
- [x] R4 (medium). `watcher.sh` checked `soak_alive` only at its loop's
  top, so after its 65s wait and a wait on the restart lock it could start
  a manager after prodsim and its cleanup had ended, leaving it running.
  - Red: a fake prodsim (argv set with `exec -a`, ending at +80s), a failed
    start line, and the lock held from +30s to +95s: at `471cec3a` the
    watcher started a manager at about +95s (`start rc=0 ...
    watcher-after-failure`) after prodsim had gone; the check killed that
    manager by its verified pid.
  - Fix: the watcher checks `soak_alive` before taking the lock, and
    `restart_if_down` checks it again first thing once the lock is held.
  - After: no start line, no manager running, `prodsim gone, exiting`.
  - Files: `developers/soak/watcher.sh`.
