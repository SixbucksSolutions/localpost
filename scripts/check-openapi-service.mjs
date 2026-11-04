import assert from 'node:assert/strict';
import { readFile, mkdtemp, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createValueValidator, validateDocument } from './validate-openapi.mjs';

const schemaRef = (name) => `#/components/schemas/${name}`;
const uuid4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const filenamePattern = /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$/;

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
  const upload = document.paths['/registrations/{id}/messages'].post.requestBody;
  assert.equal(upload.required, false, 'Empty uploads are accepted');
  assert.equal(upload['x-byte-limit'], 10485760);
  assert.deepEqual(Object.keys(upload.content).sort(), ['application/json', 'application/octet-stream']);
  for (const media of Object.values(upload.content)) assert.equal(media.schema, undefined, 'Upload bytes must remain schema-free');
  assert.equal(document.paths['/registrations'].post.requestBody['x-byte-limit'], 65536);
  const validate = createValueValidator(document);
  for (const key of ['directory', 'Directory', 'DiReCtOrY']) {
    validate(schemaRef('RegistrationRequest'), { [key]: '/localpost-probe-directory' });
  }
  assert.throws(() => validate(schemaRef('RegistrationRequest'), {}));
  assert.throws(() => validate(schemaRef('RegistrationRequest'), { directory: '/localpost-probe-directory', unexpected: true }));
  const id = '4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b';
  const v7 = '019a4fb0-1a00-7000-8000-000000000001';
  validate(schemaRef('Registration'), { id });
  assert.throws(() => validate(schemaRef('Registration'), { id, port: 53147 }));
  assert.throws(() => validate(schemaRef('Registration'), { id: v7 }));
  validate(schemaRef('Receipt'), { filename: `localpost_${v7}.dat`, bytes: 10485760 });
  assert.throws(() => validate(schemaRef('Receipt'), { filename: `localpost_${id}.dat`, bytes: 0 }));
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

