# Incidental findings during customdb delivery

Branch-owned list of findings that came up during implementation and review
and are not part of the current item. Each is resolved before the PR is
called ready, or explicitly deferred with the owner.

- [ ] (from 0a.1 review) The rerun guard's member check has no `*db`-level
  test: changing the `archiveJobTx` `addStored` closure to `live != nil`
  survives every test. Add a bbolt test that archives a guarded dependent
  after `setDependents` but before the add's write commits, and asserts it
  is not kept live. Pre-existing gap at d1a4b6d7; C4 tests 9-11 cover the
  log store in Phase 1.
- [x] (from 0a.1 review) spec.md's store code block declares
  `storeKindLog`/`storeKindBolt`, which Phase 0 omits as unused; add them
  in Phase 1 (1c.1/1e.4) when the log kind is first read. Done in 0b.1:
  `forEachStore` needs both kinds, so `store.go` now declares them.
- [ ] (from 0a.1 implementation) `cleanorder -min-diff` wants to move
  untouched declarations in `reliable4_status_limit_test.go` and
  `runner_lifecycle_test.go`; pre-existing ordering drift, left alone.
- [x] (from 0b.1) `db_test.go` and `db_runstate_test.go` stay whole in
  `boltOnlyTests`, so F5 would delete their behavioural tests with their
  bbolt ones. Move the behavioural tests onto `storeInspector` (or into
  `*_bolt_test.go` files) before Phase 2.
  Done in 0b.1: the bbolt tests are in `db_bolt_test.go` and
  `db_runstate_bolt_test.go`; the rest run through the store API in
  `store_jobs_test.go`, `backup_copy_writer_test.go`, `db_encode_test.go`
  and `db_runstate_test.go`.
- [x] (from 0b.1) Unlisted test files still call helpers defined in
  bolt-only files, so F5's deletions break their build until each helper
  has a store-neutral home: `boltWrites` (`db_bench_test.go`, used by
  `readd_queued_test.go`), `rewriteStoredLiveJob`
  (`store_bolt_inspect_test.go`, used by
  `running_dependent_rerun_test.go`), and `server.boltDB()` with
  `archivedDecodes` (status and history-scan tests). The A2 guard only
  greps for direct bbolt use, so it does not catch these.
  Done in 0b.1: generic helpers moved to unlisted files, bbolt ones stay
  with the tests that need them in listed files, `rewriteStoredLiveJob`
  and `openStoreFileInspector` go through the store, and A2 test 4
  (`TestBoltOnlyTests`) type-checks the package with every listed file
  emptied. `boltDB()` and `archivedDecodes` were never in listed files.
- [ ] (from 0b.1) Unlisted tests reach the bbolt store through
  `server.boltDB()`, which panics once 1e.4 makes the log store the
  default, so they fail at 1e.4, not at F5. 0b.1 ported the plain store
  inspection ones (`crash_mid_add_guard_test.go`,
  `buried_dependent_restart_test.go`, `confirm_dead_after_stop_test.go`,
  `kick_order_test.go`, `running_dependent_rerun_test.go`,
  `shutdown_runner_wait_test.go`, `runner_lifecycle_test.go`, and the
  `recommendedReqGroup*`, `recMBRound` and `archiveJob` uses in
  `jobqueue_test.go`, `nohold_test.go`, `queues_avoid_scheduling_test.go`
  and `runner_scheduling_test.go`) and moved the bbolt-only ones to
  `release_std_bolt_test.go` and `resume_order_bolt_test.go`. Left, each
  needing something the log store brings in Phase 1:
  `configureFastTestBackups` and `s3accessor` (`jobqueue_test.go`: backup
  tuning and the S3 backup client); `archivedDecodes`
  (`reliable4_history_scan_test.go`, `reliable4_status_limit_test.go`,
  `reliable4_status_unbounded_test.go`, `status_seed_decode_test.go`: a
  decode counter); `reqStats` (`nohold_test.go` `loadsAfter`,
  `reqstat_cache_test.go`, `rac_learned_bench_test.go`: the learned
  requirement cache). Also failing at 1e.4: tests driven by `db.go` hooks
  the log store does not call (`jobChangeAheadHook`,
  `jobChangeAheadQueueingHook`, `jobExitSnapshotHook`,
  `dependentsReadHook`, `dependentsGuardHook`, `archiveGuardsSeenHook`,
  `reqStatLoadHook`; `grep` each in `jobqueue/*_test.go`), and listed tests
  that `serve()` a manager and call `boltDB()`, which must set
  `storeKind: storeKindBolt`. Owner: 1e.4, which ports each, pins it to
  bbolt, or deletes it where a log-store test covers it.
- [ ] (from 0b.1) Unlisted tests still open a bbolt `*db` directly
  (`initDB`, `depGroupSeenGets`, for example in `jobqueue_test.go`,
  `db_unreadable_test.go`, `db_encode_test.go`, `client_payload_test.go`
  and `status_summary_decode_test.go`), so they build only while `db.go`
  exists. A2 test 4 cannot see this, since `db.go` stays until F5. Owner:
  2e.1 (F5), which deletes `db.go` and must port each to the store API or
  `storeInspector`, or delete it where a log-store test covers it.
