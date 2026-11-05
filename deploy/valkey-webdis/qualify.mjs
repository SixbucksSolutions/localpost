import {qualifyAdoption} from '../../scripts/qualify-adoption.mjs';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {readFileSync, mkdirSync, writeFileSync} from 'node:fs';
import path from 'node:path';

const file = fileURLToPath(new URL('./compose.yaml', import.meta.url));
const project = `localpost-qualify-${process.pid}-${Date.now()}`;
const port = process.env.LOCALPOST_PORT || '53170';
const env = {...process.env, LOCALPOST_PORT: port, LOCALPOST_MAXMEMORY: '128mb'};
const pins = JSON.parse(readFileSync(new URL('./images.json', import.meta.url), 'utf8').replace(/^\uFEFF/, ''));
const base = `http://127.0.0.1:${port}`;
function compose(...args) {
  return execFileSync('docker', ['compose', '-p', project, '-f', file, ...args],
    {env, encoding: 'utf8', timeout: 300_000});
}
async function command(commandPath, method = 'GET', body) {
  const segments = commandPath.split('/');
  const encodedPath = segments.map(segment => encodeURIComponent(segment)).join('/');
  const response = await fetch(`${base}/${encodedPath}`, {method, body, signal: AbortSignal.timeout(5000)});
  assert.equal(response.status, 200);
  const result = await response.json();
  const reply = result[segments[0]];
  if (Array.isArray(reply) && reply[0] === false) {
    evidence.nativeError = {command: segments[0], reply: JSON.stringify(reply).slice(0, 512)};
    throw new Error(`Webdis rejected qualification command ${segments[0]}`);
  }
  return result;
}
async function ready() {
  let lastResponse;
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      const response = await fetch(`${base}/PING`, {signal: AbortSignal.timeout(5000)});
      const text = (await response.text()).slice(0, 512);
      lastResponse = {status: response.status, body: text};
      const reply = JSON.parse(text).PING;
      if (response.status === 200 && Array.isArray(reply) && reply[0] === true && reply[1] === 'PONG') return;
    } catch (error) {
      lastResponse = {error: error.name, code: error.cause?.code};
    }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  function probe(...args) {
    try {
      return compose('exec', '-T', ...args).slice(0, 512);
    } catch (error) {
      return {exitCode: error.status, stdout: error.stdout?.toString().slice(0, 512),
        stderr: error.stderr?.toString().slice(0, 512)};
    }
  }
  evidence.readiness = {hostHTTP: lastResponse,
    internalHTTP: probe('valkey', 'sh', '-c', 'wget -T 5 -qO- http://webdis:7379/PING'),
    backend: probe('valkey', 'valkey-cli', 'PING')};
  console.error(JSON.stringify(evidence.readiness));
  throw new Error('Webdis did not become ready; bounded PING diagnostics recorded');
}
const evidence = {status: 'failed', project, architecture: process.arch, pins: pins.images};
function stopCleanly(service, id) {
  compose('stop', service);
  const [container] = JSON.parse(execFileSync('docker', ['inspect', id], {encoding: 'utf8'}));
  assert.equal(container.State.Running, false);
  assert.equal(container.State.ExitCode, 0);
  assert.equal(container.State.OOMKilled, false);
}
try {
  compose('up', '-d', '--wait');
  await ready();
  const ids = compose('ps', '-q').trim().split(/\s+/);
  assert.equal(ids.length, 2);
  const containers = JSON.parse(execFileSync('docker', ['inspect', ...ids], {encoding: 'utf8'}));
  for (const container of containers) {
    assert.equal(container.Config.User, '65532:65532');
    assert.equal(container.HostConfig.ReadonlyRootfs, true);
    assert.equal(container.Mounts.some(mount => mount.RW), false);
    assert.equal(container.Mounts.some(mount => mount.Type === 'volume'), false);
  }
  for (const pin of pins.images) {
    const container = containers.find(item => item.Config.Image === `${pin.image}@${pin.indexDigest}`);
    assert.ok(container, `Pinned image missing: ${pin.image}`);
    const [image] = JSON.parse(execFileSync('docker', ['image', 'inspect', container.Image], {encoding: 'utf8'}));
    assert.equal(Object.keys(image.Config.Volumes || {}).length, 0);
    assert.ok(pin.platforms.some(platform => platform.architecture === image.Architecture));
    if (pin.image.startsWith('nicolas/webdis:')) {
      assert.equal(image.Config.Labels['org.opencontainers.image.version'], '0.1.25');
      assert.equal(image.Config.Labels['org.opencontainers.image.revision'], '56d0a9285bd04ff06d02f42e4896cf1a0a5cf548');
    }
  }
  const valkey = containers.find(container => container.Config.Labels['com.docker.compose.service'] === 'valkey');
  const webdis = containers.find(container => container.Config.Labels['com.docker.compose.service'] === 'webdis');
  const backendName = `${project}_backend`;
  const frontendName = `${project}_frontend`;
  assert.deepEqual(Object.keys(valkey.NetworkSettings.Networks), [backendName]);
  assert.deepEqual(Object.keys(webdis.NetworkSettings.Networks).sort(), [backendName, frontendName].sort());
  const networks = JSON.parse(execFileSync('docker', ['network', 'inspect', backendName, frontendName], {encoding: 'utf8'}));
  assert.equal(networks.find(network => network.Name === backendName).Internal, true);
  assert.equal(networks.find(network => network.Name === frontendName).Internal, false);
  assert.deepEqual(Object.keys(webdis.NetworkSettings.Ports), ['7379/tcp']);
  assert.deepEqual(valkey.Config.Entrypoint, ['valkey-server']);
  assert.deepEqual(webdis.Config.Entrypoint, ['/usr/local/bin/webdis']);
  assert.equal(Object.values(valkey.NetworkSettings.Ports || {}).some(Boolean), false);
  assert.deepEqual(webdis.NetworkSettings.Ports['7379/tcp'], [{HostIp: '127.0.0.1', HostPort: port}]);
  assert.match(compose('exec', '-T', 'valkey', 'valkey-server', '--version'), /v=9\.0\.7\b/);
  for (const [name, expected] of [['save', ''], ['appendonly', 'no'], ['maxmemory-policy', 'noeviction'], ['pidfile', ''], ['logfile', '']]) {
    const output = compose('exec', '-T', 'valkey', 'valkey-cli', '--raw', 'CONFIG', 'GET', name).trimEnd().split('\n');
    assert.equal(output[0], name);
    assert.equal(output[1] || '', expected);
  }
  assert.equal(compose('exec', '-T', 'valkey', 'valkey-cli', '--raw', 'CONFIG', 'GET', 'maxmemory').trim().split('\n')[1], '134217728');
  const webdisLogs = compose('logs', '--no-color', 'webdis');
  assert.match(webdisLogs, /Webdis 0\.1\.25 up and running/);
  const denied = await fetch(`${base}/CONFIG/GET/save`, {signal: AbortSignal.timeout(5000)});
  assert.equal(denied.status, 403);
  const key = `localpost:qualification-${project}`;
  const payloads = ['first', 'second', ''];
  for (const payload of payloads) {
    const result = await command(`XADD/${key}/*/data`, 'PUT', 'b64:' + Buffer.from(payload).toString('base64'));
    assert.equal(typeof result.XADD, 'string');
  }
  const before = await command(`XRANGE/${key}/-/+`);
  assert.equal(before.XRANGE.length, 3);
  assert.deepEqual(before.XRANGE.map(entry => Buffer.from(entry.msg.data.slice(4), 'base64').toString()), payloads);
  assert.deepEqual(await command(`XRANGE/${key}/-/+`), before);
  assert.ok(before.XRANGE.every(entry => entry.msg.data.startsWith('b64:')));
  let adoption;
  if (process.env.LOCALPOST_CLIENT_BINARY) {
    adoption = await qualifyAdoption({
      client: process.env.LOCALPOST_CLIENT_BINARY, baseUrl: base,
      recordEvidence(value) { evidence.adoption = value; },
      setCapacity(value) {
        assert.equal(compose('exec', '-T', 'valkey', 'valkey-cli', '--raw', 'CONFIG', 'SET', 'maxmemory', value).trim(), 'OK');
      },
    });
    evidence.adoption = adoption.evidence;
  }
  stopCleanly('webdis', webdis.Id);
  compose('start', 'webdis');
  await ready();
  assert.deepEqual(await command(`XRANGE/${key}/-/+`), before);
  adoption?.verifyFrontendRestart();
  stopCleanly('valkey', valkey.Id);
  compose('start', 'valkey');
  await ready();
  assert.deepEqual((await command(`XRANGE/${key}/-/+`)).XRANGE, []);
  adoption?.verifyRestart();
  const changes = ids.map(id => execFileSync('docker', ['diff', id], {encoding: 'utf8'}));
  assert.deepEqual(changes, ['', '']);
  stopCleanly('webdis', webdis.Id);
  stopCleanly('valkey', valkey.Id);
  Object.assign(evidence, {status: 'passed', checks: ['pins', 'unprivileged', 'readonly', 'no-volumes', 'loopback', 'configuration',
      'retained-read', 'empty-payload', 'command-allowlist', 'frontend-restart',
      'backend-restart-loss', 'graceful-shutdown', 'binary-versions', 'empty-container-diff']});
  console.log(JSON.stringify(evidence));
} finally {
  try {
    if (process.env.LOCALPOST_EVIDENCE_DIR) {
      mkdirSync(process.env.LOCALPOST_EVIDENCE_DIR, {recursive: true});
      writeFileSync(path.join(process.env.LOCALPOST_EVIDENCE_DIR, 'runtime.json'), JSON.stringify(evidence, null, 2));
      writeFileSync(path.join(process.env.LOCALPOST_EVIDENCE_DIR, 'runtime.log'), compose('logs', '--no-color'));
    }
  } finally {
    compose('down', '--remove-orphans');
  }
}