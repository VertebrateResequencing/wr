# jobqueue testdata

This directory holds data and small executable fixtures for `jobqueue` tests.
Browser/UI repro scripts belong here, under a directory named for the behaviour
they exercise, rather than in ad-hoc scratch paths or package roots.

## Status page stale counts

`status-page-stale-counts/` contains the issue-1 web UI regression fixtures:

- `repro.mjs` loads the real status-page websocket handler and checks that an
  authoritative current-status snapshot clears stale live counts after dropped
  websocket deltas. Run it with `--assert` for the regression check, or without
  that flag to generate an HTML repro artifact.
- `screenshot.mjs` serves `jobqueue/static`, injects a fake websocket, runs the
  same stale-count scenario in Chromium, searches for `tabletest`, and writes a
  post-fix screenshot.

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
