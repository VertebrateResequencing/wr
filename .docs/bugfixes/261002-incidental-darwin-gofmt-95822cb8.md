# Incidental issues found while fixing perf regressions (2026-10-02)

- Branch: `fix-incidental-darwin-gofmt-95822cb8`
- Base: `origin/develop` at `8207049c` (rebased from `6ec427cd`)
- Queue owner: `fix-perf-regressions`,
  `.docs/bugfixes/261002-perf-regressions.md`
- Origin: `fix-perf-regressions`, `.docs/bugfixes/261002-perf-regressions.md`,
  item 3 (JobCleanup); reported by its implementor and reviewer.
- Independent of the origin item: neither touches cleanup code, and both
  reproduce on unmodified `origin/develop`.

- [x] jobqueue's tests do not compile on darwin.
  `GOOS=darwin go vet ./jobqueue/` fails with
  `jobqueue/depgranularity_startup_test.go:1135: undefined: pscBindRetryBudget`.
  `pscBindRetryBudget` is defined only in the linux-only
  `jobqueue/port_selfconnect_test.go`.
  - Seen on `c9d9c5fa`. Already fixed on this branch's base `6ec427cd`
    (#660), which defines it in `jobqueue/server_startup_test.go`:
    `GOOS=darwin go vet ./jobqueue/` exits 0. Nothing to do here.
- [x] `gofmt -l jobqueue/` lists `jobqueue/modify_validation_test.go` and
  `jobqueue/server.go` as not gofmt-formatted (a struct field alignment in
  server.go's depGroups and a composite literal in the test). `make lint` does
  not flag them.
  - Drained 2026-10-02 on this branch, worktree `wr-hygiene`.
  - Red: `gofmt -l $(git ls-files '*.go')` lists both files (plus two
    `.docs/` harness files that Go tooling and golangci-lint skip);
    `golangci-lint fmt --diff` exits 1 with exactly the two jobqueue diffs.
  - Why `make lint` misses it: `.golangci.yml` enables the `gci` and
    `goimports` formatters, but `new-from-rev: origin/master` keeps only
    issues on changed lines. The drifted lines (server.go `depGroups`,
    from `4e5739fc`, already on master) were unchanged; a neighbouring field
    added in `c9d9c5fa` broke their alignment.
  - Fixed: formatted both files with `golangci-lint fmt`, and the `make lint`
    target now runs a whole-tree `golangci-lint fmt --diff` (the configured
    gci and goimports formatters, not baseline-scoped) before
    `golangci-lint run`, failing with a pointer to `golangci-lint fmt`. CI
    runs `make lint`, so it gets the check too. Files: `Makefile`,
    `jobqueue/server.go`, `jobqueue/modify_validation_test.go`.
  - Green: `golangci-lint fmt --diff` exits 0; `make lint` exits 0. With a
    longer const added to the block in `queue/queue.go` (misaligning its
    unchanged neighbours), `golangci-lint run ./queue/...` still reported
    0 issues while `make lint` failed at the format step (exit 2).
- [ ] Recurring flake TestManagerStopWhileShuttingDown in cmd: "helper
  manager did not become ready" after 5.06s (helper exited early), seen
  2026-10-02 in `make test`. Earlier record in
  `.docs/bugfixes/260929-archive-before-start.md` (64s timeout; suspected
  cause: the helper's port taken as another connection's source port).
  - Source: caller-assigned incidental item.
  - Mechanism: the helper's port comes from `closedLocalPort()` in
    `cmd/manager_stop_test.go`, which binds `localhost:0` and closes it, so
    the port is in the kernel's ephemeral range (32768-60999 here). Any other
    process's `:0` listener or outgoing connection can then be given it
    before the helper binds. `reservePort` retries a port with a listener
    for `serverBindRetryBudget` (5s), then Serve fails and the helper exits
    2, matching the 5.06s report; a port taken as a connection's source port
    is retried for `serverBindLingerBudget` (90s), matching the earlier 64s
    report.
  - Red (mechanism, diagnostic build only): holding a dual-stack listener on
    the chosen port before the helper starts reproduces the report exactly.
    `TestManagerStopWhileShuttingDown` failed after 5.23s; helper exit status
    2, helper log:

    ```text
    lvl=warn msg="could not reserve the manager port yet, retrying" port=46363 err="bind: address already in use"
    lvl=eror msg="helper manager failed to start" err="manager port 46363 is in use by another process: bind: address already in use"
    Actual:   'helper manager did not become ready'
    --- FAIL: TestManagerStopWhileShuttingDown (5.23s)
    ```

    25 unpinned runs while another process held 2000 rotating `:0`
    listeners all passed, so the natural collision is rare; the red command
    for the fix pins the property that prevents it instead.