export async function runServiceCheck({ baseUrl, document, request = requestBounded, timeoutMs = 5000, maxResponseBytes = 65536 }) {
  const origin = validateBaseUrl(baseUrl);
  const validate = checkContractInvariants(document);
  const temporaryRoot = path.resolve(os.tmpdir());
  const directory = await mkdtemp(path.join(temporaryRoot, 'localpost-openapi-'));
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
  };
  const snapshot = async () => {
    const names = (await readdir(directory)).sort();
    return Promise.all(names.map(async (name) => [name, (await readFile(path.join(directory, name))).toString('hex')]));
  };
  try {
    mailboxCleanupConfirmed = false;
    const response = await send('/registrations', 'POST', Buffer.from(JSON.stringify({ DiReCtOrY: directory })), 'application/json');
    // Retain a safe identity before envelope checks so an extra response field does not leak our mailbox.
    try {
      const candidate = JSON.parse(response.body.toString('utf8')).id;
      if (typeof candidate === 'string' && uuid4.test(candidate)) registrationId = candidate;
    } catch { /* No trustworthy ID is available for cleanup. */ }
    const registration = checkResponse(response, 200, 'Registration', validate);
    assert.ok(uuid4.test(registration.id), 'Registration ID must be UUIDv4');
    registrationId = registration.id;
    const route = `/registrations/${registrationId}/messages`;
    const expectedFiles = [];
    for (const [body, type] of [
      [Buffer.from([0, 255, 13, 10, 128, 65]), 'application/octet-stream'],
      [Buffer.from('{not JSON\n\u0000'), 'application/json; charset=utf-8'],
      [Buffer.alloc(0), 'application/json'],
    ]) {
      const receipt = checkResponse(await send(route, 'POST', body, type), 200, 'Receipt', validate);
      assert.ok(filenamePattern.test(receipt.filename), 'Receipt filename must be a UUIDv7 basename');
      assert.equal(receipt.bytes, body.length);
      assert.deepEqual(await readFile(path.join(directory, receipt.filename)), body, 'Delivery bytes changed');
      expectedFiles.push([receipt.filename, body.toString('hex')]);
    }
    expectedFiles.sort(([a], [b]) => a.localeCompare(b));
    assert.deepEqual(await snapshot(), expectedFiles, 'Unexpected files after delivery');
    for (const preflight of ['/registrations', route, '/registrations/not-a-uuid/messages', '/unknown']) {
      checkResponse(await send(preflight, 'OPTIONS'), 204, undefined, validate);
    }
    await error('/registrations', 'POST', Buffer.from('{'), 'application/json', 400, 'invalid_request');
    await error('/registrations/not-a-uuid/messages', 'POST', Buffer.alloc(0), 'application/json', 400, 'invalid_registration_id');
    await error('/unknown', 'POST', Buffer.alloc(0), 'application/json', 404, 'not_found');
    await error(route, 'POST', Buffer.from('rejected'), 'text/plain', 415, 'unsupported_media_type');
    await error('/registrations', 'POST', Buffer.alloc(0), undefined, 415, 'unsupported_media_type');
    await error(route, 'POST', Buffer.alloc(0), undefined, 415, 'unsupported_media_type');
    await error('/registrations', 'GET', undefined, undefined, 405, 'method_not_allowed', 'POST');
    await error(`/registrations/${registrationId}`, 'GET', undefined, undefined, 405, 'method_not_allowed', 'DELETE');
    await error(route, 'GET', undefined, undefined, 405, 'method_not_allowed', 'POST');
    assert.deepEqual(await snapshot(), expectedFiles, 'Preflight or rejected requests changed files');
    const retired = checkResponse(await send(`/registrations/${registrationId}`, 'DELETE'), 200, 'Unregistration', validate);
    assert.equal(retired.id, registrationId);
    assert.equal(retired.forced, false);
    registrationId = undefined;
    mailboxCleanupConfirmed = true;
    await error(route, 'POST', Buffer.from('retired'), 'application/octet-stream', 404, 'registration_not_found');
    assert.deepEqual(await snapshot(), expectedFiles, 'Retired mailbox changed files');
  } catch (error) {
    failure = error;
  } finally {
    if (registrationId) {
      try {
        const response = await send(`/registrations/${registrationId}`, 'DELETE');
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
    if (mailboxCleanupConfirmed) {
      try {
        // Verify this exact mkdtemp result remains a direct child of the intended temporary root.
        assert.equal(path.dirname(path.resolve(directory)), temporaryRoot);
        assert.ok(path.basename(directory).startsWith('localpost-openapi-'));
        await rm(directory, { recursive: true, force: true });
      } catch { failure = new Error('Private probe directory cleanup failed', { cause: failure }); }
    } else {
      failure = new Error('Mailbox cleanup could not be confirmed; private probe directory preserved', { cause: failure });
      // Non-enumerable recovery context is available to an embedding caller, never printed by the CLI.
      Object.defineProperty(failure, 'preservedDirectory', { value: directory });
      Object.defineProperty(failure, 'registrationId', { value: registrationId });
    }
  }
  if (failure) throw failure;
  return { requests, deliveries: 3 };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    assert.equal(process.argv.length, 3, 'Usage: node scripts/check-openapi-service.mjs http://127.0.0.1:53147');
    const document = JSON.parse(await readFile(new URL('../openapi.json', import.meta.url), 'utf8'));
    const result = await runServiceCheck({ baseUrl: process.argv[2], document });
    console.log(`OpenAPI service check passed: ${result.deliveries} byte-exact deliveries; ${result.requests} bounded requests; cleanup complete.`);
  } catch {
    // Assertion diagnostics may contain private paths or payloads; keep CLI failures deliberately terse.
    console.error('OpenAPI service check failed. Verify the explicit loopback URL, contract, service behavior, and cleanup.');
    process.exitCode = 1;
  }
}
