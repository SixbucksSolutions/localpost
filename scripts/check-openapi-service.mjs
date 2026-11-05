import assert from 'node:assert/strict';
import { readFile, open, opendir, realpath, lstat, unlink, rmdir } from 'node:fs/promises';
import { constants } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createValueValidator, validateDocument } from './validate-openapi.mjs';

const apiPrefix = '/api/2026-11-05';
const schemaRef = (name) => `#/components/schemas/${name}`;
const uuid4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$(?![\s\S])/;
const filenamePattern = /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$(?![\s\S])/;

export function validateBaseUrl(value) {
  assert.equal(typeof value, 'string', 'An explicit loopback base URL is required');
  const url = new URL(value);
  assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1', 'Base URL must use HTTP on 127.0.0.1');
  assert.ok(!url.username && !url.password && !url.search && !url.hash && url.pathname === '/', 'Base URL must contain only the loopback origin');
  return url.origin;
}

export function checkContractInvariants(document) {
  validateDocument(document);
  assert.deepEqual(document.security, [], 'The shared service has no authentication');
  assert.equal(document.servers.length, 1, 'All routes share one server');
  assert.equal(document.servers[0].url, 'http://127.0.0.1:{port}');
  assert.equal(document.servers[0].variables.port.default, '53147');
  const resolve = (value) => value.$ref
    ? value.$ref.slice(2).split('/').reduce((node, part) => node[part.replaceAll('~1', '/').replaceAll('~0', '~')], document)
    : value;
  const cors = {
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Headers': 'Content-Type',
    'Access-Control-Allow-Methods': 'POST, DELETE, OPTIONS',
  };
  for (const item of Object.values(document.paths)) {
    assert.equal(item.servers, undefined, 'Path-specific servers would split the shared listener');
    for (const method of ['get', 'put', 'post', 'delete', 'options', 'patch', 'head', 'trace']) {
      if (!item[method]) continue;
      assert.equal(item[method].servers, undefined, 'Operation-specific servers would split the shared listener');
      if (item[method].security) assert.deepEqual(item[method].security, []);
      if (method === 'post') assert.deepEqual(item[method]['x-required-request-headers'], ['Content-Type']);
      for (const [status, entry] of Object.entries(item[method].responses)) {
        const response = resolve(entry);
        for (const [name, expected] of Object.entries(cors)) {
          assert.ok(response.headers?.[name], `Response ${status} must document ${name}`);
          const header = resolve(response.headers[name]);
          assert.equal(header.required, true, `${name} must be guaranteed`);
          assert.equal(header.schema.const, expected, `${name} must document its exact value`);
        }
        if (status === '405') {
          const allow = resolve(response.headers.Allow);
          assert.equal(allow.required, true, 'Allow must be guaranteed on 405');
          assert.equal(allow.schema.const, method.toUpperCase(), 'Allow must name the supported method');
        }
      }
    }
  }
  const versionError = document.components.responses.UnsupportedApiVersion;
  for (const [name, expected] of Object.entries(cors)) {
    const header = resolve(versionError.headers[name]);
    assert.equal(header.required, true, `${name} must be guaranteed on version errors`);
    assert.equal(header.schema.const, expected);
  }
  assert.equal(versionError.content['application/json'].schema.$ref, schemaRef('Error'));
  assert.equal(document.info.version, '0.2.0');
  assert.deepEqual(Object.keys(document.paths).sort(), [apiPrefix + '/registrations', apiPrefix + '/registrations/{id}', apiPrefix + '/registrations/{id}/messages']);
  const upload = document.paths[apiPrefix + '/registrations/{id}/messages'].post.requestBody;
  assert.equal(upload.required, false, 'Empty uploads are accepted');
  assert.equal(upload['x-byte-limit'], 10485760);
  assert.deepEqual(Object.keys(upload.content).sort(), ['application/json', 'application/octet-stream']);
  for (const media of Object.values(upload.content)) assert.equal(media.schema, undefined, 'Upload bytes must remain schema-free');
  assert.equal(document.paths[apiPrefix + '/registrations'].post.requestBody['x-byte-limit'], 65536);
  const validate = createValueValidator(document);
  validate(schemaRef('RegistrationRequest'), {});
  for (const key of ['directory', 'Directory', 'DiReCtOrY', 'extra']) {
    assert.throws(() => validate(schemaRef('RegistrationRequest'), { [key]: '/example' }));
  }
  for (const value of [null, [], '', 1]) assert.throws(() => validate(schemaRef('RegistrationRequest'), value));
  const id = '4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b';
  const v7 = '019a4fb0-1a00-7000-8000-000000000001';
  validate(schemaRef('Registration'), { id, directory: `/mailboxes/${id}` });
  assert.throws(() => validate(schemaRef('Registration'), { id, directory: `/mailboxes/${id}`, port: 53147 }));
  assert.throws(() => validate(schemaRef('Registration'), { id: v7, directory: `/mailboxes/${v7}` }));
  for (const directory of [`relative/${id}`, `/mailboxes/${v7}`, `/mailboxes/${id}\n`]) {
    assert.throws(() => validate(schemaRef('Registration'), { id, directory }));
  }
  validate(schemaRef('Registration'), { id, directory: `C:\\mailboxes\\${id}` });
  validate(schemaRef('Receipt'), { filename: `localpost_${v7}.dat`, bytes: 10485760 });
  assert.throws(() => validate(schemaRef('Receipt'), { filename: `localpost_${id}.dat`, bytes: 0 }));
  assert.throws(() => validate(schemaRef('Receipt'), { filename: `localpost_${v7}.dat\n`, bytes: 0 }));
  assert.throws(() => validate(schemaRef('Receipt'), { filename: `localpost_${v7}.dat`, bytes: 10485761 }));
  return validate;
}

