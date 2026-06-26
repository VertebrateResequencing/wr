#!/usr/bin/env node

// Browser regression fixture for the residual status web UI bar-flicker bug
// (follow-up to issue 260625-7 / commit 4e306f7). The absolute per-RepGroup
// data is correct and converges, but during a high-rate job-state storm the
// per-RepGroup progress bar "is basically invisible due to flickering": the
// stacked bar collapses to ~0 width on every update and pops back, instead of
// the segments smoothly changing colour proportion while the bar stays full.
//
// Requirement (verbatim): "Bars in the frontend should smoothly reduce or
// increase in size as their numbers change, not be cleared and redrawn on
// every change."
//
// This fixture serves the real jobqueue/static status page, injects a fake
// WebSocket that drives the real websocket-handler.js with a realistic storm
// of ABSOLUTE-state messages for one RepGroup ("echo"), moving ~10000 jobs
// ready -> running -> complete over hundreds of messages at storm rate, ending
// all-complete. A high-frequency in-page sampler (requestAnimationFrame) reads
// the ACTUAL rendered segment widths of `[data-repgroup="echo"] .progress-bar`
// (summed pixel width / container pixel width = filled %), and asserts:
//
//   (a) NO-FLICKER: once the RepGroup has jobs, the summed filled width never
//       collapses to ~0. A stacked bar is always ~full (segments sum to 99.9%),
//       so we require filled >= 95% and count "collapse frames" (filled < 50%
//       while total > 0); collapse frames must be 0.
//   (b) NOT-RECREATED: the segment DOM nodes are stamped once and must persist
//       across the whole storm (Knockout must not destroy/recreate them).
//   (c) SMOOTH: consecutive samples never jump from ~full to ~0 (bounded
//       per-frame delta), and the bar converges to the correct final
//       proportions (complete ~= 99.9%).
//
// It FAILS against the pre-fix websocket-handler.js / inflight-tracking.js
// (which zero every pct observable on each update, collapsing the bar) and
// PASSES after the fix. It is wired into `make browser-test`.

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultScreenshotOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-repgroup-bar-flicker.png');
const defaultTraceOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-repgroup-bar-flicker-trace.json');
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

