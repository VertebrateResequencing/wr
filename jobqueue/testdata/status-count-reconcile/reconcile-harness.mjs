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
//      arrive around the connect handshake overlap the snapshot seed, so the
//      same transition is reported twice. Scored PER BUCKET against what a
//      client that can tell a duplicate from new information would show: the
//      total is conserved by the occupancy model whatever happens, so only the
//      per-bucket distribution can see reliable4 FINDING 7 (a job stuck in
//      `running` that has long since left it). B2 is the same race with emission
//      jitter, which measures the residual no seed boundary can remove.
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
vm.runInContext(source +
  '\nglobalThis.handleStateChangeMessage = handleStateChangeMessage;' +
  '\nglobalThis.applyStatusMessage = typeof applyStatusMessage === "function" ? applyStatusMessage : null;',
  context, { filename: 'websocket-handler.js' });
const applyDelta = context.handleStateChangeMessage;
if (typeof applyDelta !== 'function') {
  console.error('reconcile-harness: handler did not expose handleStateChangeMessage');
  process.exit(2);
}

// applyMessage drives the handler's own message router when it has one, so the
// seed boundary markers the server brackets its scan-on-connect seed with are
// routed the way the browser routes them. A handler that predates the router
// only ever saw messages carrying FromState, so that is all it is given - which
// is exactly what an older status page sees against a newer server, and what
// makes the pre-fix/post-fix A/B fair on an identical stream.
const applyMessage = context.applyStatusMessage
  ? context.applyStatusMessage
  : (vmodel, m) => {
    if (Object.prototype.hasOwnProperty.call(m, 'FromState')) {
      applyDelta(vmodel, m);
    }
  };

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
// handshake, only deltas emitted at/after connect delivered live, and the seed
// bracketed by the boundary markers the server writes under its per-connection
// write mutex (so the bracket is one uninterrupted run of messages).
//
// It is scored PER BUCKET, not on the per-RepGroup total. The total is conserved
// by the occupancy model by construction (total occupancy == the sum of the
// creations it was told about), so a seed race that moves one job from `ready`
// to `running` and leaves it there - reliable4 FINDING 7, the status page
// showing 274 running with 4 running - is invisible to a total-only score. The
// reference is what a client that could tell duplicates from new information
// would show: the seed, plus every DELIVERED mutation that the seed does not
// already account for (mtime > snapM). `leftover` jobs never complete, so the
// final distribution spans more than one bucket and a misdistribution cannot
// hide in a single terminal one.
//
// The same stream is replayed twice: once with the boundary markers routed to
// the handler, and once without them, which is exactly what a status page that
// predates them sees. The difference is what the boundary bought.
// ---------------------------------------------------------------------------
function scenarioB(seed, N, jitter, handshake, connectFrac, leftover) {
  const rand = rng(seed);
  const muts = [{ from: 'new', to: 'ready', count: N, mtime: 0 }];
  const evs = [];
  for (let j = 0; j < N; j++) {
    const s = 1 + rand() * (N - 2);
    evs.push({ from: 'ready', to: 'running', mtime: s });
    if (j >= leftover) {
      evs.push({ from: 'running', to: 'complete', mtime: s + rand() * (N * 0.01) + 1e-4 });
    }
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
  const deltas = [];
  for (const m of liveMuts) {
    for (const rg of ['+all+', RG]) {
      deltas.push({ etime: m.etime, mtime: m.mtime, RepGroup: rg, FromState: m.from, ToState: m.to, Count: m.count });
    }
  }

  const snap = trueAt(snapM);
  const seedBlock = [];
  for (const st of ['ready', 'running']) {
    if (snap[st] > 0) seedBlock.push({ seed: true, RepGroup: '+all+', FromState: 'new', ToState: st, Count: snap[st] });
  }
  for (const st of ['ready', 'running', 'complete']) {
    if (snap[st] > 0) seedBlock.push({ seed: true, RepGroup: RG, FromState: 'new', ToState: st, Count: snap[st] });
  }

  // the bracket goes in as one block at the snapshot: the server holds the
  // connection's write mutex from before the snapshot until after the last seed
  // message, so no live delta can land inside it.
  const stream = [
    ...deltas.filter(d => d.etime <= snapM),
    { boundary: 'begin' },
    ...seedBlock,
    { boundary: 'end' },
    ...deltas.filter(d => d.etime > snapM),
  ];

  // the duplicates the boundary CANNOT remove: a mutation the snapshot already
  // saw whose delta was emitted late enough to arrive after the bracket (a
  // changedCb goroutine descheduled past the snapshot). Each can misplace at
  // most one job.
  const dupAfterSeed = deltas.filter(d => d.RepGroup === RG && d.mtime <= snapM && d.etime > snapM).length;

  const aware = newVM();
  const blind = newVM();
  const ideal = Object.create(null);
  const rgOf = (vmv) => vmv.repGroups[vmv.repGroupLookup[RG]];
  const errOf = (vmv) => {
    const t = rgOf(vmv);
    if (!t) return 0;
    let worst = 0;
    for (const p of COUNT_PROPS) worst = Math.max(worst, Math.abs(t[p]() - (ideal[p] || 0)));
    return worst;
  };

  let scoring = false, worstAware = 0, worstBlind = 0;
  for (const d of stream) {
    if (d.boundary) {
      applyMessage(aware, { SeedBoundary: d.boundary });
      if (d.boundary === 'end') scoring = true;
      continue;
    }

    const msg = { RepGroup: d.RepGroup, FromState: d.FromState, ToState: d.ToState, Count: d.Count };
    applyMessage(aware, msg);
    applyMessage(blind, msg);

    if (d.RepGroup === RG && (d.seed || d.mtime > snapM)) {
      if (d.FromState !== 'new') ideal[d.FromState] = (ideal[d.FromState] || 0) - d.Count;
      ideal[d.ToState] = (ideal[d.ToState] || 0) + d.Count;
    }

    if (scoring) {
      worstAware = Math.max(worstAware, errOf(aware));
      worstBlind = Math.max(worstBlind, errOf(blind));
    }
  }

  const rt = rgOf(aware);
  const rgFinal = rt ? total(rt, COUNT_PROPS) : 0;
  const rgComplete = rt ? rt.complete() : 0;
  const rgRunning = rt ? rt.running() : 0;

  return {
    worstAware, worstBlind, dupAfterSeed,
    finalAware: errOf(aware), finalBlind: errOf(blind),
    converged: rgFinal === N && rgComplete === N - leftover && rgRunning === leftover,
    rgFinal, rgComplete, rgRunning,
  };
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

// B1: the seed race with emission in causal order, so everything delivered
// before the bracket really does predate the snapshot. A client that resets to
// the seed on the boundary is then EXACT per bucket; one that cannot see the
// boundary double-counts the whole handshake window and stays wrong.
{
  let awareFail = 0, wAware = 0, wBlind = 0, convFail = 0, exFinal = 0, exComplete = 0;
  for (let s = 1; s <= 120; s++) {
    const r = scenarioB(s * 40503, 1500, 0, 8, 0.3 + (s % 5) * 0.1, 7);
    wAware = Math.max(wAware, r.worstAware); wBlind = Math.max(wBlind, r.worstBlind);
    if (r.worstAware !== 0) awareFail++;
    if (!r.converged) { convFail++; if (convFail === 1) { exFinal = r.rgFinal; exComplete = r.rgComplete; } }
  }
  record('B/connect-mid-burst-seed-race',
    awareFail === 0 && wAware === 0 && convFail === 0,
    `worstPerBucketError=${wAware} (boundary-blind client on the same stream: ${wBlind})`
    + ` inexactTrials=${awareFail}/120 convergeFailures=${convFail}/120`
    + (convFail ? ` egFinalTotal=${exFinal} egComplete=${exComplete} (want 1500/1493)` : ''));
}

// B2: the same race with emission JITTER, i.e. a changedCb goroutine descheduled
// past the snapshot. Those duplicates arrive after the bracket, so no boundary
// can remove them - this measures that accepted residual, and asserts the
// boundary never makes it worse and never exceeds the duplicates the model
// actually delivered late.
{
  let wAware = 0, wBlind = 0, worse = 0, overBound = 0, dupMax = 0;
  for (let s = 1; s <= 120; s++) {
    const r = scenarioB(s * 40503, 1500, 18, 8, 0.3 + (s % 5) * 0.1, 7);
    wAware = Math.max(wAware, r.worstAware); wBlind = Math.max(wBlind, r.worstBlind);
    dupMax = Math.max(dupMax, r.dupAfterSeed);
    if (r.worstAware > r.worstBlind) worse++;
    if (r.finalAware > r.dupAfterSeed) overBound++;
  }
  record('B2/seed-race-residual-under-emission-jitter',
    worse === 0 && overBound === 0,
    `worstPerBucketError=${wAware} (boundary-blind: ${wBlind}) lateDuplicates<=${dupMax}`
    + ` madeWorse=${worse}/120 overResidualBound=${overBound}/120`);
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
