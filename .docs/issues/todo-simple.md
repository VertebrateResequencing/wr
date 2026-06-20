# Simple Todos

Straightforward, unambiguous items — the implementation is determined and needs no
product decisions. Items are grouped into proposed **branches** (the section
headers). Each branch batches work that can be implemented and reviewed together —
either closely related or individually trivial — so we amortise the
local-tests → CI → Copilot-review cycle instead of paying it per tiny change.

Tick the checklist as each branch progresses.

## fix/small-fixes-batch

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

Three trivial, independent fixes batched into one PR to amortise overhead. #507 and
#324 are small Go changes with unit tests; #310 is a frontend-only change that needs
manual/browser verification.

- **[#507 Option to only add the first x command in a file as a test](https://github.com/VertebrateResequencing/wr/issues/507)** — add a `--head N` flag to `wr add` that keeps only the first N parsed commands when reading the command file (in `cmd/add.go`, where `parseCmdFile` builds the job list). `0` (default) means all. Add a unit test.
- **[#324 Strange start/end time issue with quick-running dependent jobs](https://github.com/VertebrateResequencing/wr/issues/324)** — in `jobqueue/job.go` `ToStatus()` (~lines 978/983) emit `StartTime.UnixNano()` / `EndTime.UnixNano()` instead of `.Unix()`, so sub-second jobs no longer report `Started == Ended` despite a non-zero `Walltime`. The newer push path already uses `UnixNano` (`server.go:2847`) and the `JStatus.Started/Ended` fields are `*int64`, so this is a pure reporting fix. (Dependency ordering is already guaranteed structurally by the queue — no behaviour change there.)
- **[#310 Status webpage: times out](https://github.com/VertebrateResequencing/wr/issues/310)** — in `jobqueue/static/js/wr/websocket-handler.js`, on `onclose` attempt reconnection with a capped backoff, and on successful reconnect re-send `{Request:"current"}` to resync counts/state. Today it only reports the connection loss and requires a manual refresh.

## fix/openstack-reserved-quota-leak

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#453 wr manager in cloud mode fails to update stats of reserved cpus when OS image is not available](https://github.com/VertebrateResequencing/wr/issues/453)** — confirmed live bug. `opst.spawn` increments `reserved{Instances,Cores,RAM,Volume}` (`jobqueue/scheduler/openstack.go:866-871`) and the only decrement is `usingQuotaCB`, which the provider goroutine runs after `usingQuotaCh <- true` (`cloud/openstack.go:953`). But `getImage`/`getFlavor` failures return early (`cloud/openstack.go:874-882`, "no OS image..." at `:355`) before that signal, so the callback never fires (also leaking a goroutine) and the scheduler's error-return path (`openstack.go:1022-1041`) doesn't decrement either — so every bad-image spawn permanently leaks the reservation until quota is exhausted and the manager locks up. **Fix:** guarantee the reservation is released on every early-return error path (e.g. have `cloud.Provider.Spawn` invoke the callback on its error return, or release in the scheduler's error branch / via `defer`). Add a regression test using the existing `debugEffect` mock-spawn hooks (`jobqueue/scheduler/openstack.go` ~`:900-905`, `:1012-1014`). No OpenStack environment is needed to reproduce or test.

## chore/error-wrapping-sweep

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#301 Update error handling](https://github.com/VertebrateResequencing/wr/issues/301)** — mechanical modernisation: replace `err == someErr` comparisons (other than `nil` / `io.EOF`) with `errors.Is`, replace `err.(*T)` type assertions with `errors.As`, and add `%w` wrapping where it adds useful context. **Scope note:** a full-repo sweep would be far too large for one reviewable PR (Copilot will refuse), so split the work one top-level package per PR (e.g. `jobqueue`, then `cloud`, `cmd`, `fs`, `queue`, ...). The checklist above tracks the overall effort; expect several PRs under this branch theme.
