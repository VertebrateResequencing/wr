#!/usr/bin/env node

// Browser regression fixture for the status web UI flicker / transient-overcount
// family (.docs/flicker/). Companion to repgroup-bar-flicker (which covers
// smooth rendering under an ORDERED storm); this one covers the harder,
// order-and-seed problems that the pre-fix client gets WRONG:
//
//   * connect DURING a burst (the scan-on-connect SEED RACE): live deltas that
//     arrive around the connect handshake overlap the snapshot seed. The pre-fix
//     client PERMANENTLY diverges - it ends the storm showing MORE jobs than
//     exist and more "complete" than were added. This fixture drives exactly
//     that sequence and asserts the bar converges to the true totals.
//   * OUT-OF-ORDER emission during the burst: the server emits each transition
//     from its own goroutine, so a job's running->complete delta can arrive
//     before its ready->running delta. The stacked bar must stay ~full and never
//     collapse/redraw, and must converge exactly.
//
// The seed-race convergence check is a DETERMINISTIC discriminator: pre-fix it
// ends (e.g.) 1800 total / 1650 complete for 1500 jobs; post-fix it ends
// 1500/1500. (A real browser's 350ms Knockout rate-limit coalesces the purely
// transient overcount, so this fixture keys its hard assertion on the permanent
// divergence and on bar collapse, and records the transient peak in the trace
// for information. The deterministic count-level checks live in
// reconcile-harness.mjs / TestStatusCountReconcile.)
//
// Serves the real jobqueue/static status page, injects a fake WebSocket driving
// the real websocket-handler.js, samples the rendered bar at animation-frame
// rate, screenshots, writes a trace, and FAILS (non-zero exit) if the bar
// collapses or the final totals do not converge. Wired into `make browser-test`.

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultScreenshotOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-count-reconcile.png');
const defaultTraceOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-count-reconcile-trace.json');
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
    case '.css': return 'text/css; charset=utf-8';
    case '.js': return 'text/javascript; charset=utf-8';
    case '.woff': return 'application/font-woff';
    case '.woff2': return 'application/font-woff2';
    case '.ttf': return 'application/x-font-truetype';
    case '.eot': return 'application/vnd.ms-fontobject';
    case '.svg': return 'image/svg+xml';
    case '.ico': return 'image/x-icon';
    default: return 'text/html; charset=utf-8';
  }
}

function staticPathFor(requestPath) {
  const cleanPath = decodeURIComponent(requestPath.split('?')[0]);
  const relative = cleanPath === '/' || cleanPath === '/status' ? 'status.html' : cleanPath.replace(/^\/+/, '');
  const resolved = path.resolve(staticRoot, relative);
  if (resolved !== staticRoot && !resolved.startsWith(staticRoot + path.sep)) {
    return null;
  }
  return resolved;
}

