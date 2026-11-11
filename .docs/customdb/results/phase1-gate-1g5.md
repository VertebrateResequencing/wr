# Phase 1 gate: daytime NFS targets (item 1g.5)

Gate (spec.md, Implementation Order, Phase 1, Gates; phase1-gate.md item
1g.5): measured apart from the crash soak, both runs wholly between 10:00
and 17:00 on one weekday with the work directory on NFS:

- a stall-free soak (`run.sh` with `SOAK_ROOT` on NFS, `USE_FUSE=0`,
  `HOURS=1`, `RESTART_MIN=120`, no injectors) where every
  `"reservation wait"` line has `p99` at most 50ms and `abandoned=0`, with
  no averaging across lines;
- `TestAddLatency1000` with `WR_ADDBENCH_DIR` under the NFS work directory
  and `WR_TEST_STORE=log`, with 1,000-job add p50 at most 200ms in each of 3
  runs.

The item also asks for the 1d.2 incidental on the unpaced checkpoint write
to be resolved, with reserve p99 measured while a `.ckpt` of 40MB or more
is written ([incidentals.md](../phases/incidentals.md)).

All runs were on Wednesday 2026-11-11 (GMT, which is UK local time),
from farm22-wrstat01 (8 cores, 91GB, shared with other users), on LSF
queue `normal`. The binary and test binary were built from HEAD `f7006373`
(`customdb-f397b547`, a clean tree with the log store the default). The
work directory was `/nfs/hgi/wr/sb10-bigdb/customdb/1g5/`. Trimmed outputs
and the scripts are in [phase1-gate-1g5/](phase1-gate-1g5/).

**Verdict: FAIL** on the soak criterion. 31 of the soak's 61
`"reservation wait"` lines have `p99` above 50ms, the worst 427.8ms. Every
line has `abandoned=0` and `notDurable=0`. The add criterion passes: p50
25.7ms, 28.8ms and 24.6ms. The checkpoint incidental was measured with
`.ckpt` files of 88MB to 133MB. The minute lines covering those
checkpoints, and no portal burst, have `p99` 26.7ms to 49.3ms, in line
with the minutes around them. The lines over 50ms follow the portal's add
bursts and a wal fsync latency that grows over the hour. They do not
follow the checkpoints. The incidental stays open for the owner to rule
on (see "Checkpoint incidental").

| criterion | run | result |
| --- | --- | --- |
| every `"reservation wait"` `p99` <= 50ms | stall-free soak, 10:00:04 to 11:01:58 | FAIL (31 of 61 lines over; worst 427.8ms) |
| every `"reservation wait"` `abandoned=0` | stall-free soak | PASS (0 in every line) |
| `TestAddLatency1000` p50 <= 200ms, 3 runs | 11:02:10 to 11:02:31 | PASS (25.7ms, 28.8ms, 24.6ms) |
| reserve p99 while a `.ckpt` >= 40MB is written | checkpoint soak, 11:06:40 to 11:53:30 | measured; see below |

## Stall-free soak

### Method

[soak-go.sh](phase1-gate-1g5/soak-go.sh) `gate` ran `run.sh` from a
`git archive f7006373` export (`src-f7006373/`, `git init` there, as 1g.3
did) with `SOAK_ROOT=/nfs/hgi/wr/sb10-bigdb/customdb/1g5/soak/run`,
`USE_FUSE=0`, `HOURS=1`, `RESTART_MIN=120`, `SCHED=lsf`, no `FIXTURE`, no
injectors, and `run.sh`'s defaults for everything else: `SIMMIN=6`,
`SCALE=1`, `RAMP0=600` and the default ramp (600, 1,200 at t+10m, 2,100 at
t+20m, 3,000 at t+35m). As in 1g.3, `HOME` was an isolated directory under
the work directory, with `.ssh` linked in. Ports were 53350 to 53353, pprof
6335. Beside the soak,
[ckptwatch.py](phase1-gate-1g5/ckptwatch.py) recorded the store
directory's inotify events. The manager runs on this host, so its creates,
closes and renames on NFS raise local events. A checkpoint's write window
is from the create of its `.ckpt.tmp` to its rename to `.ckpt`, and
covers the unpaced write and its fsync.
[waitlines.py](phase1-gate-1g5/waitlines.py) judges each
`"reservation wait"` line on its own and marks the lines whose minute
overlaps a checkpoint's write window.

