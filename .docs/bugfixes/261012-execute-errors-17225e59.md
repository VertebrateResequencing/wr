# Execute drops its own secondary errors

- Branch: `execerrs-472e3746` (worktree `../wr-execerrs`)
- Base: `origin/develop` at `7b445f43`
- Queue owner: this branch and checklist (deliver row 11 of
  `.docs/reserve-runstate/delivery-queue.md`)

## Triage (owner, 2026-10-12)

Not intentional. The success path returning nil is `Execute`'s documented
contract ("If no error is returned, the Cmd will have run OK ... and been
Archive()d"), but on the failure path the classified command error has
replaced the accumulated errors since the original 2016 code (the pre-#534
classification assigned `myerr` after the unmount step too). The commented-out
block "need a better way to see these errors from runner clients" in `Execute`
records the gap. Behaviour errors already reach the job's stderr through
`appendExecProblems`; kill, close, unmount, mount-cache removal and the earlier
cleanup errors (lsf emulation dir, dir close, tmpdir removal, docker container
lookup) reach neither the caller nor the runner log.

## Items

- [x] Execute drops its own kill/close/behaviour/unmount errors: jobqueue/client.go
  Execute builds up kill, close, behaviour and unmount errors in myerr, then
  `myerr = outcome.myerr` overwrites them for every failed command (and for a
  successful one when unmount doesn't force a release), so "behaviour(s) also
  had problem(s)" and similar never reach the caller or runner log; no test
  asserts them. Confirm whether intentional; if not, bugfix with a red test.
  - Source: delivery-queue row 11, found during #689's review.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v -run
    TestExecuteReportsSecondaryErrors` failed both cases: the failed command's
    error was only `command [exit 1] exited with code 1 [sterr: ... Behaviour
    problems: ... run behaviour failed: exit status 7 the-behaviour-broke ...]`
    (no "behaviour(s) also had problem(s)"), and the successful command's run
    logged no "command ran OK, but executing it had problem(s)" warning.
  - Files: `jobqueue/client.go`, `jobqueue/exec_secondary_errors_test.go`.
  - Approach: Execute now records its secondary problems in an `execProblems`
    list instead of `myerr`, and `execProblems.resultErr` appends them, with
    their existing phrases, after the outcome's own error, so a failed
    command's classified error still leads and `errors.As`/`errors.Is` still
    find it; an upload-failure release returns the problems alone, as before.
    For an archived command Execute still returns nil and logs the problems
    with `clog.Warn`. The lsf emulation dir removal, which ran in a defer
    after the return value was fixed, now logs its error too. The stale
    commented-out debugging block is gone. Nothing sent to the server changes.
