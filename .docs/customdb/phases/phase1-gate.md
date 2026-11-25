# Phase 1 gate: crash, replay, soak, speed and NFS targets

Ref: [spec.md](../spec.md) sections A4, A5, A3, C3, F4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.
Implementors and reviewers follow `testing-principles` for test work, and
reviewers apply `go-test-strength` to the tests each item adds.

Brief every subagent with these project constraints:

- Run one subagent at a time. Items run sequentially; there are no
  parallel batches, because of the user's rate limits.
- Work only on branch `customdb-f397b547` in the main clone. Do not
  switch branches or create worktrees for this work; `go-test-strength`
  mutants go in a scratch copy as `agent-conduct` describes.
- Commit messages carry no attribution lines: no `Co-Authored-By` and no
  `Generated with` trailers.
- Kill only exact PIDs that the agent started and verified. Never use
  `pkill -f` or any pattern kill, and guard empty variables in cleanup.
- Run `make test` and `make race` with every `OS_*` variable unset and an
  empty scratch `HOME` (DEVELOPERS.md, "Fast test suite"), for example:

  ```bash
  (unset $(compgen -v | grep '^OS_'); HOME=$(mktemp -d -p <scratchpad>) make test)
  ```

- Put NFS work and measurements under `/nfs/hgi/wr/sb10-bigdb/customdb/`
  (`.docs/customdb/README.md`, "Where to run on NFS").
- This sub-phase is complete when every item is reviewed and `make test`,
  `make race` and `make lint` pass.
- The items below are this phase's gates. Run them after its
  sub-phases, one at a time.

## Items

Run these after 1a to 1f are all reviewed, one at a time. Each needs the
LSF farm, NFS or hours of wall time, so the orchestrator schedules them
with the user. Each item's implementor runs the gate and records its
evidence (commands, commit, outputs) in the branch's docs; its reviewer
checks that evidence against the spec's criteria. A failing gate is a
defect to route through the `bugfix` skill, not a reason to relax the
gate.

### Item 1g.1: A4 on the log store, and fuzzing

spec.md section: Implementation Order, Phase 1, Gates; A4

Run A4 against the log store: 16 rounds local and 16 on NFS
(`WR_CRASHTEST_ROUNDS=16`, with `WR_CRASHTEST_DIR` under the NFS work
directory for the NFS run), plus 20 damaged copies each, with 0 missing.
This is the first run of A4 acceptance test 2 (damaged copies), deferred
from item 0d.1 ([phase0d.md](phase0d.md)); acceptance tests 1, 3 and 4 run
against the log store too. Run `FuzzScan` and `FuzzFlatSpec` for 10
minutes each with no crasher.

- [x] implemented
- [x] reviewed

### Item 1g.2: A5 replay of the Phase 0 recording

spec.md section: Implementation Order, Phase 1, Gates; A5

Replay the Phase 0 prodsim recording (item 0d.3; its path, shape and
first bbolt replay are in
[results/phase0-prodsim-recording.md](../results/phase0-prodsim-recording.md))
against the log store with 0 mismatches, removed methods mapped or
skipped as A5 says, and pass the A2 suite with `WR_TEST_STORE=all`. The
replay folds (the D1 flush hook) before each learned-requirements probe,
as A5 says; 1e.5 added this as `replayRecordingFolding`
(`jobqueue/store_record_test.go`), which `TestStoreReplay` uses. Those
forced folds hide fold-liveness bugs (a folder that stalls or never folds
without the hook), so D1's tests must cover fold liveness; this replay
cannot.

- [x] implemented
- [x] reviewed

### Item 1g.3: Crash soak at production LSF concurrency

spec.md section: Implementation Order, Phase 1, Gates

