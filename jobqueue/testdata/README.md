# jobqueue testdata

This directory holds data and small executable fixtures for `jobqueue` tests.
Browser/UI repro scripts belong here, under a directory named for the behaviour
they exercise, rather than in ad-hoc scratch paths or package roots.

## dbcompat/db.golden (DB compatibility fixture)

`dbcompat/db.golden` is a small committed BoltDB fixture used by
`jobqueue/reliable2_dbcompat_test.go` to prove that the reworked ("Option R")
build opens a database already upgraded by current/pre-removal `reliable2` code
without error or data loss (spec.md section F1).

It is produced by the committed generator `dbcompat/gen.go` (a `//go:build
ignore` program, so it is excluded from the normal build and lint). The
generator drives the real `jobqueue` DB open path: it starts an in-process
`Serve` on a fresh development BoltDB, connects a client, and creates ~4 jobs
across two rep groups:

- two jobs in rep group `reliable2-dbcompat-complete`, reserved + started +
  archived successfully (so `jobscomplete`, `endTimeToKey`, `repgroupEndTime`
  and the now-dead `repGroupCompleteCount` populate, and the backfill sentinel
  is written to the now-dead `repGroupCompleteBackfilled`);
- two jobs in rep group `reliable2-dbcompat-incomplete`, left incomplete in
  `jobslive`, each carrying non-empty `WaitingForDepGroups` and `LimitGroups`
  (so `LimitGroupsForDisplay` is set too).

The now-dead buckets `repGroupCompleteCount` and `repGroupCompleteBackfilled`
are only written by pre-removal code, so the fixture MUST be regenerated from a
pre-removal commit.

### Regeneration procedure

1. Check out a pre-removal `reliable2` commit that STILL maintains the
   per-RepGroup complete counters - i.e. one that still has
   `adjustRepGroupComplete`, the `CreateBucketIfNotExists` calls for
   `repGroupCompleteCount` / `repGroupCompleteBackfilled`, and the backfill
   sentinel write. The `reliable2` base commit `07355ba` ("Add reliable2 spec")
   is such a commit (Phase 2 removed the backfill launcher and Phase 3 removed
   the counter write-side and the buckets, so any commit at or before Phase 1,
   `3cc9903`, works). Verify with, e.g.:

   ```
   git grep -n 'repGroupCompleteCount\|adjustRepGroupComplete\|backfillSentinelKey' <commit> -- jobqueue/db.go
   ```

   The cleanest way to build against that commit without disturbing your working
   tree is a detached git worktree:

   ```
   git worktree add --detach /tmp/wr-genwt 07355ba
   cp jobqueue/testdata/dbcompat/gen.go /tmp/wr-genwt/jobqueue/testdata/dbcompat/gen.go
   ```

2. Build and run the committed generator through the `jobqueue` DB open path,
   passing the absolute output path as its sole argument:

   ```
   cd /tmp/wr-genwt
   go run jobqueue/testdata/dbcompat/gen.go /abs/path/to/wr/jobqueue/testdata/dbcompat/db.golden
   ```

3. The generator writes the bolt file directly to that path. Return to your
   normal checkout, `git add` the (binary) fixture, and remove the worktree:

   ```
   git worktree remove /tmp/wr-genwt
   git add jobqueue/testdata/dbcompat/db.golden
   ```

   Keep the fixture small (a handful of jobs, as above).

## Status page count delta protocol

The status page receives v0.36.5-style per-RepGroup count deltas over the
websocket as `{ RepGroup, FromState, ToState, Count }` messages: the count in
`FromState` drops by `Count` and the count in `ToState` rises by `Count`.
`+all+` aggregates the live jobs across all RepGroups. The feed is lossy and
unordered, so the client compensates for an out-of-order delta that would drive
a count negative (clamping at zero and recording an amount to ignore from a
later increment of that state), and a reconnecting client re-seeds from a fresh
scan-on-connect rather than any resync: it sends a "current" request and the
server replies with the seed as deltas from the `new` state (incomplete-only, so
a completed-only RepGroup is omitted from a fresh connection). The fixtures below
drive this delta protocol.

