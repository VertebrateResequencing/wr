# Phase 1 gate: A5 replay of the Phase 0 recording (item 1g.2)

Gate (spec.md, Implementation Order, Phase 1, Gates): the Phase 0 prodsim
recording replays against the log store with 0 mismatches, removed methods
mapped or skipped as A5 says, and the A2 suite passes with
`WR_TEST_STORE=all`.

Run on 2026-11-09 on farm22-wrstat01 (8 cores, 91GB), go1.27.1, HEAD
`3f1733c4` on `customdb-f397b547`, where the log store is the default. The
first replay against the log store failed with 534 mismatches. Both causes
were in the replay harness, not the log store: it compared two answers that
the stores give differently and that a server does not read. With the
harness fixed (uncommitted working tree on `3f1733c4`, described below),
the recording replays against the log store with 0 mismatches and every
gate passes. Review round 1 then made the harness note bbolt's live record
at every write that rewrites it, not only adds and modifies, and narrowed
the `moveJobChangeAhead` acceptance. Review round 2 added tests for the
noting code no test reached, and corrected spec A5's list of what the notes
miss; the replays and gates below were rerun on that final tree.

Trimmed outputs are in [phase1-gate-1g2/](phase1-gate-1g2/).

## The recording

```text
/nfs/hgi/wr/sb10-bigdb/customdb/recordings/phase0-prodsim-20261028b.rec
size   1,188,370,355 bytes
sha256 a107a2f33cfa3c40a6e696307ff0d431ff05c5b8f341b5375750ef4cc3c060b2
```

`sha256sum` gave the hash recorded in
[phase0-prodsim-recording.md](phase0-prodsim-recording.md) (6.8s). The
file was only read.

## Replay

Each replay used a fresh scratch `HOME` with every `OS_*` variable unset,
and its store in a local temporary directory:

```bash
(unset $(compgen -v | grep '^OS_'); export HOME=<scratch>; \
 /usr/bin/time -v env \
 WR_STORE_REPLAY=/nfs/hgi/wr/sb10-bigdb/customdb/recordings/phase0-prodsim-20261028b.rec \
 WR_TEST_STORE=<kind> go test -tags netgo -count=1 -timeout 0 -v -run '^TestStoreReplay$' ./jobqueue/)
```

`TestStoreReplay` replays through `replayRecordingFolding`, which flushes
the log store's folder before each learned-requirements probe (spec A5, D1
flush hook).

| | log, before the fix | log | bbolt |
| --- | --- | --- | --- |
| result | FAIL | PASS | PASS |
| calls | 400,863 | 400,863 | 400,863 |
| skipped | 1 | 1 | 1 |
| mismatches | 534 | 0 | 0 |
| test time | 329.2s | 331.7s | 110.6s |
| wall time (with build) | 6:20 | 5:40 | 1:53 |
| peak RSS | 1.55GB | 0.60GB | 0.63GB |

The log and bbolt replays are of the final tree, after review round 2
(`store_record.go` sha256 `198a45d6...`, `store_record_test.go`
`55219684...`, both rechecked after the replays), run one after the other
with nothing else of this worker's on the machine and with the package
already built in the scratch `HOME`, which may be why the log replay's
peak RSS is lower than the first column's. The sha256 of the recording was
checked again before them (6.4s).

Removed methods, as A5 says: the 27,766 `updateJobRunStateDurableWithin`
calls replay as `reserveRunState` then `WaitWithin` of the recorded wait,
with their digests not compared; the one `setBatchTuning` call is the 1
skipped call; the recording holds no `logReserveNotDurable` call. Every
other method replays against the method of the same name.

The learned-requirements reads (`cachedReqGroupRecs`,
`refreshReqGroupRecs`, `reqStatChanges`) had no mismatch in any run, with
the fold before each probe.

## The 534 mismatches

| method | mismatches | calls |
| --- | ---: | ---: |
| `prepareJobChange` | 526 | 603 |
| `storeNewJobsGuarded` | 8 | 1,205 |

The other 77 `prepareJobChange` calls overlapped a write in the
recording, which A5 does not compare; each of the other 1,197
`storeNewJobsGuarded` calls matched or overlapped a write.