- [ ] (from 0b.1) The tests that open their crash window by holding the
  bbolt write lock are listed, so F5 deletes them: `start_durability_test.go`,
  `release_durability_test.go`, `release_durability_resend_bolt_test.go`,
  `release_std_bolt_test.go`, `reserve_ttr_test.go`,
  `reserve_durability_bolt_test.go`, `archive_stall_bolt_test.go`,
  `archive_before_start_bolt_test.go` and `warn_storm_bolt_test.go` (its
  reservation-warning half; the slow-request half needs no bbolt and is in
  the unlisted `warn_storm_test.go`); so are `resume_order_bolt_test.go`'s
  resumes racing the bbolt writer's final drain. On the log store, stalled
  reservations are C3's (1e.1, tests 2-4 and 6) and acknowledged starts and
  releases are A4's (0d.1, 1g.1). An acknowledged bury, a buried job's
  stderr seen once it is buried, a re-sent release or bury, a stalled
  archive, a late start and a resume during the writer's last drain have
  no log-store test yet. Owner: 1e.1 (C3), whose sync hook can hold the
  commit as these tests hold bbolt's lock.
  The same holds for four more listed tests that hold bbolt's writer:
  `TestReliable4AddCloseDrains` and `TestReliable4ArchiveCloseDrains`
  (adds and archives queued when `close()` is called are all answered
  and durable), `TestReliable4BestEffortCloseDrains` (best-effort changes
  queued when `close()` is called are durable with their latest state)
  and `TestReliable4ArchiveBeatsResurrectingChange` (a change or exit
  queued behind an archive's held commit does not bring the job back
  live). Their unheld forms run on every store
  (`TestStoreCloseDrainsWrites`, `TestStoreArchivedJobNotResurrected` in
  `store_jobs_test.go`), but there the race with `close()` decides how
  many writes are in flight; 1e.1 adds held-commit forms on the log store.
- [x] (from 0b.1 review) F5 broke the `reliability_repro` build:
  `envIntDefault` and `reliable4GiB` were defined only in the listed
  `reliable4_backup_repro_test.go`. Done in 0b.1: both moved to
  `reliable4_reportstorm_test.go`, and A2 test 4 now vets every tag set.
- [x] (from 0b.1 review) F5 deleted behavioural coverage with its bbolt
  step: `TestMovedOnRunnerAfterRestartSecondReserved` now composes its
  crash image through the store in `moved_on_runner_test.go`, and
  `TestReserveAndStartWriteRunState`'s crash recovery and duplicate start
  are in `TestReservationAndStartRecoveredAfterCrash`
  (`db_runstate_test.go`), leaving the live record bytes and the
  `runStates=1` log in `db_runstate_bolt_test.go`.
- [ ] (from 0b.1 review) `reliable2_dbcompat_test.go` is listed whole, as
  its fixture is a bbolt golden file, so F5 deletes
  `TestReliable2RecoveryWindowReturnsRecovering` and
  `TestReliable2RecoveryRestoresIncompleteJobs`: a recovery-window j* call
  gets `ErrRecovering`, and the fixture's incomplete jobs become reservable
  once recovery ends. These are E1 conversion coverage: a converted golden
  bbolt file must recover the same way. Owner: 1f.2 (E1, Phase 1 form),
  kept by 2d.1.
- [x] (from 0b.1 review 5) More store-neutral behaviour that F5 would
  have deleted with its listed file now runs on every store: an env
  retrieved before it is stored is still stored (`TestStoreEnv`, from
  `TestReliable4EnvMissNotCached`); a second open of a held store gets
  `ErrDBLocked` and changes no file, the holder keeps working, an open
  waits for a briefly held lock, and a corrupt store is restored from its
  backup (`store_lock_test.go`, from `depgranularity_dblock_test.go`,
  which keeps only its bbolt `ErrTimeout` check); the recovery dependency
  pass resolves as per-job resolution does and stops on a cancelled
  context (`TestStoreRecoveryDependencyPass`, from
  `TestReliable4RecoveryDependencyPass`); a reopened store gives back its
  live and archived jobs (`TestStoreReopenRoundTrip`, from
  `TestDBMapFreelistRoundTrip`); and a key archived twice at one end time
  is recent once (`TestStoreRetrieveCompleteJobsRecent`, from
  `TestDBEndTimeIndex`).
- [x] (from 0b.1 review) The ported lock tests (`TestStoreOpenLocked` in
  `store_lock_test.go`) hold a store open and require a second open to
  fail with `ErrDBLocked` leaving every file in and beside `dbFile`
  unchanged, `.restoring` and `.corrupt` included. bbolt meets this
  because its lock is on the file it opens, but its dev wipe removes
  `dbFile` before taking that lock. The log store must take its lock
  before E2's open-order steps 1-3 (dev wipe, removing `.restoring` and
  `.corrupt`, resuming conversion), and so cannot keep it inside the
  store directory those steps remove or rename. spec.md now says so: the
  lock is `dbFile + ".lock"`, taken first (C7, E2, E3), and C7 test 1
  checks the files. Owner: 1c.2 (C7), kept by 1f.4 (E2). Done in 1c.2:
  `lockStore` (`store_open.go`) flocks `dbFile + ".lock"`, retried until
  `managerDBOpenTimeout`, then `ErrDBLocked`. `openStore` takes it before
  `initDB` for every kind, so bbolt's dev wipe no longer runs while
  another manager holds the store, and `openLogStoreLocked` takes it
  before the store directory or `VERSION` is touched; each store releases
  it on close. `TestStoreOpenLock` (`store_open_test.go`) proves a refused
  open changes no file, with a dev wipe asked for (bbolt) and with only the
  lock held and no store directory yet (log).
- [x] (from 0b.1 review) `TestReqStatCacheUncommittedStat`
  (`reqstat_cache_bolt_test.go`) holds a bbolt write transaction open,
  so F5 deletes its rule: a learned-requirements load that runs while a
  new sample is not yet committed is not current once it commits. On the
  log store the folder adds samples and bumps `reqStatChanges` (spec
  "Folder and history"), so the same race is a fold commit. Owner: 1d.1
  (D1), which adds a log-store test with the fold commit held.
  Done in 1d.1: `TestStoreFoldUncommittedSample` (`store_fold_test.go`)
  loads the req group from inside a fold's open history.bolt transaction
  (`logFoldTxHook`), which sees no sample, and requires the cache not
  current once the fold commits and the next load to give the new sample.
  The fold adds samples through `reqStatCache.putStat`, whose commit hook
  invalidates, as bbolt's archive does.
