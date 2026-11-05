import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rm, statfs, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { readRegularFileBounded, requestBounded, runServiceCheck, validateBaseUrl } from './check-openapi-service.mjs';

const execute = promisify(execFile);
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const filename = /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$/;
const timeoutMs = 5000;
const apiPrefix = '/api/2026-11-05';

export function parseArguments(args) {
  const values = {};
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    assert.ok(['--package-dir', '--base-url', '--expected-commit', '--evidence', '--mailbox-root'].includes(key), 'Unknown option');
    assert.ok(!Object.hasOwn(values, key) && args[index + 1] && !args[index + 1].startsWith('--'), 'Missing or repeated option');
    values[key] = args[index + 1];
  }
  assert.equal(Object.keys(values).length, 5, 'All five options are required');
  validateBaseUrl(values['--base-url']);
  assert.match(values['--expected-commit'], /^[0-9a-f]{40}$/);
  return { packageDir: values['--package-dir'], baseUrl: values['--base-url'], expectedCommit: values['--expected-commit'], evidence: values['--evidence'], mailboxRoot: values['--mailbox-root'] };
}

// Reject links and require each executable/identity file to be a direct package child.
export async function inspectPackage({ packageDir, expectedCommit, run = execute, host = process }) {
  assert.match(expectedCommit, /^[0-9a-f]{40}$/);
  const root = await realpath(packageDir);
  const read = async (name) => {
    const target = path.join(root, name);
    assert.ok((await lstat(target)).isFile(), 'Package member must be a regular file');
    assert.equal(path.dirname(await realpath(target)), root, 'Package member escaped package');
    return readFile(target);
  };
  const manifestBytes = await read('build-info.json');
  const contractBytes = await read('openapi.json');
  const manifest = JSON.parse(manifestBytes);
  const document = JSON.parse(contractBytes);
  const target = `${host.platform === 'win32' ? 'windows' : host.platform}/${host.arch === 'x64' ? 'amd64' : host.arch}`;
  assert.ok(['windows/amd64', 'linux/amd64', 'linux/arm64'].includes(target), 'Unsupported qualification host');
  assert.equal(manifest.version, '0.2.1');
  assert.equal(manifest.sourceCommit, expectedCommit);
  assert.equal(manifest.target, target);
  assert.equal(manifest.toolchain, 'go1.27.3');
  assert.equal(manifest.settings?.CGO_ENABLED, '0');
  assert.equal(manifest.settings?.GOAMD64, host.arch === 'arm64' ? undefined : 'v1');
  assert.equal(manifest.settings?.GOARM64, host.arch === 'arm64' ? 'v8.0' : undefined);
  assert.equal(manifest.settings?.trimpath, 'true');
  assert.equal(manifest.settings?.buildvcs, 'false');
  assert.equal(document.info.version, '0.2.1');
  const binaryName = host.platform === 'win32' ? 'localpost.exe' : 'localpost';
  const binaryBytes = await read(binaryName);
  assert.match(manifest.binarySHA256, /^[0-9a-f]{64}$/);
  assert.equal(digest(binaryBytes), manifest.binarySHA256, 'Packaged binary hash mismatch');
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'));
  env.PATH = '';
  const result = await run(path.join(root, binaryName), ['--version'], { env, cwd: root, timeout: timeoutMs, maxBuffer: 4096, windowsHide: true });
  assert.equal(result.stderr, '', 'Version command wrote stderr');
  assert.equal(result.stdout.trim(), `localpost 0.2.1 (commit ${expectedCommit}; go1.27.3; ${manifest.target})`);
  return { document, identity: { version: manifest.version, sourceCommit: expectedCommit, target: manifest.target, toolchain: manifest.toolchain, binarySHA256: manifest.binarySHA256, manifestSHA256: digest(manifestBytes), openapiSHA256: digest(contractBytes), versionWithEmptyPath: true } };
}

async function waitFor(predicate) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error('Filesystem observation timed out');
}

