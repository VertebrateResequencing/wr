#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-suspended-job-actions.png');
const outputPath = path.resolve(process.cwd(), process.argv[2] || defaultOutput);
const suspendedHint = 'suspended - use wr resume to make it schedulable again';

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

function fakeWebSocketScript() {
  return `(() => {
    window.__wrSuspendedFixtureRequests = [];

    const currentSnapshot = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'suspended', Count: 2 },
      { RepGroup: 'rg-suspended', FromState: 'new', ToState: 'suspended', Count: 2 }
    ];

    const suspendedJob = {
      Key: 'suspended-job-1',
      RepGroup: 'rg-suspended',
      ReqGroup: 'suspended-fixture',
      State: 'suspended',
      Cmd: 'echo suspended',
      Cwd: '',
      CwdBase: '/tmp',
      Host: '',
      HostID: '',
      HostIP: '',
      SSHCommand: '',
      StdErr: '',
      StdOut: '',
      ExpectedRAM: 1024,
      ExpectedTime: 3600,
      RequestedDisk: 0,
      Cores: 1,
      Attempts: 0,
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
      Exitcode: 0,
      Walltime: 0,
      CPUtime: 0,
      PeakRAM: 0,
      PeakDisk: 0,
      Pid: 0,
      Exited: false,
      Similar: 1,
      Override: 0,
      Priority: 0,
      Retries: 3,
      CwdMatters: false,
      HomeChanged: false
    };

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        setTimeout(() => {
          this.readyState = 1;
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        window.__wrSuspendedFixtureRequests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request === 'current') {
          this.emitEach(currentSnapshot);
        }

        if (request.Request === 'details' && request.RepGroup === 'rg-suspended' && request.State === 'suspended') {
          this.emitEach([suspendedJob], 10);
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
    await page.goto(`${baseURL}/status.html?token=suspended-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    await page.locator('[data-repgroup="rg-suspended"] .progress-bar', { hasText: '2 suspended' }).click();
    await page.waitForFunction(() => {
      return window.__wrSuspendedFixtureRequests.some(raw => raw.includes('"details"'));
    }, { timeout: 10000 });
    await page.getByText('echo suspended').waitFor({ timeout: 10000 });

    const stateTag = page.locator('.prop-tag').filter({
      has: page.locator('.prop-name', { hasText: 'State' })
    }).filter({
      has: page.locator('.prop-value', { hasText: 'suspended' })
    }).first();
    await stateTag.waitFor({ timeout: 10000 });

    const stateValue = (await stateTag.locator('.prop-value').innerText()).trim();
    if (stateValue !== 'suspended') {
      throw new Error(`suspended State text was ${JSON.stringify(stateValue)}`);
    }

    const visibleText = await page.locator('body').innerText();
    if (visibleText.includes(suspendedHint)) {
      throw new Error(`suspended job details still include CLI hint ${JSON.stringify(suspendedHint)}`);
    }

    await page.getByRole('button', { name: 'Resume' }).click();
    await page.getByRole('button', { name: 'Resume 1' }).click();
    await page.waitForFunction(() => {
      return window.__wrSuspendedFixtureRequests.some(raw => {
        try {
          const request = JSON.parse(raw);

          return request.Request === 'resume' && request.Key === 'suspended-job-1';
        } catch {
          return false;
        }
      });
    }, { timeout: 10000 });

    await page.screenshot({ path: outputPath, fullPage: true });
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  console.log(outputPath);
}

await captureScreenshot();
