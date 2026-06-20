# Simple Todos

## Workflow — how to tackle this file

Execute this file with **one orchestrator agent** that fans the sections out to
**parallel section agents** (one per `##` branch section below). These mechanics have
been verified to work in this environment: the `develop` branch exists; isolated
`git worktree`s created off `origin/develop` work; subagents can spawn their own
subagents; subagents can invoke the `bugfix` and `pr-resolver` skills; `gh` can push,
open PRs, and comment on / close issues; and the `Monitor` tool can watch a PR for
merge.

**Section agent** — spawn one per branch section, all in parallel (several `Agent`
calls in a single message), each given its branch name and item list. It:

1. Makes an isolated checkout off the latest develop and creates the branch (run
   `git fetch origin develop` first):
   `git -C <main-repo> worktree add /tmp/wr-work/<branch> -b <branch> origin/develop`.
   All work happens inside that worktree.
2. Implements the section's items by invoking the **`bugfix` skill** (it runs TDD via
   its own implementor/reviewer subagents and auto-commits each fix). Make the
   affected package's tests and linter pass locally.
3. Ticks **Implemented** in its own section below. (Each section's checkboxes are
   distinct lines, so parallel edits don't collide; re-read and retry if an edit ever
   fails.)
4. `git push -u origin <branch>`, then opens a PR against **develop**:
   `gh pr create --base develop ...`. The body says it **solves** the section's
   issues, listing them as `Solves #N` — **not** `Fixes/Closes #N`, because we close
   the issues ourselves after merge with a release note.
5. Drives the PR to a good state by invoking the **`pr-resolver` skill** (CI +
   Copilot/human comments, looped until clean). Ticks **Reviewed**.
6. Reports back to the orchestrator — PR URL, branch, worktree path, issue numbers —
   then returns.

**Orchestrator** — once section agents report ready:

7. Tells you, per section: "PR `<url>` is ready to merge (covers #a, #b)."
8. **Monitors each open PR** for you to merge (e.g. `Monitor` a persistent poll of
   `gh pr view <url> --json state` that emits when `state == MERGED`).
9. **On each merge:** comments on that section's issues — *"Fixed on `develop`; will
   ship in the next release."* — and `gh issue close`s them; ticks **Merged** and
   **Solved**. Then `git fetch origin develop` and **rebases every still-open
   section's branch** onto the new develop (`git rebase origin/develop` in each
   worktree → `git push --force-with-lease`, re-running `pr-resolver` if the rebase
   changed anything). Repeat until all sections are Merged + Solved.

Monitoring, issue-closing and the cross-branch rebase live in the orchestrator (not
inside each section agent) because a section agent can't reach its siblings to make
them rebase, and one central watcher is far more reliable than several agents each
blocked for hours — the net effect is exactly the flow described above.

Notes: PRs target **`develop`**, not `master`. Keep each branch to its section's
items so the diff stays reviewable (Copilot refuses very large diffs).
`chore/error-wrapping-sweep` (#301) will likely need splitting into several
per-package PRs — run each as its own branch off develop through the same flow.

---

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