A 12-minute smoke run of the same tooling (`soak-go.sh smoke`, 09:15 to
09:29, before the window) checked the watcher and the seeding script. Its
directory has been deleted.

The launcher started at 10:00:04 and the manager at 10:00:19. The soak's
final clean stop ended at 11:01:38 and `run.sh` exited at 11:01:58. There
were no restarts ([gate-restarts.tsv](phase1-gate-1g5/gate-restarts.tsv)).
Peak LSF RUN was 3,121. There were 280,956 runs of 278,190 keys, with 0
double runs and 0 overlaps
([gate-markers.txt](phase1-gate-1g5/gate-markers.txt)).

### Every `"reservation wait"` line

From [gate-waitlines.txt](phase1-gate-1g5/gate-waitlines.txt). Times are
GMT; each line covers the minute before it. "ckpt" names the checkpoint
(`0000000N.ckpt`) whose write window falls in that minute.

| time | n | p99 | max | notDurable | abandoned | verdict | ckpt |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 10:01:18 | 1758 | 427.819ms | 1.941517s | 0 | 0 | FAIL | |
| 10:02:18 | 1387 | 4.325ms | 8.365ms | 0 | 0 | pass | |
| 10:03:18 | 1405 | 4.129ms | 5.948ms | 0 | 0 | pass | |
| 10:04:18 | 1385 | 4.85ms | 29.272ms | 0 | 0 | pass | |
| 10:05:18 | 1267 | 5.112ms | 27.136ms | 0 | 0 | pass | |
| 10:06:18 | 1441 | 8.651ms | 32.964ms | 0 | 0 | pass | 2 |
| 10:07:18 | 1389 | 10.224ms | 49.178ms | 0 | 0 | pass | |
| 10:08:18 | 1375 | 42.992ms | 175.749ms | 0 | 0 | pass | |
| 10:09:18 | 1390 | 21.496ms | 42.487ms | 0 | 0 | pass | |
| 10:10:18 | 1354 | 7.995ms | 29.1ms | 0 | 0 | pass | |
| 10:11:18 | 2738 | 18.35ms | 36.606ms | 0 | 0 | pass | 3 |
| 10:12:18 | 2652 | 16.515ms | 35.361ms | 0 | 0 | pass | |
| 10:13:18 | 2689 | 31.982ms | 73.221ms | 0 | 0 | pass | |
| 10:14:18 | 2552 | 28.836ms | 42.089ms | 0 | 0 | pass | |
| 10:15:18 | 2664 | 34.603ms | 45.293ms | 0 | 0 | pass | |
| 10:16:18 | 2710 | 38.797ms | 86.038ms | 0 | 0 | pass | 4 |
| 10:17:18 | 2726 | 38.797ms | 44.22ms | 0 | 0 | pass | |
| 10:18:18 | 2663 | 45.089ms | 72.011ms | 0 | 0 | pass | |
| 10:19:18 | 2712 | 59.769ms | 80.551ms | 0 | 0 | FAIL | |
| 10:20:18 | 2627 | 77.595ms | 102.511ms | 0 | 0 | FAIL | |
| 10:21:18 | 4843 | 45.089ms | 153.609ms | 0 | 0 | pass | 5 |
| 10:22:18 | 4637 | 51.38ms | 68.235ms | 0 | 0 | FAIL | |
| 10:23:18 | 4693 | 42.992ms | 188.674ms | 0 | 0 | pass | |
| 10:24:18 | 4711 | 38.797ms | 81.208ms | 0 | 0 | pass | |
| 10:25:18 | 4716 | 47.186ms | 72.969ms | 0 | 0 | pass | |
| 10:26:18 | 4679 | 42.992ms | 136.668ms | 0 | 0 | pass | 6 |
| 10:27:18 | 4643 | 102.76ms | 241.154ms | 0 | 0 | FAIL | |
| 10:28:18 | 4709 | 66.06ms | 129.853ms | 0 | 0 | FAIL | |
| 10:29:18 | 4703 | 73.4ms | 101.606ms | 0 | 0 | FAIL | |
| 10:30:18 | 4691 | 49.283ms | 81.349ms | 0 | 0 | pass | |
| 10:31:18 | 4633 | 59.769ms | 176.678ms | 0 | 0 | FAIL | 7 |
| 10:32:18 | 4637 | 34.603ms | 62.007ms | 0 | 0 | pass | |
| 10:33:18 | 4650 | 33.03ms | 156.76ms | 0 | 0 | pass | |
| 10:34:18 | 4675 | 34.603ms | 60.814ms | 0 | 0 | pass | |
| 10:35:18 | 4648 | 34.603ms | 125.058ms | 0 | 0 | pass | |
| 10:36:18 | 6749 | 47.186ms | 151.57ms | 0 | 0 | pass | 8 |
| 10:37:18 | 6661 | 47.186ms | 98.474ms | 0 | 0 | pass | |
| 10:38:18 | 6708 | 59.769ms | 123.237ms | 0 | 0 | FAIL | |
| 10:39:18 | 6528 | 66.06ms | 133.968ms | 0 | 0 | FAIL | |
| 10:40:18 | 6184 | 61.866ms | 125.11ms | 0 | 0 | FAIL | |
| 10:41:18 | 6823 | 205.521ms | 326.359ms | 0 | 0 | FAIL | 9 |
| 10:42:18 | 6638 | 77.595ms | 241.01ms | 0 | 0 | FAIL | |
| 10:43:18 | 6609 | 69.206ms | 129.897ms | 0 | 0 | FAIL | |
| 10:44:18 | 6560 | 55.575ms | 134.28ms | 0 | 0 | FAIL | |
| 10:45:18 | 6615 | 51.38ms | 93.276ms | 0 | 0 | FAIL | |
| 10:46:18 | 6702 | 222.298ms | 312.92ms | 0 | 0 | FAIL | a |
| 10:47:18 | 6639 | 111.149ms | 337.6ms | 0 | 0 | FAIL | |
| 10:48:18 | 6678 | 106.955ms | 153.44ms | 0 | 0 | FAIL | |
| 10:49:18 | 6651 | 127.926ms | 187.515ms | 0 | 0 | FAIL | |
| 10:50:18 | 6600 | 106.955ms | 148.878ms | 0 | 0 | FAIL | |
| 10:51:18 | 6525 | 276.824ms | 319.342ms | 0 | 0 | FAIL | b |
| 10:52:18 | 6710 | 205.521ms | 368.209ms | 0 | 0 | FAIL | |
| 10:53:18 | 6568 | 73.4ms | 127.624ms | 0 | 0 | FAIL | |
| 10:54:18 | 6701 | 77.595ms | 128.091ms | 0 | 0 | FAIL | |
| 10:55:18 | 6606 | 73.4ms | 132.164ms | 0 | 0 | FAIL | |
| 10:56:18 | 6626 | 171.966ms | 250.431ms | 0 | 0 | FAIL | c |
| 10:57:18 | 6476 | 63.963ms | 281.203ms | 0 | 0 | FAIL | c |
| 10:58:18 | 6651 | 51.38ms | 121.711ms | 0 | 0 | FAIL | |
| 10:59:18 | 6694 | 45.089ms | 214.395ms | 0 | 0 | pass | |
| 11:00:18 | 6610 | 53.477ms | 281.178ms | 0 | 0 | FAIL | |
| 11:00:58 | 4292 | 59.769ms | 160.683ms | 0 | 0 | FAIL | d |