### `prepareJobChange`

Spec A5 digests a handle result only by whether it is nil, and said the
first log-store replay would revisit that choice. Any store may return nil
from `prepareJobChange` (spec, The store interface), and
`queueJobChangeAhead` given nil records the change afresh. The log store
always returns nil, so every compared call where bbolt had returned a
handle mismatched. The answer is not a different one to a server.

Fix: replay accepts a nil change-ahead handle from `prepareJobChange`, or
from a `moveJobChangeAhead` that was given none, for a recorded handle
(`acceptNoAhead`). A `moveJobChangeAhead` given a handle that returns nil
still mismatches, since only `prepareJobChange` may return none
(`jobqueue/store.go`). A handle where the recording has none still
mismatches, as does a nil `*rerunGuard` or `expectJobExitUpdate` func.

### `storeNewJobsGuarded`

In each of the 8 calls the add's one live dependent was a running job,
the shape of `rundep.sh`, which adds a member to the dep group of a
running job (12 instances in the recording).
A probe in a scratch copy dumped that dependent from a bbolt replay and a
log replay ([dependents-probe.txt](phase1-gate-1g2/dependents-probe.txt)).
bbolt answers it as its live record holds it, as added: no state, attempts,
host, pid or start time, and the requirements it was added with. bbolt
keeps run state in a separate bucket. The log store answers it with its
latest state: running on `node-14-16`, with `Requirements.Time` 91s and
the 600s it was added with in `RequirementsOrig`. The `Dependencies`
and keys are the same.

A server reads only each dependent's key and `Dependencies`
(`updateLiveDependents`, `gatherDependencyUpdates` in
`jobqueue/running_dependent.go` and `server.go`); the comment there calls
them "the copies the add decoded from the database". Neither answer is
wrong. The recorded digest holds bbolt's whole copy, so the replay cannot
compare only the fields a server reads.

Fix: the replayer notes, by key, the recorded encoding bbolt's live record
holds, at each write bbolt makes to it (`noteLive`; spec A5 lists them):
an add's stored job unless bbolt keeps the existing record (handed out, or
under a run state written since), a modify, a full change
(`updateJobAfterChangeDurable`, `queueJobChangeAhead` with a recorded
handle), an exit, a running job's rerun mark, and, as bbolt's rerun
record, `storeLiveForRerun` and an archive that keeps the job live. For an
add that answers live dependents, it also accepts the recorded digest of
the replayed answer with each dependent replaced by that encoding, given
the replayed dependent's `Dependencies` (`acceptDependentsAsStored`).
The notes follow the recording's call order, not bbolt's commits, and are
not complete: A5 lists the known cases they miss or get wrong, such as
recovered jobs and writes bbolt skips. A dependent with no note is compared
only as replayed, and a wrong note gives a wrong alternative, so either
can only add a mismatch. Everything else in the
answer, including each dependent's key and `Dependencies`, still comes
from the replayed store. The probe found that this alternative equals the
recorded digest for all 48 adds in the recording that answered live
dependents, on both stores; on the log store the plain digest matched the
36 adds whose dependents were not running and not the 12 whose dependent
was (8 of them compared, 4 overlapping a write).

Neither fix changes product code. Spec A5 now describes both, in place of
the Phase 0 note that 1g.2 would revisit the handle and error choices. No
error text mismatched, so errors stay digested by their text.

### Tests

Test first, in `jobqueue/store_record_test.go`:

- `TestStoreRecordReplayAcrossStores` records the scripted A5 workload
  against bbolt and replays it against the log store. `recordGuardedAdd`
  now starts the guarded add's live dependent first, as `rundep.sh` does.
  Before the fix it failed with mismatches in `moveJobChangeAhead`,
  `prepareJobChange` and `storeNewJobsGuarded`; after it, none.
- The `flippedChangeAheadStore` leaf of `TestStoreRecordReplay` now
  requires a mismatch only where the store answers a handle and the
  recording has none (log-store recordings), and none where it answers nil
  for a recorded handle (bbolt recordings). Before the fix its bbolt run
  failed.
- A new leaf replays against `undependentStore`, whose add answers its live
  dependent without `Dependencies`, and requires the one mismatching method
  to be `storeNewJobsGuarded`, on both stores.
