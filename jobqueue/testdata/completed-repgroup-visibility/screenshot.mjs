#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const scenario = process.env.WR_FIXTURE_SCENARIO === 'deleted-refresh' ? 'deleted-refresh' : 'completed';
const suffix = scenario === 'deleted-refresh' ? '-deleted-refresh-post-fix' : '';
const defaultScreenshotOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-completed-repgroup${suffix}.png`);
const defaultTraceOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-completed-repgroup${suffix}-trace.json`);
const screenshotOutputPath = path.resolve(process.cwd(), process.argv[2] || defaultScreenshotOutput);
const traceOutputPath = path.resolve(process.cwd(), process.argv[3] || defaultTraceOutput);

for (const outputPath of [screenshotOutputPath, traceOutputPath]) {
  if (outputPath !== repoRoot && !outputPath.startsWith(repoRoot + path.sep)) {
    throw new Error(`refusing to write outside repo: ${outputPath}`);
  }
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

function fakeWebSocketScript(scenario) {
  return `(() => {
    const SCENARIO = ${JSON.stringify(scenario)};

    window.__wrCompletedRepGroupFixture = {
      intervalTimers: [],
      requests: [],
      messages: [],
      samples: [],
      phase: 'boot',
      currentRequests: 0,
      connections: 0
    };

    const originalSetInterval = window.setInterval.bind(window);

    window.setInterval = (callback, delay, ...args) => {
      window.__wrCompletedRepGroupFixture.intervalTimers.push({ delay });

      return originalSetInterval(callback, delay, ...args);
    };

    // The RepGroup starts with 2 ready jobs, runs them, then completes them. The
    // server sends v0.36.5-style jstateCount deltas: seed counts as deltas from
    // 'new', then each live transition as a from->to delta.
    const initialState = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: 2 },
      { RepGroup: 'done-rg', FromState: 'new', ToState: 'ready', Count: 2 }
    ];

    const runningState = [
      { RepGroup: '+all+', FromState: 'ready', ToState: 'running', Count: 2 },
      { RepGroup: 'done-rg', FromState: 'ready', ToState: 'running', Count: 2 }
    ];

    const completedState = [
      // running -> complete. complete is terminal on +all+, so its live
      // aggregate just loses the running count and is now empty ...
      { RepGroup: '+all+', FromState: 'running', ToState: 'complete', Count: 2 },
      // ... but the completed RepGroup keeps its complete count and stays visible.
      { RepGroup: 'done-rg', FromState: 'running', ToState: 'complete', Count: 2 }
    ];

    // A later wake of the coalescing sender with no live jobs to seed produces no
    // delta at all; the completed RepGroup row must stay visible with its
    // complete count regardless (there is no wholesale replace to remove it).
    const liveAggregateResend = [];

    // Post-fix regression for the reported successful-jobs-as-deleted sequence.
    // The authoritative job store has six jobs in done-rg. Four complete while
    // two keep running; both the live update and a fresh subscription must keep
    // those completed counts. A fifth completion then arrives as a dirty update,
    // which must publish five complete plus one running and no deleted state.
    const deletedRefreshInitial = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'running', Count: 6 },
      { RepGroup: 'done-rg', FromState: 'new', ToState: 'running', Count: 6 }
    ];

    const deletedRefreshFourComplete = [
      { RepGroup: '+all+', FromState: 'running', ToState: 'complete', Count: 4 },
      { RepGroup: 'done-rg', FromState: 'running', ToState: 'complete', Count: 4 }
    ];

    // The refresh is a fresh connection, so the seed rebuilds state from 'new':
    // two still running plus four already complete.
    const deletedRefreshSeed = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'running', Count: 2 },
      { RepGroup: 'done-rg', FromState: 'new', ToState: 'running', Count: 2 },
      { RepGroup: 'done-rg', FromState: 'new', ToState: 'complete', Count: 4 }
    ];

    const deletedRefreshDirty = [
      { RepGroup: '+all+', FromState: 'running', ToState: 'complete', Count: 1 },
      { RepGroup: 'done-rg', FromState: 'running', ToState: 'complete', Count: 1 }
    ];

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        const previousConnections = Number(sessionStorage.getItem('wrCompletedRepGroupConnections') || '0');
        this.connection = previousConnections + 1;
        sessionStorage.setItem('wrCompletedRepGroupConnections', String(this.connection));
        window.__wrCompletedRepGroupFixture.connections = this.connection;
        window.__wrCompletedRepGroupFixture.socket = this;

        setTimeout(() => {
          this.readyState = 1;
          window.__wrCompletedRepGroupFixture.phase = 'open';
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        window.__wrCompletedRepGroupFixture.requests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        window.__wrCompletedRepGroupFixture.currentRequests += 1;

        if (SCENARIO === 'deleted-refresh') {
          this.runDeletedRefreshScenario();
          return;
        }

        window.__wrCompletedRepGroupFixture.phase = 'initial-current';
        this.emitEach(initialState, 5);
        setTimeout(() => {
          window.__wrCompletedRepGroupFixture.phase = 'live-running';
          this.emitEach(runningState, 5);
        }, 700);
        setTimeout(() => {
          window.__wrCompletedRepGroupFixture.phase = 'live-completing';
          this.emitEach(completedState, 5);
        }, 1500);
        setTimeout(() => {
          window.__wrCompletedRepGroupFixture.phase = 'live-aggregate-resend';
          this.emitEach(liveAggregateResend, 5);
        }, 2400);
      }

      runDeletedRefreshScenario() {
        if (this.connection === 1) {
          window.__wrCompletedRepGroupFixture.phase = 'initial-running';
          this.emitEach(deletedRefreshInitial, 5);
          setTimeout(() => {
            window.__wrCompletedRepGroupFixture.phase = 'live-four-complete';
            this.emitEach(deletedRefreshFourComplete, 5);
          }, 500);
          return;
        }

        window.__wrCompletedRepGroupFixture.phase = 'refreshed-authoritative-seed';
        this.emitEach(deletedRefreshSeed, 5);
        setTimeout(() => {
          window.__wrCompletedRepGroupFixture.phase = 'later-dirty-fifth-complete';
          this.emitEach(deletedRefreshDirty, 5);
        }, 3000);
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }

      emitEach(messages, delay = 5) {
        messages.forEach((message, index) => {
          setTimeout(() => {
            window.__wrCompletedRepGroupFixture.messages.push(message);
            if (this.onmessage) {
              this.onmessage({ data: JSON.stringify(message) });
            }
          }, delay * (index + 1));
        });
      }
    }

    window.WebSocket = FixtureWebSocket;
  })();`;
}

async function sampleStatus(page, label) {
  const sample = await page.evaluate((sampleLabel) => {
    const statusElement = document.getElementById('status');
    const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
    const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
    const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];
    const repGroupElement = document.querySelector('[data-repgroup="done-rg"]');
    const badge = repGroupElement ? repGroupElement.querySelector('.badge') : null;
    const completeBar = repGroupElement ? repGroupElement.querySelector('.progress-bar-success') : null;
    const progressBars = repGroupElement ? repGroupElement.querySelectorAll('.progress-bar') : [];
    const deletedBar = progressBars[7] || null;

    const status = {
      label: sampleLabel,
      phase: window.__wrCompletedRepGroupFixture.phase,
      currentRequests: window.__wrCompletedRepGroupFixture.currentRequests,
      inflightTotal: viewModel ? viewModel.inflight.total() : null,
      repGroupExists: Boolean(repGroup),
      repGroupTotal: repGroup ? repGroup.total() : null,
      repGroupReady: repGroup ? repGroup.ready() : null,
      repGroupRunning: repGroup ? repGroup.running() : null,
      repGroupDeleted: repGroup ? repGroup.deleted() : null,
      repGroupComplete: repGroup ? repGroup.complete() : null,
      repGroupCompletePct: repGroup ? repGroup.completePct() : null,
      repGroupBadgeText: badge ? badge.textContent.trim() : null,
      repGroupCompleteBarText: completeBar ? completeBar.textContent.trim().replace(/\\s+/g, ' ') : null,
      repGroupCompleteBarWidth: completeBar ? completeBar.style.width : null,
      repGroupDeletedBarText: deletedBar ? deletedBar.textContent.trim().replace(/\\s+/g, ' ') : null,
      repGroupDeletedBarWidth: deletedBar ? deletedBar.style.width : null,
      repGroupText: repGroupElement ? repGroupElement.innerText : null,
      messageCount: window.__wrCompletedRepGroupFixture.messages.length,
      lastMessages: window.__wrCompletedRepGroupFixture.messages.slice(-4)
    };

    window.__wrCompletedRepGroupFixture.samples.push(status);

    return status;
  }, label);

  return sample;
}

