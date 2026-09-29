# Buried dependent comes back dependent after a restart

- [x] A BURIED job whose dep group (dep_grp it depends on via --deps) has an
  incomplete member comes back as "dependent" after a clean manager restart,
  instead of staying buried. The spec (.docs/dep-granularity/spec.md, B2) says
  a bury leaves it buried with the new dependencies and only a kick makes it
  dependent. Impact: the job silently loses its buried state across a restart
  and would run automatically once the dep-group member completes. Owner
  policy: "stop means buried". In the soak: a running dependent D was killed by
  a clean stop (killed jobs are buried) after a new member B was added to its
  dep group; B was also killed and buried; after restart D showed dependent
  (Attempts 1, "killed by user request") while B stayed buried. Pre-existing:
  reproduces on 8aadcdd6, before #649.
  - Source: soak repro `developers/soak4/rundepkill.sh` modes 2 and 3 (in the
    wr-soak4 clone); after `wr manager stop` and start, `wr status -i rdD`
    printed `Status: dependent on other jobs` while rdB stayed buried.
  - Red command (exit 1 before the fix):
    `GOFLAGS=-p=2 go test -tags netgo -count 1 ./jobqueue -run TestBuriedDependentRestart`
    and `go test -tags netgo -count 1 ./queue -run BuryStartQueue`
  - Red output excerpt:

    ```
    Expected: jobqueue.JobState("buried")
    Actual:   jobqueue.JobState("dependent")
    (x5: clean and crash restarts, B ready or buried, D buried before or
    while B joined)
    --- FAIL: TestBuriedDependentRestart (1.18s)
    Actual:   queue.ItemState("dependent")
    --- FAIL: TestAddManyBuryStartQueueWithDependencies (0.00s)
    Actual:   queue.ItemState("dependent")
    --- FAIL: TestAddBuryStartQueueWithDependencies (0.00s)
    ```
  - Root cause: `Server.recoveredItemDef` starts a recovered buried job with
    `StartQueue` `queue.SubQueueBury` and its resolved dependencies, which
    include the incomplete member. `queue.AddMany` (`addManyItem`) and
    `queue.Add` (`handleItemForAdd`) honoured the start queue alongside
    dependencies only for `SubQueueSuspended`, so the item went to the
    dependent sub-queue. Clean and crash restarts share this path. #649's
    `recoverRerunMark` only clears the rerun mark for a job not recovering into
    run, so it is not a separate cause.
  - Fix: `queue/queue.go` adds `holdsDependencies`. An item added with
    dependencies and a bury or suspended start queue starts there with its
    dependencies recorded, as a live buried item re-blocked by `q.Update` is.
    A kick then makes it dependent, and a dependency satisfied while it is
    buried leaves it buried. The old suspended special cases fold into the same
    path. Doc comments for `Add` and `ItemDef.StartQueue` are updated.
  - Tests: `jobqueue/buried_dependent_restart_test.go`
    (`TestBuriedDependentRestart`: clean and crash restarts, member ready or
    buried, D buried before or while the group gained a member) and
    `queue/bury_dependencies_test.go` (Add and AddMany with bury start queue
    plus dependencies, and a dependency satisfied while buried).
  - CHANGELOG entry under Unreleased, Fixed.