// Native HTTP deliberately follows no redirects and bounds the entire response lifetime and body.
export function requestBounded({ url, method, headers = {}, body, timeoutMs = 5000, maxResponseBytes = 65536 }) {
  assert.ok(Number.isInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= 60000, 'Invalid HTTP timeout');
  assert.ok(Number.isInteger(maxResponseBytes) && maxResponseBytes > 0 && maxResponseBytes <= 1048576, 'Invalid response bound');
  const target = new URL(url);
  validateBaseUrl(target.origin);
  return new Promise((resolve, reject) => {
    let timer;
    const request = http.request(target, { method, headers }, (response) => {
      const chunks = [];
      let size = 0;
      response.on('data', (chunk) => {
        size += chunk.length;
        if (size > maxResponseBytes) {
          const error = new Error('HTTP response exceeded byte limit');
          clearTimeout(timer);
          reject(error);
          response.destroy(error);
          request.destroy(error);
          return;
        }
        chunks.push(chunk);
      });
      response.on('error', (error) => {
        clearTimeout(timer);
        request.destroy();
        reject(error);
      });
      response.on('end', () => {
        clearTimeout(timer);
        resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) });
      });
    });
    request.on('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });
    timer = setTimeout(() => request.destroy(new Error('HTTP request exceeded time limit')), timeoutMs);
    request.end(body);
  });
}

function checkResponse(response, status, schema, validate) {
  assert.equal(response.status, status, 'Unexpected HTTP status');
  for (const [header, expected] of Object.entries({
    'access-control-allow-origin': '*',
    'access-control-allow-headers': 'Content-Type',
    'access-control-allow-methods': 'POST, DELETE, OPTIONS',
  })) assert.equal(response.headers[header], expected, `Unexpected ${header}`);
  assert.equal(response.headers['access-control-allow-credentials'], undefined);
  if (!schema) {
    assert.equal(response.body.length, 0, 'Preflight must have an empty body');
    return undefined;
  }
  assert.match(response.headers['content-type'] ?? '', /^application\/json(?:\s*;|$)/i);
  let value;
  try { value = JSON.parse(response.body.toString('utf8')); }
  catch { throw new Error('Response is not valid JSON'); }
  validate(schemaRef(schema), value);
  return value;
}

