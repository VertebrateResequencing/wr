#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-live-heartbeat-details.png');
const outputPath = path.resolve(process.cwd(), process.argv[2] || defaultOutput);

if (outputPath !== repoRoot && !outputPath.startsWith(repoRoot + path.sep)) {
  throw new Error(`refusing to write outside repo: ${outputPath}`);
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

function liveJob(overrides = {}) {
  return {
    Key: 'live-heartbeat-job-1',
    RepGroup: 'livetest',
    ReqGroup: 'live-fixture',
    State: 'running',
    Cmd: "bash -c 'for i in $(seq 1 60); do echo progress $i; sleep 1; done'",
    Cwd: '/job1',
    CwdBase: '/tmp/wr',
    Host: 'worker1',
    HostID: '',
    HostIP: '10.0.0.8',
    SSHCommand: "ssh -- ubuntu@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'",
    StdErr: 'warning 1\n',
    StdOut: 'progress 1\n',
    ExpectedRAM: 1024,
    ExpectedTime: 300,
    RequestedDisk: 0,
    Cores: 1,
    Attempts: 1,
    WaitingForDepGroups: [],
    Dependencies: [],
    DepGroups: [],
    LimitGroups: [],
    Modules: [],
    OtherRequests: [],
    Env: [],
    Behaviours: '',
    Mounts: '',
    MonitorDocker: '',
    WithDocker: '',
    WithSingularity: '',
    ContainerMounts: '',
    FailReason: '',
    Exitcode: -1,
    Walltime: 5,
    CPUtime: 0,
    PeakRAM: 0,
    PeakDisk: 0,
    Pid: 1234,
    Exited: false,
    Similar: 0,
    Override: 0,
    Priority: 0,
    Retries: 3,
    CwdMatters: false,
    HomeChanged: false,
    ...overrides
  };
}

function fakeWebSocketScript() {
  return `(() => {
    window.__wrLiveHeartbeatFixtureRequests = [];

    const currentSnapshot = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'running', Count: 1 },
      { RepGroup: 'livetest', FromState: 'new', ToState: 'running', Count: 1 }
    ];

    const initialJob = ${JSON.stringify(liveJob())};
    const heartbeatUpdate = ${JSON.stringify(liveJob({
      IsPushUpdate: true,
      PeakRAM: 321,
      CPUtime: 4,
      PeakDisk: 12
    }))};

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        setTimeout(() => {
          this.readyState = 1;
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        window.__wrLiveHeartbeatFixtureRequests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request === 'current') {
          this.emitEach(currentSnapshot);
        }

        if (request.Request === 'details' && request.RepGroup === 'livetest' &&
            (request.State === 'running' || request.State === 'reserved')) {
          this.emitEach([initialJob], 10);
          this.emitEach([heartbeatUpdate], 80);
        }
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }

      emitEach(messages, delay = 5) {
        messages.forEach((message, index) => {
          setTimeout(() => {
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

async function captureScreenshot() {
  const { chromium } = await loadPlaywright();
  const server = await createStaticServer();
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}`;

  fs.mkdirSync(path.dirname(outputPath), { recursive: true });

  const browser = await chromium.launch({ headless: true });

  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    page.on('console', message => {
      if (message.type() === 'error') {
        console.error(`browser console error: ${message.text()}`);
      }
    });
    page.on('pageerror', error => {
      console.error(`browser page error: ${error.message}`);
    });

    await page.addInitScript(fakeWebSocketScript());
    await page.goto(`${baseURL}/status.html?token=live-heartbeat-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    await page.locator('[data-repgroup="livetest"] .progress-bar', { hasText: '1 running' }).click();
    await page.waitForFunction(() => {
      return window.__wrLiveHeartbeatFixtureRequests.some(raw => raw.includes('"details"'));
    }, { timeout: 10000 });
    await page.getByText('progress 1').waitFor({ timeout: 10000 });
    await page.getByText('warning 1').waitFor({ timeout: 10000 });
    await page.locator('.actual-value.live-value', { hasText: '321 MB' }).waitFor({ timeout: 10000 });
    await page.getByText('CPU: 4s').waitFor({ timeout: 10000 });
    await page.screenshot({ path: outputPath, fullPage: true });

    const visibleText = await page.locator('body').innerText();
    for (const expected of ['STDOUT', 'STDERR', 'progress 1', 'warning 1', '321 MB']) {
      if (!visibleText.includes(expected)) {
        throw new Error(`live heartbeat details did not include ${JSON.stringify(expected)}`);
      }
    }
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  console.log(outputPath);
}

await captureScreenshot();
