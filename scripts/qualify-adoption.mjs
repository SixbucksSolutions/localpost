import assert from 'node:assert/strict';
import {spawn, spawnSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {existsSync, mkdtempSync, readFileSync, rmSync} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const maxMessage = 10 * 1024 * 1024;
const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));

export function clientRunner(client, baseUrl) {
  return (args, input = Buffer.alloc(0), expectedExit = 0, expectedCode) => {
    const result = spawnSync(client, ['--url', baseUrl, '--timeout', '15s', ...args], {
      input, maxBuffer: 24 * 1024 * 1024, timeout: 30_000, windowsHide: true,
    });
    assert.ifError(result.error);
    assert.equal(result.status, expectedExit, `client ${args[0]} exit`);
    if (expectedCode) {
      assert.equal(result.stdout.length, 0);
      assert.deepEqual(JSON.parse(result.stderr), {error: {code: expectedCode, operation: args[0]}});
      return;
    }
    assert.equal(result.stderr.length, 0);
    return args[0] === 'get' ? result.stdout : JSON.parse(result.stdout);
  };
}

// Uses the runtime harness's private CONFIG channel. No extra HTTP commands are enabled.
export async function qualifyAdoption({client, baseUrl, setCapacity, browser = process.env.LOCALPOST_CHROME,
  requireBrowser = process.env.LOCALPOST_REQUIRE_BROWSER === '1', recordEvidence = () => {}}) {
  const run = clientRunner(client, baseUrl);
  const owned = new Set();
  const register = () => {
    const {id} = run(['register']);
    assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    owned.add(id);
    return id;
  };
  const identity = spawnSync(client, ['--version'], {encoding: 'utf8', timeout: 5000});
  assert.ifError(identity.error);
  assert.equal(identity.status, 0);
  assert.equal(identity.stderr, '');
  const identityMatch = /^localpost-client (\S+) \(commit ([^;]+); (go[^;]+); ([^)]+)\)$/.exec(identity.stdout.trim());
  assert.ok(identityMatch, 'Unexpected client build identity');
  if (process.env.GITHUB_SHA) {
    assert.equal(identityMatch[1], 'adoption-r1');
    assert.equal(identityMatch[2], process.env.GITHUB_SHA);
    assert.equal(identityMatch[3], 'go1.27.3');
  }
  const evidence = {
    status: 'failed', binarySHA256: createHash('sha256').update(readFileSync(client)).digest('hex'),
    version: identity.stdout.trim(), sourceCommit: identityMatch[2],
    checks: [], browser: {status: 'not-run'},
  };
  recordEvidence(evidence);
  try {
    const id = register();
    assert.deepEqual(run(['read', id]), {id, messages: [], next_after: null});
    assert.equal(run(['clear', id]).cleared, 0);
    assert.equal(run(['delete', id]).deleted, false);
    const payloads = [Buffer.from([0, 255, 13, 10, 128, 1, 0]), Buffer.alloc(0), Buffer.from('third')];
    const messages = payloads.map(payload => run(['send', id], payload));
    const first = run(['read', id, '--count', '2']);
    assert.equal(first.messages.length, 2);
    assert.equal(first.next_after, messages[1].message_id);
    assert.deepEqual(run(['read', id, '--count', '2']), first);
    for (let index = 0; index < 2; index++) {
      assert.equal(first.messages[index].id, messages[index].message_id);
      assert.equal(first.messages[index].bytes, payloads[index].length);
      assert.equal(first.messages[index].data, payloads[index].toString('base64'));
    }
    const second = run(['read', id, '--after', first.next_after, '--count', '2']);
    assert.equal(second.messages.length, 1);
    assert.equal(second.messages[0].id, messages[2].message_id);
    assert.equal(second.messages[0].data, payloads[2].toString('base64'));
    assert.deepEqual(run(['read', id, '--after', second.next_after]), {id, messages: [], next_after: null});
    messages.forEach((message, index) => assert.deepEqual(run(['get', id, message.message_id]), payloads[index]));
    evidence.checks.push('binary-empty', 'retained-read', 'exclusive-pagination', 'raw-get');
    assert.equal(run(['clear', id]).cleared, 3);
    assert.deepEqual(run(['read', id]).messages, []);
    run(['get', id, messages[0].message_id], undefined, 3, 'message_not_found');
    const afterClear = run(['send', id], Buffer.from('after-clear'));
    assert.ok(compareIDs(afterClear.message_id, messages[2].message_id) > 0);
    assert.equal(run(['delete', id]).deleted, true);
    assert.equal(run(['delete', id]).deleted, false);
    const reused = run(['send', id], payloads[0]);
    assert.deepEqual(run(['get', id, reused.message_id]), payloads[0]);
    evidence.checks.push('clear-retains-id-order', 'delete-address-reuse');

    const largeID = register();
    const maximum = Buffer.alloc(maxMessage, 0xa5);
    const large = run(['send', largeID], maximum);
    assert.equal(large.bytes, maxMessage);
    assert.deepEqual(run(['get', largeID, large.message_id]), maximum);
    run(['send', largeID], Buffer.alloc(maxMessage + 1), 2, 'input_too_large');
    assert.equal(run(['read', largeID, '--count', '2']).messages.length, 1);
    evidence.checks.push('10MiB-roundtrip', '10MiB-plus-one-rejection');

    await setCapacity('8mb');
    try {
      run(['send', id], Buffer.from('rejected'), 1, 'memory_limit');
      // Independently establish that the native error really travels inside HTTP 200.
      const response = await fetch(`${baseUrl}/XADD/${encodeURIComponent(`localpost:v1:${id}`)}/*/data`, {
        method: 'PUT', headers: {'Content-Type': 'text/plain;charset=UTF-8'}, body: 'b64:cmVqZWN0ZWQ=',
        signal: AbortSignal.timeout(5000),
      });
      assert.equal(response.status, 200);
      const rejection = await response.json();
      assert.equal(rejection.XADD[0], false);
      assert.match(rejection.XADD[1], /^OOM/);
      assert.deepEqual(run(['get', largeID, large.message_id]), maximum);
      assert.deepEqual(run(['get', id, reused.message_id]), payloads[0]);
      assert.equal(run(['read', id, '--count', '16']).messages.length, 1);
      assert.equal(run(['delete', largeID]).deleted, true);
      const recovered = run(['send', id], Buffer.from('capacity-recovered'));
      assert.deepEqual(run(['get', id, recovered.message_id]), Buffer.from('capacity-recovered'));
    } finally {
      await setCapacity('128mb');
    }
    evidence.checks.push('HTTP200-OOM-typed-error', 'OOM-preserves-accepted-streams', 'capacity-recovery');
    const diagnosticsID = register();
    evidence.browser = await qualifyBrowser({baseUrl, id: diagnosticsID, browser, requireBrowser});
    if (evidence.browser.status === 'passed') {
      const delivered = run(['read', diagnosticsID, '--count', '2']);
      assert.equal(delivered.messages.length, 2);
      assert.deepEqual(JSON.parse(Buffer.from(delivered.messages[1].data, 'base64')), {
        operation: 'adoption-browser', phase: 'send-read', status: 'passed', correlation: diagnosticsID,
      });
    }
    evidence.status = 'passed';
    owned.delete(id);
    // The harness invokes this after restarting Valkey, proving old logical addresses work.
    return {evidence, verifyFrontendRestart() {
      assert.equal(run(['read', id, '--count', '16']).messages.length, 2);
      assert.deepEqual(run(['get', id, reused.message_id]), payloads[0]);
      evidence.checks.push('frontend-restart-retention');
    }, verifyRestart() {
      assert.deepEqual(run(['read', id]).messages, []);
      const sent = run(['send', id], payloads[0]);
      assert.deepEqual(run(['get', id, sent.message_id]), payloads[0]);
      assert.equal(run(['delete', id]).deleted, true);
      evidence.checks.push('restart-loss-and-address-reuse');
    }};
  } finally {
    for (const id of owned) run(['delete', id]);
  }
}

