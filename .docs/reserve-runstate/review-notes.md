# Non-blocking review notes to carry into phase plans

From coverage reviews 13 and 14 (both PASS on spec 23d86d73). Not folded into
the spec, so the passes stand; phase plans should address them.

1. A2 test 10 (`TestManagerQueueDefaultsAreNotStored`) is vacuous as written:
   reserve a live job before Stop so a jobRunState record exists, or drop it.
2. C1 test 8: `populateCompactStdDB(unversioned=true)` (db_compact_std_test.go
   ~299-305) reopens with initDB, which C1 will refuse; and
   `reliable2_dbcompat_test.go` (copyFixtureToTempDB at ~93, 179, 226) does
   not compact today. Both need changing, not just re-running.
3. A5 narrow race: two concurrent adds of the same new job; the second add's
   fresh copy can land after an undrained reservation, so the overlay sits on
   the second add's non-run fields until the next full write. Note under Key
   Decisions or cover with a test.
4. E5: manager.log `t=` is ISO with zone offset; stall.log is epoch seconds.
   soakgate must convert; test fixtures use ISO lines matching quoted epochs.
5. E5: an unmapped double counts as outside a stall (conservative); an
   unmapped double should be investigated before being treated as a failure.
6. Helper users: release_after_lost_test.go and
   running_dependent_archive_test.go also use the changed storedLiveJobState
   helpers (cosmetic).
7. Review 13 carry-overs: RerunAfterRun in older record (A1); binc map order
   in A2 test 1; A3 test file naming; E1 test 5 subprocess vs helper; E4 test
   3 vs F2.2; full soak/sweep.sh run with SWEEP_DB_DIR=$G/fixtures.