// Read at most the caller limit plus one sentinel byte from a stable regular-file handle.
export async function readRegularFileBounded(file, maxBytes) {
  assert.ok(Number.isInteger(maxBytes) && maxBytes >= 0 && maxBytes <= 10485760, 'Invalid probe file byte bound');
  const before = await lstat(file, { bigint: true });
  assert.ok(before.isFile() && !before.isSymbolicLink(), 'Unexpected probe file');
  const flags = constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0);
  const handle = await open(file, flags);
  try {
    const opened = await handle.stat({ bigint: true });
    assert.ok(opened.isFile(), 'Opened probe file must be regular');
    const confirmIdentity = async () => {
      const current = await lstat(file, { bigint: true });
      assert.ok(current.isFile() && !current.isSymbolicLink(), 'Probe file path changed');
      for (const stat of [before, current]) {
        assert.equal(stat.dev, opened.dev, 'Probe file device changed');
        assert.equal(stat.ino, opened.ino, 'Probe file identity changed');
      }
    };
    await confirmIdentity();
    const buffer = Buffer.alloc(maxBytes + 1);
    let total = 0;
    while (total < buffer.length) {
      const { bytesRead } = await handle.read(buffer, total, buffer.length - total, total);
      if (bytesRead === 0) break;
      total += bytesRead;
    }
    assert.ok(total <= maxBytes, 'Probe file exceeded byte limit');
    await confirmIdentity();
    return buffer.subarray(0, total);
  } finally {
    await handle.close();
  }
}

async function boundedEntries(directory, limit) {
  const entries = [];
  const handle = await opendir(directory);
  for await (const entry of handle) {
    assert.ok(entries.length < limit, 'Probe directory entry limit exceeded');
    entries.push(entry.name);
  }
  return entries;
}

