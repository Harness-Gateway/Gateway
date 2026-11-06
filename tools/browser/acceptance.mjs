import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { mkdir, mkdtemp, readdir, readlink, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const repository = fileURLToPath(new URL('../..', import.meta.url));
const artifacts = process.env.HARNESS_GATEWAY_BROWSER_OUTPUT_DIR || fileURLToPath(new URL('./artifacts', import.meta.url));
await mkdir(artifacts, { recursive: true });
const runtime = await mkdtemp(join(tmpdir(), 'harness-gateway-browser-'));
await mkdir(`${runtime}/pids`);
try {
  execFileSync('go', ['build', '-o', `${runtime}/gateway`, './cmd/harness-gateway'], { cwd: repository, stdio: 'inherit' });
  for (const model of ['codex', 'copilot']) {
    execFileSync('go', ['test', '-c', '-o', `${runtime}/${model}-fixture`, `./internal/adapters/outbound/${model}`], { cwd: repository, stdio: 'inherit' });
  }
} catch (error) {
  await rm(runtime, { recursive: true, force: true });
  throw error;
}
let log = '';
const gateway = spawn(`${runtime}/gateway`, [
  '-l', '127.0.0.1:0', '-c', '1', '-t', '30s',
  '--codex-path', `${runtime}/codex-fixture`, '--copilot-path', `${runtime}/copilot-fixture`,
], { env: { ...process.env,
  HARNESS_GATEWAY_CODEX_TEST: 'acceptance', HARNESS_GATEWAY_COPILOT_TEST: 'acceptance',
  HARNESS_GATEWAY_ACCEPTANCE_PIDS: `${runtime}/pids`,
  HARNESS_GATEWAY_COPILOT_LOG: `${runtime}/copilot.log`,
  HARNESS_GATEWAY_COPILOT_PID: `${runtime}/copilot.pid`,
} });
gateway.stderr.on('data', data => { log += data; });
gateway.on('error', error => { log += error.message; });
const alive = pid => { try { process.kill(Number(pid), 0); return true; } catch { return false; } };
const liveCLIs = async () => (await readdir(`${runtime}/pids`)).filter(alive);
async function until(check, description, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await check()) return; await delay(50); }
  throw new Error(`Timed out: ${description}`);
}
let browser, page;
const report = { cases: [], external: [], errors: [], requests: [] };
try {
  await until(() => log.includes('Harness-Gateway listening'), 'gateway readiness', 10000);
  const base = log.match(/url=(http:\/\/127\.0\.0\.1:\d+)/)[1];
  assert(log.includes('codex:0.0.0-fixture') && log.includes('copilot:0.0.0-fixture'), 'startup must identify both fixture CLI versions');
  assert(!log.includes('Harness CLI version unavailable'), 'fixture version discovery failed');
  report.base = base;
  assert.notEqual(new URL(base).port, '8080');
  browser = await chromium.launch({ executablePath: process.env.HARNESS_GATEWAY_BROWSER_EXECUTABLE || undefined, headless: true });
  report.browser = browser.version();
  const context = await browser.newContext({ viewport: { width: 1440, height: 1080 } });
  await context.route('**/*', route => {
    const url = route.request().url();
    report.requests.push({ url, method: route.request().method() });
    if (new URL(url).origin !== base) { report.external.push(url); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage();
  page.on('pageerror', error => report.errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error' && !message.text().includes('400 (Bad Request)')) report.errors.push(message.text());
  });
  await page.goto(`${base}/docs`);
  await page.getByRole('heading', { name: 'Harness-Gateway', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: /Ask AI/ }).count(), 0);
  await page.screenshot({ path: `${artifacts}/docs-desktop.png` });
  report.cases.push('UI renders with bundled assets, no cloud assistant, and off-origin requests blocked');

  const open = async (method, path) => {
    await page.getByRole('button', { name: new RegExp(`Test Request.*${method} ${path.replaceAll('/', '\\/')}`) }).click();
  };
  const close = () => page.getByRole('button', { name: 'Close Client', exact: true }).click();
  const send = async (method, path) => {
    const pending = page.waitForResponse(response => response.url() === base + path && response.request().method() === method.toUpperCase());
    await page.getByRole('button', { name: new RegExp(`Send ${method} request to`) }).click();
    return pending;
  };
  await open('get', '/health');
  assert.deepEqual(await (await send('get', '/health')).json(), { status: 'ok' });
  await close();
  report.cases.push('health request through Scalar');

  await open('get', '/v1/models');
  const models = await (await send('get', '/v1/models')).json();
  for (const id of ['codex', 'copilot', 'codex/native-one', 'copilot/native-one']) assert(models.data.some(model => model.id === id));
  await until(async () => (await page.locator('body').innerText()).includes('codex/native-one'), 'model catalog visible in client');
  await close();
  report.cases.push('discovered aliases and native IDs through Scalar');

  await open('post', '/v1/chat/completions');
  const first = await send('post', '/v1/chat/completions');
  assert.equal(first.status(), 200);
  assert.equal((await first.json()).choices[0].message.content, 'hello world');
  report.cases.push('default documented Codex example works without editing');

  const edit = async body => {
    const editor = page.locator('[role="textbox"][contenteditable="true"]').first();
    await editor.click();
    await page.keyboard.press('ControlOrMeta+A');
    await page.keyboard.insertText(JSON.stringify(body, null, 2));
    await page.keyboard.press('Tab');
    // Scalar's editor commits through a debounced change handler.
    await delay(500);
  };
  const body = (model, text = 'Say hello', stream = false) => ({
    model, messages: [{ role: 'system', content: 'Be brief' }, { role: 'user', content: text }],
    ...(stream ? { stream: true, stream_options: { include_usage: true } } : {}),
  });
  for (const model of ['codex', 'copilot']) {
    await edit(body(model));
    const response = await send('post', '/v1/chat/completions');
    assert.equal(response.status(), 200);
    const completion = await response.json();
    assert.equal(completion.model, model);
    assert.equal(completion.choices[0].message.content, 'hello world');
    report.cases.push(`${model}: buffered completion`);

    await edit(body(model, 'Say hello', true));
    const stream = await send('post', '/v1/chat/completions');
    assert.equal(JSON.parse(stream.request().postData()).stream, true);
    assert.equal(stream.headers()['content-type'], 'text/event-stream');
    const streamText = await stream.text();
    assert(streamText.endsWith('data: [DONE]\n\n'));
    assert(streamText.includes('hello '));
    assert(streamText.includes('"total_tokens"'));
    await until(async () => (await page.locator('body').innerText()).includes('[DONE]'), `${model} SSE response visible`);
    report.cases.push(`${model}: successful SSE visible, including [DONE] and usage`);

    await edit(body(model, 'hold', true));
    const active = await send('post', '/v1/chat/completions');
    let finished = false;
    active.finished().then(() => { finished = true; }, () => { finished = true; });
    await until(async () => /\bready\b/.test(await page.locator('body').innerText()), `${model} incremental SSE before completion`);
    assert.equal(finished, false, 'UI must show output before the stream finishes');
    const pids = await liveCLIs();
    assert.equal(pids.length, 1, 'one fixture CLI must still be active');
    if (process.platform === 'linux') assert.equal(await readlink(`/proc/${pids[0]}/exe`), `${runtime}/${model}-fixture`);
    report.activeButtons = await page.getByRole('button').allTextContents();
    const cancel = page.getByRole('button', { name: /^Cancel/ }).first();
    await cancel.click({ timeout: 3000 });
    await until(async () => (await liveCLIs()).length === 0, `${model} CLI reaped after Cancel`);
    await edit(body(model));
    const recovered = await send('post', '/v1/chat/completions');
    assert.equal(recovered.status(), 200);
    assert.equal((await recovered.json()).choices[0].message.content, 'hello world');
    report.cases.push(`${model}: text arrives before stream completion; Cancel reaps PID ${pids[0]} and capacity recovers`);
  }

  await edit({ ...body('codex'), temperature: 0.5 });
  const invalid = await send('post', '/v1/chat/completions');
  assert.equal(invalid.status(), 400);
  assert.equal((await invalid.json()).error.code, 'invalid_request');
  await until(async () => (await page.locator('body').innerText()).includes('unknown field'), 'error response visible');
  report.cases.push('unsupported request returns a visible HTTP 400 error');
  await page.screenshot({ path: `${artifacts}/docs-client.png` });
  await close();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${base}/docs`);
  await page.getByRole('heading', { name: 'Harness-Gateway', exact: true }).waitFor();
  await page.screenshot({ path: `${artifacts}/docs-mobile.png` });
  report.cases.push('mobile render');
  assert.deepEqual(report.external, []);
  assert.deepEqual(report.errors, []);
  await until(async () => (await liveCLIs()).length === 0, 'all fixture CLIs stopped');
  report.passed = true;
  delete report.activeButtons;
} catch (error) {
  report.passed = false;
  report.failure = error.stack;
  if (page) {
    await page.screenshot({ path: `${artifacts}/failure.png` }).catch(() => {});
    await writeFile(`${artifacts}/failure-body.txt`, await page.locator('body').innerText().catch(() => ''));
    report.buttons = await page.getByRole('button').allTextContents().catch(() => []);
  }
  process.exitCode = 1;
} finally {
  await browser?.close();
  gateway.kill('SIGINT');
  try {
    await until(() => gateway.exitCode !== null || gateway.signalCode !== null, 'gateway shutdown', 6000);
    assert.equal(gateway.exitCode, 0);
    await until(async () => (await liveCLIs()).length === 0, 'fixture CLI cleanup');
  } catch (error) {
    gateway.kill('SIGKILL');
    report.passed = false;
    report.cleanupFailure = error.stack;
    process.exitCode = 1;
  }
  await writeFile(`${artifacts}/gateway.log`, log);
  await writeFile(`${artifacts}/acceptance.json`, JSON.stringify(report, null, 2));
  await rm(runtime, { recursive: true, force: true });
  console.log(`${report.passed ? 'PASS' : 'FAIL'} browser acceptance (${report.cases.length} cases)`);
  for (const test of report.cases) console.log(`  ${test}`);
  if (report.failure || report.cleanupFailure) console.error(report.failure || report.cleanupFailure);
}