- [ ] (from 0b.1 review) Listed tests cover what a manager does when it
  opens an older bbolt database, and F5 deletes them: its upgrade is
  logged with progress, rate-limited, and not at all when there is none
  (`reliable4_upgrade_log_test.go`, `TestDBUpgradeProgress`); the
  startup-progress sidecar stays live after it until the token is ready
  (`TestServeReportsPostUpgradeStartupUntilTokenReady`); and pre-upgrade
  records (retired dep group index, missing reverse lookups and seen dep
  groups) still resolve dependencies and allow a key-changing modify
  (`TestDepGroupRetiredIndex`, `TestDBReverseLookupIndex`,
  `TestDBDepGroups`). On the log store the old database is converted
  instead. Owner: 1f.2 (E1), which shows a converted pre-upgrade database
  resolves and modifies the same way, and that the conversion's sidecar
  and log lines behave as these tests require of the upgrade's.
- [x] (from 0b.1 review) `TestReliable2FastStartupNoHistoryScan`
  (`reliable2_startup_scale_bolt_test.go`) writes its 250,000-job history
  as raw bbolt records, so F5 deletes the check that startup time does not
  grow with completed-only history and seeds no complete counts. Owner:
  1d.1 (D1), which can seed `history.bolt` at that scale and assert the
  same of the log store's startup.
  Done in 1d.1: `TestLogStoreFastStartupNoHistoryScan`
  (`store_history_bolt_test.go`, in `boltOnlyTests` as it seeds
  history.bolt raw; Phase 2 needs its own over segments) seeds 25,000 and
  250,000 completed-only jobs whose history records no decoder accepts, so
  an open that read one would fail, and requires the large open within
  `c2AbsoluteStartupLimit`. It does not assert bbolt's no-scaling ratio:
  the log store loads the in-memory history key index (spec "In-memory
  state of logStore") from `completeSeq` at open, about 0.25µs per
  completed key (6ms at 25,000, 62ms at 250,000), which that ratio fails.
- [x] (from 0c.1) Four `db.bolt.Update` calls bypass write turns and
  Batch, so no "db writer" line counts them: `storeRunningRerunMarks`
  and `storeLiveForRerun` (while serving), `dropRunStates` (recovery)
  and `syncFreelist` (close). A3 names five writers only; the 0c.1
  review decides whether the two serving-time ones count under `batch`.
  Done in 0c.1: the two serving-time writes count under `batch` via
  `updateUncoalesced`; the recovery and close ones stay uncounted.
- [x] (from 0d.2) Replaying the prodsim recording may report false
  mismatches: `cachedReqGroupRecs` and `reqStatChanges` depend on when the
  async `refreshReqGroupRecsLater` loads finish, and `RepGroupStatus`
  float sums may differ in summation order. They are not writes, so the
  overlap rule does not exclude them. Check at the first replay of the
  0d.3 recording (A5 gate) and, if they mismatch, exclude or normalise
  them in `storeDigest` with a spec note. At the same replay, revisit two
  Phase 0 digest choices (spec A5): error results digest by their text,
  which the log store's errors may word differently; and handle results
  digest only whether they are nil, so a log store returning a nil handle
  where bbolt returns one mismatches.
  Done in 0d.3: the first replay had 30,701 mismatches, all in
  `cachedReqGroupRecs`, `refreshReqGroupRecs` and `reqStatChanges`; status
  float sums had none. The cause was the cache's 1s refresh interval, which
  held back loads a replay at full speed reached in under a second (so
  `reqStatChanges` ended 3 below the recording's every time), and background
  loads. Replay now runs with no interval and probes the learned values
  before each call naming a ReqGroup and after each write, and accepts a
  recorded answer that is one of the probed values or a stamp no higher
  than the replayed one (spec A5, `store_record_learned.go`,
  `TestStoreReplayLearned`); the recording now replays with 0 mismatches.
  The error-text and handle digests give bbolt's own answers on a bbolt
  replay, so only a log-store replay can test them: 1g.2 owns that.
- [x] (from 0d.3 review) The 1g.2 replay folds before each
  learned-requirements probe, which hides a folder that stalls or never
  invalidates `reqStatCache`. Spec D1 has no acceptance test that the
  folder keeps folding and invalidates the cache unprompted (D1 test 2 and
  C6's wait on the folder's position cover it only in part). Owner: 1d.1
  (D1), which adds that test.
  Done in 1d.1: `TestStoreFoldUnprompted` archives two jobs with new
  samples, one after the other, never calling `foldNow`, and requires each
  folded and the cache invalidated within 5 fold intervals, then the new
  value learned; `TestStoreFoldFailureRetried` requires the folder to fold
  by itself after failed folds.
- [x] (from 1a.2) `wal.WriterStats` is cumulative and has no `MaxSync`,
  but A3's `log` line reports `maxSync` per interval. Owner: 1c.1 (C2),
  which adds the `log` writer's "db writer" stats and decides how the
  per-interval maximum is kept (in the writer or in the log store).
  Done in 1c.1: the writer keeps it, since only its goroutine times each
  sync. `Writer.TakeMaxSync` returns the longest batch sync since the last
  call and resets it (`TestWriterTakeMaxSync`); `WriterStats` stays totals.
  The log store's reporter, its one caller, takes it each interval and
  turns the totals into per-interval figures (`logWriterReport` in
  `store_stats.go`, `TestLogStoreWriterStats`).
- [x] (from 1c.1) After a failed sync the wal writer opened the next file
  only when the next batch arrived, so a manager stopping first left the
  abandoned file unbounded and recovery replayed the failed batch. Done in
  1c.1: the writer opens the next file at once, retries it while idle and
  makes a last try at `Close`, and never skips a file number
  (`TestWriterReopensAfterAbandon`, `TestLogStoreFailedWriteNotReplayed`;
  spec B2 and Recovery step 4 amended, with the residual window stated).
- [x] (from 1b.1 review) A decoded Behaviours or Dependencies holding a
  nil element panics in `dropImpossibleCleanups` (`behaviours.go`
  `withoutCleanups` reads `b.Do`). `FuzzFlatSpec` found it in the flat
  spec's blob, and bbolt's `decodeJob` calls the same function, so a
  corrupt bbolt record panics the same way. wr never writes nil elements.
  Owner: 1b.1 (C1), which rejects such a blob as malformed in both
  decoders through one shared check. Done in 1b.1: `checkStoredElements`
  (`job.go`) returns `errNilStoredElement`, which `decodeJob` returns and
  the flat blob decode wraps in `errFlatMalformed`.
- [x] (from 1c.1) The log store's `applySpec` adds reverse dep-group index
  entries for a new spec but never removes the replaced spec's entries, so
  the index depends on apply order once a key's spec changes its
  dependencies. Today's re-adds keep the spec, so it is not reachable yet.
  Owner: 1c.4 (C4), whose modify records change a key's spec; remove the
  replaced spec's entries and test the index after a modify in either
  apply order. The 1c.1 review also found two more order dependences C4
  must settle: a delete applied before an older spec lets that spec make
  the key live again (the reducer keeps no tombstone), and
  `applyComplete` copies whichever spec is live at apply time. Extend
  `TestLogStoreReducerOrder` to delete, modify and complete records.
  Done in 1c.4: `applySpec` unindexes the replaced spec's dep groups, and a
  modify's old key leaves the index even if complete, as bbolt's modify
  drops its lookups (`TestLogStoreReverseDepsAfterSpecChange`, both apply
  orders). No tombstone is needed because no removal applies before a
  lower-seq spec or complete record of its key: every add, delete, modify,
  archive, put-back and rep group lookup claims its keys (`logClaim`) from
  before it decides until it has applied, and a guarded add's put-backs,
  which claim nothing, decide and append under `guardMu` as removals do
  (`TestLogStoreArchiveRacesRemoval`, `TestLogStoreClaimedWrites`; tested
  with a hook holding a write between its append and its Refs,
  `logAppendedHook`); an archive
  decides and takes its seqs under the job's read lock (`decideArchive`), so
  a rerun mark's put-back after the decision is later than the archive's
  delete (`TestLogStoreArchiveRacesRemoval`). A later spec a complete record
  may pair with is a put-back of the same job, with the same spec. A
  transition or exit of a key a put-back or modify is making live waits
  for it to apply (`logStore.reviving`, spec Write path: reviving), so it
  is not dropped in memory and kept by replay
  (`TestLogStoreRevivedKeyTransitions`). The extended
  `TestLogStoreReducerOrder` applies random histories of adds,
  transitions, exits, archives (kept live or not, and of a key not live),
  put-backs, rep group lookups, deletes and modifies (keeping the key or
  not, of a live key or not, then the new key's own writes), each spec
  with its own rep and dep group, in every order those rules allow and
  checks each key ends as in seq order, rglookups included; it found that
  `clearStd` cleared a std record later than the complete record, now fixed
  (spec Records amended), and without the reviving rule it finds the
  transitions that would be dropped.

- [ ] (from 1c.2) The store lock now creates `dbFile + ".lock"` in the
  store's parent directory, so that directory must be writable. This is
  new for deployments whose parent directory is read-only. Owner: 2e.2
  (F6): add a CHANGELOG line saying so.

- [x] (from 1c.3) The reducer drops a complete record for a key that is
  not live and whose only history entry is already folded: it would need
  the key's spec from history.bolt to make an overlay entry (see the
  comment in `applyComplete`). Only an archive racing a delete (C4) can
  produce one, once 1d.1 folds. Owner: 1c.4 (C4), which owns that race.
  Done in 1c.4: an archive claims its key, so decides once any delete or
  modify of it in flight has applied, and if the key is then not live (a
  lost job removed before its run ended) its block starts with the job's
  spec record, so its complete record always finds a spec and the
  completion is recorded, as bbolt records it
  (`TestLogStoreArchiveRacesRemoval`; spec Records and Archive amended).
  The reducer's drop stays only for records no archive writes.

- [x] (from 1c.3) `foldHistory` trusts its caller for contiguity: it does
  not check that a fold starts at the previous folded position F, and
  `putHistoryMeta` persists `f.to` even when it is below the stored F
  (in-memory F never moves back). Both are safe with one folder reading
  the log in order. Owner: 1d.1: have the folder fold contiguously from F,
  and either assert that in `foldHistory` or never persist a lower F.
  Done in 1d.1: folds are made one at a time (`foldNow` under `foldMu`),
  each reading the log from F, taken under the store's lock, to the
  writer's durable end; `putHistoryMeta` never writes a folded position
  before the one `meta` holds (`TestStoreFoldNeverMovesBack`, which reopens
  after a fold to an earlier position).

- [ ] (from 1c.4) C4 tests 9 to 11 say "run a server on a log store",
  which cannot run until 1e.4 makes the log store the server's store.
  1c.4 covers every store-visible clause of them at the store level
  (`store_log_guard_test.go`). Still to cover with a server: the
  dependent going `ItemStateDependent` then `ItemStateReady`, "reserved
  again", "runs again once m completes", and test 9's `archiveKeptLive`
  (set by the server's mark; at the store level the outcome is
  `archiveRemovedLive` with the guard keeping d live). Owner: 1e.4.

- [x] (from 1c.4) A modify drops its old key's rglookups, as bbolt's
  `deleteOldLiveJobs` drops its `repgroupToKey` entries, but the log store
  drops only those not yet folded (`applyModify` over the in-memory
  rglookups). Once 1d.1 folds rglookups into `history.bolt`, a modify after
  the fold leaves the old key under its folded rep groups, so
  `retrieveCompleteJobsByRepGroup` and the add dedup's
  `checkIfCompleteUnderRepGroup` still find it there, unlike bbolt.
  Owner: 1d.1: have the folder delete a modify's old key from
  `repgroupToKey` (history.bolt has no reverse lookup, so it needs one or a
  scan), and have reads hide folded lookups of keys modified away and not
  yet folded; extend `TestStoreModifyDropsRepGroupLookups` with a fold
  before the modify. Likewise for 1d.2 (C6): replay applies rglookup
  records from F but modify records only from a checkpoint's `walFrom`, so
  a modify between F and `walFrom` must not let its old key's earlier
  rglookups come back on recovery.
  1d.1 half done: history.bolt gains `keyRepgroups`, `repgroupToKey`'s
  reverse, and a fold of a modify record deletes its old key's folded
  rglookups through it, in log order with the fold's rglookups. Until the
  modify folds, the reducer notes the old key in `logState.unlooked` (not
  for a modify already folded), and `repGroupKeys` and the add dedup's
  `completedUnder` skip its folded rglookups. In-memory rglookups now carry
  their record's seq, so a fold of an older rglookup never drops a newer
  one of the same rep group and key (re-added after a modify the fold did
  not read). `TestStoreModifyDropsRepGroupLookups` runs each case with a fold
  before the modify and checks again once the modify folds and after
  reopen; `TestStoreFoldLookupAfterModify` makes the put-back, modify and
  delete while a fold of the original rglookup commits. The 1d.2 half
  stays open, and must also settle this: the folder drops a modify's old
  key's folded rglookups whenever it folds the modify record, while
  `applyModify` ignores a modify at or below its key's live spec seq. No
  live path writes such a modify today, but checkpoint replay (a modify
  replayed after a spec its checkpoint already holds) may reach it, and
  then history.bolt would lose rglookups the in-memory state keeps.
  Done in 1d.2: in Phase 1 there is no log between F and `walFrom`. A
  checkpoint folds to the writer's durable end, past its cut, before it
  merges (spec "Checkpoints", step 2), and F never moves back, so F is at
  or past the newest checkpoint's `walFrom`; recovery replays from
  `walFrom` and refuses an F before it as `errStoreCorrupt`
  (`TestLogStoreRecoverHistoryBeforeCheckpoint`, which puts back a
  history.bolt from before the checkpoint; spec "Recovery", step 3). Nor
  can a checkpoint make a modify meet a spec it would not meet in a full
  replay: the checkpoint is the reducer's state over the log before its
  cut, and replay applies the records after it in log order, so each
  modify after the cut meets the live spec seqs a replay of the whole log
  gives it, and no spec of its key with a higher seq is earlier in the
  log, as claims and `guardMu` make such a spec's write decide after the
  modify has applied. Phase 2's checkpoints, whose F can be before the
  cut, are 2b's (spec "Checkpoints", step 3).

- [x] (from 1c.4 review) `TestLogStoreFailedWriteNotReplayedAfterSpentFile`
  flakes about 1 in 500 under load, pre-existing (load-matched A/B at 1000
  runs: 2 fails at 4d128f96, 1 on the 1c.4 tree; files `[1 2]` instead of
  `[1 2 3]`). In `jobqueue/wal/writer.go` `run()`, if Close is seen before
  the writer's first reopen try, `reopen()` tries file 2, which fails and
  spends that number, and the loop returns without trying file 3.
  Owner: 1c.5: decide whether a close after a spent open must open file
  n+1 (a product fix) or the test must wait for the reopen before
  closing, with a red loop first.
  Done in 1c.5, as a test fix: red loop 3 fails in 1,200 runs (8 at once,
  -race), 0 in 1,200 after. The writer does what spec B2 says: retries
  while idle, and `Close` makes one last try; the residual window names
  a close whose last try fails, spent or not, as leaving the abandoned file
  unbounded. A close racing the first try is that case, not the test's
  (file 3 opened while idle), so the test now waits for `Durable()` to
  reach file 3 before closing.

- [x] (from 1c.4 review) No test pins that a delete over the block size
  limit splits only between keys (mutant `delete-not-grouped`, dropping
  `f.group()` per key in `deleteLiveJobs`, survives). Impact is small: a
  delete is one record per key. Owner: 1c.5: add a Convey under
  `logBlockMaxFrames = 1` asserting a 3-key delete gives 3 frames, like
  `TestLogStoreSplitPutBacks`.
  Done in 1c.5: `TestLogStoreSplitPutBacks` deletes 3 live jobs under a
  1 byte limit and requires 3 frames of 1 delete record each, and none live
  after reopen; `delete-not-grouped` is killed.

- [x] (from 1c.5 review) Two recovery behaviours have no test:
  `recoverIncompleteJobs` returning jobs in key order, as bbolt does
  (mutant `jobs-unsorted` survives), and `jobsOf` failing with
  `errStoreCorrupt` when a live ref's wal file is missing (mutant
  `missing-record-skipped`, unreachable until checkpoints delete old wal
  files). Owner: 1d.2: once checkpoints delete wal files, add a test that
  deletes a wal file holding a live ref after open and expects
  `errStoreCorrupt`, and pin the key order of recovered jobs (or state in
  the spec that callers must not rely on it).
  Done in 1d.2: `TestLogStoreRecoverMissingWAL` deletes the wal file
  holding the live jobs' records after open and requires
  `recoverIncompleteJobs` to fail with `errStoreCorrupt`, and requires the
  recovered jobs in key order (mutant `jobs-unsorted` killed). `liveJobs`
  now reads each file its Refs name, a checkpoint's frame by frame, so a
  missing file is `errStoreCorrupt` before `jobsOf`'s check of each live
  job's records, which stays; a mutant skipping either alone survives as
  the other still fails the recovery, and skipping both is killed.

- [ ] (from 1d.1) E1 step 4 writes `history.bolt` (spec "Folder and
  history"), which now also has `keyRepgroups`, `repgroupToKey`'s reverse:
  a fold of a modify record finds the old key's folded rglookups only
  through it, so a converted store whose `keyRepgroups` is empty keeps a
  modified key under its old rep groups, as the log store did before 1d.1.
  Owner: 1f.2 (E1): write one `keyRepgroups` entry per `repgroupToKey`
  entry it converts, and add a modify-after-conversion check.

- [x] (from 1d.1) The folder paired a `complete` record with its key's
  spec (`logState.specOf`): the overlay entry's if the record applied as
  it, else the latest spec of its key read before it by the same
  `foldNow`, in any of its chunks (from 1d.1 fix round 2, so chunk
  boundaries did not matter: `TestStoreFoldSpecPrecedence` folds in chunks
  of 1 record), else the live spec. What remained was pairing across
  separate `foldNow` calls: a record that applied and was replaced by a
  later `complete` of its key before the folder read it (an archive, a
  re-add and a second archive within one fold interval), whose spec record
  an earlier `foldNow` read (or one before a restart), gets its key's
  current spec, whose ReqGroup may differ if the re-add changed it, so
  that one learned sample goes to the new ReqGroup, where bbolt gives it to
  the old one. Only that sample differs: the history entry is replaced by
  the later one either way, and no in-memory state depends on it. Owner:
  2b.2 (folder writes segments): carry the spec Ref of a replaced overlay
  entry until it folds, or accept and say so in the spec.
  Resolved by 1d.1 (fix round 3), as review round 3 showed the in-memory
  state did depend on it (next entry): the reducer keeps each replaced
  overlay entry until it folds (`logState.replaced`), so a `complete` that
  applied is always paired with the spec it applied with, whichever
  `foldNow`, chunk or restart reads it; one that has not applied yet gets
  the spec the reducer will give it, as before. No pairing difference
  remains. Review round 4 showed the spec records kept across chunks were
  then dead, and could grow past 100MB at 2M jobs: a `complete` that has
  not applied finds every earlier write of its key applied (claims), so
  its key's live spec is the one, or, if the key is not live, its write is
  an archive of a key not live, one block holding its spec record, which
  no chunk splits. Fix round 4 keeps only the chunk's own spec records
  (`foldRead.specs`). The open entries below route keeping
  `logState.replaced` to 2b.2 and swapping its spec Refs to 1d.2.

- [x] (from 1d.1 review round 4) A replaced overlay entry
  (`logState.replaced`) keeps the spec Ref of the `complete` record it
  applied as until it folds, and the folder reads that spec through it
  (`specOf`, then `readRecordBodies`). Once checkpoints delete wal files, a
  replaced entry whose spec Ref points into a deleted file makes every fold
  fail on `readRecordBodies`, and the folder never moves past it. Owner:
  1d.2 (C6): swap the spec Refs of replaced entries with the overlay's
  (spec C6 step 4 now lists them), and add a test that archives a job,
  re-adds it, archives it again before a fold, checkpoints past the first
  archive's spec and deletes its wal file, then folds both archives.
  Done in 1d.2: a checkpoint swaps the spec (and complete) Refs of every
  overlay and replaced entry below its cut, replacing the entry, as
  entries are never changed (`logState.swapRefs`).
  `TestLogStoreCheckpointReplacedSpecs` archives k, re-adds and archives it
  again after the checkpoint's cut but before its swap
  (`logCheckpointFoldedHook`), so the first archive's replaced entry pairs
  with k's spec in the wal file the checkpoint deletes, then folds both
  archives and requires the second's completion, also after reopen
  (mutant `replaced-not-swapped` killed: the fold fails reading the
  deleted file).

- [ ] (from 1d.1 review round 4) The Phase 2 folder pairs each `complete`
  record with the spec it applied with through `logState.replaced`, so
  when the folder writes segments it must keep `replaced` and drop its
  entries only as they seal, and the Phase 2 checkpoint must hold one spec
  per overlay or replaced entry, not one per key, as a re-add or modify can
  change a key's spec between two of its `complete` records (spec C6 step
  3 now says so). Owner: 2b.2: keep `replaced` in the segment folder, and
  cover C6 test 2 with a key re-added under another spec between two
  `complete` records in the open segment.

- [x] (from 1d.1 review round 3) Memory diverged from replay: a job added
  under rep group rg1 and req group q1 and folded, archived (C1), added
  again under rg2 and q2 and archived again (C2), with one fold then
  reading C1, the re-add's spec and C2, paired C1 with the re-add's spec,
  since C2 had replaced C1's overlay entry and C1's spec was before F. So
  C1's EndTime went to rg2, `overlayEnds[rg1]` was never dropped, and after
  a reopen rg1 was missing from `retrieveLastCompletionTimeByRepGroup`,
  `repGroupHasHistory(rg1)` was false and q1 learned no RAM, where bbolt
  gives 300. A `wr mod` changing the rep group of a kept-live job between
  two archives did the same. Owner: 1d.1. Done in 1d.1 (fix round 3): see
  the previous entry. `TestStoreFoldReplacedElsewhere` moves the job
  through three rep and req groups, by re-adds and, kept live, by modifies
  (archived twice under the first, so two replaced entries share a spec),
  each with one fold of every archive, with two folds (the first to the
  log's durable end noted after the first archive, made once later ones
  have replaced it, which must drop that archive's replaced entry from
  memory while the current one stays, from fix round 4) and with a reopen
  before any fold of them, and
  requires bbolt's last completion times, history, learned requirements
  and sample count before and after reopen, with nothing left unfolded.

- [ ] (from 1d.1) The log store loads its whole history key index from
  `completeSeq` at open (spec "In-memory state of logStore", from 1c.3),
  so its startup and heap grow with the number of completed keys, which
  DEVELOPERS.md rule 6 forbids for bbolt's startup: measured at about
  0.25µs per key (62ms at 250,000, `TestLogStoreFastStartupNoHistoryScan`),
  so roughly 0.5s, and tens of MB of map, at prod's 2.15M completed jobs.
  It decodes no history record, so it is far from the cold scan rule 6
  was written for, but nothing measures it at prod scale yet. Owner: 1f.8
  (F3, dbstart over the new store): time `openStore` and its heap on a
  store converted from a prod-sized database, and if either is too much,
  route a lazier index (seqs read from `completeSeq` on demand) back to
  the spec.

- [x] (from 1d.1 review) Folds were unbounded: a fold read the whole log
  from F to the durable end, so after a backlog (150,000 archives from 64
  writers) one fold of 158,000 records held the store's write lock for
  86ms in `foldHistory` and its read lock for 22ms in `specRefs`, and the
  slowest archive rose from 24-36ms to 124-361ms. Owner: 1d.1. Done in
  1d.1 (fix round 1): `foldNow` folds in chunks of `foldChunkRecords`
  (4,096) records, each ending at a frame and committed with the folded
  position it moves to in a transaction of its own, until it reaches the
  durable end it started for (`TestStoreFoldChunks`, which crashes between
  two chunks and reopens). `foldHistory` changes the in-memory state in
  batches of `foldApplyBatch` (1,024), each under the lock: the folded
  position and history index first, then the modify drops, then the
  overlay's completions and rglookups, so no answer has a gap between
  them (`TestStoreFoldBeforeApply`, `TestStoreFoldTwoModifies`). One block,
  such as a 150,000-job add's, is still folded in one transaction, since
  the folded position cannot fall inside a block; only its lock holds are
  batched. Interleaved A/B against 875f784e (no folder), 150,000 archives
  from 64 writers: slowest archive 77-85ms against 6-24ms, p99 11-14ms
  against 3.1-3.3ms, throughput 16-21% lower. Lock holds are now at most
  about 3ms; the remaining slowest archive is the one transaction folding
  the probe's 150,000-job add block, whose writes delay the log's syncs
  (28-57ms with that add folded before the archives). p99 and throughput
  are the folder's own work (reading spec blocks, decoding and encoding
  jobs, bbolt writes) on the same CPUs and disk: with the folder paused
  the build matches 875f784e.

