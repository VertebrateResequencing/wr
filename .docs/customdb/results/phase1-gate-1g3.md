# Phase 1 gate: crash soak at production LSF concurrency (item 1g.3)

Gate (spec.md, Implementation Order, Phase 1, Gates; phase1-gate.md item
1g.3): the crash soak with a staged ramp (battery10 shape, about 6,000
runners, at least 3h, injected stalls and crashes, `relbury.sh` batches at
the injected kills), without `FIXTURE`. It passes when `soakgate.py`'s
`missing` line has `absent=0`, `relburycheck.py` prints `problems: 0`,
`soakgate.py --source wait` prints `nondurable source=wait inside=0
outside=0` with an `abandoned` line ending `ok` and `stalls` at least 1,
and the `doubles` line has `outside=0`. `wr status -l 1` is also timed just
after each injected restart, and any time over 60s fails the gate. By the
owner's ruling of 2026-11-18, `wr status -i <rep group> -z --limit 1` is
timed and reported but does not gate; item 2b.3 owns it.

Two runs so far; neither passes, and item 1g.3 stays unticked.

- [Run 2](#run-2-2026-11-18-097dcf8c) (2026-11-18, 097dcf8c): not passed.
  Scale peaked at 4,968 RUN with the daytime farm full;
  `relburycheck.py` printed `problems: 1`, a stale retry of a durable but
  unacknowledged release, with no acknowledged write lost; the other four
  soak criteria and `wr status -l 1` (at most 216ms) pass. The run also
  exposed a log-store defect: the folder fell steadily behind the add
  rate, so no checkpoint completed after 13:41 and every start replayed a
  growing wal. Its last hour was compromised by a co-tenant's memory use:
  the kernel OOM-killed the manager four times.
- [Run 1](#run-1-2026-11-0910-fe45846d) (2026-11-09/10, fe45846d): the
  five soak criteria pass and `wr status -l 1` peaked at 152ms; scale was
  4,110, capped by its ramp.

## Run 2: 2026-11-18, 097dcf8c

Run on Wednesday 2026-11-18, 13:00:54 to 16:02 GMT, from farm22-wrstat01
on LSF queue `normal`, with the wr binary built once from an export of
`097dcf8c` (`customdb-f397b547`, log store the default). Work directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g3/`, soak tree `soak/run2/`.

**Verdict: not passed**, on scale (peak 4,968, not about 6,000) and on
`relburycheck.py`'s literal `problems: 1`. A rerun is needed after the fold
fix and the soak changes proposed below; this run's last hour (14:50 on)
does not measure the store alone.

### Method

- Source: `git archive 097dcf8c` into `src-097dcf8c/`, with `git init` and
  one local commit (`eb2dbb5`) so that `soak/config.sh` finds a checkout.
  `run.sh` built the soak's wr from it once; it was never replaced while
  runners were live.
- Launcher [run2-soak-go.sh](phase1-gate-1g3/run2-soak-go.sh) `main`: the
  first run's `soak-go.sh` with `SOAK_ROOT` `soak/run2`, `GOCACHE` in the
  work directory, `startprof.sh` from the start, and the ramp
  `RAMP0=1500 RAMP="0:1500 4:2500 8:3500 13:4500 18:5300 24:6000"`, so the
  `results_portal` target was 6,000 from t+24m
  ([run2-ramp.log](phase1-gate-1g3/run2-ramp.log)). Everything else as the
  first run: `HOURS=3`, `RESTART_MIN=15`, `USE_FUSE=1`, the same injectors,
  `relbury.sh` r1 and r2, `stopstate.sh`, `rundep.sh`, ports and isolated
  `HOME`, no `FIXTURE`, `WRDEV_PRODSIM_KEEP_DB=1`.
- [statustime.sh](phase1-gate-1g3/statustime.sh) and
  [startprof.sh](phase1-gate-1g3/startprof.sh) unchanged from the first
  run.
- After the run: `bjobs` listed no `wrpiso53312h3579731207_*` job and no
  manager pid from `restarts.tsv` was running, so the soak's own cleanup
  had finished and nothing was killed by hand. The store was kept for
  `dbstart`.
- Analysis: [run2-analyse.sh](phase1-gate-1g3/run2-analyse.sh), the first
  run's `analyse.sh` pointed at `src-097dcf8c` and `soak/run2`, with
  `GOCACHE` and its own `dbstart` build under the work directory. It ran as
  LSF job 951591 with 120GB reserved, since `dbstart` recovers the store's
  2.66M live jobs and the soak host had no memory to spare (Investigation).
- The investigation read the soak's `manager.log*`, `samples.tsv`,
  `statustime.tsv`, `lsf.tsv`, `lsfprobe.tsv`, its profiles, the host's
  `dmesg` and `atop` logs, and the first run's and battery10's records.
  [run2-starts.py](phase1-gate-1g3/run2-starts.py) and
  [run2-foldlag.py](phase1-gate-1g3/run2-foldlag.py) made the tables.

### Soak criteria

```text
nondurable source=wait inside=0 outside=0 runs=884856 outsidePct=0.0000
totals n/a
abandoned n=786 stalls=1 ok
doubles inside=0 outside=0 acknowledged=0
missing ran=871410 absent=0 excused=192
peakRUN=4968
```

| criterion | result |
| --- | --- |
| `missing` `absent=0` | PASS (871,410 keys ran, 0 absent) |
| `relburycheck.py` `problems: 0` | FAIL as printed (`problems: 1`); no acknowledged write lost (below) |
| `nondurable source=wait inside=0 outside=0` | PASS |
| `abandoned` line ends `ok`, `stalls` >= 1 | PASS (`n=786 stalls=1 ok`) |
| `doubles` `outside=0` | PASS |

`markers.py` found no double run and no overlap
([run2-markers.txt](phase1-gate-1g3/run2-markers.txt)). The 786 abandoned
reservations are 53 in the injected stall (lines at 13:54:43 and 13:55:43) and
733 in the line at 14:55:43, from the stall that was not injected (Waits, under
Investigation). `soakgate.py` prints `ok` when the soak had a stall window and
some reservation was abandoned; every abandoned reservation still has
`notDurable=0`. `rundepcheck.py`: 28 OK, 2 OK-late-add, 5 CHECK and 1
B-not-added. Each CHECK is a dependency run killed by a clean stop
(`D-noend-then-stop-clean`), as in the first run, and the B-not-added is the
last instance, which the soak ended before adding
([run2-rundepcheck-summary.txt](phase1-gate-1g3/run2-rundepcheck-summary.txt)).

`relburycheck.py`
([run2-relburycheck.txt](phase1-gate-1g3/run2-relburycheck.txt)): r1 (kill at
D+646ms) had no problem: all 400 buried, every release ran twice and every bury
once. r2 (kill at D+2126ms) had 180 releases and 180 buries acknowledged before
the kill; all 400 ended buried, each release job ran twice and each bury job
once. Its one problem is `BAD-JOB relb.r2.rel.113`, traced in
[run2-relbury-badjob.txt](phase1-gate-1g3/run2-relbury-badjob.txt):

1. Its first run exited 3 at 14:09:51.035. The manager recorded the
   release (state ready, attempt 1, ended 14:09:52.773) and was killed at
   14:09:53.126 before the runner had the reply; the runner logged
   `receive time out` at 14:10:53.
2. After the restart the job was ready in the `up` snapshot. A second
   runner ran it at 14:12:56 (its second and last attempt), it exited 3
   and was buried, as `--retries 1` requires.
3. At 14:13:02 the first runner reconnected and sent its release again.
   The job was no longer reserved by it, so the manager answered `bad job
   (not in queue or correct sub-queue)`.

So the release was durable but unacknowledged, the job ran exactly the
two times it should and ended buried, and no acknowledged release or bury
was lost, which is what spec.md says `problems: 0` shows. The rejection of
the stale retry is the expected answer to a report from a runner that no
longer holds the job. The first run, whose r2 kill came at D+2356ms, had
no such case. Whether the gate counts it is for the owner: either
`relburycheck.py` stops counting a `bad job` answer to a final-state report
the runner never had acknowledged when the store shows that report
applied, or the manager answers a repeated release idempotently. Either
way the gate's literal criterion fails on this run.

`runnerlogs.py` reported 5 runs as `rerun` (as the first run had 9), each a
runner whose final report was rejected after a stall or restart, this job
among them ([run2-runnerlogs.txt](phase1-gate-1g3/run2-runnerlogs.txt)).
Its 39,644 failed final-state reports (11,952 in the first run) are
runners that reported while the manager was down or swamped, mostly in the
last hour.

`dbstart` ran for 1h16m with a 63GB peak RSS, on a farm node
([run2-dbstart-err.txt](phase1-gate-1g3/run2-dbstart-err.txt)). It folded
the stopped store's 753MB backlog, 1,104,339 items in 73 minutes, 2,587-71,231
a minute with a mean transaction hold of 1-8s: about 15,000 items a
minute, on a host with no memory pressure.

Load: 884,856 runs of 871,410 keys over 3h. `history.bolt` was 9.6GB at
the soak's end and 10.6GB after `dbstart` folded the backlog. The stops
were 10 `kill -9` of a live manager (6 injected: relbury-r1, crashon-stall
in the stall, crashon-burst twice, relbury-r2, crashafter; 4 scheduled),
3 clean stops mid-run (each ending in `wrdev.sh`'s kill, below), 3
scheduled stops that found the manager already dead (`rc=skipped`), the
final clean stop, and 4 kernel OOM kills of the manager, which
`restarts.tsv` does not record
([run2-restarts.tsv](phase1-gate-1g3/run2-restarts.tsv)).

### `wr status` just after each restart

[statustime.sh](phase1-gate-1g3/statustime.sh), unchanged, ran B
`wr status -i portal -z --limit 1`, A `wr status --limit 1`, C
`wr status -l 1` and B2 (B again) one after another as each start line
reached `restarts.tsv`. Times in ms; backlog is the unfolded wal at the
start ([run2-statustime.tsv](phase1-gate-1g3/run2-statustime.tsv)).

| start (epoch) | after | backlog | B | A | C | B2 |
| --- | --- | --- | --- | --- | --- | --- |
| 1795006857 | initial | 3.5MB | 80 | 55 | 53 | 119 |
| 1795007767 | crash | 2.9MB | 34,931 | 201 | 105 | 1,705 |
| 1795008181 | relbury-r1 | 58.2MB | 37,005 | 180 | 99 | 2,698 |
| 1795008743 | clean | 97.8MB | 53,435 | 314 | 60 | 3,838 |
| 1795009738 | crash | 69.8MB | 77,872 | 669 | 46 | 6,243 |
| 1795010345 | crashon-stall | 118.2MB | 17,663 | 1,473 | 134 | 8,824 |
| 1795010561 | crashon-burst | 204.4MB | 105,170 | 740 | 66 | 10,590 |
| 1795010765 | clean | 258.5MB | 59,415 | 954 | 82 | 12,909 |
| 1795011091 | relbury-r2 | 294.7MB | 1,066, rc=1 | 38, rc=1 | 38, rc=1 | 35, rc=1 |
| 1795011173 | crashafter | 286.7MB | 42,553 | 1,220 | 130 | 10,220 |
| 1795011367 | crashon-burst | 328.7MB | 77,903 | 1,477 | 55 | 30,173 |
| 1795011799 | crash | 351.0MB | 25,828 | 1,642 | 79 | 18,010 |
| 1795012842 | crash | 376.5MB | 44,921 | 1,561 | 174 | 20,872 |
| 1795014168 | clean | 517.0MB | 120,110, rc=1 | 144, rc=1 | 221, rc=1 | 376, rc=1 |
| 1795015481 | OOM kill | 502.7MB | 120,100, rc=1 | 16,017 | 216 | 120,378, rc=1 |
| 1795016782 | OOM kill | 645.0MB | 120,097, rc=1 | 13,794 | 157 | 120,101, rc=1 |

- C, the gate's criterion: at most 216ms after every restart it could be
  measured at, against 60s. PASS where measured. Two readings failed fast (rc=1)
  because the manager had gone: at 1795011091 `crashafter` killed it a second
  after its start line, and at 1795014168 the OOM killer took it while B was
  waiting. The start at 16:01:23 never wrote a start line, as the soak ended
  during it, so it has no reading.
- A: at most 16.0s, both readings over 2s in the memory pressure after
  15:20.
- B (reported, not gated, owner ruling 2026-11-18): over 60s after 6 of
  the 16 starts; the client gave up at 120s after the last three.
  B2 reached 30.2s while the host was healthy and timed out twice at
  120s. B's cost grows with history, as the first run found; item 2b.3
  owns it.

### Scale

Peak LSF RUN was 4,968 at 13:22:02, with 1,624 PEND
([run2-lsf.txt](phase1-gate-1g3/run2-lsf.txt)). RUN was 4,000 or more in
123 of the 180 one-minute samples, 4,400 or more in 43 and 4,800 or more in
12. From 13:40 to 14:50, between restarts, RUN held at 4,200-4,590 with
1,000-6,800 PEND. After 14:50 it peaked at 4,222. RUN held at 3,755 and
3,730 while the manager was dead (15:10-15:20, 15:50-16:01): runners whose
manager had gone, waiting for the next one (`orphans.tsv`).

The farm set the cap, not the ramp. The ramp asked for 6,000 from t+24m
([run2-ramp.log](phase1-gate-1g3/run2-ramp.log)) and each portal burst was
sized for 6,000. Once RUN stopped rising, every `bjobs -p` sample in
[run2-lsf.txt](phase1-gate-1g3/run2-lsf.txt) gives two reasons for the
soak's pending jobs: "New job is waiting for scheduling" (a resubmission
burst that LSF had not yet dispatched) and, for the jobs LSF had looked
at, "Affinity resource requirement cannot be met because there are not
enough processor units to satisfy the job affinity request: 86-94 hosts"
or "Job slot limit reached: 93-94 hosts". Queue `normal` has no per-user
or per-queue job limit (`MAX -`, `JL/U -`) and shares by fairshare across
`WTSI`; its `RES_REQ` asks one core per job (`affinity[thread(1)]`). So
every host the queue could use had no free core or no free slot: the farm
was full. The soak did not record `bhosts` or `bqueues` during the run.
After it, at 16:30, queue `normal` held 28,499 jobs from all users with
24,555 pending, which shows a busy farm but not its state at 13:20-14:50.

Unlike the first run, the daytime farm was the limit here, so a rerun that
must reach about 6,000 needs a farm with about 1,500 more free cores for
this soak's user than this Wednesday afternoon gave. Nights and weekends
are when that is most likely, but this soak has no measurement of it. A
probe of free cores (`bhosts` summed over the hosts in `standard_hosts`)
before the start would show it.

### Investigation

Five things were seen while it ran: manager start times growing with each
restart (to 412s), the folder's `maxLag` growing (the operator saw 5m41s;
the peak was 17m35s at 14:56:50), minutes of reservation and add `p99` of
1-8s, manager RSS up to 76.8GB, and `load1` up to 1,196. They have three
causes, in the order they act.

#### 1. The soak's offered load was over three times battery10's

prodsim's dynamic portal mode (`developers/prodsim/soak.go`
`portalDynamic`) submits a burst every 5 minutes sized to keep the target
concurrency busy, and waits for nothing but its own adds. With bbolt's
slow adds the burst loop throttled itself; with the log store's fast adds
it did not ([run2-offered-load.txt](phase1-gate-1g3/run2-offered-load.txt)):

| soak | portal add p50 | portal bursts in 3h | jobs submitted | runs | live jobs at the last start |
| --- | --- | --- | --- | --- | --- |
| battery10 (bbolt, target 4,000) | 6.9-8.5s | 15 | 1,005,371 | 972,213 | 199,204 |
| first run (log, target 4,000) | 162-179ms | 36 | 2,765,033 | 853,677 | 1,922,266 |
| run2 (log, target 6,000) | 205-222ms | 28 | 3,534,488 | 884,856 | 2,663,130 |

Runs were about 0.9M in all three: the runners, not the store, set the
drain rate. In both log-store soaks submissions outran it, so the live
(incomplete) set grew by 0.6-0.9M jobs an hour, from 79,254 at the first
restart to 2,663,130 at the last. battery10's live set fell from 279,000
to 199,000. So battery10's 12-52s starts and 20GB RSS are not a like for
like baseline for the log store's starts and memory: they had 7-13 times
fewer live jobs.

#### 2. The folder fell behind, and no checkpoint completed after 13:41

The folder folds a `complete` record for each completion and an rglookup for
each added job, so its work follows the add rate. In run2 that was about 19,600
adds and 4,900 completions a minute. The folder's `"db writer" writer=fold`
lines folded 3,156,074 items in 134 minutes, a mean of 23,553 a minute, while
its `meanHold` rose from 0.1s to 4-6s per transaction as `history.bolt` grew on
NFS ([run2-fold.txt](phase1-gate-1g3/run2-fold.txt)). Measured in wal bytes
between `statustime.tsv` readings, the folder advanced 0.04-0.42MB/s while the
wal grew 0.08-1.39MB/s, faster in the first minutes after each start. The
unfolded backlog grew from 3MB at 13:15 to 753MB at 16:01 (`backlogBytes` in
[run2-statustime.tsv](phase1-gate-1g3/run2-statustime.tsv)), and 105 of the 134
fold lines report `maxLag` over 60s, 17 over 10 minutes. `dbstart`, folding the
rest of the backlog on an idle farm node after the soak, managed about 15,000
items a minute, so host load alone does not explain the shortfall. The first
run, with 15,400 adds a minute, folded about the same 22,000-27,000 items a
minute and kept up: its backlog never passed 43MB and most stops found one or
two wal files.

A checkpoint folds to its cut before it writes (spec.md "Checkpoints", step 2,
and `cutAndFold` -> `foldNow` in `store_checkpoint.go`), taking turns with the
folder. Once the backlog at a checkpoint's cut took longer to fold than the
manager lived (4-17 minutes between kills), no checkpoint finished. In five of
the six pre-stop goroutine dumps the checkpointer is in `cutAndFold` ->
`foldNow`
([run2-checkpoint-stacks.txt](phase1-gate-1g3/run2-checkpoint-stacks.txt)), and
the store directory after the soak holds one checkpoint, `0000000a.ckpt` from
13:41, and every wal file from `0000000a.wal` to `00000021.wal`, 2.3GB
([run2-store-listing.txt](phase1-gate-1g3/run2-store-listing.txt)). By the wal
deletion rule no wal file below the newest checkpoint's `walFrom` can be
deleted, so every start replayed and re-read all of it.

This is a defect of the log store against its spec. spec.md's Key
Decisions say "the folder trails the log by about a second", and its
Checkpoints section has a checkpoint every 5 minutes or 256MiB of wal; the
spec's lag-scaled fold transactions (`foldLagStep`) are its only answer to
a folder that falls behind, and here they did not stop the lag growing.
The spec has no bound on the backlog and no back-pressure on adds, and a
checkpoint that waits for the folder inherits its lag, so the wal and the
replay at each start grow without bound while adds outrun the folder. It
is not a bbolt regression in the usual sense: bbolt writes the same
rglookups inside the add's own transaction, which is why its adds took
7-8s and throttled prodsim. The log store moved that cost off the add's
reply path without bounding it.

#### 3. A co-tenant took the host's memory; four managers were OOM-killed

The soak host, farm22-wrstat01, has 91GB of RAM and 8 cores. From about
14:50 a `wrstat-ui` process owned by `mercury`, started from cron, grew from
1.4GB to 57-80GB (atop, [run2-memory.txt](phase1-gate-1g3/run2-memory.txt)).
On the first run's night it stayed at 5.9GB or less. With the manager
at 56-69GB the host ran out of memory, and the kernel's OOM killer killed
the soak's manager four times and `wrstat-ui` three times:

| manager pid | started serving | OOM-killed (wall clock) | anon RSS |
| --- | --- | --- | --- |
| 1402732 | 15:02:47 | about 15:04-15:05 | 56.6GB |
| 1616846 | 15:24:40 | about 15:32-15:33 | 69.2GB |
| 1850112 | 15:46:22 | about 15:48-15:49 | 68.7GB |
| 2062726 | 16:05:23 | about 16:07-16:08, during the final stop | 59.7GB |

`dmesg` times on this host run 7.0-8.4 minutes behind the wall clock; the
wall-clock times come from the last line of each manager's log and the
first `samples.tsv` row that finds it gone. The `wrstat-ui` kills were at
about 15:21, 15:43 and 16:03. So the scheduled stops at 15:17:48,
15:39:42 and 16:01:23 found no manager (`rc=skipped` in
[run2-restarts.tsv](phase1-gate-1g3/run2-restarts.tsv)), and the soak had
no serving manager for about 46 of its last 60 minutes, counting the three
356-412s starts. Nothing in the soak restarts a manager that dies on its
own.

The manager's memory is its live jobs. The in-use heap at 15:25:54, 74s after a
start that decoded 2,179,296 live jobs, was 28.3GB, 22.2GB of it the spec record
bodies `recoverIncompleteJobs` read
([run2-heap.txt](phase1-gate-1g3/run2-heap.txt)), which the decoded jobs'
strings alias (`flatReader.str`). That is 13KB a live job. battery10's pre-stop
heap was 1.9GB for 261,221 live jobs, 7.3KB a job, but its live set included the
fixture's 120,000 jobs, so the per-job figures are not comparable. The first run
reached 75.4GB with 1.92M live jobs on an otherwise idle host and was never
OOM-killed. Whether the log store holds more per live job than bbolt is unknown;
a heap profile of both stores holding the same job set would decide it. Memory
in proportion to live jobs is how the manager has always worked
(requirements.md: the manager holds every live job in memory), so the 64-77GB is
a consequence of cause 1, not a defect found here.

#### Start times

[run2-starts.txt](phase1-gate-1g3/run2-starts.txt) splits every start into
the phases the manager logs (`recovering: opened database`, `decoded live
jobs`, `built dependency-group state`, `enqueued prior jobs`):

| start | total | open | decode | dep groups | enqueue | live jobs | wal since checkpoint |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 13:16 | 10.7s | 3.1s | 2.2s | 0.8s | 0.4s | 79,254 | - |
| 13:48 | 91.2s | 15.3s | 22.4s | 32.1s | 12.3s | 680,224 | 242MB |
| 14:06 | 72.9s | 21.6s | 28.4s | 12.3s | 7.7s | 1,002,045 | 563MB |
| 14:40 | 135.0s | 36.6s | 57.5s | 21.1s | 11.8s | 1,730,159 | 1,325MB |
| 15:02 | 356.0s | 56.3s | 223.8s | 55.8s | 16.3s | 2,081,837 | 1,730MB |
| 15:24 | 411.7s | 82.8s | 177.9s | 128.8s | 17.2s | 2,179,296 | 1,807MB |
| 15:46 | 399.3s | 107.9s | 203.2s | 64.3s | 18.5s | 2,465,658 | 2,088MB |

Start time follows the live set first. Until 14:40, decode cost 27-50us
a live job, as in the first run (25-33us), and the four in-memory phases
grew with the live set. The `opened database` phase also grew with the wal
since the checkpoint: 36.6s at 1.73M live jobs and 1.3GB of wal against
the first run's 23.1s at 1.76M live jobs with checkpoints keeping up. From
15:02 decode cost 82-108us a job and dependency groups up to 129s: those
three starts ran in the memory pressure of cause 3: in each 10-minute atop
interval from 14:50 the page cache was 0.1-13GB (51-57GB before 13:50),
the kernel made up to 22,986 direct-reclaim stalls an interval (0 or 1
before 14:50) and swapped up to 2.2M pages out
([run2-memory.txt](phase1-gate-1g3/run2-memory.txt)). At the per-job costs
of 14:40, those three starts would have taken roughly 170-240s, so roughly
half of their 356-412s was host memory pressure; the rest is the live set
and the unbounded wal. Start time does not follow the unfolded
backlog as such: the backlog is folded in the background, not during the
start. It follows the wal kept since the last checkpoint, which cause 2
let grow.

spec.md sets no start-time target. requirements.md records recovery time
in proportion to live bytes on NFS as a property only D3's lazy recovery or
D5's local copy removes, so the growth with the live set is inherited, not
a regression. battery10's 12-52s starts had at most 279,000 live jobs.

#### Host load

`load1` reached 1,196 at 15:49:36, 730-801 in the next minute and 351 in atop's
15:40-15:50 interval. Every sample over 100 (14:58, 15:05, 15:27, 15:49-15:53)
falls in the memory pressure: the first two around the OOM kill of pid 1402732,
15:27 with the manager at 74.6GB, and 15:49 at the OOM kill of pid 1850112
([run2-memory.txt](phase1-gate-1g3/run2-memory.txt)). Linux counts tasks in
uninterruptible sleep, such as direct reclaim and swap-in, in the load average,
and atop counted 13,495 direct-reclaim stalls and 2.2M pages swapped out in its
15:40-15:50 interval, so a load of 1,196 on 8 cores is tasks stalled on memory,
not CPU demand. Before 14:50 `load1` stayed under 70 (in the injected NFS stall
and at manager starts). Whether the manager's threads or `wrstat-ui`'s made up
those tasks is unknown: atop does not record per-process load.

#### Waits

Outside the injected stall (13:54-13:58), every minute with reservation
or add wait `p99` over 1s was at or after 14:50, in the memory pressure
([run2-fold.txt](phase1-gate-1g3/run2-fold.txt)): reservation p99 1.1s
(14:50), 2.2s (14:55), 4.7s (15:03), 8.2s (15:27), 1.8s and 3.0s
(15:47-15:48); add p99 4.9s (15:03) and 8.2s (15:27), plus 1.0-2.1s at
14:17, 14:23 and 14:41. One stall was not injected: from 14:55:08 to the
clean stop at 14:55:43, writes stopped being durable within 10s. 1,433
`jarchive` replies took 10-21s and 733 reservations were abandoned
(`notDurable=0 abandoned=733`), while the page cache was collapsing from
21.5GB toward 0.1GB in atop's 14:50-15:00 interval. Its cause is not
established: this manager had no pprof (port 6331 was still bound by its
predecessor when it started), so there is no goroutine dump of it.

#### The clean stop killed after 68s

At 14:55:43 the clean stop of pid 1198417 spent about 50s in
`waitForRunnersToDie` while LSF RUN fell from 4,081 to 44, and the manager
logged "gave up waiting for runners to exit" at 14:56:44 and "gracefully
stopped" at 14:56:51. `wr manager stop` returned at 14:56:51 with the
process still alive, so `wrdev.sh` killed it (`aliveAtReturn=y`). All three
clean stops of a live manager mid-run ended that way, as did 3 in the
first run, 4 of 5 in battery10 and 5 of 5 in nohold-g3b: an existing
behaviour of `wr manager stop`, not new here.

#### What is unknown

- Whether the folder alone, with no `dbstart` reading jobs beside it,
  keeps up at run2's add rate on a quiet host. Everything measured says
  not: it was short of that rate before 14:50, and `dbstart` on an idle
  farm node folded about 15,000 items a minute.
- Whether the log store holds more memory per live job than bbolt.
- The cause of the 14:55:08 durability stall.
- How many free cores the farm offers this user at night or at a weekend.

### Proposed next steps

1. **Bugfix: bound the fold backlog and free checkpoints from it.** Route
   through `/bugfix` with a red test first: a log store whose folder is
   slowed by a test hook below the rate of adds, run past several
   checkpoint intervals, must still complete checkpoints and keep its wal
   files bounded; today it completes none. The spec owner should choose
   the fix, as each option changes spec.md: let a Phase 1 checkpoint hold
   the overlay's spec records, as the Phase 2 checkpoint already does
   (Checkpoints, step 3), so it no longer waits for the folder; make adds
   wait, boundedly, while the backlog is over a limit, the back-pressure
   bbolt's in-transaction rglookups gave; or make folding rglookups,
   about 80% of the items in this soak, cheaper. A scale check after the
   fix: `wrdev.sh` or a soak with a sustained 20,000 adds a minute on NFS
   against a multi-GB `history.bolt`, where `maxLag` must stay bounded.
2. **Owner ruling on the soak's offered load.** prodsim's dynamic portal
   submits on a timer whatever the farm drains, so on the log store the
   live set grows by up to 0.9M jobs an hour and dominates start time and
   memory. battery10's shape held its live set at 200,000-280,000 only
   because bbolt's adds were slow. Either cap prodsim (skip a burst while
   the portal's incomplete jobs exceed, say, twice the target), so a rerun
   measures the store at battery10's live set, or keep the growth and add
   a memory and start-time budget for it to the gate. The first is the
   comparison the gate asks for.
3. **A soak host the soak cannot starve or be starved on.** farm22-wrstat01
   also runs `mercury`'s `wrstat-ui` from cron, which reached 57-80GB on
   this afternoon; between them the kernel OOM-killed the two seven
   times. Run the rerun where nothing else needs the memory, or at night
   when `wrstat-ui` stayed at 5.9GB, and add a watchdog that ends the soak
   (with its own cleanup) when `MemAvailable` falls below about 10GB.
   The soak should also record a manager that dies on its own as a stop,
   since `doubles.py` and `soakgate.py` window their checks on
   `restarts.tsv`.
4. **Then the about 6,000-runner rerun, at a weekend.** This Wednesday
   afternoon the farm capped the soak at 4,200-4,970 with every candidate
   host full; the first run's night supplied 4,110 with PEND about 0 but
   was never asked for more. A Saturday or Sunday start is the likeliest to
   find about 1,500 more free cores. Before starting, check free slots on
   `standard_hosts` (`bhosts`) and `bqueues normal` PEND, and start only
   if about 6,500 cores are free; then confirm the plateau in `lsf.tsv`
   as RUN about 6,000 with PEND about 0, as the first run's record says.
   Rerun only after 1-3, or the rerun will repeat this one.

### Files

- [run2-soakgate.txt](phase1-gate-1g3/run2-soakgate.txt),
  [run2-relburycheck.txt](phase1-gate-1g3/run2-relburycheck.txt),
  [run2-markers.txt](phase1-gate-1g3/run2-markers.txt),
  [run2-doubles.txt](phase1-gate-1g3/run2-doubles.txt),
  [run2-runnerlogs.txt](phase1-gate-1g3/run2-runnerlogs.txt),
  [run2-rundepcheck-summary.txt](phase1-gate-1g3/run2-rundepcheck-summary.txt),
  [run2-dbstart-err.txt](phase1-gate-1g3/run2-dbstart-err.txt),
  [run2-relbury-badjob.txt](phase1-gate-1g3/run2-relbury-badjob.txt):
  analysis outputs.
- [run2-restarts.tsv](phase1-gate-1g3/run2-restarts.tsv),
  [run2-statustime.tsv](phase1-gate-1g3/run2-statustime.tsv),
  [run2-hook.log](phase1-gate-1g3/run2-hook.log),
  [run2-stall.log](phase1-gate-1g3/run2-stall.log),
  [run2-ramp.log](phase1-gate-1g3/run2-ramp.log),
  [run2-orphans.tsv](phase1-gate-1g3/run2-orphans.tsv): the soak's own
  records.
- [run2-lsf.txt](phase1-gate-1g3/run2-lsf.txt): scale.
- [run2-offered-load.txt](phase1-gate-1g3/run2-offered-load.txt),
  [run2-fold.txt](phase1-gate-1g3/run2-fold.txt),
  [run2-checkpoint-stacks.txt](phase1-gate-1g3/run2-checkpoint-stacks.txt),
  [run2-store-listing.txt](phase1-gate-1g3/run2-store-listing.txt),
  [run2-starts.txt](phase1-gate-1g3/run2-starts.txt),
  [run2-memory.txt](phase1-gate-1g3/run2-memory.txt),
  [run2-heap.txt](phase1-gate-1g3/run2-heap.txt): investigation evidence.
- [run2-soak-go.sh](phase1-gate-1g3/run2-soak-go.sh),
  [run2-analyse.sh](phase1-gate-1g3/run2-analyse.sh),
  [run2-starts.py](phase1-gate-1g3/run2-starts.py),
  [run2-foldlag.py](phase1-gate-1g3/run2-foldlag.py): the launcher,
  analysis and table scripts.

## Run 1: 2026-11-09/10, fe45846d

Run on 2026-11-09/10 (23:34:52 to 02:40 GMT, a weeknight) from
farm22-wrstat01 on LSF queue `normal`, with the wr binary built once from an
export of HEAD `fe45846d` (`customdb-f397b547`, log store the default) and
never replaced while runners were live. Work directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g3/`.

**Verdict: not passed, on scale and on the status criterion.** All five
soak criteria pass.

- Scale: peak LSF RUN was 4,110, not about 6,000. The ramp capped it, not
  the farm (see Scale).
- Status: the gate's 60s criterion applies to B,
  `wr status -i portal -z --limit 1`, the limit-1 history query the gate's
  rationale names (the reviewer reads it the same way). B fails it three
  times: 71.6s after the injected restart 1794270818, a client timeout at
  120s after the scheduled crash 1794277091 (the manager took 3m18s), and
  75.3s for B2, the repeat that followed it. The literal `wr status -l 1`
  (`-l` is `--cmdline`) and `wr status --limit 1` never exceeded 3s.

The status criterion's scope is for the owner to rule on. It can stay in
1g.3, so its fix must land before a rerun, or move to 2b with a regression
test. spec.md's Phase 1 Gates do not contain it; only phase1-gate.md 1g.3
does. Phase1-gate.md's 1g.3 is left unticked.

The owner ruled on 2026-11-18 that C, the literal `wr status -l 1`, gates
and B moves to item 2b.3. Under that ruling this run fails on scale alone:
its C peaked at 152ms.

Trimmed outputs are in [phase1-gate-1g3/](phase1-gate-1g3/).

### Method

- Source: `git archive fe45846d` into `src-fe45846d/`, `git init` there (as
  battery10 did, so `soak/config.sh` finds a checkout), plus this item's
  `developers/wrdev.sh` fix (below). `run.sh` built the soak's wr from it.
- Launcher [soak-go.sh](phase1-gate-1g3/soak-go.sh) `main`: battery10's
  `soak-go.sh` unchanged except `FIXTURE` unset, its own ports
  (53310-53313, pprof 6331), an isolated `HOME` (as runstate-gate
  nohold-g3b: `~/.wr_config.yml` names farm TLS certs, which `wrdev.sh`
  refuses for an isolated manager) and one extra watcher. So: `HOURS=3`,
  `RESTART_MIN=15`, battery10's crash/clean kind list, `USE_FUSE=1`
  (`FUSE_ON_AT=3 FUSE_OFF_AT=4 STALL_AFTER_MIN=7 STALL_SECS=180`),
  `RAMP0=600 RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"`,
  `RUNNER_FILELOG=1`, injectors `crashon.sh` (stall 90, burst 60, burst 45),
  `crashafter.sh 8 300`, `relbury.sh` r1 (400 jobs, kill D+500ms) and r2
  (400, D+2000ms), `stopstate.sh`, `rundep.sh`.
- [statustime.sh](phase1-gate-1g3/statustime.sh) watched `restarts.tsv`. At
  every stop line it recorded the store's unfolded wal backlog
  ([walbacklog.py](phase1-gate-1g3/walbacklog.py): `history.bolt`'s
  `meta/folded` position read without a lock, and the wal bytes after it).
  At every start line it recorded the backlog the new manager opened with,
  then ran one after another: B `wr status -i portal -z --limit 1`, A
  `wr status --limit 1`, C `wr status -l 1`, B2 = B again, then the backlog
  again. Output: [statustime.tsv](phase1-gate-1g3/statustime.tsv).
- From 00:17 [startprof.sh](phase1-gate-1g3/startprof.sh) took goroutine
  dumps every 4s for 40s after each start. For restart 1794270077 only, its
  first version also took a CPU profile. That clashed with prodsim's own
  post-start CPU profile, which then holds only an error message.
- Analysis: [analyse.sh](phase1-gate-1g3/analyse.sh) `main`, the soak
  README's steps with the export's tools and `dbstart` built from it, run
  after the manager's final stop.
- A 20-minute smoke soak (`soak-go.sh smoke`: ramp 100, restarts every 3
  minutes, one 40s stall, one relbury batch) checked the tooling first. Its
  first attempt found the `wrdev.sh` defect below.

### Soak criteria

```
nondurable source=wait inside=0 outside=0 runs=853677 outsidePct=0.0000
totals n/a
abandoned n=189 stalls=1 ok
doubles inside=0 outside=0 acknowledged=0
missing ran=838938 absent=0 excused=289
peakRUN=4110
killed portal_dedupe 20261110T002458.13448 outside reserved=1794270979
```

`relburycheck.py`: `problems: 0`. r1: kill at D+583ms, all 400 buried, every
release ran twice and every bury once. r2: kill at D+2356ms, 176 buries
acknowledged before the kill, all 400 buried, each ran once.

| criterion | result |
| --- | --- |
| `missing` `absent=0` | PASS (838,938 keys ran, 0 absent) |
| `relburycheck.py` `problems: 0` | PASS |
| `nondurable source=wait inside=0 outside=0` | PASS |
| `abandoned` line ends `ok`, `stalls` >= 1 | PASS (`n=189 stalls=1 ok`) |
| `doubles` `outside=0` | PASS |

The 189 abandoned reservations are all in one `"reservation wait"` line at
00:28:59, inside the 206s commit stall (`p50=9.93s max=10.0s`, the 10s
`reserveDurableWait`), from the manager the `crashon-stall` kill then took
down ([abandoned-lines.txt](phase1-gate-1g3/abandoned-lines.txt)).

`markers.py` found one double run, portal_dedupe
20261110T002458.13448. Its first run exited 0 at 1794270994, 15s after the
clean stop began at 1794270979. `soakgate.py` prints it as `killed`: that
run's runner logged `killed by user request` for that reservation, so it
does not count. `rundepcheck.py`: 36 OK, 1 OK-late-add, and 3 CHECK. Each
CHECK is a run killed by a clean stop (`B-noend-then-stop-clean`).

Load: 853,677 runs of 838,938 keys over 3h. There were 18 stops: 13
`kill -9` (6 injected: relbury-r1, crashon-stall in the stall, crashon-burst
twice, relbury-r2 and crashafter; 7 scheduled) and 5 clean.
`history.bolt` was 11.35GB at the end.

### Scale

Peak LSF RUN was 4,110 (1794275782; [lsf-peak.txt](phase1-gate-1g3/lsf-peak.txt)),
against "about 6,000". The ramp set that ceiling, not the farm. Its last
stage sets the `results_portal` target to 4,000 at t+60m
([ramp.log](phase1-gate-1g3/ramp.log)), and the soak's other jobs add
about 110. In the soak's `run/prodsim-1794267292/lsf.tsv`, every sample
near the peak has PEND about 0: RUN 4,104 with PEND 1, 4,108 with 6,
4,109 with 0 and 4,110 with 0 twice. LSF ran everything wr submitted. Only
11 of the 181 samples are above 4,000.

The large PEND values, up to 7,700 (1794271328, with RUN 1,533), are the
resubmission bursts after a restart, when RUN has fallen to a few hundred
or about 1,000. RUN took 9 and 15 minutes to climb back to 3,900 after the
restarts at 1794275007 and 1794274011. After 1794276069 it reached only
2,347 before the next stop. With a restart every 15 minutes, the plateau
is short. The run was already at night, so time of day is not the lever.

battery10 reached 6,104 with the same `RAMP` because it ran from
`FIXTURE` fix120k, whose 120,000 incomplete jobs (battery10 `RESULTS.md`
line 17) ran alongside the portal ramp. 109 of its 180 samples are above
4,300, though 6,104 itself is a single sample. runstate-gate nohold-g3b
reached 3,733.

The gate asks for both "battery10 shape" and about 6,000 runners "without
`FIXTURE`", which conflict: battery10's shape reaches about 6,000 only with
its fixture. A rerun must:

- raise `RAMP` to about 6,000, for example by appending `75:5000 90:6000`
  or compressing the stages;
- reach the plateau quickly, since RUN took 9 to 15 minutes to rebuild
  after each 15-minute restart;
- confirm the plateau in `lsf.tsv` as RUN about 6,000 with PEND about 0.

B (below) should be expected to get worse at 6,000 runners, because
history grows faster.

### `wr status` just after each restart

`-l` is `--cmdline`, so the gate's literal `wr status -l 1` asks for the
jobs whose command line is `1`. It reads no history (C below). The
incidental the gate cites (from 1f.11, owned by 2b) is about the history
paths `historyRead.offerArchived` and `addOverlayStatus`. The limit-1
history query that reaches them is `-i <substr> -z --limit 1` (B), the
same query as `TestReliable4StatusLimitPushdown` and prodsim's
`status_fofn_z`. Times in ms. Backlog is the unfolded wal at the new
manager's start.

| start (epoch) | kind | backlog | B | A | C | B2 |
| --- | --- | --- | --- | --- | --- | --- |
| 1794267298 | initial | 0 | 75 | 59 | 55 | 54 |
| 1794268202 | crash | 110KB | 12,905 | 79 | 47 | 680 |
| 1794268605 | relbury-r1 | 545KB | 20,035 | 94 | 44 | 783 |
| 1794269150 | clean (killed) | 14.1MB | 32,400 | 165 | 74 | 895 |
| 1794270077 | crash, onto fusestall | 1.6MB | 37,876 | 244 | 61 | 3,301 |
| 1794270704 | crashon-stall | 2.1MB | 2,586 | 284 | 50 | 2,426 |
| 1794270818 | crashon-burst | 7.5MB | **71,646** | 334 | 43 | 3,388 |
| 1794271072 | clean, direct | - | 36,708 | 365 | 56 | 2,551 |
| 1794271249 | relbury-r2 | - | 46,866 | 503 | 85 | 3,533 |
| 1794271416 | crashafter | - | 30,010 | 396 | 42 | 3,460 |
| 1794271611 | crashon-burst | - | 45,835 | 519 | 46 | 3,483 |
| 1794272017 | crash | - | 29,158 | 539 | 54 | 3,439 |
| 1794272980 | crash | - | 22,703 | 777 | 60 | 4,664 |
| 1794274011 | clean | - | 24,109 | 779 | 43 | 5,715 |
| 1794275007 | crash | 7.2MB | 36,760 | 946 | 52 | 9,974 |
| 1794276069 | clean (killed) | 1.4MB | 29,213 | 1,271 | 52 | 9,558 |
| 1794277091 | crash | 23.1MB | **120,121, rc=1** | 2,852 | 152 | **75,263** |

Rows marked `-` have their backlog in [statustime.tsv](phase1-gate-1g3/statustime.tsv).
At 1794277091 the client gave up at 120s ("rc=1"). The manager logged that
`getbr` as taking 3m18s, and a web seed's `getrs` of the same rep groups as
2m59s ([slow-requests.txt](phase1-gate-1g3/slow-requests.txt)).

- C, the literal command: max 152ms. PASS.
- A: max 2.85s. PASS.
- B: over 60s at injected restart 1794270818 (71.6s) and at scheduled
  crash 1794277091 (over 120s). B2, its repeat at 1794277091, took 75.3s,
  a third reading over 60s. FAIL.

#### Cause of B's times

It is not the overlay decode that the 1f.11 incidental describes. The
overlay was small at each start (at most 23MB of wal), the folder runs
throughout, and B's time does not follow the backlog (2.6s at 2.1MB, 32.4s
at 14.1MB, 71.6s at 7.5MB). Two things cause it:

1. **The query is O(history bytes).** For every key of every rep group
   matching `portal`, `historyRead.offerArchived` calls `folded`, a bbolt
   `Get` of that job's whole complete record from `jobsComplete`, to offer
   its facets. That is nearly every record in the 11GB `history.bolt`.
   bbolt's own `retrieveOldestCompleteJobsByRepGroup` (`db.go`) does the
   same per-key `completeJobBucket.Get`, so this shape predates the log
   store.
2. **Cold pages over NFS and competing readers make each byte dearer.**
   B, the first run after a restart, took 2.6s to 198s (the manager's time
   for the one the client gave up on). B2, run straight after B, took
   0.7-0.9s at the first three restarts, 2.4-5.7s in the middle of the
   run and 9.6-10.0s at 1794275007 and 1794276069. It grows with history
   size, and at 1794277091 it took 75.3s. A warm repeat can therefore
   also exceed 60s, so cold pages are not the whole cause. The cost is
   O(history bytes read). Cold `history.bolt` page faults and the
   concurrent whole-file backup copy raise the cost of each byte. The
   goroutine dumps in the slow windows
   ([goroutines-1794277091-g20.txt](phase1-gate-1g3/goroutines-1794277091-g20.txt))
   show the `getbr`, the web status seeds (`writeStatusCountSeed` ->
   `retrieveCompleteJobStatusByRepGroup`, which also `Get`s each record) and
   the folder's commit (`foldHistoryLookups` `Put`) all in
   `Page.FastCheck`, a page fault on the `history.bolt` mmap. The backup
   tick's `shipHistory` was meanwhile copying the whole `history.bolt` with
   `tx.WriteTo` (the Phase 1 caveat spec.md names). In the last window the
   folder fell behind while `getbr`, the web seeds and `shipHistory`
   competed: the backlog grew from 23MB to 46MB in 200s. The
   CPU profile of the 32.4s window (`poststart.1794269150`) used 57% of
   one core over 30s, so the manager was waiting on I/O, not computing.
   The one fast first run (2.6s, crashon-stall) is not explained. It opened
   through `fusestall`, which keeps its page cache across opens
   (`-coherent`), but so did the 37.9s and 71.6s starts.

The same history-path status calls went over 60s in the bbolt soaks too.
prodsim's own calls ([prodsim-status-calls-compare.txt](phase1-gate-1g3/prodsim-status-calls-compare.txt)):

| soak | `status_fofn_z` over 60s / max | `status_portal_summary` over 60s / max |
| --- | --- | --- |
| battery10 (bbolt, develop) | 8 / 120.6s | 4 / 120.0s |
| runstate-gate nohold-g3b (bbolt) | 1 / 100.9s | 5 / 97.4s |
| this soak (log store) | 1 / 99.2s | 3 / 120.1s |

The comparison is indicative, not like-for-like. It counts prodsim's own
status calls over each whole soak, not calls timed just after restarts, and
battery10 started on a 7GB fixture database while this soak's history grew
from empty to 11.35GB. It shows that the same history-path shape is slow on
bbolt, so this is not a log-store regression: it is the existing
O(history) shape of the history status paths, which the log store keeps in
Phase 1. It gives no bbolt timing just after a restart.

Proposed fix, for the owner to route: the 1f.11 incidental's fix (2b, D3)
should cover folded entries as well as overlay ones. Status and control
paths should read a key's facets (state, times, requirements) from a small
per-key index, or from a segment footer or index in Phase 2, not `Get` the
whole record. That makes a limit-1 `-z` query read O(rep groups + limit)
bytes, not O(history). Phase 2's incremental backup, which stops copying
`history.bolt` whole, removes the competing reads. Neither is a small fix,
so no product code changed here. A cheap reproducer: a store seeded with a
multi-GB folded history, page cache dropped (or `history.bolt` on NFS
reopened), then `wr status -i <substr> -z --limit 1` timed. After the fix
it should not grow with the history's size, as B and B2 did here.

### Other observations

- Three clean stops (1794269140, 1794275973, 1794278082) logged
  `aliveAtReturn=y`, so `wrdev.sh` killed the manager after `wr manager stop`
  reported success. This is not new: battery10 had 4 of 5 clean stops like
  that, and nohold-g3b 5 of 5.
- `dbstart` ran for 17 minutes. It folded the stopped store's backlog,
  logging `db writer writer=fold` lines with a mean hold of 3.7-5.2s per
  transaction on NFS.

### Tooling fix: prodsim with `WRDEV_PRODSIM_DBDIR` and the log store

The first smoke soak failed at the first manager start:
`mkdir .../.wr-prod_production/db: file exists`. `wrdev.sh prodsim`, which
`run.sh` always runs with `WRDEV_PRODSIM_DBDIR`, links `$PROD_RUN/db` to
`$WRDEV_PRODSIM_DBDIR/db` without creating the target. bbolt created the
file through the dangling link, but the log store's `createStoreDir`
(`os.MkdirAll`) cannot create a directory through one. `developers/wrdev.sh`
now removes an earlier run's store with `rm -rf`, makes the target an
empty directory when there is no fixture (wr makes a new store in an empty
directory), and its cleanup (without `WRDEV_PRODSIM_KEEP_DB`) removes the
store and the `$PROD_RUN/db_bk*` backup directories with `rm -rf`. It
still removes `$WRDEV_PROD_BKFILE*` with `rm -f`, which leaves a log-store
backup directory there (the soak does not set `WRDEV_PROD_BKFILE`). Other
scenarios also still `rm -f` what are now store directories; both are
recorded as an open entry in [incidentals.md](../phases/incidentals.md).
There is no test harness for
`wrdev.sh`. The failed smoke start was the red run, and the second smoke
soak and the main soak are the green runs. A dangling `managerdbfile`
symlink is a behaviour change from bbolt, recorded in
[incidentals.md](../phases/incidentals.md).

### Files

- [soakgate.txt](phase1-gate-1g3/soakgate.txt),
  [relburycheck.txt](phase1-gate-1g3/relburycheck.txt),
  [markers.txt](phase1-gate-1g3/markers.txt),
  [doubles.txt](phase1-gate-1g3/doubles.txt),
  [rundepcheck-summary.txt](phase1-gate-1g3/rundepcheck-summary.txt):
  analysis outputs.
- [restarts.tsv](phase1-gate-1g3/restarts.tsv),
  [hook.log](phase1-gate-1g3/hook.log),
  [stall.log](phase1-gate-1g3/stall.log),
  [ramp.log](phase1-gate-1g3/ramp.log),
  [lsf-peak.txt](phase1-gate-1g3/lsf-peak.txt): the soak's own records.
- [statustime.tsv](phase1-gate-1g3/statustime.tsv),
  [slow-requests.txt](phase1-gate-1g3/slow-requests.txt),
  [goroutines-1794277091-g20.txt](phase1-gate-1g3/goroutines-1794277091-g20.txt),
  [prodsim-status-calls-compare.txt](phase1-gate-1g3/prodsim-status-calls-compare.txt),
  [abandoned-lines.txt](phase1-gate-1g3/abandoned-lines.txt): status timing
  evidence.
- [soak-go.sh](phase1-gate-1g3/soak-go.sh),
  [analyse.sh](phase1-gate-1g3/analyse.sh),
  [statustime.sh](phase1-gate-1g3/statustime.sh),
  [walbacklog.py](phase1-gate-1g3/walbacklog.py),
  [startprof.sh](phase1-gate-1g3/startprof.sh): the launcher, analysis and
  watchers.
