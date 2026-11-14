# Phase 1 gate: wrdev.sh scenarios (item 1g.6)

Gate ([phase1-gate.md](../phases/phase1-gate.md) item 1g.6): run wrdev
`crash-recovery` and every other `wrdev.sh` scenario, and all must pass.
Also resolve the 1g.3 incidental on scenarios that `rm -f` what are now
log store directories ([incidentals.md](../phases/incidentals.md)).

All runs were on Saturday 2026-11-14 (GMT), from farm22-wrstat01 (8 cores,
91GB, shared), on LSF queue `normal`, with HEAD `8b7ccade`
(`customdb-f397b547`, the log store the default) and this item's
uncommitted changes to `developers/wrdev.sh`, `developers/soak/sweep.sh`
and one test fixture (`jobqueue/reliable4_history_seed_test.go`), none of
which is in the `wr` binary. In sweeps s1 and s2 the binary was
8b7ccade's own. From s3 on (s3, r1, rev2fix, rev3fix and rev4fix) it
also had the conversion progress fix in `jobqueue/store_convert.go` and
`jobqueue/db.go` (checklist item 8); from rev3fix it also has a test hook
per counted job, which is nil outside tests. The work directory was
`/nfs/hgi/wr/sb10-bigdb/customdb/1g6/`.
Trimmed logs, the drivers and the comparison runs are in
[phase1-gate-1g6/](phase1-gate-1g6/).

**Verdict: PASS**, after the fixes below. Every scenario passes at its
default settings. Three failed first, none of them a product defect:

- control-rpc-history: its history seed wrote complete records under
  keys that are not job keys, which the log store's conversion
  correctly refuses. The seed now writes job keys.
- unsuspend-burst: its manager was still converting the bbolt fixture to
  the log store (7 minutes) when the mode's 90s start timeout ended.
  backup-stall-check and report-storm-lsf had the same timeout and
  passed only because they ran after the fix.
- add-storm-lsf: add-storm-fixture now produces a log store directory,
  which add-storm-lsf and the sweep could not use.

