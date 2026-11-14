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

## Fix

The soak failure was routed through `bugfix`
([.docs/bugfixes/261111-reserve-tail-nfs-4766eb70.md](../../bugfixes/261111-reserve-tail-nfs-4766eb70.md)).
This section is the diagnosis, the fix and the soaks that measured them,
on 2026-11-11 and 2026-11-12 (GMT) from farm22-wrstat01, work directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/`. Raw outputs are under
[phase1-gate-1g5/](phase1-gate-1g5/), named `fix-*`.

### Method

A reservation waits for its state record's wal batch to be durable: at
worst the sync in flight when it is appended, then its own. So the line's
`p99` follows the tail of the wal's syncs, and the question was what makes
some of them slow.

A diagnostic build of `f1bcc1d7`
([fix-diag.patch](phase1-gate-1g5/fix-diag.patch), never committed) wrote,
to local disk, one line per wal batch (start, write time, sync time,
bytes), per fold transaction (start, end, items, bbolt's page writes and
allocations, `history.bolt`'s size), per `history.bolt` copy to the backup
(start, end, size) and per reservation (end, wait). Of its knobs, one was
used, for A1: `WR_DIAG_NOHISTBACKUP=1` copies `history.bolt` to the backup
on the first tick and never again.
[fix-traceana.py](phase1-gate-1g5/fix-traceana.py) summarises a trace by
minute, and [fix-linesplit.py](phase1-gate-1g5/fix-linesplit.py) marks each
`"reservation wait"` line whose minute, or the 30s before it, holds a portal
burst's start (prodsim's `events.tsv`).

Each arm was the gate soak's shape ([fix-soak.sh](phase1-gate-1g5/fix-soak.sh):
`run.sh`, `SOAK_ROOT` on NFS, `USE_FUSE=0`, `RESTART_MIN=120`, no
injectors, the default ramp) for 45 minutes (`HOURS=0.75`, so 10 minutes at
3,000 runners), one after another on ports 53380 to 53383:

| arm | build (all diagnostic) | prodsim start | end |
| --- | --- | --- | --- |
| B1 | f1bcc1d7 | 12:04:04 | 12:50:42 |
| A1 | f1bcc1d7, `WR_DIAG_NOHISTBACKUP=1` | 12:54:18 | 13:40:50 |
| F2 | backup fix + fold pacing between chunks | 13:41:10 | 14:27:30 |
| F3 | F2 + chunks of 1,024 records | 14:31:02 | 15:17:39 |
| F4 | backup fix + chunks of 256, no fold pacing | 15:17:57 | 16:04:35 |
| F6 | backup fix + a fold's rglookups in transactions of at most 256, no fold pacing | 16:04:53 | 16:51:24 |

"Backup fix" is the history.bolt re-copy deferral and the 1MiB copy pace
(see "The fix"). A second normal arm (B2) was started at 14:28 and stopped
at 14:30 to make room for F3; F5 was planned and dropped.

### What held the wal's syncs up

Per arm, from `fix-<arm>-lines.txt` (every line), `fix-<arm>-trace.tsv`
(per minute) and `fix-<arm>-waitlines.txt`. "Burst" lines are those whose
minute or the 30s before it holds a portal burst's start; the rest are
"other". The 3,000-runner half is the lines with `n` of 4,000 or more.

| arm | lines | over 50ms | other lines, n >= 4,000: p99 | burst lines, n >= 4,000: p99 | wal syncs over 50ms |
| --- | --- | --- | --- | --- | --- |
| B1 | 46 | 4 (all burst) | 33.0-49.3ms, median 38.8ms (20 lines) | 38.8-163.6ms (5 lines, 4 over) | 267 of 451,152 |
| A1 | 45 | 4 (3 burst, 1 NFS stall) | 6.4-25.7ms, median 13.4ms (16 lines) | 14.4-119.5ms (9 lines, 3 over) | 143 of 465,478 |
| F2 | 46 | 2 (both burst) | 5.6-29.9ms, median 18.4ms (17 lines) | 14.4-86.0ms (9 lines, 2 over) | 193 of 480,744 |
| F3 | 46 | 3 (one burst's fold) | 10.2-61.9ms, median 17.3ms (21 lines, 2 over) | 20.4-69.2ms (5 lines, 1 over) | 306 of 467,462 |
| F4 | 46 | 2 (both burst) | 6.2-34.6ms, median 11.8ms (15 lines) | 6.4-66.1ms (8 lines, 2 over) | 224 of 480,264 |
| F6 | 46 | 1 (burst) | 2.3-34.6ms, median 6.4ms (21 lines) | 32.0-53.5ms (5 lines, 1 over) | 118 of 477,411 |

1. **The backup's `history.bolt` copy** made the steady growth. In B1 the
   copy ran every tick: `history.bolt` reached 1.3GB at 12:37 and 2.3GB at
   12:49 (it grows 17-35MB a minute in this soak), and the manager wrote
   about 24MB/s on average. A1 differs from B1 only in not copying it again
   after the first tick, and its other lines at 4,600 to 6,600
   reservations a minute had p99 6.4-25.7ms against B1's 33.0-49.3ms, with
   the same load and fold activity (fold duty 0.30 of the run in both).
   B1's other lines were rising towards 50ms as `history.bolt` grew; the
   1h gate soak's last 20 minutes, with more history, had them at
   51-128ms. The same copy shows on local disk: the red test's p99 is
   64-75ms with backups and 4.3-5.4ms without (4 interleaved runs each).
2. **The folder's chunks after a portal burst** made the burst lines fail.
   A burst adds 13,000 to 33,000 jobs twice over, in adds of about 1,000
   jobs, and the folder then folds their rglookup records in chunks of
   4,096 records back to back for 30-40s. Each chunk's `history.bolt`
   transaction wrote 10-15MB in about 2,600 runs of pages and held 0.6-1.6s,
   and every wal sync over 50ms in those minutes fell inside one (A1: 18 of
   18 at 13:19, 24 of 24 at 13:24; over whole runs, 247 of 267 in B1 and
   136 of 143 in A1, against a fold duty of 0.30). The steady folds, one a
   second of about 200 completions, wrote 6-12MB in about 750 runs and held
   no sync over 50ms. An NFS micro-benchmark on the same mount
   ([fix-iotest.out](phase1-gate-1g5/fix-iotest.out),
   [fix-iotest.go.txt](phase1-gate-1g5/fix-iotest.go.txt), 12:51 to 12:54)
   shows the mechanism in isolation: a 400-byte append and fdatasync takes
   p99 1.3ms alone, and up to 180ms (its max) beside 8MB commits that each
   take 152ms, at the same 4MB/s.
3. **NFS stalls** are outside wr. In A1, from 13:00:11 to 13:00:37 every
   wal sync and every fold commit took 0.5-2.2s together, with no burst,
   copy or checkpoint in flight; that minute's line had p99 102.8ms. The
   1h gate soak's first-minute 1.94s fsync is the same kind of event.

The checkpoint is not among them, as 1g.5's checkpoint soak found: in these
arms every checkpoint again fell in a burst minute, and A1's and F2's
other lines include no slow minute at a checkpoint.

### Narrowing the burst cause

- **F2** (backup fix, and the fold waiting between chunks as long as each
  took) kept the other lines low with `history.bolt` copied 8 times while
  under 64MiB and once more at 14:02 (571MB in 1.7s, its line 8.2ms). Two
  burst lines still failed (53.5ms, 86.0ms): each paced chunk of 4,096
  rglookup records still wrote about 13MB in some 2,600 runs of pages, and
  the syncs in flight during its commit took 100-150ms.
- **F3** (F2 with chunks of 1,024 records) did not shrink the commits much:
  a chunk never splits a block, and a 1,000-job add is one block of about
  2,000 records, so a chunk became one add's block, about 1,000 rglookups,
  7-9MB in 1,500-2,000 runs, with syncs of 60-115ms in flight. Halving the
  pace of a now longer fold spread the 33,093 x 2 burst at 15:11 over three
  minute lines (69.2ms, 61.9ms, 53.5ms).
- **F4** (chunks of 256, so still a block a chunk, and no pacing) failed two
  burst lines (55.6ms, 66.1ms). Against F3, pacing had kept the 4,600-a-minute
  burst lines lower (17.6-37.9ms against 47.2-54.8ms) but cannot help when
  each commit alone holds a sync past 50ms.
- **F6** puts a fold's rglookups in transactions of at most 256 (about 500
  runs of pages, 2MB, committed in about 40ms) ahead of the transaction that
  commits the rest of the chunk and moves the folded position, without
  pacing. It had the fewest slow syncs of any arm (118) and the lowest other
  lines (median 6.4ms), and only the last, largest burst line failed
  (53.5ms at 16:45:59). In that minute 276 fold transactions ran, 231 of
  them ahead transactions nearly back to back, and the 96 reservations over
  50ms overlapped an ahead transaction (56) or a chunk's last transaction
  (39), whose commit, holding the chunk's completions, took up to 1s.

So small transactions helped most. A first final fix kept F6's split and
added a wait after each of a fold's transactions; review found it slowed
the `*UnderReserveLoad` benchmarks and a backlog's fold (see "Speed"
below), so it was replaced by a split bounded by the
pages a transaction dirties, without pacing.

### The fix

In the working tree on top of `f1bcc1d7` (not committed); the bugfix
checklist has the files, tests and mutants.

1. Backup (cause 1). A tick copies `history.bolt` again only when the
   backup's copy is at most 64MiB (`historyRecopyFloor`) or the wal bytes
   shipped since that copy are at least its size (`historyDue`).
   Meanwhile the backup keeps its checkpoint and the wal from its copy's
   folded position, which restore folds again; a newer checkpoint kept
   back for that reason is not warned about. A store's first tick, and a
   backup started again, still copy it. So the history bytes a backup ships
   are at most the wal bytes it ships.
2. Backup copy pace. `backupCopySyncInterval` 8MiB to 1MiB, so any copy
   that does run (the first after a restart, a due re-copy, a checkpoint, a
   large wal part) holds a concurrent sync behind at most about 20ms of
   writeback on NFS. On local disk an 842MB copy took 1.86s against 1.82s.
3. Folder (cause 2). A fold puts its rglookups 64 at a time, then records its
   completions one at a time (at least 16 a transaction), each in key order, and
   once its transaction's estimated size reaches its bound commits it, the first
   such transaction also making the chunk's rglookup drops, and makes the rest
   in further transactions; the last commits the chunk's samples, rep groups
   ever added and folded position. The bound (`foldTxLimit`) is `foldTxBytes`
   (2MB) while the fold keeps up, that times lag/60s once its lag passes 60s
   (the oldest record it is folding may have waited that long since it was
   durable; a 5s step, soaked in G5, let portal bursts' folds grow to 15-20MB
   transactions and fail 3 lines), so a fold that falls behind makes bigger
   transactions, cheaper per record, as a backlog's unsplit 4,096-record chunks
   were; and never less than the freelist every commit writes. The estimate
   (`foldTxSize`) is a page for each node the transaction has changed (bbolt's
   node count) plus the bytes of the keys and values it has put, scaled by the
   bytes of pages the last fold transaction wrote (bbolt's `PageAlloc`, less its
   freelist write) per byte estimated; the scale is 1 until the first fold
   transaction after an open commits, and has no clamp or smoothing. Those puts,
   drops and completions are idempotent (`completeSeq` makes a completion
   recorded again a no-op) and only add to or take from what the overlay holds,
   so a crash before the last leaves every history query's answer as it was but
   for learned-requirement samples, which go with their completions; recovery
   clears a live job's std for a complete record `history.bolt` already holds
   past the folded position, as for one before it (`logState.applyComplete`,
   review round 2's F1). There is no wait between transactions, and
   `foldChunkRecords` stays 4,096. The folder's `"db writer"` line logs its
   longest lag in the interval as `maxLag` (an upper bound, see "Round 4").
   History: the bound first covered rglookups alone (G1); completions were
   added after G1 failed on chunks of completions; the node count alone (G2,
   G3) was replaced by this estimate after review round 2 (F2); the lag
   growth, leaving the freelist out of the scale, the
   no-smaller-than-the-freelist floor and the 16-completion floor came after
   review round 3 (B1, B2). See "Round 3" and "Round 4" below.
4. Checkpoint (round 5, the 1d.2 incidental): a checkpoint's `.ckpt.tmp`
   write forces writeback every `backupCopySyncBytes` (1MiB) through the
   backup copy's `backupCopyWriter`, keeping the written pages cached, as
   folds and queries read its records. See "Round 5" below.

The spec's E4 tick steps 2 and 4, the paced-copy paragraph, the Folder
section and the crash soak's caveat say so.

### Red and green

`TestReserveWaitDuringBackup` (`jobqueue/reserve_backup_tail_repro_test.go`,
tag `reliability_repro`) is the in-process reproduction of cause 1: a
production log store whose `history.bolt` holds 300,000 completed jobs
(about 840MB), backup ticks every 10s, and 110 archives and 110 reservation
records a second for 40s. On local disk, at `f1bcc1d7`: p99 73.9ms, FAIL
([fix-red-1.txt](phase1-gate-1g5/fix-red-1.txt)); without backups the
harness it came from gave 4.3-5.4ms in 4 interleaved runs, with backups
64-75ms ([fix-local-backup-ab.txt](phase1-gate-1g5/fix-local-backup-ab.txt)).
With the backup fix: p99 10.5ms, PASS
([fix-green-1.txt](phase1-gate-1g5/fix-green-1.txt)); with the first final
fix before its wait was capped, 7.5ms; with the rglookup-only bound
(2026-11-12 06:10), 6.3ms; with the G3 code (14:10), 17.0ms; with the
final code (20:19), 6.3ms. On NFS
in the evening (18:20, `WR_REPRO_DIR` under the work directory)
`f1bcc1d7` gave p99 46.7ms, so the test is not red there out of
hours; the soak, in the day, is the NFS evidence. Cause 2 does not
reproduce on local disk (folding on or off gave the same p99 there): its
evidence is the soaks above, and its unit tests check the mechanism (a
fold's transactions bounded by the pages they dirty).

### Gates

On the round 3 code (superseded: the final code's gates are under "Round 5"
below, round 4's under "Round 4"), from farm22-wrstat01 on 2026-11-12, each with
a scratch `HOME` and every `OS_` variable unset: `make lint` 0 issues
(20:16 to 20:17); `TestReserveWaitDuringBackup` p99 6.3ms, PASS (20:17 to
20:19); `make test` (20:19 to 20:25), `WR_TEST_STORE=all make test` (20:25
to 20:31) and `make race` (20:31 to 20:39) passed, 1,292 tests each. The
G3 code passed the same after the gate soaks (14:07 to 14:30,
`TestReserveWaitDuringBackup` p99 17.0ms). The rglookup-only code passed
the same at 06:07 to 06:32 (`TestReserveWaitDuringBackup` p99 6.3ms), and
the first final fix on
2026-11-11 (19:30 to 19:55); before that fix's wait was capped, one `make
test` (17:11 to 17:18) failed only `TestWriterSpendsAnExistingNumber` in
`jobqueue/wal`, a package the fix does not touch; it passed 200 times in a
row after, and is recorded as an incidental in the checklist.

### Speed

The four `*UnderReserveLoad` benchmarks, `developers/speed.sh quick`
(`SPEED_BENCH=UnderReserveLoad`, `SPEED_SCENARIOS=none`) on
farm22-wrstat01.

The first final fix (the split into transactions of at most 256 rglookups,
plus a wait after each fold transaction) regressed them. Its evening runs
(6 rounds, load-unmatched) gave FAIL against `d1a4b6d7`
([fix-speed-vs-d1a4b6d7.txt](phase1-gate-1g5/fix-speed-vs-d1a4b6d7.txt))
and against `f1bcc1d7`
([fix-speed-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-vs-f1bcc1d7.txt)).
This doc first said the fold changes did not account for that; review
round 1 showed they did, with 8 interleaved rounds on a quiet host
(2026-11-11 23:25 to 2026-11-12 03:03): that code against `f1bcc1d7`
FAIL (Kick and Resume sec/op +14.3% and +20.7%, reserve-p50 +32.8% and
+40.5%, Resume B/op +18.8%); the same code with the split and the wait
switched off against `f1bcc1d7` PASS; that code against the switched-off
copy FAIL (sec/op +26% and +30%, reserve-p50 +65% and +57%, Kick
reserve-p99 +26%). Each benchmark round starts a fresh server, adds 20,000
filler jobs and folds them while the op runs, so 78 extra commits and the
waits after them landed in the timed window.

The rglookup-only bound against `f1bcc1d7`, 8 interleaved rounds,
2026-11-12 03:43 to 05:51 (1-minute load average 1.6 to 3.0, one sample
8.3): **PASS**, no metric worse by more than 10% at p<0.05; every sec/op,
reserve-p50, reserve-p99, reserves/op, B/op and allocs/op difference is
not significant (p 0.08 to 0.96), geomean sec/op +1.5%, and Suspend
reserve-max -47.7%
([fix-speed-bytesplit-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-bytesplit-vs-f1bcc1d7.txt)).
Its fold of those 20,000 fillers into a fresh `history.bolt` dirties about
330 pages, under the 2MB bound, so it is one transaction as at `f1bcc1d7`.
A first attempt of this run at 03:39 was void: `/tmp` filled (the Go
build cache), and the base's benchmarks failed to write their wal.

The final code (completions bounded too) against `f1bcc1d7`, after the
gate soaks, 8 interleaved rounds, 14:30 to 16:11 (load 2.1 to 4.1)
([fix-speed-final-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-final-vs-f1bcc1d7.txt)):
FAIL on two Suspend metrics, reserve-p50 +40.7% (7.3 to 10.2us, p=0.007)
and B/op +10.7% (p=0.038), every other metric of every benchmark not
significant (sec/op geomean +0.6%). These benchmarks archive nothing, so
they run the same code as the rglookup-only bound, whose Suspend
reserve-p50 was 8.5 against 7.5us in the morning run. Suspend alone again,
10 interleaved rounds, 16:12 to 17:04 (load 1.7 to 2.7)
([fix-speed-final-suspend-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-final-suspend-vs-f1bcc1d7.txt)):
**PASS**, reserve-p50 8.9 against 10.2us (p=0.39), B/op +4% (p=0.39),
nothing significant. So the one failing run's Suspend figures did not
repeat; the base's own Kick and Resume p50 moved from 8.4 and 12.4us in
the morning to 19.7 and 27.0us in the afternoon.

#### Fold time

The reviewer's probe (60 unfolded adds of 1,000 jobs over 7 rep groups,
folder paused, then one `foldNow`; local disk; 4 interleaved runs each,
2026-11-12 05:52,
[fix-foldtime-review-probe.txt](phase1-gate-1g5/fix-foldtime-review-probe.txt)):
`f1bcc1d7` 0.76-0.85s in 30 transactions; the final fix 0.98-1.02s in 68,
+24%. Review measured the first final fix at 7.29-7.92s, and the 256
split alone at 2.35-2.53s in 240 transactions.

A probe with `history.bolt` holding 0 to 3,000,000 rglookups first (random
keys over 50 rep groups), folding one add of 4,096 then 20,000 jobs with
the bound off and then on, 3 runs
([fix-foldsplit-probe.go.txt](phase1-gate-1g5/fix-foldsplit-probe.go.txt),
[fix-foldsplit-probe.out](phase1-gate-1g5/fix-foldsplit-probe.out)): a
20,000-job add's fold took 108-118ms against 121-132ms (3 transactions)
with none held before it, 223-238ms against 261-539ms (12) at 200,000,
435-464ms against 538-556ms (27-28) at 1,000,000, and 649-716ms against
1.03-1.08s (36) at 3,000,000, writing 81MB against 111MB. A 4,096-job
add's: 18-19ms against 24-27ms (1) at none, 220-336ms against 293-373ms
(11) at 3,000,000. So the bound costs nothing while a transaction's
rglookups dirty less than 2MB, and at most about 1.6 times the fold time
and 1.4 times the bytes for a large add into a large `history.bolt`,
about one commit per 2MB dirtied. (These figures are for the page-count
measure the G2 and G3 code used; "Round 3" has the final code's.)

Completions, bounded after G1: a probe with `history.bolt` also holding N
1.5KB `jobscomplete` entries (random keys), folding the completions of
4,000 archived jobs with the bound off then on, twice each, 2026-11-12
14:0x
([fix-foldcomplete-probe.go.txt](phase1-gate-1g5/fix-foldcomplete-probe.go.txt),
[fix-foldcomplete-probe.out](phase1-gate-1g5/fix-foldcomplete-probe.out)):
0.11-0.16s against 0.17-0.21s at none (6-8 transactions against 2),
0.42-0.43s against 0.51-0.56s at 300,000 (21), and 0.47-0.50s against
0.72-0.77s at 1,000,000 (25), writing 66MB against 87MB. The
`*UnderReserveLoad` benchmarks and review's probe archive nothing, so the
completion bound does not change them; the run below measures the final
code anyway.

### Gate soaks, 2026-11-12

Each the gate soak's settings (`fix-soak.sh <arm> <src> 1`, `HOURS=1`, no
diagnostic build), one after another on farm22-wrstat01 against the same
NFS work directory. Per-line results are in `fix-<arm>-lines.txt` (burst
split) and `fix-<arm>-waitlines.txt` (every line, with checkpoints), the
fold and wal writer lines in `fix-<arm>-dbwriter.txt`, LSF counts in
`fix-<arm>-lsf.tsv`.

| arm | code | prodsim | LSF RUN 2,000 / 3,000 / peak | lines | over 50ms | worst p99 | abandoned | reservations |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G1 | backup fix + rglookups bounded | 10:00:32-11:01:12 | 10:21 / 10:47 / 3,103 | 61 | 3 | 57.7ms | 0 | 273,559 |
| K1 (45 min) | backup fix alone (bound off) | 11:02-11:48 | never / never / 1,686 | 46 | 1 | 64.0ms | 0 | 123,190 |
| G2 | backup fix + rglookups and completions bounded by node count (round 2) | 11:49:35-12:50:13 | 12:39 / 12:42 / 3,107 | 61 | 0 | 27.8ms | 0 | 206,911 |
| G3 | as G2 | 12:54:02-13:53:40 | 13:15 / 13:29 / 3,228 | 61 | 0 | 36.7ms | 0 | 280,553 |
| G4 (2026-11-13) | round 3 final (scaled size estimate, F1 fix) | 10:01:40-11:01:18 | 10:21 / 10:36 / 3,056 | 61 | 0 | 25.7ms | 0 | 279,706 |
| G5 (2026-11-13) | round 4 draft (bound grows from a 5s lag; freelist handling) | 12:55:01-13:54:40 | 13:16 / 13:31 / 3,109 | 61 | 3 | 98.6ms | 0 | 278,700 |
| H1 (2026-11-13, prodsim scale 2) | as G5 | 13:57:44-14:57:23 | peak 3,126 | 61 | 6 | 123.7ms | 0 | 278,943 |
| G6 (2026-11-13) | round 4 final (bound grows from a 60s lag) | 15:23:02-16:22:41 | 15:44 / 15:58 / 3,109 | 61 | 1 | 61.9ms | 0 | 278,937 |

G1 is the first complete gate soak of a fixed build: 3 of 61 lines over
50ms (57.7ms at 10:59:32, 53.5ms at 11:00:32, 51.4ms at the partial last
line, 11:01:12), against 31 of 61 in the 1g.5 run. Every burst line passed
(p99 9.2-29.9ms; 12 lines), where every arm on 2026-11-11 failed 1-4. The
three failures are the last three minutes, at 6,600 reservations a minute,
with no burst start, and the last (partial, 40s) also held the soak's last
checkpoint, 0000000d (59.6MB written in 6.5s, 11:00:43-11:00:50), so it
cannot be put on the fold alone. The fold writer's lines show its
transactions holding 1.6s on average and up to 3.3-3.7s (10:58:32,
10:59:32), folding chunks of completions (22,423 and 10,154 records in 36
and 26 transactions), with the wal's slowest syncs 332-341ms in those
minutes. Over the hour the
fold's mean transaction rose from 65ms to about 1s at a steady 3-4ms a
record: each completion writes its job, end-time index, rep group end
time, seq and samples at random places in a 1-2GB `history.bolt`, and a
chunk of hundreds of them commits like the burst's rglookups did. So
completions were bounded too.

K1, the A/B arm for the rglookup bound, is void as a comparison: the farm
gave it at most 1,686 runners (2,800 pending), so it never reached the
load at which earlier arms failed. Its one failure (64.0ms) is the first
burst line, at 11:03:43, with 1,682 reservations.

G2, the round 2 code (node-count bound), passed every line: 61 lines, worst p99
27.8ms, 0 abandoned, no checkpoint or burst line over 30ms; the fold's
transactions held at most 0.85s in the 3,000-runner minutes (G1: 1.1-3.7s). But
the farm held it under 2,000 runners until 12:39, so it ran at 3,000 for its
last 8 minutes against G1's 14, and its 13 lines at 4,000 or more reservations a
minute compare with G1's 33. G3 repeats it.

**G3, the round 2 code, passes the gate criterion**: all 61 lines at p99
50ms or under (worst 36.7ms, 13:34:02, a burst line; the rest of the
3,000-runner lines 3.3-30.9ms), 0 abandoned, 0 not durable, 280,553
reservations, more than G1's, with 3,000 runners from 13:29 (G1: 10:47)
and 40 lines at 4,000 or more reservations a minute (G1: 33). Its last
five lines, where G1 failed, were 17.3-30.9ms, and its fold's
transactions held at most 0.87s all hour. The largest single wait,
3.25s, did not move any line's p99 past 50ms.

`TestAddLatency1000` 3 times after each (`WR_TEST_STORE=log`,
`WR_ADDBENCH_DIR` on NFS, `fix-<arm>-addbench-<n>.out`): after G1 p99
55.6, 59.8 and 57.7ms; after G2 55.6, 59.8 and 53.5ms; after G3 57.7,
55.6 and 66.1ms; PASS each time.

### Round 3: review round 2's findings

Review round 2 passed the speed question and the lint refactor, and failed
the fold split on two defects; both are fixed in the final code.

- **Speed.** The reviewer's quiet-host A/B of the G3 code against
  `f1bcc1d7`, all four `*UnderReserveLoad` benchmarks, 12 interleaved
  rounds, 2026-11-12 17:07 to 19:21 (load 0.5 to 2.6)
  ([fix-speed-review2-all12.txt](phase1-gate-1g5/fix-speed-review2-all12.txt)):
  **PASS**, no metric significant; Suspend reserve-p50 -10.8% (8.2 to
  7.3us, p=0.16). So the 14:30 run's Suspend FAIL was noise.
- **F1, recovery after a split fold.** A crash, or a restore of a backup
  whose tick copied `history.bolt`, between a fold's transactions left
  completions in `completeSeq` past the folded position. Recovery loads
  its history index from `completeSeq`, and replay's `applyComplete`
  returned early for a complete record at or below its key's index seq
  without clearing a live job's std: a job archived and kept live to run
  again showed its old std, and every checkpoint failed
  (`errCheckpointRefsUnswapped`) until a restart. Fixed by clearing std in
  that case as in the folded-position case (`completeOutranked`).
  `TestStoreFoldSplitCrashImages` images the store before each fold
  transaction and requires each image to answer and checkpoint as the
  pre-fold image, then as the store once folded; red without the fix (the
  kept-live job's `stdo="out" stde="err"` against `""`).
- **F2, the bound's measure.** The node count alone missed values larger
  than a page: with a 2MB bound, review measured 2.7-2.9MB written per
  transaction with 100-byte commands, 15-17MB with 8KB and 60-85MB with
  40KB. The final estimate adds the key and value bytes put and scales by
  the bytes of pages earlier fold transactions wrote per byte estimated
  (see "The fix" item 3). `TestStoreFoldTransactionsWriteAboutTheBound`
  folds 400 completions of 100-byte, 8KiB and 40KiB commands with a 1MiB
  bound and requires each transaction to write (history.bolt's
  `PageAlloc`) at most 1.5 times the bound, and all but the last at least
  a third of it: they wrote 0.5-0.7MB (100 bytes, 2 transactions),
  1.1-1.4MB (8KiB) and 0.9-1.4MB (40KiB). G3's node-count measure folds
  each set in one transaction.
- **Evidence the gate soaks are not.** The backup half has no clean A/B:
  A1 (2026-11-11) is a diagnostic proxy, a build that never re-copied
  `history.bolt`, not the fix, and K1 was void. G1 against G3 is
  load-unmatched evidence (3,000 runners for 14 and 25 minutes) for the
  completion bound only. And the final code's transactions differ from
  G3's (prodsim's jobs wrote about 2.8MB a transaction under G3's 2MB
  node-count bound; the final estimate keeps them near 2MB), so G3 no
  longer evidences the final code. G4 ran it.

**G4, the round 3 final code** (`src-gate` at 353a4db, a fresh build;
2026-11-13, prodsim's lines 10:01:40 to 11:01:18;
`fix-G4-*` files): **passes the gate criterion**: all 61 lines at p99
50ms or under (worst 25.7ms at 10:21:40; burst lines 7.2-25.7ms, the rest
2.9-23.6ms), 0 abandoned, 0 not durable, the largest single wait 177ms,
279,706 reservations, 3,000 runners from 10:36 (peak 3,056), 41 lines at
4,000 or more reservations a minute. `TestAddLatency1000` 3 times after it
(11:02 to 11:03): p99 59.8, 64.0 and 57.7ms, PASS each time.

Its folder ran near saturation late in the hour: from 10:48 the fold
writer's lines show 307-318 transactions a minute of about 35 records
each, held 184-191ms on average (at most 0.6s), so busy about 58s of each
minute, against G3's 57-110 a minute held 0.53-0.64s (about 31-60s a
minute). The estimate's scale splits even the steady folds of completions
(each writes its job and indexes at random places, so about 57KB of pages
per completion in a history.bolt of this size). Whether the fold fell
behind: review round 3 measured it from each soak's `ckptwatch.tsv` (see
"Round 4").

The final code's transactions and fold time, local disk, 2026-11-12 20:39
to 20:44 ([fix-round3-probes.out](phase1-gate-1g5/fix-round3-probes.out),
probe sources `fix-*-probe.go.txt`):

- **Bytes written per transaction** at the default 2MB bound, folding the
  completions of 4,000 archived jobs into a store already holding 20,000
  ([fix-txsize-probe.go.txt](phase1-gate-1g5/fix-txsize-probe.go.txt)):
  100-byte commands 2.08-2.51MB (16 transactions; review measured
  2.7-2.9MB with the node count); 8KB commands 4.32MB for the first, then
  1.77-2.62MB (70); 40KB commands 5.26MB for the first, then 1.65-2.75MB
  (302). The first transaction of a fold whose records differ from the
  last fold's runs on the old scale; the rest are near the bound.
- **Fold time** (bound on against `f1bcc1d7` or the bound off): review's
  probe 0.84-0.90s at `f1bcc1d7` (30 transactions) against 1.08-1.13s
  (73), +27%; a 20,000-job add's rglookups 0.66s against 1.17s (53) with
  3,000,000 already held, 0.47s against 0.61s (32) with 1,000,000; the
  completions of 4,000 jobs 0.49-0.55s against 1.07-1.17s (18-54) with
  1,000,000 already held, 0.41-0.44s against 0.67-0.70s (39) with
  300,000. A split fold costs about one commit per 2MB written, so the
  more it writes per record the more it costs: up to about 2.2 times the
  time in these probes, for a large chunk into a large `history.bolt`.
- **Speed**: the four `*UnderReserveLoad` benchmarks against `f1bcc1d7`,
  8 interleaved rounds, 20:44 to 22:40 (load 2.0 to 3.2, one sample 9.4)
  ([fix-speed-round3-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-round3-vs-f1bcc1d7.txt)):
  **PASS**, no metric significant, though noisy (Kick and Resume
  reserve-p50 ranges of 250-350% in both arms). These benchmarks fold
  20,000 fresh rglookups and no completions, which the estimate splits
  into a few transactions (review's probe: 73 against 30).


### Round 4: review round 3's findings

Review round 3 verified F1 fixed and F2's bound holding, and failed the
round 3 code on fold throughput.

- **B1, capacity.** Folder busy time (transactions times mean hold, from
  the fold writer's lines) at matched load, 10:46 to 10:50: `f7006373`
  (1g.5's first gate soak) folded 100,088 records busy 188s of 300s (63%);
  G4 folded 99,146 busy 289s of 300s (96%), and was busy 55-60s of every
  minute from 10:41 to 11:01. G1 and G3 were as busy (57-60s a minute) in
  their burst minutes. The split takes away what a backlog bought at
  `f1bcc1d7`: chunks of up to 4,096 records committed once each, so a
  backlog folded cheaper per record. Fold lag, which this doc said was not
  measured, is bounded by each soak's `ckptwatch.tsv` (the wal file's
  rotation to the next `.ckpt.tmp`'s creation): `f7006373` 0-21s, at most
  51s; G1 up to 133s, G3 up to 116s, G4 up to 147s (review round 3's
  figures).
- **B2, a large freelist.** bbolt writes the whole freelist at every
  commit, and the scale counted it, so with a freelist near 2MB each
  transaction folded about one completion. Review's probe, 4,000
  completions: a 2.1MB freelist took 2.26s in about 226 transactions
  writing 485MB (`f1bcc1d7`: 0.21s, 2 transactions); a 4.1MB one 57s in
  3,616 transactions writing 14.4GB (`f1bcc1d7`: 0.21s, 2). Production's
  old bbolt database reached 45.9% free, a 13.5MB freelist.
- **Fixes.** The bound grows with the fold's lag (`foldTxLimit`: 2MB to a lag of
  60s, then in proportion to it; first 5s, see G5 below), restoring big, cheap
  transactions to a fold that is behind, while one that keeps up stays at 2MB,
  the size that kept G4's lines under 50ms; the scale leaves out the freelist
  write; no transaction is bounded below the freelist write; and a transaction
  records at least 16 completions. The fold writer's `"db writer"` line logs
  `maxLag`, the longest a record being folded may have waited since it was
  durable (since the start of the last fold that reached its target). It is
  an upper bound: as it counts from that fold's start, it can read up to
  about twice the oldest record's real age when the folder trails. After an
  open it starts at 0, as the first fold sets its start, which errs towards
  the small bound.
- **Docs.** "The fix" and the spec claimed the scale was kept within 0.25
  and 64; it never was (the clamp was dropped in round 3). Corrected. The
  crash-image test's split fold is now 3 transactions (16, 16 and 8
  completions); in round 3 it was 40 (one each), not the 3 its
  description said.

Round 4's evidence (local disk unless said;
[fix-round4-probes.out](phase1-gate-1g5/fix-round4-probes.out); the
probes ran on the 5s-step draft at 12:49 and again on the final code at
16:39 to 16:44 with the same results within noise, as none of them lets
the lag pass 5s; the figures are the final code's):

- **Freelist** (review round 3's probe,
  [fix-freelist-probe.go.txt](phase1-gate-1g5/fix-freelist-probe.go.txt),
  4,000 completions): freelist 1KB 0.22s in 13 transactions writing 25MB
  (`f1bcc1d7`: 0.20s, 2, 24MB); 1.0MB 0.25s, 13, 38MB (0.19s, 2, 26MB);
  2.1MB 0.31s, 13, 51MB (0.20s, 2, 28MB; round 3's code 2.26s, about 226,
  485MB); 4.1MB 0.31s, 7, 52MB (0.21s, 2, 32MB; round 3's 57s, 3,616,
  14.4GB).
- **Fold time**: review's probe (60 unfolded 1,000-job adds) 0.88-0.90s at
  `f1bcc1d7` (30 transactions) against 1.07-1.09s (72), +22%; 4,000
  completions into a history already holding 1,000,000, bound off then
  on: 0.54-0.58s (2) against 0.85-0.88s (33), and at 300,000 0.45-0.47s
  against 0.61-0.64s (28-29). These probes fold just after the records
  were written, so their lag stays a few seconds and the bound 2MB: they
  measure a fold that keeps up. A fold that is behind is
  `TestStoreFoldBacklogCapacity`'s: with records that waited 20 minutes,
  2,000 completions of 8KiB-command jobs fold in at most 2 transactions
  writing
  at most 1.25 times the bound-off fold (1 transaction), against more than
  4 when they have just been written.
- **Bytes per transaction** at the 2MB bound, 4,000 completions into a
  store of 20,000: 100-byte commands 2.02-2.64MB (16 transactions); 8KB,
  after the first (5.06MB), about 1.8-2.4MB (70); 40KB, after the first
  (6.58MB), about 2.1-3.1MB (248): here the 16-completion floor binds, 16
  of these completions writing 2-3MB.
- **Tests and mutants**: `TestStoreFoldTxLimit`,
  `TestStoreFoldBacklogCapacity`, `TestStoreFoldLargeFreelist` and the
  extended `TestStoreStatsReport` are new; all 8 round 4 mutants and the
  13 still-applicable earlier ones are killed (the checklist's table).
- **Gates** (14:59 to 15:21, the 60s step): `make lint` 0 issues;
  `TestReserveWaitDuringBackup` p99 8.0ms; `make test`,
  `WR_TEST_STORE=all make test` and `make race` passed, 1,295 tests each
  (the 5s-step draft passed the same at 12:24 to 12:48).
- **Soaks**: G5 (2026-11-13 12:55:01 to 13:54:40, the gate soak's
  settings; `src-gate` at 22acb76, round 4 with a 5s `foldLagStep`;
  `fix-G5-*`) failed 3 of 61 lines, all portal-burst lines at 3,000
  runners: 77.6ms at 13:40:01, 69.2ms at 13:45:01, 98.6ms at 13:50:01; 0
  abandoned; 278,700 reservations. In each of those minutes the fold's
  lag reached 46-51s (`maxLag`), the bound grew to 15-20MB, its
  transactions held up to 1.7-2.4s, and the wal's slowest syncs were
  171-233ms. Between bursts its lag was 2.7-3.3s and the folder busy 31-42s
  a minute (G4: 55-60s); at 46 to 50 minutes in, the window review
  compared, busy 206s of 300 (69%; `f7006373` 63%, G4 96%). Fold lag
  bounded by the checkpoints (`ckptwatch.tsv`, [fix-ckptlag.txt](phase1-gate-1g5/fix-ckptlag.txt)):
  at most 51.5s (`f7006373` 51.0s; G1 133.3s, G3 116.4s, G4 147.4s).
  `TestAddLatency1000` 3 times after it: p99 64.0, 57.7 and 61.9ms, PASS.
  So a 5s step bought back the capacity but gave the bursts back their
  big commits; the step is now 60s (the bursts' lag stayed under about
  50s), which G5 did not soak.

  H1 (13:57:44 to 14:57:23; the same 5s-step code at prodsim scale 2,
  bursts twice the size; `fix-soak2.sh H1-scale2 <src> 1 SCALE=2`;
  `fix-H1-*`): fold lag stayed bounded, `maxLag` at most 52.0s and the
  checkpoints' bound at most 48.3s (`f7006373` at scale 1: 51.0s), with
  3,126 runners at peak and 278,943 reservations; but 6 of 61 lines
  failed, all burst lines at 4,600-6,800 reservations a minute
  (59.8-123.7ms), for the reason G5's did.

  G6 (15:23:02 to 16:22:41; the final code, `src-gate` at 4c3e7d5, a
  60s `foldLagStep`; `fix-G6-*`): 60 of 61 lines passed; every burst line
  passed (5.4-36.7ms); 0 abandoned, 278,937 reservations, 3,000 runners
  from 15:58 (peak 3,109). **It fails the gate on one line**: the last,
  partial one (16:22:41, 4,177 reservations), p99 61.9ms. That line covers
  39s, 16:22:02 to `wr manager stop` at 16:22:40, and coincides with the
  soak's last checkpoint, 0000000d (58.5MB written in 5.7s,
  16:22:18-16:22:23). Round 4's lag growth was inactive in that window:
  the fold writer's line covering it (16:23:02) gives `maxLag` 6.2s, so
  the bound was 2MB (the 19.1s of its 16:22:02 line is the minute
  before). That 16:23:02 line, whose interval includes the shutdown, also
  gives the wal's slowest sync (87.7ms) and the fold's longest hold
  (910ms); nothing finer covers the 39s alone. The lines before it were
  40.9ms at 16:21:02 (no burst) and 36.7ms at 16:22:02. Final checkpoints
  of the same size passed in G5 (0000000d, 58.8MB, line p99 10.2ms) and
  H1 (58.2MB, 24.6ms), and G1's failing last line (51.4ms) also held its
  0000000d (59.6MB). Without a trace of that window, the cause cannot be
  separated between the checkpoint's write, the 2MB fold commits and the
  NFS load; the checkpoint is the likely one, as the unpaced write of a
  checkpoint this size holds reservations up to about 150ms on local
  disk and NFS alike (round 5, `TestReserveWaitDuringCheckpoint`), and
  round 5 paces it. Fold lag, from 3,000 runners on (15:58): the first
  portal-burst cycle took `maxLag` to 55s, and each of the next four
  (16:03, 16:08, 16:13, 16:18) to 2m0s-2m16s over 2-3 minutes (bound up
  to about 4.5MB), the folder busy 57-60s of those minutes, and back to
  3-7s before the next burst, with the folder then busy 34-44s a minute;
  those five bursts' lines (`fix-G6-lines.txt`) were 23.6-36.7ms. The
  checkpoints bound the lag at 106.6s (`f7006373` 51.0s, G4 147.4s, G5
  51.5s). So at G6's load the fold catches up between
  bursts, but with less to spare than `f7006373`.
  `TestAddLatency1000` 3 times after it: p99 59.8, 66.1 and 61.9ms, PASS.
  The higher-load soak of the final code (scale 2) is still to run: H1
  measured the 5s draft.
- **Speed**: the four `*UnderReserveLoad` benchmarks against `f1bcc1d7`,
  8 interleaved rounds, 17:39 to 19:12, after the soaks (load 1.7 to 3.4)
  ([fix-speed-round4-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-round4-vs-f1bcc1d7.txt)):
  **PASS**, no metric significant (sec/op geomean -1.1%). A first run
  from 16:44 was void at round 5: the base's Kick benchmark server could
  not bind its port.

### Round 5: review round 4's findings and the checkpoint

Review round 4 found the code sound and failed it on a test gap and on
this doc's account of G6.

- **Test gap.** Mutant MA (`foldChunk` setting `foldLagFrom` to the fold's
  end, not its start) survived every test, as
  `TestStoreFoldBacklogCapacity` set `foldLagFrom` by hand. Its new Convey
  folds 200 jobs with the bound at 1 byte and each transaction taking
  100ms (about 0.41s), then 10 more, and requires the second fold's
  `maxLag` to be at least the first fold's duration: MA gives 1.4ms
  against 409.9ms. A Convey in `TestStoreStatsReport` also kills MC (no
  restore of `maxLag` in `writerStats.take` for an interval with no
  transaction).
- **G6's account.** "Round 4" and "Open for the owner" above are reworded:
  G6's failing line coincides with its last checkpoint but cannot be put
  on it from the soak alone, and the figures that came from the wrong
  interval are corrected.
- **The checkpoint (1d.2).** With the fold's lag growth inactive in G6's
  failing window and production's checkpoints at 55-76MB written over
  5-7.6s, round 5 paces the checkpoint's write.

#### Checkpoint pacing

Root cause, from the code: `checkpointMerge.write` wrote the whole
`.ckpt.tmp` through a 4KiB `bufio.Writer` straight to the file and made
it durable with one `f.Sync` at the end. No lock the hot path takes is
held meanwhile (the checkpointer holds only `ckptMu`; the store's lock is
taken later, for the Ref swap in batches), so the cost is I/O alone: the
written pages stay dirty in the page cache (well under the kernel's
dirty thresholds at these sizes) until that fsync, which then sends tens
of MB to the device or NFS server at once, and the wal's syncs, which
each reservation waits for, queue behind it.

The fix writes through the backup copy's `backupCopyWriter` (async
writeback of each `backupCopySyncBytes`, 1MiB, waiting on the one before;
`jobqueue/db.go`), so at most about 2MiB is ever outstanding. Unlike the
backup copy it keeps the written pages cached (`keepCached`, which skips
`FADV_DONTNEED`), as folds and queries read the checkpoint's records
through their Refs. The temp file, fsync, rename and directory fsync are
unchanged.

`TestReserveWaitDuringCheckpoint` (tag `reliability_repro`, in
`jobqueue/reserve_checkpoint_tail_repro_test.go`): a log store with
120,000 live jobs of 1KiB random commands, checkpointed once; then
reservations every 5ms, waiting for their record to be durable, from a
second before the next checkpoint until it ends; p99 of those made while
it ran must be at most 50ms, and the checkpoint over 40MB (it is 72.9MB).
Interleaved runs, 2026-11-13
([fix-r5-ckpt-ab.txt](phase1-gate-1g5/fix-r5-ckpt-ab.txt); the local
round 5 row's 8 runs are 7 there and the gates' run in
[fix-r5-gates.txt](phase1-gate-1g5/fix-r5-gates.txt)):

| arm | where | runs | reservation p99 | max | checkpoint took |
| --- | --- | --- | --- | --- | --- |
| `f1bcc1d7` | local disk | 4 | 122.1-142.4ms | 142.0-156.7ms | 2.33-2.74s |
| round 4 (pre-fix write) | local disk | 4 | 128.3-138.3ms | 146.0-158.0ms | 2.33-2.39s |
| round 5 | local disk | 8 | 5.5-16.1ms | 9.0-28.4ms | 2.19-2.36s |
| `f1bcc1d7` | NFS (evening) | 5 | 97.4-110.6ms | 119.6-135.3ms | 2.70-3.03s |
| round 4 (pre-fix write) | NFS (evening) | 3 | 99.5-143.0ms | 117.5-168.1ms | 2.78-2.94s |
| round 5 | NFS (evening) | 8 | 3.4-43.7ms | 6.1-62.7ms | 2.12-2.66s |

So the test is red on local disk as well as NFS, and pacing costs the
checkpoint nothing: it is 0.1-0.6s faster, as the final fsync no longer
waits for the whole file. On NFS one round 5 run of 8 came near the
bound (23:28, p99 43.7ms, the rest 3.4-6.6ms).

They also show where the test's longer runtime at round 4 and 5 on NFS
(35-38s against 14.2-14.8s at `f1bcc1d7`) goes: not into the measured
checkpoint but into the first one, which folds the 120,000 jobs' adds
(24 adds of 5,000) before it merges. It took 27.5-27.7s against
6.6-6.8s at `f1bcc1d7`, the adds themselves 2.6-2.7s at both (on local
disk at round 5, the gates' run: adds 2.06s, first checkpoint 4.6s). That is the round 1-4 fold bound on NFS: a fold
that keeps up commits its adds' rglookups in 2MB transactions, each
with its own fsync, where `f1bcc1d7` committed each 4,096-record chunk
once. See "The fold bound's cost" below.

`TestLogStoreCheckpointPacesItsWrite` (untagged, in `make test`): a
checkpoint of 3,000 live jobs (over 3MiB) forces writeback at least once
a MiB (`backupPaceHook`) and leaves more than half its file in the page
cache (`mincore`; 100% in fact, against 22% with the pages dropped; round
6 lowered the bound from 90%). Mutants, each in a plain copy, each
compiling ([fix-r5-mutants.txt](phase1-gate-1g5/fix-r5-mutants.txt)):

| mutant | killed by |
| --- | --- |
| CK1: write unpaced (`bufio.NewWriter(f)`, the pre-fix write) | PacesItsWrite, Line 79 (0 paces, want 5); `TestReserveWaitDuringCheckpoint` (the table's pre-fix arm) |
| CK2: `keepCached` false (pages dropped as the backup copy's are) | PacesItsWrite, Line 80 (23% resident) |
| CK3: pace every 8MiB | PacesItsWrite, Line 79 (0 paces, want 5) |
| MA: lag from a fold's end | FoldBacklogCapacity, Line 437 |
| MC: `take` not restoring `maxLag` | StoreStatsReport, Line 390 |

Round 6 (review round 5): nothing tested that the backup copies still drop
their pages from the cache, which `keepCached` made switchable. Two tests
now check it, each pacing every 128KiB:
`TestLogStoreIncrementalBackupDropsHistoryCopyFromCache` (a tick's copy
of a history.bolt over 3MiB to a directory target, through
`writePacedFile`) and a Convey in `TestDBBackupCopy` (`db.copyBackup`
of a database of 4MiB of payload, a 6.0MiB copy of 1,545 pages)
require at most 50% of the copy resident; it is
2.1% and 2.7% ([fix-r6-mutants.txt](phase1-gate-1g5/fix-r6-mutants.txt)).

| mutant | killed by |
| --- | --- |
| M1: `keepCached: true` in `writePacedFile` | DropsHistoryCopyFromCache, Line 280 (100% resident) |
| M2: `keepCached: true` in `copyBackup` | DBBackupCopy, Line 1343 (100% resident) |
| CK2 again, with the 50% bound | PacesItsWrite, Line 80 (22% resident) |

Round 7 (review round 6): those residency checks hold only where the
temporary directory's filesystem can show them. On tmpfs `FADV_DONTNEED`
cannot drop shmem pages, so the backup checks failed on the unmutated
tree; on NFS `mincore` showed no page resident in a file opened again
after a rename, so the backup checks passed vacuously (M2 survived) and
the checkpoint's failed. All three now go through one helper,
`soResidency` (`store_checkpoint_pace_test.go`), which first probes the
directory: a 64KiB file written, synced, renamed and opened again must
show its pages resident, and lose them on `FADV_DONTNEED`. If not, or off
Linux, it logs why and skips the assertion (`SkipSo`). On ext4 `/tmp`
all three still assert, and M1, M2 and CK2 are killed (each now at the
helper's assertion, Line 100 of `store_checkpoint_pace_test.go`;
[fix-r7-mutants.txt](phase1-gate-1g5/fix-r7-mutants.txt)); with
`TMPDIR` on `/dev/shm` and on NFS (a directory under
`/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix`) all three log their skip and
pass on the unmutated tree
([fix-r7-tmpdirs.txt](phase1-gate-1g5/fix-r7-tmpdirs.txt)).

Round 7's gates (2026-11-14 00:57 to 01:09, scratch `HOME`, every `OS_`
variable unset; [fix-r7-gates.txt](phase1-gate-1g5/fix-r7-gates.txt)):
`make lint` 0 issues; `go vet` of `./jobqueue/` and `./jobqueue/...`,
with and without `reliability_repro`, clean; the targeted set passed, and
again under `-race` (85 tests each); `make test` passed, 1,297 tests.
Against round 5's tree, still only the three `_test.go` files changed
outside `.docs`.

Round 6's gates (2026-11-14 00:22 to 00:34, scratch `HOME`, every `OS_`
variable unset; [fix-r6-gates.txt](phase1-gate-1g5/fix-r6-gates.txt)):
`make lint` 0 issues; `go vet` with and without `reliability_repro`
clean; `TestStoreFold*`, `TestStoreStats*`, `TestLogStoreCheckpoint*`,
`TestLogStoreIncrementalBackup*` and `TestDBBackupCopy` passed, and again
under `-race` (85 tests each); `make test` passed, 1,297 tests. Against
round 5's tree only three `_test.go` files changed outside `.docs`
(`db_bolt_test.go`, `store_backup_history_copy_test.go`,
`store_checkpoint_pace_test.go`).

#### Gates

On the final code, farm22-wrstat01, 2026-11-13, each with a fresh scratch
`HOME` and every `OS_` variable unset: `make lint` 0 issues (20:30 to
20:31); `TestReserveWaitDuringCheckpoint` p99 11.6ms and
`TestReserveWaitDuringBackup` p99 7.4ms, PASS (20:31 to 20:33); `make
test` (20:33 to 20:39), `WR_TEST_STORE=all make test` (20:39 to 20:45)
and `make race` (20:45 to 20:55) passed, 1,296 tests each. Mutants above
re-run on the final code at 20:56 to 21:00
([fix-r5-gates.txt](phase1-gate-1g5/fix-r5-gates.txt)).

Speed: the four `*UnderReserveLoad` benchmarks against `f1bcc1d7`, 8
interleaved rounds (`developers/speed.sh quick`, 21:52 to 23:25, load 1.4
to 5.7;
[fix-speed-round5-vs-f1bcc1d7.txt](phase1-gate-1g5/fix-speed-round5-vs-f1bcc1d7.txt)):
**PASS**, no metric significant (sec/op geomean +1.5%, reserve-p50 +8.1%
with every p over 0.5, reserve-p99 0.0%). A first run from 20:57 was
void at round 5, as round 4's was: the base's AddMany benchmark server
could not bind its port. The checkpoint's own time is in the table above.


#### Monday soaks, prepared

Not run yet. `src-gate` is at 8dab632aa844fad73528e4172ba257ac90e7ab3f,
file for file round 5's working tree outside `.docs` (whose records of it
came after); its wr build gives that `vcs.revision` with
`vcs.modified=false`. [fix-r5-chain17.sh](phase1-gate-1g5/fix-r5-chain17.sh)
(as `tools/chain17.sh`) waits for 09:40 on 2026-11-16, refuses to run if
`src-gate` is not clean at that commit, rebuilds the jobqueue test binary
from it, runs G7 (the gate soak's settings,
[fix-r5-soak.sh](phase1-gate-1g5/fix-r5-soak.sh)) from 10:00, then
`TestAddLatency1000` 3 times
([fix-r5-addbench.sh](phase1-gate-1g5/fix-r5-addbench.sh), runs 19-21),
then H2 (prodsim scale 2) if it can start by 15:52, and logs each soak's
wr buildinfo. Nothing it uses is in a session's `/tmp` scratchpad (its Go
build cache is on NFS, warmed on 2026-11-13).

Started at 23:33:24 UTC on Friday 2026-11-13, as PID 308228 (by the
orchestrator); it is sleeping until 09:40 on 2026-11-16. Do not start it
again: a second copy would collide on the soaks' `SOAK_ROOT` and ports.
Note for future chain scripts: take a `flock` on a lock file at start, so
a second copy exits at once. Its command was:

```text
setsid nohup /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/tools/chain17.sh > /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/chain17.out 2>&1 < /dev/null &
```

Round 6 changed only tests and docs (`git diff --stat` against round 5's
tree), so the wr binary it builds from `src-gate` is still the final
code; `src-gate`'s test files are round 5's, which matters only to the
test binary it rebuilds for `TestAddLatency1000`, a test round 6 left
unchanged.


### Open for the owner

- **NFS stalls outside wr.** A1 had a 26s stretch (13:00:11-13:00:37) in
  which every wal sync and fold commit took 0.5-2.2s, failing its line at
  102.8ms; the 1h gate soak's first-minute 1.94s fsync looks the same. No
  write-path change in wr avoids a stall of the NFS service itself, and
  any one in the hour fails the gate as written.
- **The backup's checkpoint now lags.** While a `history.bolt` copy is put
  off, the backup keeps its older checkpoint and every wal part since its
  history's folded position, so a restore replays up to about
  `history.bolt`'s size of wal. This is the Phase 1 form only; Phase 2's
  sealed history segments replace the whole-file copy.
- **The 1d.2 checkpoint incidental, paced in round 5, not soaked yet.**
  The checkpoint grows with the live set, to 55-76MB written over 5-7.6s
  by the end of each gate soak. G6's one failing line (the last, partial,
  39s) coincided with its last checkpoint (58.5MB in 5.7s), with the fold
  at 2MB; same-size final checkpoints passed in G5 and H1, so the soak
  alone does not separate the checkpoint from the fold commits or NFS
  load. Round 5 paces the checkpoint's writeback every MiB, after
  `TestReserveWaitDuringCheckpoint` showed an unpaced 73MB checkpoint
  holding reservations to p99 100-143ms, on local disk and NFS alike
  (3.4-43.7ms paced, all but two under 12ms). The Monday soak G7 is its
  gate evidence.
- **Fold capacity against the reserve tail.** Small commits protect
  reservations and cost the folder throughput. The final code keeps 2MB
  transactions until a record has waited 60s, then grows them: G6 passed
  every burst line, with its fold lag (`maxLag`, an upper bound) peaking
  at 55s in the first 5-minute burst cycle at 3,000 runners and
  2m0s-2m16s in the next four, and back to a few seconds between
  (`f7006373`: lag at most 51s). So the 60s step lets the fold settle
  minutes behind where `f1bcc1d7` settles seconds behind: review round
  4's probe on simulated NFS at 400 completions a second gave a lag of
  108s and rising slowly, against 3-7s for `f1bcc1d7`. A restart, which
  sets the lag back to 0, puts off growth until records have waited 60s
  again. Growing from 5s (G5, H1) kept the lag near
  `f7006373`'s but failed 3 and 6 burst lines. At loads well above G6's,
  the bound grows and burst lines can fail again; the final code's
  higher-load soak (prodsim scale 2) has not run. Making history.bolt's
  commits cheaper for the wal (another device, or bbolt's `NoSync` with
  rebuild from the wal on a host crash) would remove the trade-off; both
  are design changes for the spec.
- **The fold bound's cost.** A fold that keeps up, of a large add or a
  chunk of completions into a large `history.bolt`, takes up to about 1.6
  times as long on local disk (round 4's probes), for one commit per 2MB
  written; a fold that is behind grows its transactions. On NFS it is
  more: a checkpoint's fold of 120,000 freshly added jobs took 27.5-27.7s
  against 6.6-6.8s at `f1bcc1d7` (round 5, `fix-r5-ckpt-ab.txt`), its
  records too fresh for the bound to grow. K1, the A/B arm for the
  bound, was void (the farm gave it 1,686 runners), so the bound's benefit
  rests on 2026-11-11's arms (F6 against F2-F4) and G1 against G3.
- **One wait of 3.25s in G3** (13:01, a 1,374-reservation minute, p99
  28.8ms) and 1.67s in G2 (12:00, at a checkpoint) did not fail their
  lines; single NFS stalls of that size remain possible.
