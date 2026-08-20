#!/usr/bin/env node

// replay-stream.mjs replays a RECORDED /status_ws message stream through the
// REAL browser client logic (jobqueue/static/js/wr/websocket-handler.js) and
// prints the per-tracker bucket counts the status bars would show.
//
// It exists because the only faithful way to say "the web UI would have shown
// N running" is to run the shipped client's own reconstruction over the exact
// bytes the server sent. Hand-rolled reconstructions (e.g. the naive
// increment/decrement in .docs/reliable2/phase2/wsprobe) no longer match the
// shipped client, which uses an order-independent occupancy model.
//
// Input: a JSON file holding either an array of /status_ws messages, or an
// object with a "messages" array. Each message is exactly as it arrived on the
// wire: a {RepGroup, FromState, ToState, Count} delta, or a {SeedBoundary}
// marker bracketing the scan-on-connect seed. The seed's own messages are
// ordinary members of the array (FromState "new"), in the order received.
// Messages are applied strictly in file order, so the recording order is what is
// judged, and they go through the handler's own message router
// (applyStatusMessage) so the routing is judged too. A handler that predates the
// router is driven through handleStateChangeMessage instead, which is how the
// pre-fix/post-fix A/B is run against the same recording.
//
// Output: two lines
//   SEEDBRACKET begin=<n> end=<n> interleaved=<n>
//   RECONSTRUCTED {"+all+":{...},"<repgroup>":{...}}
// the first counting the seed boundaries seen and any delta that arrived between
// a begin and its end (which the server's write mutex must make impossible), the
// second mapping each tracker to its displayed bucket counts (zero buckets
// omitted). Exit status is 0 unless the input could not be read/parsed.
//
// Usage:
//   node replay-stream.mjs <stream.json> [handlerFile] [--ignore-boundaries]
//     --ignore-boundaries strips the seed boundary markers from the recording
//     before replaying it, which is exactly what a status page that predates
//     them sees. Replaying one recording both ways measures what the boundary
//     bought, within a single run and on identical bytes.
//
// Used by the Finding 7 (web count divergence) reproducer:
// jobqueue/reliable4_seedoverlap_test.go (build tag reliability_repro) and
// `developers/wrdev.sh status-seed-overlap`. Also the replay half of the live
// fallback: record a real prod stream, replay it here.

import fs from 'node:fs';
import vm from 'node:vm';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const IGNORE_BOUNDARIES = argv.includes('--ignore-boundaries');
const positional = argv.filter(a => !a.startsWith('--'));
const streamFile = positional[0];

if (!streamFile) {
  console.error('replay-stream: usage: node replay-stream.mjs <stream.json> [handlerFile] [--ignore-boundaries]');
  process.exit(2);
}

const handlerFile = positional[1]
  ? path.resolve(process.cwd(), positional[1])
  : path.resolve(scriptDir, '../../static/js/wr/websocket-handler.js');

let source = fs.readFileSync(handlerFile, 'utf8');
source = source
  .replace(/^import .*;\n/gm, '')
  .replace(/^export \{[^}]*\};?\n/gm, '')
  .replace(/export function /g, 'function ');

const COUNT_PROPS = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried', 'deleted', 'complete'];
// "+all+" is the live aggregate: it has no complete/deleted observable, exactly
// as jobqueue/static/js/wr/inflight-tracking.js.
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
  console.error('replay-stream: handler did not expose handleStateChangeMessage');
  process.exit(2);
}

// A handler that knows about seed boundaries routes every message itself. An
// older one has no router to call, so it is driven the way its own onmessage
// did: only messages carrying FromState reach the delta path, which is exactly
// how an old status page behaves against a new server - it never sees the
// markers. That makes the pre-fix/post-fix A/B a fair one on the same recording.
const applyMessage = context.applyStatusMessage
  ? context.applyStatusMessage
  : (vmodel, m) => {
    if (Object.prototype.hasOwnProperty.call(m, 'FromState')) {
      applyDelta(vmodel, m);
    }
  };

const raw = JSON.parse(fs.readFileSync(streamFile, 'utf8'));
const messages = Array.isArray(raw) ? raw : raw.messages;
if (!Array.isArray(messages)) {
  console.error('replay-stream: input holds no message array');
  process.exit(2);
}

const viewModel = {
  inflight: makeTracker('+all+', INFLIGHT_PROPS),
  repGroups: [],
  repGroupLookup: {},
  sortableRepGroups: { push() {} },
};

let begins = 0;
let ends = 0;
let interleaved = 0;
let inSeed = false;

for (const m of messages) {
  if (!m) {
    continue;
  }

  if (typeof m.SeedBoundary === 'string' && m.SeedBoundary !== '') {
    if (IGNORE_BOUNDARIES) {
      continue;
    }

    if (m.SeedBoundary === 'begin') {
      begins++;
      inSeed = true;
    } else if (m.SeedBoundary === 'end') {
      ends++;
      inSeed = false;
    }

    applyMessage(viewModel, m);

    continue;
  }

  if (typeof m.RepGroup !== 'string' || m.RepGroup === '') {
    continue;
  }

  if (inSeed && m.FromState !== 'new') {
    interleaved++;
  }

  applyMessage(viewModel, m);
}

function shown(tracker, props) {
  const out = {};
  for (const p of props) {
    const v = tracker[p]();
    if (v !== 0) {
      out[p] = v;
    }
  }

  return out;
}

const result = { '+all+': shown(viewModel.inflight, INFLIGHT_PROPS) };
for (const rg of viewModel.repGroups) {
  result[rg.id] = shown(rg, COUNT_PROPS);
}

console.log(`SEEDBRACKET begin=${begins} end=${ends} interleaved=${interleaved}`);
console.log('RECONSTRUCTED ' + JSON.stringify(result));
