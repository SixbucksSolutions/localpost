// Bounded OS-process stdio fixture. Runtime protocol qualification uses the pinned stack separately.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFileSync, writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

function processResult(binary, args, input = Buffer.alloc(0)) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {windowsHide: true, stdio: ['pipe', 'pipe', 'pipe']});
    const chunks = {stdout: [], stderr: []};
    const sizes = {stdout: 0, stderr: 0};
    let failure;
    const fail = error => {
      failure ||= error;
      child.kill();
    };
    const timer = setTimeout(() => fail(new Error('Client process timeout')), 15_000);
    child.once('error', fail);
    child.stdin.on('error', error => {
      if (error.code !== 'EPIPE') fail(error);
    });
    for (const name of ['stdout', 'stderr']) {
      child[name].on('data', chunk => {
        sizes[name] += chunk.length;
        if (sizes[name] > 65536) fail(new Error('Client output limit exceeded'));
        else chunks[name].push(chunk);
      });
    }
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      if (failure) reject(failure);
      else resolve({code, signal, stdout: Buffer.concat(chunks.stdout), stderr: Buffer.concat(chunks.stderr)});
    });
    child.stdin.end(input);
  });
}

export async function qualifyClientProcess(binary) {
  const id = '123e4567-e89b-42d3-a456-426614174000';
  const key = `localpost:v1:${id}`;
  let expected;
  let fixtureError;
  let requests = 0;
  // This fixed-response test fixture exists only for this bounded subprocess proof.
  const server = createServer((request, response) => {
    void (async () => {
      assert.ok(expected, 'Unexpected HTTP request');
      assert.equal(expected.consumed, false, 'Unexpected retry');
      expected.consumed = true;
      requests++;
      assert.equal(request.method, expected.method);
      assert.equal(request.url, expected.url);
      assert.equal(request.headers['content-type'], 'text/plain;charset=UTF-8');
      assert.equal(request.headers.authorization, undefined);
      assert.equal(request.headers.cookie, undefined);
      const chunks = [];
      let bytes = 0;
      for await (const chunk of request) {
        bytes += chunk.length;
        assert.ok(bytes <= 1024, 'Fixture request limit exceeded');
        chunks.push(chunk);
      }
      assert.deepEqual(Buffer.concat(chunks), Buffer.from(expected.body));
      response.writeHead(200, {'Content-Type': 'application/json'});
      response.end(JSON.stringify(expected.reply));
    })().catch(error => {
      fixtureError ||= error;
      if (!response.headersSent) response.writeHead(500);
      response.end();
    });
  });
  server.requestTimeout = 5000;
  server.headersTimeout = 5000;
  server.maxConnections = 1;
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const endpoint = `http://127.0.0.1:${server.address().port}`;
  const run = async (args, input, expectedExit = 0, errorCode) => {
    const result = await processResult(binary, ['--url', endpoint, '--timeout', '5s', ...args], input);
    assert.ifError(fixtureError);
    assert.equal(result.signal, null);
    assert.equal(result.code, expectedExit);
    if (expected) assert.equal(expected.consumed, true, 'Expected HTTP request missing');
    if (errorCode) {
      assert.equal(result.stdout.length, 0);
      assert.deepEqual(result.stderr, Buffer.from(JSON.stringify({error: {code: errorCode, operation: args[0]}}) + '\n'));
    } else {
      assert.equal(result.stderr.length, 0);
    }
    expected = undefined;
    return result.stdout;
  };
  const expect = (method, url, body, reply) => { expected = {method, url, body, reply, consumed: false}; };
  try {
    const identity = await processResult(binary, ['--version']);
    assert.equal(identity.code, 0);
    assert.equal(identity.signal, null);
    assert.equal(identity.stderr.length, 0);
    assert.match(identity.stdout.toString(), /^localpost-client [^\r\n]+\n$/);
    const checks = [];
    const payloads = [Buffer.from([0, 255, 13, 10, 128, 0, 1]), Buffer.alloc(0)];
    for (const [index, payload] of payloads.entries()) {
      const messageID = `${index + 1}-0`;
      const encoded = 'b64:' + payload.toString('base64');
      expect('PUT', `/XADD/${encodeURIComponent(key)}/%2A/data`, encoded, {XADD: messageID});
      const sent = await run(['send', id], payload);
      assert.deepEqual(JSON.parse(sent), {id, message_id: messageID, bytes: payload.length});
      assert.equal(sent.at(-1), 10);
      const query = ['XRANGE', key, messageID, messageID, 'COUNT', '1'].map(encodeURIComponent).join('/');
      expect('POST', '/', query, {XRANGE: [{id: messageID, msg: {data: encoded}}]});
      assert.deepEqual(await run(['get', id, messageID]), payload);
      checks.push(index === 0 ? 'binary-stdin-raw-stdout' : 'empty-stdin-empty-stdout');
    }
    const absentID = '99-0';
    expect('POST', '/', ['XRANGE', key, absentID, absentID, 'COUNT', '1'].map(encodeURIComponent).join('/'), {XRANGE: []});
    await run(['get', id, absentID], undefined, 3, 'message_not_found');
    await run(['send', 'invalid-id'], undefined, 2, 'invalid_argument');
    expect('PUT', `/XADD/${encodeURIComponent(key)}/%2A/data`, 'b64:', {XADD: [false, 'OOM fixture rejection']});
    await run(['send', id], Buffer.alloc(0), 1, 'memory_limit');
    assert.equal(requests, 6);
    checks.push('exit-0-empty-stderr', 'exit-1-memory-limit', 'exit-2-invalid-argument', 'exit-3-message-not-found');
    return {
      status: 'passed', scope: 'native-process-stdio-fixture', platform: process.platform, architecture: process.arch,
      binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex'),
      version: identity.stdout.toString().trim(), checks, requests,
    };
  } finally {
    server.closeAllConnections();
    await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  assert.ok(process.argv[2], 'Usage: node qualify-client-process.mjs <client-binary> [evidence-json]');
  const evidence = await qualifyClientProcess(path.resolve(process.argv[2]));
  if (process.argv[3]) writeFileSync(process.argv[3], JSON.stringify(evidence, null, 2) + '\n');
  console.log(JSON.stringify(evidence));
}