export async function runServiceCheck({ baseUrl, document, mailboxRoot, request = requestBounded, timeoutMs = 5000, maxResponseBytes = 65536 }) {
  const origin = validateBaseUrl(baseUrl);
  const validate = checkContractInvariants(document);
  assert.ok(typeof mailboxRoot === 'string' && path.isAbsolute(mailboxRoot), 'An explicit absolute mailbox root is required');
  const root = await realpath(mailboxRoot);
  assert.equal(path.resolve(mailboxRoot), root, 'Mailbox root must be canonical');
  assert.ok((await lstat(root)).isDirectory(), 'Mailbox root must be an existing directory');
  const existing = new Set(await boundedEntries(root, 4096));
  let directory;
  let registrationId;
  let mailboxCleanupConfirmed = true;
  let failure;
  let requests = 0;
  const send = async (route, method, body, type) => {
    requests += 1;
    return request({ url: origin + route, method, body, headers: type ? { 'Content-Type': type } : {}, timeoutMs, maxResponseBytes });
  };
  const error = async (route, method, body, type, status, code, allow) => {
    const response = await send(route, method, body, type);
    const value = checkResponse(response, status, 'Error', validate);
    if (allow) assert.equal(response.headers.allow, allow, 'Unexpected Allow header');
    assert.equal(value.error.code, code, 'Unexpected stable error code');
    if (code === 'unsupported_api_version') assert.ok(value.error.message.includes(apiPrefix), 'Version error must identify the supported prefix');
  };
  const confirmDirectory = async () => {
    assert.ok(directory && registrationId && !existing.has(registrationId), 'Cannot access a preexisting or unidentified mailbox');
    assert.equal(directory, path.join(root, registrationId), 'Mailbox must be a direct child of the expected root');
    assert.equal(await realpath(root), root, 'Mailbox root changed');
    const stat = await lstat(directory);
    assert.ok(stat.isDirectory() && !stat.isSymbolicLink(), 'Mailbox must be a real directory');
    assert.equal(await realpath(directory), directory, 'Mailbox path changed');
  };
  const snapshot = async (expectedFiles) => {
    await confirmDirectory();
    const names = (await boundedEntries(directory, 16)).sort();
    assert.deepEqual(names, expectedFiles.map(([name]) => name), 'Probe directory changed files');
    return Promise.all(expectedFiles.map(async ([name, hex]) => {
      const bytes = await readRegularFileBounded(path.join(directory, name), hex.length / 2);
      return [name, bytes.toString('hex')];
    }));
  };
  try {
    mailboxCleanupConfirmed = false;
    const response = await send(apiPrefix + '/registrations', 'POST', Buffer.from('{}'), 'application/json');
    // Retain only the identity and derived path for cleanup, never a server-selected path.
    try {
      const candidate = JSON.parse(response.body.toString('utf8'));
      if (typeof candidate.id === 'string' && uuid4.test(candidate.id) && !existing.has(candidate.id)) {
        registrationId = candidate.id;
        const expected = path.join(root, registrationId);
        if (candidate.directory === expected) directory = expected;
      }
    } catch { /* No trustworthy identity is available for cleanup. */ }
    const registration = checkResponse(response, 200, 'Registration', validate);
    assert.ok(uuid4.test(registration.id), 'Registration ID must be UUIDv4');
    assert.equal(registration.directory, path.join(root, registration.id), 'Returned mailbox path must match the configured root and UUID');
    await confirmDirectory();
    const route = `${apiPrefix}/registrations/${registrationId}/messages`;
    const expectedFiles = [];
    for (const [body, type] of [
      [Buffer.from([0, 255, 13, 10, 128, 65]), 'application/octet-stream'],
      [Buffer.from('{not JSON\n\u0000'), 'application/json; charset=utf-8'],
      [Buffer.alloc(0), 'application/json'],
    ]) {
      const receipt = checkResponse(await send(route, 'POST', body, type), 200, 'Receipt', validate);
      assert.ok(filenamePattern.test(receipt.filename), 'Receipt filename must be a UUIDv7 basename');
      assert.equal(receipt.bytes, body.length);
      await confirmDirectory();
      const file = path.join(directory, receipt.filename);
      assert.deepEqual(await readRegularFileBounded(file, body.length), body, 'Delivery bytes changed');
      expectedFiles.push([receipt.filename, body.toString('hex')]);
    }
    expectedFiles.sort(([a], [b]) => a.localeCompare(b));
    assert.deepEqual(await snapshot(expectedFiles), expectedFiles, 'Unexpected files after delivery');
    for (const preflight of [apiPrefix + '/registrations', route, apiPrefix + '/registrations/not-a-uuid/messages', apiPrefix + '/unknown']) {
      checkResponse(await send(preflight, 'OPTIONS'), 204, undefined, validate);
    }
    for (const retiredPath of ['/registrations', `/registrations/${registrationId}/messages`, '/api/2026-01-01/registrations']) {
      for (const method of ['POST', 'OPTIONS']) await error(retiredPath, method, Buffer.from('{}'), 'application/json', 404, 'unsupported_api_version');
    }
    for (const invalid of ['{', '', 'null', '[]', '{} {}', '{"directory":"/old/inbox"}']) {
      await error(apiPrefix + '/registrations', 'POST', Buffer.from(invalid), 'application/json', 400, 'invalid_request');
    }
    await error(apiPrefix + '/registrations/not-a-uuid/messages', 'POST', Buffer.alloc(0), 'application/json', 400, 'invalid_registration_id');
    await error('/unknown', 'OPTIONS', undefined, undefined, 404, 'not_found');
    await error('/unknown', 'POST', Buffer.alloc(0), 'application/json', 404, 'not_found');
    await error(route, 'POST', Buffer.from('rejected'), 'text/plain', 415, 'unsupported_media_type');
    await error(apiPrefix + '/registrations', 'POST', Buffer.alloc(0), undefined, 415, 'unsupported_media_type');
    await error(route, 'POST', Buffer.alloc(0), undefined, 415, 'unsupported_media_type');
    await error(apiPrefix + '/registrations', 'GET', undefined, undefined, 405, 'method_not_allowed', 'POST');
    await error(`${apiPrefix}/registrations/${registrationId}`, 'GET', undefined, undefined, 405, 'method_not_allowed', 'DELETE');
    await error(route, 'GET', undefined, undefined, 405, 'method_not_allowed', 'POST');
    assert.deepEqual(await snapshot(expectedFiles), expectedFiles, 'Preflight or rejected requests changed files');
    const retired = checkResponse(await send(`${apiPrefix}/registrations/${registrationId}`, 'DELETE'), 200, 'Unregistration', validate);
    assert.equal(retired.id, registrationId);
    assert.equal(retired.forced, false);
    mailboxCleanupConfirmed = true;
    await error(route, 'POST', Buffer.from('retired'), 'application/octet-stream', 404, 'registration_not_found');
    assert.deepEqual(await snapshot(expectedFiles), expectedFiles, 'Retired mailbox changed files');
  } catch (error) {
    failure = error;
  } finally {
    if (registrationId && !mailboxCleanupConfirmed) {
      try {
        const response = await send(`${apiPrefix}/registrations/${registrationId}`, 'DELETE');
        if (response.status !== 404) {
          const retired = checkResponse(response, 200, 'Unregistration', validate);
          assert.equal(retired.id, registrationId);
        } else {
          const value = checkResponse(response, 404, 'Error', validate);
          assert.equal(value.error.code, 'registration_not_found');
        }
        mailboxCleanupConfirmed = true;
      } catch { failure = new Error('Mailbox cleanup could not be confirmed', { cause: failure }); }
    }
    if (mailboxCleanupConfirmed && directory) {
      try {
        await confirmDirectory();
        const entries = await boundedEntries(directory, 16);
        // Only remove direct regular files from our newly allocated mailbox. Never recurse.
        for (const name of entries) {
          const stat = await lstat(path.join(directory, name));
          assert.ok(stat.isFile() && !stat.isSymbolicLink(), 'Unexpected cleanup entry');
        }
        for (const name of entries) await unlink(path.join(directory, name));
        await rmdir(directory);
        directory = undefined;
      } catch { failure = new Error('Private probe directory cleanup failed', { cause: failure }); }
    } else if (!mailboxCleanupConfirmed || registrationId) {
      failure = new Error('Mailbox cleanup could not be confirmed; private probe directory preserved', { cause: failure });
    }
    if (failure) {
      // Private recovery context stays out of CLI output and enumerable diagnostics.
      Object.defineProperty(failure, 'preservedDirectory', { value: directory });
      Object.defineProperty(failure, 'registrationId', { value: registrationId });
    }
  }
  if (failure) throw failure;
  return { requests, deliveries: 3 };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    assert.equal(process.argv.length, 4, 'Usage: node scripts/check-openapi-service.mjs http://127.0.0.1:53147 ABSOLUTE_CANONICAL_MAILBOX_ROOT');
    const document = JSON.parse(await readFile(new URL('../openapi.json', import.meta.url), 'utf8'));
    const result = await runServiceCheck({ baseUrl: process.argv[2], mailboxRoot: process.argv[3], document });
    console.log(`OpenAPI service check passed: ${result.deliveries} byte-exact deliveries; ${result.requests} bounded requests; cleanup complete.`);
  } catch {
    console.error('OpenAPI service check failed. Verify the explicit loopback URL, canonical mailbox root, contract, service behavior, and cleanup.');
    process.exitCode = 1;
  }
}
