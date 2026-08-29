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

- [ ] The web UI can show a completed job as **running for ever**.

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
