# Soak tooling from two local clones (2026-10-02)

This records what happened to every commit in two local-only clones whose
developer tooling was landed as `developers/soak/`, the `developers/prodsim`
changes, the `wrdev.sh` `freelist-check` and `selfconnect-check` gates and
`jobqueue/prodsim_freelist_test.go`.

How each commit was checked: its added lines (12 characters or longer) were
looked up in the same file, or for `developers/prodsim` anywhere in that
directory, on `origin/develop` at `e70906e6`. Commits with lines missing were
then read line by line against develop. "Shipped" means develop has the change,
usually under a squash merge with a different subject. "Superseded" means
develop solves the same problem differently, so the old content is dropped.

## B: `wr-prodsim2`, branch `faux-develop` (55 commits on `4bad85b1`)

`faux-develop` stacked then-unmerged PR branches into a stand-in develop.
Seven of its commits are merges of those branches, which carry no content of
their own beyond the merged commits below.

### Product fixes and their tests: all shipped

Every added code line of these is on develop, apart from lines that develop
rewrote while keeping the behaviour (noted).

| commit | subject | shipped in |
| --- | --- | --- |
| 67d5740e | Record loose ends from the container and env fixes | #631 |
| a95410f7 | Make command dependencies wait for container and mount jobs | #631 |
| 790ea3aa | Drop empty elements from queue, limit group and module lists | #631; lsf.go now uses `trimmedCommaList` |
| 00a0db3d | Warn when a runner skips a stored env entry that defines nothing | #631 |
| c4c15bd9 | Report a container runtime's start failure as such | #631 |
| 739839c5 | Pin the container tests' alpine to the docker tests' digest | #631 |
| 0db78b08 | Note which session follow-ups are fixed and which are left | #631 |
| b22128e3 | Record flakes seen while gating the session follow-ups | #631 |
| a3096e4c | Prove the command variants index keeps nothing once its jobs leave | #631 |
| 4db2efe0 | Keep a killed job's reservation alive until its runner reports | #634; `touchJob` gained an argument |
| 7b1f27c5 | Let a released retry be ready as well as delayed in run-state test | #634 |
| d29f14ff | Wait for a stopping manager's readers to exit, not for a slow ping | #634 |
| c10ef5bf | Put a status websocket on the delta feeds before opening it | #634 |
| b2e57cb3 | Give each S3 backup test run its own backup key | #634 |
| 33a48ee2 | Make kill tests wait for the command's start, not a reservation's pid | #634 |
| 6f5afbb2 | Keep a killed job's live fields while its touches renew its TTR | #634 |
| 0d4f2157 | Log a failed touch of a killed job | #634 |
| d12d198b | Record gate results for the race flake fixes | #634 |
| 1f998309 | Drop empty elements from a REST POST's limit_grps parameter | #630 |
| 8930f61b | Map the manager DB with headroom so a backup can't stall a remap | #632; test helpers reworded |
| b9d370ef | Note bbolt's smaller mips64 mapping limit on managerMmapMaxSize | #632 |
| eea10b50 | Lower only the soft address limit in the mmap fallback test | #632 |
| f45d6a23 | Free archived jobs and emptied groups from the manager's heap | #633; test split into helpers |
| dc8ce25f | Reorder a ready job's priority change within its own reserve group | #633 |
| 153c10ba | Record CLI selection, upload and database path findings | #630 |
| 03ccefda | Unmount earlier mounts when a later wr mount fails | #630 |
| 2ce97746 | Let a --change_home job's own wr find the manager token | superseded by 096c3f08 in #630 |
| bc0a7673 | Expand only a leading ~/ in TildaToHome | #630 |
| 5f39b020 | Write a container job's cmd file in its own TMPDIR | #630 |
| 8a0b1437 | Say in wr add help that cwd alone does not make a command unique | #630 |
| 096c3f08 | Point only a --change_home job's wr at the runner's manager dir | #630 |
| c81167ba | Unmount wr mount's earlier mounts when a signal arrives mid-mount | #630 |
| 34c8e3db | Fix a typo in a moved comment | #630 |
| 3127a1f3 | Close a container cmd file whose write failed, and quote a test path | #630 |
| aac1e1b1 | Count the status page's complete jobs without decoding their history | #640 |
| 867e332a | Stop logging empty adds and closed subscription waits as errors | #640 |
| 225eef83 | Reload a Go client's token file after a bad-token rejection | #640 |
| a1f968d0 | Record gate flakes seen while verifying the token reload fix | #640 |
| ed00f168 | Give a token-reload resend only what is left of a bounded request's deadline | #640 |
| fc34498b | Make a Go client's token file path absolute at connect | #640; develop errors on an unresolvable path instead of falling back |

