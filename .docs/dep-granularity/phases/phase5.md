# Phase 5: Startup - invisible until recovery completes (section E)

Ref: [spec.md](../spec.md) sections E1, E2, E3, E4, E5, E6, E7, E8, E9

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This phase is a substantial part of the work, not a footnote: it reverses
`.docs/reliable/spec.md` B1's user story, and E1 alone rewrites `Serve`'s
ordering. E1 must land before E2-E6, E8 and E9, because every one of them
describes, survives or measures a consequence of it. E7 is the only story here
independent of E1 and runs beside it.

Depends on phases 2-4: the reason the startup model changes at all is that once
`bucketDTK` is retired there is no database index answering "which live jobs are
in group G", so a group the in-memory state has not yet learned looks empty, and
an empty *seen* group means satisfied.

The three new test files - `jobqueue/depgranularity_startup_test.go`,
`jobqueue/depgranularity_dblock_test.go` and
`jobqueue/depgranularity_scale_test.go` - carry the go-conventions copyright
header. `jobqueue` tests use GoConvey `So()` assertions only and guard with
`if runnermode || servermode { return }`; the tests this phase adds to
`internal`, `cmd` and `client/testing` files keep the `So()`-only rule but not
that guard. Never put `So()` inside a loop of more than 20 iterations; count and
assert the final count - E9 test 1 starts a server on 100 live jobs and E9
test 2 on 10k and 50k.

## Proving tests RED

Section E carries one note covering its own gates, and it matters here more than
anywhere else in the delivery. **Several E tests pass at the pre-fix commit,
because the startup window they exercise does not exist there.** A reviewer
applying "prove every gate FAILs pre-fix" must not chase them:

- E1 tests 6 and 7 (the fast-fail certificate paths, which `Serve` already
  returns today).
- All three E3 tests (`serveWebInterface` and `serveClients` already run before
  recovery, so there is nothing to nil-deref and nothing to wait for).
- E4 test 6 (a brand-new DB writes no sidecar today, so the file is already
  absent after a failed start).
- E5 tests 3 and 4 (`currentManagerDBUpgradeStatus` already rejects a sidecar
  whose recorded PID is not running, and already reports nothing when there is
  no sidecar file).
- All three E6 tests (in-package `getij` during a paused recovery already
  returns `ErrRecovering`, and the symbols are already present).
- E7 tests 4, 5 and 6 (restore-from-backup, a prompt open and an unbounded wait
  are all unchanged).
- Both E8 tests (`ClientRetryTime` is already 24 hours).

The rest fail pre-change on their own. Those naming a symbol this work
introduces - among them E1 tests 2-5 and E7 tests 1-3 - are proved RED **inside
the fixed tree with the behaviour withheld**, because the pre-fix tree would not
compile with them. E7 tests 1-3 go further: in the RED run they fail by
**hanging** rather than by asserting, so they only report a named failure when
written with the bounded harness E7 specifies.

## Items

### Batch 1 (parallel)

#### Item 5.1: E1 - Publish at the end of recovery [parallel with 5.2]

spec.md section: E1

`Serve` must **not** block on recovery: the `serve` test helper calls it
synchronously and `pausedRecoveringFixtureServer` waits for the pause hook only
after it returns. Keep recovery in its background goroutine and publish from
that goroutine's tail.

Split `configureAndListen` (`server.go:3478`) into a non-binding
`prepareListener(sock, interruptTime, caFile, certFile, keyFile)` that sets
`OptionMaxRecvSize` and `OptionRecvDeadline`, calls `earliestCertExpiry`, and
loads the TLS keypair and CA into a `*tls.Config`, returning the expiry, that
config and an error. Everything that can fail on bad input still fails there,
fast, through `Serve`'s error return. Only the port bind moves.
`startPriorStateRecovery` moves up to just after `certExpired` and
`go s.handleSignals`, and `Serve` then returns: it no longer calls
`serveWebInterface`, `persistToken` or `serveClients`.

`setRecovering(0)` moves into `startPriorStateRecovery` as its **first**
statement, before `db.recoverIncompleteJobs`. The order inside that function is
not free: `setRecovering` resets `recoveryTotal` and `recoveryRestored` while
`setRecoveryTotal` (`server.go:1323`) deliberately does not touch the flag, so
calling `setRecovering` after it would zero the total it just filled in and make
acceptance test 3's `recoveryProgress() == (0, 3)` read `(0, 0)`.

