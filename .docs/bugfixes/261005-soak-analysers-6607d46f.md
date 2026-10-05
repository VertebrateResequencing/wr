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
