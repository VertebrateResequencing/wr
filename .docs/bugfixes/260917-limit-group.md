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

  - Fixed in `limiter/group.go` and `jobqueue/serverCLI.go`, 14 lines. The
    saturation stays where scheduling needs it: `GroupData.Limit()` is untouched
    and its two other callers, both of them `jobqueue/db.go` persisting a count,
    are untouched with it. A new `GroupData.LimitForDisplay()` next to it returns
    the count for a count group and -1 for every other kind, and
    `handleGetSetLimitGroup` reports that instead. So the one place that knows
    MaxInt64 is a scheduling convention rather than a limit is still the limiter,
    and `serverCLI.go` gained no knowledge of group modes.
  - Both MaxInt64 cases report -1, as the help text already promised for the
    unknown one, and nothing at the CLI needs to tell them apart: a group nothing
    knows a limit for and a group whose limit was just removed are the same
    unlimited group, and `wr limit` with no options already omits both from its
    listing. A group limited by time of day rather than by a count reports -1
    too, which is the same answer the listing gives by leaving it out.
  - Regression test `TestLimitGroupReport` in
    `jobqueue/limit_group_report_test.go`, on the existing `dgrStartServer`
    fixture. It asserts on what a real client is told by `GetOrSetLimitGroup`,
    never on `GroupData`, for all five cases the CLI can reach: an unknown group,
    a group set to 5 and read back, a group set to 0 and read back (so -1 cannot
    be reached by treating any falsy limit as unlimited), a group removed with
    `:-1` and read back after, and a time-of-day group.
  - Item 10's `TestReliable4LimitGroupRemoval` needed no change. It discards
    `GetOrSetLimitGroup`'s limit (`_, err := jq.GetOrSetLimitGroup(group)`) and
    asserts on the listing, on `bucketLGs` and on the limiter's remaining
    capacity, none of which this fix touches. It passes unchanged, and the real
    `wr limit` with no options still lists a `:0` group and omits a removed one.
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
  | `handleGetSetLimitGroup` reports `int(limit.Limit())` again | `limit_group_report_test.go:68` Expected -1, Actual 9223372036854775807 (also `:81` and `:89`) |
  | `LimitForDisplay` returns -1 for every group | `:72` Expected 5, Actual -1 |

  **Gates** (host load average 3-6, 8 cores). `make lint`: 2 issues, exactly the
  pre-existing `jobqueue/behaviours.go:323` funlen and
  `jobqueue/modify_validation_test.go:421` gci. `make test`: **668 passed - 20
  skipped - 29 packages - 6m17s**, PASSED, no flake. `CGO_ENABLED=1 make race`:
  668 passed - 19 skipped - 29 packages - 9m53s, PASSED. `cleanorder -min-diff`
  is a no-op on all three edited Go files.

  Note for whoever runs the gates next in a fresh clone: `.golangci.yml` sets
  `new-from-rev: master`, and this clone had no local `master`, so
  `golangci-lint` silently fell back to reporting every issue in the tree (44).
  `git branch master origin/master` (both at `3caeba4`) restores the 2-issue
  baseline. The branch was created for that reason and nothing else.