Review round 1 found five more defects, fixed and rerun in sweep s3 and
the r1 runs ([Review round 1](#review-round-1)): two starts still on a
short timeout while converting, a fixture copy that follows no symlink,
a backup cleanup that deletes prefix siblings, and two scenarios that
could pass without testing their claim (unsuspend-burst's only verdict
is bbolt-only; crash-recovery could kill before its command ran). It
also routed one product defect: the first start on a big bbolt file
showed no progress for most of its conversion, against spec E1. That is
fixed in `jobqueue` with a test.

Review round 2 found the conversion progress tests too weak, the
crash-recovery wait and its failure message imprecise, and rows not
rerun on the final code; [Fix round 2](#fix-round-2) answers each, and
the nine affected scenarios pass again on the final code (rev2fix).
Review round 3 found the sidecar untested while counting and two
defects in crash-recovery's cleanup and host match;
[Fix round 3](#fix-round-3) answers them, and crash-recovery and
add-storm-lsf pass again on the final code (rev3fix).
Review round 4 found a surviving mutant that rewrites the sidecar per
job counted, named the `TestStoreOpenLocked` failure as a test race,
and split a mixed incidental; [Fix round 4](#fix-round-4) answers them,
and add-storm-lsf passes again on the final code (rev4fix).

The fixes are recorded in
[bugfixes/261114-wrdev-scenarios-a9b41fdc.md](../../bugfixes/261114-wrdev-scenarios-a9b41fdc.md).
The `rm -f` incidental is resolved. The sweep found further issues that
do not fail a scenario; they are entries in
[incidentals.md](../phases/incidentals.md).

## Method

`developers/soak/sweep.sh` runs every `wrdev.sh` mode in turn, each with
its documented fixture and default arguments, and records each mode's exit
status, wall time, load and free space
([sweep-driver.sh](phase1-gate-1g6/sweep-driver.sh) wraps it). As in
earlier gate items, `HOME` was an isolated directory under the work
directory with `.ssh` linked in, so `~/.wr_config*.yml` could not point
wr elsewhere (`assert_isolated` refuses a CA file outside the run
directory). The Go cache was on NFS under the work directory. Ports were
53480 to 53483. The big fixtures were the compacted bbolt copies in
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/` (pristine10, pristine6,
prod.db), which the modes only copy. The scenarios ran one at a time:

- **s1** (01:28 to 05:05): every mode once, in the sweep's order. Another
  user's NFS use dropped `/nfs/hgi` to 39GB free at 01:48. The sweep's
  40GB disk guard then stopped web-burst and ended the sweep. It was
  resumed at 01:50 from control-rpc-history (after its fix) and
  web-burst, with the guard lowered to 30GB. The disk recovered to 145GB
  within two minutes.
- **s2** (05:05 to 05:27): after the tooling fixes, add-storm-fixture,
  add-storm-lsf, unsuspend-burst and crash-recovery again, in a fresh
  root.
- **s3** (05:58 to 06:56), after review round 1: build,
  control-rpc-history, add-storm-fixture, add-storm-lsf,
  add-storm-lsf-fix120k (on the bbolt fixture below), unsuspend-burst,
  backup-stall-check, report-storm-lsf and crash-recovery, in a fresh
  root, with the round 1 fixes and the conversion progress fix in the
  binary. Logs are `phase1-gate-1g6/s3/`.
- **r1** (06:57 to 07:31, a fresh root): unsuspend-burst on a symlink
  to a converted store directory, unsuspend-burst with its status bound
  lowered to 50ms, crash-recovery with a 1s start wait and three times
  more (`03`, `04-*`, with the second version of its start wait), and a
  6-minute prodsim with its backup elsewhere. After crash-recovery's
  start wait was reworked a third time, it ran again with a 1s wait and
  four times more (`06`, `07-*`). Logs are `phase1-gate-1g6/r1/`, with
  the drivers, the fix120k build log and the scratch demonstrations for
  checklist items 3, 4 and 5.
- **rev2fix** (08:11 to 08:41 and 09:00 to 09:36, root
  `rev2fix/root`, ports 53484 to 53487), after fix round 2, on the
  final code: wrdev.sh md5
  `8a9f12f09fc9`, binary md5 `1f8bdb845aa7` built from 8b7ccade plus
  this item's changes. A first start converting a copy of pristine10,
  then dep-granularity-check, control-rpc-history, crash-recovery twice
  and once with a 1s wait, unsuspend-burst on the pristine10 bbolt
  fixture, and add-storm-lsf on s3's directory fixture; then, with the
  same binary, add-storm-fixture on pristine6, add-storm-lsf on the
  fix120k bbolt fixture, backup-stall-check and report-storm-lsf on
  pristine10. Logs are
  `phase1-gate-1g6/rev2fix/` with the driver
  ([rev2fix-drive.sh](phase1-gate-1g6/rev2fix/rev2fix-drive.sh)).
- **rev3fix** (09:59 to 10:08, a fresh root `rev3fix/root`, ports
  53488 to 53491), after fix round 3, on the final code: wrdev.sh md5
  `0a74c63b35e7`, binary md5 `4ea1f24558f6` built from 8b7ccade plus
  this item's changes. crash-recovery twice and once with a 1s wait,
  then add-storm-lsf on s3's directory fixture. Logs are
  `phase1-gate-1g6/rev3fix/` with the driver
  ([rev3fix-drive.sh](phase1-gate-1g6/rev3fix/rev3fix-drive.sh)).
- **Extra runs:** crash-recovery three more times
  ([cr-repeat.out](phase1-gate-1g6/cr-repeat.out)); unsuspend-burst on a
  converted log store directory fixture
  ([unsuspend-burst-storedir.out](phase1-gate-1g6/unsuspend-burst-storedir.out));
  flicker-check with the Playwright browser fixtures, which the isolated
  `HOME` hid from s1
  ([flicker-playwright.out](phase1-gate-1g6/flicker-playwright.out));
  compact-fixture on a small bbolt file; and control-rpc-history on
  d1a4b6d7.

s1 and s2 left out one sweep mode, `add-storm-lsf-fix120k`: the
sweep's second add-storm-lsf run, on a 120,000-job fixture. The existing
fixture's jobs run in a directory baked into it, under
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/fix120k.db.jobcwd`, where
add-storm-lsf writes `wradd.sh` and an owner file, outside this item's
NFS area. s3 replaced it: d1a4b6d7's own wrdev.sh (bbolt was that release's
default store) built a new 120,000-job fixture from a copy of pristine6,
under `1g6/fix120k/` with its job directory there
([fix120k-build.out](phase1-gate-1g6/r1/fix120k-build.out)), so it is a
bbolt file, as the sweep's fix120k fixture is. s3 ran add-storm-lsf on
it with this item's code, converting it inside the mode's first start.
No mode was skipped in s3.

## Scenarios

Kinds: **local** means in process or a manager on this host with no LSF
job; **LSF** means real LSF runners. Times are wall seconds. Raw logs are
`phase1-gate-1g6/s1/<n>-<mode>.out` and `phase1-gate-1g6/s2/<n>-<mode>.out`.

Which run backs each row's verdict on the final code:

- **rev4fix** (the final wrdev.sh, md5 `da20b00abcfe`, and binary, md5
  `4ea1f24558f6`): add-storm-lsf, whose cleanup's state regex fix round
  4 changed. Round 5's review reran crash-recovery on the same wrdev.sh
  and binary (PASS, and a 1s start wait exits 1 as not measured); its
  logs are in `/nfs/hgi/wr/sb10-bigdb/customdb/1g6/rev5check/`.
- **rev3fix** (wrdev.sh md5 `0a74c63b35e7`, binary md5
  `4ea1f24558f6`): crash-recovery and add-storm-lsf, the two modes
  whose wrdev.sh code fix round 3 changed (crash-recovery's start wait
  and exits, and add-storm-lsf's cleanup, `asl_cleanup`). Fix round 3
  changed the binary only by a test hook called per counted job, nil
  outside tests, so the rev2fix rows below stand for the final binary.
- **rev2fix** (the final wrdev.sh, md5 `8a9f12f09fc9`, and binary, md5
  `1f8bdb845aa7`): dep-granularity-check, control-rpc-history,
  crash-recovery, unsuspend-burst (on the bbolt fixture, conversion
  included), add-storm-lsf (on a directory fixture), add-storm-fixture,
  add-storm-lsf-fix120k (on the 120,000-job bbolt fixture),
  backup-stall-check and report-storm-lsf (on the pristine10 bbolt
  fixture, conversion included); crash-recovery and add-storm-lsf were
  rerun in rev3fix. These are every row whose wrdev.sh
  code changed after s1 or that starts on a bbolt file, so the
  conversion's counting progress reaches it (dep-granularity-check
  converts one at its start, and ran only in s1 before).
- **s1** (8b7ccade's binary): every other row. Item 8 changed only how
  a conversion reports progress, and none of these modes starts on a
  bbolt file. Of their wrdev.sh code, only prodsim's cleanup changed
  after s1 (item 5's backup-name fix), and r1's 6-minute prodsim ran
  that cleanup.

rev2fix results (logs `phase1-gate-1g6/rev2fix/`, drivers
[rev2fix-drive.sh](phase1-gate-1g6/rev2fix/rev2fix-drive.sh) and
[rev2fix-drive2.sh](phase1-gate-1g6/rev2fix/rev2fix-drive2.sh)):
add-storm-fixture PASS (20000 incomplete, 0 commands of a foreign form,
3,169,042,448 bytes); add-storm-lsf-fix120k PASS (5696/5696 acknowledged
adds present after kill -9 and restart, p50 46ms, p99 111ms, max 726ms;
81,319 of the 120,000 recovered jobs completed, 0 buried; 664s with the
conversion); backup-stall-check PASS (8000/8000 drained, maxDelayed 0,
badjob delta 0, max status 125ms); report-storm-lsf PASS (100000
complete, badjob 0, lost 0, delayed 0, max status 701ms);
dep-granularity-check PASS (peak RSS 272MB, recovery
172ms, add 346ms); control-rpc-history PASS (limit 40ms, suspend 216ms,
resume -z 208ms, status -z --limit 1 1161ms; ungated count-only warm-up
31,036ms); crash-recovery PASS twice, and FAIL (not measured) with a 1s
wait; unsuspend-burst PASS (max status RPC 191ms, 0 failed calls,
100000/100000 ready; 575s with the conversion); add-storm-lsf PASS
(5605/5605 acknowledged adds present after kill -9 and restart, p50
42ms, p99 82ms).

| scenario | kind | pass criterion (as the script defines it) | result | key output | s |
| --- | --- | --- | --- | --- | --- |
| flicker-check | local (node + browser) | every harness scenario coherent and convergent; browser fixtures pass | PASS | harness all coherent; repgroup-bar-flicker and status-count-reconcile browser fixtures PASS | 16 |
| status-seed-overlap | local | seed and live deltas count each transition once (forced and natural interleavings) | PASS | both tests pass | 60 |
| overprovision-check | local | summed runners per limit group <= limit | PASS | TestReliable3LimitGroupOverProvision passes | 45 |
| overcount-check | local | uncapped > limit and 0 < final <= limit | PASS | uncapped 3500, final 2000 | 30 |
| limit-stall-check | local | held group schedules 0; both warnings name what to act on | PASS | scheduled 0, skipped 5000/5000; key facet measured | 15 |
| priority-fairness-check | local | high.count == limit, low.count == 0 | PASS | high 2000, low 0 | 15 |
| backlog-rescan-check | local | rac scan bounded, not O(backlog) | PASS | TestReliable4BacklogRescan passes | 15 |
| bkill-hygiene | local (fake bkill) | argv <= 1000 ids, all covered, no re-kill, hung bkill bounded | PASS | maxArgv 1000, 1900/1900 ids | 90 |
| runner-started-timeout-check | local | a transient Started() failure does not kill the command | PASS | test passes | 15 |
| ttrmiss-check | local | late successes accepted, none rejected | PASS | 60/60 accepted, 0 rejected | 15 |
| confirm-dead-leak | LSF host + ssh | no leaked ssh-client goroutines | PASS | test passes (ran, not skipped) | 45 |
| report-storm | local | every report RPC accepted, none bad-job or timed out | PASS | 5000/5000 started and archived; archive max 57ms | 15 |
| report-storm-profile | local | as report-storm, with profiles | PASS | 50000/50000; archive max 202ms | 30 |
| remap-stall-check | local (bbolt) | a read never waits for the backup copy | PASS | test passes | 30 |
| freelist-check | local (bbolt) | commit at 256Ki free pages < 10x empty; tests pass | PASS | all four tests pass | 45 |
| selfconnect-check | local | port tests pass, including the 60s TIME_WAIT one | PASS | all three pass | 90 |
| idle-backlog-cpu | local | CPU near the empty-backlog floor; rac cycles measured | PASS | 670ms per 25s (2.68% of a core) | 60 |
| control-rpc-history | local (prod-mode) | each control command <= 5000ms, RSS growth bounded, history big enough | **FAIL**, then PASS after the seed fix; PASS in s3 | limit 28ms, suspend 194ms, resume -z 207ms, status -z --limit 1 1294ms (68MB); s3: 35, 207, 196, 1290ms (8MB). Ungated warm-up `status -z -o counts`: 31022ms (s3 31744ms) | 150 |
| dep-granularity-check | local (prod-mode) | RSS, recovery and add under thresholds; status answers in the window | PASS | peak RSS 262MB, recovery 171ms, add 298ms | 30 |
| exec-impossible-retries | local | buried == N, attempts == N, right cause | PASS | 20/20 buried, 20 attempts | 15 |
| transient-start-retries | local | buried == N, attempts == N*(R+1) | PASS | 20/20 buried, 60 attempts | 180 |
| runner-log-bytes | local | runner <= 4096 B/job, manager <= 8192 B/job, no Cmd sentinel | PASS | 1415 B/job | 15 |
| retention-check | local | forced-GC heap flat over rounds | PASS | 26MB to 27MB over 6 rounds | 90 |
| web-burst | local | web feed converges to the true total | PASS (second run; the first was stopped by the disk guard) | web complete 10000 for `echo` and `+all+` | 150 |
| backup-stall-fast | local (bbolt) | no archive over the TTR | PASS | 49,759 archives, max 731ms | 255 |
| writestorm-freeze | local (bbolt) | archive under the 60s floor | PASS | max 6.659s | 45 |
| archive-rate | local (bbolt) | mean <= 5000ms, p99 <= 60000ms, none over the floor | PASS | 170.2/s, mean 88ms, p99 1210ms | 210 |
| archive-ceiling | local (bbolt) | factor >= 10x, none over 30000ms or the floor | PASS | 43.92x for 57.1x, worst 1654ms | 420 |
| add-storm | local (bbolt) | overSlow 0, p50 <= 5000ms, factor >= 10, txns/add <= 1 | PASS | 23.41x, p50 346ms, max 4948ms, 0.17 txns/add | 361 |
| add-storm-fixture | local (fixture builder) | fixture written, audited, stamped | PASS (s1 wrote an unusable stamp; s2 after the fix; s3; rev2fix) | 20000 incomplete, 0 unsafe; size 3,169,058,421 (s3 3,169,051,926) | 345 |
| add-storm-lsf | LSF | no acknowledged add lost, none over 10s, p50 <= 5000ms, every phase measured | **SKIPPED** in s1 (unusable fixture), PASS in s2, s3, rev2fix, rev3fix and rev4fix | 5750/5750 acknowledged adds present after kill -9 and restart; 20000/20000 recovered jobs and 1000 self-added jobs ran; p50 41ms, p99 82ms, max 178ms. s3: 5813/5813, p50 41ms, max 194ms. rev3fix: 5533/5533, p50 47ms, p99 212ms, max 609ms; 0 jobs left in LSF at cleanup. rev4fix: 5510/5510, p50 55ms, p99 271ms; 0 jobs left in LSF | 331 |
| add-storm-lsf-fix120k | LSF | as add-storm-lsf | not run in s1 or s2 (see Method); PASS in s3 and rev2fix on a 120,000-job bbolt fixture | conversion inside the first start 5m12s; 5785/5785 acknowledged adds present; p50 46ms, p99 102ms, max 163ms; 91,191 of the 120,000 recovered jobs and 1,783 of 2,393 self-added jobs completed in the 120s window | 645 |
| unsuspend-burst | local (prod-mode) | write storm bounded (bw > 5000 fails; bbolt only, 0 on the log store) and, from s3, every status RPC < 60000ms with every call answered and all N jobs un-suspended | **FAIL** in s1 (start timeout), PASS in s2, on a store directory, and in s3 and r1 under the new gate; FAIL with the bound at 50ms; PASS in rev2fix on the bbolt fixture | peak bw 0, max status RPC 206ms (178ms on the directory fixture); s3 170ms, 0 failed calls, 100000/100000 ready | 570 |
| churn | LSF | 40000 jobs fully drain | PASS | 40000/40000 terminal, badjob 0 | 120 |
| monitor | LSF (helper of churn) | fully drained | PASS | 40000/40000 | 15 |
| limit-drain | LSF | 60000 jobs in one limit group fully drain | PASS | 60000 complete, 0 buried, lost 0, badjob 0 | 976 |
| backup-stall-check | LSF | drains with no delayed jobs and no bad-job growth | PASS (after the start-timeout fix; s3; rev2fix) | maxDelayed 0, badjob delta 0, max status 153ms (s3 103ms) | 585 |
| report-storm-lsf | LSF | fully drains | PASS (after the start-timeout fix; s3; rev2fix) | 100000 complete; badjob 0, lost 0, delayed 0; max status 1244ms (s3 1777ms) | 510 |
| crash-recovery | LSF | after a manager kill and restart, complete=1 and the command ran once; from s3, the kill only once the command has started under a runner in RUN | PASS (5 of 5), but 2 of them (s1, s2) killed before the command ran; s3: FAIL (not measured) with the first version of the fix; r1: PASS 4 of 4 with the final one, and FAIL (not measured) with a 1s wait; rev2fix: PASS 2 of 2, FAIL (not measured) with a 1s wait; rev3fix: the same | see below | 45 |
| prodsim | LSF soak | not a gate: exits 0 unless it could not run; read report.txt | ran (rc 0) | see below | 7285 |

Helpers, run by the sweep and all exit 0: build, status, start, stop,
probe, prod-start (local), prod-stop, dump (39 goroutines in the SIGQUIT
dump), clean. reap-runners and job-token are helpers too; the sweep
calls job-token for its preflight. compact-fixture, a fixture builder,
was run by hand on a 103MB bbolt copy of the dep-granularity fixture: it
compacted it to 80MB and exited 0.

### crash-recovery

Only the three repeats tested what the mode claims. In s1 the mode's
`bjobs` lookup ran before LSF showed the runner in RUN, so it recorded
no job ID and killed the manager before the command had written its
marker (`marker=0`). In s2 it found the runner (jobid 221716), but the
marker was still 0 at the kill, with a `cr_count: No such file` error
from reading the absent marker. Both passed: after the restart the job
completed and the marker held one line. The mode waited only for the
manager's `running` count, which a job reaches when it is reserved,
before its command runs, so these kills tested a reserve, not the
re-sent archive of a running command. The three repeats killed with
`marker=1` and a jobid, and passed. The same early kill happened on the
6fcbf4ed release sweep (marker 0 there too).

That a scenario can pass without exercising its path is a defect, fixed
in checklist item 7. The first version of the fix waited for the marker
and then a RUN jobid. In s3 it saw the marker but never the runner in
RUN (the runner had already finished), and exited 1 as not measured;
its r1 reruns passed, seeing the marker 62-122s after the add. The
final version waits for the manager to report the command's pid, which
the runner's Started call sets as the command starts, and for the LSF
job on that host in RUN, then kills; the command sleeps 60s, not 30s.
In r1 the command started 63-66s after the add in each of four runs,
each killed with `marker=1` and a RUN jobid, and each printed
`PASS: re-sent archive accepted (complete=1), command ran exactly once`
([07-crash-recovery-1.out](phase1-gate-1g6/r1/07-crash-recovery-1.out)).
With `WRDEV_CR_START_WAIT=1` it exits 1 as not measured
([06-crash-recovery-nowait.out](phase1-gate-1g6/r1/06-crash-recovery-nowait.out)),
but not for the reason its message gave. That run's runner, LSF job
239337 on node-14-22, was dispatched within seconds of the add (the
other runs waited about 64s), and the manager already reported the
command's pid on node-14-22 at the second poll, t+2s, with the marker
at 1. The one `bjobs` lookup made then did not list a job of the
prefix in RUN on that host, the 1s wait had passed, and the mode
failed with a message saying the manager had not reported the command
started. So the run shows that a missing RUN lookup fails the mode,
not that a short wait fails before the command starts. Fix round 2
separates the two messages (no pid reported, or a pid but no RUN
runner on its host) and kills only when `wr status` also shows
`Status: running`, since it prints Host and Pid for complete and lost
jobs too (see [Fix round 2](#fix-round-2)).

Fix round 3 matches the reported host exactly in `bjobs`' `exec_host`
(a substring match let `node-5-1` match `node-5-12`) and stops the
manager before bkilling the prefix's jobs. On that code it passed twice
in rev3fix, killing at t+4s (`marker=0`) and t+66s (`marker=1`), each
with a RUN jobid on the reported host, and exited 1 as not measured
with a 1s wait
([01-crash-recovery-1.out](phase1-gate-1g6/rev3fix/01-crash-recovery-1.out),
[-2](phase1-gate-1g6/rev3fix/01-crash-recovery-2.out),
[02-crash-recovery-nowait.out](phase1-gate-1g6/rev3fix/02-crash-recovery-nowait.out)).

### prodsim

Two hours at scale 1 on LSF with no restarts
([s1/prodsim-report.txt](phase1-gate-1g6/s1/prodsim-report.txt)). No
call was slow (none reached the 2m client timeout). The only errors are
the three ops that fail by design, as in the 6fcbf4ed release sweep:
`operator/kill_fofn` and `operator/retry_portal` (exit 1, nothing to act
on) and `wrstatui/add_0` (an empty add, bad request). Peak manager RSS
was 2272MB against 1895MB in the release sweep, and peak heap in use
1129MB against 826MB.

RSS steps up at each of prodsim's 40,000-job submission bursts and is
flat between them ([prodsim-rss.txt](phase1-gate-1g6/r1/prodsim-rss.txt),
from `samples.tsv` and the heap profiles in
`s1/root/prodsim-1794625360/` under the work directory). The bursts
landed at t=0, between t=3710s and 3740s (about 62m) and between t=6675s
and 6705s (about 111m). RSS was 520-565MB after the first and 998-1052MB
after the second. The live heap did not grow between bursts: the heap
profiles' in-use space was 75.2MB at t=1235s, 59.6MB at t=4980s and
60.6MB at t=5918s (`go tool pprof -base` of 001235 against 005918:
-14.6MB). The release run stepped the same way (529-617MB after its
first burst, 1048-1162MB after its second apart from one 1422MB sample)
but had no third burst in its 2h. So the report's second-half RSS slope,
+580MB/h here against -222MB/h, compares a window holding a burst with
one that does not; the two runs agree at the same stage, and nothing
here shows a leak.

The run does not explain the 0.9GB by which RSS exceeds the heap in use
after the second burst, in either run: heap the runtime holds idle, or
file-backed pages such as the mmap of `history.bolt`. Sampling
`/proc/<pid>/smaps_rollup` and the runtime's `HeapIdle` and
`HeapReleased` would settle it (new incidental). `db_mb` reads 0
throughout, because prodsim stats `db` as a file (new incidental).

## Failures and fixes

### control-rpc-history

Red (s1, 01:40): the prod-mode manager did not start. Reproduced by hand
with the mode's seed and `wrdev.sh prod-start local`:

```text
wr manager failed to start : bbolt database .../db, left as it was: the
bbolt database holds a record that cannot be converted: jobscomplete key:
malformed log store record: key "hsrg0-0" is not 32 hex digits
```

The seed (`seedHistoryRepGroup`) keyed records `<repGroup>-<i>`. bbolt
never checked; the conversion refuses a key that is not a job key, by
design and under test. d1a4b6d7's own wrdev.sh and seed pass the mode
([ab-d1a4b6d7-control-rpc-history.out](phase1-gate-1g6/ab-d1a4b6d7-control-rpc-history.out)):
limit 29ms, suspend 177ms, resume -z 190ms, status -z --limit 1 1062ms.
The seed now keys each record with `byteKey` of the old name. The seeded
history converts (200,000 jobs in 1m14.8s), and the rerun passed with
figures close to d1a4b6d7's (table above).

One figure the mode prints but does not gate is far from d1a4b6d7's: its
count-only warm-up, `wr status -i hsrg -z -o counts` over the 200,000
complete jobs, took 31,022ms (31,744ms in s3) against 434ms, about 71x.
That is 52% of the 60s client floor, about 29s under it. It belongs to
the 1g.3 incidental on history reads touching whole records (proposed
owner 2b), where it is recorded: the log store `Get`s every record from
`history.bolt` to count it. The review asked for the start timeout as
well: the seed's conversion took 1m14.8s against `prod-start`'s 90s
default, so the mode now starts with the big-DB timeout (checklist
item 2).

### unsuspend-burst, backup-stall-check, report-storm-lsf

Red (s1, 02:22): unsuspend-burst's add found no manager (`could not read
token file`) because the manager was still converting pristine10. The
mode had started it with `timeout 90`. Timing the conversion alone, on
the same fixture: 7m03s, of which 5m47s passed at "counting the jobs to
convert" with no further log line, then 3,000,000 jobs converted in
1m15s ([pristine10-convert.log](phase1-gate-1g6/pristine10-convert.log)).
backup-stall-check's conversion took 6m29s. The three modes that start
on a copied big bbolt fixture now wait up to `WRDEV_BIGDB_START_TIMEOUT`
(3600s), and accept a converted log store directory as the fixture.
backup-stall-check and report-storm-lsf ran after this change in s1 and
passed. unsuspend-burst passed in s2 on the bbolt fixture and again on
the converted directory.

### add-storm-fixture and add-storm-lsf

The generator's manager converts its bbolt base, so the fixture it moves
out is a log store directory. Its manifest stamped the directory's own
size (146 bytes), and the sweep skipped add-storm-lsf because its fixture
was not a file. `asl_db_stamp` now stamps a directory by its files'
summed size and newest mtime. add-storm-lsf accepts and copies a
directory, the generator refuses to `mv` into an existing fixture
directory, and sweep.sh tests `-e`. In s2 the fixture was rebuilt and
add-storm-lsf passed (table above).

## The `rm -f` incidental

Every line the incidental names now runs `rm -rf` on `"${pr:?}/db"`,
`"${pr:?}/db".*` and `"${pr:?}/db_bk"*`. `asl_cleanup` and prodsim's
cleanup use `$PROD_RUN/...`, and `WRDEV_PROD_BKFILE` cleanup uses
`"${WRDEV_PROD_BKFILE:?}" "${WRDEV_PROD_BKFILE:?}".*` (item 5). The
`db.*` glob is also needed, as the sweep showed. A conversion keeps the
original bbolt file as `db.bolt-pre-<version>` beside the store (4.3GB
for pristine10). A leftover one fills the disk, and it makes the next
run's conversion fail with `errBoltPreExists`. After s1 and s2 no
`db.bolt-pre-*` or `db_bk` was left in either root's
`.wr-prod_production`.

The dangling-symlink incidental that follows it trips no scenario. Only
prodsim with `WRDEV_PRODSIM_DBDIR` (which `developers/soak/run.sh` sets)
links `$PROD_RUN/db`, and since 1g.3 it makes the target directory
first. The sweep's prodsim does not set it.

## Review round 1

The review's findings and what answered each, with the checklist item
that records it:

- control-rpc-history started on the 90s `prod-start` default while
  converting its seed (1m14.8s): it now uses the big-DB timeout (item
  2). s3 passed.
- unsuspend-burst's only verdict, `peakbw > 5000`, is 0 by construction
  on the log store: a store-neutral gate (status RPCs under the 60s
  client floor, every call answered, all jobs un-suspended) was added,
  passed in s3 and r1, and failed with the bound at 50ms (item 6).
- `cp -rf` copied a symlinked fixture as a symlink: now `cp -RLf`,
  shown red and green in scratch and on a symlinked store in r1 (item
  4).
- add-storm-lsf on a bbolt fixture was untested: s3 ran it on a new
  120,000-job bbolt fixture, whose conversion took 5m12s inside the
  first start, past the old 300s start timeout; it now uses the big-DB
  timeout (item 2). See Method for what replaced fix120k.
- crash-recovery s2 killed at `marker=0`: corrected above; the mode
  could pass without exercising its path, now fixed (item 7).
- prodsim RSS: re-read as per-burst steps with a flat live heap (see
  prodsim above); the unexplained 0.9GB is a new incidental.
- control-rpc-history's ungated count-only warm-up took 31,022ms against
  434ms on d1a4b6d7: recorded above and in the 1g.3 history-reads
  incidental.
- The conversion's silent counting broke spec E1's "with progress
  shown": fixed in `jobqueue/store_convert.go` with
  `TestStoreConvertReportsCountingProgress` (item 8). On the final
  binary, a first start on a copy of pristine10 logged `counting the
  jobs to convert (200000 counted)` 30.1s into counting, then every 30s
  to 2770000 counted at 5m2.4s
  ([rev2fix/01-convert-manager.log](phase1-gate-1g6/rev2fix/01-convert-manager.log)).
  An earlier figure from s3 had no preserved log and is withdrawn.
- `rm -rf "${WRDEV_PROD_BKFILE:?}"*` deleted prefix siblings: now the
  name and `name.*` (item 5).

## Fix round 2

Review round 2 failed the item on six findings. What answered each,
with the checklist item that records it:

- `TestStoreLongConvertReportsProgress` passed on counting's "still
  running" lines, so a conversion with no converting progress in the
  log (mutant M8) survived: it now asserts only on `converting
  database` lines (item 8).
- The counting detail's count was unchecked (mutant M3): the counting
  test now asserts `counting the jobs to convert (20000 counted)`, and
  its fixture adds orphaned run-state records so progress reported
  again during that walk (mutant M7) fails too. All eight of the
  review's mutants are KILLED
  ([rev2fix/mutants.out](phase1-gate-1g6/rev2fix/mutants.out); item
  8).
- The real-binary counting figure had no preserved log and two
  versions: replaced by the rev2fix conversion's redacted manager log
  and sidecar samples (item 8 and [Review round 1](#review-round-1)).
- Not every row ran on the final code: rev2fix reran the nine
  scenarios the later changes touch, and the note under
  [Scenarios](#scenarios) says which run backs each row.
- crash-recovery's FAIL message conflated two causes, and r1's 1s-wait
  run was misexplained: the messages are separate, and
  [crash-recovery](#crash-recovery) gives that run's real cause (item
  7).
- crash-recovery could kill on the Host and Pid of a complete or lost
  job: it now needs `Status: running` in the same output (item 7).

The review's cheaper incidentals were fixed too: sweep.sh removes its
own previous add-storm-fixture output before that mode reruns (item
3), `countJobs` checks `progressDue` before formatting its detail (item
8), crash-recovery bkills every job of its isolated prefix on each exit
(item 7), and dep-granularity-check's cleanup no longer runs `rm -f`
on what may be a directory before its `rm -rf "${PROD_RUN:?}"`. The
empty `db/` directory an unsuspend-burst cleanup left is a new entry in
[incidentals.md](../phases/incidentals.md).

## Fix round 3

Review round 3 failed the item on seven findings. What answered each:

- The sidecar was not tested while counting (mutants N1 and N2
  survived): `countJobs` now calls a test hook per counted job, and
  `TestStoreConvertSidecarShowsCounting` reads the sidecar there,
  asserting the state, Processed and the count in the detail (item 8).
- `setPhaseTotal` without `r.phaseDetail = detail` (N10) survives
  because it is equivalent: the sidecar write uses the `detail`
  argument, and a probe shows `phaseDetail` is never read after the
  total is set ([rev3fix/n10-probe.txt](phase1-gate-1g6/rev3fix/n10-probe.txt);
  item 8). The new test still asserts a `converting database` detail
  once the total is known.
- Counting's 2s time pacing was untested (N8): the new test sleeps
  `dbUpgradeProgressInterval` after the first job counted and requires
  the sidecar to show the second (item 8).
- Stale `"${WRDEV_PROD_BKFILE:?}"*` in this doc and incidentals.md: now
  the code's `"${WRDEV_PROD_BKFILE:?}" "${WRDEV_PROD_BKFILE:?}".*`.
- This doc said no change was in the `wr` binary: that now holds only
  for s1 and s2.
- Item 8 said counting reports through `dbUpgradeReporter.progress`:
  it calls `progressDue` and `writeProgress` directly.
- crash-recovery bkilled before stopping its manager, and matched the
  host as a substring of `exec_host`: both fixed, and rerun twice on the
  farm (item 7; [crash-recovery](#crash-recovery)).

All of round 2's mutants M1-M8 and round 3's N1-N10 were rerun on the
final code ([rev3fix/mutants.out](phase1-gate-1g6/rev3fix/mutants.out)):
all KILLED except N3, a defect no fixture reaches (a nested bucket
counted as a job, new incidental below), and N10, equivalent.
`asl_cleanup`'s "jobs left in LSF" count included finished DONE, EXIT
and ZOMBI jobs; it now counts only PEND, RUN and SUSP states, and
rev3fix's add-storm-lsf printed 0.

## Fix round 4

Review round 4 failed the item on three findings. What answered each:

- Mutant R1, `jobCounted` writing the sidecar for every job counted
  (`progressDue(n)` changed to `active()`), survived: 3,000,000 file
  publishes over NFS for pristine10.
  `TestStoreConvertSidecarShowsCounting` now reads the sidecar at the
  first job counted, before its sleep, and requires the start phase
  there (Detail `counting the jobs to convert`, Processed 0). R1 and R7
  survived the round 3 tests
  ([rev4fix/mutants-r1-red.out](phase1-gate-1g6/rev4fix/mutants-r1-red.out))
  and are KILLED now. All of M1-M8, N1-N10 and R1-R11 were rerun on the
  final code
  ([rev4fix/mutants.out](phase1-gate-1g6/rev4fix/mutants.out)): 26 of
  29 valid mutants KILLED (R6 after a rerun, as the full run's version
  did not compile). N3 is the known defect no fixture reaches (2d.1),
  and N10 and R3 (the hook's error swallowed; the hook is nil outside
  tests) are equivalent (checklist item 8).
- `TestStoreOpenLocked`'s fix round 3 failure is a test race, not
  unattributed. The holder's folder makes its first pass
  `foldInterval` (1s) after open and commits the wal file the open
  started to `history.bolt`; the test's snapshots around the second,
  bounded open (up to 500ms) straddle that pass when slowed, as under
  `make race`. A 700ms sleep after the first snapshot fails the "with no
  backup" case every time with the reported shape, and the first case
  the same way. Both cases now flush the holder (pending writes done,
  log folded to the end) before the first snapshot; with 700ms or 3s
  sleeps injected the test passes, as do 50 runs under `-race`
  ([rev4fix/openlocked-red-green.txt](phase1-gate-1g6/rev4fix/openlocked-red-green.txt);
  checklist item 9).
- The `asl_cleanup` incidental was ticked while it held the open
  `prodsim_cleanup` follow-up: they are now two entries, the second
  open for 2e.1.

The review's incidentals:

- `TestStoreConvertShowsStartBeforeCounting` did not check the start
  Detail (R7): one assertion, added.
- crash-recovery's `cr_bkill_ours` runs straight after `safe_kill`'s
  `kill -9`, which does not wait: added to the `safe_kill` entry in
  [incidentals.md](../phases/incidentals.md) (2e.1).
- `asl_cleanup`'s count left out PROV, WAIT and UNKWN: added, as the
  count only feeds its printed line. add-storm-lsf ran again on that
  code in a fresh root (wrdev.sh md5 `da20b00abcfe`, binary md5
  `4ea1f24558f6`, the same binary as rev3fix's, as round 4 changed only
  tests) and passed: 5,510/5,510 acknowledged adds present after the
  kill and restart, p50 55ms, p99 271ms, and its cleanup printed 0 jobs
  left in LSF
  ([rev4fix/01-add-storm-lsf.out](phase1-gate-1g6/rev4fix/01-add-storm-lsf.out),
  [rev4fix/rev4fix.console](phase1-gate-1g6/rev4fix/rev4fix.console)).
  The console's jobqueue diff md5 changed during the run because
  `cleanorder` reordered `store_lock_test.go` then; no product file
  changed.

## New incidentals

Recorded in [incidentals.md](../phases/incidentals.md):

- prodsim's `db_mb` and `dbbk_mb` read 0 for the log store, as do
  `restarts.tsv`'s `db_mb`, and its `freelistAtOpen` probe is
  bbolt-only (proposed owner 2e.1).
- prodsim's manager RSS stays about 0.9GB above its heap in use after
  the second submission burst, unexplained (proposed owner 2e.1).
- Seven scenarios (backup-stall-fast, writestorm-freeze, archive-rate,
  archive-ceiling, add-storm, remap-stall-check, freelist-check) drive
  bbolt's `db` in process. They pass, but no longer measure the default
  store's hot path, and unsuspend-burst's `bw` signal is bbolt-only
  (proposed owner 2e.1).
- The 1g.3 history-reads entry gains the count-only warm-up figure
  (proposed owner 2b).
- A `wrdev.sh` cleanup can leave an empty `db/` directory, probably
  because `safe_kill` does not wait for the manager to exit before the
  `rm -rf` (proposed owner 2e.1).
- No conversion fixture has a nested bucket under `jobslive` or
  `jobscomplete`, so `countJobs` counting one as a job (mutant N3) goes
  undetected (proposed owner 2d.1).
- `prodsim_cleanup` counts finished jobs as left in LSF, as
  `asl_cleanup` did before fix round 3, and polls for up to 2 minutes
  for that count to reach 0 (proposed owner 2e.1).
- `TestStoreOpenLocked` failed once under `make race`. Fix round 4
  named the cause, a test race with the holder's first fold, and fixed
  the test (checklist item 9), so the entry is closed.

The entry on the first start's silent conversion, first recorded here
for 2d.1, is closed: it was fixed as checklist item 8.

## Checks on the changes

- `bash -n developers/wrdev.sh developers/soak/sweep.sh`: OK. shellcheck
  is not installed on this host.
- `make lint`: 0 issues. `make test`: 1298 passed, 26 skipped, PASSED.
  `make race`: 1298 passed, 25 skipped, PASSED. Both after the product
  fix (item 8), with an empty scratch `HOME` and no `OS_` variables.
- After fix round 2, on the final code: `bash -n` of both scripts OK;
  `make lint` 0 issues; `make test` 1298 passed, 26 skipped, PASSED;
  `make race` 1298 passed, 25 skipped, PASSED; `go vet -tags
  reliability_repro ./jobqueue/` OK.
- After fix round 3, on the final code: `bash -n` of both scripts OK;
  `make lint` 0 issues; `make test` 1299 passed, 26 skipped, PASSED;
  `go vet -tags reliability_repro ./jobqueue/` OK. The first `make race`
  failed one case of `TestStoreOpenLocked`, which this item does not
  touch: the holder's own `history.bolt` changed between the test's
  snapshots. A rerun passed (1299 passed, 25 skipped), and 30 runs of
  that test under `-race`, side by side on the final code and on
  8b7ccade, all passed. Recorded in
  [incidentals.md](../phases/incidentals.md) unattributed; fix round 4
  found and fixed the cause, a test race.
- After fix round 4, on the final code: `bash -n` of both scripts OK;
  `make lint` 0 issues; `make test` 1299 passed, 26 skipped, PASSED;
  `make race` 1299 passed, 25 skipped, PASSED, at the first attempt;
  `go vet -tags 'netgo reliability_repro' ./jobqueue/` OK;
  `TestStoreOpenLocked` 50 runs under `-race`, all passed.
- `go vet -tags reliability_repro ./jobqueue/`: OK. The seed fix's test
  file builds only with that tag, so `make test` and `make race` do not
  compile it.
- Each changed path in wrdev.sh ran end to end in this item:
  backup-stall-check, report-storm-lsf and unsuspend-burst on a bbolt
  fixture (s3); unsuspend-burst on a directory fixture and on a symlink
  to one (r1); control-rpc-history (s3); add-storm-fixture then
  add-storm-lsf on its directory fixture, and add-storm-lsf on a bbolt
  fixture (s3); crash-recovery (r1); and the cleanups of all of them.
  prodsim's cleanup ran in s1 and, with `WRDEV_PROD_BKFILE` set, in r1.
  No `db.bolt-pre-*` was left in the s3 or r1 roots'
  `.wr-prod_production`. The `db` and `db_bk` there (under 200KB each)
  are the last crash-recovery's, which keeps its run directory and
  removes it at its next start, as before this item.
