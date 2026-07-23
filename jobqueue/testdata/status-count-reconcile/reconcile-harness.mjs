#!/usr/bin/env node

// Deterministic, full-strength reproducer for the web status-bar
// flicker / transient-overcount family (.docs/flicker/). It drives the REAL
// client delta-application logic (jobqueue/static/js/wr/websocket-handler.js)
// with adversarial `jstateCount` delta streams and asserts the reconstructed
// per-RepGroup and "+all+" counts stay coherent and converge exactly.
//
// It needs no browser and no rate-limit timing: the trackers are stubbed with
// plain observables, so it reads the reconstruction the INSTANT each delta is
// applied - which is exactly where the bug lives (a real browser's 350ms
// Knockout rate-limit merely coalesces and hides these transients, which is why
// the artefact is "briefly twitchy" rather than a stable wrong number). This
// harness therefore catches deterministically what a screenshot fixture can
// only catch flakily.
//
// Three problem classes are exercised, each the mechanism documented in
// .docs/flicker/issue.md and solution.md:
//
//   A. connected-client burst, OUT OF ORDER. The server emits every transition
//      from its own goroutine (queue.changed -> `go changedCb`), so a job's
//      running->complete delta can arrive before its ready->running delta.
//      Coherent code must never transiently overcount or dip.
//
//   B. connect DURING a burst (the scan-on-connect SEED RACE). Live deltas that
//      arrive around the connect handshake overlap the snapshot seed. The
//      pre-fix client does not just twitch here - it PERMANENTLY diverges
//      (ends with more jobs than exist). Coherent code converges exactly.
//
//   C. mixed workload with rerun CYCLES (complete->ready->running->complete),
//      bury, delete and lost, out of order. Guards order-independence and that
//      a rerun is reconciled, not double-counted, and never loses a job.
//
// Exit code 0 => all scenarios coherent and convergent (a correct handler).
// Exit code 1 => at least one scenario overcounts, dips or diverges (the bug).
//
// Usage:
//   node reconcile-harness.mjs [handlerFile] [--verbose]
//     handlerFile  path to the websocket-handler.js under test
//                  (default: ../../static/js/wr/websocket-handler.js)
//
// It is deterministic (seeded PRNG), so it is safe as a CI regression gate; it
// is exercised by TestStatusCountReconcile (go test) and
// `developers/wrdev.sh flicker-check`.

import fs from 'node:fs';
import vm from 'node:vm';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2).filter(a => a !== '--verbose');
const VERBOSE = process.argv.includes('--verbose');
const handlerFile = args[0]
  ? path.resolve(process.cwd(), args[0])
  : path.resolve(scriptDir, '../../static/js/wr/websocket-handler.js');

// ---------------------------------------------------------------------------
// Load the real handler, exposing its internal handleStateChangeMessage.
// ---------------------------------------------------------------------------
let source = fs.readFileSync(handlerFile, 'utf8');
source = source
  .replace(/^import .*;\n/gm, '')
  .replace(/^export \{[^}]*\};?\n/gm, '')
  .replace(/export function /g, 'function ');

const COUNT_PROPS = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried', 'deleted', 'complete'];
// "+all+" is the live aggregate: no complete/deleted observable (terminal jobs
// leave the live bar), exactly as jobqueue/static/js/wr/inflight-tracking.js.
const INFLIGHT_PROPS = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried'];

function makeTracker(id, props) {
  const t = { id, old_total: 0 };
  for (const p of props) {
    t[p] = (function () { let v = 0; return function (n) { return n === undefined ? v : (v = n); }; })();
  }
  return t;
}

