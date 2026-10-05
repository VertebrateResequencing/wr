# Small durable run-state records for reserve and start

At production scale (about 6,000 concurrent LSF runners), the manager hands
out tens of thousands of reservations before they are durable: it waits
`ReserveWriteWait` (10s) for the reservation write to commit, then hands the
job out anyway, and a crash in that window can make the job run twice. In the
battery10 soak (commit be432093, 2026-10-05) 23,586 reservations were handed
out non-durably (2.43% of 972k runs, against 0.034% in soak9 at about 3,900
runners), and 59 double runs followed a crash outside any injected stall.

The investigation (evidence and scripts in
`/nfs/hgi/wr/sb10-bigdb/battery10/a3/`, summarised in
`/nfs/hgi/wr/sb10-bigdb/battery10/RESULTS.md` anomaly A3) found that bbolt's
single write lock is saturated by four writers with no priority (the archive
writer, the best-effort writer that carries every reserve and start durable
write plus exit ops, chunked adds through `bolt.Batch`, and deleteLiveJobs),
that about 85% of the lock hold time is NFS `fdatasync`, and that each
reservation and start re-writes the whole encoded job (about 10KB for portal
commands), so commit time grows with batch size and the system collapses into
queueing above about 5,000 runners. A bbolt-only model of the write path
(`a3/a3model_test.go`) showed that writing a small (about 200 byte) run-state
record for reserve and start, instead of re-encoding the whole job, took
reservations over 10s from 16.9% to 0% at 5,000 runners, median wait from 7.4s
to 1.0s, and raised throughput by about 30%.

The owner chose this option ("A"): make the durable write for a reservation
and a start a small run-state record (for example state, ReservedBy, host,
pid, token and the times needed), stored separately from the full job
encoding, which recovery overlays onto the job it belongs to. It must keep the
existing durability and ordering guarantees (including a job's
release-before-reservation and change-versus-exit ordering through the
best-effort writer's sequence, and the 260928 reserve-durability behaviour),
drop stale run-state records when a job is archived, removed or re-run, and
handle upgrade from and downgrade to databases without these records. The
change must be gated by `make test`, `make race`, `make speed`, the
`wrdev.sh` crash and recovery modes, and a production-scale LSF crash soak
comparable to battery10, showing far fewer reservations handed out
non-durably and no new double runs or lost jobs.

Repository: wr (github.com/VertebrateResequencing/wr), develop at 6ed23588.

## Notes

- Today's write path (findings): a reservation is persisted by `persistReservation` (jobqueue/serverCLI.go ~1161) via `updateJobAfterChangeDurableWithin(job, ReserveWriteWait)`, and a start by `handleStart` via `updateJobAfterChangeDurable`; both re-encode the whole Job under `job.RLock` and queue it on the best-effort writer (db.go `queueJobChange`), which orders changes and exit ops by an in-memory `beSeq` and skips a change older than the same job's last exit op in a batch. No per-job counter is persisted, so a run-state record's precedence over the full record must come from transaction structure (a full-record write of a live job supersedes or deletes its run-state record in the same transaction). Full live-record writes outside the best-effort writer (add path `putNewLiveJobs` and its `liveRecordHandedOut` check, rerun marks, archive keep-live and putBack paths, modify and delete paths) each need an explicit rule for the run-state record. Recovery (`recoverIncompleteJobs`, `recoveredItemDef`, `recoverRunnerHold`, `recoverRunningJob`) needs State, Pid, Host, ReservedBy, RunnerReservation and LimitGroups, so the overlay is applied before they run. A new bucket needs no migration and survives `wr manager compact`.
- Every reservation handed out before its write was durable is logged individually at info level with its job key and the running total (a cumulative counter), so the manager log holds an exact, per-key count up to any crash (battery10 would have produced about 23k such lines over 3h, peaking about 4k/min during crash recoveries). The existing rate-limited warning may stay or be replaced by these lines.
- The gating production-scale LSF crash soak passes only if reservations handed out non-durably outside injected stalls are at or below soak9's rate (0.034% of runs), and there are zero double runs and zero lost jobs not attributable to an injected stall or commit stall.
- The soak database inspection tools that decode live job records directly (`developers/soak/dbstart` and `.docs/reliable2/harness/statinspect`) learn the run-state overlay in the same PR, since the soak gate depends on them.
- Downgrading to an earlier wr version after this change has run is not supported: there is no fold-back of run-state records into full records at stop. The CHANGELOG states plainly that once this version has opened the database, an earlier version may re-run jobs that were reserved or running.
- The database schema version is bumped to 2 (the database may hold run-state records), and from this version on the manager refuses to start on a database whose schema version is newer than it understands, with a clear error naming both versions. This protects future downgrades to this version or later; it cannot protect a downgrade to v0.38.0, which does not check.
- Other full live-record writes through the best-effort writer (suspend, resume, kick, lost marking, and the re-sent-report answers `handleReportOnOwnBuriedJob` and `ackAlreadyReleased`) and the exit ops (release, bury, exit, which carry stdout/stderr) stay full writes and must replace or delete any waiting run-state record; that is for the spec to define. Besides recovery, `dbstart` and `statinspect`, no reader of live records needs the overlay (others only check presence or dependencies). The `wrdev.sh` crash and recovery modes print verdict lines usable as gates. The A3 model's small-record option alone cut reserve p50 from 7.39s to 1.04s, raised runs/s from 126 to 164, and cut best-effort transaction time from 1.45s to 60ms at 5,000 runners.
- The schema version stays a single ladder (each version implies the ones below; 1 means no completed-job record holds stdout/stderr). Startup writes version 2 onto an existing version-1 database (today only a new database is stamped), and `wr manager compact` writes the current version (today `copyAll` hard-codes 1). The manager refuses to start on a version-0 database (created before v0.38.0 and never compacted), with an error telling the user to run `wr manager compact` first; this is a one-off required offline step when upgrading such a database, and the CHANGELOG says so. All manager opens, including restore-from-backup, go through the one startup sequence in db.go, so one check covers them; whether compact itself also refuses a newer database is for the spec to decide. Nothing outside the manager reads the schema version.
- Restore-from-backup copies the backup over the database file before opening it, so a refused version-0 backup is already in place for the user to `wr manager compact` and start again. At version 1 and above `compactBolt` copies the database unchanged (a run-state bucket survives); compact must stamp the current version on every path and must not strip a version-2 database again. The refusal and the stamp to version 2 belong before anything else writes on open, so a refused database is left unmodified. The `wr manager compact` help text (cmd/manager.go) must say compact is now required for databases from 0.37.2 or earlier, not optional.
- The soak fixtures (fix120k.db, pristine6, pristine10, prod.db under /nfs/hgi/wr/sb10-bigdb) are schema version 0, so the gate compacts copies of them (never the originals) and regenerates their manifests, and runs a baseline production-scale soak of current develop on the compacted fixtures as well as the soak of this change, so the comparison is like for like. The pass criteria compare the change's soak with that baseline and with soak9's rate as already stated.
- Learned requirements: before a reservation, `prepareReadyJob` -> `updateJobRequirementsForRetry` can change `Requirements` and create `RequirementsOrig` in memory only, and today the reservation's full write is what persists them. The change must keep them durable at reservation (correctness, not optional), and the spec must require an audit that no other in-memory-only change to a persisted field relies on the reservation's or start's full write.

