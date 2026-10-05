# developers/soak/

Tooling for long soaks of an isolated wr manager under production-shaped load,
with manager crashes, clean restarts and commit stalls injected while jobs are
in flight. It wraps `developers/wrdev.sh prodsim` (see
[`../README.md`](../README.md)) with injectors, monitors and analysis scripts
that look for jobs run twice, and releases or buries lost, across manager
crashes. Two injectors reproduce issues #649 and #654 at soak scale.

Nothing here is part of wr or its tests. `make lint` and `go vet` check the Go
helpers, and `make test` runs nothing from this directory.

## Tools

### Launcher and orchestration

| file | what it does |
| --- | --- |
| `config.sh` | Sourced by every soak script. Reads the settings below from the environment, with defaults, and holds the shared safety checks. |
| `run.sh` | Builds the isolated binary, mounts `fusestall` when `USE_FUSE=1`, runs `wrdev.sh prodsim` and starts the helpers on its output directory. At the end it runs `markers.py`. |
| `sweep.sh <sweepdir> [mode ...]` | Runs every `wrdev.sh` mode in turn, or the modes named, recording rc, wall time, load and free space per mode in `results.tsv`. |
| `hook.sh` | `run.sh`'s `WRDEV_PRODSIM_PRESTART_HOOK` when `USE_FUSE=1`. Before restart `FUSE_ON_AT` it points the manager's DB symlink through the `fusestall` mount, and before restart `FUSE_OFF_AT` back at the file. |

### Fault injectors

`crashafter.sh`, `crashon.sh` and `relbury.sh` each wait until `restarts.tsv`
has a given number of manager starts, then crash and restart the manager,
recording both in `restarts.tsv` with a `manual=` tag. Each kills only the pid
that our manager's pid file names, and only after `wr conf` confirms that
`--deployment production` resolves to our manager and `ps` confirms that the
pid runs our binary. Each holds `<outdir>/restart.lock` (`flock`) from the kill
until the restarted manager is up, as `wrdev.sh prodsim`'s scheduled restarts
and `watcher.sh`'s restart do, so no two restarts overlap: `crashafter.sh` and
`crashon.sh` wait for the lock, and `relbury.sh`, whose kill must land on time,
skips its crash if another restart holds it. `rundep.sh` adds jobs and kills
nothing. `stall.sh` waits for `hook.sh` to put the DB behind `fusestall`, the
daemon that `run.sh` starts when `USE_FUSE=1`.

| file | what it does |
| --- | --- |
| `crashafter.sh <outdir> <nStarts> <delaySecs>` | One extra `kill -9` and restart, `delaySecs` after start `nStarts`. |
| `crashon.sh <outdir> <nStarts> burst\|stall <delaySecs>` | One extra `kill -9` and restart, `delaySecs` after the next portal burst or induced stall. |
| `stall.sh <outdir>` | `STALL_AFTER_MIN` minutes after the DB goes behind `fusestall`, holds every fsync for `STALL_SECS`, taking goroutine dumps and a CPU profile meanwhile. Needs `USE_FUSE=1`; `run.sh` starts it. |
| `rundep.sh <outdir> [intervalSecs] [lastStartSecs]` | The #649 case at soak scale: a running job's dependency group gains a member, so the job must run exactly twice. |
| `relbury.sh <outdir> <nStarts> <tag> <nJobs> <killDelayMs> [leadSecs]` | The #654 case at soak scale: a batch of releases and buries arrives at one deadline, and the manager is killed `killDelayMs` after it. |
| `fusestall/` | A FUSE loopback mount whose fsyncs block while a control file exists, so commits stall on demand. With `USE_FUSE=1`, `run.sh` builds it into `$SOAK_ROOT` and runs it. |

### Monitors

`run.sh` starts the first four, and `lsfprobe.sh` too under `SCHED=lsf`. Run
`mon.sh` in a terminal and `stopstate.sh` from a launcher when you want them.
`stopwatch.sh` and `stopstate.sh` exit when prodsim does, so they do not
record the final stop that `wrdev.sh prodsim` makes as it ends.

| file | output in `<outdir>` |
| --- | --- |
| `diskguard.sh <outdir> <wrdevpid>` | `disk.tsv`; stops the soak when space runs low |
| `ramp.sh <outdir> "<min>:<target> ..."` | `ramp.log`; sets the portal concurrency and `results_portal` limit per stage |
| `stopwatch.sh <outdir>` | `stopwatch.log`, `profiles/stop.*`: goroutine dumps and stop phases during each clean stop |
| `watcher.sh <outdir>` | `watcher.log`; restarts the manager if a scheduled start failed |
| `lsfprobe.sh <outdir>` | `lsfprobe.tsv`: bjobs and bqueues latency, pending reasons (LSF only) |
| `mon.sh <outdir>` | stdout: a status line every 10 minutes and new restart, ramp, stall and spike lines |
| `stopstate.sh <outdir>` | `stopstate.log`: the manager pid's `/proc` state through each stop |