- [x] (from 1d.1 fix round 1) `retrieveRepGroups` (rep groups ever added,
  spec D1) lost a rep group on the log store when a modify dropped its only
  rglookup before the folder folded it: the reducer's `dropLookups` removed
  it from the in-memory rglookups (from 1c.4), and the folder's net of the
  rglookup and the modify put nothing into `repgroups`, where bbolt's add
  keeps it there for good. Owner: 1d.1. Done in 1d.1 (fix round 1): the
  reducer keeps the rep group of each rglookup not yet folded in
  `logState.lookedUpRGs`, which a modify does not drop and a fold of that
  rglookup does, and a fold puts every rglookup's rep group into
  `repgroups`. `TestStoreModifyDropsRepGroupLookups` moves the modified job
  to rep group B and requires A still listed, on bbolt and on the log store
  before the modify folds, after and after reopen.

- [x] (from 1d.1 review round 2) The folder takes the spec Refs of a
  chunk's `complete` records (`foldRead.specRefs`) under the store's read
  lock, then reads those records' frames with no lock
  (`readRecordBodies`), and reads wal files listed once per chunk
  (`walFileNumbers`). Nothing deletes a wal file yet, so this is safe
  today; once checkpoints swap Refs and delete wal files, a checkpoint
  between the two can delete a file a Ref names, and the fold fails with
  an error until it retries. A rotation during a fold is the other half:
  `readFiles` skips files after its durable file (`n > to.file`), which
  `TestStoreFoldStopsAtItsDurableFile` pins by reading to a durable end
  noted before a reopen started a new file, but no test races a real
  fold with a rotation or a checkpoint. Owner: 1d.2 (checkpoints): give the
  folder a reader hold that keeps the files it reads until its fold ends,
  or add a test of a fold racing a checkpoint, with a rotation during the
  fold, that shows the fold succeeds or retries to the same history.
  Done in 1d.2, both: every reader of records through Refs, the fold's
  `specRefs` included, takes a hold (`holdRefs`) while it copies them under
  the store's lock and releases it once it has read them, and a checkpoint
  deletes files only once no reader holds a generation from before its
  swap (spec "Checkpoints", step 4). The wal files a fold scans are at or
  after F, which deletion never reaches.
  `TestLogStoreCheckpointWaitsForFoldReads` holds a fold after it took k's
  spec Ref, below the cut, and requires the checkpoint to wait, k's wal
  file kept, until the fold has read it, then both to succeed and k's
  completion folded (mutant `no-reader-wait` killed).
  `TestLogStoreCheckpointRotatesDuringFold` rotates the log with a
  checkpoint while a fold of 10 archives is reading, and requires the
  checkpoint to wait for that fold, which folds them all, also after
  reopen.