Use `make browser-test` (or the alias `make webui-test`) to run these browser
fixtures as a discoverable gate. The normal `make test` and `make race` targets
do not run browser tests.

## Repgroup bar flicker

`repgroup-bar-flicker/` contains the regression fixture for the residual status
web UI bar-rendering bug after the absolute-state migration (commit 4e306f7).
The per-RepGroup totals are correct, but during a high-rate job-state storm the
progress bar "is basically invisible due to flickering": the previous wholesale
apply path zeroed every per-RepGroup percentage observable on each update, so
the bound segment widths collapsed to 0% on every message instead of the bar
staying full and only its colour proportions shifting. The requirement is that
"bars in the frontend should smoothly reduce or increase in size as their
numbers change, not be cleared and redrawn on every change."

`screenshot.mjs` serves `jobqueue/static`, injects a fake websocket, and drives
a realistic storm of jstateCount delta messages for one RepGroup
(`echo`), moving ~10000 jobs ready -> running -> complete over hundreds of
messages at storm rate (~20/sec), ending all-complete. A requestAnimationFrame
sampler reads the ACTUAL rendered segment widths of
`[data-repgroup="echo"] .progress-bar` (summed segment pixel width / container
pixel width = filled %) throughout, and asserts: (a) the summed filled width
never collapses while jobs exist (0 collapse frames where filled < 50%, minimum
filled >= 95%); (c) the bar stays ~full essentially all the time (it is the
*share* of frames below 95% that discriminates, since the CSS width transition
makes each per-frame step small) and converges to ~99.9% complete; and (b) the
segment DOM nodes are stamped once and persist (a guard that the fix does not
start tearing the bar down). On the pre-fix code (a)/(c) fail: ~872/938
populated frames collapse and ~94% are below 95% (minimum filled 0%); after the
fix all are 0 and the minimum filled is ~97%. It is wired into
`make browser-test`.

## Dependent job details

`dependent-job-details/` contains a browser fixture for dependent jobs waiting
on a dependency group that has not appeared yet. It serves the real status page,
injects a fake websocket, opens the dependent-job details row, asserts that the
visible status row explains the missing dependency group wait, and writes a
screenshot.

## Suspended job actions

`suspended-job-actions/` contains a browser fixture for suspended jobs in the
status page. It serves the real status page, injects a fake websocket, opens a
suspended-job details row, asserts that the visible state text is exactly
`suspended`, clicks the Resume action, verifies a single-job resume websocket
request, and writes a screenshot.

## Websocket reconnect warnings

`websocket-reconnect-warnings/` contains a browser fixture for manager
disconnect/reconnect handling in the status page. It serves the real status
page, injects a fake websocket that fails while preserving the same token on
reconnect, asserts that repeated `WebSocket error: Unknown error` warnings do
not accumulate while the manager is down, then delivers a current snapshot plus
new completion activity and verifies that the lost-manager and websocket
warnings are cleared.

## Live heartbeat details

`live-heartbeat-details/` contains a browser fixture for running-job heartbeat
updates in the status page. It serves the real status page, injects a fake
websocket, opens a running job details row whose live stdout/stderr are already
visible, then delivers a live heartbeat push and asserts that peak RAM, CPU
time, STDOUT, and STDERR are all visible together.

## Completed RepGroup visibility

`completed-repgroup-visibility/` contains a browser regression fixture for a
RepGroup that transitions from pending to all complete while the status page is
open (issue 260625-6). It serves the real status page, injects a fake websocket,
drives the RepGroup through ready/running/complete as jstateCount deltas (the
live `+all+` aggregate returning to empty as the jobs complete) and asserts that
the completed RepGroup remains visible with the correct completed count/bar. It
is wired into `make browser-test`.