// The fake socket drives a realistic high-rate storm of ABSOLUTE-state messages
// for one RepGroup, moving ~10000 jobs ready -> running -> complete over
// hundreds of messages, ending all-complete. Each message carries the current
// absolute per-state counts ({ RepGroup, Counts }), exactly as the server
// pushes. An in-page sampler reads the rendered segment widths of the bar.
function fakeWebSocketScript() {
  return `(() => {
    const TOTAL_JOBS = 10000;
    const REPGROUP = 'echo';
    // Number of absolute-state messages used to walk the storm to completion.
    const STORM_MESSAGES = 300;
    // Real wall-clock gap between storm messages (storm rate ~ 20/sec).
    const MESSAGE_GAP_MS = 50;

    window.__wrBarFixture = {
      totalJobs: TOTAL_JOBS,
      repGroup: REPGROUP,
      stormMessages: STORM_MESSAGES,
      requests: [],
      currentRequests: 0,
      deliveredMessages: 0,
      phase: 'boot',
      done: false,
      // ---- rendered-DOM sampler aggregates ----
      sampleCount: 0,
      barPopulated: false,
      // segmentsSeen flips true on the first frame the bar's segment nodes
      // actually exist in the DOM (which can lag total() > 0 by a rate-limit
      // window on initial render). Recreation is only judged after this.
      segmentsSeen: false,
      // Filled width = sum(segment rendered px width) / container px width * 100.
      minFilledAfterPopulated: null,
      maxFilledAfterPopulated: null,
      // Every frame with measurable width while the bar has jobs.
      populatedFrames: 0,
      // A collapse frame: bar populated (total > 0) but rendered filled < 50%.
      collapseFrames: 0,
      worstCollapseFilled: null,
      // Frames where the always-full stacked bar is not ~full (< 95%): the
      // bar is "cleared / not smoothly sized" rather than just changing colour.
      framesBelow95: 0,
      // Largest downward jump in filled% between consecutive frames while populated.
      maxDownwardJump: 0,
      prevFilled: null,
      // Node-identity tracking: stamp each segment once. After the segment set
      // has first appeared, any brand-new (unstamped) segment node means
      // Knockout destroyed and recreated the DOM (the bug clears+redraws it).
      stampSeq: 0,
      stampedNodes: 0,
      nodeRecreations: 0,
      lastStampSignature: null,
      finalFilled: null,
      finalCompletePct: null
    };

    const stampMarker = '__wrBarStamp';

    function progressContainer() {
      return document.querySelector('[data-repgroup="' + REPGROUP + '"] .progress');
    }

    function segmentNodes() {
      const container = progressContainer();
      if (!container) {
        return [];
      }
      return Array.prototype.slice.call(container.querySelectorAll('.progress-bar'));
    }

    // Sample the ACTUAL rendered bar at animation-frame rate.
    function sampleNow() {
      const fixture = window.__wrBarFixture;
      const statusElement = document.getElementById('status');
      const viewModel = window.ko && statusElement ? window.ko.dataFor(statusElement) : null;
      if (!viewModel) {
        return;
      }
      const index = viewModel.repGroupLookup[REPGROUP];
      const repGroup = index === undefined ? null : viewModel.repGroups[index];
      const total = repGroup ? repGroup.total() : 0;

      fixture.sampleCount += 1;

      const container = progressContainer();
      const segments = segmentNodes();

      // Stamp every freshly-seen segment node exactly once. If a stamped node is
      // ever discarded and replaced by a new node, that is DOM recreation.
      let newlyStamped = 0;
      let signatureParts = [];
      for (const seg of segments) {
        if (seg[stampMarker] === undefined) {
          fixture.stampSeq += 1;
          seg[stampMarker] = fixture.stampSeq;
          seg.setAttribute('data-wr-bar-stamp', String(fixture.stampSeq));
          fixture.stampedNodes += 1;
          newlyStamped += 1;
        }
        signatureParts.push(seg[stampMarker]);
      }
      const signature = signatureParts.join(',');

      // The segment set is created once (when the bar first renders) and must
      // then persist. A brand-new stamp appearing AFTER we have already seen the
      // segments means Knockout tore the bar down and rebuilt it => recreation.
      if (fixture.segmentsSeen && newlyStamped > 0) {
        fixture.nodeRecreations += newlyStamped;
      }
      if (segments.length > 0) {
        fixture.segmentsSeen = true;
      }
      fixture.lastStampSignature = signature;

      if (total > 0) {
        fixture.barPopulated = true;
      }

      // Compute rendered filled width as a percentage of the container width.
      let filled = null;
      if (container && segments.length > 0) {
        const containerWidth = container.getBoundingClientRect().width;
        if (containerWidth > 0) {
          let sum = 0;
          for (const seg of segments) {
            sum += seg.getBoundingClientRect().width;
          }
          filled = (sum / containerWidth) * 100;
        }
      }

      if (fixture.barPopulated && filled !== null) {
        fixture.populatedFrames += 1;

        if (fixture.minFilledAfterPopulated === null || filled < fixture.minFilledAfterPopulated) {
          fixture.minFilledAfterPopulated = filled;
        }
        if (fixture.maxFilledAfterPopulated === null || filled > fixture.maxFilledAfterPopulated) {
          fixture.maxFilledAfterPopulated = filled;
        }

        if (filled < 95) {
          fixture.framesBelow95 += 1;
        }

        if (total > 0 && filled < 50) {
          fixture.collapseFrames += 1;
          if (fixture.worstCollapseFilled === null || filled < fixture.worstCollapseFilled) {
            fixture.worstCollapseFilled = filled;
          }
        }

        if (fixture.prevFilled !== null) {
          const drop = fixture.prevFilled - filled;
          if (drop > fixture.maxDownwardJump) {
            fixture.maxDownwardJump = drop;
          }
        }
        fixture.prevFilled = filled;
      }
    }

    function frameLoop() {
      sampleNow();
      window.requestAnimationFrame(frameLoop);
    }
    window.requestAnimationFrame(frameLoop);

    function emptyCounts() {
      return { dependent: 0, delayed: 0, suspended: 0, ready: 0, running: 0, lost: 0, buried: 0, deleted: 0, complete: 0 };
    }

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        this.step = 0;
        window.__wrBarFixture.socket = this;

        setTimeout(() => {
          this.readyState = 1;
          window.__wrBarFixture.phase = 'open';
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        window.__wrBarFixture.requests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        window.__wrBarFixture.currentRequests += 1;

        if (window.__wrBarFixture.currentRequests === 1) {
          this.startStorm();
        }
      }

      raw(message) {
        window.__wrBarFixture.deliveredMessages += 1;
        if (this.onmessage) {
          this.onmessage({ data: JSON.stringify(message) });
        }
      }

      // Compute the ground-truth absolute counts at storm step n (0..STORM_MESSAGES).
      // Jobs start all ready, then progress smoothly: by step n a fraction have
      // started (ready -> running) and a slightly smaller fraction have completed
      // (running -> complete). At the final step everything is complete.
      countsAtStep(n) {
        const frac = Math.min(1, n / STORM_MESSAGES);
        // started leads complete so there is always a running cohort mid-storm.
        const started = Math.min(TOTAL_JOBS, Math.round(TOTAL_JOBS * Math.min(1, frac * 1.15)));
        const complete = Math.round(TOTAL_JOBS * Math.max(0, frac - 0.1) / 0.9);
        const completeClamped = Math.min(started, Math.min(TOTAL_JOBS, complete));
        const running = started - completeClamped;
        const ready = TOTAL_JOBS - started;

        const counts = emptyCounts();
        counts.ready = ready;
        counts.running = running;
        counts.complete = completeClamped;
        return counts;
      }

      startStorm() {
        window.__wrBarFixture.phase = 'storm';

        // Initial absolute state: all jobs ready.
        const initial = this.countsAtStep(0);
        this.raw({ RepGroup: '+all+', Counts: { ready: TOTAL_JOBS } });
        this.raw({ RepGroup: REPGROUP, Counts: initial });

        const tick = () => {
          this.step += 1;
          const counts = this.countsAtStep(this.step);

          // The live +all+ aggregate only ever shows incomplete jobs.
          const allCounts = emptyCounts();
          allCounts.ready = counts.ready;
          allCounts.running = counts.running;

          this.raw({ RepGroup: '+all+', Counts: allCounts });
          this.raw({ RepGroup: REPGROUP, Counts: counts });

          if (this.step >= STORM_MESSAGES) {
            // Final authoritative all-complete push.
            const finalCounts = emptyCounts();
            finalCounts.complete = TOTAL_JOBS;
            this.raw({ RepGroup: REPGROUP, Counts: finalCounts });
            this.raw({ RepGroup: '+all+', Counts: emptyCounts() });
            window.__wrBarFixture.phase = 'converged';
            window.__wrBarFixture.done = true;
            return;
          }

          setTimeout(tick, MESSAGE_GAP_MS);
        };

        setTimeout(tick, MESSAGE_GAP_MS);
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
    const rg = window.__wrBarFixture.repGroup;
    const repGroupIndex = viewModel ? viewModel.repGroupLookup[rg] : undefined;
    const repGroup = repGroupIndex === undefined ? null : viewModel.repGroups[repGroupIndex];

    const container = document.querySelector('[data-repgroup="' + rg + '"] .progress');
    let filled = null;
    let completePct = null;
    if (container) {
      const segments = Array.prototype.slice.call(container.querySelectorAll('.progress-bar'));
      const containerWidth = container.getBoundingClientRect().width;
      if (containerWidth > 0) {
        let sum = 0;
        for (const seg of segments) {
          sum += seg.getBoundingClientRect().width;
        }
        filled = (sum / containerWidth) * 100;
      }
    }
    if (repGroup) {
      completePct = repGroup.completePct();
    }

    return {
      phase: window.__wrBarFixture.phase,
      done: window.__wrBarFixture.done,
      repGroupExists: Boolean(repGroup),
      repGroupTotal: repGroup ? repGroup.total() : null,
      repGroupReady: repGroup ? repGroup.ready() : null,
      repGroupRunning: repGroup ? repGroup.running() : null,
      repGroupComplete: repGroup ? repGroup.complete() : null,
      filledPct: filled,
      completePct
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
    scenario: 'high-rate transition storm: per-RepGroup bar smoothly changes size (segments stay ~full, never collapse/redraw to 0) and converges to all-complete',
    source: {
      page: 'jobqueue/static/status.html',
      handler: 'jobqueue/static/js/wr/websocket-handler.js',
      tracker: 'jobqueue/static/js/wr/inflight-tracking.js'
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

    await page.addInitScript(fakeWebSocketScript());
    await page.goto(`${baseURL}/status.html?token=bar-flicker-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });

    const totalJobs = await page.evaluate(() => window.__wrBarFixture.totalJobs);

    // Wait until the bar is rendered with jobs before judging (the brief
    // pre-population render latency is the same on any page load).
    await page.waitForFunction(() => window.__wrBarFixture.barPopulated, undefined, { timeout: 15000 });

    // Let the storm run to completion. The in-page rAF sampler records the
    // rendered bar widths throughout; we only wait for the storm to finish.
    await page.waitForFunction(() => window.__wrBarFixture.done, undefined, { timeout: 30000 });

    // Let the rate-limited observables settle and the final transition animate.
    await page.waitForTimeout(1200);

    const finalSample = await sampleRepGroup(page);
    const fixtureState = await page.evaluate(() => ({
      samples: window.__wrBarFixture.sampleCount,
      deliveredMessages: window.__wrBarFixture.deliveredMessages,
      barPopulated: window.__wrBarFixture.barPopulated,
      populatedFrames: window.__wrBarFixture.populatedFrames,
      minFilledAfterPopulated: window.__wrBarFixture.minFilledAfterPopulated,
      maxFilledAfterPopulated: window.__wrBarFixture.maxFilledAfterPopulated,
      collapseFrames: window.__wrBarFixture.collapseFrames,
      worstCollapseFilled: window.__wrBarFixture.worstCollapseFilled,
      framesBelow95: window.__wrBarFixture.framesBelow95,
      maxDownwardJump: window.__wrBarFixture.maxDownwardJump,
      stampedNodes: window.__wrBarFixture.stampedNodes,
      nodeRecreations: window.__wrBarFixture.nodeRecreations,
      currentRequests: window.__wrBarFixture.currentRequests
    }));

    trace.totalJobs = totalJobs;
    trace.finalSample = finalSample;
    trace.fixtureState = fixtureState;

    const collapseFrames = fixtureState.collapseFrames;
    const minFilled = fixtureState.minFilledAfterPopulated;
    const populatedFrames = fixtureState.populatedFrames;
    const framesBelow95 = fixtureState.framesBelow95;
    const nodeRecreations = fixtureState.nodeRecreations;
    const maxDownwardJump = fixtureState.maxDownwardJump;
    const below95Fraction = populatedFrames > 0 ? framesBelow95 / populatedFrames : 1;

    // Take the screenshot before assertions so the record exists even on
    // post-storm convergence; the storm visuals are gone by now but the final
    // converged bar (full green) is the documented end state.
    await page.screenshot({ path: screenshotOutputPath, fullPage: true });

    // (a) NO-FLICKER: a stacked bar is always ~full (segments sum to 99.9%); its
    // rendered filled width must never collapse toward 0 while jobs exist. This
    // is the symptom the user reports ("basically invisible due to flickering")
    // and the primary pre-fix/post-fix discriminator. Pre-fix, zeroing the pct
    // observables on every message drives the bound widths to 0%.
    if (collapseFrames > 0) {
      throw new Error(`bar flicker detected: ${collapseFrames}/${populatedFrames} populated frames collapsed (rendered filled width < 50% while jobs existed; worst ${fixtureState.worstCollapseFilled}%)`);
    }
    if (minFilled === null || minFilled < 95) {
      throw new Error(`bar collapsed: minimum rendered filled width after population was ${minFilled}% (expected >= 95% for an always-full stacked bar) across ${populatedFrames} populated frames`);
    }

    // (c) SMOOTH / ALWAYS-SIZED: the bar must stay ~full essentially all the
    // time (only its colour proportions change), not be repeatedly cleared and
    // redrawn. Pre-fix the bar spends the vast majority of frames below 95%; the
    // CSS width transition makes each per-frame step small, so the discriminator
    // is the SHARE of frames the bar is not full, not the per-frame delta. We
    // also guard against any single full->empty jump.
    if (below95Fraction > 0.02) {
      throw new Error(`bar not smoothly sized: ${framesBelow95}/${populatedFrames} populated frames (${(below95Fraction * 100).toFixed(1)}%) had rendered filled width < 95% (expected the stacked bar to stay ~full, only changing colour proportions)`);
    }
    if (maxDownwardJump > 50) {
      throw new Error(`bar not smooth: rendered filled width dropped ${maxDownwardJump}% between consecutive frames (expected a bounded, smooth change, not a clear-and-redraw)`);
    }

    // (b) NOT-RECREATED: the segment DOM nodes must persist across the storm
    // (Knockout must not destroy and rebuild the bar). This guards that the fix
    // keeps the existing nodes; it is a regression guard rather than the failing
    // discriminator, because the documented bug manifests as the bound widths
    // collapsing (above), not as DOM teardown.
    if (nodeRecreations > 0) {
      throw new Error(`bar segments recreated: ${nodeRecreations} new segment nodes appeared after the bar was populated (expected 0 - segments must be reused, not redrawn)`);
    }

    // Converges to the correct final proportions: all complete.
    if (!finalSample.repGroupExists || finalSample.repGroupTotal !== totalJobs || finalSample.repGroupComplete !== totalJobs) {
      throw new Error(`expected bar to converge to ${totalJobs} complete, saw ${JSON.stringify(finalSample)}`);
    }
    if (finalSample.completePct === null || finalSample.completePct < 99) {
      throw new Error(`expected converged complete segment ~99.9%, saw completePct ${finalSample.completePct}`);
    }
    if (finalSample.filledPct === null || finalSample.filledPct < 95) {
      throw new Error(`expected converged bar to be ~full, saw filled ${finalSample.filledPct}%`);
    }
  } catch (error) {
    capturedError = error;
    trace.failure = { message: error.message, stack: error.stack };

    if (page) {
      try {
        trace.failureSample = await sampleRepGroup(page);
        trace.fixtureState = await page.evaluate(() => ({
          samples: window.__wrBarFixture.sampleCount,
          deliveredMessages: window.__wrBarFixture.deliveredMessages,
          barPopulated: window.__wrBarFixture.barPopulated,
          populatedFrames: window.__wrBarFixture.populatedFrames,
          minFilledAfterPopulated: window.__wrBarFixture.minFilledAfterPopulated,
          maxFilledAfterPopulated: window.__wrBarFixture.maxFilledAfterPopulated,
          collapseFrames: window.__wrBarFixture.collapseFrames,
          worstCollapseFilled: window.__wrBarFixture.worstCollapseFilled,
          framesBelow95: window.__wrBarFixture.framesBelow95,
          maxDownwardJump: window.__wrBarFixture.maxDownwardJump,
          stampedNodes: window.__wrBarFixture.stampedNodes,
          nodeRecreations: window.__wrBarFixture.nodeRecreations,
          currentRequests: window.__wrBarFixture.currentRequests,
          phase: window.__wrBarFixture.phase,
          done: window.__wrBarFixture.done
        }));
      } catch (sampleError) {
        trace.failure.sampleError = sampleError.message;
      }
    }

    if (page) {
      try {
        await page.screenshot({ path: screenshotOutputPath, fullPage: true });
      } catch {
        // best-effort screenshot for the record
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