- [x] (from 1d.1 review round 3) Close waited for a whole backlog's fold:
  `stopFolder` waits for the folder goroutine, whose `foldNow` folded every
  chunk to the durable end it started for. Owner: 1d.1. Done in 1d.1 (fix
  round 3): `foldNow` returns `errDBClosed` after the chunk it is folding
  once close has asked the folder to stop, which `foldRound` does not count
  or log as a failed fold; each chunk commits with its folded position, so
  this is the crash-between-chunks case and the next open folds the rest.
  `TestStoreFoldStopsAtClose` closes while the folder reads its first chunk
  of several (`logFoldReadHook`, from fix round 4, so that a close
  discarding the chunk it read fails), and requires that chunk committed and
  no other, no failed-fold line, and a reopen that holds that chunk and folds
  the rest.

- [x] (from 1d.1 review round 3) `TestReqStatCache` never asserted that a
  reload learning the same value leaves `reqStatChanges` alone, so a
  `reqStatCache` that bumped `changes` on every load passed (mutant
  `changes-every-load`). The code moved to `reqstat_cache.go` in 1d.1 and
  both stores share it, so the log-versus-bbolt comparisons cannot catch
  it. Owner: 2b. Done in 1d.1 (fix round 3): `TestReqStatCache` reloads an
  unchanged req group and requires the stamp unchanged, then learns a new
  value and requires it raised.