function createStaticServer() {
  const server = http.createServer((req, res) => {
    const filePath = staticPathFor(req.url || '/');
    if (!filePath) { res.writeHead(403).end('forbidden'); return; }
    fs.readFile(filePath, (error, data) => {
      if (error) { res.writeHead(404).end('not found'); return; }
      res.writeHead(200, { 'Content-Type': contentType(filePath) });
      res.end(data);
    });
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

// The fake socket reproduces a connect-during-burst seed race followed by an
// out-of-order drain to completion, deterministically (seeded PRNG).
function fakeWebSocketScript() {
  return `(() => {
    const TOTAL = 1500, REPGROUP = 'echo';
    // deterministic PRNG so the fixture never flakes
    let __a = 0x9e3779b9 >>> 0;
    function rand(){ __a|=0; __a=(__a+0x6D2B79F5)|0; let t=Math.imul(__a^(__a>>>15),1|__a); t=(t+Math.imul(t^(t>>>7),61|t))^t; return ((t^(t>>>14))>>>0)/4294967296; }

    window.__wrReconcile = {
      totalJobs: TOTAL, repGroup: REPGROUP, phase: 'boot', done: false,
      // rendered-bar sampler
      barFilledOnce: false, segmentsSeen: false, populatedFrames: 0,
      minFilledAfterPopulated: null, collapseFrames: 0, worstCollapseFilled: null,
      framesBelow95: 0, nodeRecreations: 0, stampSeq: 0, stampedNodes: 0,
      // transient overcount peak (informational; rate-limit coalesces most of it)
      maxRgTotal: 0, maxAllTotal: 0, peakOvercount: 0,
      finalRgTotal: null, finalRgComplete: null, finalAll: null
    };
    const stampMarker = '__wrReconcileStamp';
    function progressContainer(){ return document.querySelector('[data-repgroup="' + REPGROUP + '"] .progress'); }
    function segmentNodes(){ const c = progressContainer(); return c ? Array.prototype.slice.call(c.querySelectorAll('.progress-bar')) : []; }

    function sampleNow(){
      const f = window.__wrReconcile;
      const el = document.getElementById('status');
      const vm = window.ko && el ? window.ko.dataFor(el) : null;
      if (!vm) return;
      const idx = vm.repGroupLookup[REPGROUP];
      const rg = idx === undefined ? null : vm.repGroups[idx];
      const total = rg ? rg.total() : 0;
      const allTotal = vm.inflight ? vm.inflight.total() : 0;
      if (total > f.maxRgTotal) f.maxRgTotal = total;
      if (allTotal > f.maxAllTotal) f.maxAllTotal = allTotal;
      if (total > TOTAL) f.peakOvercount = Math.max(f.peakOvercount, total - TOTAL);

      const container = progressContainer();
      const segments = segmentNodes();
      let newlyStamped = 0;
      for (const seg of segments) {
        if (seg[stampMarker] === undefined) { f.stampSeq += 1; seg[stampMarker] = f.stampSeq; f.stampedNodes += 1; newlyStamped += 1; }
      }
      if (f.segmentsSeen && newlyStamped > 0) f.nodeRecreations += newlyStamped;
      if (segments.length > 0) f.segmentsSeen = true;

      let filled = null;
      if (container && segments.length > 0) {
        const w = container.getBoundingClientRect().width;
        if (w > 0) { let s = 0; for (const seg of segments) s += seg.getBoundingClientRect().width; filled = (s / w) * 100; }
      }
      if (filled !== null && filled >= 95) f.barFilledOnce = true;
      if (f.barFilledOnce && filled !== null) {
        f.populatedFrames += 1;
        if (f.minFilledAfterPopulated === null || filled < f.minFilledAfterPopulated) f.minFilledAfterPopulated = filled;
        if (filled < 95) f.framesBelow95 += 1;
        if (total > 0 && filled < 50) { f.collapseFrames += 1; if (f.worstCollapseFilled === null || filled < f.worstCollapseFilled) f.worstCollapseFilled = filled; }
      }
    }
    function frameLoop(){ sampleNow(); window.requestAnimationFrame(frameLoop); }
    window.requestAnimationFrame(frameLoop);

    class FixtureWebSocket {
      constructor(){ this.readyState = 0; setTimeout(() => { this.readyState = 1; window.__wrReconcile.phase = 'open'; if (this.onopen) this.onopen({}); }, 0); }
      raw(m){ if (this.onmessage) this.onmessage({ data: JSON.stringify(m) }); }
      emit(from, to, n){ this.raw({ RepGroup: '+all+', FromState: from, ToState: to, Count: n }); this.raw({ RepGroup: REPGROUP, FromState: from, ToState: to, Count: n }); }
      send(raw){ let r = {}; try { r = JSON.parse(raw); } catch { return; } if (r.Request === 'current') this.run(); }
      run(){
        window.__wrReconcile.phase = 'race';
        // 1) client already joined the caster: a wave of live deltas arrives
        //    BEFORE the seed, reordered, moving jobs ready->running->complete.
        const pre = [];
        let started = 0, complete = 0;
        for (let k = 0; k < 300; k++) { pre.push(['ready', 'running', 1]); started++; if (k % 2 === 0) { pre.push(['running', 'complete', 1]); complete++; } }
        for (let i = pre.length - 1; i > 0; i--) { const j = Math.floor(rand() * (i + 1)); const t = pre[i]; pre[i] = pre[j]; pre[j] = t; }
        for (const [f, t, n] of pre) this.emit(f, t, n);
        // 2) scan-on-connect seed reflects TRUE state now (overlaps the wave above).
        const ready = TOTAL - started, running = started - complete, comp = complete;
        this.raw({ RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: ready });
        this.raw({ RepGroup: '+all+', FromState: 'new', ToState: 'running', Count: running });
        this.raw({ RepGroup: REPGROUP, FromState: 'new', ToState: 'ready', Count: ready });
        this.raw({ RepGroup: REPGROUP, FromState: 'new', ToState: 'running', Count: running });
        this.raw({ RepGroup: REPGROUP, FromState: 'new', ToState: 'complete', Count: comp });
        // 3) drain to completion, emitting each step's deltas out of order.
        let curReady = ready, curRunning = running;
        const tick = () => {
          const batch = [];
          const s = Math.min(curReady, 40);
          if (s > 0) { batch.push(['ready', 'running', s]); curReady -= s; curRunning += s; }
          const c = Math.min(curRunning - (curReady > 0 ? 20 : 0), 40);
          if (c > 0) { batch.push(['running', 'complete', c]); curRunning -= c; }
          for (let i = batch.length - 1; i > 0; i--) { const j = Math.floor(rand() * (i + 1)); const t = batch[i]; batch[i] = batch[j]; batch[j] = t; }
          for (const [f, t, n] of batch) this.emit(f, t, n);
          if (curReady <= 0 && curRunning <= 0) { setTimeout(() => { window.__wrReconcile.phase = 'converged'; window.__wrReconcile.done = true; }, 400); return; }
          setTimeout(tick, 20);
        };
        setTimeout(tick, 20);
      }
      close(){ this.readyState = 3; if (this.onclose) this.onclose({}); }
    }
    window.WebSocket = FixtureWebSocket;
  })();`;
}

async function sampleRepGroup(page) {
  return page.evaluate(() => {
    const el = document.getElementById('status');
    const vm = window.ko && el ? window.ko.dataFor(el) : null;
    const rg = vm ? vm.repGroups[vm.repGroupLookup['echo']] : null;
    return {
      repGroupExists: Boolean(rg),
      repGroupTotal: rg ? rg.total() : null,
      repGroupComplete: rg ? rg.complete() : null,
      repGroupReady: rg ? rg.ready() : null,
      repGroupRunning: rg ? rg.running() : null,
      allTotal: vm ? vm.inflight.total() : null,
      completePct: rg ? rg.completePct() : null,
    };
  });
}

async function captureScreenshot() {
  const { chromium } = await loadPlaywright();
  const server = await createStaticServer();
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  for (const outputPath of [screenshotOutputPath, traceOutputPath]) {
    fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  }

  const browser = await chromium.launch({ headless: true });
  let page, capturedError;
  const trace = {
    scenario: 'connect-during-burst seed race + out-of-order drain: bar converges to the true totals and never collapses',
    source: { page: 'jobqueue/static/status.html', handler: 'jobqueue/static/js/wr/websocket-handler.js' },
    artifacts: { screenshot: screenshotOutputPath, trace: traceOutputPath },
  };

  try {
    page = await browser.newPage({ viewport: { width: 1280, height: 820 } });
    page.on('pageerror', error => console.error(`browser page error: ${error.message}`));
    await page.addInitScript(fakeWebSocketScript());
    await page.goto(`${baseURL}/status.html?token=count-reconcile-fixture`, { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    const totalJobs = await page.evaluate(() => window.__wrReconcile.totalJobs);
    await page.waitForFunction(() => window.__wrReconcile.maxRgTotal > 0, undefined, { timeout: 15000 });
    await page.waitForFunction(() => window.__wrReconcile.done, undefined, { timeout: 40000 });
    await page.waitForTimeout(1200);

    const finalSample = await sampleRepGroup(page);
    const fixtureState = await page.evaluate(() => {
      const f = window.__wrReconcile;
      f.finalRgTotal = f.finalRgTotal; // keep shape
      return {
        maxRgTotal: f.maxRgTotal, maxAllTotal: f.maxAllTotal, peakOvercount: f.peakOvercount,
        populatedFrames: f.populatedFrames, minFilledAfterPopulated: f.minFilledAfterPopulated,
        collapseFrames: f.collapseFrames, worstCollapseFilled: f.worstCollapseFilled,
        framesBelow95: f.framesBelow95, nodeRecreations: f.nodeRecreations,
      };
    });
    trace.totalJobs = totalJobs;
    trace.finalSample = finalSample;
    trace.fixtureState = fixtureState;

    await page.screenshot({ path: screenshotOutputPath, fullPage: true });

    // (1) CONVERGENCE (the deterministic discriminator). Pre-fix, the seed race
    // leaves the bar permanently overcounted (more total/complete than exist).
    if (!finalSample.repGroupExists || finalSample.repGroupTotal !== totalJobs || finalSample.repGroupComplete !== totalJobs) {
      throw new Error(`bar did not converge: expected ${totalJobs} total / ${totalJobs} complete, saw ${JSON.stringify(finalSample)} (pre-fix seed-race divergence)`);
    }
    if (finalSample.allTotal !== 0) {
      throw new Error(`"+all+" live aggregate did not drain to 0, saw ${finalSample.allTotal}`);
    }

    // (2) NO COLLAPSE while jobs exist (smooth stacked bar, same guard as
    // repgroup-bar-flicker).
    if (fixtureState.collapseFrames > 0) {
      throw new Error(`bar collapsed: ${fixtureState.collapseFrames}/${fixtureState.populatedFrames} frames rendered < 50% filled (worst ${fixtureState.worstCollapseFilled}%)`);
    }
    if (fixtureState.minFilledAfterPopulated === null || fixtureState.minFilledAfterPopulated < 90) {
      throw new Error(`bar not kept ~full: minimum rendered filled width was ${fixtureState.minFilledAfterPopulated}% (expected >= 90%)`);
    }
    if (fixtureState.nodeRecreations > 0) {
      throw new Error(`bar segments recreated: ${fixtureState.nodeRecreations} new segment nodes appeared after population (expected 0)`);
    }
  } catch (error) {
    capturedError = error;
    trace.failure = { message: error.message, stack: error.stack };
    if (page) {
      try { trace.failureSample = await sampleRepGroup(page); } catch { /* best effort */ }
      try { await page.screenshot({ path: screenshotOutputPath, fullPage: true }); } catch { /* best effort */ }
    }
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  fs.writeFileSync(traceOutputPath, `${JSON.stringify(trace, null, 2)}\n`);
  if (capturedError) throw capturedError;
  console.log(screenshotOutputPath);
  console.log(traceOutputPath);
}

await captureScreenshot();
