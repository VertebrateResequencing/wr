# Incidental bugfixes 2 (branch `incidental-fixes-2`)

Bugs found while working on the LSF-scale reliability branch (`reliable4`, PR
#555), split out so that branch stays on-topic and these are not held up behind
it.

**Filename convention.** The repo allocates checklists as `YYMMDD-N.md`, so
parallel branches keep reaching for the same next number and colliding as an
add/add conflict on files with no shared content - it has happened three times
now (`reliable4` vs develop's `260828-1.md`, and #558 vs #559 both adding
`260829-1.md`). This file carries an `-incidental` suffix no sequence allocator
will generate. Anything added here appends to this one file.

Quality gates (run with ALL `OS_*` unset, or they take ~16 minutes and run the
OpenStack tests): `make test`, `CGO_ENABLED=1 make race`, `make lint`.

## Already on develop, not repeated here

PR #555 merged as `4e5739fc`, bringing `.docs/bugfixes/260827-2.md` with it, so
the three items this file was created to carry are on develop already with
their evidence intact. Re-checked against develop `41a04a26` on 2026-09-17, all
three still unfixed:

- item 13, the temp-dir leak on *successful* runs. `internal/config_test.go`
  still has 9 `os.MkdirTemp("", ...)` calls,
  `jobqueue/scheduler/scheduler_test.go` 15, plus `wr_signal_marker` at
  `jobqueue/jobqueue_test.go:1943`.
- item 14, the unconditional writes to fixed shared `/tmp` paths:
  `jobqueue/jobqueue_test.go:8333` (`/tmp/ccfmod`) and `:8409` (`/tmp/csmod`).
- item 16, `wr limit -g <group>` reporting `math.MaxInt64` instead of `-1`.
  `handleGetSetLimitGroup` still does `int(limit.Limit())`
  (`jobqueue/serverCLI.go:1884`).

That file's "Further run-state leaks across a reservation" section records
`HostIP`, `FailReason`, `StdOutC`/`StdErrC` and `State` surviving a
reservation. Still true: `resetJobForReservation`
(`jobqueue/serverCLI.go:1051`) clears `RunnerPid` and not those four. Two
things that section does not say, which decide how to approach it:

- the structural fix is worth more than the four field fixes. One list, or one
  of the two lists deriving from the other, so the next run-describing field
  cannot drift between `resetJobExecutionFields`
  (`jobqueue/serverWebI.go:930`) and `resetJobForReservation`. Decide that
  before patching entries individually.
- do NOT change `State` without checking `wr status`'s consumers: the web UI
  reconciles absolute state client-side, and a reserved job reading its
  previous state may be load-bearing there.

## Moved off this branch

The `TestJobqueueSignal` lost-state entry that this file carried has moved to
branch `fix-start-durability`, where it is being fixed rather than recorded.
It was the one item here that needed a change to the manager's write path, and
this branch records rather than fixes, so keeping it here would have meant
correcting the record in one PR and fixing the defect in another.

Its open counterpart on develop is BUG 20 in `.docs/bugfixes/260829-1.md`,
whose suggested "Next" step (disk load) that work disproves.

## Open

- [ ] **`make lint`'s verdict depends on a local ref nothing keeps current,
      and says nothing when that ref is missing.** Recorded in place of a
      finding that turned out to be an artefact of exactly this - see the
      correction below.

      `.golangci.yml` sets `new-from-rev: master`, so `make lint` reports only
      what is new since the LOCAL `master` branch. Nothing updates that ref: a
      clone made long ago keeps whatever `master` pointed at then, and `git
      fetch` does not move a local branch. Measured on 2026-09-17:

      | clone's local `master` | `make lint` |
      |---|---|
      | `b2f0ff97`, i.e. `origin/master` | `0 issues.` |
      | `3caeba4d`, an ancestor of it (this clone) | 2 issues |
      | absent | 44 issues |

      The last row is the worst of the three: with the ref missing,
      golangci-lint does not error, it silently falls back to reporting the
      whole tree. A fresh clone therefore fails its first `make lint` with 44
      findings in code nobody touched, and the fix is to create a ref rather
      than to change any code.

  - suggested direction: have the gate name a revision that is always right -
    `origin/master`, as `.github/workflows/golangci-lint.yml` already does for
    CI - or fail loudly when the configured rev does not resolve. The choice
    between "same as CI" and "stricter than CI on purpose" is the maintainer's;
    the defect is that today it is neither reliably, and quietly so.

  - **CORRECTION.** This entry first claimed `make lint` was red on develop in
    two places, `jobqueue/behaviours.go:323` (funlen, 21 > 20) and
    `jobqueue/modify_validation_test.go:421` (gci), and blamed CI's
    `--new-from-rev=origin/master` for not catching them. That was wrong. Both
    constructs are already present on `origin/master` (`#547` is an ancestor of
    it), so neither is new relative to it, and a clone whose `master` is at
    `origin/master` reports `0 issues.` both with the config default and with
    CI's invocation. They surfaced here only because this clone's `master` is
    the older `3caeba4d`. Nothing is red on develop; the reporting is.

  - Note for whoever runs the gates: a `golangci-lint run` straight after
    editing a file in this clone also reported two G703 hits in
    `jobqueue/behaviours_test.go` that #580 had already suppressed with
    `#nosec`. `golangci-lint cache clean` removed them - the shared module path
    across the sibling `wr` clones poisons the cache, exactly as the earlier
    checklists warn.

## Found in review of this branch

- [x] **A `WR_TEST_SERVER_LOG` that cannot be opened is ignored, and the
      reason is discarded.** `logServerToFileIfAsked` logs the open failure
      with the standard library logger and returns, so the daemon comes up
      with no log and the developer who asked for one gets no explanation:
      `startServer` (`jobqueue/jobqueue_test.go:1287`) leaves `cmd.Stdout` and
      `cmd.Stderr` nil, and `os/exec` connects a nil `Stderr` to `os.DevNull`.

      Raised by Copilot on PR #600, thread `4036106276`, comment on
      `jobqueue/jobqueue_test.go:1333` at `dd7666f8`.

  - red: build the test binary and start the daemon with an unopenable path.
    The daemon starts anyway, so `timeout` has to kill it (exit 124):

    ```bash
    go test -tags netgo -c -o /tmp/jq.test ./jobqueue/
    WR_TEST_SERVER_LOG=/tmp/probe/no/such/dir/log.txt \
      WR_MANAGERDIR=/tmp/probe/mgr WR_MANAGERPORT=44411 WR_MANAGERWEB=44412 \
      timeout 12 /tmp/jq.test -test.run TestJobqueue --servermode; echo $?
    ```

    ```text
    2026/09/17 12:08:44 failed to open WR_TEST_SERVER_LOG ...: no such file or directory
    t=... lvl=warn msg="test daemon up, will block"
    exit=124
    ```

  - wanted: a log that was explicitly asked for and cannot be provided stops
    the daemon promptly, rather than being ignored for the rest of the run.

  - FIXED in `jobqueue/jobqueue_test.go`. `logServerToFileIfAsked` returns the
    open error instead of swallowing it, and `runServer` turns that into
    `clog.Crit` + `os.Exit(1)`, the same fatal idiom its two other startup
    failure paths already use. The decision happens before any config load or
    port bind, so the daemon dies in milliseconds. Unset, the hook still
    returns early and a normal run is unchanged.
  - The old `#nosec G706` went with the `log.Printf` it annotated, and no new
    gosec finding replaced it: `make lint` reports no issue this change is
    responsible for, at either of the baselines the item above describes.
  - Regression test `TestJobqueueServerLog` drives the real binary as its own
    `--servermode` child with an unopenable path and asserts the process
    boundary: exit 1, the reason on combined output, and no `test daemon up`.
    Proven red both ways - with the fix it passes in 0.01s, and with
    `runServer` reverted to `_ = logServerToFileIfAsked(ctx)` it fails after
    30s on `context deadline exceeded`.

- [ ] **A `--servermode` daemon's fatal reason never reaches the developer.**
      Found reviewing the fix above, and pre-existing rather than caused by it.
      `startServer` (`jobqueue/jobqueue_test.go:1287`) leaves `cmd.Stdout` and
      `cmd.Stderr` nil, so `os/exec` wires both to `os.DevNull`. Every fatal
      path in `runServer` - a failed `os.Executable()`, a failed `serve()`, and
      now a failed log open - writes its reason there and it is lost. What the
      developer sees is the parent timing out in `readManagerToken`.

  - the fix above deliberately does not address this: it makes the run fail
    fast and unambiguously instead of silently carrying on, which is a
    different thing from saying why.
  - smallest treatment is `cmd.Stderr = os.Stderr` in `startServer`, which
    exposes every daemon-fatal reason rather than one. That trades against the
    test output noise a blocking daemon produces, so it is a choice about the
    suite's output rather than a defect fix - hence recorded, not done.
    Capturing into a buffer and surfacing it on the `readManagerToken` error
    avoids the noise at the cost of touching a path many tests share.
