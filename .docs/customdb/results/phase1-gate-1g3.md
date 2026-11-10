# Phase 1 gate: crash soak at production LSF concurrency (item 1g.3)

Gate (spec.md, Implementation Order, Phase 1, Gates; phase1-gate.md item
1g.3): the crash soak with a staged ramp (battery10 shape, about 6,000
runners, at least 3h, injected stalls and crashes, `relbury.sh` batches at
the injected kills), without `FIXTURE`. It passes when `soakgate.py`'s
`missing` line has `absent=0`, `relburycheck.py` prints `problems: 0`,
`soakgate.py --source wait` prints `nondurable source=wait inside=0
outside=0` with an `abandoned` line ending `ok` and `stalls` at least 1,
and the `doubles` line has `outside=0`. `wr status -l 1` is also timed just
after each injected restart, and any time over 60s fails the gate.

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

Trimmed outputs are in [phase1-gate-1g3/](phase1-gate-1g3/).

## Method

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

## Soak criteria

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

## Scale

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

## `wr status` just after each restart

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

### Cause of B's times

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

## Other observations

- Three clean stops (1794269140, 1794275973, 1794278082) logged
  `aliveAtReturn=y`, so `wrdev.sh` killed the manager after `wr manager stop`
  reported success. This is not new: battery10 had 4 of 5 clean stops like
  that, and nohold-g3b 5 of 5.
- `dbstart` ran for 17 minutes. It folded the stopped store's backlog,
  logging `db writer writer=fold` lines with a mean hold of 3.7-5.2s per
  transaction on NFS.

## Tooling fix: prodsim with `WRDEV_PRODSIM_DBDIR` and the log store

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

## Files

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