// Keep the body deliberately incomplete until the caller inspects the active temp file.
export async function partialUpload({ url, prefix, tail, observe, disconnect = false, requestTimeoutMs = timeoutMs }) {
  assert.ok(Number.isInteger(requestTimeoutMs) && requestTimeoutMs > 0 && requestTimeoutMs < 30000, 'Invalid partial upload timeout');
  const target = new URL(url);
  validateBaseUrl(target.origin);
  let timer;
  let req;
  const completion = new Promise((resolve, reject) => {
    req = http.request(target, { method: 'POST', headers: { 'Content-Type': 'application/octet-stream', 'Content-Length': prefix.length + tail.length } }, (res) => {
      const chunks = [];
      let count = 0;
      res.on('data', (chunk) => {
        count += chunk.length;
        if (count > 65536) req.destroy(new Error('Response too large'));
        else chunks.push(chunk);
      });
      res.on('error', reject);
      res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks) }));
    });
    req.on('error', reject);
    timer = setTimeout(() => req.destroy(new Error('Partial upload timed out')), requestTimeoutMs);
    req.write(prefix);
  });
  // Attach rejection handling immediately while filesystem observations run.
  completion.catch(() => {});
  try {
    await observe();
    if (disconnect) {
      req.destroy();
      await completion.catch(() => {});
      return;
    }
    req.end(tail);
    return await completion;
  } finally {
    clearTimeout(timer);
    req.destroy();
  }
}

export async function runPublicationProbe({ baseUrl, mailboxRoot, request = requestBounded, upload = partialUpload }) {
  const origin = validateBaseUrl(baseUrl);
  assert.ok(typeof mailboxRoot === 'string' && path.isAbsolute(mailboxRoot), 'An absolute mailbox root is required');
  const root = await realpath(mailboxRoot);
  assert.ok((await lstat(root)).isDirectory(), 'Mailbox root must be a directory');
  let directory;
  const sentinel = Buffer.from([0, 255, 4, 13, 10, 128]);
  const sentinelName = 'retained-sentinel.dat';
  const prefix = Buffer.alloc(65536, 0xa5);
  const tail = Buffer.from([0, 128, 255, 13, 10]);
  let registrationId;
  let cleanupConfirmed = true;
  let failure;
  let result;
  let requests = 0;
  const send = async (route, method, body) => {
    requests++;
    return request({ url: origin + apiPrefix + route, method, body, headers: body ? { 'Content-Type': 'application/json' } : {}, timeoutMs });
  };
  const names = async () => (await readdir(directory)).sort();
  const temporaryNames = async () => (await names()).filter((name) => name.startsWith('.localpost-upload-'));
  try {
    cleanupConfirmed = false;
    const registration = await send('/registrations', 'POST', Buffer.from('{}'));
    let value;
    try { value = JSON.parse(registration.body); } catch { throw new Error('Invalid registration response'); }
    if (typeof value.id === 'string' && uuid.test(value.id)) registrationId = value.id;
    assert.equal(registration.status, 200);
    assert.ok(registrationId, 'Missing safe registration identity');
    const expectedDirectory = path.join(root, registrationId);
    assert.equal(value.directory, expectedDirectory, 'Mailbox escaped configured root');
    assert.ok((await lstat(expectedDirectory)).isDirectory(), 'Mailbox must be a real directory');
    assert.equal(await realpath(expectedDirectory), expectedDirectory, 'Mailbox resolved outside configured root');
    directory = expectedDirectory;
    await writeFile(path.join(directory, sentinelName), sentinel, { flag: 'wx' });
    const fs = await statfs(directory);
    const source = path.join(directory, 'collision-source');
    await writeFile(source, 'collision probe', { flag: 'wx' });
    await assert.rejects(link(source, path.join(directory, sentinelName)), { code: 'EEXIST' });
    assert.deepEqual(await readRegularFileBounded(path.join(directory, sentinelName), sentinel.length), sentinel);
    await rm(source);
    const published = [sentinelName];
    const observe = async () => {
      let stagingName;
      await waitFor(async () => {
        const temporary = await temporaryNames();
        assert.ok(temporary.length <= 1, 'More than one active upload file');
        if (temporary.length === 0) return false;
        stagingName = temporary[0];
        assert.match(stagingName, /^\.localpost-upload-.+\.dat$/, 'Active upload must use a hidden .dat file');
        const staging = await lstat(path.join(directory, stagingName));
        assert.ok(staging.isFile(), 'Active upload must be a regular file');
        return staging.size > 0;
      });
      assert.deepEqual(await names(), [...published, stagingName].sort(), 'Unexpected file during active upload');
      assert.deepEqual(await readRegularFileBounded(path.join(directory, sentinelName), sentinel.length), sentinel);
    };
    requests++;
    const response = await upload({ url: `${origin}${apiPrefix}/registrations/${registrationId}/messages`, prefix, tail, observe });
    assert.equal(response.status, 200);
    const receipt = JSON.parse(response.body);
    assert.match(receipt.filename, filename);
    assert.equal(receipt.bytes, prefix.length + tail.length);
    const complete = await readRegularFileBounded(path.join(directory, receipt.filename), prefix.length + tail.length);
    assert.deepEqual(complete, Buffer.concat([prefix, tail]), 'Complete delivery bytes changed');
    published.push(receipt.filename);
    assert.deepEqual(await temporaryNames(), []);
    requests++;
    await upload({ url: `${origin}${apiPrefix}/registrations/${registrationId}/messages`, prefix, tail, observe, disconnect: true });
    await waitFor(async () => (await temporaryNames()).length === 0);
    assert.deepEqual(await names(), [receipt.filename, sentinelName].sort(), 'Disconnected upload published a file');
    assert.deepEqual(await readRegularFileBounded(path.join(directory, sentinelName), sentinel.length), sentinel);
    assert.deepEqual(await readRegularFileBounded(path.join(directory, receipt.filename), prefix.length + tail.length), complete);
    result = { filesystemType: String(fs.type), partialUpload: { temporaryObserved: true, finalAbsentUntilCompletion: true, bytes: complete.length, sha256: digest(complete) }, disconnectedUpload: { temporaryRemoved: true, finalAbsent: true }, retainedSentinel: { unchanged: true, sha256: digest(sentinel) }, noClobber: { scope: 'OS hard-link primitive (not a forced daemon filename collision)', existingDestinationRejected: true }, requests };
  } catch (error) { failure = error; }
  finally {
    if (registrationId) {
      try {
        const response = await send(`/registrations/${registrationId}`, 'DELETE');
        const value = JSON.parse(response.body);
        assert.ok((response.status === 200 && value.id === registrationId && value.unregistered === true && value.forced === false) || (response.status === 404 && value.error?.code === 'registration_not_found'));
        cleanupConfirmed = true;
      } catch { failure = new Error('Owned registration cleanup failed', { cause: failure }); }
    }
    if (cleanupConfirmed && directory) {
      assert.equal(path.dirname(directory), root);
      assert.equal(path.basename(directory), registrationId);
      assert.equal(await realpath(directory), directory);
      await rm(directory, { recursive: true, force: true });
    } else if (!cleanupConfirmed) {
      failure = new Error('Owned registration cleanup unconfirmed; probe directory retained', { cause: failure });
      Object.defineProperty(failure, 'preservedDirectory', { value: directory });
      Object.defineProperty(failure, 'registrationId', { value: registrationId });
    }
  }
  if (failure) throw failure;
  return { ...result, requests, cleanupComplete: true };
}