Totals: 280,956 reservations, `abandoned` 0 and `notDurable` 0 in every
line. p50 stayed between 0.9ms and 3.3ms throughout. The failures are in
the tail.

### Diagnosis

Three things push the tail over 50ms. None of them is a stall, and no
reservation was abandoned (the wait bound is 10s).

1. **The first minute.** One wal fsync took 1.94s (the `"db writer"
   writer=log` line at 10:01:18 has `maxSync=1.941394s`;
   [gate-stats-lines.txt](phase1-gate-1g5/gate-stats-lines.txt)), as 600
   runners started and the first portal burst and fofn adds wrote 10MB of
   wal. Its fold transaction held 695ms. No checkpoint ran in that minute.
   It is a single NFS fsync of nearly 2s on a near-empty store, which no
   wr-side change in the write path would avoid.
2. **Growth over the hour.** In the lines without a portal burst, the
   median p99 rose with the wal's `maxSync` and the folder's transaction
   hold time, as the reservation rate and `history.bolt` grew:

   | minutes | median p99 | median wal `maxSync` | median fold `meanHold` | median n |
   | --- | --- | --- | --- | --- |
   | 10:02-10:10 | 6.6ms | 23.6ms | 89ms | 1,386 |
   | 10:12-10:20 | 36.7ms | 39.2ms | 248ms | 2,663 |
   | 10:22-10:35 | 45.1ms | 75.9ms | 513ms | 4,683 |
   | 10:37-10:45 | 60.8ms | 102.9ms | 940ms | 6,612 |
   | 10:47-11:00 | 75.5ms | 152.0ms | 1,018ms | 6,645 |

   Over those 48 lines, p99 correlates with the fold's mean hold (r=0.82),
   the wal's max sync (r=0.71) and n (r=0.66). The folder's
   `history.bolt` commits (bbolt, fdatasync on a growing file) and the
   wal's syncs go to the same NFS client. The likely cause is that the
   folder's commits lengthen the wal's syncs, as the backup copy did for
   bbolt's committer in reliable4. That is a correlation from one run, not a
   load-matched A/B, so it is a hypothesis for the bugfix, not a finding.