### Analysis

| file | what it reports |
| --- | --- |
| `markers.py <outdir>` | Runs per kind, runs with no end marker, double runs (a key run again after an exit 0), overlaps, re-runs by nearest restart or stall, and `--cmd_deps` ordering. |
| `doubles.py <outdir> [doubles.tsv]` | Each double run classified by the outage its first run straddled. |
| `anyway.py <outdir> <runnerlogdir> <doubles.tsv> <managerlog>...` | Which double runs had their first reservation handed out before it was on disk, judged from the first run's own runner log (its reports rejected as "bad job" or "you must Reserve()" after a crash, and no "command ran OK" for it) or the manager's rate-limited warning, with a reserve reply 10s or more after the request as supporting evidence; how often each signal fires on all other reservations; and the first run's own outcome lines. |
| `runnerlogs.py <runnerlogdir> [unacked.tsv]` | Unacknowledged start reports and how they settled, final-state failures, kills with no kill line. |
| `starttimes.py <outdir> <dbstart.tsv> <unacked.tsv>` | Recorded StartTime against the real start, for all, pre-crash and unacknowledged runs. |
| `latency.py <outdir> <runnerlogdir>` | End-to-acknowledgement latency per outcome and per 10 minutes, and ends per minute. |
| `rundepcheck.py <outdir>` | A verdict per `rundep.sh` instance. |
| `relburycheck.py <outdir> <runnerlogdir> [dbstart.tsv]` | A verdict per `relbury.sh` batch: acknowledged-then-lost, stuck and extra runs. |
| `stopcheck.sh <outdir> <stopBegin> <stopEnd> [<prevStart>]` | Joins the portal runs in flight at a clean stop to the manager's state for them: an exit 0 must be complete, a killed run buried. Needs the manager up. |

### DB helpers

Build these with `go build -o <dir> ./developers/soak/<name>`. `run.sh` builds
`psinspect` into `$SOAK_ROOT`, and `fusestall` too when `USE_FUSE=1`.

| directory | what it does |
| --- | --- |
| `psinspect/` | Prints state, exit code, attempts and fail reason of the jobs in the given rep groups (`inc:` for incomplete only, `key:` for one job) of the manager `WR_CONFIG_DIR` names. |
| `dbstart/` | Reads a stopped manager's DB read-only and prints each soak job's state, attempts, host, pid and start and end times. |
| `bboltexp/` | Checks (`tx.Check()`) or churns a copy of a manager DB, opened as the manager opens it. |

### Isolated repros

`repro/` holds short single-case repros, each on its own isolated manager
under `$REPRO_ROOT/<name>`, which it wipes first. Set `REPRO_ROOT` to a
scratch directory and `REPRO_WR` to the wr binary to test. `REPRO_PORTS`
(default `51876 51877 51878 51879`) and `WRDEV` (default this checkout's
`wrdev.sh`) are optional.

