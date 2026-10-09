# soakgate counts a killed run as a double (2026-10-08)

- Branch: `soakgate-5c0eec66`
- Base: `origin/develop` at `bad1bc32` (#687)
- Worktree: `../wr-soakgate`
- Queue owner: item 9 of `.docs/reserve-runstate/delivery-queue.md`.

Quality gates: `bash developers/soak/testdata/soakgate/run.sh` (every
fixture case). The repository defines no Python or Markdown lint; `make lint`
runs `golangci-lint` on Go code only, and this change touches no Go.

## Prior checked items that must not regress

- `261008-clean-stop-double-run-c9dfbc33.md` item A records the evidence
  this item acts on. The earlier `soakgate.py` rules are pinned by
  fixture cases 01 to 30 of `developers/soak/testdata/soakgate/`.

## Items

- [x] soakgate.py should not count a first run as a double when its runner
  logged "killed by user request" or the job was buried before its next run
  (the soak job script records exit 0 before exiting, so a stop's kill can
  land between); test, then run the tool end to end on the existing soak
  evidence
  - Source: `261008-clean-stop-double-run-c9dfbc33.md` item A (baseline
    soak keys 643290... and 61c6ea5c...).
  - Red: fixture case `31-killed-not-double`. A.1's first run has an `E`
    status-0 marker, and its runner then logged "kill requested externally"
    and "jobqueue Execute(<key>): killed by user request" before the rerun.
    E.1 is the same, with the kill in the rerun's start second. Doubles that
    must still count: B.1 (no kill), C.1 (kill logged after the rerun
    began), D.1 (a kill line for another key) and F.1 (a kill of an earlier
    run by the same runner, before this first run started).
    `bash developers/soak/testdata/soakgate/run.sh`, exit 1:

    ```text
    FAIL 31-killed-not-double: output differs from expected.txt
    -doubles inside=0 outside=3 acknowledged=0
    +doubles inside=0 outside=5 acknowledged=0
    +double portal_compress A.1 outside reserved=4000 acknowledged=no
    -killed portal_compress A.1 outside reserved=4000
    -killed portal_qc E.1 outside reserved=4800
    +double portal_qc E.1 outside reserved=4800 acknowledged=no
    FAIL: 1 of 31 soakgate cases
    ```

    (The case later gained rows F.1 to N.1, so `expected.txt` now has
    `inside=0 outside=11 acknowledged=5`.)
  - Which signal: the runner's exact line
    `msg="jobqueue Execute(<key>): killed by user request"` alone. That
    line comes only from the kill branch of `classifyReleasedExit`
    (`jobqueue/client.go`) after the bury report succeeded. The kill branch
    needs a non-zero wait status and sets `dobury`; a wait status of 0
    always archives. Kill, close, behaviour and unmount errors accumulate
    in `myerr`, but `combineExecOutcomes` returns the command's outcome
    whenever it buries, and `myerr = outcome.myerr` drops them, so a bare
    kill line can follow any of those. `Execute` returns the bare reason
    only when nothing is added after classification: no high-memory note,
    `hadProblems` false (else `; recovered on a new server; you should stop
    reserving`) and no `signalledAfterExit` (else a signal error leads). `buryKilledBeforeStart` also builds
    `Execute(<key>): killed by user request`, but wrapped in
    `command [...] was not started: ...`, which `KILLED` does not match.
    The runner logs the error as `warn("%s", err)` (`cmd/runner.go`). So
    the line proves the command did not exit 0 and that wr buried the job.
    That is how this fix covers the bug text's "buried before its next
    run": a buried job runs again only when kicked or retried, so the rerun
    is the user's request, not wr's. No separate bury evidence is in
    soakgate's inputs: the manager log has no per-job bury line, and
    `dbstart.tsv` holds only the final state. The wrapped and suffixed
    forms are not treated as kills, so their rows still count as doubles.
    That is the conservative direction: it can only over-report. Every kill
    line in both soaks' runner logs is the exact form (10,561 in
    `soak-base`, 8,696 in `soak-change`). The kill line must be of the first
    run's own reservation: in its runner log, after that reservation's
    `reserved a job` line and before the log's next one. It must also be
    logged no later than the rerun's start, in whole seconds. A kill logged
    after the rerun began cannot explain it. That case is a real double.
    A kill of any other reservation by the same runner says nothing about
    the first run, so its row counts.
  - Fix: `developers/soak/soakgate.py` reads each runner log's exact
    `msg="jobqueue Execute(<key>): killed by user request"` lines. A
    doubles row whose chosen reservation has one for its key, as above, is
    left out of the `doubles` counts (cycle 4 gives the final rule). It is printed as
    `killed <kind> <id> <inside|outside> reserved=<t>` after the `double`
    lines. `developers/soak/README.md` and the `run.sh` list of equivalent
    mutants say so too.
  - Mutants that each fail case 31 (cycle 1; cycle 4 removed the lower
    bound): no key check, no lower bound, no upper bound, and an exclusive
    upper bound.
  - End to end, with this tree's `soakgate.py` and the invocations recorded
    in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/analysis/analyse.sh`
    (`--source warning` for `soak-base`, `--source d1` for `soak-change`;
    inputs `run/current`, `run/runnerlogs`, and `analysis/<soak>/doubles.tsv`
    and `dbstart.tsv`). Output went to scratch only.
    - Baseline, before: `doubles inside=665 outside=2 acknowledged=0`,
      doubles `20261007T074510.6291` and `20261007T074510.20444`. After:
      `doubles inside=665 outside=0 acknowledged=0`, and both print
      `killed ... outside`. No other line changed.
    - Change soak, before and after, identical:
      `doubles inside=606 outside=1 acknowledged=0`,
      `double portal_dedupe 20261007T115143.17261 outside
      reserved=1791372335 acknowledged=no` (key d1582bc1..., the pre-#686
      completion lost to the resource-check wait). Its runner logged
      "aborting due to signal", not a kill, so it still counts.
  - Gates: `bash developers/soak/testdata/soakgate/run.sh`, exit 0,
    `PASS: 31 soakgate cases`.
  - Review follow-up (cycle 2): an exclusive lower bound survived, and so
    did `KILLED` without its closing quote, without its `msg="jobqueue `
    prefix, or taking any key.
    - Case 31 gained three rows. G.1 runs from x000700 to x000900 ms and is
      killed in second x000, so it must print `killed`. H.1 has the kill
      reason suffixed with `; recovered on a new server; you should stop
      reserving`. I.1 ran OK, was reserved again by the same runner, and was
      killed before its command started (the `was not started` wrapping).
      H.1 and I.1 must still count as doubles.
    - Each of these mutants now fails case 31: exclusive lower bound (G.1
      prints `double`), no closing quote (H.1 prints `killed`), no prefix
      (I.1 prints `killed`). The earlier four still fail. `KILLED` taking
      any key survives and is listed in `run.sh` with `RES` and `OK`, since
      keys are 32 hex. `run.sh` also says why the prefix and quote are
      pinned rather than listed.
    - `soakgate.py`'s docstring now names the exact line and says the
      wrapped and suffixed forms still count as doubles. The checklist's
      "Which signal" note is corrected as above.
    - Gate: `run.sh` exit 0, `PASS: 31 soakgate cases`. End to end with the
      same invocations: baseline output identical to the cycle-1 "after"
      (`outside=0`, both `killed`); change soak identical to "before"
      (`outside=1`).
  - Review follow-up (cycle 3): two mutants of the counting survived. One
    left out killed rows only outside a stall; another hard-coded `outside`
    in the `killed` line. A third counted a row that was both acknowledged
    and killed. The "Which signal" note and `soakgate.py`'s docstring also
    overclaimed: a bare kill line can follow a failed kill, close,
    behaviours or unmount, since `Execute` drops those errors when the
    command's outcome buries (checked in `jobqueue/client.go`).
    - Case 31 gained J.1, reserved at 1050 inside the 1000 to 1170 s stall
      and killed with the exact line: it must leave `inside=0` and print
      `killed portal_trim J.1 inside`. It also gained K.1: run A ran OK and
      was acknowledged, then the same runner reserved it again as B, which
      was killed with the exact line, and C is the retry on another host.
      The (A, B) row counts as an acknowledged outside double. (Cycle 4
      found that excluding the (A, C) row hid a genuine double, and made it
      count.)
    - Each mutant now fails case 31: killed rows left out only outside a
      stall (`inside=1`), `outside` hard-coded in the `killed` line (J.1
      prints `outside`), and acknowledged-and-killed rows counted
      (`outside=8 acknowledged=3`; cycle 4 makes that the right answer).
      The seven earlier mutants (no key check,
      no lower bound, no upper bound, exclusive upper bound, exclusive lower
      bound, no closing quote, no `msg="jobqueue ` prefix) still fail.
    - The "Which signal" note above and the docstring now say which errors
      `Execute` drops and which additions change the line. The README says "by the time the rerun
      began". `run.sh` lists `KILLED` taking any key on its own, without the
      trailing-space claim, and names the high-memory note among the
      suffixes.
    - Gate: `run.sh` exit 0, `PASS: 31 soakgate cases`. End to end with the
      same invocations: baseline output identical to cycle 2
      (`inside=665 outside=0`, both `killed ... outside`); change soak
      identical to cycle 2 (`inside=606 outside=1`).
  - Review follow-up (cycle 4): the rule could hide a genuine double. It
    took any same-key kill line anywhere in the runner's log between the
    two starts, even one of a different reservation by that runner. In the
    reviewer's K.1 variant, the re-reservation B was killed before
    `psimjob.sh` wrote its S marker, so only the (A, C) row existed, and it
    printed `killed`. In the F.1 variant, an earlier run's kill fell in the
    same second as the next reservation, which ran OK and was rerun on
    node-9; that row printed `killed` too.
    - Rule now: the kill line must be of the first run's own reservation.
      `scan_runner_log` keeps, per reservation, the times of exact kill
      lines for its key logged after its `reserved a job` line and before
      the log's next one (or none, for a reservation that is not a
      `psimjob.sh` command). A row is killed if one of those is no later
      than the rerun's start second. The lower bound at the first run's
      start is gone, since the reservation's own lines cannot precede it.
    - A reservation that logged `command ran OK` was archived, so it never
      has the kill line. A killed row can still be acknowledged, since the
      acknowledgement rule reads any OK line of the key in that log between
      the two starts: an earlier reservation's OK in the first run's start
      second does it. Such a row is counted nowhere.
    - Case 31: K.1's (A, C) row now counts (`double portal_call K.1 outside
      reserved=5800 acknowledged=yes`, twice). I.1 no longer pinned the
      prefix, since a wrapped `was not started` kill has no S marker and so
      is never a first run; it became the reviewer's K.1 variant (A ran OK,
      the same runner's B started and was killed with the exact line before
      any marker, C on node-9), which must count. New rows: L.1 (the F.1
      variant, kill and next reservation both at 6005), which must count as
      an acknowledged double; M.1, killed with the signal error leading
      (`runner received a signal to stop; jobqueue Execute(<key>): killed by
      user request`), which must count; N.1, whose second run B was killed
      with the exact line after A's OK in B's start second, so the (B,
      node-9) row prints `killed` and is acknowledged, while (A, B) counts.
      node-13's log starts with a non-`psimjob.sh` reservation killed with
      the exact line: each soak's runner logs hold 1,357 non-`psimjob.sh`
      reservations (relbury and rundep), 6 of them killed with the exact
      line, so the kill branch's `seg is not None`
      guard is a real rule. G.1 now pins that a kill in the reservation's
      own second belongs to it; E.1 still pins the inclusive upper bound.
      `expected.txt`: `doubles inside=0 outside=11 acknowledged=5`,
      `missing ran=14`, five `killed` lines (A, E, G, J, N).
    - Red: with the cycle-3 `soakgate.py`, case 31 printed `outside=8
      acknowledged=2` and `killed` for I.1, K.1 and L.1.
    - Mutants, each failing case 31: whole-log kills with no reservation
      scoping (F, I, K, L print `killed`), the cycle-3 `[s1, s2]` window,
      scoping by time instead of log position (`[res, next)`, `[res,
      next]` and `res <= t` fail L.1; `(res, next]` fails G.1), no key
      check (D.1), no upper bound, exclusive upper bound (E.1), no closing
      quote (H.1), no `msg="` prefix and no `msg="jobqueue ` prefix (M.1),
      no `seg is not None` guard on the kill branch (crash), killed rows
      counted, inside or outside or acknowledged counted over all rows
      (J.1, `outside=16`, N.1), killed only when not acknowledged (N.1),
      `double` lines over all rows, `outside` hard-coded or `reserved`
      dropped in the `killed` line, and no `killed` lines. Survivors, listed
      in `run.sh`: `KILLED` taking any key (keys are 32 hex and must equal
      the reservation's), and the `killed by user request` substring
      pre-check (the regex requires it). `run.sh` now says M.1 and H.1 pin
      the prefix and quote, and calls the signal form a prefix.
    - `soakgate.py`'s docstring, the README row ("the exact line", "that
      run's own reservation") and the "Which signal" and "Fix" notes above
      describe the new rule.
    - Gate: `run.sh` exit 0, `PASS: 31 soakgate cases`. End to end with the
      same invocations (output in `/tmp/claude-11346/soakgate-e2e/c4-*`):
      baseline identical to cycle 3 (`doubles inside=665 outside=0
      acknowledged=0`; `20261007T074510.6291` and `.20444` print `killed`),
      2 to 0 against "before"; change soak identical to cycle 3
      (`inside=606 outside=1`, the same `double portal_dedupe
      20261007T115143.17261` line).
