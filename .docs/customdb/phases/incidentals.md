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
- [ ] (from 0b.1 review) The ported lock tests (`TestStoreOpenLocked` in
  `store_lock_test.go`) hold a store open and require a second open to
  fail with `ErrDBLocked` leaving every file in and beside `dbFile`
  unchanged, `.restoring` and `.corrupt` included. bbolt meets this
  because its lock is on the file it opens, but its dev wipe removes
  `dbFile` before taking that lock. The log store must take its lock
  before E2's open-order steps 1-3 (dev wipe, removing `.restoring` and
  `.corrupt`, resuming conversion), and so cannot keep it inside the
  store directory those steps remove or rename. spec.md now says so: the
  lock is `dbFile + ".lock"`, taken first (C7, E2, E3), and C7 test 1
  checks the files. Owner: 1c.2 (C7), kept by 1f.4 (E2).
- [ ] (from 0b.1 review) `TestReqStatCacheUncommittedStat`
  (`reqstat_cache_bolt_test.go`) holds a bbolt write transaction open,
  so F5 deletes its rule: a learned-requirements load that runs while a
  new sample is not yet committed is not current once it commits. On the
  log store the folder adds samples and bumps `reqStatChanges` (spec
  "Folder and history"), so the same race is a fold commit. Owner: 1d.1
  (D1), which adds a log-store test with the fold commit held.
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
- [ ] (from 0b.1 review) `TestReliable2FastStartupNoHistoryScan`
  (`reliable2_startup_scale_bolt_test.go`) writes its 250,000-job history
  as raw bbolt records, so F5 deletes the check that startup time does not
  grow with completed-only history and seeds no complete counts. Owner:
  1d.1 (D1), which can seed `history.bolt` at that scale and assert the
  same of the log store's startup.