3. **Portal bursts.** Every 5 minutes prodsim's portal adds a burst of up
   to 33,093 x 2 jobs with 10KB commands: 35-50MB of wal in one minute
   against 3-4MB otherwise. Those minutes have the highest p99s (205.5ms,
   222.3ms, 276.8ms, 172.0ms) and wal `maxSync` of 245ms to 310ms.

In this soak checkpoints 2 to b each started within 8s of a portal burst,
c 42s after one, and d in the soak's final partial minute
([gate-portal-bursts.tsv](phase1-gate-1g5/gate-portal-bursts.tsv),
[gate-ckptwatch.tsv](phase1-gate-1g5/gate-ckptwatch.tsv)). The store
opened when prodsim started, and both repeat every 5 minutes, so every
checkpoint shares its minute line with a burst. The soak cannot separate a
checkpoint's effect from a burst's. The checkpoint soak below separates
them.

The soak's own live set grew to production size. Its last three
checkpoints were 42.9MB, 53.3MB and 57.0MB, written in 4.3s to 5.6s.

## TestAddLatency1000

[addbench.sh](phase1-gate-1g5/addbench.sh) ran the `jobqueue` test binary
built at `f7006373` (`go test -tags netgo -c`), once per run, with every
`OS_*` variable unset, a fresh scratch `HOME`, `WR_TEST_STORE=log` and a
fresh `WR_ADDBENCH_DIR=/nfs/hgi/wr/sb10-bigdb/customdb/1g5/addbench/runN`:

```bash
HOME=$(mktemp -d -p <scratchpad>) WR_ADDBENCH_DIR=$dir WR_TEST_STORE=log \
  jobqueue.test -test.run '^TestAddLatency1000$' -test.v -test.count 1 -test.timeout 90m
```

The runs went one after another, starting at 11:02:10, after the soak's
`run.sh` had exited and LSF reported none of its jobs. A leftover
background loop of the soak's `wrdev.sh` (`prodsim_orphan_snapshot`, which
runs `bjobs` at +30s, +60s, +120s and +300s after the final stop) was
still sleeping. That is negligible load.

| run | start (GMT) | end (GMT) | n | p50 | p99 | result |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 11:02:10.9 | 11:02:17.4 | 100 | 25.690ms | 90.178ms | PASS |
| 2 | 11:02:17.5 | 11:02:24.2 | 100 | 28.836ms | 90.178ms | PASS |
| 3 | 11:02:24.2 | 11:02:30.9 | 100 | 24.642ms | 73.400ms | PASS |

Outputs: [addbench-run1.out](phase1-gate-1g5/addbench-run1.out),
[addbench-run2.out](phase1-gate-1g5/addbench-run2.out),
[addbench-run3.out](phase1-gate-1g5/addbench-run3.out).

## Checkpoint incidental

The 1d.2 incidental asks whether a checkpoint's unpaced `.ckpt` write and
its single fsync on NFS delay the wal's syncs, and so reservations, and
says to pace the write as `backupCopyWriter` does if a `"reservation wait"`
line covering a checkpoint has `p99` above 50ms. It asks for the
measurement with a production-sized live set, a `.ckpt` of 40MB or more.

### Method

The smallest faithful measurement is the soak itself, with production's
manager, NFS store and runners, plus a large live set that never runs, and
with the checkpoints moved off the portal bursts. `soak-go.sh ckpt`:

- the same `run.sh` shape as the gate soak, for 45 minutes
  (`HOURS=0.75`), in its own `SOAK_ROOT`
  (`/nfs/hgi/wr/sb10-bigdb/customdb/1g5/ckpt/run`) on ports 53360 to
  53363;
