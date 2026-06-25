#!/usr/bin/env node

import fs from 'node:fs';
import assert from 'node:assert/strict';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const defaultOutput = path.join(repoRoot, '.tmp/agent/status-stale-counts.html');
const outputPath = path.resolve(process.cwd(), process.argv[2] || defaultOutput);

if (outputPath !== repoRoot && !outputPath.startsWith(repoRoot + path.sep)) {
  throw new Error(`refusing to write outside repo: ${outputPath}`);
}

function observable(initial) {
  let value = initial;

  return function observe(next) {
    if (arguments.length > 0) {
      value = next;

      return observe;
    }

    return value;
  };
}

const countProps = [
  'delayed',
  'dependent',
  'suspended',
  'ready',
  'running',
  'lost',
  'buried',
  'deleted',
  'complete'
];

function createRepGroupTracker(id) {
  const tracker = { id, old_total: 0 };

  for (const prop of countProps) {
    tracker[prop] = observable(0);
  }

  return tracker;
}

function createViewModel() {
  return {
    rateLimit: 0,
    ignore: {},
    inflight: createRepGroupTracker('+all+'),
    repGroups: [],
    repGroupLookup: {},
    sortableRepGroups: []
  };
}

function loadStatusHandler() {
  const handlerPath = path.join(repoRoot, 'jobqueue/static/js/wr/websocket-handler.js');
  let source = fs.readFileSync(handlerPath, 'utf8');
  source = source
    .replace(/^import .*;\n/gm, '')
    .replace(/export function /g, 'function ');

  const context = {
    console,
    createRepGroupTracker,
    setupLiveWalltime() {}
  };
  context.globalThis = context;

  vm.createContext(context);
  vm.runInContext(
    `${source}\nglobalThis.handleStateChangeMessage = handleStateChangeMessage;`,
    context,
    { filename: 'websocket-handler.js' }
  );

  return context.handleStateChangeMessage;
}

const handleStateChangeMessage = loadStatusHandler();

function deliver(viewModel, event) {
  handleStateChangeMessage(viewModel, event);
}

function deliverEmptyAuthoritativeSnapshot(viewModel, snapshotID) {
  deliver(viewModel, {
    RepGroup: '+all+',
    FromState: 'new',
    ToState: '',
    Count: 0,
    SnapshotID: snapshotID
  });
  deliver(viewModel, {
    RepGroup: '+all+',
    SnapshotID: snapshotID,
    SnapshotDone: true
  });
}

function snapshotTracker(tracker) {
  const snapshot = {};

  for (const prop of countProps) {
    snapshot[prop] = tracker[prop]();
  }

  snapshot.total = countProps.reduce((sum, prop) => sum + snapshot[prop], 0);

  return snapshot;
}

function snapshotGroup(viewModel, id) {
  const index = viewModel.repGroupLookup[id];

  if (index === undefined) {
    return Object.fromEntries(countProps.concat('total').map(prop => [prop, 0]));
  }

  return snapshotTracker(viewModel.repGroups[index]);
}

function runTabletestStaleRunningFixture() {
  const viewModel = createViewModel();
  const events = [
    { RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: 2 },
    { RepGroup: 'tabletest', FromState: 'new', ToState: 'ready', Count: 2 },
    { RepGroup: '+all+', FromState: 'ready', ToState: 'running', Count: 2 },
    { RepGroup: 'tabletest', FromState: 'ready', ToState: 'running', Count: 2 },

    // Reproduces the lossy websocket path: one running->complete delta is
    // visible, while the other terminal delta has been dropped before the
    // browser can apply it.
    { RepGroup: '+all+', FromState: 'running', ToState: 'complete', Count: 1 },
    { RepGroup: 'tabletest', FromState: 'running', ToState: 'complete', Count: 1 }
  ];

  for (const event of events) {
    deliver(viewModel, event);
  }

  return {
    name: 'tabletest stale running',
    events,
    live: {
      incomplete: snapshotTracker(viewModel.inflight),
      group: snapshotGroup(viewModel, 'tabletest')
    },
    truth: {
      note: 'Fresh page + search for tabletest reads archived jobs, not the dropped delta stream.',
      incomplete: { total: 0, running: 0 },
      group: { total: 2, running: 0, complete: 2 }
    }
  };
}

