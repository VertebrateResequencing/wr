# Incidental issues found while fixing perf regressions (2026-10-02)

- Branch: `fix-incidental-darwin-gofmt-95822cb8`
- Base: `origin/develop` at `6ec427cd`
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
- [ ] `gofmt -l jobqueue/` lists `jobqueue/modify_validation_test.go` and
  `jobqueue/server.go` as not gofmt-formatted (a struct field alignment in
  server.go's depGroups and a composite literal in the test). `make lint` does
  not flag them.