Publication, in `recoverInBackground` (`server.go:1462`), as the **last plain
statement of the function body - not a defer**, in E1's five numbered steps:
the web interface and `<-ready`; `persistToken`; the RPC bind with the prepared
`tlsConfig`; `go s.serveClients(...)` plus recording that the readers started
(E3); then removing the startup sidecar (E4) and `close(s.serving)`. Publication
registers its own waitgroup keys, each `s.wg.Add(1)` immediately before its
`go`, replacing the two `wg.Add(1)` calls `Serve` makes today
(`server.go:3592`, `:3613`) - E1 explains that keys issued in `Serve` would
leave `shutdown`'s `s.wg.Wait(ServerShutdownWaitTime)` never returning on every
run where publication does not run, and a `Stop` that never returns is worse
than the breakages E3 lists.

Five more constraints, each with its reason in E1: both goroutines take
**`Serve`'s `ctx`, not `bgCtx`** (which `stopBackgroundStartupTasks` cancels at
the top of `shutdown`, before the readers are meant to stop); publish on
recovery **ending**, not succeeding; skip publication when `bgCtx.Err() != nil`;
a tail statement rather than a defer, accepting the sub-millisecond window with
the listener up while `isRecovering()` is still true, because that order keeps
`waitUntilRecovered`-gated tests race-free; and a transient bind failure must
not kill the process - retry the bind every **500 ms for up to 5 s**, the budget
the `serve` helper already uses for exactly this failure. If the bind still
fails after that budget, or `persistToken` fails (not port contention, so not
retried), log at error level and exit through a `publishExit` package var
defaulting to `os.Exit`, in the style of `recoveryPauseHookForTest`. Publication
**returns immediately after calling `publishExit`**, so a server that reaches it
is left with no listener and with `s.serving` never closed. The web interface
needs none of this: its bind error is raised inside `runHTTPServer`, which only
logs it.

File: `jobqueue/server.go`. Tests in `jobqueue/depgranularity_startup_test.go`,
covering all 7 E1 acceptance tests. Carry these:

- Test 2 asserts `isRecovering() == false` only once
  `waitUntilRecovered(server)` (`server_startup_test.go:344`) has returned true.
  That wait is not tidiness: publication is a tail statement and
  `finishRecovering` is a defer, so `Serving()` closes a sub-millisecond before
  the flag clears.
- Test 3: 3 prior incomplete jobs; `s.q.Stats().Items == 0` and
  `recoveryProgress()` reporting `(0, 3)` while paused, `(3, 3)` and all 3
  reservable after release.
- Test 4 destroys the queue before releasing the hook so recovery's
  `enqueueItems` fails with `queue.ErrQueueClosed`; publication still happens. A
  corrupted job record is the **wrong seam** for this test, and E1 says why.
- Test 5 is the `publishExit` test, and **the order of three steps makes or
  breaks it**: observe `publishExit`, then close the test's own listener, then
  assert `Connect`. E1 gives both reasons in full - `Connect` would hang
  forever against a plain listener that never speaks TLS (mangos dials
  synchronously and the `tls+tcp` dialer has no handshake deadline), and while
  the test owns the port a failed `Connect` says nothing about whether
  publication bound anything. Closing first is also what lets `Stop` return,
  since `shutdown` calls `waitForPortsClosed` unconditionally. The test also
  asserts `publishExit` is called exactly once, only after the 5 s budget, and
  asserted after the fact rather than inside a loop; and its second half
  releases the port 1 s in, after which the retry binds, `<-server.Serving()`
  returns and `publishExit` is never called.

**Spec reading, flagged.** `Server.Serving()` appears in the Architecture's new
symbols and is added by E2, but E1 tests 2 to 5 consume it. Land the
`s.serving` channel, the `close(s.serving)` at publication step 5 and the
`Serving()` accessor here; leave E2 the `beginShutdown` close, the callers'
waits, the doc amendments and the test-helper split.

- [x] implemented
- [x] reviewed

#### Item 5.2: E7 - A double-started manager fails cleanly [parallel with 5.1]

spec.md section: E7

