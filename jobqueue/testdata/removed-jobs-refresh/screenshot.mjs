#!/usr/bin/env node

// Browser regression fixture for the "removed jobs reappear after refresh" bug
// (a regression from the absolute-state status broadcast rework).
//
// Repro: many jobs are added to a RepGroup (echo), some complete, then the rest
// are removed with `wr remove -a` while still pending/ready. The LIVE view
// correctly shows echo as part green (complete) + part red (deleted) - that is
// expected. The BUG is that REFRESHING the page (a fresh websocket connection)
// makes echo reappear, even though echo now has only terminal members (complete
// + deleted, no live job): a freshly loaded page must show NOTHING for a
// complete-only / deleted-only RepGroup.
//
// The fix is server-side: the seed a freshly-connected (or refreshed) client
// receives must include a RepGroup only if it has >=1 LIVE job, send its live
// states + complete, and NEVER send deleted; complete-only / deleted-only
// RepGroups are omitted (jobqueue/statusstate.go liveSeedLocked). The frontend
// renders whatever RepGroups the server sends, so this fixture models the server
// seed in JS (computeSeed) and drives the real websocket-handler.js against it.
//
// WR_FIXTURE_SEED selects how the post-removal fresh-connect seed is computed:
//   - 'filtered' (default): the live-only seed the fixed server sends. echo is
//     complete+deleted only, so it is omitted, and the refreshed page shows no
//     echo row. The assertions PASS.
//   - 'unfiltered': the pre-fix server seed, which seeded a new subscriber with
//     EVERY RepGroup in its counts (including complete-only/deleted-only ones and
//     their deleted state). echo is re-sent, so the refreshed page shows the echo
//     row again (with the red deleted bar). This reproduces the bug, so the
//     refresh assertion FAILS.
// The live-phase assertions (echo visible with a red deleted bar while the page
// stayed open) are identical for both and always PASS: the fix only changes the
// fresh-connect seed, not live updates to an already-connected client.

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const seedMode = process.env.WR_FIXTURE_SEED === 'unfiltered' ? 'unfiltered' : 'filtered';
const suffix = seedMode === 'unfiltered' ? '-unfiltered' : '';
const defaultScreenshotOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-removed-jobs-refresh${suffix}.png`);
const defaultTraceOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-removed-jobs-refresh${suffix}-trace.json`);
const screenshotOutputPath = path.resolve(process.cwd(), process.argv[2] || defaultScreenshotOutput);
const traceOutputPath = path.resolve(process.cwd(), process.argv[3] || defaultTraceOutput);

for (const outputPath of [screenshotOutputPath, traceOutputPath]) {
  if (outputPath !== repoRoot && !outputPath.startsWith(repoRoot + path.sep)) {
    throw new Error(`refusing to write outside repo: ${outputPath}`);
  }
}

// The RepGroup's ground-truth job counts. echo starts with 10000 ready jobs;
// 6000 complete normally, then the remaining 4000 are removed (`wr remove -a`).
const REP_GROUP = 'echo';
const TOTAL = 10000;
const COMPLETED = 6000;
const REMOVED = TOTAL - COMPLETED;

const LIVE_STATES = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried'];

function hasLiveJob(counts) {
  return LIVE_STATES.some((state) => (counts[state] || 0) > 0);
}

// computeSeed models the server's fresh-connect seed. When filtered (the fix),
// a RepGroup is included only if it has a live job, and its deleted count is
// dropped; complete is kept. When unfiltered (the pre-fix bug), every RepGroup
// is sent verbatim, including its deleted count. The +all+ aggregate always
// holds live jobs only (the server maintains that invariant separately).
function computeSeed(state, filtered) {
  const messages = [];
  const liveAggregate = {};

  for (const [rg, counts] of Object.entries(state)) {
    for (const live of LIVE_STATES) {
      if ((counts[live] || 0) > 0) {
        liveAggregate[live] = (liveAggregate[live] || 0) + counts[live];
      }
    }
  }

  messages.push({ RepGroup: '+all+', Counts: liveAggregate });

  for (const [rg, counts] of Object.entries(state)) {
    if (filtered) {
      if (!hasLiveJob(counts)) {
        continue;
      }

      const sent = {};
      for (const [state, count] of Object.entries(counts)) {
        if (count > 0 && state !== 'deleted') {
          sent[state] = count;
        }
      }

      messages.push({ RepGroup: rg, Counts: sent });
    } else {
      const sent = {};
      for (const [state, count] of Object.entries(counts)) {
        if (count > 0) {
          sent[state] = count;
        }
      }

      messages.push({ RepGroup: rg, Counts: sent });
    }
  }

  return messages;
}