Run the crash soak with a staged ramp (battery10 shape, about 6,000
runners, at least 3h, injected stalls and crashes, `relbury.sh` batches at
the injected kills as in the soak README), without `FIXTURE`. Pass when
`soakgate.py`'s `missing` line has `absent=0`, `relburycheck.py` prints
`problems: 0`, `soakgate.py --source wait` prints `nondurable source=wait
inside=0 outside=0` with an `abandoned` line ending `ok` and `stalls` at
least 1, and the `doubles` line has `outside=0`. Also time
`wr status -l 1` just after each injected restart, while the restarted
manager still has a large unfolded wal backlog, and report the times: the
log store decodes each unfolded history entry in full on that path (the
1f.11 incidental owned by 2b in [incidentals.md](incidentals.md)), and any
time over 60s, the client request timeout floor, fails the gate. Also
time and report `wr status -i <rep group> -z --limit 1` after each
restart, but do not fail this gate on it: its cost grows with the number
of finished jobs on bbolt and the log store alike, and the owner ruled on
2026-11-18 that item 2b.3 makes it independent of history size. Never
replace the NFS wr binary while the soak's runners are live.

- [ ] implemented
- [ ] reviewed

### Item 1g.4: make speed-full

spec.md section: Implementation Order, Phase 1, Gates

Run `make speed-full` against `d1a4b6d7`: no regression above 10%.

Owner ruling, 2026-11-18: a plain percentile of reserve waits taken
during a background operation (such as `AddManyUnderReserveLoad`'s
reserve p99) misreports a faster operation as a regression, because a
shorter window samples fewer reserves and the same few slow ones become a
larger share. Before judging this gate, make `developers/speed.sh` judge
those benchmarks by a deterministic, window-aware rule instead of the
raw percentile delta. The rule regresses only if, compared with the
base, the background operation slows, or it delays more reserves per
operation, or its longest queue-lock hold grows, or the delayed reserves
wait longer, or (added by the orchestrator after review, 2026-11-18, as a
stricter check) the median reserve wait (a median over thousands of
samples per operation, which a shorter window does not skew the way it
skews the tail) grows, each beyond the 10% bound or the benchmark's
measured noise. Report the raw tail percentiles alongside it. Test the rule
on recorded base and head outputs, including the 1g.4 runs in
[results/phase1-gate-1g4.md](../results/phase1-gate-1g4.md), then rerun
`make speed-full` with it.

Orchestrator change, 2026-11-23, made under the owner's direction of
that date that speed-gate criteria are the orchestrator's to fix: "the
delayed reserves wait longer" compares, per operation, the total wait of
the reserves over 1ms and the count of reserves over each of 1.25, 1.5,
2, 2.5, 3, 4, 5, 6, 8, 10, 12, 16, 20, 24, 32, 40, 48 and 64ms, not the
median wait of the reserves over 1ms. A faster operation removes the
short lock waits that set the median, so the few slow reserves left are
rarer, longer stalls base also has and their median rises with no
reserve worse off (bugfix 261119 item D: across its runs, Kick's and
Resume's median slow wait rose by up to 48% while their total fell
86.35 to 98.83%). The total rises when the slow reserves wait longer at
an equal count, but a fall in their count to 1/k lets each wait up to k
times as long first. The counts cannot be raised by removing reserves
and rise when reserves wait longer, so they catch fewer reserves each
waiting much longer. Each count compares head's reserves over 1.1 times
the threshold with base's over it, so a head whose every wait is at most
10% longer cannot fail one; compared at the same threshold, an add whose
hold of about 16ms grows 5% nearly doubled the count over 16ms. What
goes unflagged: a wait that lengthens to just under 1.1 times the next
threshold up while its count stays within 10% of base's over the one
below and the total falls (1.375 times from just over 1ms, 1.32 times
from just over 1.25, 2.5, 5, 10, 20 or 40ms, 1.375 times from just over
2, 4, 8, 16 or 32ms, 1.47 times from just over 1.5, 3, 6, 12, 24 or
48ms, and without limit past 64ms); sparse long stalls, which 6 rounds
rarely make significant; stalls at a constant rate per second, which a
faster operation spans fewer of (a fixed-length observation window would
close this); and reserves still waiting when the operation ends, which
the harness drops (DEVELOPERS.md, "Benchmarks under reserve load"). The
median is reported with the raw tail percentiles as informational.

