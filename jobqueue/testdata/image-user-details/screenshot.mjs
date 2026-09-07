#!/usr/bin/env node

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../..');
const staticRoot = path.join(repoRoot, 'jobqueue/static');
const defaultOutput = path.join(repoRoot, '.tmp/agent/webui-test/status-webui-image-user-details.png');
const outputPath = path.resolve(process.cwd(), process.argv[2] || defaultOutput);
const imageUserPhrase = "the docker image's user";
const imageUserLine = `runs as: ${imageUserPhrase}`;

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

// containerJob returns a job shaped like the JStatus the manager marshals for a
// details reply, because websocket-handler.js pushes the parsed JSON itself into
// the knockout context (detailsOA.push(json)) rather than mapping it first.
function containerJob(overrides = {}) {
  return {
    Key: 'image-user-job',
    RepGroup: 'containers',
    ReqGroup: 'image-user-fixture',
    State: 'buried',
    Cmd: 'echo container',
    Cwd: '/job',
    CwdBase: '/tmp/wr',
    Host: 'worker1',
    HostID: '',
    HostIP: '10.0.0.8',
    SSHCommand: '',
    StdErr: '',
    StdOut: '',
    ExpectedRAM: 1024,
    ExpectedTime: 300,
    RequestedDisk: 0,
    Cores: 1,
    NoRetryOverWalltime: 0,
    Attempts: 1,
    WaitingForDepGroups: [],
    Dependencies: [],
    DepGroups: [],
    LimitGroups: [],
    Modules: [],
    OtherRequests: [],
    Env: [],
    EnvOverrides: [],
    Behaviours: '',
    Mounts: '',
    MonitorDocker: '',
    WithDocker: '',
    WithSingularity: '',
    ContainerMounts: '',
    ContainerImageUser: false,
    FailReason: 'command exited non-zero',
    Exitcode: 1,
    Walltime: 5,
    CPUtime: 1,
    PeakRAM: 10,
    PeakDisk: 0,
    Pid: 1234,
    Started: 1757000000,
    Ended: 1757000005,
    Exited: true,
    Similar: 0,
    Override: 0,
    Priority: 0,
    Retries: 3,
    CwdMatters: false,
    HomeChanged: false,
    ...overrides
  };
}

// The three cases the "runs as" display guard has to separate. Only the first
// one really runs as the image's user: --container_image_user suppresses
// docker's --user, while singularity has no such option and always maps the
// calling user in, so the flag is inert there.
//
// Each case carries a distinct exit code because the manager groups a details
// reply by state, exit code and fail reason and returns one job per group for
// the page's opening `Limit: 1` request, so a real server sends all three of
// these together and the page renders all three panels at once.
const cases = [
  {
    label: 'docker job with --container_image_user',
    cmd: 'echo docker-image-user',
    imageLine: 'docker image: ubuntu:latest',
    saysImageUser: true,
    overrides: {
      Key: 'image-user-docker-flag',
      Exitcode: 1,
      WithDocker: 'ubuntu:latest',
      ContainerImageUser: true
    }
  },
  {
    label: 'singularity job with --container_image_user',
    cmd: 'echo singularity-image-user',
    imageLine: 'singularity image: ubuntu.sif',
    saysImageUser: false,
    overrides: {
      Key: 'image-user-singularity-flag',
      Exitcode: 2,
      WithSingularity: 'ubuntu.sif',
      ContainerImageUser: true
    }
  },
  {
    label: 'docker job without --container_image_user',
    cmd: 'echo docker-calling-user',
    imageLine: 'docker image: ubuntu:latest',
    saysImageUser: false,
    overrides: {
      Key: 'image-user-docker-default',
      Exitcode: 3,
      WithDocker: 'ubuntu:latest',
      ContainerImageUser: false
    }
  }
];

function fakeWebSocketScript() {
  const jobs = cases.map(testCase => containerJob({ Cmd: testCase.cmd, ...testCase.overrides }));

  return `(() => {
    window.__wrImageUserFixtureRequests = [];

    const currentSnapshot = [
      { RepGroup: '+all+', FromState: 'new', ToState: 'buried', Count: ${jobs.length} },
      { RepGroup: 'containers', FromState: 'new', ToState: 'buried', Count: ${jobs.length} }
    ];

    const buriedJobs = ${JSON.stringify(jobs)};

    class FixtureWebSocket {
      constructor() {
        this.readyState = 0;
        setTimeout(() => {
          this.readyState = 1;
          if (this.onopen) this.onopen({});
        }, 0);
      }

      send(raw) {
        window.__wrImageUserFixtureRequests.push(raw);
        let request = {};
        try {
          request = JSON.parse(raw);
        } catch {
          return;
        }

        if (request.Request === 'current') {
          this.emitEach(currentSnapshot);
        }

        if (request.Request === 'details' && request.RepGroup === 'containers' && request.State === 'buried') {
          this.emitEach(buriedJobs, 10);
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

// panelTexts returns the whitespace-normalised rendered text of every job
// details panel on the page.
async function panelTexts(page) {
  const panels = await page.locator('.top-margin.panel').all();
  if (panels.length !== cases.length) {
    throw new Error(`expected ${cases.length} job details panels, got ${panels.length}`);
  }

  const texts = [];
  for (const panel of panels) {
    texts.push((await panel.innerText()).replace(/\s+/g, ' '));
  }

  return texts;
}

function assertPanel(texts, testCase) {
  const matches = texts.filter(text => text.includes(testCase.cmd));
  if (matches.length !== 1) {
    throw new Error(`expected exactly 1 details panel for ${JSON.stringify(testCase.cmd)}, got ${matches.length}`);
  }

  const text = matches[0];
  if (!text.includes(testCase.imageLine)) {
    throw new Error(`the ${testCase.label}'s panel did not show ${JSON.stringify(testCase.imageLine)}`);
  }

  const says = text.includes(imageUserPhrase);
  console.log(`${testCase.label} => ${says ? 'says' : 'does not say'} it runs as ${imageUserPhrase}`);

  if (testCase.saysImageUser && !text.includes(imageUserLine)) {
    throw new Error(`the ${testCase.label}'s panel did not show ${JSON.stringify(imageUserLine)}`);
  }

  if (!testCase.saysImageUser && says) {
    throw new Error(`the ${testCase.label}'s panel wrongly showed ${JSON.stringify(imageUserPhrase)}`);
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
    await page.goto(`${baseURL}/status.html?token=image-user-fixture`, {
      waitUntil: 'networkidle',
      timeout: 30000
    });
    await page.waitForSelector('body.ko-initialized', { timeout: 10000 });
    await page.locator('[data-repgroup="containers"] .progress-bar', { hasText: `${cases.length} buried` }).click();
    await page.waitForFunction(() => {
      return window.__wrImageUserFixtureRequests.some(raw => raw.includes('"details"'));
    }, { timeout: 10000 });

    for (const testCase of cases) {
      await page.getByText(testCase.cmd).first().waitFor({ timeout: 10000 });
    }

    await page.screenshot({ path: outputPath, fullPage: true });

    const texts = await panelTexts(page);
    for (const testCase of cases) {
      assertPanel(texts, testCase);
    }

    const bodyText = await page.locator('body').innerText();
    const occurrences = bodyText.split(imageUserPhrase).length - 1;
    console.log(`rendered ${JSON.stringify(imageUserPhrase)} lines: ${occurrences}`);

    if (occurrences !== 1) {
      throw new Error(`expected exactly 1 rendered ${JSON.stringify(imageUserPhrase)} line, got ${occurrences}`);
    }
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }

  console.log(outputPath);
}

await captureScreenshot();