Add `Timeout: managerDBOpenTimeout` to **every** `bolt.Open` in `initDB`, and in
the `openedExistingDB` else-branch return immediately when `errors.Is(err,
bolt.ErrTimeout)`, wrapping the new `ErrDBLocked` sentinel and naming the file -
**before** the restore-from-backup block, never entering it on a lock timeout.
That clause is the whole point: `initDB` treats any `bolt.Open` error as
"corrupt (?) db file", so a naive `Options.Timeout` would let a second manager
unlink the live DB out from under the running one and come up on a stale backup
with the flock protecting nothing. Do not change `reborn`; hardening it would
produce a guard that silently does not apply under `-f`.

`managerDBOpenTimeout` is **30 s** and a package **var**, not a const, declared
near `offlineDBOpenTimeout = 10 * time.Second` rather than inside that block,
carrying the `//nolint:gochecknoglobals` comment the house style uses for a
test-tunable knob (`dependencyResolutionChunkSize`, `dependency.go:69`;
`recoveryHeartbeatInterval`, `server.go:246`). As a const, tests 1-3 each really
wait the full 30 s with a 35 s harness deadline, adding ~90 s to `make test`; as
a var, a test lowers it and the deadline shrinks with it, since the deadline is
written as `managerDBOpenTimeout + 5*time.Second`. E7 records where 30 s comes
from (between the bounded waits inside a shutdown and the 120 s
`daemonStopGiveupS`) and what it costs (a restart overlapping a slow shutdown
can now fail and need retrying).

File: `jobqueue/db.go`. Tests in `jobqueue/depgranularity_dblock_test.go`,
covering all 6 E7 acceptance tests.

**The harness for tests 1-3 is not scaffolding to remove.** Without
`Options.Timeout`, bbolt's `flock` retries every 50 ms for as long as the lock
is held, so each of the three would block until `go test`'s 10-minute panic took
the whole package down with a stack dump rather than a named failure. Each must
call `initDB` on a goroutine and `select` between its result and a
`time.After(managerDBOpenTimeout + 5*time.Second)` deadline, failing on the
deadline branch. That is the same bound the tests assert once the behaviours are
restored. The goroutine must close whatever `*db` it eventually receives, and
the test's release of the held lock must be deferred, so a goroutine left
blocked by the RED run unblocks and lets the file go rather than holding it for
the rest of the package run.

Test 1 also asserts the DB file still exists with the same size, modification
time and **inode**; test 2 that `db_bk` is unmodified and the holder's
subsequent reads and writes still succeed; test 3 that a locked DB with no
`db_bk` still yields `ErrDBLocked` rather than a bare `bolt.ErrTimeout`.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in
the batch together in a single review pass).

### Item 5.3: E2 - `Serve`'s callers wait for publication

spec.md section: E2

Item 5.1 landed `Server.Serving() <-chan struct{}` and its close at publication.
Close it from `beginShutdown` too, so a caller never waits forever on a server
being stopped, and make `cmd`'s `startJQ` (`cmd/manager.go:1324`) wait on it
before `logStarted`.

**Every direct caller that then talks to the server must wait on `Serving()`,
and one that does not fails on every run rather than intermittently**:
publication step 1 sits on `serverListenWait = 10 * time.Millisecond` before
signalling ready, the RPC bind is step 3, and `dialClientSocket` turns a failed
dial straight into `ErrNoServer` with no retry. E2 lists the direct callers at
`8b9ba00` - `startJQ`, the in-package `serve` helper (`jobqueue_test.go:1396`),
the **exported** `client/testing.Serve` (`client/testing/server.go:240`, with 9
in-repo callers), `startStatusTestServer` (`cmd/status_test.go:1319`, 14 call
sites), `startQueueCommandTestServer` (`cmd/suspend_test.go:234`), `TestREST`
(`jobqueue/rest_test.go:920`), the `//go:build ignore` fixture generator
`jobqueue/testdata/dbcompat/gen.go:95`, and `jobqueue/server_startup_test.go:70`
and `:254` which E4 reframes. `measureCompletedHistoryStartup`
(`jobqueue/reliable2_startup_test.go:114`) is the one direct caller that needs
no wait and **must not be given one**: it times `Serve` itself and never
connects.

Each helper keeps its existing retry and adds its own wait; nothing carries the
wait for them. What none of those retries covers any more is the RPC port bind,
which moves past `Serve`'s return into the recovery goroutine - publication
carries that retry on the same 500 ms / 5 s budget (E1). `prepareListener`'s
failures stay on `Serve`'s error return, so the retries still cover those
exactly as today. Split the `serve` helper's current body out as
`serveWithoutPublication` for `pausedRecoveringFixtureServer`
(`jobqueue/reliable2_dbcompat_test.go:304`), which must not wait.