function assertCompletedRepGroupVisible(sample) {
  if (!sample.repGroupExists ||
    sample.repGroupTotal !== 2 ||
    sample.repGroupComplete !== 2 ||
    sample.repGroupBadgeText !== '2' ||
    sample.repGroupCompleteBarText !== '2 complete' ||
    !sample.repGroupCompleteBarWidth ||
    sample.repGroupCompleteBarWidth === '0px' ||
    sample.repGroupCompleteBarWidth === '0%') {
    throw new Error(`expected done-rg to stay visible as 2 complete, saw ${JSON.stringify(sample)}`);
  }
}

function assertDeletedRefreshMatchesAuthoritativeStatus(sample) {
  if (!sample.repGroupExists ||
    sample.repGroupTotal !== 6 ||
    sample.repGroupRunning !== 1 ||
    sample.repGroupComplete !== 5 ||
    sample.repGroupDeleted !== 0 ||
    sample.repGroupBadgeText !== '6') {
    throw new Error(
      `expected authoritative done-rg state (5 complete, 1 running, 0 deleted), saw ${JSON.stringify(sample)}`
    );
  }
}

async function captureScreenshot() {
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
    scenario: scenario === 'deleted-refresh'
      ? 'successful completions must not project as deleted across refresh and later dirty updates'
      : 'completed repgroup remains visible after live completion and current resync',
    source: {
      page: 'jobqueue/static/status.html',
      handler: 'jobqueue/static/js/wr/websocket-handler.js'
    },
    artifacts: {
      screenshot: screenshotOutputPath,
      trace: traceOutputPath
    },
    samples: []
  };

  try {
    page = await browser.newPage({ viewport: { width: 1280, height: 820 } });
    page.on('console', message => {
      if (message.type() === 'error') {
        console.error(`browser console error: ${message.text()}`);
      }
    });
    page.on('pageerror', error => {
      console.error(`browser page error: ${error.message}`);
    });

    await page.addInitScript(fakeWebSocketScript(scenario));
    await page.goto(`${baseURL}/status.html?token=completed-repgroup-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });

    if (scenario === 'deleted-refresh') {
      await page.waitForFunction(() => {
        const statusElement = document.getElementById('status');
        const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
        const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
        const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];

        return window.__wrCompletedRepGroupFixture.phase === 'live-four-complete' &&
          repGroup && repGroup.total() === 6 && repGroup.running() === 2 &&
          repGroup.complete() === 4 && repGroup.deleted() === 0;
      }, undefined, { timeout: 10000 });
      await page.waitForTimeout(500);
      trace.samples.push(await sampleStatus(page, 'live update keeps four successful completions'));

      await page.reload({ waitUntil: 'networkidle', timeout: 30000 });
      await page.waitForSelector('body.ko-initialized', { timeout: 10000 });

      await page.waitForFunction(() => {
        const statusElement = document.getElementById('status');
        const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
        const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
        const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];

        return window.__wrCompletedRepGroupFixture.phase === 'refreshed-authoritative-seed' &&
          repGroup && repGroup.total() === 6 && repGroup.running() === 2 &&
          repGroup.complete() === 4 && repGroup.deleted() === 0;
      }, undefined, { timeout: 10000 });
      await page.waitForTimeout(500);
      trace.samples.push(await sampleStatus(page, 'refresh preserves four successful completions'));

      await page.waitForFunction(() => {
        const statusElement = document.getElementById('status');
        const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
        const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
        const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];
        const completeBar = document.querySelector('[data-repgroup="done-rg"] .progress-bar-success');

        return window.__wrCompletedRepGroupFixture.phase === 'later-dirty-fifth-complete' &&
          repGroup && repGroup.total() === 6 && repGroup.running() === 1 &&
          repGroup.complete() === 5 && repGroup.deleted() === 0 && completeBar &&
          completeBar.style.width === '83%' && completeBar.textContent.includes('5 complete');
      }, undefined, { timeout: 10000 });
      await page.waitForTimeout(200);

      const afterFifthComplete = await sampleStatus(page, 'later dirty update keeps five successful completions');
      trace.samples.push(afterFifthComplete);
      assertDeletedRefreshMatchesAuthoritativeStatus(afterFifthComplete);
      await page.screenshot({ path: screenshotOutputPath, fullPage: true });

      const fixtureState = await page.evaluate(() => ({
        requests: window.__wrCompletedRepGroupFixture.requests,
        messages: window.__wrCompletedRepGroupFixture.messages,
        samples: window.__wrCompletedRepGroupFixture.samples,
        intervalTimers: window.__wrCompletedRepGroupFixture.intervalTimers
      }));
      trace.requests = fixtureState.requests;
      trace.messages = fixtureState.messages;
      trace.intervalTimers = fixtureState.intervalTimers;
      trace.browserSamples = fixtureState.samples;

      if (trace.intervalTimers.some((timer) => timer.delay === 10000)) {
        throw new Error(`status page registered blind periodic current timer: ${JSON.stringify(trace.intervalTimers)}`);
      }
    } else {
      await page.waitForFunction(() => {
        const statusElement = document.getElementById('status');
        const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
        const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
        const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];

        return repGroup && repGroup.ready() === 2 && repGroup.total() === 2;
      }, undefined, { timeout: 10000 });
      trace.samples.push(await sampleStatus(page, 'initial ready visible'));

      await page.waitForFunction(() => {
        const statusElement = document.getElementById('status');
        const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
        const repGroupIndex = viewModel ? viewModel.repGroupLookup['done-rg'] : undefined;
        const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];
        const completeBar = document.querySelector('[data-repgroup="done-rg"] .progress-bar-success');

        return repGroup &&
          repGroup.complete() === 2 &&
          repGroup.completePct() > 0 &&
          completeBar &&
          completeBar.textContent.includes('2 complete') &&
          completeBar.style.width !== '0%' &&
          completeBar.style.width !== '0px';
      }, undefined, { timeout: 10000 });
      const afterLiveComplete = await sampleStatus(page, 'after live completion');
      trace.samples.push(afterLiveComplete);
      assertCompletedRepGroupVisible(afterLiveComplete);

      // After a later re-send of the (now empty) live aggregate, the completed
      // RepGroup row must remain visible with its complete count.
      await page.waitForFunction(() => {
        return window.__wrCompletedRepGroupFixture.phase === 'live-aggregate-resend';
      }, undefined, { timeout: 10000 });
      await page.waitForTimeout(500);
      const afterResync = await sampleStatus(page, 'after empty live aggregate resend');
      trace.samples.push(afterResync);
      assertCompletedRepGroupVisible(afterResync);

      const fixtureState = await page.evaluate(() => ({
        requests: window.__wrCompletedRepGroupFixture.requests,
        messages: window.__wrCompletedRepGroupFixture.messages,
        samples: window.__wrCompletedRepGroupFixture.samples,
        intervalTimers: window.__wrCompletedRepGroupFixture.intervalTimers
      }));
      trace.requests = fixtureState.requests;
      trace.messages = fixtureState.messages;
      trace.intervalTimers = fixtureState.intervalTimers;
      trace.browserSamples = fixtureState.samples;

      if (trace.intervalTimers.some((timer) => timer.delay === 10000)) {
        throw new Error(`status page registered blind periodic current timer: ${JSON.stringify(trace.intervalTimers)}`);
      }

      await page.screenshot({ path: screenshotOutputPath, fullPage: true });
    }
  } catch (error) {
    capturedError = error;
    trace.failure = {
      message: error.message,
      stack: error.stack
    };

    if (page) {
      try {
        trace.samples.push(await sampleStatus(page, 'failure state'));
        const fixtureState = await page.evaluate(() => ({
          requests: window.__wrCompletedRepGroupFixture.requests,
          messages: window.__wrCompletedRepGroupFixture.messages,
          samples: window.__wrCompletedRepGroupFixture.samples,
          intervalTimers: window.__wrCompletedRepGroupFixture.intervalTimers,
          phase: window.__wrCompletedRepGroupFixture.phase,
          currentRequests: window.__wrCompletedRepGroupFixture.currentRequests
        }));
        trace.requests = fixtureState.requests;
        trace.messages = fixtureState.messages;
        trace.intervalTimers = fixtureState.intervalTimers;
        trace.browserSamples = fixtureState.samples;
        trace.fixtureState = fixtureState;
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

await captureScreenshot();