- [x] (from 1d.1 review round 6) A rep group's latest overlay EndTime
  (`logState.overlayEnds`) can differ between memory and a replay of the
  log, though every answer agrees. Memory drops a rep group's entry once a
  fold commits a completion as late, as spec "In-memory state of
  `logStore`" allows, while a replay rebuilds it from the completions after
  F, which may include an earlier-ending one still unfolded; history.bolt's
  later EndTime then gives the same answer. Owner: 1d.2 (C6): checkpoint
  and recovery comparisons must compare `overlayEnds` by its answers
  (`retrieveLastCompletionTimeByRepGroup`, `repGroupHasHistory`), not
  field by field, or the checkpoint must canonicalise it, as memory may
  legitimately differ from replay.
  Done in 1d.2: `comparableLogState` leaves `overlayEnds` out, and every
  comparison of memory with a replay (`soStateLikeReplay`, C5 test 1) also
  compares `overlayEndAnswers`, each rep group's last completion time and
  `repGroupHasHistory`. `TestLogStoreReplayDiffersOnlyInAnswers` folds a
  completion ending at 200 while one of its rep group ending at 100 applies
  after the fold read the log, and requires memory without the entry, a
  replay with 100, and the same answers (comparing field by field fails).

- [x] (from 1d.1 review round 7) The order of a key's rep groups in
  `logState.keyRepGroups` follows the order its rglookups applied in, so
  under concurrent writes it can differ from a replay's, which applies them
  in log order, when a key has two or more rep groups. Every answer
  agrees, as the slice is used as a set. This predates 1d.1 (from 1c).
  Owner: 1d.2 (C6): checkpoint and recovery comparisons must compare
  `keyRepGroups` as a set, or the checkpoint must normalise its order.
  Done in 1d.2: `comparableLogState` sorts each key's rep groups.
  `TestLogStoreReplayDiffersOnlyInAnswers` looks a key up under a, then b
  and a again after a fold read its first lookup, and requires [a b] in
  memory, [b a] in a replay and the states equal as sets (comparing in
  order fails).

