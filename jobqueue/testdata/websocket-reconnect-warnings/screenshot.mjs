#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-reconnect-warnings.png');
const outputPath = path.resolve(process.cwd(), process.argv[2] || defaultOutput);
const lostWarning = 'Connection to the manager has been lost!';
const websocketWarning = 'WebSocket error: Unknown error';
const distinctWebsocketWarning = 'WebSocket error: manager restarted but status websocket was not ready';
const expectedToken = 'reconnect-token';

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
    window.__wrReconnectFixture = {
      attempts: 0,
      failedAttempts: [],
      requests: [],
      urls: []
    };

    // Absolute per-RepGroup state. On (re)connect the server pushes the full
    // current map as { RepGroup, Counts }.
    const initialSnapshot = [
      { RepGroup: '+all+', Counts: { ready: 1 } },
      { RepGroup: 'reconnect-rg', Counts: { ready: 1 } }
    ];

    const reconnectedSnapshot = [
      { RepGroup: '+all+', Counts: { running: 1 } },
      { RepGroup: 'reconnect-rg', Counts: { running: 1 } }
    ];

    const completionActivity = [
      { RepGroup: '+all+', Counts: {} },
      { RepGroup: 'reconnect-rg', Counts: { complete: 1 } }
    ];

    class FixtureWebSocket {
      constructor(url) {
        this.readyState = 0;
        this.url = url;
        this.id = ++window.__wrReconnectFixture.attempts;
        window.__wrReconnectFixture.urls.push(url);

        if (this.id === 2) {
          setTimeout(() => this.fail([
            {},
            { message: 'manager restarted but status websocket was not ready' }
          ]), 0);
          return;
        }

        setTimeout(() => this.open(), 0);
      }

      open() {
        this.readyState = 1;
        if (this.onopen) this.onopen({});
      }

      fail(errors = [{}]) {
        for (const error of errors) {
          if (this.onerror) this.onerror(error);
        }
        this.readyState = 3;
        window.__wrReconnectFixture.failedAttempts.push(this.id);
        if (this.onclose) this.onclose({});
      }

      send(raw) {
        window.__wrReconnectFixture.requests.push({ attempt: this.id, raw });
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request !== 'current') {
          return;
        }

        if (this.id === 1) {
          this.emitEach(initialSnapshot);
          setTimeout(() => this.fail(), 80);
        } else {
          this.emitEach(reconnectedSnapshot);
          setTimeout(() => this.emitEach(completionActivity), 80);
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

async function statusWarnings(page) {
  return page.locator('#statuserrors .alert p').allInnerTexts();
}

function countWarning(warnings, warning) {
  return warnings.filter(message => message.trim() === warning).length;
}

async function assertOutageWarningsDoNotAccumulate(page) {
  await page.waitForFunction(() => {
    return window.__wrReconnectFixture.failedAttempts.length >= 2;
  }, { timeout: 10000 });
  await page.waitForFunction((warning) => {
    return Array.from(document.querySelectorAll('#statuserrors .alert p'))
      .some(element => element.textContent.trim() === warning);
  }, lostWarning, { timeout: 10000 });
  await page.waitForFunction((warning) => {
    return Array.from(document.querySelectorAll('#statuserrors .alert p'))
      .some(element => element.textContent.trim() === warning);
  }, distinctWebsocketWarning, { timeout: 10000 });

  const warnings = await statusWarnings(page);
  const lostCount = countWarning(warnings, lostWarning);
  const websocketCount = countWarning(warnings, websocketWarning);
  const distinctWebsocketCount = countWarning(warnings, distinctWebsocketWarning);

  if (lostCount !== 1) {
    throw new Error(`expected one lost-manager warning while down, saw ${lostCount}: ${warnings.join(' | ')}`);
  }

  if (websocketCount !== 1) {
    throw new Error(`expected one rendered unknown websocket warning while down, saw ${websocketCount}: ${warnings.join(' | ')}`);
  }

  if (distinctWebsocketCount !== 1) {
    throw new Error(`expected distinct websocket warning to remain visible while down, saw ${distinctWebsocketCount}: ${warnings.join(' | ')}`);
  }

  const websocketWarnings = warnings.filter(message => message.trim().startsWith('WebSocket error:'));
  if (websocketWarnings.length !== 2) {
    throw new Error(`expected exactly two distinct websocket warnings while down, saw ${websocketWarnings.length}: ${warnings.join(' | ')}`);
  }
}

async function assertReconnectClearsWarnings(page) {
  await page.waitForFunction(() => {
    return window.__wrReconnectFixture.requests.some(entry => {
      try {
        return entry.attempt >= 3 && JSON.parse(entry.raw).Request === 'current';
      } catch {
        return false;
      }
    });
  }, { timeout: 15000 });
  await page.locator('[data-repgroup="reconnect-rg"] .progress-bar', { hasText: '1 complete' })
    .waitFor({ timeout: 10000 });
  await page.waitForFunction(() => {
    return Array.from(document.querySelectorAll('#statuserrors .alert p'))
      .every(element => {
        const text = element.textContent.trim();

        return text !== 'Connection to the manager has been lost!' &&
          !text.startsWith('WebSocket error:');
      });
  }, { timeout: 10000 });

  const urls = await page.evaluate(() => window.__wrReconnectFixture.urls);
  if (urls.length < 3 || !urls.every(url => url.includes(`token=${expectedToken}`))) {
    throw new Error(`expected reconnects to keep the same token, saw URLs: ${urls.join(' | ')}`);
  }
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
    await page.goto(`${baseURL}/status.html?token=${expectedToken}`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    await page.locator('[data-repgroup="reconnect-rg"] .progress-bar', { hasText: '1 pending' })
      .waitFor({ timeout: 10000 });

    await assertOutageWarningsDoNotAccumulate(page);
    await assertReconnectClearsWarnings(page);
    await page.screenshot({ path: outputPath, fullPage: true });
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  console.log(outputPath);
}

await captureScreenshot();