Orchestrator change, 2026-11-24, made under the owner's direction of
2026-11-23 that speed-gate criteria are the orchestrator's to fix
("redefine them if they are saying that things that are better are
slower, but don't let real slowdowns that could be improved through"):
`make speed` builds each tree's benchmark binaries in 6 code layouts (the
tree as it is, and with 1 to 5 generated empty Test functions sorted
before the package's own test files, added through a build overlay so no
tree is written to), runs both trees in the same layout each round, and
compares the pooled rounds; the default rounds rise from 6 to 12, two per
layout. Bugfix 261119 item I found `BenchmarkReadyBacklogSnapshot` 7 to
11% slower than d1a4b6d7 from a commit that changed only test code: the
product functions compiled to the same instructions, adding 1 to 4 empty
Test functions moved it from +8.67% to -0.6%, +6.86%, +0.3% and -1.4%,
and each binary was stable across rounds, so more rounds of one binary
could not remove it. Over 36 rounds a tree, each tree's six layouts
spanned about 15%, and pooled they read -1.3% (p=0.810). Pooling widens
each tree's spread, so 6 rounds detected a head scaled 15% slower no
better than one fixed layout did (77% against 72% of resampled gates);
12 rounds detect it in 87%. On the host, a head given an injected
slowdown of about 10 to 16% (varying by run) passed all three 6-round
runs and failed 5 of 6 12-round runs, the pass reading +7.87%; a miss is
expected when a run measures the slowdown under or near the 10%
threshold (DEVELOPERS.md, "Code layouts").

Orchestrator change, 2026-11-25, made under the same direction of
2026-11-23: before the measured window opens, the big-DB scenarios
(`archive-rate`, `archive-ceiling`, `add-storm`) fsync their copy of the
database and sync the filesystem holding it, on both trees; max-ms stays
gated. Bugfix 261119 item M found `archive-rate`'s max-ms about 35% higher
on head than on `d1a4b6d7` with the archive path unchanged. The worst
archive was one bolt commit stalled behind writeback of the unsynced 4GB
copy and of the test binary `go test` had just linked into the same
`/tmp`, which is 31% larger on head. With both synced, max-ms read 9 to
35ms on either tree in windows without writeback from other processes,
and 90 to 161ms in windows where another process on the host wrote back
about 1GB. `developers/speed.sh` applies the change to a base it builds
from a ref, and DEVELOPERS.md's recipe to a prepared base. In a 4-round
interleaved A/B with the change on both trees, head's max-ms was not
significantly higher (p=0.886). By default (`SPEED_SCENARIO_COUNT=1`) a
scenario runs once a tree, so the judge cannot find its max-ms
significant; a large move in it is checked with an interleaved A/B, as
item M was.

- [x] implemented
- [ ] reviewed

### Item 1g.5: Daytime NFS targets

spec.md section: Implementation Order, Phase 1, Gates; A3; C3

Measured apart from the crash soak, both runs wholly between 10:00 and
17:00 on one weekday with the work directory on NFS: a stall-free soak
(`run.sh` with `SOAK_ROOT` on NFS, `USE_FUSE=0`, `HOURS=1`,
`RESTART_MIN=120`, no injectors) where every `"reservation wait"` line has
`p99` at most 50ms and `abandoned=0`, with no averaging across lines; and
`TestAddLatency1000` with `WR_ADDBENCH_DIR` under the NFS work directory
and `WR_TEST_STORE=log`, with 1,000-job add p50 at most 200ms in each of 3
runs. Also resolve the 1d.2 incidental on the unpaced checkpoint write
and reserve p99 while a checkpoint runs, measured while a checkpoint of a
production-sized live set (a `.ckpt` of 40MB or more) is written, not
only the small ones a test-scale soak makes
([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1g.6: wrdev.sh scenarios

spec.md section: Implementation Order, Phase 1, Gates

Run wrdev `crash-recovery` and every other `wrdev.sh` scenario; all pass.
Also resolve the 1g.3 incidental on `wrdev.sh` scenarios that `rm -f`
what are now store directories ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed
