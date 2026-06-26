#!/usr/bin/env node

// Browser regression fixture for issue 260625-7: the status web UI "flickers so
// fast it looks like it's not there" and the per-RepGroup total "keeps rising
// above the total number of jobs actually added".
//
// It serves the real jobqueue/static status page, injects a fake WebSocket that
// drives the real websocket-handler.js, runs a high-rate transition storm, and
// asserts the two symptoms never occur:
//   (a) flicker: a RepGroup row dropping to total 0 / disappearing while jobs
//       still exist;
//   (b) overcount: a RepGroup total exceeding the number of jobs added.
//
// WR_FIXTURE_PROTOCOL selects how the fake socket drives the handler:
//   - 'delta'    (default): the legacy non-idempotent delta protocol over a
//     faithful model of the server's lossy 1-slot coalescing caster, where an
//     overflowing delta is replaced by a single StatusResync and the resulting
//     current-snapshot fragments also overflow. This reproduces both symptoms on
//     the pre-fix code, so the assertions FAIL.
//   - 'absolute': the idempotent absolute per-RepGroup protocol ({RepGroup,
//     Counts}). Dropping intermediate messages is harmless (skip-to-newest), so
//     both symptoms are designed out and the assertions PASS.
// The behavioural assertions are identical for both protocols.

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const protocol = process.env.WR_FIXTURE_PROTOCOL === 'delta' ? 'delta' : 'absolute';
const suffix = protocol === 'delta' ? '-delta' : '';
const defaultScreenshotOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-repgroup-flicker-overcount${suffix}.png`);
const defaultTraceOutput = path.join(repoRoot, `.tmp/agent/webui-test/status-webui-repgroup-flicker-overcount${suffix}-trace.json`);
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

// The fixture drives a storm of new->ready->running->complete transitions for a
// fixed number of jobs in one RepGroup. Both protocol implementations share the
// same ground-truth state machine; they differ only in what gets put on the
// wire and how the lossy channel behaves.
function fakeWebSocketScript(protocol) {
  return `(() => {
    const PROTOCOL = ${JSON.stringify(protocol)};
    const TOTAL_JOBS = 12000;
    const REPGROUP = 'stormrg';

    window.__wrFlickerFixture = {
      protocol: PROTOCOL,
      totalJobs: TOTAL_JOBS,
      repGroup: REPGROUP,
      requests: [],
      currentRequests: 0,
      sentMessages: 0,
      deliveredMessages: 0,
      droppedMessages: 0,
      resyncs: 0,
      phase: 'boot',
      done: false,
      // High-rate in-page sampler aggregates, so transient flicker/overcount
      // during the storm is observed even though Playwright round-trips are slow.
      sampleCount: 0,
      flickerEvents: 0,
      worstFlickerTotal: null,
      overcountEvents: 0,
      worstOvercountTotal: 0,
      rowPopulated: false
    };

    // Sample the live Knockout view model ~200 Hz from inside the page.
    function sampleNow() {
      const fixture = window.__wrFlickerFixture;
      const statusElement = document.getElementById('status');
      const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
      if (!viewModel) {
        return;
      }
      const index = viewModel.repGroupLookup[fixture.repGroup];
      const repGroup = index === undefined ? null : viewModel.repGroups[index];
      const total = repGroup ? repGroup.total() : 0;

      fixture.sampleCount += 1;

      // The flicker symptom is a row that, having been visibly populated,
      // disappears or drops to 0 while jobs still exist. We only start judging
      // once the row has shown a positive total at least once, so the brief
      // pre-population render latency (the same on any page load) is not counted.
      if (total > 0) {
        fixture.rowPopulated = true;
      }

      if (fixture.rowPopulated) {
        if (!repGroup || total === 0) {
          fixture.flickerEvents += 1;
          fixture.worstFlickerTotal = total;
        }

        if (total > fixture.totalJobs) {
          fixture.overcountEvents += 1;
          if (total > fixture.worstOvercountTotal) {
            fixture.worstOvercountTotal = total;
          }
        }
      }
    }

    window.__wrFlickerSampler = setInterval(sampleNow, 5);

    function emptyCounts() {
      return { dependent: 0, delayed: 0, suspended: 0, ready: 0, running: 0, lost: 0, buried: 0, deleted: 0, complete: 0 };
    }

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        // ground-truth absolute counts the "server" holds.
        this.groupCounts = emptyCounts();
        this.allCounts = emptyCounts();
        this.groupCounts.ready = TOTAL_JOBS;
        this.allCounts.ready = TOTAL_JOBS;
        this.started = 0;
        this.complete = 0;
        this.running = 0;
        this.ticks = 0;
        window.__wrFlickerFixture.socket = this;

        setTimeout(() => {
          this.readyState = 1;
          window.__wrFlickerFixture.phase = 'open';
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        window.__wrFlickerFixture.requests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        window.__wrFlickerFixture.currentRequests += 1;

        // The scenario drives all subsequent messages itself; we only need the
        // first 'current' (sent on connect) to start it.
        if (window.__wrFlickerFixture.currentRequests === 1) {
          this.startStorm();
        }
      }

      raw(message) {
        if (this.onmessage) {
          this.onmessage({ data: JSON.stringify(message) });
        }
      }

      // deliverAbsolute models the server's coalescing per-client sender: it
      // sends the CURRENT absolute counts, and dropping intermediate messages is
      // harmless because the next delivered message overwrites wholesale.
      deliverAbsolute(repGroup, counts, force) {
        window.__wrFlickerFixture.sentMessages += 1;

        if (!force && (window.__wrFlickerFixture.sentMessages % 11) !== 0) {
          window.__wrFlickerFixture.droppedMessages += 1;
          return;
        }

        window.__wrFlickerFixture.deliveredMessages += 1;
        this.raw({ RepGroup: repGroup, Counts: Object.assign({}, counts) });
      }

      // ---- storm drivers ----

      startStorm() {
        window.__wrFlickerFixture.phase = 'storm';

        if (PROTOCOL === 'absolute') {
          this.runAbsoluteStorm();
        } else {
          this.runDeltaScenario();
        }
      }

      // runAbsoluteStorm drives a genuine high-rate transition storm over the
      // idempotent absolute protocol, dropping ~90% of intermediate messages.
      // Because each delivered message carries the current absolute state, the
      // row never flickers to zero and never overshoots; it converges exactly.
      runAbsoluteStorm() {
        this.deliverAbsolute(REPGROUP, this.groupCounts, true);
        this.deliverAbsolute('+all+', this.allCounts, true);

        const batch = 400;
        const tick = () => {
          this.ticks += 1;

          if (this.started < TOTAL_JOBS) {
            const move = Math.min(batch, TOTAL_JOBS - this.started);
            this.started += move;
            this.groupCounts.ready -= move;
            this.allCounts.ready -= move;
            this.groupCounts.running += move;
            this.allCounts.running += move;
            this.running += move;
          }

          if (this.complete < this.started - batch || this.started >= TOTAL_JOBS) {
            const move = Math.min(batch, this.running);
            if (move > 0) {
              this.running -= move;
              this.complete += move;
              this.groupCounts.running -= move;
              this.groupCounts.complete += move;
              this.allCounts.running -= move;
            }
          }

          this.deliverAbsolute('+all+', this.allCounts, false);
          this.deliverAbsolute(REPGROUP, this.groupCounts, false);

          if (this.started >= TOTAL_JOBS && this.complete >= TOTAL_JOBS) {
            // final authoritative push so the absolute UI converges exactly.
            this.deliverAbsolute(REPGROUP, this.groupCounts, true);
            this.deliverAbsolute('+all+', this.allCounts, true);
            window.__wrFlickerFixture.phase = 'converged';
            window.__wrFlickerFixture.done = true;
            return;
          }

          setTimeout(tick, 8);
        };

        setTimeout(tick, 8);
      }

      // runDeltaScenario reproduces the two documented symptoms of the legacy
      // non-idempotent delta protocol over a lossy 1-slot caster, with enough
      // dwell time between phases that the ~200 Hz sampler reliably observes
      // each one:
      //   1. the row appears populated (ready = TOTAL_JOBS);
      //   2. storm deltas overflow and are lost; an overflow becomes a
      //      StatusResync; the resulting 'current' snapshot's per-RepGroup
      //      fragments also overflow, so an EMPTY snapshot completes and
      //      pruneEmptyLiveRepGroups deletes the row -> FLICKER (total 0 while
      //      jobs exist);
      //   3. a later snapshot reports complete = TOTAL_JOBS, then a lagging
      //      running->complete delta arrives after SnapshotDone and is added
      //      again additively -> OVERCOUNT (total > jobs added).
      runDeltaScenario() {
        this.raw({ RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: TOTAL_JOBS });
        this.raw({ RepGroup: REPGROUP, FromState: 'new', ToState: 'ready', Count: TOTAL_JOBS });

        const phaseDwell = 700;

        // phase 2: empty resync snapshot prunes the populated row (flicker). The
        // snapshot begins with an empty +all+ fragment (the per-RepGroup count
        // fragments were lost to overflow) then completes, so the client applies
        // an empty snapshot and pruneEmptyLiveRepGroups deletes the row.
        setTimeout(() => {
          window.__wrFlickerFixture.phase = 'flicker';
          window.__wrFlickerFixture.resyncs += 1;
          const id = window.__wrFlickerFixture.currentRequests + 1;
          window.__wrFlickerFixture.currentRequests = id;
          this.raw({ RepGroup: '+all+', FromState: 'new', ToState: '', Count: 0, SnapshotID: id });
          this.raw({ RepGroup: '+all+', SnapshotID: id, SnapshotDone: true });
        }, phaseDwell);

        // phase 3: a populated snapshot then a lagging delta overshoots (overcount).
        setTimeout(() => {
          window.__wrFlickerFixture.phase = 'overcount';
          const id = window.__wrFlickerFixture.currentRequests + 1;
          window.__wrFlickerFixture.currentRequests = id;
          this.raw({ RepGroup: REPGROUP, FromState: 'new', ToState: 'complete', Count: TOTAL_JOBS, SnapshotID: id });
          this.raw({ RepGroup: '+all+', SnapshotID: id, SnapshotDone: true });
          // lagging duplicate completion delta after the snapshot already counted them.
          this.raw({ RepGroup: REPGROUP, FromState: 'running', ToState: 'complete', Count: 400 });
        }, phaseDwell * 2);

        setTimeout(() => {
          window.__wrFlickerFixture.phase = 'converged';
          window.__wrFlickerFixture.done = true;
        }, phaseDwell * 3);
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }
    }

    window.WebSocket = FixtureWebSocket;
  })();`;
}

async function sampleRepGroup(page) {
  return page.evaluate(() => {
    const statusElement = document.getElementById('status');
    const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
    const rg = window.__wrFlickerFixture.repGroup;
    const repGroupIndex = viewModel ? viewModel.repGroupLookup[rg] : undefined;
    const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];

    return {
      phase: window.__wrFlickerFixture.phase,
      done: window.__wrFlickerFixture.done,
      repGroupExists: Boolean(repGroup),
      repGroupTotal: repGroup ? repGroup.total() : null,
      repGroupReady: repGroup ? repGroup.ready() : null,
      repGroupRunning: repGroup ? repGroup.running() : null,
      repGroupComplete: repGroup ? repGroup.complete() : null
    };
  });
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
    scenario: 'high-rate transition storm: no flicker (row never drops to 0 while jobs exist) and no overcount (total never exceeds jobs added)',
    protocol,
    source: {
      page: 'jobqueue/static/status.html',
      handler: 'jobqueue/static/js/wr/websocket-handler.js'
    },
    artifacts: { screenshot: screenshotOutputPath, trace: traceOutputPath },
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

    await page.addInitScript(fakeWebSocketScript(protocol));
    await page.goto(`${baseURL}/status.html?token=flicker-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });

    const totalJobs = await page.evaluate(() => window.__wrFlickerFixture.totalJobs);

    // The in-page sampler (~200 Hz) records flicker/overcount throughout the
    // storm; Playwright only waits for the storm to finish then reads the
    // aggregates. We do NOT block on the row staying present: a vanished row is
    // exactly the flicker symptom we are asserting against.
    await page.waitForFunction(() => window.__wrFlickerFixture.done, undefined, { timeout: 25000 });

    // Let the rate-limited observables settle, sampling a little longer so a late
    // flicker/overcount from observable settling is still caught.
    await page.waitForTimeout(800);

    const finalSample = await sampleRepGroup(page);
    const fixtureState = await page.evaluate(() => ({
      samples: window.__wrFlickerFixture.sampleCount,
      flickerEvents: window.__wrFlickerFixture.flickerEvents,
      worstFlickerTotal: window.__wrFlickerFixture.worstFlickerTotal,
      overcountEvents: window.__wrFlickerFixture.overcountEvents,
      worstOvercountTotal: window.__wrFlickerFixture.worstOvercountTotal,
      sentMessages: window.__wrFlickerFixture.sentMessages,
      deliveredMessages: window.__wrFlickerFixture.deliveredMessages,
      droppedMessages: window.__wrFlickerFixture.droppedMessages,
      resyncs: window.__wrFlickerFixture.resyncs,
      currentRequests: window.__wrFlickerFixture.currentRequests
    }));

    const flickerEvents = fixtureState.flickerEvents;
    const worstFlickerTotal = fixtureState.worstFlickerTotal;
    const overcountEvents = fixtureState.overcountEvents;
    const worstOvercountTotal = fixtureState.worstOvercountTotal;
    const samples = fixtureState.samples;

    trace.totalJobs = totalJobs;
    trace.samples = samples;
    trace.flickerEvents = flickerEvents;
    trace.worstFlickerTotal = worstFlickerTotal;
    trace.overcountEvents = overcountEvents;
    trace.worstOvercountTotal = worstOvercountTotal;
    trace.finalSample = finalSample;
    trace.fixtureState = fixtureState;

    if (flickerEvents > 0) {
      throw new Error(`flicker detected: row dropped to 0/disappeared ${flickerEvents} times (worst total ${worstFlickerTotal}) while ${totalJobs} jobs existed across ${samples} samples`);
    }

    if (overcountEvents > 0) {
      throw new Error(`overcount detected: total exceeded ${totalJobs} jobs ${overcountEvents} times (worst ${worstOvercountTotal}) across ${samples} samples`);
    }

    if (!finalSample.repGroupExists || finalSample.repGroupTotal !== totalJobs || finalSample.repGroupComplete !== totalJobs) {
      throw new Error(`expected row to converge to ${totalJobs} complete, saw ${JSON.stringify(finalSample)}`);
    }

    await page.screenshot({ path: screenshotOutputPath, fullPage: true });
  } catch (error) {
    capturedError = error;
    trace.failure = { message: error.message, stack: error.stack };

    if (page) {
      try {
        trace.failureSample = await sampleRepGroup(page);
        trace.fixtureState = await page.evaluate(() => ({
          sentMessages: window.__wrFlickerFixture.sentMessages,
          deliveredMessages: window.__wrFlickerFixture.deliveredMessages,
          droppedMessages: window.__wrFlickerFixture.droppedMessages,
          resyncs: window.__wrFlickerFixture.resyncs,
          currentRequests: window.__wrFlickerFixture.currentRequests,
          phase: window.__wrFlickerFixture.phase,
          done: window.__wrFlickerFixture.done
        }));
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