function compareIDs(left, right) {
  const a = left.split('-').map(BigInt);
  const b = right.split('-').map(BigInt);
  return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : a[1] < b[1] ? -1 : a[1] > b[1] ? 1 : 0;
}

// Browser code is self-contained, with bounded bodies and no credentials or raw diagnostics.
export async function browserProof(baseUrl, id) {
  const key = `localpost:v1:${id}`;
  async function command(name, args, payload) {
    const encoded = [name, ...args].map(encodeURIComponent).join('/');
    const response = await fetch(payload === undefined ? `${baseUrl}/` : `${baseUrl}/${encoded}`, {
      method: payload === undefined ? 'POST' : 'PUT',
      headers: {'Content-Type': 'text/plain;charset=UTF-8'}, credentials: 'omit', cache: 'no-store', redirect: 'error',
      body: payload === undefined ? encoded : payload, signal: AbortSignal.timeout(5000),
    });
    if (response.status !== 200) throw new Error('http_error');
    const reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 65536) { await reader.cancel(); throw new Error('response_too_large'); }
      chunks.push(value);
    }
    const bytes = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    const envelope = JSON.parse(new TextDecoder().decode(bytes));
    if (Object.keys(envelope).length !== 1 || !Object.hasOwn(envelope, name)) throw new Error('invalid_response');
    const result = envelope[name];
    if (Array.isArray(result) && result.length === 2 && result[0] === false && typeof result[1] === 'string') {
      throw new Error(result[1].startsWith('OOM') ? 'memory_limit' : 'backend_error');
    }
    return result;
  }
  const payload = 'b64:' + btoa(String.fromCharCode(0, 255, 13, 10, 128));
  const sent = await command('XADD', [key, '*', 'data'], payload);
  if (typeof sent !== 'string' || !/^\d+-\d+$/.test(sent)) throw new Error('invalid_response');
  const rows = await command('XRANGE', [key, '-', '+', 'COUNT', '1']);
  if (!Array.isArray(rows) || rows.length !== 1 || Object.keys(rows[0]).sort().join() !== 'id,msg' ||
    rows[0].id !== sent || Object.keys(rows[0].msg).join() !== 'data' || rows[0].msg.data !== payload) {
    throw new Error('invalid_response');
  }
  const diagnostic = {operation: 'adoption-browser', phase: 'send-read', status: 'passed', correlation: id};
  const reported = await command('XADD', [key, '*', 'data'], 'b64:' + btoa(JSON.stringify(diagnostic)));
  if (typeof reported !== 'string' || !/^\d+-\d+$/.test(reported)) throw new Error('invalid_response');
  return {status: 'passed', checks: ['binary-send-read', 'diagnostic-delivery']};
}