// Use only a newly allocated private root. Startup deliberately destroys its contents.
export async function runStartupProbe({ packageDir }) {
  const owned = await realpath(await mkdtemp(path.join(os.tmpdir(), 'localpost-startup-')));
  const root = path.join(owned, 'mailboxes');
  await mkdir(root);
  const executable = path.join(await realpath(packageDir), process.platform === 'win32' ? 'localpost.exe' : 'localpost');
  const children = [];
  const freePort = () => new Promise((resolve, reject) => {
    const server = net.createServer();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
  const launch = (port) => {
    const child = spawn(executable, ['--mailbox-root', root, '--port', String(port)], { windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    const state = { child, output: '', exited: false };
    const capture = (chunk) => {
      state.output = (state.output + chunk.toString()).slice(0, 32769);
      if (state.output.length > 32768) child.kill('SIGKILL');
    };
    child.stdout.on('data', capture);
    child.stderr.on('data', capture);
    state.exit = new Promise((resolve, reject) => {
      child.once('error', (error) => { state.exited = true; reject(error); });
      child.once('exit', (code, signal) => { state.exited = true; resolve({ code, signal }); });
    });
    state.exit.catch(() => {});
    children.push(state);
    return state;
  };
  const stop = async (state, signal) => {
    state.child.kill(signal);
    await waitFor(() => state.exited);
    return state.exit;
  };
  const ready = async (state, port) => {
    await waitFor(async () => {
      assert.equal(state.exited, false, 'Packaged process exited before readiness');
      try { await requestBounded({ url: `http://127.0.0.1:${port}/`, method: 'GET', timeoutMs: 100 }); }
      catch { return false; }
      assert.deepEqual(await readdir(root), [], 'Listener accepted requests before startup cleanup completed');
      return true;
    });
    await waitFor(() => state.output.includes('localpost listening'));
    assert.match(state.output, /WARNING/);
    assert.ok(state.output.indexOf('WARNING') < state.output.indexOf('localpost listening'), 'Warning must precede readiness');
  };
  const stale = async () => {
    const directory = path.join(root, 'stale-directory', 'nested');
    await mkdir(directory, { recursive: true });
    await writeFile(path.join(directory, 'stale.dat'), Buffer.from([0, 255, 128]));
    await writeFile(path.join(root, 'stray-file'), 'stale bytes');
  };
  try {
    const port = await freePort();
    await stale();
    let active = launch(port);
    await ready(active, port);
    const response = await requestBounded({ url: `http://127.0.0.1:${port}${apiPrefix}/registrations`, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: Buffer.from('{}') });
    assert.equal(response.status, 200);
    const registration = JSON.parse(response.body);
    assert.match(registration.id, uuid);
    assert.equal(registration.directory, path.join(root, registration.id));
    const retained = path.join(root, registration.id, 'live.dat');
    const bytes = Buffer.from([0, 255, 13, 10]);
    await writeFile(retained, bytes);
    await stop(active, 'SIGKILL');
    assert.deepEqual(await readFile(retained), bytes, 'Crash unexpectedly erased retained bytes');
    await stale();
    active = launch(port);
    await ready(active, port);
    const obsolete = await requestBounded({ url: `http://127.0.0.1:${port}${apiPrefix}/registrations/${registration.id}/messages`, method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: bytes });
    assert.equal(obsolete.status, 404);
    assert.equal(JSON.parse(obsolete.body).error.code, 'registration_not_found');
    await stale();
    const graceful = process.platform !== 'win32';
    const stopped = await stop(active, graceful ? 'SIGINT' : 'SIGKILL');
    if (graceful) assert.equal(stopped.code, 0, 'Normal shutdown failed');
    assert.deepEqual(await readFile(path.join(root, 'stray-file')), Buffer.from('stale bytes'), 'Shutdown should retain files until next startup');
    active = launch(port);
    await ready(active, port);
    await stop(active, 'SIGKILL');
    return { warningBeforeReadiness: true, rootEmptyAtFirstReadiness: true, crashRestartPurged: true, oldRegistrationInvalidated: true, normalRestartPurged: graceful, windowsTerminationRestartPurged: !graceful };
  } finally {
    for (const state of children) if (!state.exited) await stop(state, 'SIGKILL');
    assert.equal(path.dirname(root), owned);
    assert.equal(await realpath(owned), owned);
    await rm(owned, { recursive: true, force: true });
  }
}

export async function qualifyPackage(options) {
  const { document, identity } = await inspectPackage(options);
  const contract = await runServiceCheck({ baseUrl: options.baseUrl, document, mailboxRoot: options.mailboxRoot });
  const publication = await runPublicationProbe({ baseUrl: options.baseUrl, mailboxRoot: options.mailboxRoot });
  const startup = await runStartupProbe(options);
  return { schemaVersion: 1, passed: true, host: { platform: process.platform, architecture: process.arch, release: os.release(), version: os.version() }, identity, contract, publication, startup, lifecycle: 'Caller must independently verify the primary packaged-daemon port ownership and platform shutdown.' };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseArguments(process.argv.slice(2));
    const result = await qualifyPackage(options);
    await writeFile(path.resolve(options.evidence), JSON.stringify(result, null, 2) + '\n', { flag: 'wx' });
    console.log('Packaged service qualification passed; private JSON evidence written.');
  } catch {
    console.error('Packaged service qualification failed. Check package identity, explicit loopback service, filesystem behavior and owned-registration cleanup.');
    process.exitCode = 1;
  }
}
