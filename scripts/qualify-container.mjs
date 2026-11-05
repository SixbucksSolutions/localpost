import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, statfs, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import { qualifyPackage, partialUpload } from './qualify-packaged-service.mjs';
import { readRegularFileBounded, requestBounded } from './check-openapi-service.mjs';

const execute = promisify(execFile);
const docker = async (...args) => {
  try {
    return (await execute('docker', args, { timeout: 120000, maxBuffer: 1024 * 1024 })).stdout.trim();
  } catch (error) {
    error.dockerOperation = args.slice(0, 2).join(' ');
    throw error;
  }
};
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const image = process.env.CONTAINER_IMAGE;
const architecture = process.env.ARCHITECTURE;
assert.ok(image);
assert.ok(['amd64', 'arm64'].includes(architecture));
assert.equal(process.arch, architecture === 'amd64' ? 'x64' : 'arm64');
assert.notEqual(process.getuid(), 0);
const packageDir = path.join(process.env.RUNNER_TEMP, 'unpacked', `localpost_0.2.0_linux_${architecture}`);
const report = path.join(process.env.RUNNER_TEMP, 'container-evidence');
await mkdir(report, { recursive: true });
const root = await mkdtemp(path.join(os.tmpdir(), 'localpost-container-'));
await chmod(root, 0o700);
const containers = [];
const prefix = `localpost-${process.pid}-${Date.now()}`;
const candidate = JSON.parse(await readFile('release/0.2.0-candidate.json'));
const expectedCommit = candidate.sourceCommit;
assert.match(expectedCommit, /^[0-9a-f]{40}$/);
const apiPrefix = '/api/2026-11-05';
let phase = 'host-metadata';
const send = (port, route, method, body) => requestBounded({ url: `http://127.0.0.1:${port}${route}`, method, body: body && Buffer.from(JSON.stringify(body)), headers: body ? { 'Content-Type': 'application/json' } : {}, timeoutMs: 5000 });
const start = async (suffix, args = []) => {
  const name = `${prefix}-${suffix}`;
  containers.push(name);
  const user = ['--user', `${process.getuid()}:${process.getgid()}`];
  await docker('run', '--detach', '--name', name, '--network', 'host', '--read-only', '--stop-timeout', '40', ...user, '--env', 'PATH=', '--env', `TMPDIR=${root}`, '--mount', `type=bind,source=${root},target=${root}`, image, '--mailbox-root', root, ...args);
  return name;
};
const ready = async (port, name) => {
  for (let attempt = 0; attempt < 50; attempt++) {
    assert.equal(await docker('inspect', '--format', '{{.State.Running}}', name), 'true');
    try { await send(port, '/', 'GET'); return; } catch { await new Promise((resolve) => setTimeout(resolve, 100)); }
  }
  throw new Error('Readiness bound exceeded');
};
const waitTemporary = async (directory) => {
  for (let attempt = 0; attempt < 100; attempt++) {
    const names = (await readdir(directory)).filter((name) => name.startsWith('.localpost-upload-'));
    if (names.length === 1 && (await stat(path.join(directory, names[0]))).size > 0) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error('Admitted upload was not observed');
};
try {
  const host = {
    os: (await readFile('/etc/os-release', 'utf8')).match(/^PRETTY_NAME=(.*)$/m)?.[1].replaceAll('"', ''),
    kernel: os.release(),
    version: os.version(),
    architecture: process.arch,
    uid: process.getuid(),
    filesystemType: String((await statfs(root)).type),
    filesystem: (await execute('findmnt', ['-n', '-o', 'FSTYPE', '-T', root], { timeout: 5000, maxBuffer: 4096 })).stdout.trim(),
    docker: await docker('version', '--format', 'client={{.Client.Version}} server={{.Server.Version}}'),
  };
  phase = 'image-inspect';
  const [configuration] = JSON.parse(await docker('image', 'inspect', image));
  phase = 'image-configuration';
  assert.equal(configuration.Architecture, architecture);
  assert.equal(configuration.Os, 'linux');
  assert.deepEqual(configuration.Config.Entrypoint, ['/localpost']);
  assert.equal(configuration.Config.User, '65532:65532');
  assert.equal(configuration.Config.StopSignal, 'SIGINT');
  const labels = configuration.Config.Labels;
  assert.equal(labels['org.opencontainers.image.version'], '0.2.0');
  assert.equal(labels['org.opencontainers.image.revision'], expectedCommit);
  assert.equal(labels['org.opencontainers.image.source'], 'https://github.com/SixbucksSolutions/localpost');
  assert.equal(labels['org.opencontainers.image.licenses'], 'MIT');
  assert.equal(labels['io.localpost.packaging-revision'], process.env.GITHUB_SHA);
  assert.match(labels['io.localpost.packaging-revision'], /^[0-9a-f]{40}$/);
  const copyContainer = `${prefix}-copy`;
  containers.push(copyContainer);
  phase = 'copy-container-create';
  await docker('create', '--name', copyContainer, image, '--version');
  phase = 'binary-copy';
  await docker('cp', `${copyContainer}:/localpost`, path.join(root, 'image-binary'));
  phase = 'binary-identity';
  assert.equal(hash(await readFile(path.join(root, 'image-binary'))), hash(await readFile(path.join(packageDir, 'localpost'))));
  phase = 'assets-copy';
  await docker('cp', `${copyContainer}:/usr/share/localpost`, path.join(root, 'assets'));
  phase = 'assets-identity';
  const compare = async (relative) => {
    const source = path.join(packageDir, relative);
    if ((await stat(source)).isDirectory()) {
      for (const entry of await readdir(source)) await compare(path.join(relative, entry));
    } else assert.equal(hash(await readFile(path.join(root, 'assets', relative))), hash(await readFile(source)));
  };
  for (const member of ['LICENSE', 'build-info.json', 'openapi.json', 'licenses']) await compare(member);
  phase = 'default-user-version';
  const versionContainer = `${prefix}-version`;
  containers.push(versionContainer);
  assert.equal(await docker('run', '--rm', '--name', versionContainer, '--read-only', '--network', 'none', image, '--version'), `localpost 0.2.0 (commit ${expectedCommit}; go1.27.3; linux/${architecture})`);
  phase = 'isolated-port-check';
  for (const port of [53147, 53151, 53152]) {
    let occupied = false;
    try { await send(port, '/', 'GET'); occupied = true; } catch { /* A refused connection means this isolated port is available. */ }
    assert.equal(occupied, false, 'Qualification port already occupied');
  }
  phase = 'full-contract';
  const override = await start('override', ['--port', '53151']);
  const defaultPort = await start('default-port');
  await ready(53151, override);
  await ready(53147, defaultPort);
  for (const [port, args] of [[53147, []], [53151, ['--port', '53151']]]) {
    const name = `${prefix}-conflict-${port}`;
    containers.push(name);
    await assert.rejects(docker('run', '--name', name, '--network', 'host', '--read-only', '--user', `${process.getuid()}:${process.getgid()}`, '--mount', `type=bind,source=${root},target=${root}`, image, '--mailbox-root', root, ...args));
    assert.notEqual(await docker('inspect', '--format', '{{.State.ExitCode}}', name), '0');
    const logs = await execute('docker', ['logs', name], { timeout: 5000, maxBuffer: 16384 });
    assert.ok((logs.stdout + logs.stderr).includes(`127.0.0.1:${port}`));
  }
  const previousTmp = process.env.TMPDIR;
  process.env.TMPDIR = root;
  let qualification;
  try { qualification = await qualifyPackage({ packageDir, baseUrl: 'http://127.0.0.1:53151', expectedCommit, mailboxRoot: root }); }
  finally { if (previousTmp === undefined) delete process.env.TMPDIR; else process.env.TMPDIR = previousTmp; }
  phase = 'docker-stop-admitted-upload';
  const registration = await send(53151, `${apiPrefix}/registrations`, 'POST', {});
  assert.equal(registration.status, 200);
  const { id: shutdownId, directory: shutdownDirectory } = JSON.parse(registration.body);
  assert.equal(shutdownDirectory, path.join(root, shutdownId));
  const directoryStat = await stat(shutdownDirectory);
  assert.equal(directoryStat.uid, process.getuid());
  assert.equal(directoryStat.mode & 0o777, 0o700);
  assert.match(shutdownId, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  const prefixBytes = Buffer.alloc(65536, 0xa5);
  const tail = Buffer.from([0, 255, 128]);
  let stopped;
  const response = await partialUpload({ url: `http://127.0.0.1:53151${apiPrefix}/registrations/${shutdownId}/messages`, prefix: prefixBytes, tail, observe: async () => {
    await waitTemporary(shutdownDirectory);
    stopped = docker('stop', override);
    stopped.catch(() => {});
    let listenerClosed = false;
    for (let attempt = 0; attempt < 40; attempt++) {
      try { await send(53151, '/', 'GET'); }
      catch { listenerClosed = true; break; }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    assert.ok(listenerClosed, 'docker stop did not close the listener before completing the upload');
    assert.equal(await docker('inspect', '--format', '{{.State.Running}}', override), 'true');
  } });
  assert.equal(response.status, 200);
  await stopped;
  const shutdownReceipt = JSON.parse(response.body);
  assert.match(shutdownReceipt.filename, /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$/);
  const deliveredPath = path.join(shutdownDirectory, shutdownReceipt.filename);
  assert.deepEqual(await readRegularFileBounded(deliveredPath, prefixBytes.length + tail.length), Buffer.concat([prefixBytes, tail]));
  const deliveredStat = await stat(deliveredPath);
  assert.equal(deliveredStat.uid, process.getuid());
  assert.equal(deliveredStat.mode & 0o777, 0o600);
  assert.deepEqual(await readdir(shutdownDirectory), [shutdownReceipt.filename]);
  await docker('stop', defaultPort);
  for (const name of [override, defaultPort]) assert.equal(await docker('inspect', '--format', '{{.State.ExitCode}}', name), '0');
  await writeFile(path.join(report, 'qualification.json'), JSON.stringify({ passed: true, host, imageId: configuration.Id, architecture, labels, qualification, defaultUserVersion: true, runtimeUser: process.getuid(), mailboxRootMode: '0700', portsAndConflicts: true, dockerStopAdmittedUpload: true, persistedBytes: prefixBytes.length + tail.length }, null, 2));
  console.log('Container qualification passed.');
} catch (error) {
  // This isolated CI harness invokes only fixed commands with owned paths and synthetic payloads.
  // Bound diagnostics locally so a Docker failure remains actionable without dumping process state.
  const bounded = (value, limit) => typeof value === 'string' ? value.slice(0, limit) : undefined;
  await writeFile(path.join(report, 'failure.json'), JSON.stringify({
    passed: false,
    phase,
    code: error.code ?? error.name,
    dockerOperation: error.dockerOperation,
    message: bounded(error.message, 4096),
    stderr: bounded(error.stderr, 8192),
    stdout: bounded(error.stdout, 4096),
  }, null, 2));
  throw new Error(`Container qualification failed during ${phase}`);
} finally {
  for (const name of containers) await docker('rm', '--force', name).catch(() => {});
  await rm(root, { recursive: true, force: true });
}
