import assert from 'node:assert/strict';
import { access, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { checkContractInvariants, requestBounded, runServiceCheck, validateBaseUrl } from './check-openapi-service.mjs';

const document = JSON.parse(await readFile(new URL('../openapi.json', import.meta.url), 'utf8'));
const id = '4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b';
const cors = {
  'access-control-allow-origin': '*',
  'access-control-allow-headers': 'Content-Type',
  'access-control-allow-methods': 'POST, DELETE, OPTIONS',
  'content-type': 'application/json',
};
const reply = (status, value) => ({ status, headers: { ...cors }, body: value === undefined ? Buffer.alloc(0) : Buffer.from(JSON.stringify(value)) });

function fakeService({ failDelivery, registrationExtra = false, badCleanup = false } = {}) {
  const state = { directory: undefined, registrationKey: undefined, deleted: 0, posts: 0 };
  const request = async ({ url, method, body, headers }) => {
    const route = new URL(url).pathname;
    if (method === 'OPTIONS') return reply(204);
    if (method === 'GET') {
      const response = reply(405, { error: { code: 'method_not_allowed', message: 'Test response' } });
      response.headers.allow = route === `/registrations/${id}` ? 'DELETE' : 'POST';
      return response;
    }
    if (method === 'POST' && !headers['Content-Type']) return reply(415, { error: { code: 'unsupported_media_type', message: 'Test response' } });
    if (route === '/registrations' && body.toString() !== '{') {
      const registration = JSON.parse(body);
      state.registrationKey = Object.keys(registration).find((key) => key.toLowerCase() === 'directory');
      state.directory = registration[state.registrationKey];
      return reply(200, registrationExtra ? { id, port: 53147 } : { id });
    }
    if (method === 'DELETE') {
      state.deleted++;
      return reply(badCleanup ? 500 : 200, { id, unregistered: true, forced: false });
    }
    if (route.endsWith('/messages') && route.includes(id) && headers['Content-Type'] !== 'text/plain' && !state.deleted) {
      state.posts++;
      if (failDelivery) return failDelivery;
      const filename = `localpost_019a4fb0-1a00-7000-8000-${String(state.posts).padStart(12, '0')}.dat`;
      await writeFile(path.join(state.directory, filename), body);
      return reply(200, { filename, bytes: body.length });
    }
    let status = 400;
    let code = 'invalid_request';
    if (route.includes('not-a-uuid')) code = 'invalid_registration_id';
    else if (route === '/unknown') { status = 404; code = 'not_found'; }
    else if (state.deleted) { status = 404; code = 'registration_not_found'; }
    else if (headers['Content-Type'] === 'text/plain') { status = 415; code = 'unsupported_media_type'; }
    return reply(status, { error: { code, message: 'Test response' } });
  };
  return { state, request };
}

const run = (request) => runServiceCheck({ baseUrl: 'http://127.0.0.1:53147', document, request });
const removed = async (directory) => assert.rejects(access(directory), { code: 'ENOENT' });

test('requires an explicit plain loopback origin before making requests', async () => {
  for (const value of [undefined, 'http://example.org:53147', 'https://127.0.0.1:53147', 'http://user:password@127.0.0.1:53147', 'http://127.0.0.1:53147/path']) {
    assert.throws(() => validateBaseUrl(value));
  }
  assert.equal(validateBaseUrl('http://127.0.0.1:53147/'), 'http://127.0.0.1:53147');
});

test('completes the probe with byte-exact files and removes its registration and directory', async () => {
  const service = fakeService();
  const result = await run(service.request);
  assert.equal(result.deliveries, 3);
  assert.equal(service.state.posts, 3);
  assert.equal(service.state.registrationKey, 'DiReCtOrY');
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('malformed delivery JSON still retires the mailbox and removes its private directory', async () => {
  const service = fakeService({ failDelivery: { ...reply(200), body: Buffer.from('{') } });
  await assert.rejects(run(service.request), /valid JSON/);
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('unsafe receipt filenames never become filesystem paths', async () => {
  const service = fakeService({ failDelivery: reply(200, { filename: '../outside.dat', bytes: 6 }) });
  await assert.rejects(run(service.request));
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('unexpected registration fields do not leak the identifiable mailbox', async () => {
  const service = fakeService({ registrationExtra: true });
  await assert.rejects(run(service.request));
  assert.equal(service.state.deleted, 1);
  assert.equal(service.state.posts, 0);
  await removed(service.state.directory);
});

test('unconfirmed retirement preserves the inbox and private recovery context', async () => {
  const service = fakeService({ failDelivery: { ...reply(200), body: Buffer.from('{') }, badCleanup: true });
  try {
    await assert.rejects(run(service.request), (error) => {
      assert.match(error.message, /cleanup could not be confirmed/);
      assert.equal(error.preservedDirectory, service.state.directory);
      assert.equal(error.registrationId, id);
      assert.ok(!JSON.stringify(error).includes(service.state.directory));
      return true;
    });
    await access(service.state.directory);
  } finally {
    // The fake service owns no real mailbox; retire the test fixture explicitly.
    assert.equal(path.dirname(path.resolve(service.state.directory)), path.resolve(os.tmpdir()));
    assert.ok(path.basename(service.state.directory).startsWith('localpost-openapi-'));
    await rm(service.state.directory, { recursive: true, force: true });
  }
});

test('contract guards reject opaque-body schemas and authentication drift', () => {
  const changed = structuredClone(document);
  changed.paths['/registrations/{id}/messages'].post.requestBody.content['application/json'].schema = { type: 'object' };
  assert.throws(() => checkContractInvariants(changed));
  const authenticated = structuredClone(document);
  authenticated.paths['/registrations'].post.security = [{ bearer: [] }];
  assert.throws(() => checkContractInvariants(authenticated));
});

async function withServer(handler, action) {
  const server = http.createServer(handler);
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  try { await action(`http://127.0.0.1:${server.address().port}`); }
  finally {
    server.closeAllConnections();
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
}

test('transport rejects an oversized streamed response', async () => {
  await withServer((req, res) => { res.write('1234'); res.end('5678'); }, async (url) => {
    await assert.rejects(requestBounded({ url, method: 'POST', maxResponseBytes: 7 }), /byte limit/);
  });
});

test('transport deadline includes an unfinished response body', async () => {
  await withServer((req, res) => { res.writeHead(200); res.write('{'); }, async (url) => {
    await assert.rejects(requestBounded({ url, method: 'POST', timeoutMs: 50 }), /time limit/);
  });
});

test('transport returns redirects without contacting their target', async () => {
  let calls = 0;
  await withServer((req, res) => { calls++; res.writeHead(302, { Location: 'http://example.org/' }); res.end(); }, async (url) => {
    const response = await requestBounded({ url, method: 'POST' });
    assert.equal(response.status, 302);
    assert.equal(calls, 1);
  });
});

test('missing CORS headers fail the probe and retain enough identity for cleanup', async () => {
  const service = fakeService();
  const request = async (options) => {
    const response = await service.request(options);
    if (options.method === 'POST' && new URL(options.url).pathname === '/registrations') delete response.headers['access-control-allow-origin'];
    return response;
  };
  await assert.rejects(run(request), /access-control-allow-origin/);
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('a false byte count fails even when the filename and file are valid', async () => {
  const service = fakeService();
  const request = async (options) => {
    const response = await service.request(options);
    if (service.state.posts === 1 && options.method === 'POST' && new URL(options.url).pathname.endsWith('/messages')) {
      const receipt = JSON.parse(response.body);
      response.body = Buffer.from(JSON.stringify({ ...receipt, bytes: receipt.bytes + 1 }));
    }
    return response;
  };
  await assert.rejects(run(request));
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('preflight side effects are detected and cleaned up', async () => {
  const service = fakeService();
  const request = async (options) => {
    const response = await service.request(options);
    if (options.method === 'OPTIONS') await writeFile(path.join(service.state.directory, 'unexpected'), 'side effect');
    return response;
  };
  await assert.rejects(run(request), /changed files/);
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});


test('contract guards reject weakened mandatory response and request headers', () => {
  const mutations = [
    (copy) => { delete copy.components.headers['Access-Control-Allow-Origin'].required; },
    (copy) => { copy.components.headers['Access-Control-Allow-Headers'].required = false; },
    (copy) => { copy.components.headers['Access-Control-Allow-Methods'].schema.const = 'POST'; },
    (copy) => { delete copy.paths['/registrations'].post.responses['405'].headers.Allow.required; },
    (copy) => { copy.paths['/registrations/{id}'].delete.responses['405'].headers.Allow.schema.const = 'POST'; },
    (copy) => { delete copy.paths['/registrations'].post['x-required-request-headers']; },
    (copy) => { copy.paths['/registrations/{id}/messages'].post['x-required-request-headers'] = []; },
    (copy) => { delete copy.paths['/registrations'].post.responses['200'].headers['Access-Control-Allow-Origin']; },
  ];
  for (const mutate of mutations) {
    const copy = structuredClone(document);
    mutate(copy);
    assert.throws(() => checkContractInvariants(copy));
  }
});

test('missing Allow on a method rejection fails the live check', async () => {
  const service = fakeService();
  const request = async (options) => {
    const response = await service.request(options);
    if (options.method === 'GET') delete response.headers.allow;
    return response;
  };
  await assert.rejects(run(request), /Allow header/);
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});

test('accepting an empty upload without Content-Type fails the live check', async () => {
  const service = fakeService();
  const request = async (options) => {
    if (options.method === 'POST' && !options.headers['Content-Type'] && new URL(options.url).pathname.endsWith('/messages')) {
      return reply(200, {});
    }
    return service.request(options);
  };
  await assert.rejects(run(request), /HTTP status/);
  assert.equal(service.state.deleted, 1);
  await removed(service.state.directory);
});


test('registration input schema accepts case aliases but rejects missing and unknown fields', () => {
  const validate = checkContractInvariants(document);
  for (const key of ['directory', 'Directory', 'DiReCtOrY']) {
    assert.doesNotThrow(() => validate('#/components/schemas/RegistrationRequest', { [key]: '/example/inbox' }));
  }
  assert.throws(() => validate('#/components/schemas/RegistrationRequest', {}));
  assert.throws(() => validate('#/components/schemas/RegistrationRequest', { folder: '/example/inbox' }));
  assert.throws(() => validate('#/components/schemas/RegistrationRequest', { Directory: '/example/inbox', unexpected: true }));
});
