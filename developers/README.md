# developers/

Developer tooling for reliability work on wr. **Not part of the shipped binary
or the test suite.** Apart from shell scripts it holds one Go command,
`prodsim/`. `make lint` lints it and `go build ./...` compiles it, but it has no
tests, so `make test` runs nothing from it.

Start with [`../DEVELOPERS.md`](../DEVELOPERS.md), then use `wrdev.sh`:

```bash
developers/wrdev.sh help
```

`wrdev.sh` runs an **isolated** wr manager (its own config, ports, managerdir,
and `wrd_*` job names) so it can never disturb a real `--deployment production`
manager. It refuses to kill any process that is not its own isolated binary.
Everything it creates lives under `$WRDEV_ROOT` (default `$HOME/wr-devtest`).

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
`WRDEV_PRODSIM_RESTART_MIN=<n>` restarts the manager every n minutes.
`developers/wrdev.sh help` lists the rest.

Each run writes `$WRDEV_ROOT/prodsim-<epoch>/`:

- `calls.tsv`: every client call's latency and error
- `samples.tsv`: manager RSS, goroutines, heap, fds, DB size, ping and host load
- `events.tsv`, `restarts.tsv`, `profiles/` and a copy of the manager log
- `report.txt`: the output of `prodsim -report`

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