| file | the question it answers |
| --- | --- |
| `readdcrash.sh <0\|1>` | Does a `--rerun` re-add of a running job, then a `kill -9`, make it run twice? |
| `rundepcrash.sh <0\|1> [bSecs] [crashDelay]` | Does a running job whose dependency group gains a member (#649) run exactly twice, with or without a crash? |
| `rundepkill.sh <0\|1\|2\|3>` | What state does such a job reach when it is killed, its new dependency is killed, or the manager restarts? |
| `relburyiso.sh <killDelayMs>...` | `relbury.sh`'s #654 batches on a quiet LSF manager, where acknowledgements are quick. Needs LSF. |

The first three use the local scheduler.

## Settings

`config.sh` reads these. `SOAK_ROOT` is the only one without a default.

| variable | default | meaning |
| --- | --- | --- |
| `SOAK_ROOT` | none, required | The soak's `WRDEV_ROOT`: binary, manager dir, DB and every output. An absolute path of letters, digits and `._/-`. |
| `DEV_PORT DEV_WEB PROD_PORT PROD_WEB` | `51860`-`51863` | The isolated managers' ports. |
| `PPROF_PORT` | `6112` | The soak manager's `WR_PPROF_ADDR` port. |
| `PROD_JOBTOKEN` | `wrdev.sh job-token prod`: `iso$PROD_PORT` plus a checksum of the host and `SOAK_ROOT` | LSF jobs are named `wrp<token>_*`; the scripts only count or kill those. Letters and digits only. Run every script for a soak on the same host. |
| `SCHED` | `lsf` | `lsf` or `local`. |
| `QUEUE` | `normal` | LSF queue for the soak's jobs. |
| `WRSRC` | this checkout | The checkout wr is built from. |
| `WRDEV` | this checkout's `developers/wrdev.sh` | The `wrdev.sh` to drive. |
| `SOAK_DBDIR` | `$SOAK_ROOT/dbdir` | Directory of the working DB, which `$SOAK_ROOT/.wr-prod_production/db` links to. |
| `GUARD_GB` | `40` | `diskguard.sh` stops the soak when the filesystem of `SOAK_ROOT` or `SOAK_DBDIR` has less free. |
| `TMP_GUARD_GB` | `20` | The same for `${TMPDIR:-/tmp}`. |
| `USE_FUSE` | `0` | `1` puts the DB behind `fusestall` for part of the run. |
| `FUSE_MNT` | `${TMPDIR:-/tmp}/wr-soak-fuse-$USER-$PROD_PORT` | `fusestall`'s mountpoint, on local disk. |

`run.sh` also reads `HOURS SIMMIN SCALE` (`3 6 1`), `RESTART_MIN` (`30`),
`RESTART_KINDS` (`clean,crash,clean,crash,clean`), `RAMP0` (`600`), `RAMP`,
`FIXTURE`, `CTR_IMAGE`, `FUSE_ON_AT FUSE_OFF_AT` (`3 4`),
`STALL_AFTER_MIN STALL_SECS` (`15 180`), `RUNNER_FILELOG` and `EXTRA_ARGS`.
Its header says what each does. Any `WRDEV_PRODSIM_*` variable that `run.sh`
does not set, such as `WRDEV_PRODSIM_KEEP_DB=1`, passes through to
`wrdev.sh prodsim`.

## Running a soak

1. Choose `SOAK_ROOT` on a filesystem with room for the fixture DB, its backup
   and a few GB of output. With `SCHED=lsf` it must be visible from every exec
   node, because jobs write their run markers there. Keep the Go build cache
   off quota-limited home directories too, for example
   `export GOCACHE=/local/scratch/$USER/gocache`.
2. Optionally make a fixture. A soak at production scale wants a big DB with a
   production-sized freelist, such as one made with
   `wrdev.sh add-storm-fixture` (see `wrdev.sh help`). Without `FIXTURE` the
   soak starts from an empty DB.
3. Write a launcher that sets the settings and starts any injectors, then runs
   `run.sh`. The example below crashes the manager on a mixed schedule, holds
   commits for 3 minutes behind FUSE, ramps the portal from 600 to 3000
   concurrent jobs, and injects the #649 and #654 cases:

   ```bash
   #!/usr/bin/env bash
   export SOAK_ROOT=/big/scratch/$USER/soak/run
   export PROD_PORT=51892 PROD_WEB=51893 DEV_PORT=51890 DEV_WEB=51891 PPROF_PORT=6118
   export GUARD_GB=40 HOURS=2 SIMMIN=6 SCALE=1
   export RESTART_MIN=15 RESTART_KINDS=crash,clean,crash,clean,crash,crash,clean
   export USE_FUSE=1 FUSE_ON_AT=3 FUSE_OFF_AT=4 STALL_AFTER_MIN=7 STALL_SECS=180
   export RAMP0=600 RAMP="0:600 8:1200 16:2100 25:3000" RUNNER_FILELOG=1
   export FIXTURE=/big/scratch/$USER/fix120k.db WRDEV_PRODSIM_KEEP_DB=1
   H=/path/to/wr/developers/soak
   rm -f "$SOAK_ROOT/current"
   ( # injectors, once run.sh has written the output dir's path to $SOAK_ROOT/current
     for _ in $(seq 1 900); do [ -s "$SOAK_ROOT/current" ] && break; sleep 2; done
     out=$(cat "$SOAK_ROOT/current")
     "$H/crashon.sh" "$out" 4 stall 90 > "$out/crashon-stall.out" 2>&1 &
     "$H/crashon.sh" "$out" 6 burst 60 > "$out/crashon-burst1.out" 2>&1 &
     "$H/crashon.sh" "$out" 8 burst 45 > "$out/crashon-burst2.out" 2>&1 &
     "$H/relbury.sh" "$out" 2 r1 400 500 > "$out/relbury-r1.out" 2>&1 &
     "$H/relbury.sh" "$out" 7 r2 400 2000 > "$out/relbury-r2.out" 2>&1 &
     "$H/stopstate.sh" "$out" &
     "$H/rundep.sh" "$out" 240 6300 > "$out/rundep.out" 2>&1 &
     wait ) &
   exec nice "$H/run.sh"
   ```

4. Watch it from another terminal with the same settings exported, at least
   `SOAK_ROOT` and any port you changed:
   `developers/soak/mon.sh $(cat $SOAK_ROOT/current)`. Ctrl-C on the launcher
   reaches `run.sh`, which sends `wrdev.sh prodsim` a TERM; its cleanup stops
   prodsim and our manager (with a final clean stop) and bkills only our jobs.
   That can take many minutes. Press Ctrl-C once: `run.sh` ignores a second
   one, and a second TERM sent to `wrdev.sh` itself would abort the cleanup and
   leave our manager and jobs behind.
   `run.sh` waits for that, then unmounts `fusestall` and runs `markers.py`.
   The injectors and monitors exit once prodsim has gone.

`run.sh` writes `$SOAK_ROOT/prodsim-<epoch>/`. On top of what
[`../README.md`](../README.md) lists for `prodsim`, it holds:

- `restarts.tsv`: every stop and start, with kind, duration, DB size, freelist
  state, load and the `manual=` tag of an injected crash
- `markers/<host>.tsv`: one `S` and one `E` line per run of every job, with
  its real exit code, written by `psimjob.sh`
- `lsf.tsv` and `orphans.tsv` (LSF only): our job states every minute, and
  which runners outlived each stop
- `profiles/`: post-start, pre-stop, spike, stall and stop captures
- the monitors' logs above, `rundep/` and `relbury/` from those injectors,
  `hook.log`, `manager-start.out`, `manager-stop.*.out`, `fusestall.log`
- `markers-analysis.txt`: `markers.py`'s report

With `RUNNER_FILELOG=1` runner logs are in `$SOAK_ROOT/runnerlogs/`.

## Analysis afterwards

With `out=$SOAK_ROOT/prodsim-<epoch>` and `rl=$SOAK_ROOT/runnerlogs`:

```bash
python3 developers/soak/markers.py "$out"            # double runs first
python3 developers/soak/doubles.py "$out" "$out/doubles.tsv"
python3 developers/soak/anyway.py "$out" "$rl" "$out/doubles.tsv" "$out"/manager.log*
python3 developers/soak/runnerlogs.py "$rl" "$out/unacked.tsv"
python3 developers/soak/latency.py "$out" "$rl"
python3 developers/soak/rundepcheck.py "$out"
# needs the DB kept (WRDEV_PRODSIM_KEEP_DB=1) and the manager stopped
go build -o "$SOAK_ROOT/dbstart" ./developers/soak/dbstart
"$SOAK_ROOT/dbstart" "$SOAK_DBDIR/db" > "$out/dbstart.tsv"
python3 developers/soak/starttimes.py "$out" "$out/dbstart.tsv" "$out/unacked.tsv"
python3 developers/soak/relburycheck.py "$out" "$rl" "$out/dbstart.tsv"
```

The soak finds problems; it is not a gate. A double run, a stuck job or an
acknowledged release that was lost is a defect to reproduce in `repro/` or a
`jobqueue` test.

## Without LSF or FUSE

- `SCHED=local` runs every job on the manager's host. `run.sh` then skips
  `lsfprobe.sh`, and `wrdev.sh` skips `lsf.tsv` and `orphans.tsv`. With
  `SCHED=lsf`, `run.sh` refuses to start unless `bjobs` is on `PATH`.
- `USE_FUSE=0`, the default, skips `fusestall`, `hook.sh` and `stall.sh`, and
  `crashon.sh`'s `stall` trigger refuses to run. `USE_FUSE=1` needs
  `/dev/fuse` and `fusermount`.
- `CTR_IMAGE` unset skips prodsim's container jobs. When set, it must be a
  singularity image readable from every exec node, with `singularity` on
  `PATH`.
- `lsfprobe.sh`, `relburyiso.sh` and `sweep.sh`'s LSF modes need LSF.

## Safety rules

- **Never replace a wr binary that runners are using.** A runner executing a
  binary that is rewritten in place, on NFS especially, dies, and its job then
  looks like it ran twice. `run.sh` refuses to rebuild while any process on
  this host runs `$SOAK_WR` or LSF still has our jobs. Build a new version to a
  new path or a new `SOAK_ROOT`, and never swap it in mid-run.
- **Keep soak data and caches off quota-limited home directories.** A
  production-sized DB, its backup, runner logs and the Go build cache can fill
  a home quota and break every other job of yours. `diskguard.sh` stops the
  soak at `GUARD_GB`, but only on the filesystems it watches.
- **Isolate from production.** Give each soak its own ports, `SOAK_ROOT` and
  job token, and never point any of them at a production manager. `wrdev.sh`
  writes the run's own config dir, and every manager start, stop or kill here
  first checks that `wr conf` resolves `--deployment production` to our port
  and that the pid runs our binary. The `repro/` scripts start through
  `wrdev.sh prod-start` and check `wr conf` before a clean stop, but their
  kills check only that the pid runs their own binary. Two soaks at once need
  different ports.
- Do not edit `wrdev.sh` or these scripts while a soak is running from them.
