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

Browser-test dependencies and artifacts must stay repo-local:

- Playwright npm package: `.tmp/agent/playwright`
- npm cache: `.tmp/agent/npm-cache`
- Playwright browser cache: `.tmp/agent/ms-playwright`
- generated repro HTML and screenshots: `.tmp/agent/webui-test`

These paths are ignored by git through the top-level `.gitignore`. Do not
install system packages for these fixtures. If a future UI regression needs a
browser repro or screenshot, add the scripts under a descriptive
`jobqueue/testdata/<scenario>/` directory and wire them into `make browser-test`
instead of scattering `.mjs` files elsewhere.