async function loadPlaywright() {
  try {
    const mod = await import('playwright');

    return mod.default || mod;
  } catch (error) {
    const packageDir = process.env.PLAYWRIGHT_PACKAGE_DIR;
    if (!packageDir) {
      throw new Error(
        'playwright is not importable. Set PLAYWRIGHT_PACKAGE_DIR to a repo-local playwright package, ' +
        `for example ${path.join(repoRoot, '.tmp/agent/playwright/node_modules/playwright')}: ${error.message}`
      );
    }

    const mod = await import(pathToFileURL(path.join(packageDir, 'index.js')).href);

    return mod.default || mod;
  }
}

function contentType(filePath) {
  switch (path.extname(filePath)) {
    case '.css':
      return 'text/css; charset=utf-8';
    case '.js':
      return 'text/javascript; charset=utf-8';
    case '.woff':
      return 'application/font-woff';
    case '.woff2':
      return 'application/font-woff2';
    case '.ttf':
      return 'application/x-font-truetype';
    case '.eot':
      return 'application/vnd.ms-fontobject';
    case '.svg':
      return 'image/svg+xml';
    case '.ico':
      return 'image/x-icon';
    default:
      return 'text/html; charset=utf-8';
  }
}

function staticPathFor(requestPath) {
  const cleanPath = decodeURIComponent(requestPath.split('?')[0]);
  const relative = cleanPath === '/' || cleanPath === '/status'
    ? 'status.html'
    : cleanPath.replace(/^\/+/, '');
  const resolved = path.resolve(staticRoot, relative);

  if (resolved !== staticRoot && !resolved.startsWith(staticRoot + path.sep)) {
    return null;
  }

  return resolved;
}

