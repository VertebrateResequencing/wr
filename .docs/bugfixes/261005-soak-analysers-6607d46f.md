# Bugfix: prodsim build memory and soak analysers

- Branch: `soak-analysers-6607d46f`
- Base: `origin/develop` at `6ed235880a4c04f4386afe1293a07e26dd4478c2`
- Queue owner: `soak-analysers-6607d46f`, this checklist
- Worktree: `../wr-soakanalysers`

## Items

- [x] developers/prodsim/actors.go uses wrstatUIBuildRAM (1500) as both the
  memory the psimjob holds and the job's RAM requirement, so on LSF the first
  attempt of every wrstat-ui build is killed TERM_MEMLIMIT and wr retries at
  1600MB.
  - Source: battery10 F4 (overnight battery RESULTS.md, Phase 4, A1 resolved:
    `bhist -n 0` shows LSF job 846119 `runner -s '1500:180:1:0:...'` "Exited
    by LSF signal TERM_MEMLIMIT", MAX MEM 1.4GB vs MEMLIMIT 1.4G; soak runner
    logs show the build in groups 1500 x1 and 1600 x4).
  - Red command:
    `go test -tags netgo --count 1 ./developers/prodsim/ -run TestEveryJobHoldsLessMemoryThanItRequests`,
    exit 1:

    ```text
    Line 73:
    Expected '1500' to be less than '1500' (but it wasn't)!
    --- FAIL: TestEveryJobHoldsLessMemoryThanItRequests (0.64s)
    ```

  - Cause: the build's `jobCmd` memMB argument was `wrstatUIBuildRAM`, the
    same value as its `Requirements.RAM`. psimjob.sh then holds that many MB
    in perl, plus perl's own memory, which crosses LSF's MEMLIMIT.
  - Files: `developers/prodsim/actors.go`, `developers/prodsim/actors_test.go`.
  - Approach: a named `wrstatUIBuildMemMB = wrstatUIBuildRAM * 2 / 3` (1000MB)
    is what the build holds. The new test runs every job-adding actor
    (ibserver, fofn, wrstat, wrstatui, portal, waiter) against a test manager,
    waits until it holds a job of every psimjob kind, and asserts each job's
    held memory is below its RAM requirement. The other kinds were already
    below: put 200/1024, fofnput 300-999/1024, walk 800/1000, combine
    1500/2000, portal RAM/3, pipeline 100/200, tidy/publish/ctrdep 0, and
    psimjob.sh's own stat jobs 400 of `-m 500M`.
  - Green: the red command passes; `go test ./developers/prodsim/` plain and
    `-race` pass, the new test passes `--count 5`, and
    `golangci-lint run ./developers/...` reports 0 issues.

- [x] developers/soak/anyway.py only counts per-job "handing the job out
  anyway" warnings, but the manager rate-limits that warning (one full line
  per minute per key + a "(repeated) repeats=N ... sample_key=..." summary),
  and a crash loses the pending summary; it also takes the runner's next
  outcome line rather than the run's own. So it labelled 373/374 doubles
  "NOT-anyway".
  - Source: battery10 F5 (RESULTS.md, Phase 4; old output
    `soak/analysis/anyway.txt`).
  - Red command (battery10 soak data, read only; `B=battery10/soak`,
    `O=$B/run/prodsim-1791171218`):
    `anyway.py $O $B/run/runnerlogs $B/analysis/doubles.tsv $O/manager.log`,
    exit 0 with the wrong verdicts:

    ```text
    "handing the job out anyway" warnings: 38; mapped to a psimjob: 38
         3  first run near crash@1791174584             reservation NOT-anyway
        59  first run near crash@1791179326             reservation NOT-anyway
       311  first run near no-outage                    reservation NOT-anyway
         1  first run near no-outage                    reservation anyway
    runner outcome lines after the first run, for doubles NOT preceded by the warning:
        59  command ran OK
    ```

    A synthetic fixture (one double whose first reservation came 10s after
    the runner asked and whose reports were rejected after a crash, one whose
    reservation was prompt) gave NOT-anyway for both, and listed the second
    run's "command ran OK" as the first run's outcome.
  - Cause: the warning is aggregated under one key for every job
    (`persistReservation` calls `s.warns.warn` with the message as the key),
    so one minute's thousands of non-durable reservations name one key in
    full and one in the summary's `sample_key`; the script also matched only
    `key=`, ignored summaries, and gathered outcome lines for every run of
    the job's key within 15 minutes of the first run.
  - Files: `developers/soak/anyway.py`, `developers/soak/README.md`.
  - Approach: find the first run's own runner log segment through its S
    marker's (host, pid) and the runner's `started executing ... pid=` line,
    and call the reservation handed out anyway when the `reserved a job` line
    is at least ReserveWriteWait (10s) after the runner's previous line
    (`slow`), when a later report of that run was rejected as "bad job" or
    "you must Reserve()" after a stop in `restarts.tsv` (`lost`), or when a
    full or summary warning names its key within 60s (`warned`). Outcome
    lines come only from that segment. Summaries' `repeats=` are totalled,
    and any number of overlapping manager log copies are read once each line.
  - Green: on the same data, with `$O/manager.log $O/manager.log.17*`:

    ```text
    "handing the job out anyway" warnings: 38 full lines + 32 summaries of 23548 repeats = 23586 reservations handed out before they were on disk (less any summary a crash lost)
         3  first run near crash@1791174584             reservation anyway     (slow+lost)
        59  first run near crash@1791179326             reservation anyway     (slow+lost)
       311  first run near no-outage                    reservation anyway     (slow+lost)
         1  first run near no-outage                    reservation anyway     (slow+lost+warned)
    ```

    All 374 are anyway, matching RESULTS.md's manual analysis (314 in the
    FUSE stall, 59 at the 06:48:46 crash). No first run's own outcome is
    "ran OK" any more; 59 are "killed for server error: jstart(K): bad job",
    the line RESULTS.md found by hand. The fixture gives
    `anyway (slow+lost)` and `NOT-anyway (-)`.
