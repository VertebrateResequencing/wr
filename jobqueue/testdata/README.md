# jobqueue testdata

This directory holds data and small executable fixtures for `jobqueue` tests.
Browser/UI repro scripts belong here, under a directory named for the behaviour
they exercise, rather than in ad-hoc scratch paths or package roots.

## Status page absolute count protocol

The status page receives idempotent absolute per-RepGroup counts over the
websocket as `{ RepGroup, Counts }` messages (issue 260625-7). The client
replaces a RepGroup's displayed counts wholesale, so dropped or duplicated
messages are harmless. The fixtures below drive this absolute protocol; older
fixtures that previously injected non-idempotent count deltas / snapshot+resync
messages have been migrated to it while keeping their behavioural assertions.

## Repgroup flicker and overcount

`repgroup-flicker-overcount/` contains the issue-260625-7 regression fixture for
the status web UI that "flickers so fast it looks like it's not there" with a
per-RepGroup total that "keeps rising above the total number of jobs actually
added". `screenshot.mjs` serves `jobqueue/static`, injects a fake websocket, and
drives a high-rate `ready -> running -> complete` transition storm for a fixed
number of jobs in one RepGroup while a ~200 Hz in-page sampler watches the
Knockout view model. It asserts the row never drops to 0 / disappears while jobs
exist (no flicker) and never exceeds the number of jobs added (no overcount), and
converges exactly. By default it drives the idempotent absolute protocol (which
passes). Set `WR_FIXTURE_PROTOCOL=delta` to instead drive the legacy
non-idempotent delta protocol over a model of the lossy 1-slot coalescing caster;
against the pre-fix `websocket-handler.js` that variant reproduces both symptoms
and fails. It is wired into `make browser-test`.

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
a realistic storm of idempotent absolute-state messages for one RepGroup
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

## Status page stale counts

`status-page-stale-counts/` contains the issue-260625-5 web UI regression
fixtures, migrated to the absolute protocol:

- `repro.mjs` loads the real status-page websocket handler and checks that an
  authoritative absolute per-RepGroup update clears stale live counts (e.g. a
  RepGroup left showing jobs running/pending) by replacing the counts wholesale.
  Run it with `--assert` for the regression check, or without that flag to
  generate an HTML repro artifact.
- `screenshot.mjs` serves `jobqueue/static`, injects a fake websocket, delivers
  stale then authoritative absolute state, verifies the stale running/pending
  counts clear and the RepGroup shows its real terminal state, searches for
  `tabletest`, and writes a post-fix screenshot.

Use `make browser-test` (or the alias `make webui-test`) to run these browser
fixtures as a discoverable gate. The normal `make test` and `make race` targets
do not run browser tests.

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

## Status page snapshot twitch

`status-page-snapshot-twitch/` contains a browser regression fixture for
steady-state RepGroup twitching (issue 260625-6), migrated to the absolute
protocol. It serves the real status page, injects a fake websocket, pushes the
steady-state absolute counts (`bigmod` = 15,000 dependent), then re-sends the
same absolute state with the per-RepGroup part delayed, and asserts that the
visible `bigmod` row remains at exactly 15,000 dependent jobs throughout (no
twitch to a partial/zero value). It also asserts that the status page does not
register the old blind 10-second current-status polling timer. It is wired into
`make browser-test`.

## Completed RepGroup visibility

`completed-repgroup-visibility/` contains a browser regression fixture for a
RepGroup that transitions from pending to all complete while the status page is
open (issue 260625-6), migrated to the absolute protocol. It serves the real
status page, injects a fake websocket, drives the RepGroup through
ready/running/complete absolute states, then re-sends the (now empty) live
`+all+` aggregate and asserts that the completed RepGroup remains visible with
the correct completed count/bar. It is wired into `make browser-test`.

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