**A missed helper looks like a flake and invites the wrong fix.** A
whole-package `ErrNoServer` in `cmd` or `client` is a helper that never got its
wait, not a load victim. The tempting repair - a dial retry inside `Connect` -
is a production change papering over a test-harness gap, and it would blunt E1
acceptance test 1, which needs `ErrNoServer` back immediately. Fix the helper.

Amend the docs too: `Serve`'s own doc (`server.go:3379-3381`) still says it is
listening for connections by the time it returns, and `jobqueue/doc.go:55-71`'s
Server example runs `jobqueue.Serve` straight into `server.Block()`. `Serve` is
exported and an out-of-repo caller has no other signal.

Files: `jobqueue/server.go`, `jobqueue/doc.go`, `cmd/manager.go`,
`client/testing/server.go`, plus the helper edits in
`jobqueue/jobqueue_test.go`, `jobqueue/rest_test.go`, `cmd/status_test.go`,
`cmd/suspend_test.go` and `jobqueue/testdata/dbcompat/gen.go`. Tests in
`jobqueue/depgranularity_startup_test.go` and
`client/testing/server_test.go`, covering all 6 E2 acceptance tests; F4
acceptance tests 2 and 4 are what prove the helper edits. Test 2's release of
the pause hook once `Stop` has been entered is **required, not tidiness**:
`stopBackgroundStartupTasks` waits on `bgWG` and that wait does not time out, so
a test that skipped the release would pass while leaking a wedged `Stop`, the
held bolt file lock and both held ports into the rest of the package run. Test 6
is a doc and lint check, not a Go test.

- [x] implemented
- [x] reviewed

### Item 5.4: E3 - Shutdown during the window

spec.md section: E3

Three things break when SIGTERM reaches a manager inside the window: add a nil
guard to `shutdownHTTPServer` (`server.go:6970`), which dereferences
`s.httpServer` unguarded; record whether the readers started and skip both the
`close(s.stopClientHandling)` and `waitForClientHandling` in
`closeServerCommsAndDB` (`server.go:6841`) when they did not; and say in the
code that scheduler messages raised during the window are discarded, rather than
losing them by accident (the openstack implementation nil-checks both callbacks,
`caster.Broadcasting` is a no-op and `Send` with no members drops).

A fourth thing does not break but gains a new input state, and E3 names it
because it is easy to walk into: `waitForPortsClosed` (`server.go:6826`,
`:6987-6998`) probes ports the manager may never have bound. Inside the window
that is benign, which is why no production change is needed; it bites only when
something else holds one of those ports, and that is why E1 acceptance test 5
closes its own listener before stopping the server.

File: `jobqueue/server.go`. Tests in `jobqueue/depgranularity_startup_test.go`,
covering all 3 E3 acceptance tests. **Test 1's bound is 2 s, and the figure is
load-bearing**: it must be well under `ServerShutdownWaitTime`, which is exactly
5 s (`server.go:183`), or the test passes whether or not
`waitForClientHandling` was skipped and so never fails pre-fix. Its release of
the pause hook once `Stop` has been entered is required for the same reason as
E2 test 2. The test also asserts the DB file lock is released, by a second
`initDB` on the same file succeeding.

- [x] implemented
- [x] reviewed

### Item 5.5: E4 - The sidecar is the primary operator channel during startup

spec.md section: E4

Recovery observability **moves** from the request surface to the file sidecar;
it does not disappear. Three changes make it the primary channel: drop the `if
upgradedOnOpen` condition on `keepPostUpgradeStartupStatus` (`server.go:3221`)
so the file is written on every start; move its removal off `Serve`'s defer
(`server.go:3467`) to publication step 5 and to the shutdown path; and, because
that uncovers `Serve`'s **error** path where neither runs, keep an error-only
removal on the defer in the existing `closeOnError` idiom (`server.go:3245`).
The PID checks in `currentManagerDBUpgradeStatus` are a backstop for a process
killed without returning, not a licence to leave the file.