function createStaticServer() {
  const server = http.createServer((req, res) => {
    const filePath = staticPathFor(req.url || '/');
    if (!filePath) {
      res.writeHead(403).end('forbidden');
      return;
    }

    fs.readFile(filePath, (error, data) => {
      if (error) {
        res.writeHead(404).end('not found');
        return;
      }

      res.writeHead(200, { 'Content-Type': contentType(filePath) });
      res.end(data);
    });
  });

  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

// fakeWebSocketScript installs a fake WebSocket that replays a fixed list of
// absolute-state message batches in response to the client's "current" request.
// Each batch is emitted message-by-message with a small delay, mirroring how the
// server sends one { RepGroup, Counts } per dirty RepGroup. batches is an array
// of { phase, messages } applied in order; the live phases model updates pushed
// to an already-connected client, the seed batch models the first drain a fresh
// connection receives.
function fakeWebSocketScript(batches) {
  return `(() => {
    const BATCHES = ${JSON.stringify(batches)};

    window.__wrRemovedRefreshFixture = window.__wrRemovedRefreshFixture || {
      connections: 0,
      messages: [],
      phase: 'boot'
    };
    window.__wrRemovedRefreshFixture.messages = [];
    window.__wrRemovedRefreshFixture.phase = 'boot';

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        window.__wrRemovedRefreshFixture.connections += 1;
        window.__wrRemovedRefreshFixture.socket = this;

        setTimeout(() => {
          this.readyState = 1;
          window.__wrRemovedRefreshFixture.phase = 'open';
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        let elapsed = 0;
        for (const batch of BATCHES) {
          const startAt = elapsed + 50;
          setTimeout(() => {
            window.__wrRemovedRefreshFixture.phase = batch.phase;
          }, startAt);

          batch.messages.forEach((message, index) => {
            setTimeout(() => {
              window.__wrRemovedRefreshFixture.messages.push(message);
              if (this.onmessage) {
                this.onmessage({ data: JSON.stringify(message) });
              }
            }, startAt + 5 * (index + 1));
          });

          elapsed = startAt + 5 * (batch.messages.length + 1) + 600;
        }
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }
    }

    window.WebSocket = FixtureWebSocket;
  })();`;
}

async function sampleStatus(page, label) {
  return page.evaluate((sampleLabel) => {
    const statusElement = document.getElementById('status');
    const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
    const repGroupIndex = viewModel ? viewModel.repGroupLookup['echo'] : undefined;
    const repGroup = (viewModel && repGroupIndex !== undefined) ? viewModel.repGroups[repGroupIndex] : null;
    const repGroupElement = document.querySelector('[data-repgroup="echo"]');
    // The repgroup row has three red (.progress-bar-danger) segments: lost,
    // buried and deleted. The deleted bar is the one whose label reads
    // "(deleted)", so pick it by text rather than position.
    const dangerBars = repGroupElement
      ? Array.from(repGroupElement.querySelectorAll('.progress-bar-danger'))
      : [];
    const deletedBar = dangerBars.find((bar) => bar.textContent.includes('deleted')) || null;
    const completeBar = repGroupElement ? repGroupElement.querySelector('.progress-bar-success') : null;
    const deletedBarText = deletedBar ? deletedBar.textContent.trim().replace(/\s+/g, ' ') : null;
    const deletedBarWidth = deletedBar ? deletedBar.style.width : null;

    return {
      label: sampleLabel,
      phase: window.__wrRemovedRefreshFixture.phase,
      connections: window.__wrRemovedRefreshFixture.connections,
      repGroupExists: Boolean(repGroup),
      repGroupInDom: Boolean(repGroupElement),
      repGroupTotal: repGroup ? repGroup.total() : null,
      repGroupReady: repGroup ? repGroup.ready() : null,
      repGroupComplete: repGroup ? repGroup.complete() : null,
      repGroupDeleted: repGroup ? repGroup.deleted() : null,
      deletedBarVisible: Boolean(deletedBar) && deletedBarText.includes('deleted') &&
        deletedBarWidth !== '0%' && deletedBarWidth !== '0px' && deletedBarWidth !== '',
      deletedBarText,
      deletedBarWidth,
      completeBarVisible: Boolean(completeBar) && completeBar.style.width !== '0%' && completeBar.style.width !== '0px',
      messageCount: window.__wrRemovedRefreshFixture.messages.length
    };
  }, label);
}

function assertEchoLiveWithRedBar(sample) {
  if (!sample.repGroupExists ||
    sample.repGroupComplete !== COMPLETED ||
    sample.repGroupDeleted !== REMOVED ||
    !sample.deletedBarVisible) {
    throw new Error(
      `live phase: expected echo visible with ${COMPLETED} complete, ${REMOVED} deleted ` +
      `and a red deleted bar, saw ${JSON.stringify(sample)}`
    );
  }
}

function assertEchoAbsentAfterRefresh(sample) {
  if (sample.repGroupExists || sample.repGroupInDom) {
    throw new Error(
      `refresh phase: expected NO echo row after a fresh page load (echo is complete+deleted only), ` +
      `saw ${JSON.stringify(sample)}`
    );
  }
}

async function waitForKoReady(page) {
  await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
}

async function run() {
  const { chromium } = await loadPlaywright();
  const server = await createStaticServer();
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}`;

  for (const outputPath of [screenshotOutputPath, traceOutputPath]) {
    fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  }

  const browser = await chromium.launch({ headless: true });
  let page;
  let capturedError;
  const trace = {
    scenario: 'removed jobs do not reappear on the status page after a refresh',
    seedMode,
    source: {
      page: 'jobqueue/static/status.html',
      handler: 'jobqueue/static/js/wr/websocket-handler.js',
      serverSeed: 'jobqueue/statusstate.go liveSeedLocked'
    },
    artifacts: { screenshot: screenshotOutputPath, trace: traceOutputPath },
    samples: []
  };

  // Phase 1: an already-connected (live) client. Its first drain is the live
  // seed (echo all ready). Then jobs complete and the rest are removed: live
  // updates carry complete and deleted, so the red deleted bar appears.
  const liveSeed = computeSeed({ [REP_GROUP]: { ready: TOTAL } }, true);
  const afterComplete = [
    { RepGroup: '+all+', Counts: { ready: REMOVED } },
    { RepGroup: REP_GROUP, Counts: { ready: REMOVED, complete: COMPLETED } }
  ];
  const afterRemoval = [
    // every remaining live job removed: +all+ is now empty (no live jobs) ...
    { RepGroup: '+all+', Counts: {} },
    // ... and echo carries its complete + deleted counts as a live update.
    { RepGroup: REP_GROUP, Counts: { complete: COMPLETED, deleted: REMOVED } }
  ];

  const liveBatches = [
    { phase: 'initial-current', messages: liveSeed },
    { phase: 'live-completing', messages: afterComplete },
    { phase: 'live-removed', messages: afterRemoval }
  ];

  // Phase 2: a fresh page load (refresh) AFTER the removal. The fresh-connect
  // seed is computed from the post-removal absolute state (echo is complete +
  // deleted only). filtered (the fix) omits echo; unfiltered (the bug) re-sends
  // it.
  const postRemovalState = { [REP_GROUP]: { complete: COMPLETED, deleted: REMOVED } };
  const refreshSeed = computeSeed(postRemovalState, seedMode === 'filtered');
  const refreshBatches = [
    { phase: 'refresh-current', messages: refreshSeed }
  ];

  const openPage = async (batches) => {
    const newPage = await browser.newPage({ viewport: { width: 1280, height: 820 } });
    newPage.on('console', message => {
      if (message.type() === 'error') {
        console.error(`browser console error: ${message.text()}`);
      }
    });
    newPage.on('pageerror', error => {
      console.error(`browser page error: ${error.message}`);
    });

    await newPage.addInitScript(fakeWebSocketScript(batches));
    await newPage.goto(`${baseURL}/status.html?token=removed-jobs-refresh`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await waitForKoReady(newPage);

    return newPage;
  };

  try {
    // ---- Phase 1: live client watches the removal ----
    page = await openPage(liveBatches);

    // echo appears live as all-ready.
    await page.waitForFunction((expectedTotal) => {
      const statusElement = document.getElementById('status');
      const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
      const idx = viewModel ? viewModel.repGroupLookup['echo'] : undefined;
      const rg = (viewModel && idx !== undefined) ? viewModel.repGroups[idx] : null;

      return rg && rg.total() === expectedTotal;
    }, TOTAL, { timeout: 10000 });
    trace.samples.push(await sampleStatus(page, 'live: echo all ready'));

    // after the removal, the live client shows echo with its red deleted bar.
    await page.waitForFunction(({ removed, completed }) => {
      const statusElement = document.getElementById('status');
      const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
      const idx = viewModel ? viewModel.repGroupLookup['echo'] : undefined;
      const rg = (viewModel && idx !== undefined) ? viewModel.repGroups[idx] : null;
      const row = document.querySelector('[data-repgroup="echo"]');
      const bars = row ? Array.from(row.querySelectorAll('.progress-bar-danger')) : [];
      const deletedBar = bars.find((bar) => bar.textContent.includes('deleted'));

      return rg && rg.deleted() === removed && rg.complete() === completed &&
        deletedBar && deletedBar.style.width !== '0%' && deletedBar.style.width !== '0px' &&
        deletedBar.style.width !== '';
    }, { removed: REMOVED, completed: COMPLETED }, { timeout: 10000 });
    const liveSample = await sampleStatus(page, 'live: echo after removal (red bar)');
    trace.samples.push(liveSample);
    assertEchoLiveWithRedBar(liveSample);

    // The live page is now closed, exactly as a user closing/refreshing the tab.
    await page.close();

    // ---- Phase 2: refresh (a fresh page load / new websocket) ----
    page = await openPage(refreshBatches);

    // wait for the fresh connection to deliver its entire seed batch.
    await page.waitForFunction((expectedMessages) => {
      return window.__wrRemovedRefreshFixture.phase === 'refresh-current' &&
        window.__wrRemovedRefreshFixture.messages.length >= expectedMessages;
    }, refreshSeed.length, { timeout: 10000 });
    // give the handler time to render any row it was told about (so an absent
    // row is a real absence, not just a not-yet-rendered one).
    await page.waitForTimeout(800);
    const refreshSample = await sampleStatus(page, 'refresh: echo must be absent');
    trace.samples.push(refreshSample);
    assertEchoAbsentAfterRefresh(refreshSample);

    trace.liveBatches = liveBatches;
    trace.refreshBatches = refreshBatches;
    trace.fixtureState = await page.evaluate(() => window.__wrRemovedRefreshFixture);

    await page.screenshot({ path: screenshotOutputPath, fullPage: true });
  } catch (error) {
    capturedError = error;
    trace.failure = { message: error.message, stack: error.stack };

    if (page) {
      try {
        trace.samples.push(await sampleStatus(page, 'failure state'));
        trace.fixtureState = await page.evaluate(() => window.__wrRemovedRefreshFixture);
      } catch (sampleError) {
        trace.failure.sampleError = sampleError.message;
      }
    }
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  fs.writeFileSync(traceOutputPath, `${JSON.stringify(trace, null, 2)}\n`);

  if (capturedError) {
    throw capturedError;
  }

  console.log(screenshotOutputPath);
  console.log(traceOutputPath);
}

await run();