- `EXTRA_ARGS="-portal-gap 7m"`, so portal bursts come every 7 minutes
  and drift against the 5-minute checkpoints;
- [seedlive.sh](phase1-gate-1g5/seedlive.sh): 4 minutes after the manager
  started it added 250,000 jobs in limit group `ckptlive:0`
  (ready-but-blocked as in `wrdev.sh`'s gates, so they never run and no LSF
  job is made), in adds of 10,000, from 11:10:55 to 11:11:45
  ([ckpt-seedlive.log](phase1-gate-1g5/ckpt-seedlive.log)). Each command
  is about 775 bytes, 512 of them random hex from a fixed seed, so zstd
  cannot shrink the checkpoint to test scale. The first checkpoint after
  it, at 11:11:54, already held the whole seeded set;
- `ckptwatch.py` and `waitlines.py` as above.

It ran from 11:06:40 (launch; manager start 11:06:54) to 11:53:30 with no
restarts. Peak LSF RUN was 3,109, with 182,493 runs and 0 double runs.

### Result

From [ckpt-waitlines.txt](phase1-gate-1g5/ckpt-waitlines.txt) and
[ckpt-ckptwatch.tsv](phase1-gate-1g5/ckpt-ckptwatch.tsv). "Write" is the
`.ckpt.tmp` create to the rename. "Burst" says whether a portal burst
([ckpt-portal-bursts.tsv](phase1-gate-1g5/ckpt-portal-bursts.tsv)) fell in
the same minute line. The neighbours are the p99s of the lines just before
and after.

| ckpt | size | write | covering line | p99 | max | wal `maxSync` | burst | neighbours' p99 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2 | 88.3MB | 2.4s | 11:12:54 | 26.7ms | 53.6ms | 35.1ms | no | 59.8ms (seeding), 45.1ms |
| 3 | 91.4MB | 3.1s | 11:17:54 | 27.8ms | 38.8ms | 29.5ms | no | 15.5ms, 30.9ms |
| 4 | 97.3MB | 3.7s | 11:22:54 | 36.7ms | 44.0ms | 44.2ms | no | 38.8ms, 36.7ms |
| 5 | 94.1MB | 3.7s | 11:27:54 | 40.9ms | 47.7ms | 71.5ms | no | 43.0ms, 127.9ms (burst) |
| 6 | 104.9MB | 4.6s | 11:32:54 | 43.0ms | 68.9ms | 59.9ms | no | 49.3ms, 49.3ms |
| 7 | 115.2MB | 5.2s | 11:37:54 | 38.8ms | 128.5ms | 109.6ms | no | 40.9ms, 36.7ms |
| 8 | 112.5MB | 5.2s | 11:42:54 | 146.8ms | 233.9ms | 195.2ms | yes | 38.8ms, 45.1ms |
| 9 | 117.8MB | 6.3s | 11:47:54 | 49.3ms | 116.8ms | 134.8ms | no | 43.0ms, 45.1ms |
| a | 132.7MB | 7.5s | 11:52:31 | 55.6ms | 112.4ms | 97.6ms | no | 45.1ms (and the final stop) |

The other lines over 50ms all hold a portal burst and no checkpoint:
11:28:54 (127.9ms), 11:35:54 (123.7ms) and 11:49:54 (180.4ms), plus 11:11:54
(59.8ms), the seeding minute, and 11:50:54 (59.8ms), the minute after the
largest burst. In the bursts' minutes the wal's `maxSync` was 134ms to 258ms,
against 29ms to 135ms in the checkpoint-only minutes
([ckpt-stats-lines.txt](phase1-gate-1g5/ckpt-stats-lines.txt)).

### Reading

- A minute line holds about 4,700 to 6,700 reservations, and a 3s to 7s
  write window holds about 5% to 12% of them. Line 9's p99 of 49.3ms
  therefore means at most 1% of that minute's reservations, under 10% of
  those inside its 6.3s window, took over 49.3ms, and none over 116.8ms.
  The unpaced write of an 88MB to 118MB checkpoint did not stall
  reservations. Lines 2 to 7 and 9, which hold a checkpoint and no burst,
  have p99 26.7ms to 49.3ms, within a few ms of their neighbours.