Add `Total int json:"total,omitempty"` to `internal.DBUpgradeStatus` and the
four new states `DBStartupPrepareState`, `DBStartupDecodeState`,
`DBStartupDepGroupState` and `DBStartupRecoveryState` alongside
`DBUpgradePostStartupState`. (The spec originally named three; `PrepareState`
was added during implementation because the other three left the initDB ->
`startPriorStateRecovery` span with no sidecar, so a non-upgrade start reported
"non-responsive" for its whole duration.) `Total` is
`omitempty`, so files written when it is unset are byte-identical to today's.
`DBStartupRecoveryState` is written as the **last synchronous statement of
`startPriorStateRecovery`** - after `setRecoveryTotal`, before the `go
s.recoverInBackground(...)`. Written inside `recoverInBackground` instead, it
would usually land before `Serve` returns and occasionally not, turning
acceptance test 2 into a manufactured flake.

The recovery phase is refreshed on the existing `recoveryHeartbeatInterval`
(1 minute) tick plus once on each phase change, so no new timer appears. This
overlaps `.docs/bugfixes/260825-3.md` item 2, which is already fixed at
`8b9ba00`; keep the two consistent and do not restructure the reporter.

**`Processed` cannot climb during the recovery phase, and the sidecar must not
imply that it can.** `recoverPriorJobs` enqueues in a single `AddMany` batch, so
`restored` reads 0 for the whole multi-minute window and then jumps to `total`;
an operator watching `0/150472` would read a hang. So the recovery phase reports
**elapsed time** in `Detail`, refreshed with `UpdatedAt` on each tick; `Total`
is still written; `Processed` keeps its current meaning on the DB-upgrade phases
and is left unset on the recovery phase.

Both committed sidecar tests in `jobqueue/server_startup_test.go` are reframed,
not deleted. `TestServeReportsPostUpgradeStartupUntilTokenReady` (`:70`) moves
its assertion from "gone when `Serve` returns" to "gone when
`<-server.Serving()` returns", since the token FIFO now holds publication.
`TestServeDoesNotReportPostUpgradeStartupForBrandNewDB` (`:254`) is reframed
onto `recoveryPauseHookForTest`, **dropping the FIFO**: the FIFO blocks at
`persistToken`, which is publication step 2, by which time step 1 has already
brought the web port up, so a FIFO window cannot assert the web port is still
closed. The pause hook works even for a brand-new DB, firing in
`recoverPriorJobsWithHeartbeat` (`server.go:1526-1532`) before
`recoverPriorJobs` reaches its `len(priorJobs) == 0` early return.

Files: `internal/db_upgrade_status.go`, `jobqueue/server.go`. Tests in
`internal/db_upgrade_status_test.go` and
`jobqueue/depgranularity_startup_test.go`, covering all 8 E4 acceptance tests:

- Test 1 scopes its round-trip equality to `State`, `Detail`, `Processed` and
  `Total`, because `WriteDBUpgradeStatus`
  (`internal/db_upgrade_status.go:80`) overwrites `PID` and `UpdatedAt` and
  fills a zero `StartedAt`, so no whole-struct comparison can hold. With
  `Total: 0` the JSON contains no `total` key; with `Total: 150472` it contains
  `"total": 150472`.
- Test 4's **present-first half is what makes it discriminate**: pre-change,
  `keepPostUpgradeStartupStatus` writes nothing at all for a DB needing no
  upgrade, so a bare "removed" assertion passes vacuously.
- Test 5 takes two samples during the window with the heartbeat interval
  lowered well under the sampling window, and asserts both report
  `DBStartupRecoveryState` and `Total == 3`, the second's `UpdatedAt` after the
  first's, its `Detail` a strictly larger elapsed time, and **neither** sample
  carrying a non-zero `Processed`. A "between 0 and 3" assertion would be
  satisfied by a constant 0 and would test nothing, which is exactly what a
  `restored`-fed `Processed` would produce.
- Test 7 keeps its name (`prepareDBNeedingStartupUpgrade` is still its fixture)
  but asserts the startup phase rather than the upgrade detail, and needs a
  **state-matching sibling** of `waitForDBUpgradeStatusDetail`
  (`server_startup_test.go:186`), because the recovery phase's `Detail` is an
  elapsed time that changes on every tick.

- [x] implemented
- [x] reviewed

### Batch 2 (parallel, after item 5.5 is reviewed)

#### Item 5.6: E5 - Manager status reads the sidecar [parallel with 5.7, 5.8]

spec.md section: E5

