# Bugfixes 2026-09-17

Filename deliberately not `260917-1.md`: three sibling branches are being cut
today, and a `YYMMDD-N.md` name would collide as an add/add conflict, which
`.docs/bugfixes/260903-1-incidental.md` records happening three times already.

- [x] `wr limit -g <group>` prints 9223372036854775807 instead of -1 for a
      group with no limit.

  Recorded as item 16 of `.docs/bugfixes/260827-2.md`, quoted verbatim:

  > **16. MOVED OFF THIS BRANCH. `wr limit -g <group>` prints
  > 9223372036854775807 instead of -1 for a group with no limit**,
  > contradicting `cmd/limit.go`'s own help, which says "Groups that are not
  > known about will report -1."
  >
  > `handleGetSetLimitGroup` (`jobqueue/serverCLI.go:1892`) does
  > `int(limit.Limit())` and `GroupData.Limit()` returns `math.MaxInt64` for any
  > non-count GroupData. Verified against a real client and server:
  >
  > ```
  > wr limit -g zztmp-unknown -> 9223372036854775807
  > wr limit -g zztmp-g:5     -> 5
  > wr limit -g zztmp-g:-1    -> 9223372036854775807
  > ```
  >
  > Pre-existing and untouched by item 10, which deliberately asserted on
  > `wr limit`'s listing rather than this path so as not to bake the wrong value
  > into a test.
  >
  > **DEFERRED until after the rebase**, same rule as items 13 and 14: the fix
  > is in `jobqueue/serverCLI.go`, which BOTH queued PRs edit.

  It gained urgency from #555, which made `name:-1` delete the limit record, so
  every removed group now reports MaxInt64 rather than only groups nothing ever
  knew about.

  Intended behaviour: -1 means "no limit" to the user, for an unknown group and
  for a known group with no count limit alike. Both are MaxInt64 internally, and
  the help text already commits to -1 for the unknown case, so both report -1.

  - Red command, driving the real client and manager on a private manager
    directory and ports 31719/31720:

    ```bash
    CGO_ENABLED=0 go build -tags netgo -o /tmp/sb10-limitgroup/wr .
    export WR_MANAGERDIR=/tmp/sb10-limitgroup/mgr WR_MANAGERPORT=31719 \
      WR_MANAGERWEB=31720 WR_DEPLOYMENT=development
    ./wr manager start --deployment development
    ./wr limit --deployment development -g zztmp-unknown
    ./wr limit --deployment development -g zztmp-g:5
    ./wr limit --deployment development -g zztmp-g
    ./wr limit --deployment development -g zztmp-g:-1
    ./wr limit --deployment development -g zztmp-g
    ./wr limit --deployment development
    ./wr manager stop --deployment development
    ```

    Output at `41a04a2`:

    ```
    === unknown group ===
    9223372036854775807
    === set 5 ===
    5
    === get zztmp-g ===
    5
    === set -1 ===
    9223372036854775807
    === get zztmp-g after removal ===
    9223372036854775807
    === listing ===
    ```

    Three of the six reads are wrong: the unknown group, the `:-1` removal, and
    the read of the removed group all want -1. The `:5` set and its read-back
    are the control, and the empty listing is item 10's behaviour, which must
    not change.

  - Fixed in `limiter/group.go` and `jobqueue/serverCLI.go`, 17 lines.
    `GroupData.Limit()` is untouched, and so are its only two callers, both of
    them `jobqueue/db.go` persisting a count inside an `IsCount()` branch. A new
    `GroupData.LimitForDisplay()` next to it returns the count for a count group
    and -1 for every other kind, and `handleGetSetLimitGroup` reports that
    instead. The knowledge that MaxInt64 stands in for "no limit" stays inside
    the limiter, and `serverCLI.go` gained no knowledge of group modes.
  - What MaxInt64 is NOT: scheduling does not read it. `canIncrement()`,
    `capacity()` and `GetRemainingCapacity()` all work from the group's own mode
    and counts, so after this fix nothing in the tree reaches `Limit()`'s
    `math.MaxInt64` branch at all. It stays because `Limit()` is exported and an
    outside consumer may saturate on it; collapsing it would change the exported
    API's semantics, which is a separate decision from this bug.
  - Both MaxInt64 cases report -1, as the help text already promised for the
    unknown one, and nothing at the CLI needs to tell them apart: a group nothing
    knows a limit for and a group whose limit was just removed are the same
    unlimited group, and `wr limit` with no options already omits both from its
    listing. A group limited by time of day or by absolute date-time, rather than
    by a count, reports -1 too, which is the same answer the listing gives by
    leaving it out.
  - Regression test `TestLimitGroupReport` in
    `jobqueue/limit_group_report_test.go`, on the existing `dgrStartServer`
    fixture. It asserts on what a real client is told by `GetOrSetLimitGroup`,
    never on `GroupData`, for all five cases the CLI can reach: an unknown group,
    a group set to 5 and read back, a group set to 0 and read back (so -1 cannot
    be reached by treating any falsy limit as unlimited), a group removed with
    `:-1` and read back after, and a time-of-day group.
  - Item 10's `TestReliable4LimitGroupRemoval` did not need changing to keep
    passing: it discarded `GetOrSetLimitGroup`'s limit and asserts on the
    listing, on `bucketLGs` and on the limiter's remaining capacity, none of
    which this fix touches. Its two post-restart reads now also assert the
    scalar the server reports (`rl4rmReportedLimit`, the first read after each
    restart, so it is the call that makes the fresh limiter vivify the group from
    `bucketLGs`): the limit that survived a restart reports 3, and the removed
    one reports -1. That is the only path `TestLimitGroupReport` does not reach,
    since it never restarts a server. Nothing else in that test changed.
  - Not changed: `cmd/limit.go`'s help, which already says -1; the wire protocol,
    where `serverResponse.Limit` is still an `int` carrying the same -1 the
    client's doc comment has always promised; and `Limiter.GetLimits()`, which
    the listing uses and which already skipped non-count groups.

  Output after the fix, same commands, plus a `:0` group to show 0 is still
  itself:

  ```
  === unknown group ===
  -1
  === set 5 ===
  5
  === get zztmp-g ===
  5
  === set -1 ===
  -1
  === get zztmp-g after removal ===
  -1
  === set 0 ===
  0
  === listing ===
  zztmp-z: 0
  ```

  **Mutation mapping:**

  | Reverted | Assertion that fails |
  | --- | --- |
  | `handleGetSetLimitGroup` reports `int(limit.Limit())` again | `limit_group_report_test.go:69` Expected -1, Actual 9223372036854775807 (also `:82` and `:90`) |
  | `LimitForDisplay` returns -1 for every group | `:73` Expected 5, Actual -1 |
  | the same revert, against item 10's test | `reliable4_limit_removal_test.go:85` Expected -1, Actual 9223372036854775807 |

  **Lint baseline: `0 issues.`, and check your `master` ref before believing
  anything else.** `.golangci.yml` pins `new-from-rev: master`, so `make lint`
  reports only what is new relative to whatever the local `master` ref points at.
  This clone's was stale (`3caeba4`, v0.37.1), which made `make lint` report 2
  issues - `jobqueue/behaviours.go:323` funlen and
  `jobqueue/modify_validation_test.go:421` gci - that an earlier run of these
  gates recorded as pre-existing. They are phantoms of the stale ref: both
  constructs are already on the real `origin/master` at `b2f0ff9`, so neither is
  new. With `master` fetched up to `b2f0ff9`, `make lint` is `0 issues.` A clone
  with no local `master` at all is worse and quieter: `golangci-lint` then falls
  back to reporting every issue in the tree, 44 of them here. Fetch `master` to
  `origin/master` before reading a lint result.

  **Gates**, with `master` at `b2f0ff9` and `WR_TEST_PORT_BASE` set to keep off
  the sibling clones' ports (23011 for test, 23511 for race):

  - `make lint`: **0 issues.**
  - `make test`: **PASSED - 668 passed, 20 skipped, 29 packages, 7m48s.**
  - `CGO_ENABLED=1 make race`: **PASSED - 668 passed, 19 skipped, 29 packages,
    10m11s**, on a re-run. The first attempt failed six tests
    (`TestSubscriptionBoundedIsolatedBuffer`, `TestSubscriptionReconnectResync`,
    `TestJobqueueExecutionAndDependencyScenarios`, `TestSchedulerSubmitJobsAndWait`,
    `TestJobqueueSignal`, `TestClientExecuteLiveTouchPayloads`), none of them
    anywhere near limit groups and none reporting a port error. Host load, not a
    defect: a sibling clone was running its own race suite at the same time, the
    15-minute load average over that run was 18 against 8 cores, and one of the
    six failures is a bare clock reading - `jobqueue_test.go:1730` wanted a
    measured 3601-3630 and got 3644.58. The re-run at load ~5, with no other
    suite running, passed every one of them.
  - `cleanorder -min-diff` is a no-op on all four edited Go files. It wanted
    `rl4rmReportedLimit` moved below `limitGroupRecorded`, and that move is
    applied.

- [x] Copilot review on PR #602 (thread comment `4039267018`): the
      `LimitForDisplay` doc comment in `limiter/group.go` says "the two places
      that call Limit()", but there are three.

  Verified. `grep -rn '\.Limit()' --include='*.go' .` lists three call sites
  against a comment that says two: `jobqueue/db.go:274` and
  `jobqueue/db.go:1388` in production, and
  `jobqueue/reliable4_add_tx_test.go:255` in a test. The comment's point
  survives: `db.go:274` is inside an `IsCount()` branch, `db.go:1388` is reached
  only through that branch, and the test asserts `IsCount()` first. Only the
  count is wrong, and any count goes stale when a caller is added or removed.

  Reworded to say wr's own callers of `Limit()` are all reached only after an
  `IsCount()` check, so they only ever see a real count. It no longer says
  "to persist a count", which the test caller does not do. The MaxInt64
  saturation convention and the `canIncrement()`/`capacity()` point are
  unchanged.

  Comment-only change with no behaviour change, so per testing-principles'
  Cleanup and Removal rule no new test is appropriate; the existing `limiter`
  tests and the limit-group `jobqueue` tests are the gate.
