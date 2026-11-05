import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { access, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { inspectPackage, parseArguments, runPublicationProbe } from './qualify-packaged-service.mjs';

const commit = '1'.repeat(40);
const id = '4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b';
const delivered = 'localpost_019a4fb0-1a00-7000-8000-000000000001.dat';
const binary = process.platform === 'win32' ? 'localpost.exe' : 'localpost';
const target = `${process.platform === 'win32' ? 'windows' : process.platform}/${process.arch === 'x64' ? 'amd64' : process.arch}`;
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');

async function fixture(t, host = process) {
  const directory = await realpath(await mkdtemp(path.join(os.tmpdir(), 'localpost-identity-test-')));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const packageTarget = `${host.platform === 'win32' ? 'windows' : host.platform}/${host.arch === 'x64' ? 'amd64' : host.arch}`;
  const architectureSettings = host.arch === 'arm64' ? { GOARM64: 'v8.0' } : { GOAMD64: 'v1' };
  const manifest = { version: '0.1.0', sourceCommit: commit, target: packageTarget, toolchain: 'go1.27.3', binarySHA256: hash('test binary'), settings: { CGO_ENABLED: '0', ...architectureSettings, trimpath: 'true', buildvcs: 'false' } };
  await writeFile(path.join(directory, host.platform === 'win32' ? 'localpost.exe' : 'localpost'), 'test binary');
  await writeFile(path.join(directory, 'openapi.json'), JSON.stringify({ info: { version: '0.1.0' } }));
  const save = () => writeFile(path.join(directory, 'build-info.json'), JSON.stringify(manifest));
  await save();
  return { directory, manifest, save };
}

test('requires explicit distinct options and a plain loopback origin', () => {
  const args = ['--package-dir', 'package', '--base-url', 'http://127.0.0.1:53147', '--expected-commit', commit, '--evidence', 'evidence.json'];
  assert.equal(parseArguments(args).expectedCommit, commit);
  assert.throws(() => parseArguments(args.slice(0, -2)));
  assert.throws(() => parseArguments([...args, '--evidence', 'other.json']));
  assert.throws(() => parseArguments(args.map((item) => item === 'http://127.0.0.1:53147' ? 'http://example.org' : item)));
});

test('validates exact packaged identity before running absolute binary with an empty PATH', async (t) => {
  const { directory } = await fixture(t);
  let called = false;
  const result = await inspectPackage({ packageDir: directory, expectedCommit: commit, run: async (command, args, options) => {
    called = true;
    assert.equal(command, path.join(directory, binary));
    assert.deepEqual(args, ['--version']);
    assert.equal(options.env.PATH, '');
    assert.ok(Object.entries(options.env).every(([key, value]) => key.toLowerCase() !== 'path' || value === ''));
    return { stdout: `localpost 0.1.0 (commit ${commit}; go1.27.3; ${target})\n`, stderr: '' };
  } });
  assert.equal(called, true);
  assert.equal(result.identity.binarySHA256, hash('test binary'));
});

test('rejects wrong manifest, mismatched bytes and linked package members before execution', async (t) => {
  const { directory, manifest, save } = await fixture(t);
  const options = { packageDir: directory, expectedCommit: commit, run: () => assert.fail('Invalid package executed') };
  manifest.sourceCommit = '2'.repeat(40);
  await save();
  await assert.rejects(inspectPackage(options));
  manifest.sourceCommit = commit;
  await save();
  await writeFile(path.join(directory, binary), 'changed binary');
  await assert.rejects(inspectPackage(options));
  await rm(path.join(directory, binary));
  // Directory links work without Windows developer-mode symlink privileges.
  await symlink(directory, path.join(directory, binary), process.platform === 'win32' ? 'junction' : 'dir');
  await assert.rejects(inspectPackage(options));
});

for (const host of [{ platform: 'win32', arch: 'x64' }, { platform: 'linux', arch: 'x64' }, { platform: 'linux', arch: 'arm64' }]) {
  test(`accepts native ${host.platform}/${host.arch} package identity`, async (t) => {
    const { directory, manifest } = await fixture(t, host);
    const result = await inspectPackage({ packageDir: directory, expectedCommit: commit, host, run: async (command) => {
      assert.equal(command, path.join(directory, host.platform === 'win32' ? 'localpost.exe' : 'localpost'));
      return { stdout: `localpost 0.1.0 (commit ${commit}; go1.27.3; ${manifest.target})\n`, stderr: '' };
    } });
    assert.equal(result.identity.target, manifest.target);
  });
}

test('rejects incompatible ARM64 targets and build settings before execution', async (t) => {
  const host = { platform: 'linux', arch: 'arm64' };
  const { directory, manifest, save } = await fixture(t, host);
  const original = structuredClone(manifest);
  const options = { packageDir: directory, expectedCommit: commit, host, run: () => assert.fail('Invalid package executed') };
  for (const change of [
    (value) => { value.target = 'linux/amd64'; },
    (value) => { value.target = 'windows/arm64'; },
    (value) => { value.settings.CGO_ENABLED = '1'; },
    (value) => { delete value.settings.GOARM64; },
    (value) => { value.settings.GOARM64 = 'v8.1'; },
    (value) => { value.settings.GOAMD64 = 'v1'; },
  ]) {
    Object.assign(manifest, structuredClone(original));
    change(manifest);
    await save();
    await assert.rejects(inspectPackage(options));
  }
  Object.assign(manifest, original);
  await save();
  await assert.rejects(inspectPackage({ ...options, host: { platform: 'win32', arch: 'arm64' } }), /Unsupported qualification host/);
  await assert.rejects(inspectPackage({ ...options, host: { platform: 'linux', arch: 'x64' } }));
});

test('rejects ARM64 tuning in an AMD64 package before execution', async (t) => {
  const host = { platform: 'linux', arch: 'x64' };
  const { directory, manifest, save } = await fixture(t, host);
  manifest.settings.GOARM64 = 'v8.0';
  await save();
  await assert.rejects(inspectPackage({ packageDir: directory, expectedCommit: commit, host, run: () => assert.fail('Invalid package executed') }));
});

async function fakeServer(t, { premature = false, cleanupFails = false } = {}) {
  const state = { directory: undefined, retired: 0 };
  const server = http.createServer(async (req, res) => {
    const json = (status, value) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
    if (req.url === '/registrations') {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      state.directory = JSON.parse(Buffer.concat(chunks)).directory;
      json(200, { id });
    } else if (req.method === 'DELETE') {
      state.retired++;
      json(cleanupFails ? 500 : 200, { id, unregistered: true, forced: false });
    } else {
      const temporary = path.join(state.directory, '.localpost-upload-test');
      const chunks = [];
      let ended = false;
      let writing = Promise.resolve();
      req.on('data', (chunk) => {
        chunks.push(chunk);
        writing = writing.then(async () => {
          await writeFile(temporary, Buffer.concat(chunks));
          if (premature) await writeFile(path.join(state.directory, delivered), Buffer.concat(chunks));
        });
      });
      req.on('end', async () => {
        ended = true;
        await writing;
        const bytes = Buffer.concat(chunks);
        await writeFile(path.join(state.directory, delivered), bytes);
        await rm(temporary, { force: true });
        json(200, { filename: delivered, bytes: bytes.length });
      });
      req.on('close', async () => {
        if (!ended) { await writing; await rm(temporary, { force: true }); }
      });
    }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  return { state, baseUrl: `http://127.0.0.1:${server.address().port}` };
}

test('observes in-progress bytes, exact completion and disconnect cleanup, then retires and removes owned inbox', async (t) => {
  const { state, baseUrl } = await fakeServer(t);
  const result = await runPublicationProbe({ baseUrl });
  assert.equal(result.partialUpload.finalAbsentUntilCompletion, true);
  assert.equal(result.disconnectedUpload.temporaryRemoved, true);
  assert.equal(result.retainedSentinel.unchanged, true);
  assert.equal(result.noClobber.existingDestinationRejected, true);
  assert.equal(result.cleanupComplete, true);
  assert.equal(state.retired, 1);
  await assert.rejects(access(state.directory), { code: 'ENOENT' });
});

test('premature publication fails but still retires owned registration and cleans directory', async (t) => {
  const { state, baseUrl } = await fakeServer(t, { premature: true });
  await assert.rejects(runPublicationProbe({ baseUrl }));
  assert.equal(state.retired, 1);
  await assert.rejects(access(state.directory), { code: 'ENOENT' });
});

test('failed retirement preserves owned inbox and returns private recovery context', async (t) => {
  const { state, baseUrl } = await fakeServer(t, { cleanupFails: true });
  let failure;
  try { await runPublicationProbe({ baseUrl }); } catch (error) { failure = error; }
  assert.ok(failure);
  assert.equal(failure.preservedDirectory, state.directory);
  assert.equal(failure.registrationId, id);
  assert.equal(Object.keys(failure).includes('preservedDirectory'), false);
  assert.ok((await readFile(path.join(state.directory, delivered))).length > 0);
  await rm(state.directory, { recursive: true, force: true });
});