In `managerStatusCmd`, before either of today's wrong outcomes (the daemonized
path's "non-responsive" `die()` at `cmd/manager.go:511`, and the `-f` path's
"stopped"), read the sidecar via the existing `currentManagerDBUpgradeStatus`
(`cmd/manager.go:883`), passing the **zero `time.Time` and `0`**: `wr manager
status` has neither a `preStart` nor a child process handle, so the recorded
PID's liveness is the whole test, and passing `time.Now()` would reject every
sidecar since the file always predates the call. `managerDBUpgradeStatusFresh`
(`cmd/manager.go:108`) is not part of this helper.

If the helper reports a live startup phase, print it and exit 0, with
`Processed`/`Total` when both are set and `Total` plus the phase's elapsed time
otherwise. Build the line in a second helper,
`managerStartupStatusMessage(status internal.DBUpgradeStatus) string`, so the
wording is testable without running the command.

File: `cmd/manager.go`. Tests in `cmd/manager_test.go`, covering all 4 E5
acceptance tests. `managerStatusCmd` itself `die()`s and has no in-process
harness, so the tests drive the helpers it calls, in the pattern that file
already uses (`:274-308`, `:680-706`): swap the package `config` for a
`t.TempDir()` one, write a sidecar with `writeDBUpgradeStatusForTest`, and call
the helper directly. Test 1 uses `Total: 150472` with no `Processed` and asserts
the message says "starting" rather than "stopped" or "non-responsive"; test 2
uses `Processed: 9000, Total: 150472` and asserts the message contains
"9000/150472". **The wiring in `managerStatusCmd` is not reachable that way**,
so F3 procedure step 5 is the only automated exercise of it.

- [x] implemented
- [x] reviewed

#### Item 5.7: E6 - Retain the ErrRecovering machinery [parallel with 5.6, 5.8]

spec.md section: E6

No production change. The decision is to **keep** `ErrRecovering`
(`server.go:101`), its two returns in `getij` (`serverCLI.go:2057`) and
`getijForReport` (`serverCLI.go:2004`), and the `!s.isRecovering()` scheduling
gate (`server.go:4618`), for the three reasons E6 records. What becomes
unreachable is specifically the server-request pathway in production, so
`.docs/reliable2/spec.md` H2's contract is satisfied vacuously rather than
actively.

The two committed tests that assert it connect a client while recovery is
paused - `reliable2_dbcompat_test.go:193` and `:240` - and can no longer connect
at all. Reframe them as server-state assertions using the in-package server,
which needs no listener.

Test file: `jobqueue/reliable2_dbcompat_test.go`. Covers all 3 E6 acceptance
tests: `s.getij(cr, true)` in-package returns an error string that is
`ErrRecovering` and contains neither `ErrBadJob` nor `ErrBadRequest`, with the
`isRecovering()` and `recoveryProgress() == (0, dbcompatIncompleteCount)`
assertions unchanged and only the `Connect`/`Touch` pair replaced;
`s.q.Stats().Items == 0` asserted server-side instead of via a client
`Reserve`, with the client connect and full reserve moved after `release()`,
`waitUntilRecovered(server)` and `<-server.Serving()`; and a live grep proving
all three symbols are still present and referenced, so they are not deleted as
dead code.

- [x] implemented
- [x] reviewed

#### Item 5.8: E8 - A long absence keeps a runner [parallel with 5.6, 5.7]

spec.md section: E8

No production change expected. `ClientRetryTime` being a constant is not the
same as proving a runner survives a long absence and resumes correctly, and the
whole justification for E1 rests on it, so pin it.

Test file: `jobqueue/depgranularity_startup_test.go`. Covers both E8 acceptance
tests: a client holding a reserved job whose `Archive` must retry across a
server stop and a restart with a recovery window longer than one client retry
interval eventually succeeds, the job ends `complete` exactly once, the client
never returned a terminal error, the job is not run twice, and the final queue
holds exactly one item for its key. Drive the timings from `ClientRetryWait` and
a shortened `retryTime` rather than waiting 24 hours.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in
the batch together in a single review pass).

### Item 5.9: E9 - Measure and state the startup window

spec.md section: E9

Measure, on a synthetic DB, the wall time of each published phase separately:
`initDB` (open plus mmap), the live-bucket decode (`db.recoverIncompleteJobs`),
the dependency-group state build (`registerDepGroupMembers`), the dependency
resolution pass, and `enqueueItems`. Record the numbers at three live-job counts
(for example 10k, 50k, 150k) in `.docs/dep-granularity/` and state the resulting
ceiling and its scaling with live-job count. **Do not quote the 37 s and 51 s
production figures as the decode or build cost**: they are
process-start-to-post-scan and include mmapping a 7 GB file.