- Review round 1: a `nilMoveStore` leaf, whose `moveJobChangeAhead`
  drops the handle it was given, requires the first mismatch of a bbolt
  replay to be that call (red before the narrowing), and none on the log
  store.
- Review round 1: `TestStoreRecordReplayLiveDependents` records, against
  bbolt, an add reading a started live dependent after each write class
  spec A5 says is noted, and replays it against the log store with no
  mismatch. The classes: modified; added again (stored over); added again
  once reserved or once delayed (record kept); modified to another key and
  the old key added again; reserved, put back and added again; kept live by
  its archive and added again ignoring complete jobs (not stored); changed
  durably; changed ahead with a handle; exited; exited durably; put back to
  run again; kept live by its archive; marked to run again; and given to
  `storeRunningRerunMarks` unmarked (not written). Two more leaves: a
  change ahead with no handle (recorded through `flippedChangeAheadStore`,
  so bbolt writes nothing), and an add whose store also answers an error
  (`failingDependentsAddStore`). Against the harness before round 1, 10 of
  these 15 classes failed with a `storeNewJobsGuarded` mismatch: all but
  modified, added again, the two re-adds after a record was rewritten or
  removed, and the unmarked one. Those five, and the two extra leaves,
  guard against noting too much or too little in the new code, as the
  mutants below show.
- Review round 2: four more classes in
  `TestStoreRecordReplayLiveDependents`. Modified keeping its key (only
  `Priority` changed, as `wr mod` does when it leaves the command alone),
  which needs the modify's replay to forget the old key before it notes the
  new job. Reserved through `reserveRunState` (waited for) and added again,
  where bbolt keeps the record. Delayed, deleted and added again, where
  bbolt stores the re-add afresh because the delete removed the record.
  And a second started dependent with other `Dependencies`, so the add
  reads two dependents. A last leaf rewrites the `reserveRunState` call of
  the reserveRunState class's recording as the Phase 0
  `updateJobRunStateDurableWithin` (`asPhase0Within`), since no current
  recorder records that call, and replays it against the log store with
  no mismatch.

Mutants of the fix, each in a scratch copy, against
`WR_TEST_STORE=all go test -run TestStoreRecordReplay`:

| mutant | result |
| --- | --- |
| dependents keep the noted encoding's `Dependencies` | killed |
| `acceptDependentsAsStored` accepts any digest | killed |
| `acceptDependentsAsStored` not called | killed |
| `acceptNoAhead` accepts any digest | killed |
| `acceptNoAhead` not called for `prepareJobChange` | killed |
| `acceptNoAhead` without its non-nil early return | equivalent |

The last is equivalent: for a non-nil handle both accepted digests are the
"has a handle" digest, which is the plain comparison.

Review round 1 mutants, one at a time on a plain copy of the tree (no git
worktree), each run against `WR_TEST_STORE=all go test -run
'TestStoreRecord|TestStoreReplay'` on the final tests; a mutant that did
not compile was rewritten until it did:

| mutant | result |
| --- | --- |
| `modifyLiveJobs` replay notes nothing | killed |
| add notes jobs not in `jobsToQueue` | killed |
| add notes a job whose record bbolt keeps | killed |
| kept check ignores a run state written since | killed |
| kept check ignores a handed-out record | killed |
| alternative digest drops the add's error | killed |
| no note at `updateJobAfterChangeDurable` | killed |
| no note at `updateJobAfterExit` | killed |
| no note at `updateJobAfterExitDurable` | killed |
| `queueJobChangeAhead` notes with no recorded handle | killed |
| `queueJobChangeAhead` never notes | killed |
| rerun record keeps `RerunAfterRun` | killed |
| rerun record keeps `ReservedBy` | killed |
| `storeLiveForRerun` notes the raw job | killed |
| `storeLiveForRerun` notes nothing | killed |
| archive kept live notes nothing | killed |
| `storeRunningRerunMarks` notes unmarked jobs | killed |
| `storeRunningRerunMarks` notes nothing | killed |
| move given a handle accepts nil | killed |
| run states never noted | killed |
| modify keeps the old keys' notes | killed |
| a new note keeps the run-state flag | killed |
| archive removed live forgets nothing | killed |
| `acceptNoAhead` not called for `prepareJobChange` | killed |
| `acceptNoAhead` not called for `moveJobChangeAhead` | killed |
| dependents keep the noted encoding's `Dependencies` | killed |
| run state flagged for an unnoted key | equivalent |
| forgetting a key keeps its run-state flag | equivalent |