- Two covering lines are over 50ms. Line 8's minute also holds a portal
  burst, and the three larger bursts alone give 124ms to 180ms. Line a's
  55.6ms is the partial line written at the final clean stop, whose minute
  is not like the others; the line before it was 45.1ms.
- So the measurement does not show the checkpoint write to be a problem at
  these sizes, but the incidental's own rule ("if any is above 50ms, pace
  the checkpoint's write") is met literally, by lines 8 and a here and by
  the gate soak's covering lines, which all hold a burst too. The rule
  cannot be judged at the 1-minute resolution of the stats lines when a
  burst shares the minute. The owner should rule whether the evidence
  above closes it or whether pacing should land anyway (a cheap change:
  the `backupCopyWriter` pattern of forcing writeback every `syncEvery`
  bytes). Pacing would not fix the gate failure, which is in minutes
  without a checkpoint.

## Proposed routing

The soak criterion is a defect to route through `bugfix`, not a reason to
relax the gate:

1. Reservation tail latency under daytime load: p99 grows past 50ms as the
   reservation rate, wal size per minute and `history.bolt` grow. The
   first thing to test is whether the folder's `history.bolt` commits
   lengthen the wal's fsyncs on the same NFS client. A load-matched A/B
   with the folder slowed or paused (on a copy of this soak's shape) would
   settle it before any fix. Phase 2 replaces `history.bolt` with
   segments, which may change this, so the owner may prefer to route it to
   2b with a regression measurement.
2. Burst adds: a 35-50MB wal burst in one minute raises every reservation
   in that minute to 120-280ms p99. Reservations share the one wal writer
   with adds, so an add burst's large batches delay the reserve records'
   group commit. Whether reserve records should be able to commit ahead of
   a large add batch is a design question for the owner.
3. The first minute's 1.94s fsync is NFS behaviour at start-up and is
   probably not fixable in wr. It alone fails the gate as written, so the
   owner may want to rule whether one line at start-up should count.

## Files

- [gate-waitlines.txt](phase1-gate-1g5/gate-waitlines.txt),
  [gate-stats-lines.txt](phase1-gate-1g5/gate-stats-lines.txt),
  [gate-ckptwatch.tsv](phase1-gate-1g5/gate-ckptwatch.tsv),
  [gate-portal-bursts.tsv](phase1-gate-1g5/gate-portal-bursts.tsv),
  [gate-restarts.tsv](phase1-gate-1g5/gate-restarts.tsv),
  [gate-ramp.log](phase1-gate-1g5/gate-ramp.log),
  [gate-lsf.tsv](phase1-gate-1g5/gate-lsf.tsv),
  [gate-report.txt](phase1-gate-1g5/gate-report.txt),
  [gate-markers.txt](phase1-gate-1g5/gate-markers.txt): the stall-free soak.
- [addbench-run1.out](phase1-gate-1g5/addbench-run1.out),
  [addbench-run2.out](phase1-gate-1g5/addbench-run2.out),
  [addbench-run3.out](phase1-gate-1g5/addbench-run3.out): the add runs.
- [ckpt-waitlines.txt](phase1-gate-1g5/ckpt-waitlines.txt),
  [ckpt-stats-lines.txt](phase1-gate-1g5/ckpt-stats-lines.txt),
  [ckpt-ckptwatch.tsv](phase1-gate-1g5/ckpt-ckptwatch.tsv),
  [ckpt-portal-bursts.tsv](phase1-gate-1g5/ckpt-portal-bursts.tsv),
  [ckpt-seedlive.log](phase1-gate-1g5/ckpt-seedlive.log),
  [ckpt-restarts.tsv](phase1-gate-1g5/ckpt-restarts.tsv),
  [ckpt-ramp.log](phase1-gate-1g5/ckpt-ramp.log): the checkpoint soak.
- [soak-go.sh](phase1-gate-1g5/soak-go.sh),
  [ckptwatch.py](phase1-gate-1g5/ckptwatch.py),
  [seedlive.sh](phase1-gate-1g5/seedlive.sh),
  [waitlines.py](phase1-gate-1g5/waitlines.py),
  [addbench.sh](phase1-gate-1g5/addbench.sh): the launcher, watcher,
  seeder, analysis and add runner.

The soaks' output directories (`soak/run/prodsim-1794391211/`,
`ckpt/run/prodsim-1794395208/`, with the manager logs) stay under the work
directory. The soaks' stores were removed by `wrdev.sh`'s cleanup, and the
add runs' stores and the smoke run were deleted.