The production change is one log line per phase carrying an `elapsed` field, at
**warn** level so it appears at the default log level, as `bf53de0`'s recovery
lines do.

Test files: `jobqueue/depgranularity_startup_test.go` (test 1) and
`jobqueue/depgranularity_scale_test.go` behind the `reliability_repro` build tag
(test 2). Covers all 3 E9 acceptance tests. Test 1 uses **100 live jobs** -
enough to produce every phase line, small enough to leave `make test` at its
baseline. Test 2 measures 10k and 50k live jobs of the same shape and asserts
the recorded decode and build durations are within **2x of a linear
relationship** in live-job count, recorded and asserted only as "not
superlinear by more than 2x" so it is not a wall-clock flake on a loaded host;
it sits behind the tag because two full `serve` recoveries over 10k and 50k
live-job DBs would materially lengthen `make test`, which F4 item 4 gates at the
baseline. The 150k point is measured by hand through the same tagged entry
point. Test 3 is the recorded numbers and stated ceiling in
`.docs/dep-granularity/`, required before merge.

**File ownership note.** `jobqueue/depgranularity_scale_test.go` is created here
and F3 (phase 6) adds `TestDepGranularityFixture` to it. Leave room for that
rather than assuming the file is E9's alone.

- [x] implemented
- [x] reviewed

## Phase gate

- `go vet ./jobqueue/ ./cmd/ ./client/...` clean; `make lint` at 0 issues.
- This phase's acceptance tests pass, plus phases 1-4's gates.
- `jobqueue`, `cmd`, `client` and `client/testing` green: E2's helper changes
  are the only thing keeping the last three green after E1, so a whole-package
  `ErrNoServer` there is a missed helper, not a flake.


## Phase 5 outcome (2026-08-26)

Implemented, **FAILED first review with two blocking findings**, fixed, and
passed re-review. Gates on the final tree: `make lint` 0 issues, `go vet -tags
netgo` and `-tags 'netgo reliability_repro'` clean across all five package sets,
`make test` **481 passed - 9 skipped - 29 packages** four times across the two
review passes at load 117-119, `-race` clean, `cleanorder -min-diff -dry` clean
apart from the two documented pre-existing spots.

### Blocking 1: E1 could kill runner subprocesses

`withRunnerServer` (`runner_lifecycle_test.go`) spawned `--runnermode` with no
`-test.run` filter, so the runner subprocess ran the whole suite before reaching
`runner()` - including an unguarded test that starts a server on the **default
development port**, because `jobqueueTestInit` deliberately skips
`isolateTestConfig` in runnermode. Under E1 a lost bind is no longer an error a
helper can retry: publication burns its budget and calls `publishExit` ->
`os.Exit(1)`, killing the runner before it reaches the entrypoint.

Proven both ways, twice, with port 46407 held by an unrelated listener:
`TestJobqueueRunnerLostJobs` FAILs at ~180 s with repeated `err="exit status 1"`
without the filter, and passes in seconds with it. The class was **illegible as
well as broken** - `silenceExpectedRunCmdWaitLogs` swallows the killed-runner
log, so only a downstream assertion fails.

Fixed by adding `-test.run '^TestJobqueueRunnerModeEntrypoint$'`, placed after
`--runnermode`+`failArg` so the silencer's prefix match still holds. An
independent sweep confirmed the other four spawn sites already carry a filter,
and that the two using the *broad* `-test.run TestJobqueue` are safe: of ~60
matching tests, exactly two lack the `runnermode || servermode` guard, and both
are the dispatch entrypoints themselves.

Narrowing the silencer was considered and declined: the obvious narrowing breaks
`TestJobqueueRunnerScheduling`, and in `TestJobqueueRunnerFailureRetry` a
deliberate exit-1 and a `publishExit` exit-1 are indistinguishable at the
`log15.Record` level. With the filter in place the runner subprocess can no
longer reach a `serve()` at all, so the class is closed at source.

**Correction to the earlier diagnosis.** The `-test.run` fix applied to
`TestJobqueueRunnerScheduling` was right, but the reason recorded with it was
not: startup stagger is real and pre-existing, but the dominant new cost was E1
killing runner processes. Phase 5 adds no pre-entrypoint CPU of its own - all 26
new test functions carry the guard.