Set `WR_FIXTURE_SCENARIO=deleted-refresh` to exercise the short-job ordering
regression with six jobs in one RepGroup. The fixture verifies that four rapid
completions remain complete in both the live view and a refreshed connection,
then that the next dirty update renders five complete plus one running with no
deleted jobs. The default output names use the `-deleted-refresh-post-fix`
suffix so the pre-fix reproduction screenshot and trace remain available for
comparison.

## Removed jobs refresh

`removed-jobs-refresh/` contains a browser regression fixture for the "removed
jobs reappear after refresh" bug (a regression from the absolute-state status
broadcast rework). Repro: many jobs are added to a RepGroup (`echo`), some
complete, then the rest are removed with `wr remove -a` while still
pending/ready. The LIVE view correctly shows `echo` as part green (complete) +
part red (deleted). The BUG was that refreshing the page (a fresh websocket
connection) made `echo` reappear, even though it now has only terminal members
(complete + deleted, no live job): a freshly loaded page must show nothing for a
RepGroup that has any deleted terminal contribution and no live jobs.

The fix is server-side: the seed a freshly-connected (or refreshed) client
receives is the incomplete-only scan-on-connect - live states plus complete for
RepGroups that still have live jobs - so a RepGroup with only terminal members
(complete-only, complete+deleted, or deleted-only) is omitted and never re-shown,
and deleted is never seeded (`jobqueue/serverWebI.go`
`sendCurrentStatusCounts`). The frontend renders whatever
RepGroups the server sends, so `screenshot.mjs` models the server seed in JS
(`computeSeed`) and drives the real `websocket-handler.js` against it. Phase 1 keeps a page open while `echo`
completes then is removed and asserts the red `(deleted)` bar shows live (the
260625-6 live-retain / transient-red guarantee). Phase 2 opens a brand-new page
(a refresh) whose fresh-connect seed is computed from the post-removal state and
asserts the `echo` row is absent.

`WR_FIXTURE_SEED` selects how the fresh-connect seed is computed: `filtered`
(default) is the filtered seed the fixed server sends, so `echo` is omitted and
the refresh assertion passes; `unfiltered` is the pre-fix server seed (every
RepGroup, including deleted), which re-sends `echo`, so the refresh assertion
fails and reproduces the bug. The live-phase assertions are identical for both.
It is wired into `make browser-test`.

## Local dependency and artifact locations

`make browser-test` separates persistent dependencies from ephemeral artifacts so
that wiping `.tmp` never forces a Playwright reinstall or a Chromium re-download:

- Playwright browser cache: `~/.cache/ms-playwright` (Playwright's standard
  per-user location). An existing Chromium build is reused and shared across
  projects; a missing build is downloaded here once and persists across `.tmp`
  wipes. The `npm install playwright` step sets `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`
  so the npm postinstall does not redundantly download browsers; the explicit
  `playwright install chromium` step (which sets `PLAYWRIGHT_BROWSERS_PATH` to
  this cache) is the sole browser fetch.
- Playwright npm package: `~/.cache/wr-webui-playwright/node_modules/playwright`
  (cached outside `.tmp`, so a `.tmp` wipe does not trigger an npm reinstall).
- npm cache: `~/.cache/wr-webui-playwright/npm-cache`.
- generated repro HTML, screenshots and traces: `.tmp/agent/webui-test` (ephemeral;
  safe to wipe).

All of these paths are overridable via the `WEBUI_TEST_*` make variables (e.g.
`WEBUI_TEST_BROWSER_CACHE`, `WEBUI_TEST_PLAYWRIGHT_ROOT`, `WEBUI_TEST_NPM_CACHE`,
`WEBUI_TEST_ARTIFACT_DIR`), so CI can pin a sandboxed location. The `.tmp/agent`
artifact path is ignored by git through the top-level `.gitignore`. Do not
install system packages for these fixtures. If a future UI regression needs a
browser repro or screenshot, add the scripts under a descriptive
`jobqueue/testdata/<scenario>/` directory and wire them into `make browser-test`
instead of scattering `.mjs` files elsewhere.
