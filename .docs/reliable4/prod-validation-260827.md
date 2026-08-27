# Production validation of the dep-granularity work, 2026-08-27

**STATUS: the change works in production.** Recovery went from 42m56s to 36.4s
and peak RSS from ~181 GB to 7.84 GB, with no OOM and no crash. Three issues
remain, none urgent; the top one is DB-backup copy I/O, which is a bounded piece
of work that has already been prototyped.

Read this first for the current position. Background: the OOM diagnosis is in
`prod-restart-260825.md`, the design in `.docs/dep-granularity/spec.md`, and each
phase file's "Phase N outcome" section carries corrections to that spec rather
than just status.

## What was deployed

`wr v0.37.1-77-gfb5df01` on farm22-ibackup01, started 2026-08-27 07:36:26 by the
operator. Seven commits, each implemented, independently reviewed and committed
separately:

| commit | change |
| --- | --- |
| `b967dbb` | `queue.SatisfyDependency`; a dependency key may name a group, not an item |
| `3390f23` | sharded `depGroupMembers`; `dependencyKeys` replaces member expansion; `bucketDTK` no longer written |
| `93d106b` | recovery rebuilds membership, archive releases it; per-pass seen-group cache |
| `0180128` | add/modify/delete hooks keep membership current |
| `f2ccaee` | publish the listener only once recovery completes, plus the whole startup series |
| `0b24478` | memory, transaction-count and scale gates |
| `fb5df01` | startup-ordering gate; `max(1, chunkSize)` guard; dead status call removed |

Two operator actions matter for reading the numbers below: the DB was **compacted**
before this run (15.03 GB at 45.9% free pages -> 7.3 GB), and `db_predep-granularity`
was kept as a pre-deployment copy.

## Startup, measured

These are the phase lines this delivery added (section E9), at warn so they appear
in an ordinary manager log with no `--debug`:

| phase | elapsed |
| --- | --- |
| opened database | 924 ms |
| decoded live jobs (151,280) | **34.664 s** |
| built dependency-group state (112,486 memberships) | 318 ms |
| resolved prior job dependencies (151,280) | **95 ms** |
| enqueued prior jobs | 286 ms |
| **07:36:26 -> 07:37:02 total** | **~36.4 s** |

The comparison, from `prod-restart-260825.md`: **42m56s** to recover 150,472 jobs,
after which the manager served for four minutes and was OOM-killed.

**Dependency resolution is 95 ms.** That is the same work that previously took
tens of minutes and held 97.55% of a 15.65 GB heap. The 112,486 memberships that
used to be expanded into per-job key lists are now built once, in 318 ms.

## Memory, measured

```
VmHWM:  7844984 kB
VmRSS:  7844984 kB
```

`VmHWM == VmRSS`, so it rose to 7.84 GB and stayed flat - no spike to hide. The
five OOM kills were at 180,537,676 / 181,374,128 / 181,400,708 / 174,834,676 kB
on a 182.7 GB node.

Most of the 7.84 GB is the mmap'd 7.34 GB database, which is file-backed page
cache rather than anonymous memory (the node showed 126 GB in buff/cache and
168 GB available), so the Go heap is a fraction of a gigabyte. The figure to
compare it against is the old **140 GB live heap with a 218 GB GC goal** - above
physical RAM, which is why the kill was arithmetically certain.

## Also confirmed working in production

- **Publication after recovery.** The pid file appeared at 07:36, `client.token`
  and `manager.addr` at 07:37 - the token is now written at publication, so
  nothing outside could reach a half-recovered manager.
- **Default-level startup progress.** Every phase above is visible without
  `--debug`. This was the operator's original ask in `.docs/bugfixes/260825-1.md`,
  after a 15-minute recovery that logged nothing but client errors and read as
  total job loss.
- **`wr manager v0.37.1-77-gfb5df01 started on ...`** is logged *after*
  `prior state recovered`, which is the ordering the `fb5df01` gate now enforces.

## Remaining issues

### 1. Decode is now the entire startup cost

34.664 s of the 36.4 s window - 95%. It has nothing to do with dependencies any
more; it is reading and decoding 151,280 job records from a 7.3 GB DB.