function runTabletestResyncedFixture() {
  const result = runTabletestStaleRunningFixture();
  const viewModel = createViewModel();

  for (const event of result.events) {
    deliver(viewModel, event);
  }

  deliverEmptyAuthoritativeSnapshot(viewModel, 1);

  return {
    name: 'tabletest after authoritative current snapshot',
    events: result.events,
    live: {
      incomplete: snapshotTracker(viewModel.inflight),
      group: snapshotGroup(viewModel, 'tabletest')
    },
    truth: result.truth
  };
}

function runBulkRemoveStaleFixture() {
  const viewModel = createViewModel();
  const events = [
    { RepGroup: '+all+', FromState: 'new', ToState: 'ready', Count: 4 },
    { RepGroup: 'deletebulk', FromState: 'new', ToState: 'ready', Count: 4 },

    // Only half of a four-job remove burst reaches the browser.
    { RepGroup: '+all+', FromState: 'ready', ToState: 'deleted', Count: 2 },
    { RepGroup: 'deletebulk', FromState: 'ready', ToState: 'deleted', Count: 2 }
  ];

  for (const event of events) {
    deliver(viewModel, event);
  }

  return {
    name: 'deletebulk stale removed',
    events,
    live: {
      incomplete: snapshotTracker(viewModel.inflight),
      group: snapshotGroup(viewModel, 'deletebulk')
    },
    truth: {
      note: 'Fresh page no longer sees removed jobs in current status.',
      incomplete: { total: 0, ready: 0 },
      group: { total: 0, ready: 0, deleted: 0 }
    }
  };
}

function runBulkRemoveResyncedFixture() {
  const result = runBulkRemoveStaleFixture();
  const viewModel = createViewModel();

  for (const event of result.events) {
    deliver(viewModel, event);
  }

  deliverEmptyAuthoritativeSnapshot(viewModel, 2);

  return {
    name: 'deletebulk after authoritative current snapshot',
    events: result.events,
    live: {
      incomplete: snapshotTracker(viewModel.inflight),
      group: snapshotGroup(viewModel, 'deletebulk')
    },
    truth: result.truth
  };
}

function assertResolvedScenario(scenario) {
  assert.equal(scenario.live.incomplete.running || 0, scenario.truth.incomplete.running || 0);
  assert.equal(scenario.live.incomplete.ready || 0, scenario.truth.incomplete.ready || 0);
  assert.equal(scenario.live.incomplete.total || 0, scenario.truth.incomplete.total || 0);
  assert.equal(scenario.live.group.running || 0, scenario.truth.group.running || 0);
  assert.equal(scenario.live.group.ready || 0, scenario.truth.group.ready || 0);
}

if (process.argv.includes('--assert')) {
  assertResolvedScenario(runTabletestResyncedFixture());
  assertResolvedScenario(runBulkRemoveResyncedFixture());
  console.log('status-page-stale-counts regression passed');
  process.exit(0);
}

