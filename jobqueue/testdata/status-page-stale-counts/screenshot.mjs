#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-stale-running-resolved.png');
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

function fakeWebSocketScript() {
  return `(() => {
    window.__wrFixtureRequests = [];
    window.__wrFixtureMessages = [];

    // Stale absolute state: the page first shows tabletest jobs running and
    // deletebulk jobs pending (this is the state that would have been left
    // behind by dropped deltas under the old protocol).
    const staleState = [
      { RepGroup: '+all+', Counts: { running: 1, ready: 2 } },
      { RepGroup: 'tabletest', Counts: { running: 1, complete: 1 } },
      { RepGroup: 'deletebulk', Counts: { ready: 2 } }
    ];

    // Authoritative absolute state: tabletest jobs are actually complete, and
    // the deletebulk jobs have been removed. The client replaces each RepGroup's
    // counts wholesale, so the stale running/pending counts are cleared.
    const authoritativeState = [
      { RepGroup: '+all+', Counts: {} },
      { RepGroup: 'tabletest', Counts: { complete: 2 } },
      { RepGroup: 'deletebulk', Counts: {} }
    ];

    const tabletestSearchResults = [
      {
        Key: 'tabletest-a',
        RepGroup: 'tabletest',
        State: 'complete',
        Cmd: 'echo a',
        Cwd: '/tmp',
        CwdBase: '/tmp',
        Host: 'localhost',
        Exitcode: 0,
        FailReason: '',
        Walltime: 0.01,
        Started: 1780000000,
        Ended: 1780000000,
        Exited: true
      },
      {
        Key: 'tabletest-b',
        RepGroup: 'tabletest',
        State: 'complete',
        Cmd: 'echo b',
        Cwd: '/tmp',
        CwdBase: '/tmp',
        Host: 'localhost',
        Exitcode: 0,
        FailReason: '',
        Walltime: 0.01,
        Started: 1780000000,
        Ended: 1780000000,
        Exited: true
      }
    ];

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        this.sent = [];
        this.currentRequests = 0;

        setTimeout(() => {
          this.readyState = 1;
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        this.sent.push(raw);
        window.__wrFixtureRequests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request === 'current') {
          this.currentRequests += 1;

          // Deliver the stale absolute state first, then the authoritative
          // absolute state that converges the counts; the wholesale replace
          // clears the stale running/pending counts.
          this.emitEach(staleState);
          setTimeout(() => this.emitEach(authoritativeState), 200);
        }

        if (request.Request === 'details' && request.RepGroup === 'tabletest') {
          this.emitEach(tabletestSearchResults, 20);
        }
      }

      close() {
        this.readyState = 3;
        if (this.onclose) this.onclose({});
      }

      emitEach(messages, delay = 5) {
        messages.forEach((message, index) => {
          setTimeout(() => {
            window.__wrFixtureMessages.push(message);
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
    const page = await browser.newPage({ viewport: { width: 1280, height: 960 } });
    page.on('console', message => {
      if (message.type() === 'error') {
        console.error(`browser console error: ${message.text()}`);
      }
    });
    page.on('pageerror', error => {
      console.error(`browser page error: ${error.message}`);
    });
    await page.addInitScript(fakeWebSocketScript());
    await page.goto(`${baseURL}/status.html?token=repro-token`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    // The authoritative absolute state replaces each RepGroup's counts wholesale,
    // so the previously-shown stale running/pending counts are gone and the
    // RepGroup shows its real terminal state (tabletest = 2 complete).
    await page.waitForFunction(() => {
      const tabletest = document.querySelector('[data-repgroup="tabletest"]');

      return tabletest && tabletest.innerText.includes('2 complete');
    }, undefined, { timeout: 10000 });
    await page.waitForFunction(() => {
      const bodyText = document.body.innerText;

      return !bodyText.includes('1 running') && !bodyText.includes('2 pending');
    }, undefined, { timeout: 10000 });
    const searchBox = page.getByRole('textbox');
    await searchBox.click();
    await searchBox.pressSequentially('tabletest');
    await page.getByRole('button', { name: /Search/ }).click();
    await page.waitForFunction(() => {
      return window.__wrFixtureRequests.some(raw => raw.includes('"details"'));
    }, undefined, { timeout: 10000 });
    try {
      await page.getByText('2 jobs found across 1').waitFor({ timeout: 10000 });
    } catch (error) {
      console.error(await page.locator('body').innerText());
      throw error;
    }
    await page.getByText('Show All Jobs').waitFor({ timeout: 10000 });
    await page.screenshot({ path: outputPath, fullPage: true });
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  console.log(outputPath);
}

await captureScreenshot();