const context = {
  console,
  createRepGroupTracker(rg) { return makeTracker(rg, COUNT_PROPS); },
  removeBadServer() {},
  setupLiveWalltime() {},
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(source + '\nglobalThis.handleStateChangeMessage = handleStateChangeMessage;', context,
  { filename: 'websocket-handler.js' });
const applyDelta = context.handleStateChangeMessage;
if (typeof applyDelta !== 'function') {
  console.error('reconcile-harness: handler did not expose handleStateChangeMessage');
  process.exit(2);
}

// ---------------------------------------------------------------------------
// Deterministic PRNG (mulberry32) so the gate never flakes.
// ---------------------------------------------------------------------------
function rng(seed) {
  let a = seed >>> 0;
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function newVM() {
  return { inflight: makeTracker('+all+', INFLIGHT_PROPS), repGroups: [], repGroupLookup: {}, sortableRepGroups: { push() {} } };
}
function total(t, props) { let s = 0; for (const p of props) s += t[p](); return s; }
function reorder(list, window, rand) {
  return list.map((item, i) => ({ item, key: i + rand() * window })).sort((a, b) => a.key - b.key).map(k => k.item);
}

const RG = 'echo';
const RECORDS = [];
function record(name, ok, detail) {
  RECORDS.push({ name, ok, detail });
  if (VERBOSE || !ok) console.error(`  [${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`);
}

// ---------------------------------------------------------------------------
// Scenario A: connected-client burst, out of order.
// Per-RepGroup total must equal N at every step after the (single-state) seed;
// "+all+" must never exceed N nor go negative; converges to N complete.
// ---------------------------------------------------------------------------
function scenarioA(seed, N, window) {
  const rand = rng(seed);
  const events = [];
  for (let j = 0; j < N; j++) {
    const start = rand();
    const dur = rand() * 0.02;
    events.push({ t: start, k: 'r2run' });
    events.push({ t: start + dur + 1e-6, k: 'run2c' });
  }
  events.sort((a, b) => a.t - b.t);
  let live = [];
  for (const e of events) {
    const [f, t] = e.k === 'r2run' ? ['ready', 'running'] : ['running', 'complete'];
    live.push({ RepGroup: '+all+', FromState: f, ToState: t, Count: 1 });
    live.push({ RepGroup: RG, FromState: f, ToState: t, Count: 1 });
  }
  live = reorder(live, window, rand);

  const vmv = newVM();
  applyDelta(vmv, { RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: N });
  applyDelta(vmv, { RepGroup: RG, FromState: 'new', ToState: 'ready', Count: N });

  let worstRgErr = 0, worstAllOver = 0, worstAllNeg = 0;
  const rgT = () => vmv.repGroups[vmv.repGroupLookup[RG]];
  for (const d of live) {
    applyDelta(vmv, d);
    const rt = rgT();
    if (rt) worstRgErr = Math.max(worstRgErr, Math.abs(total(rt, COUNT_PROPS) - N));
    const at = total(vmv.inflight, INFLIGHT_PROPS);
    if (at > N) worstAllOver = Math.max(worstAllOver, at - N);
    if (at < 0) worstAllNeg = Math.max(worstAllNeg, -at);
  }
  const rt = rgT();
  const converged = rt && total(rt, COUNT_PROPS) === N && rt.complete() === N && total(vmv.inflight, INFLIGHT_PROPS) === 0;
  return { worstRgErr, worstAllOver, worstAllNeg, converged, rgFinal: rt ? total(rt, COUNT_PROPS) : 0, rgComplete: rt ? rt.complete() : 0 };
}

// ---------------------------------------------------------------------------
// Scenario B: connect DURING a burst (seed race). Faithful model: mutation
// order, per-delta emission jitter, seed reflecting true state at connect +
// handshake, only deltas emitted at/after connect delivered live.
// ---------------------------------------------------------------------------
function scenarioB(seed, N, jitter, handshake, connectFrac) {
  const rand = rng(seed);
  const muts = [{ from: 'new', to: 'ready', count: N, mtime: 0 }];
  const evs = [];
  for (let j = 0; j < N; j++) {
    const s = 1 + rand() * (N - 2);
    evs.push({ from: 'ready', to: 'running', mtime: s });
    evs.push({ from: 'running', to: 'complete', mtime: s + rand() * (N * 0.01) + 1e-4 });
  }
  evs.sort((a, b) => a.mtime - b.mtime);
  let idx = 1;
  for (const e of evs) { e.mtime = idx++; e.count = 1; muts.push(e); }
  const maxM = idx;
  const connectM = Math.floor(maxM * connectFrac);
  const snapM = connectM + handshake;

  const trueAt = (t) => {
    const st = { ready: 0, running: 0, complete: 0 };
    for (const m of muts) {
      if (m.mtime > t) continue;
      if (m.from !== 'new') st[m.from] -= m.count;
      st[m.to] += m.count;
    }
    return st;
  };

  const liveMuts = muts.map(m => ({ ...m, etime: m.mtime + rand() * jitter })).filter(m => m.etime >= connectM);
  liveMuts.sort((a, b) => a.etime - b.etime);
  const stream = [];
  for (const m of liveMuts) {
    stream.push({ etime: m.etime, RepGroup: '+all+', FromState: m.from, ToState: m.to, Count: m.count });
    stream.push({ etime: m.etime, RepGroup: RG, FromState: m.from, ToState: m.to, Count: m.count });
  }
  const snap = trueAt(snapM);
  for (const s of ['ready', 'running']) if (snap[s] > 0) stream.push({ etime: snapM, seed: true, RepGroup: '+all+', FromState: 'new', ToState: s, Count: snap[s] });
  for (const s of ['ready', 'running', 'complete']) if (snap[s] > 0) stream.push({ etime: snapM, seed: true, RepGroup: RG, FromState: 'new', ToState: s, Count: snap[s] });
  stream.sort((a, b) => (a.etime - b.etime) || ((a.seed ? 1 : 0) - (b.seed ? 1 : 0)));

  const vmv = newVM();
  const rgT = () => vmv.repGroups[vmv.repGroupLookup[RG]];
  let seeded = false, worstOver = 0;
  for (const d of stream) {
    if (d.seed) seeded = true;
    applyDelta(vmv, { RepGroup: d.RepGroup, FromState: d.FromState, ToState: d.ToState, Count: d.Count });
    if (!seeded) continue; // pre-seed the client is legitimately still loading
    const rt = rgT();
    if (rt && total(rt, COUNT_PROPS) > N) worstOver = Math.max(worstOver, total(rt, COUNT_PROPS) - N);
  }
  const rt = rgT();
  const rgFinal = rt ? total(rt, COUNT_PROPS) : 0;
  const rgComplete = rt ? rt.complete() : 0;
  return { converged: rgFinal === N && rgComplete === N, rgFinal, rgComplete, worstOver };
}

// ---------------------------------------------------------------------------
// Scenario C: mixed workload with rerun cycles, out of order.
// ---------------------------------------------------------------------------
function scenarioC(seed, N, window) {
  const rand = rng(seed);
  const pathFor = () => {
    const r = rand();
    if (r < 0.65) return [['ready', 'running'], ['running', 'complete']];
    if (r < 0.75) return [['ready', 'running'], ['running', 'buried']];
    if (r < 0.85) return [['ready', 'deleted']];
    if (r < 0.92) return [['ready', 'running'], ['running', 'lost']];
    return [['ready', 'running'], ['running', 'complete'], ['complete', 'ready'], ['ready', 'running'], ['running', 'complete']];
  };
  const evs = [];
  const finalRg = {}; for (const p of COUNT_PROPS) finalRg[p] = 0;
  for (let j = 0; j < N; j++) {
    const p = pathFor();
    let t = 1 + rand() * N;
    for (const [f, to] of p) { evs.push({ f, to, t }); t += rand() * 3; }
    finalRg[p[p.length - 1][1]] += 1;
  }
  evs.sort((a, b) => a.t - b.t);
  let live = [];
  for (const e of evs) {
    live.push({ RepGroup: '+all+', FromState: e.f, ToState: e.to, Count: 1 });
    live.push({ RepGroup: RG, FromState: e.f, ToState: e.to, Count: 1 });
  }
  live = reorder(live, window, rand);
  const vmv = newVM();
  applyDelta(vmv, { RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: N });
  applyDelta(vmv, { RepGroup: RG, FromState: 'new', ToState: 'ready', Count: N });
  const rgT = () => vmv.repGroups[vmv.repGroupLookup[RG]];
  let worstRgErr = 0, worstAllOver = 0;
  for (const d of live) {
    applyDelta(vmv, d);
    const rt = rgT();
    if (rt) worstRgErr = Math.max(worstRgErr, Math.abs(total(rt, COUNT_PROPS) - N));
    const at = total(vmv.inflight, INFLIGHT_PROPS);
    if (at > N) worstAllOver = Math.max(worstAllOver, at - N);
  }
  const rt = rgT();
  let finalOk = true; const diff = {};
  for (const p of COUNT_PROPS) { const got = rt ? rt[p]() : 0; if (got !== finalRg[p]) { finalOk = false; diff[p] = { got, want: finalRg[p] }; } }
  return { worstRgErr, worstAllOver, finalOk, diff };
}

// ---------------------------------------------------------------------------
// Run each scenario across many deterministic seeds.
// ---------------------------------------------------------------------------
console.error(`reconcile-harness: handler=${path.relative(process.cwd(), handlerFile)}`);

// A
{
  let wErr = 0, wOver = 0, wNeg = 0, convFail = 0;
  for (let s = 1; s <= 120; s++) {
    const r = scenarioA(s * 2654435761, 2000, 20);
    wErr = Math.max(wErr, r.worstRgErr); wOver = Math.max(wOver, r.worstAllOver); wNeg = Math.max(wNeg, r.worstAllNeg);
    if (!r.converged) convFail++;
  }
  record('A/connected-burst-out-of-order',
    wErr === 0 && wOver === 0 && wNeg === 0 && convFail === 0,
    `worstRepGroupTotalError=${wErr} worstAllOvercount=${wOver} worstAllNegative=${wNeg} convergeFailures=${convFail}/120`);
}

// B
{
  let convFail = 0, wOver = 0, exFinal = 0, exComplete = 0;
  for (let s = 1; s <= 120; s++) {
    const r = scenarioB(s * 40503, 1500, 18, 8, 0.3 + (s % 5) * 0.1);
    wOver = Math.max(wOver, r.worstOver);
    if (!r.converged) { convFail++; if (convFail === 1) { exFinal = r.rgFinal; exComplete = r.rgComplete; } }
  }
  record('B/connect-mid-burst-seed-race',
    convFail === 0 && wOver === 0,
    `convergeFailures=${convFail}/120 worstOvercount=${wOver}` + (convFail ? ` egFinalTotal=${exFinal} egComplete=${exComplete} (want 1500/1500)` : ''));
}

// C
{
  let wErr = 0, wOver = 0, finalFail = 0, egDiff = null;
  for (let s = 1; s <= 120; s++) {
    const r = scenarioC(s * 2246822519, 1200, 25);
    wErr = Math.max(wErr, r.worstRgErr); wOver = Math.max(wOver, r.worstAllOver);
    if (!r.finalOk) { finalFail++; if (!egDiff) egDiff = r.diff; }
  }
  record('C/mixed-workload-rerun-cycles',
    wErr === 0 && wOver === 0 && finalFail === 0,
    `worstRepGroupTotalError=${wErr} worstAllOvercount=${wOver} finalMismatches=${finalFail}/120` + (egDiff ? ` eg=${JSON.stringify(egDiff)}` : ''));
}

const failed = RECORDS.filter(r => !r.ok);
if (failed.length === 0) {
  console.error('reconcile-harness: ALL SCENARIOS COHERENT AND CONVERGENT');
  process.exit(0);
}
console.error(`reconcile-harness: ${failed.length}/${RECORDS.length} scenario(s) FAILED (flicker/overcount/divergence present)`);
process.exit(1);
