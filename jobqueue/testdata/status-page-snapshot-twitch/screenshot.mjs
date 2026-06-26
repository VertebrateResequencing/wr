#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultGapOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-snapshot-twitch-gap.png');
const defaultRestoredOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-snapshot-twitch-restored.png');
const defaultTraceOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-snapshot-twitch-trace.json');
const gapOutputPath = path.resolve(process.cwd(), process.argv[2] || defaultGapOutput);
const restoredOutputPath = path.resolve(process.cwd(), process.argv[3] || defaultRestoredOutput);
const traceOutputPath = path.resolve(process.cwd(), process.argv[4] || defaultTraceOutput);

for (const outputPath of [gapOutputPath, restoredOutputPath, traceOutputPath]) {
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

function fakeWebSocketScript() {
  return `(() => {
    window.__wrTwitchFixture = {
      intervalTimers: [],
      requests: [],
      messages: [],
      samples: [],
      phase: 'boot',
      currentRequests: 0
    };

    const originalSetInterval = window.setInterval.bind(window);

    window.setInterval = (callback, delay, ...args) => {
      window.__wrTwitchFixture.intervalTimers.push({ delay });

      return originalSetInterval(callback, delay, ...args);
    };

    // The server holds the steady-state absolute counts for bigmod: 15000 jobs,
    // all dependent. Under the idempotent absolute protocol there is no snapshot
    // staging that could transiently reset the row, so re-sending the same
    // absolute state must not twitch the displayed count.
    const steadyState = [
      { RepGroup: '+all+', Counts: { dependent: 15000 } },
      { RepGroup: 'bigmod', Counts: { dependent: 15000 } }
    ];

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        window.__wrTwitchFixture.socket = this;

        setTimeout(() => {
          this.readyState = 1;
          window.__wrTwitchFixture.phase = 'open';
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        window.__wrTwitchFixture.requests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        window.__wrTwitchFixture.currentRequests += 1;

        // On connect, push the full current absolute state.
        window.__wrTwitchFixture.phase = 'initial-current';
        this.emitEach(steadyState, 5);

        // Then, after a steady-state period with no real change, re-send the
        // SAME absolute state (as a coalescing sender might on any wake). Because
        // the payload is idempotent and applied wholesale, the row must stay at
        // exactly 15000 with no twitch to a partial/zero value.
        setTimeout(() => {
          window.__wrTwitchFixture.phase = 'steady-resend-start';
          this.emitEach([steadyState[0]], 5);
          setTimeout(() => {
            window.__wrTwitchFixture.phase = 'steady-resend-finishing';
            this.emitEach([steadyState[1]], 5);
          }, 2500);
        }, 80);
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }

      emitEach(messages, delay = 5) {
        messages.forEach((message, index) => {
          setTimeout(() => {
            window.__wrTwitchFixture.messages.push(message);
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
    const repGroupIndex = viewModel ? viewModel.repGroupLookup.bigmod : undefined;
    const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];
    const repGroupElement = document.querySelector('[data-repgroup="bigmod"]');
    const badge = repGroupElement ? repGroupElement.querySelector('.badge') : null;
    const progress = repGroupElement ? repGroupElement.querySelector('.progress') : null;

    const status = {
      label: sampleLabel,
      phase: window.__wrTwitchFixture.phase,
      currentRequests: window.__wrTwitchFixture.currentRequests,
      socketReadyState: window.__wrTwitchFixture.socket ? window.__wrTwitchFixture.socket.readyState : null,
      socketSent: window.__wrTwitchFixture.socket ? window.__wrTwitchFixture.socket.sent.slice() : [],
      intervalTimers: window.__wrTwitchFixture.intervalTimers.slice(),
      inflightTotal: viewModel ? viewModel.inflight.total() : null,
      inflightDependent: viewModel ? viewModel.inflight.dependent() : null,
      repGroupExists: Boolean(repGroup),
      repGroupTotal: repGroup ? repGroup.total() : null,
      repGroupDependent: repGroup ? repGroup.dependent() : null,
      repGroupBadgeText: badge ? badge.textContent.trim() : null,
      repGroupProgressVisible: Boolean(progress),
      repGroupText: repGroupElement ? repGroupElement.innerText : null,
      messageCount: window.__wrTwitchFixture.messages.length,
      lastMessages: window.__wrTwitchFixture.messages.slice(-4)
    };

    window.__wrTwitchFixture.samples.push(status);

    return status;
  }, label);

  return sample;
}

async function captureScreenshot() {
  const { chromium } = await loadPlaywright();
  const server = await createStaticServer();
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}`;

  for (const outputPath of [gapOutputPath, restoredOutputPath, traceOutputPath]) {
    fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  }

  const browser = await chromium.launch({ headless: true });
  let page;
  let capturedError;
  const trace = {
    scenario: 'steady-state repgroup stays visible while explicit resync snapshot is incomplete',
    source: {
      page: 'jobqueue/static/status.html',
      handler: 'jobqueue/static/js/wr/websocket-handler.js'
    },
    artifacts: {
      gapScreenshot: gapOutputPath,
      restoredScreenshot: restoredOutputPath,
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

    await page.addInitScript(fakeWebSocketScript());
    await page.goto(`${baseURL}/status.html?token=twitch-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    await page.locator('[data-repgroup="bigmod"] .progress-bar', { hasText: '15000 dependent' })
      .waitFor({ timeout: 10000 });
    trace.samples.push(await sampleStatus(page, 'initial steady-state visible'));
    // Wait until the steady-state re-send has begun (the +all+ part arrives
    // first; the bigmod part is delayed), reproducing the window in which the
    // old snapshot machinery used to transiently twitch the row to zero.
    await page.waitForFunction(() => {
      return window.__wrTwitchFixture.phase === 'steady-resend-start' ||
        window.__wrTwitchFixture.phase === 'steady-resend-finishing';
    }, undefined, { timeout: 10000 });
    trace.samples.push(await sampleStatus(page, 'after steady-state resend start'));

    await page.waitForTimeout(900);
    const gapSample = await sampleStatus(page, 'during steady-state resend gap');
    trace.samples.push(gapSample);
    if (gapSample.repGroupTotal !== 15000 || gapSample.repGroupBadgeText !== '15000' || !gapSample.repGroupProgressVisible) {
      throw new Error(`expected bigmod to stay at 15000 during resend gap, saw ${JSON.stringify(gapSample)}`);
    }
    await page.screenshot({ path: gapOutputPath, fullPage: true });

    await page.waitForTimeout(1900);
    const restoredSample = await sampleStatus(page, 'after delayed repgroup resend');
    trace.samples.push(restoredSample);
    if (restoredSample.phase !== 'steady-resend-finishing' ||
      restoredSample.repGroupTotal !== 15000 ||
      restoredSample.repGroupDependent !== 15000) {
      throw new Error(`expected delayed resend to complete with bigmod still at 15000, saw ${JSON.stringify(restoredSample)}`);
    }
    await page.screenshot({ path: restoredOutputPath, fullPage: true });

    const fixtureState = await page.evaluate(() => ({
      requests: window.__wrTwitchFixture.requests,
      messages: window.__wrTwitchFixture.messages,
      samples: window.__wrTwitchFixture.samples,
      intervalTimers: window.__wrTwitchFixture.intervalTimers
    }));
    trace.requests = fixtureState.requests;
    trace.messages = fixtureState.messages;
    trace.intervalTimers = fixtureState.intervalTimers;
    trace.browserSamples = fixtureState.samples;

    const gap = trace.samples.find(sample => sample.label === 'during steady-state resend gap');
    const restored = trace.samples.find(sample => sample.label === 'after delayed repgroup resend');
    if (!gap || gap.repGroupTotal !== 15000 || gap.repGroupBadgeText !== '15000' || !gap.repGroupProgressVisible) {
      throw new Error(`expected bigmod to stay at 15000 during resend gap, saw ${JSON.stringify(gap)}`);
    }
    if (!restored || restored.repGroupTotal !== 15000 || restored.repGroupDependent !== 15000) {
      throw new Error(`expected bigmod to stay at 15000 dependent, saw ${JSON.stringify(restored)}`);
    }
    if (trace.intervalTimers.some((timer) => timer.delay === 10000)) {
      throw new Error(`status page registered blind periodic current timer: ${JSON.stringify(trace.intervalTimers)}`);
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
          requests: window.__wrTwitchFixture.requests,
          messages: window.__wrTwitchFixture.messages,
          samples: window.__wrTwitchFixture.samples,
          intervalTimers: window.__wrTwitchFixture.intervalTimers,
          phase: window.__wrTwitchFixture.phase,
          currentRequests: window.__wrTwitchFixture.currentRequests,
          socketReadyState: window.__wrTwitchFixture.socket ? window.__wrTwitchFixture.socket.readyState : null,
          socketSent: window.__wrTwitchFixture.socket ? window.__wrTwitchFixture.socket.sent : []
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

  console.log(gapOutputPath);
  console.log(restoredOutputPath);
  console.log(traceOutputPath);
}

await captureScreenshot();