26 of 28 killed; the 2 survivors are equivalent. A run-state flag counts
only while its key has a noted record (`liveRecordKept` checks that
first), and noting a record clears the flag, so a flag set or left on an
unnoted key never changes an answer; it only holds memory.

Review round 2 named 14 mutants (K01-K14; K08 did not compile and is not
counted), five of which survived the round-1 tests. On the final tree, in
a plain copy, with the same command, followed by a rerun of every round-1
mutant above (the final rewrites of the two that had not compiled):

| mutant | round 2 | final |
| --- | --- | --- |
| K01 no run-state note in the `updateJobRunStateDurableWithin` mapping | survived | killed |
| K02 `modifyLiveJobs` replay notes before it forgets | survived | killed |
| K03 `deleteLiveJobs` replay keeps its notes | survived | killed |
| K04 archive kept live notes the raw job | killed | killed |
| K05 a dependent with no note uses the replayed copy | survived | survived, not a defect |
| K06 every dependent gets the first one's `Dependencies` | survived | killed |
| K07 alternative digest drops the add's error | killed | killed |
| K09 run-state flag set false | killed | killed |
| K10 no run-state note in the `reserveRunState` replay | survived | killed |
| K11 no run-state note in `updateJobRunStateDurable` | killed | killed |
| K12 add notes over a kept record | killed | killed |
| K13 move acceptance keyed on the recorded handle | killed | killed |
| K14 modify notes only jobs whose key changed | survived | killed |

The round-1 mutants: 26 of 28 killed, the same two equivalent survivors.
K05 is not a defect: the alternative it builds uses, for a dependent with
no note, the replayed dependent itself, which is what the plain digest
already compares, so it accepts a recorded answer only if each dependent
equals either its replayed copy or its noted record, with the replayed key
and `Dependencies` in both; it never accepts a wrong key, `Dependencies` or
rest of the answer.

Only the gate replay and the `asPhase0Within` leaf reach the
`updateJobRunStateDurableWithin` mapping; the committed removed-methods
fixture reads no dependents.

## A2 suite and quality gates

A2's suite is the tests that run once per store kind through `forEachStore`
(15 files in `jobqueue/`). It ran inside `WR_TEST_STORE=all make test`.
Every gate ran on the fixed tree with a fresh scratch `HOME` and every
`OS_*` variable unset, as
`(unset $(compgen -v | grep '^OS_'); HOME=<scratch> make <target>)`:

| gate | result |
| --- | --- |
| `make lint` | 0 issues |
| `make test` | PASSED, 1266 passed, 26 skipped, 7m11s |
| `WR_TEST_STORE=all make test` | PASSED, 1266 passed, 26 skipped, 6m38s |
| `make race` | PASSED, 1266 passed, 25 skipped, 10m4s |

These are the runs on the final tree after review round 2. `make lint`
also gave 0 issues with a warm scratch `HOME` and with a copy of the
user's `~/.cache/golangci-lint`; `store_convert.go` now shares the G703
exclusion of the other store files (incidentals, 1g.2 review round 2).

## Files

- [replay-log-before-fix.txt](phase1-gate-1g2/replay-log-before-fix.txt):
  the failing log-store replay, its mismatches counted by method, the 8
  `storeNewJobsGuarded` seqs and `time -v`.
- [replay-log.txt](phase1-gate-1g2/replay-log.txt),
  [replay-bolt.txt](phase1-gate-1g2/replay-bolt.txt): the passing replays.
  Each holds the `go test -v` lines with the store's log lines dropped, the
  dropped lines counted by message, and `time -v`.
- [dependents-probe.txt](phase1-gate-1g2/dependents-probe.txt): the probe
  that found the dependents' difference and checked the fix's alternative
  digest against every add with live dependents.