- [ ] (from 1d.2) Recovery step 4 makes "no valid checkpoint"
  `errStoreCorrupt`, but stores are still created with no checkpoint, so a
  store with no checkpoint file replays every wal file, as before 1d.2;
  only checkpoint files of which none has a valid trailer are
  `errStoreCorrupt`. Owner: 1f (E2), which creates a new store with an
  empty checkpoint: make a store with none `errStoreCorrupt` then.

- [ ] (from 1d.2 review) A checkpoint writes its whole `.ckpt` (about
  40-180MB at production's live set) through one buffered writer with no
  pacing, then makes it durable with a single fsync, on NFS. That is the
  burst pattern that starved the bbolt committer's fdatasync before, which
  `backupCopyWriter` fixed by forcing writeback of the backup copy every
  `syncEvery` bytes. Here it could delay the wal writer's syncs, and so
  reservations, while a checkpoint runs. Owner: 1g.5 (daytime NFS
  targets): make sure checkpoints run during the stall-free soak and check
  the `"reservation wait"` p99 of the lines covering them; if any is above
  50ms, pace the checkpoint's write as `backupCopyWriter` does. A soak at
  test scale writes `.ckpt` files far smaller than production's, so also
  measure that p99 while a checkpoint of a production-sized live set (a
  `.ckpt` of 40MB or more) is written on the NFS work directory, such as
  with checkpoints forced while the soak runs against a store seeded with
  that many live jobs.

- [ ] (from 1d.2 review round 6) Recovery step 1 deletes every `*.tmp`
  (`removeTmpFiles`, `jobqueue/store_checkpoint.go`), but only a
  `.ckpt.tmp` is tested, so deleting only checkpoint temp files would pass.
  No effect in Phase 1, where the only other `.tmp` is `VERSION.tmp`,
  which `createStoreDir` rewrites. Owner: 1f.4 (E2), before conversion and
  compaction write other `.tmp` files: a reopen test that plants a
  non-checkpoint `x.tmp` and requires it gone.

- [ ] (from 1d.2 review round 6) `dropBefore` (`jobqueue/store_records.go`)
  zeroes `stateSeq` but keeps the old state Ref. The swap and the
  checkpoint's copy both skip a state whose `stateSeq` is 0, so that Ref
  would stay below the cut and dangle once its file is deleted. It cannot
  happen today, as every spec write carries a state record. Owner: 1f.4
  (E2): zero the Ref there, as `clearStd` does for std, with a test.
