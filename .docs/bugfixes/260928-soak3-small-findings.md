# 260928: small findings from the third production-shaped soak

Branch `fix-soak3-small-findings`, based on `origin/develop` at `f2888015`
(#642).

- [x] **False "bkill did not reclaim all excess runners" warnings persist.**
  Run 1 logged 23. wr calls `bkill -b` (jobqueue/scheduler/lsf.go around
  :1216-1218). On farm22 it was confirmed that `-b` prints lines like `Job has
  already finished` with NO `Job <id>` prefix, so #641's range parsing
  (accountLines, takeReportedElements) never gets an id to credit.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue/scheduler
    -run TestBkillAggregateOutput` exited 1: for `Job has already finished`
    and for `No matching job found`, each with exit status 255, alreadyGone
    was `Expected: 5 Actual: 0`.
  - Observed on farm22 (LSF 10.1), on the author's own `sleep` jobs only,
    with `bkill -b` (all output is on stderr, one line per invocation):

    | ids given | output | exit |
    | --- | --- | --- |
    | 1 finished job | `Job has already finished` | 255 |
    | 2 finished jobs | `Job has already finished` (once) | 255 |
    | 3 finished array elements | `Job has already finished` (once) | 255 |
    | 1 unknown id | `No matching job found` | 255 |
    | finished + unknown, either order | `No matching job found` | 255 |
    | running + finished + running | `The requested operation is in progress.` | 0 |
    | finished + running + unknown + pending | `The requested operation is in progress.` | 0 |
    | finished + malformed id | `bogus[x: Illegal job ID.` | 255 |

    Plain `bkill` given a finished job, a finished element and an unknown id
    prints one `Job <id>: ...` line per id, in order, and exits 255. The man
    page documents only `bkill -b 0` printing `Operation is in progress`.
  - So `-b` reports once for the whole request, not once per element, and the
    lines cannot be matched to elements by position or count: crediting only
    as many elements as there are such lines would credit 1 of 1,000. Instead
    the report is read for what it means. If any element was running or
    pending, bkill accepts the request and exits 0. It exits 255 with only
    `Job has already finished` or `No matching job found` when there was
    nothing to kill, so every element was already gone.
  - `jobqueue/scheduler/lsf.go`: `bkillFoundNothingToKill` holds only when
    bkill exited by itself with a non-zero status (not killed by
    bkillExecTimeout) and every non-blank line is exactly one of those two
    phrases. `account` then counts the unexplained elements as already gone.
    Any other line, such as `Illegal job ID` or `User permission denied`, or a
    signalled bkill, still leaves them unaccounted, at warn. If LSF ever says
    this about an element that is still live, bjobs keeps reporting it as
    excess, and the next cycle's `retried` count still warns.
  - This changes a behaviour pinned by `.docs/bugfixes/260818-1.md`
    (FINDING 4): `bkillProdShapeBody`, prod's literal `No matching job found`
    for about 1,900 ids, was to stay unaccounted at warn because wr could not
    tell what happened to each element. The farm22 table above shows what it
    means: nothing in the request was live. The test in
    `jobqueue/scheduler/reliable4_bkill_test.go` now expects `killed=0
    alreadyGone=1900 unaccounted=0` and no warn. It still asserts that no
    element is assumed killed. The `retried` warn test is unchanged.
  - `jobqueue/scheduler/lsf_bkill_aggregate_test.go`: both phrases, an
    extra line, an illegal id, a signalled bkill, per-element lines, and an
    accepted request.
  - CHANGELOG: Fixed entry.
- [x] **Error-level log noise on clean restart.** After a clean manager
  restart, each long-lived Go client's first request is rejected with "wrong
  token" and then resent successfully by #640's token reload. The manager
  still logs that rejection at error level: 36 `add` and 9 `getin` lines in
  the soak.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    TestClientRequestErrorLogLevel` exited 1: the wrong-token case logged
    `lvl=eror msg="Server handle client request error" err="jobqueue add():
    Client presented the wrong token"`.
  - `jobqueue/server.go`: `logClientRequestError` logs a request refused for
    a wrong token at warn, as "Server refused a client request with the wrong
    token". Every other refusal keeps its level.
  - `jobqueue/serverCLI.go`: the reason is now the `wrongTokenReason` const,
    shared by `validateRequest` and the logger.
  - Why not skip the log only for the restart case: a clean stop deletes the
    token file, so the new manager has no record of the previous token and
    cannot tell a client presenting it from a misconfigured client or an
    attacker. At warn, each refusal stays visible at the default log level.
  - `jobqueue/client_request_log_test.go`: a wrong-token add is refused with
    `ErrPermissionDenied` and logged at warn, not error.
  - CHANGELOG: Fixed entry.
