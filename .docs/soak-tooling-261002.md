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