async function qualifyBrowser({baseUrl, id, browser, requireBrowser}) {
  browser ||= ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find(existsSync);
  if (!browser) {
    assert.equal(requireBrowser, false, 'Required Chrome/Chromium is unavailable');
    return {status: 'unavailable', reason: 'Chrome/Chromium executable absent', architecture: process.arch};
  }
  const profile = mkdtempSync(path.join(os.tmpdir(), 'localpost-adoption-chrome-'));
  let child;
  let socket;
  let nextID = 0;
  const pending = new Map();
  const network = new Map();
  const command = (method, params = {}) => new Promise((resolve, reject) => {
    const requestID = ++nextID;
    const timer = setTimeout(() => { pending.delete(requestID); reject(new Error(`Browser command timed out: ${method}`)); }, 20_000);
    pending.set(requestID, {resolve, reject, timer});
    socket.send(JSON.stringify({id: requestID, method, params}));
  });
  try {
    child = spawn(browser, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`,
      '--no-first-run', '--no-default-browser-check', 'about:blank'], {windowsHide: true, stdio: 'ignore'});
    let launchError;
    child.on('error', error => { launchError = error; });
    let port;
    for (let attempt = 0; attempt < 100 && !port; attempt++) {
      assert.ifError(launchError);
      assert.equal(child.exitCode, null, 'Browser exited during startup');
      try { port = readFileSync(path.join(profile, 'DevToolsActivePort'), 'utf8').split('\n')[0]; }
      catch (error) { if (error.code !== 'ENOENT') throw error; }
      if (!port) await sleep(100);
    }
    assert.match(port || '', /^\d+$/);
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`, {signal: AbortSignal.timeout(5000)})).json();
    const page = targets.find(target => target.type === 'page');
    assert.ok(page);
    socket = new WebSocket(page.webSocketDebuggerUrl);
    socket.addEventListener('message', ({data}) => {
      const message = JSON.parse(data);
      const item = pending.get(message.id);
      if (item) {
        clearTimeout(item.timer);
        pending.delete(message.id);
        if (message.error) item.reject(new Error('Browser protocol error'));
        else item.resolve(message.result);
      } else if (message.method === 'Network.requestWillBeSent') {
        const {requestId, request} = message.params;
        if (request.url.startsWith(baseUrl + '/') && network.size < 16) network.set(requestId, {method: request.method});
      } else if (message.method === 'Network.responseReceived') {
        const entry = network.get(message.params.requestId);
        if (entry) {
          entry.status = message.params.response.status;
          entry.allowOrigin = Object.entries(message.params.response.headers)
            .find(([name]) => name.toLowerCase() === 'access-control-allow-origin')?.[1];
        }
      }
    });
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Browser connection timeout')), 5000);
      socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, {once: true});
      socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('Browser connection error')); }, {once: true});
    });
    await command('Network.enable');
    await command('Page.enable');
    // A second loopback hostname gives the real browser a secure, cross-origin
    // source without an auxiliary server or private-network security bypass.
    const source = new URL(baseUrl);
    source.hostname = 'localhost';
    await command('Page.navigate', {url: source.origin + '/PING'});
    let origin;
    for (let attempt = 0; attempt < 50; attempt++) {
      const current = await command('Runtime.evaluate', {expression: 'location.origin', returnByValue: true});
      origin = current.result.value;
      if (origin === source.origin) break;
      await sleep(100);
    }
    assert.equal(origin, source.origin, 'Browser loopback source did not load');
    assert.notEqual(origin, new URL(baseUrl).origin);
    const version = (await command('Browser.getVersion')).product;
    const evaluated = await command('Runtime.evaluate', {
      expression: `(${browserProof.toString()})(${JSON.stringify(baseUrl)},${JSON.stringify(id)})`,
      returnByValue: true, awaitPromise: true,
    });
    assert.equal(evaluated.exceptionDetails, undefined, 'Browser send/read failed');
    assert.equal(evaluated.result.value.status, 'passed');
    const requests = [...network.values()];
    assert.ok(requests.some(item => item.method === 'OPTIONS' && item.status >= 200 && item.status < 300 && item.allowOrigin === '*'), 'Browser CORS preflight missing');
    assert.equal(requests.filter(item => ['PUT', 'POST'].includes(item.method) && item.status === 200 && item.allowOrigin === '*').length, 3);
    return {...evaluated.result.value, version, sourceOrigin: origin, requests};
  } finally {
    if (socket?.readyState === WebSocket.OPEN) {
      try { await command('Browser.close'); } catch { /* Process shutdown can precede the CDP reply. */ }
    }
    socket?.close();
    for (const item of pending.values()) clearTimeout(item.timer);
    if (child) {
      for (let attempt = 0; attempt < 50 && child.exitCode === null && child.signalCode === null; attempt++) await sleep(100);
      if (child.exitCode === null) child.kill();
      for (let attempt = 0; attempt < 50 && child.exitCode === null && child.signalCode === null; attempt++) await sleep(100);
      assert.ok(child.exitCode !== null || child.signalCode !== null, 'Owned browser did not stop');
    }
    rmSync(profile, {recursive: true, force: true});
  }
}
