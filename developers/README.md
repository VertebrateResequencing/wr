# developers/

Developer tooling for reliability work on wr. **Not part of the shipped
binary.** Apart from shell scripts it holds the Go commands
`prodsim/` and `soak/*/`. `make lint` lints them and `go build ./...` compiles
them. Only `prodsim/` has tests, which `make test` runs.

Start with [`../DEVELOPERS.md`](../DEVELOPERS.md), then use `wrdev.sh`:

```bash
developers/wrdev.sh help
```

`wrdev.sh` runs an **isolated** wr manager (its own config, ports, managerdir,
and `wrd<token>_*` job names, `DEV_JOBTOKEN` defaulting to `iso$DEV_PORT`) so
it can never disturb a real `--deployment production` manager, nor another dev
manager's LSF jobs. It refuses to kill any process that is not its own isolated
binary. Everything it creates lives under `$WRDEV_ROOT` (default
`$HOME/wr-devtest`).

## The performance gate

`speed.sh` is what `make speed` and `make speed-full` run. It benchmarks the
hot paths and runs the local speed scenarios for this tree and a baseline, then
compares them with benchstat. See
[`../DEVELOPERS.md`](../DEVELOPERS.md) §5 for when it is required, and the
script's header for its knobs.

## The production-shaped soak

Every other `wrdev.sh` mode isolates one production ingredient for minutes.
`prodsim` runs all of them at once for hours against an isolated prod-mode
manager with pprof on:

- ibackup's server re-adding the same put jobs every minute, and its fofn
  watcher polling by rep-group prefix
- wrstat walks that add their own stat jobs under a fresh `datetime<` limit
  group
- wrstat-ui's hourly empty add
- portal bursts of tens of thousands of jobs with KB-long commands
- people with the status page open, cron'd `wr status`, a Go client waiting
  through a subscription, and an operator changing limits and retrying
- optional manager restarts

The jobs run `prodsim/psimjob.sh`, which only sleeps, holds some memory,
prints and sometimes fails.

```bash
developers/wrdev.sh build
developers/wrdev.sh prodsim 4 6 1   # hours, real seconds per simulated minute, scale
```

It defaults to real LSF. `WRDEV_PRODSIM_SCHED=local` runs it on the local
scheduler. `WRDEV_PRODSIM_DB=<file>` starts from a copy of a big DB, and
`WRDEV_PRODSIM_RESTART_MIN=<n>` restarts the manager every n minutes, and
`WRDEV_PRODSIM_RESTART_KINDS=clean,crash` alternates clean stops with
`kill -9`. `developers/wrdev.sh help` lists the rest.

Each run writes `$WRDEV_ROOT/prodsim-<epoch>/`:

- `calls.tsv`: every client call's latency and error
- `samples.tsv`: manager RSS, goroutines, heap, fds, DB size, ping and host load
- `events.tsv`, `restarts.tsv`, `profiles/` and a copy of the manager log
- `markers/<host>.tsv`: a start and an end line, with the real exit code, for
  every run of every job, so a job that ran twice can be counted
- `report.txt`: the output of `prodsim -report`: per client call, its count,
  errors, latency percentiles, and how many calls took at least the Go
  clients' 2 minute Timeout and the time spent in them

prodsim's Go client calls (the `client` package's Scheduler) wait through a
manager restart, as `wr runner` does, so an outage shows in `report.txt` as
their slow calls rather than as errors. They stop waiting when the run ends,
at most a Timeout later. The `wr` commands it runs do not wait like that, so
their calls still fail during an outage.

The soak is for finding problems, not a gate. It exits non-zero only if it
could not start or measured nothing; a person reads `report.txt`.

The soak cannot touch a real deployment:

- It writes a config dir of its own for the run, so no other `wrdev.sh` call
  can move its ports mid-run.
- Before every manager start and stop, it asks `wr conf` whether
  `--deployment production` resolves to localhost, the isolated ports, and a
  managerdir that holds every file the manager writes, so an env var or
  `~/.wr_config*.yml` cannot redirect it.
- A restart only runs `wr manager stop` while the pid file names a process
  running the isolated binary, and cleanup only signals its own children.
- `prodsim` itself refuses to run unless `WR_CONFIG_DIR` is set and the
  config names its `-rundir`.
- Its LSF jobs are named `wrp<PROD_JOBTOKEN>_*`, and cleanup bkills only
  those. Cleanup runs on any exit, including Ctrl-C.

Do not edit `wrdev.sh` while a mode is running from it.

What the soak found is recorded in
`../.docs/bugfixes/260927-prodsim-findings.md`. Two of its findings have gates:

- `wrdev.sh retention-check` fails while archived jobs stay in the heap. It
  fails on develop until #633 merges.
- `wrdev.sh remap-stall-check` fails while a write that grows the DB past
  bbolt's mapping stalls reads behind a backup copy. It fails on develop
  until #632 merges.

Two later prodsim findings have gates too:

- `wrdev.sh freelist-check` fails while a one-key commit costs more as the
  freelist grows (fixed by #642).
- `wrdev.sh selfconnect-check` runs the tests for #641's port reservation,
  including the slow real-TIME_WAIT one.

## Long soaks with injected crashes

[`soak/`](soak/README.md) drives `prodsim` for hours at production concurrency
and adds crash, stall and #649/#654 injectors, monitors, analysis of double
runs and lost updates, DB helpers and short isolated repros. Its README says
how to configure, run and analyse a soak, and the rules that keep one away
from production.
