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

- [x] developers/soak/latency.py joins runner outcome lines to E markers by
  (host, pid), but pids are reused (144 host+pid pairs had >1 run), giving
  bogus 7740s maxima.
  - Source: battery10 F6 (RESULTS.md, Phase 4; old output
    `soak/analysis/latency.txt`).
  - Red command (battery10 soak data, read only):
    `latency.py $O $B/run/runnerlogs`, exit 0 with the bogus maxima:

    ```text
    outcome    set            n    p50    p90    p99      max    >5s   >30s
    archive    all       936012   3.30   8.03  38.96   7740.8 257345  14358
    archive    steady    833641   3.27   7.35  13.68   7740.8 218951   1385
    release    all         9544   3.33   8.90  86.58   7717.5   2757    317
    release    steady      8407   3.24   7.59  17.76   7717.5   2237     54
    ```

    A synthetic fixture (one host's pid 100 runs job X, ending 05:00:00 and
    acknowledged 2s later, then job Y at 07:09) gave `archive all 2 ... max
    7741.0`.
  - Cause: the runner-log index was a dict keyed by (host, pid) holding one
    outcome, so a later run with a reused pid overwrote the earlier run's,
    and the earlier run's E marker was paired with it.
  - Files: `developers/soak/latency.py`.
  - Approach: key outcomes by (host, pid, kind, id) from the `started
    executing` line, keep every outcome, and pair an E marker with the first
    one from a second before its time (outcome lines are whole seconds).
  - Green: on the same data:

    ```text
    archive    all       936008   3.30   8.03  38.84    215.7 257318  14295
    archive    steady    833643   3.27   7.35  13.65    215.7 218933   1332
    bury       all          133   3.15   7.94 519.44    553.6     36      4
    bury       steady       111   2.86   6.91  12.53     16.9     27      0
    release    all         9542   3.33   8.90  85.89    581.8   2757    316
    release    steady      8405   3.24   7.58  17.60    253.6   2237     53
    E markers without an outcome line: {'0': 15940, 'sigINT': 15, '3': 4, 'sigTERM': 4}
    ```

    The 7740s and 7717s maxima are gone and the per-10-minute maxima are at
    most 582s (were up to 7741s); p50/p90 are unchanged and p99 moved by
    under 1s. Six more exit-0 E markers have no outcome line (15934 before),
    being the runs that had borrowed a reused pid's outcome. The fixture
    gives max 2.0.
