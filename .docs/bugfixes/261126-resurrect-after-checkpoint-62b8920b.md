# Bugfix: archived dependents not resurrected after checkpoint and restart

- Branch: customdb-f397b547
- Base: develop (work started at d7799727)
- Queue owner: customdb-f397b547, .docs/delivery-queue.md row 20
- Source: 2b.1 review round 1 (incidental in
  .docs/customdb/phases/incidentals.md, owner "bugfix on customdb-f397b547
  after 2b.1")

- [x] Resurrection of archived dependents is lost after a checkpoint and
  restart. Add member M (DepGroups ["grow"]) and dependent D (depends on dep
  group "grow"), archive both, fold, checkpoint, restart, then add M2 in
  "grow": D is not in `jobsToQueue`. Without the checkpoint (reopen only, or
  fold then reopen) D is resurrected, as the bbolt store does. Likely cause:
  checkpoints carry only live specs, so archived keys drop out of the
  in-memory `reverseDeps`, while bbolt persisted `reverseDepgroupToKey`.
  - Red command: a throwaway test with three Conveys (reopen; fold+reopen;
    fold+checkpoint+reopen), run as
    `go test ./jobqueue/ -run TestZZResurrectAfterCheckpointRed -count=1`
    with OS_* unset and a scratch HOME. Exit: FAIL.

    ```text
    Failures:
      * jobqueue/zz_resurrect_red_test.go
      Line 41:
      Expected the container ([]string) to contain: 'echo dep' (but it didn't)!
    33 total assertions
    --- FAIL: TestZZResurrectAfterCheckpointRed (0.06s)
    ```

    The reopen and fold+reopen Conveys pass; only fold+checkpoint+reopen
    fails.
  - Root cause (confirmed): the reverse dep group index (`logState.reverseDeps`)
    holds a completed, not-live key only while the log or a checkpoint holds
    its spec record, and a checkpoint copies only live specs (plus, in its
    backlog, spec copies of unfolded completions, which recovery skipped).
    `history.bolt` has bbolt's `reverseDepgroupToKey` bucket, and a
    conversion fills it, but the folder never wrote it and nothing read it.
    So after a checkpoint and a restart, an add of a member found no
    completed dependents. Two more paths had the same defect: a checkpoint
    taken while the folder trailed (the dependent's completion still in the
    backlog), and every store converted from bbolt (converted with
    archived dependents, the first add of a new member put none back).
    Plain key (essence) dependencies are not resurrected on either store,
    so they have no such path; transitive dep group dependents were lost
    with the first level.
  - Fix (Phase 1 history persists the postings, as bbolt does):
    `jobqueue/store_history_bolt.go` `putReverseDeps` (a fold puts a
    `reverseDepgroupToKey` posting per completion it records under each of
    its job's dep group dependencies, unless a modify of its key later in
    the same fold dropped them: `historyLookupChanges.dropSeqs`);
    `dropAllLookups`/`dropReverseDeps` (a modify's fold deletes its old
    key's postings, reading the bucket once per fold holding a modify, as
    `dropPendingLookups` does); `foldedReverseDeps` and
    `jobqueue/store_log.go` `reverseDepKeys` (the add's dependent scan
    unions the in-memory index with the folded postings, skipping keys a
    modify not yet folded dropped, `unlooked`, copied before the read);
    `jobqueue/store_checkpoint.go` `decodeCheckpointBlock` and
    `jobqueue/store_backlog.go` `applyBacklog`/`applyBacklogModify`
    (recovery indexes a backlog spec copy's dep groups, and an applied
    backlog modify takes its old key out of the index but for its later
    live spec); `jobqueue/store_records.go` `logState.index` (shared with
    `applySpec`). No record or checkpoint format change. For 2b.2 the
    segment footers' reverse dep group sections replace the
    `history.bolt` read (incidentals entry "from the 261126 resurrection
    bugfix"; spec D2 amended).
  - Spec: `.docs/customdb/spec.md` was silent on Phase 1 writing and
    reading `reverseDepgroupToKey`, and said spec copies apply to
    nothing; amended in Checkpoints step 3, Folder and history (Phase 1
    and the dependent scan) and D2 (reverse dep group postings valid as
    rglookup postings are).
  - Tests: `TestStoreArchivedDependentsAfterRestart`
    (`jobqueue/store_log_ops_test.go`): three scenarios (member, dep group
    dependent, its transitive dependent and a key dependent; a dependent
    put back and modified to a new key, folded before the modify or not; a
    dependent put back and modified keeping its key) each on bbolt and on
    a log store reopened, folded and reopened, folded, checkpointed and
    reopened, checkpointed with the folder paused and reopened, and that
    then folded; every log store must queue and update what bbolt does.
    `TestStoreConvertKeepsArchivedDependents`
    (`jobqueue/store_convert_test.go`): the same after a conversion.
    Red before the fix: 9 log-store failures and the conversion failure
    (`Expected: [... dependent, ... new member, ... transitive] Actual:
    [... new member]`); green after. `TestLogStoreReverseDepsAfterSpecChange`
    takes `reverseDepKeys`' new error. Mutants killed: no spec copy
    indexing; no postings drop on modify; no `dropSeqs` check; no
    `unlooked` filter; no backlog modify unindex; no live re-index after
    a backlog modify.
  - Speed: interleaved A/B against an export of 2f4d5e28
    (`SPEED_BASE=<export> SPEED_BENCH='^BenchmarkAddDepGroupMember$'
    SPEED_SCENARIOS=none SPEED_COUNT=6 SPEED_LAYOUTS=1 developers/speed.sh
    quick`): 308.5ms ± 9% base v 302.5ms ± 4% head (p=0.699), B/op and
    allocs/op unchanged; PASS. A modify's fold scan (throwaway measure,
    local disk): `dropReverseDeps` over 1,048,576 postings 114-149ms, once
    per fold holding any modify, in the folder's own write transaction.
    Recovery decodes one more spec per spec copy, bounded by the fold lag.
  - Gates (every OS_ var unset, scratch HOME, 2026-11-26, uncommitted on
    2f4d5e28): `make lint` 0 issues; `make race` PASSED; `make test` first
    run failed only `TestClientAddAndWaitCancelledDuringOutage` (a client
    outage timing test that reaches no dependent scan); it passed 5 of 5
    in isolation on both base and head interleaved, and the full
    `make test` rerun PASSED (1385 passed). Not a load-matched A/B of the
    full suite, so not attributed to anything; noted for the caller.
  - Review round 1 FAIL, fixed in round 2: a fold made again after a
    failure lost a key's reverse dep group postings when one unfolded range
    held a modify of the key and then a completion of it, the transaction
    recording that completion committed and a later one failed. Every
    attempt's first transaction drops the key's postings, and the retry
    found the completion already folded (`written == 0`) and returned
    before `putReverseDeps`. Fix: `jobqueue/store_history_bolt.go`
    `recordCompletion` puts the postings unless `droppedAt > c.seq`,
    whether or not the completion was already folded (a posting put again
    is a no-op), and puts samples only when it was not. Rest of the fold
    checked for the same shape: the first transaction's other drops
    (rglookups, pending rglookups) are put again unconditionally by every
    attempt; samples are not dropped; the last transaction's rep groups,
    fail stats and meta run once per attempt. Only `reverse.pending`
    over-counts a retried put, which brings the next merge forward and
    changes no answer.
  - Round 2 tests: `TestStoreFoldRetryKeepsReverseDeps`
    (`jobqueue/store_log_ops_test.go`): a dependent of grow modified
    keeping its key and archived with 40 more jobs, its key first in key
    order, folded at 1 byte a transaction with the second transaction
    failing, then folded again, checkpointed and reopened; an add of a new
    member must plan what bbolt does. Red before the fix (`Expected:
    {queued: ["echo archived deps new member", "echo fold retry dependent
    65"]} Actual: {queued: ["echo archived deps new member"]}`), green
    after. `TestLogStoreFoldModifyAfterCompletion`: a fold of a completion
    and a modify of its key at the very next seq puts no postings, and one
    with the modify first does. No store write puts a modify straight after
    a completion (an archive's complete record is always followed by its
    delete or state record), so the test folds crafted records. Mutants
    killed: `droppedAt > c.seq+1` (boundary test: `Actual: ["echo archived
    deps dependent", "echo archived deps new member"]`); the round 1 early
    return on `written == 0` (retry test); `putJobStats` regardless of
    `written` (`TestStoreFoldRefoldAddsNoSample`, `Expected: 2 Actual: 4`).
  - Round 2 docs: spec D2's footer section of dropped keys covers reverse
    dep group postings; E6's merge applies in-run drops to the reverse dep
    group postings it merges; the 2b.2 incidental notes the segment writer
    needs the equivalent of `dropSeqs` within one segment.
  - Round 2 gates (every OS_ var unset, scratch HOME, 2026-11-26,
    uncommitted on 2f4d5e28): `make lint` 0 issues; `make test` PASSED
    (1387 passed); `make race` PASSED (1387 passed). A first `make test`
    failed `TestBoltOnlyTests`: the new tests used `logKeyOf` and
    `setFoldTxBytes` from bbolt-only test files; they now use their own
    helper and set `logFoldTxBytes` directly.
  - Review round 2 FAIL (test strength), fixed in round 3: code unchanged.
    A mutant keeping the first modify's seq in `dropSeqs` instead of the
    last survived. New Convey in `TestLogStoreFoldModifyAfterCompletion`
    (`jobqueue/store_log_ops_test.go`): one fold of a modify of the
    dependent's key, its completion, then another modify of the key, at
    seq, seq+1 and seq+2, puts no postings, so an add of a new member of
    grow does not put the dependent back (as bbolt's later modify drops
    them). Mutant red (`Expected: ["echo archived deps new member"]
    Actual: ["echo archived deps dependent", "echo archived deps new
    member"]`), real code green. The retry test now registers each
    `Reset` before the global it undoes is set. Spec lines in the segment
    layout (D2) and compact (E6) paragraphs rewrapped to 80 columns.
  - Round 3 gates (every OS_ var unset, scratch HOME, 2026-11-27,
    uncommitted on 2f4d5e28): `make lint` 0 issues; `make test` PASSED
    (1387 passed); `make race` PASSED (1387 passed).
    `TestClientAddAndWaitCancelledDuringOutage` failed in neither.
  - Review round 3 FAIL (test strength), fixed in round 4: code unchanged.
    Two surviving mutants were reachable defects. M11 (`if l.dropped` to
    `if true` in `newHistoryLookupChanges`) lets an rglookup's seq into
    `dropSeqs`; new scenario in `TestStoreArchivedDependentsAfterRestart`
    (`jobqueue/store_log_ops_test.go`): the dependent archived, put back
    live, looked up under rep group "zzother" and removed from the live
    plane, then the add of a new member of grow, on bbolt and every log
    store restart. Mutant red (`Expected: {queued: ["echo archived deps
    dependent", "echo archived deps new member"]} Actual: {queued: ["echo
    archived deps new member"]}`), real code green. M15 (the `droppedAt >
    c.seq` return above the `putJobStats` block in `recordCompletion`)
    loses the samples of a completion whose key a later modify in the fold
    drops; new `TestStoreFoldModifiedCompletionKeepsSamples`
    (`jobqueue/store_fold_test.go`): a job of req group q with peak RAM,
    disk and run time completed, put back live and modified to a new key
    in one fold interval learns on a log store what bbolt learns. Mutant
    red (`Expected: {rec: {RAM: 300, Time: 3605s, Disk: 1}, samples: 3}
    Actual: {rec: {RAM: 0, Time: 0, Disk: 0}, samples: 0}`), real code
    green.
  - Round 4 mutant sweep of the code the fix added, each mutant in a copy
    of the tree against the reverse dep group, fold, convert, checkpoint
    and backlog tests. Two more survivors closed with tests:
    `dropSeqs[l.key] = l.seq` in place of the `max` (records of one key
    read out of seq order) killed by a new Convey in
    `TestLogStoreFoldModifyAfterCompletion` (two modifies read in the
    opposite order to their seqs around the completion drop its postings);
    `foldedReverseDeps`' loop bound `bytes.HasPrefix` to `posting != nil`
    killed by a new scenario in `TestStoreArchivedDependentsAfterRestart`
    (an archived dependent of "grox", whose postings sort straight after
    grow's, is not put back by an add of a member of grow). Killed by
    existing tests: the posting prefix without `dbDelimiter`; no
    `slices.Compact` in `reverseDepKeys`; an unapplied backlog modify
    applied; no live re-index after a backlog modify; `index` without
    `addToKeyIndex`. Equivalent: `droppedAt >= c.seq` (a modify and a
    completion never share a seq, and seqs start at 1); the posting bytes
    left out of `recordCompletion`'s and `putReverseDeps`' sizes (they
    only move where a fold splits its transactions); no `len(keys) == 0`
    return in `dropReverseDeps` (the scan finds nothing); no `key != nil`
    check in `reverseDepsOf` (no key is the empty string); no
    `slices.Clone` in `reverseDepsOf` (the postings are deleted in the
    transaction that read them, while bbolt's pages stay mapped);
    `foldedReverseDeps` calling `fn` on a posting whose key does not
    parse (the zero key has no job, so the scan finds nothing for it).
    Untested error path: `undone` dropping `reverseDepKeys`' error, which
    needs a failing history.bolt read.
  - Round 4 gates (every OS_ var unset, scratch HOME, 2026-11-27,
    uncommitted on 2f4d5e28): `make lint` 0 issues; `make test` PASSED
    (1388 passed); `make race` first run failed only
    `TestBackupStreamStopWithQueuedBackup` (line 757: the waiting backup
    chunk request got a nil error when the manager stopped; backup
    streaming reaches no code this fix touched). It passed 5 of 5 alone
    under `-race`, and the full `make race` rerun PASSED (1388 passed).
    Not a load-matched A/B, so not attributed; noted for the caller.
    `TestClientAddAndWaitCancelledDuringOutage` failed in none of the runs.
