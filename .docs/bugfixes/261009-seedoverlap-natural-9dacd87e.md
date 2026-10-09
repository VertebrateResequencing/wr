# Natural seed-overlap shape fails after the exact seed (2026-10-09)

- Branch: `seedoverlap-6f57a39c`
- Base: `origin/develop` at `6fcbf4ed` (#689)
- Worktree: `../wr-seedoverlap`
- Queue owner: this branch, this checklist; found by the release sweep on
  `6fcbf4ed`.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: the build-tagged `TestReliable4StatusSeedOverlap*` tests,
`developers/wrdev.sh status-seed-overlap`, `make lint`, tagged `go vet`. Only
a test and a developer script change, so no `make speed`.

## Prior checked items that must not regress

- `260820-2.md` Bug D and D2: the seed is bracketed by `begin`/`end`
  boundaries and nothing interleaves them (`TestReliable4StatusSeedBoundary`,
  `TestReliable4StatusSeedOverlap`).
- `261008-status-late-delta-09b3ffda.md`: the pump drops a delta the seed
  already counted (`TestStatusLateDeltaAfterSeed`).

## Items

- [x] `developers/wrdev.sh status-seed-overlap` fails on the release
      candidate `6fcbf4ed`: `TestReliable4StatusSeedOverlapNaturalRace`
      prints `--- FAIL` with no visible assertion, while its numbers look
      right (true_running=4 shown_running=4, overcount 0).
  - Source: release sweep, `04-status-seed-overlap.out`.
  - Red command: `go test -tags "netgo reliability_repro" ./jobqueue/
    -run 'TestReliable4StatusSeedOverlapNaturalRace$' -count=10 -v`, exit 1,
    8 of 10 runs fail at host load 10 to 12 on 8 cores:

    ```text
    SEED-OVERLAP-REPRO natural overcount_boundary_aware=0 overcount_boundary_blind=0
    * jobqueue/reliable4_seedoverlap_test.go
    Expected '0' to be greater than '0' (but it wasn't)!
    --- FAIL: TestReliable4StatusSeedOverlapNaturalRace (10.30s)
    ```

    The failing assertion is `So(blindErr, ShouldBeGreaterThan, 0)`: the
    test required a client that ignores the seed boundary to over-count, so
    it had something to compare against. wrdev.sh's output filter hid the
    assertion line.
  - Cause: a test assumption that #687 made false, not a product fault.
    #687 made the seed a point in time (`queue.Snapshot` reads every item's
    state and the change sequence in one lock hold). Before it, the seed
    walk read each item's state after letting go of the queue's lock, so a
    move during the walk was counted by the walk and by its delta, for both
    clients alike. On `f89d28d5` (#687's parent), 10 runs: shown-client
    over-count 3 to 8 (equal to the blind client's) in 9 runs, 0 in 1, so
    the blind precondition held 9 of 10 times. On `6fcbf4ed`, 30 runs: the
    shipped client's over-count is 0 every time and the blind client's is 0
    or 1 (it is only wrong when a delta lands between the websocket joining
    the caster and the seed, microseconds in-process). Its test failure rate
    was 8 of 10 under load; the earlier pass on `f9380b6c` predates #687.
  - Fix: the natural shape now asserts the shipped client's running bar is
    exact (`awareErr == 0`), which replaces the blind precondition and the
    `>= 0` and `<= blind` bounds it implied; the blind replay is printed for
    comparison only. Doc comments, the Convey title and wrdev.sh's text no
    longer describe an accepted walk residual. wrdev.sh's gate checks
    `aware == 0` instead of `aware <= blind`, and its output filter now
    keeps GoConvey's assertion lines.
  - Mutants on `6fcbf4ed` with the new test: `queue.Snapshot` reading item
    states after releasing the queue lock fails 6 of 6 (over-count 2 or 3);
    disabling the pump's sequence drop still passes 10 of 10, because
    in-process the change callback goroutine nearly always runs before the
    seed. That drop stays pinned by `TestStatusLateDeltaAfterSeed`.
  - Files: `jobqueue/reliable4_seedoverlap_test.go`, `developers/wrdev.sh`.
  - Gates: the red command passes 20 of 20 (with the forced shape, at load
    7 to 11), shipped-client over-count 0 every run. `developers/wrdev.sh
    status-seed-overlap` with an isolated `WRDEV_ROOT`: PASS 3 of 3, rc 0.
    `make lint`: 0 issues. `go vet -tags "netgo reliability_repro"
    ./jobqueue/`: clean.