This is the only lever left if faster startup is wanted, and it would mean a
batched or parallel decode. Note the measurements doc
(`.docs/dep-granularity/startup-window-measurements.md`) bounds the *other*
phases at synthetic scale but explicitly does not bound `initDB` or decode at
production scale - this run is the first production figure for decode.

**Recommendation: leave it.** 36 s is a perfectly good startup, and a parallel
decode touches the recovery path this work has just stabilised.

### 2. A thundering herd at publication

57 `add jobs=10` requests took 10.1 s to 15.7 s each. All of them *started* at
the publication instant (they are logged 07:37:12-07:37:17 with durations of
10-15 s), and the durations climb monotonically, which is queueing rather than
individual slowness.

This is a **direct consequence of the new design**, not a defect in it: clients
that would previously have trickled in and collected `ErrRecovering` now all
arrive at the same moment, because the manager becomes reachable atomically. They
had been waiting anyway, so nothing is lost, and 15.7 s is far under the 60 s
`ClientMinRequestTimeout` floor.

Worth watching rather than fixing: the herd scales with how many clients are
blocked during the window, and the window scales with the job count. If a future
recovery is slower, or more clients are waiting, this could approach the floor -
at which point clients start retrying and the churn dynamics of
`prod-restart-260825.md` become reachable again. `replyJobs=0` on all 57 suggests
they were duplicate adds, so the work itself was cheap; the cost was contention.

### 3. Continuous full-DB backup, causing the only other latency

**This is the one worth doing next.**

```
db_bk      7,283,757,056  07:42:33   (finished)
db_bk.tmp  3,675,652,096  07:43:32   (already half-written)
```

Back-to-back full 7.3 GB copies - roughly 80 MB/s to NFS, indefinitely, because
the dirty-flag ticker refires as soon as jobs keep completing. The two slow
`jarchive` requests in the log (11.2 s at 07:40:15, 13.7 s at 07:41:25) sit
inside that copy window.

This is the **unfixed "Part C copy-I/O relief"** recorded in the
backup-coordination work: `f51af04` stopped the backup *serialising* the archive
hot path, but the copy itself is still a raw `tx.WriteTo` of the whole file, and
archives pay latency while it runs.

Context for how bad it is now, against `prod-state-260824.md`: **220 slow
jarchives per 28 minutes, 20 of them past the 60 s floor**. This run: 2 in 5
minutes, worst 13.7 s, none near the floor. Most of that improvement is the
operator's compaction (15 GB -> 7.3 GB) rather than code. But it is now the top
remaining latency source, and unlike the old behaviour it is *perpetual* rather
than occasional.

A prototype already exists and was measured: incrementally fsyncing the backup
copy every ~32 MB took a 10 GB DB's freeze from 15.8 s to 0.7 s, size-independent
(`reliable4-backup-stall-reinvestigation`). That is the starting point, not a
blank page.

## Next steps, in order

1. **Tackle issue 3** - DB-backup copy I/O. Bounded, prototyped, and it is the
   only thing still costing production latency. See the section below.
2. **Watch issue 2** across a few more restarts. It needs no code today; what
   would change that is a herd that approaches the 60 s floor.
3. **Leave issue 1** unless startup time becomes a complaint.
4. Close out the remaining recorded items in `.docs/bugfixes/260825-1.md`
   (items 2-4), several of which became moot now that recovery completes before
   any client can connect.

## Things not to "fix"

- **`wr manager status` dies with "could not read token file" for the whole
  startup window on the daemonized path.** Operator decision, 2026-08-27: this is
  expected, not a bug - status is only expected to work once `wr manager start`
  says the manager has started. Making the pid-file branch consult the sidecar
  would undo the entire point of publishing late.
- The green window in `3390f23` is deliberate: ten test functions are red at that
  commit by design. Do not bisect through it expecting a green suite.

## wrdev.sh regression sweep

Running at the time of writing; results to be appended here. The sweep covers
every self-checking gate in `developers/wrdev.sh`, including the real-LSF modes
and the big-DB modes (fixtures at `/nfs/hgi/wr/sb10-bigdb/`), with any failure
A/B'd against a worktree at `a083f1d` - the commit immediately before this
delivery - before it is called a regression.
