# Incidental bugfixes (branch `incidental-fixes`)

Bugs found while working on something else, kept off the LSF-scale reliability
branch so that branch stays reviewable and this work is not held up behind it.

**Filename convention for this branch.** The repo allocates checklists as
`YYMMDD-N.md`, so several branches in flight will each reach for the same next
number and collide as an add/add conflict on a file with no shared content - which
is exactly what happened between `reliable4` and develop's `260828-1.md`. This
file therefore carries a `-incidental` suffix that no sequence allocator will
generate. Anything added here appends to this one file; it does not take a new
number.

Quality gates (run with ALL `OS_*` unset, or they take ~16 minutes and run the
OpenStack tests):

- `make test`
- `make race` (needs `CGO_ENABLED=1`)
- `make lint`

Note the test-suite cleanup fixes (temp-dir reaping, fuse-mount reaping, the
shared-`/tmp` deletion) live on the `reliable4` branch, not here, so a suite run
in this clone leaks `/tmp/wrtest*` directories until those land.

- [x] The web UI can show a completed job as **running for ever**.

      A job that finishes within ~1-50 ms of starting can have its job-details row
      left in the running state after it has completed, until the user navigates
      away. Nothing heals it: the only interval in the UI is a walltime ticker, and
      no push update follows a terminal state.

      Chain: `queue.changed` (`queue/queue.go:565`) runs each transition's change
      callback in its own goroutine; only the `running` transition waits, in
      `waitForJobStartTime` (`jobqueue/jobtransition.go` ~268, polling 1ms x 50),
      so `complete` can overtake `running`. `statusFromSubscriptionUpdate`
      (`jobqueue/serverWebI.go:305`) then sets `status.State` from the
      **transition's** state rather than the fresh snapshot's, so the late message
      really does say `running`, and `mergeJobDetailsPushUpdate`
      (`jobqueue/static/js/wr/websocket-handler.js:431`) is `Object.assign`, so
      `State` is last-write-wins.

  - found while fixing a load flake in `TestJobSubscriptions` on the `reliable4`
    branch (`.docs/bugfixes/260827-2.md` items 12 and 15 there). That test was the
    product telling us the feed does not promise per-job ordering; the test fix
    stopped asserting an ordering the product does not provide, and this is the
    user-visible half.
  - **fixed client-side, deliberately.** `DEVELOPERS.md` rule 10 is the precedent:
    the web status-bar flicker/overcount was fixed purely client-side in this same
    file with no server change, to keep the server's reliability constraints
    untouched. The server-side alternative - not overwriting the snapshot's
    `State` - was rejected because `hasClientSubscriptionsForJobUpdate` filters on
    the transition state, so a subscriber asking for `running` would start
    receiving `complete` payloads, changing subscription semantics.
  - a worse symptom than first recorded: `handleJobDetailsMessage` then calls
    `setupLiveWalltime`, and `utility.js:241` starts a 1-second ticker whenever
    `job.State === "running"`. So the stale row does not merely say running - its
    walltime counts up for ever.
  - the chain was confirmed against the running product, not by reading alone:
    `TestLateRunningSubscriptionUpdatePayload` drives a real server through
    add/reserve/execute to completion, then asks `statusFromSubscriptionUpdate`
    for a `running` update on that key. The payload comes back `State: running`
    with `Exited: true` and a non-nil `Ended` - self-contradictory, exactly as
    predicted.
  - **The discriminator is payload self-consistency, not ordering:**
    `update.State === 'running' && update.Exited === true`. `State` is the only
    field taken from the transition; every other field is a fresh snapshot read at
    write time, so when they disagree, believe the snapshot.
  - "never leave a terminal state" was rejected: a job legitimately returns to
    running on `wr retry`, a dependency re-run or a lost-job re-attempt.
    `Attempts` was rejected because `resetCompletedJobForRerun` sets it to 0, so
    it is not monotonic and would reject the legitimate case. `Started` was
    rejected because heartbeat updates carry no `Started`/`Ended`, so the old
    run's timestamp would persist. `Exited` works because
    `resetJobForReservation` clears it at Reserve, before a job can be running
    again - so running+Exited is unreachable for a genuine attempt and guaranteed
    for the stale duplicate, since `archiveCompletedJob` writes to the DB before
    `q.Remove` fires the complete callback.
  - a gate on the EXISTING row being terminal was written and then removed: no
    test justified it, and it makes a real case worse - a failed job released for
    retry shows `ready`, and its own stale `running` should still be dropped.
  - red: `go test -tags netgo ./jobqueue -run TestStatusPageStaleRunningPushUpdate`,
    driving the real handler in a node `vm` context, which is the mechanism the
    flicker fix used and so runs inside plain `make test`. Both clauses of the
    predicate were mutation-tested: dropping the `State` check fails "the complete
    push update was not applied"; dropping `Exited` fails "a genuine re-run must be
    able to leave a terminal state".
  - gates: `make test` 360 passed / 9 skipped, `make browser-test` all 8 fixtures,
    `make lint` 0 issues.
  - NOT fixed, recorded instead: a stale `running` after a job is **deleted**
    mid-flight. That job is in neither the queue nor the complete bucket, so
    `statusFromSubscriptionUpdate` falls back to `statusFromJobUpdate`, whose
    payload has no `Exited` field at all. A rarer shape, and guessing at it would
    have meant an unjustified clause.