### Tooling

| commit | subject | disposition |
| --- | --- | --- |
| c810337a | Add a production-shaped soak and a gate for archived-job retention | shipped in #638 (prodsim split into files, `retention-check`); `260927-archived-job-retention.md` folded into `260927-prodsim-findings.md`; `queue/prodsim_retention_test.go` superseded by `queue/retention_test.go` (#633) |
| b33fd4dc | Record the soak's backup/mmap stall and three more findings | shipped in #638 (`remap-stall-check`, `prodsim_remap_stall_test.go`, findings doc rewritten with the fixing PRs) |
| 8be743e5 | Profile prodsim around latency spikes and alternate crash restarts | landed here from A: spike captures (`prodsim/soak.go`), clean/crash restart kinds and pre-stop profiles (`wrdev.sh`) |
| 00f48227 | Add prodsim round-2 probes: freelist commit cost and self-connect rebind | freelist probe landed here from A (`jobqueue/prodsim_freelist_test.go`, manager variant); self-connect probe superseded by `jobqueue/port_selfconnect_repro_test.go` (#641); its `ws_details_first` latency is on develop (`prodsim/web.go`) |
| e24301a4 | Add wrdev.sh freelist-check and selfconnect-check gates | landed here from A, pointed at develop's fixes and tests |
| f258d645 | Reproduce bkill's range output being counted as unaccounted | superseded by `jobqueue/scheduler/lsf_bkill_range.go` and its test (#641) |
| 8f210080 | Probe recovery decoding a database whose NFS cache the open dropped | superseded by `prefetchFile` and `jobqueue/db_coldstart_test.go` (#642) |
| 8ab446a0 | Measure what NoFreelistSync would cost a manager start on NFS | landed here from A (`TestProdsimNoFreelistSyncReopen`) |

Nothing in B is missing from develop.

## A: `wr-soak4`, branch `soak4` (23 commits on `7a74da59`)

A is tooling only. Its base is on develop. Its commits were landed as one
change, with `developers/soak4/` renamed to `developers/soak/`, and the
`developers/prodsim`, `developers/wrdev.sh` and
`jobqueue/prodsim_freelist_test.go` changes merged onto develop's current
versions. Beyond the moves, the landed version differs from A in these ways:

- `env.sh` is `config.sh`. It requires `SOAK_ROOT` (A's `R4`, which defaulted
  to a path on the shared scratch filesystem) and gives every other setting a
  default: ports `51860`-`51863`, `PPROF_PORT` `6112`, the job prefix from
  `PROD_JOBTOKEN` (`iso$PROD_PORT`), `SCHED` `lsf`, `QUEUE` `normal`, `WRSRC`
  and `WRDEV` from this checkout, `SOAK_DBDIR` `$SOAK_ROOT/dbdir`, and the
  FUSE mountpoint under `${TMPDIR:-/tmp}`. The isolation, pid and manager-start
  checks the scripts repeated are shell functions there. The isolation check
  matches `wr conf`'s ManagerHost, ManagerPort and ManagerDir exactly, where A
  grepped the ManagerPort line for the port as a substring.
- `run4.sh` is `run.sh`. FUSE is opt-in (`USE_FUSE=1`); without it `run.sh`
  skips `fusestall`, `hook.sh` and `stall.sh`. `FIXTURE` and `CTR_IMAGE` are
  optional, where A copied a fixed fixture and container image. `run.sh` skips
  `lsfprobe.sh` under `SCHED=local`, and refuses to rebuild the binary while a
  process or one of our LSF jobs may be running it. It restores the real binary
  over a previous `RUNNER_FILELOG=1` wrapper before building, since `go build`
  will not overwrite a script. It passes Ctrl-C on to `wrdev.sh prodsim` as a
  TERM: as a background job of a non-interactive shell, prodsim ignores SIGINT,
  so Ctrl-C on A's `run4.sh` would have left it and its manager running. It
  forwards only the first signal, since a second TERM would abort wrdev.sh's
  cleanup part-way, and reports wrdev.sh's real exit status. Its, sweep.sh's and
  relburyiso.sh's LSF in-use checks fail closed when bjobs exits non-zero or
  times out; bjobs exits 0, with "Job <...> is not found" on stderr, when no job
  matches.
- The helpers start the manager with `SCHED` and `QUEUE`, where A hard-coded
  `-s lsf` and `--queue normal`. `diskguard.sh` watches the filesystems of
  `SOAK_ROOT` and `SOAK_DBDIR` and `${TMPDIR:-/tmp}`, not a fixed mount.
- `sweep.sh` takes its fixture DBs from `SWEEP_DB_DIR`, `FIX120K`,
  `SWEEP_ASL_FIXTURE` (default the fixture the sweep's own
  `add-storm-fixture` mode writes, since develop refuses A's version 1
  fixture) and `SWEEP_BOLTBUCKETS`, and records a mode whose fixture is
  missing as `SKIPPED-NOFIXTURE`. It refuses to start while a process runs
  its root's binary or LSF has `wrp<token>_*` or `wrd_*` jobs, since its
  `build` mode rewrites that binary, and its disk guard stops the command it
  actually ran (`add-storm-lsf` for `add-storm-lsf-fix120k`).
- `readdcrash.sh`, `rundepcrash.sh`, `rundepkill.sh` and `relburyiso.sh` are in
  `developers/soak/repro/`, with ports from `REPRO_PORTS` and `wrdev.sh` from
  `WRDEV`. Each refuses to wipe its root while a process runs its binary, and
  `relburyiso.sh` also while LSF has its manager's jobs.
- `launch6.sh` and `launch7.sh` are dropped. Their settings are the example
  launcher in `developers/soak/README.md`.
- The Go helpers `bboltexp`, `dbstart`, `fusestall` and `psinspect` were
  restructured to pass `make lint`. `dbstart` and `psinspect` print what A's
  versions printed, and `fusestall` adds a line naming its required flags.
  `bboltexp` prints the same results on stdout, but its errors (a failed
  transaction or close) go to stderr with a `bboltexp:` prefix, a non-numeric
  `<txs>` is an error rather than 0, and its scratch bucket is `soakchurn`, not
  `soak5churn`. `fusestall` holds its state in a struct, not globals, and uses
  `NodeWrapChilder` in place of the deprecated `LoopbackRoot.NewNode`. prodsim's
  container batch building moved into `ctrBatch`.
- `jobqueue/prodsim_freelist_test.go` is lint-clean under its build tag: its
  bucket name is a constant, and two unused `//nolint:errcheck` are gone.
- `wrdev.sh`: the help text lists `freelist-check` and `selfconnect-check`
  and the new `WRDEV_PRODSIM_*` variables, `freelist-check` accepts only the
  free-page counts its probe measures, `lsf.tsv` and `orphans.tsv` are only
  written under LSF, and `profiles/` exists before the first post-start
  capture, whose CPU profile and first goroutine dump A's first start lost.

| commit | subject | disposition |
| --- | --- | --- |
| e10c7dd9 | restore round-3 wrdev.sh changes and round-2 freelist probe | landed |
| 44373908 | prodsim container/cmd_deps actor, dynamic portal ramp, spike captures, true-exit run markers; fusestall; soak helpers; manager freelist probe | landed |
| 55650159 | phase-2 launcher, FUSE-switch hook, stall inducer, psinspect, markers analysis | landed; `run4.sh` is `run.sh` |
| cb08581e | skip an empty ctrdep add; reset per-stop fields | landed; its stray NFS placeholder file was removed by 799825f7 |
| 799825f7 | drop an NFS placeholder file committed by mistake | nothing to land |
| 475fab0a | run sweep modes from the sweep's work dir, not the clone | landed |
| c81be002 | sweep add-storm-lsf on a fixture built for the sweep root | landed |
| cf41120b | status monitor | landed |
| 0f91c7ec | psinspect by job key | landed |
| 13136c53 | record the manager's process state across a stop | landed |
| 8ddb6d86 | runner logs option, stop check and double-run classifier | landed |
| 8638bfea | extra-crash helper | landed |
| 2d34380d | env overrides and a crash timed on a portal burst or commit stall | landed; `env.sh` is `config.sh` |
| 56b2c51a | dbstart, read a stopped manager's recorded start times | landed |
| 5b20f62f | bboltexp, check or churn a copy of a manager DB | landed |
| 0e81d28e | runner-log and start-time analysis, re-add-then-crash repro | landed; the repro is `repro/readdcrash.sh` |
| 88f62004 | readdcrash takes the binary from REPRO_WR | landed |
| 6f58edb9 | keep fusestall's page cache coherent for bbolt's mmap | landed |
| f6e9b60f | build wr from WRSRC, #649 running-dependent injector, check and repro, round-6 launcher | landed, the repro as `repro/rundepcrash.sh`, except `launch6.sh`: dropped, its settings are the README's example |
| f9d2db82 | double runs vs handed-out-anyway reservations; buried running-dependent restart repro | landed; the repro is `repro/rundepkill.sh` |
| 78363ebb | #654 release/bury-before-crash injector and check, ack latency and throughput, round-7 launcher | landed, except `launch7.sh`: dropped, as `launch6.sh` |
| 503efd1b | sweep another checkout's wrdev.sh, with monitor and prodsim, on chosen ports | landed (`SWEEP_WRDEV`, `SWEEP_PORTS`) |
| eaa9794e | #654 batches on a quiet isolated manager, latency per 10 minutes | landed; the batches are `repro/relburyiso.sh` |

## Runtime verification (2026-10-03)

The landed tooling was run end to end at `876018a5`, with each run's
`SOAK_ROOT` under `/nfs/hgi/wr/sb10-bigdb/soak8/`. Stops and crashes are
listed in the order they happened. Runs 1-3 were launched from
`soak8/bin/launch{1,2,3}.sh`, all with `RUNNER_FILELOG=1` and
`WRDEV_PRODSIM_KEEP_DB=1`:

| run | key settings | injectors |
| --- | --- | --- |
| 1 local1 | `SCHED=local USE_FUSE=0 HOURS=0.3 SIMMIN=6 SCALE=0.2 RESTART_MIN=4 RESTART_KINDS=crash,clean RAMP="0:40 8:80"`, `PRESTART_HOOK=loghook.sh` | `crashafter.sh 3 30`, `stopstate.sh`, `crashon.sh 2 stall 5` |
| 2 lsf2 | `SCHED=lsf QUEUE=normal USE_FUSE=0 HOURS=0.35 SIMMIN=6 SCALE=0.3 RESTART_MIN=6 RESTART_KINDS=crash,clean RAMP="0:100 8:300"` | `stopstate.sh`, `crashon.sh 2 burst 30` |
| 3 fuse3 | `SCHED=local USE_FUSE=1 FUSE_ON_AT=1 FUSE_OFF_AT=3 STALL_AFTER_MIN=2 STALL_SECS=60 HOURS=0.2 SIMMIN=6 SCALE=0.2 RESTART_MIN=3 RESTART_KINDS=clean,crash,clean RAMP="0:40"` | `stopstate.sh`, `crashon.sh 2 stall 30` |

With `RUNNER_FILELOG=1`, `$SOAK_WR` is a wrapper that runs `wr.real`, so the
daemonised manager's command line did not start with `$SOAK_WR`. That hid the
defect fixed in 31261f05, which only shows without runner logs, so the
injectors were checked again without `RUNNER_FILELOG` (run 7, in
`soak8/local7`).

| run | check | result |
| --- | --- | --- |
| 1 local | `run.sh` `SCHED=local`, no FUSE, 18 min: crash, clean, crashafter crash, crash, clean, final clean. Clean stops `aliveAtReturn=n token=deleted freelist=synced`; 0 LSF jobs left | PASS |
| 1 local | `stall.sh` and `crashon.sh` stall refuse without `USE_FUSE=1`, as designed | PASS |
| 1 local | `diskguard.sh` | PASS |
| 1 local | `crashafter.sh` | PASS |
| 1 local | `markers.py`: 263 runs, 0 doubles | PASS |
| 1 local | `doubles.py`: 0 | PASS |
| 1 local | `runnerlogs.py`: 118 logs, 0 problems | PASS |
| 1 local | `anyway.py`: 0 | PASS |
| 1 local | `latency.py`: 236 archives, p99 0.83s | PASS |
| 1 local | `dbstart` and `starttimes.py`: 5705 rows, StartTime p50 21ms, p99 94ms | PASS |
| 1 local | `bboltexp` check: 0 errors | PASS |
| 1 local | `stopcheck.sh`: local 8 in flight; LSF 214 with no end and 95 rc 0, all complete; 0 `EXIT0-NOT-COMPLETE`. Run 6 min after the stop, so the killed-must-be-buried check was not exercised | PASS |
| 1 local | `stopwatch.sh`, `stopstate.sh`: two of them running at once each took the other's `pgrep` for a stop (soak8/local1: 6 of 10 entries false, with empty `phase=` and `token=present`, and stray `profiles/stop.*.goroutine2.txt`) | FAIL, fixed in 99a7f8c6 |
| 1 local | `psinspect` | PASS |
| 1 local | `mon.sh`: "No such file or directory" every 10s for a missing `watcher.log`, `stall.log` or `hook.log` | FAIL, fixed in 6f9effed |
| 1 local | `rundepcheck.py` on an output dir with no `rundep/`: `FileNotFoundError` | FAIL (minor), fixed in 76e66194 |
| 2 LSF | 21 min, ramp 100->300, LSF RUN peak about 346. The up-front LSF check refuses without `bjobs`; `lsfprobe.sh` 11 rows. Crash, crashon-burst crash, clean 28.3s, crash, final clean 62.7s. Orphans: after a crash 159 at the stop and 101 still RUN at +300s, after a clean stop 0. Analysers: 11647 runs, 0 doubles, 1317 runner logs with 0 problems, archive p99 0.79s, StartTime p50 18ms. 0 LSF jobs left | PASS |
| 3 FUSE | `USE_FUSE=1`, 13 min: the hook moved the DB behind `fusestall` at restart 1 and back at restart 3; the stall held fsync for 1m8.7s; the crash restart reopened through the `/tmp` mount; unmounted with no process left; 0 doubles; `bboltexp` 0 errors | PASS |
| 3 FUSE | `crashon.sh` stall: `[: 0 0: integer expression expected` every second while waiting for the trigger | FAIL, fixed in edb2bf20 |
| 3 FUSE | `crashon.sh` stall: the manager killed during the held fsync kept its port, so the start 3s later failed with the port in use (soak8/fuse3 `restarts.tsv` `start rc=1 manual=crashon-stall`, also seen in round 7), and `watcher.sh` then ran a redundant start | FAIL, fixed in 46a2e46d |
| 4 repros | On current develop: `readdcrash.sh` 0 and 1 (one run, Attempts 1); `rundepcrash.sh` 0, 1 and `1 70 40` (D runs=2, 0 bad job); `rundepkill.sh` 0-3 (D stays buried, per the spec); `relburyiso.sh 500 2000` (`relburycheck.py` 0 problems, all buried, 0 LSF jobs left) | PASS |
| 5 wrdev | `freelist-check` (66s; manager median commit 2.37ms vs 32ms plain at 262144 free pages); `selfconnect-check` (80s); `RESTART_KINDS`, `PRESTART_HOOK`, `DBDIR`, `FINAL_STOP` (columns, `hook.log`, DB symlink, final stop line, 5 poststart and prestop profile sets) | PASS |
| 6 round 7 | The analysers on round-7 data match the saved round-7 outputs byte for byte: markers, doubles 202 (198+1+3), anyway 8436, rundep OK 25 CHECK 2, latency archive p50 1.69s p99 41.71s, StartTime p50 21ms (n=527473), relbury all buried with 0 stuck running, dbstart 933838 rows, `bboltexp` 0 errors. `rl.final.txt` and `unacked.tsv` differ only by the relative runner-log path. The original soak4 scripts give identical output | PASS |
| 7 local7 | Injectors without `RUNNER_FILELOG`: local soak on the binary from 7893f426, output `prodsim-1790984180`. `crashafter.sh` waited out a scheduled clean stop, then `stop rc=crash manual=crashafter` and `start rc=0`; `crashon.sh` burst `stop rc=crash manual=crashon-burst`, `start rc=0` | PASS |
| 7 local7 | `relbury.sh` and `relburycheck.py`: 0 problems. The #654 case was weak here: on a busy local host the jobs had not started by the deadline. Runs 4 (`relburyiso.sh`) and 6 (round-7 data) exercise it | PASS (tooling) |
| 7 local7 | `rundep.sh` and `rundepcheck.py`. The #649 case was not exercised: D never started for 2 of 3 instances. Runs 4 (`rundepcrash.sh`, `rundepkill.sh`) and 6 exercise it | PASS (tooling) |
| 7 local7 | `stopwatch.sh`, `stopstate.sh`: only the two real mid-run clean stops, `phase=waitForRunnersToDie`, `state=gone token=absent`, exactly 4 stop profiles. Both exit when prodsim does, so the final cleanup stop is not recorded, by design (now in the README) | PASS |
| 7 local7 | `mon.sh` writes nothing to stderr; `crashon.sh` no integer-expression noise | PASS |
| 7 local7 | `watcher.sh` ran no redundant start. Vacuous here, since no start failed; the `f4w.sh` stand-in harness covers that path | PASS |
| 7 local7 | 289 runs, 0 doubles; `rundepcheck.py` on a dir without `rundep/`; 0 left after cleanup | PASS |
| 8 prodstart8 | Live `wrdev.sh prod-start` after 27e0984c: `repro/readdcrash.sh 1` with `REPRO_WR` built from f5a98fa5 and `REPRO_PORTS="51936 51937 51938 51939"`, rc=0. prod-start passed the `wr conf` check; the re-added running job ran once (one run/end mark pair), Attempts 1, complete; the managers were killed after, 0 processes left (`soak8/prodstart8/readdcrash.1.out`) | PASS |

The fixes:

- 99a7f8c6 and 31261f05: `config.sh`'s `soak_wr_running` matches only a
  command line that starts with `timeout N $SOAK_WR manager <subcommand>`.
  Every start and stop of our manager runs under `timeout`, while the
  daemonised manager keeps its start's argv for life, so the first version,
  which also matched a bare `$SOAK_WR manager start`, took the running manager
  for a start in progress. `stopwatch.sh`, `stopstate.sh`, `crashon.sh`,
  `relbury.sh` and `watcher.sh` use it in place of an unanchored `pgrep -f`.
- 6f9effed: `mon.sh` groups its `wc -l < file` so the redirection's own error
  is discarded.
- edb2bf20: `crashon.sh` keeps `grep -c`'s single count, where `|| echo 0`
  appended a second 0 when nothing matched.
- 46a2e46d: after the kill, `crashon.sh` and `crashafter.sh` wait up to 10 min
  for the pid to be gone (`soak_wait_gone`) before starting the manager, and
  note in `watcher.log` if it is still there. `watcher.sh` does not start a
  manager after a failed start if a different one of ours is up.
- 76e66194: `rundepcheck.py` prints "no rundep run in <outdir>" and exits 0,
  as the other analysers do when there is nothing to check.
- 408f2a3e: `crashafter.sh` waits out a scheduled restart in progress, as
  `crashon.sh` does.
- 6dec8b1f: `repro/rundepkill.sh` and `repro/relburyiso.sh` run
  `soak_isolated` before their clean `wr manager stop`, and kill their own
  verified pid instead if it fails. 94ce84b6 makes the failure message say
  only that the isolation check failed, since `config.sh` itself can fail it.
- 27e0984c: `wrdev.sh prod-start`, which the repros start through, runs
  `assert_isolated` before starting, as prodsim's start does, so a
  `.wr_config` override or `WR_` variable cannot point it at another manager.
- 45d4a51a: that check runs in a subshell, so a refusal is a failed return and
  each caller's abort path runs (crash-recovery's bkill and `safe_kill`).
- 5df4420c: each repro stops with exit 1 when `wrdev.sh prod-start` fails,
  where it piped the start through `tail` and carried on.