### Blocking 2: the startup window was reported as a database upgrade

`managerDBUpgradeStatusLogMessage` branched only on the post-upgrade state, so
the new states fell through to the else and `wr manager start` printed
`wr manager is upgrading its database: recovering prior state, 21m0s elapsed`
once per heartbeat for the whole window. Worse, `newStartupStatusReporter` wrote
the post-upgrade state on **every** start, so a normal start also claimed
"starting after database upgrade" for the pre-recovery span. Exactly the defect
class E5 exists to remove, in the channel E4 makes primary; nothing caught it
because coverage existed only for the old state.

Fixed in three parts: the three DB-upgrade phase strings became named constants
with an `IsDBUpgradeState` predicate (replacing nine literals in `db.go`); the
message helper now defaults **unrecognised** states to "wr manager is starting",
so a state added by a later wr cannot fall through to the lie again; and
`newStartupStatusReporter` takes `upgradedOnOpen` so the post-upgrade phase is
written only when an upgrade really ran. `upgradedOnOpen` was traced to its
source and is true only when a real index rebuild ran on an existing DB.

`TestManagerDBUpgradeStatusLogMessage` is now table-driven over all eight
reachable states plus an unrecognised one; flipping the default arm back fails
five rows by name.

### Spec amendments made (2026-08-26)

- **E4 now names four states.** `DBStartupPrepareState` covers initDB-returning
  to `startPriorStateRecovery`; without it that span had no sidecar and a
  non-upgrade start reported "non-responsive" throughout.
- **E5 acceptance test 2 was asserting a state wr cannot produce.** Its
  `Processed: 9000, Total: 150472` fixture requires both writers at once, but
  they are disjoint: the startup reporter never sets `Processed`, and the
  upgrade reporter never sets `Total`. Totalling `rebuildJobLookupEntries` would
  need a second full `ForEach` over three buckets during the slowest phase of an
  already-slow start - rule 6 forbids it. The reachable "9000 so far" form is
  now the named case; the combined form is retained as forward-looking, since
  `noteRecovered` is already additive if the enqueue is ever batched.

### A third no-op mutation, resolved by deletion

The three `startupStatusReporter` nil guards could not be made to fail by any
mutation. Rather than add a test for an unreachable case, they were dropped, and
the reachability was verified independently: `shutdown` returns early unless
`beginShutdown` sees `s.up`, `up: true` appears in one production literal (the
same one that sets `startupStatus`), and none of the seven test files building
`&Server{up: true}` calls `Stop`/`shutdown`/`beginShutdown`/`closeServing`;
`report`/`refresh` are reachable only from `Serve`-built servers.

**This is deliberately the opposite of phase 4's `depGroupMembers` design**,
which panics on nil on purpose to catch a real production path. Both are right;
do not unify them.

### Carried forward

- **E2 acceptance test 4** (`wr manager start -f` prints "started on" only after
  the bind) has no automated test - `startJQ` `die()`s and daemonizes, so it is
  not in-process reachable. The production change is present and correct
  (`cmd/manager.go:1438`). Either add a seam-level test or record it in the spec
  the way E5 records its own unreachable wiring.
- **No sidecar during `initDB` itself** on a non-upgrade start, so `wr manager
  status` still dies "non-responsive" for that span - the largest remaining hole
  in "the sidecar is the primary operator channel", and the one phase whose
  production cost the measurements do not bound. A phase written before
  `bolt.Open` would close it.
- `ctx` means bgCtx inside `recoverInBackground`/`startPriorStateRecovery` but
  Serve's ctx inside `publishServingSurface`; both documented, still confusing.
- `depgranularity_startup_test.go` carries `//go:build !windows` while using
  nothing Unix-specific, and the untagged `reliable2_dbcompat_test.go` now
  depends on helpers defined there.
- `TestServeReportsPostUpgradeStartupUntilTokenReady` no longer asserts any
  post-upgrade state; the spec sanctions keeping the name.
- Newly observed load flakes, each passing 3/3 in isolation on both trees:
  `TestJobqueueRunnerResourceLearning` (one-bucket `PeakRAM` rounding),
  `internal`'s `TestConfig` (mock-stdin port calculation), `network/port`'s
  `TestPort` (host FD/port exhaustion).