function esc(value) {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function nonZeroEntries(snapshot) {
  return countProps
    .filter(prop => snapshot[prop] > 0)
    .map(prop => [prop, snapshot[prop]]);
}

function renderProgress(snapshot) {
  const entries = nonZeroEntries(snapshot);

  if (entries.length === 0) {
    return '<div class="empty">No visible jobs</div>';
  }

  const total = entries.reduce((sum, [, count]) => sum + count, 0);

  return `<div class="progress">${entries.map(([state, count]) => {
    const width = (count / total * 100).toFixed(1);

    return `<div class="bar ${state}" style="width:${width}%">${count} ${state}</div>`;
  }).join('')}</div>`;
}

function renderCounts(title, snapshot) {
  return `<section>
    <h3>${esc(title)} <span class="badge">${esc(snapshot.total || 0)}</span></h3>
    ${renderProgress(snapshot)}
    <table>
      <tbody>
        ${countProps.map(prop => `<tr><th>${esc(prop)}</th><td>${esc(snapshot[prop] || 0)}</td></tr>`).join('')}
      </tbody>
    </table>
  </section>`;
}

function renderTruth(title, truth) {
  return `<section>
    <h3>${esc(title)} <span class="badge">${esc(truth.group.total || 0)}</span></h3>
    <p>${esc(truth.note)}</p>
    ${renderProgress(truth.group)}
    <table>
      <tbody>
        ${Object.entries(truth.group).map(([prop, count]) => `<tr><th>${esc(prop)}</th><td>${esc(count)}</td></tr>`).join('')}
      </tbody>
    </table>
  </section>`;
}

function renderEvents(events) {
  return `<ol>${events.map(event => {
    const label = `${event.RepGroup}: ${event.FromState} -> ${event.ToState} x${event.Count}`;

    return `<li><code>${esc(label)}</code></li>`;
  }).join('')}</ol>`;
}

function renderScenario(scenario) {
  return `<article>
    <h2>${esc(scenario.name)}</h2>
    <div class="grid">
      ${renderCounts('Live browser state after dropped deltas: Incomplete', scenario.live.incomplete)}
      ${renderCounts('Live browser state after dropped deltas: RepGroup', scenario.live.group)}
      ${renderTruth('Refresh/search truth', scenario.truth)}
    </div>
    <details>
      <summary>Delivered websocket count deltas</summary>
      ${renderEvents(scenario.events)}
    </details>
  </article>`;
}

const scenarios = [
  runTabletestStaleRunningFixture(),
  runTabletestResyncedFixture(),
  runBulkRemoveStaleFixture(),
  runBulkRemoveResyncedFixture()
];

const html = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>WR stale status count reproduction</title>
  <style>
    body {
      color: #222;
      font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
      line-height: 1.45;
      margin: 24px auto;
      max-width: 1180px;
      padding: 0 20px 40px;
    }
    h1, h2, h3 { line-height: 1.2; }
    article {
      border-top: 1px solid #ddd;
      margin-top: 28px;
      padding-top: 20px;
    }
    .grid {
      display: grid;
      gap: 16px;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    }
    section {
      border: 1px solid #ddd;
      border-radius: 4px;
      padding: 12px;
    }
    .badge {
      background: #777;
      border-radius: 10px;
      color: white;
      display: inline-block;
      font-size: 12px;
      min-width: 22px;
      padding: 2px 7px;
      text-align: center;
    }
    .progress {
      background: #f5f5f5;
      border-radius: 4px;
      box-shadow: inset 0 1px 2px rgb(0 0 0 / 10%);
      display: flex;
      height: 28px;
      margin: 12px 0;
      overflow: hidden;
    }
    .bar {
      box-sizing: border-box;
      color: white;
      font-size: 12px;
      line-height: 28px;
      overflow: hidden;
      padding: 0 8px;
      text-align: center;
      white-space: nowrap;
    }
    .ready { background: #5bc0de; }
    .running { background: #337ab7; }
    .complete { background: #5cb85c; }
    .deleted { background: #d9534f; }
    .buried, .lost { background: #d9534f; }
    .delayed, .dependent, .suspended { background: #f0ad4e; }
    .empty {
      background: #f5f5f5;
      border-radius: 4px;
      color: #555;
      margin: 12px 0;
      padding: 7px 10px;
    }
    table {
      border-collapse: collapse;
      width: 100%;
    }
    th, td {
      border-top: 1px solid #eee;
      padding: 4px 6px;
      text-align: left;
    }
    th {
      color: #555;
      font-weight: 600;
      width: 60%;
    }
    code {
      background: #f7f7f7;
      border: 1px solid #eee;
      border-radius: 3px;
      padding: 1px 4px;
    }
  </style>
</head>
<body>
  <h1>WR stale status count reproduction</h1>
  <p>
    Generated by <code>jobqueue/testdata/status-page-stale-counts/repro.mjs</code>
    from the real <code>jobqueue/static/js/wr/websocket-handler.js</code>
    <code>handleStateChangeMessage</code> function.
  </p>
  <p>
    The live panels show the status page state after one or more websocket count
    deltas are dropped. The truth panel shows what a refresh/search obtains from
    server state after the jobs have actually completed or been removed.
  </p>
  ${scenarios.map(renderScenario).join('\n')}
</body>
</html>
`;

fs.mkdirSync(path.dirname(outputPath), { recursive: true });
fs.writeFileSync(outputPath, html);

console.log(outputPath);
