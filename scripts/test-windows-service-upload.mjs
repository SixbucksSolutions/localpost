import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstat, opendir, realpath, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { readRegularFileBounded, requestBounded, validateBaseUrl } from './check-openapi-service.mjs';
import { partialUpload } from './qualify-packaged-service.mjs';

const execute = promisify(execFile);
const apiPrefix = '/api/2026-11-05';
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$(?![\s\S])/;
const filename = /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$(?![\s\S])/;
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export function parseArguments(args) {
  const values = {};
  const required = ['--service-url', '--mailbox-root', '--installer', '--service-name', '--install-root'];
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    assert.ok([...required, '--evidence-path'].includes(key), 'Unknown option');
    assert.ok(!Object.hasOwn(values, key) && args[index + 1] && !args[index + 1].startsWith('--'), 'Missing or repeated option');
    values[key] = args[index + 1];
  }
  for (const key of required) assert.ok(values[key], `Missing ${key}`);
  validateBaseUrl(values['--service-url']);
  assert.match(values['--service-name'], /^[A-Za-z][A-Za-z0-9_-]{0,63}$(?![\s\S])/);
  for (const key of ['--mailbox-root', '--installer', '--install-root', '--evidence-path']) {
    if (values[key]) assert.ok(path.isAbsolute(values[key]), `${key} must be absolute`);
  }
  return values;
}

async function entries(directory) {
  const names = [];
  const handle = await opendir(directory);
  for await (const entry of handle) {
    assert.ok(names.length < 4, 'Unexpected mailbox entry count');
    names.push(entry.name);
  }
  return names.sort();
}

async function waitFor(predicate, description, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await delay(25);
  }
  throw new Error(description);
}

export async function runServiceStopUpload(options) {
  assert.equal(process.platform, 'win32', 'This probe requires Windows SCM');
  const origin = validateBaseUrl(options['--service-url']);
  const root = await realpath(options['--mailbox-root']);
  const installRoot = await realpath(options['--install-root']);
  const installer = await realpath(options['--installer']);
  assert.ok((await lstat(root)).isDirectory(), 'Mailbox root must be a directory');
  assert.ok((await lstat(installer)).isFile(), 'Installer must be a regular file');
  const serviceName = options['--service-name'];
  const powershell = path.join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
  // A PowerShell 7 parent can export incompatible module paths to Windows PowerShell 5.
  const shellEnvironment = { ...process.env };
  for (const key of Object.keys(shellEnvironment)) {
    if (key.toLowerCase() === 'psmodulepath') delete shellEnvironment[key];
  }
  shellEnvironment.PSModulePath = path.join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'Modules');
  const powershellArgs = ['-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass'];
  const processInfo = await execute(powershell, [...powershellArgs, '-Command',
    `$ErrorActionPreference='Stop'; $s=Get-CimInstance Win32_Service -Filter "Name='${serviceName}'"; if (!$s -or $s.State -ne 'Running') { throw 'Service is not running' }; $children=@(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($s.ProcessId)" | Where-Object Name -eq 'localpost.exe'); if ($children.Count -ne 1) { throw 'Expected one daemon child' }; @{ wrapperPid=[int]$s.ProcessId; daemonPid=[int]$children[0].ProcessId; executable=$children[0].ExecutablePath } | ConvertTo-Json -Compress`],
  { timeout: 30000, maxBuffer: 8192, windowsHide: true, env: shellEnvironment });
  const owner = JSON.parse(processInfo.stdout);
  assert.ok(Number.isInteger(owner.wrapperPid) && owner.wrapperPid > 0);
  assert.ok(Number.isInteger(owner.daemonPid) && owner.daemonPid > 0);
  assert.equal(await realpath(owner.executable), await realpath(path.join(installRoot, 'localpost.exe')), 'SCM daemon executable differs from the owned installation');
  const registration = await requestBounded({ url: `${origin}${apiPrefix}/registrations`, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: Buffer.from('{}') });
  assert.equal(registration.status, 200);
  const { id, directory } = JSON.parse(registration.body);
  assert.match(id, uuid);
  assert.equal(directory, path.join(root, id), 'Registration directory differs from canonical mailbox root');
  const directoryInfo = await lstat(directory);
  assert.ok(directoryInfo.isDirectory() && !directoryInfo.isSymbolicLink(), 'Registration directory must be a real directory');
  assert.equal(await realpath(directory), directory, 'Registration directory escaped canonical root');
  assert.deepEqual(await entries(directory), [], 'Probe registration must be empty');

  const prefix = Buffer.alloc(65536, 0xa5);
  const tail = Buffer.from([0, 255, 128, 13, 10]);
  let stopPromise;
  let stopSettled = false;
  let listenerRefused = false;
  try {
    const response = await partialUpload({ url: `${origin}${apiPrefix}/registrations/${id}/messages`, prefix, tail, requestTimeoutMs: 20000, observe: async () => {
      await waitFor(async () => {
        const names = await entries(directory);
        assert.ok(names.every((name) => name.startsWith('.localpost-upload-')), 'Final file appeared before upload completion');
        if (names.length !== 1) return false;
        const info = await lstat(path.join(directory, names[0]));
        return info.isFile() && !info.isSymbolicLink() && info.size > 0;
      }, 'No admitted partial upload was observed');
      stopPromise = execute(powershell, [...powershellArgs, '-File', installer, '-Action', 'Stop', '-ServiceName', serviceName, '-InstallRoot', installRoot, '-MailboxRoot', root],
        { timeout: 70000, maxBuffer: 16384, windowsHide: true, env: shellEnvironment });
      // Observe failures immediately while the HTTP request remains deliberately incomplete.
      stopPromise.then(() => { stopSettled = true; }, () => { stopSettled = true; });
      await waitFor(async () => {
        try { await requestBounded({ url: `${origin}/`, method: 'GET', timeoutMs: 300 }); }
        catch (error) {
          if (error.code === 'ECONNREFUSED') { listenerRefused = true; return true; }
          throw error;
        }
        return false;
      }, 'SCM stop did not close the listener before the upload completed', 15000);
      assert.equal(stopSettled, false, 'SCM stop completed before the admitted upload drained');
      process.kill(owner.daemonPid, 0);
      process.kill(owner.wrapperPid, 0);
      const names = await entries(directory);
      assert.equal(names.length, 1);
      assert.ok(names[0].startsWith('.localpost-upload-'), 'Final file appeared before upload completion');
    } });
    assert.equal(response.status, 200, 'Admitted upload did not finish successfully during SCM stop');
    const receipt = JSON.parse(response.body);
    assert.match(receipt.filename, filename);
    assert.equal(receipt.bytes, prefix.length + tail.length);
    await stopPromise;
    const expected = Buffer.concat([prefix, tail]);
    const persisted = await readRegularFileBounded(path.join(directory, receipt.filename), expected.length);
    assert.deepEqual(persisted, expected, 'Persisted upload bytes changed during SCM stop');
    assert.deepEqual(await entries(directory), [receipt.filename], 'Unexpected files remained after SCM stop');
    await assert.rejects(requestBounded({ url: `${origin}/`, method: 'GET', timeoutMs: 1000 }), { code: 'ECONNREFUSED' });
    return { passed: true, scmStopAdmittedUpload: true, temporaryObserved: true, finalAbsentUntilCompletion: true, listenerRefusedBeforeCompletion: listenerRefused, daemonAliveAfterListenerClosed: true, persistedBytes: persisted.length, sha256: createHash('sha256').update(persisted).digest('hex'), registrationId: id, directory };
  } finally {
    // The caller owns the isolated root and removes it only after inspecting retained delivery evidence.
    // Await the bounded stop even on probe failure so its rejection cannot escape or race caller cleanup.
    if (stopPromise) await stopPromise;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseArguments(process.argv.slice(2));
    const evidence = await runServiceStopUpload(options);
    if (options['--evidence-path']) await writeFile(options['--evidence-path'], JSON.stringify(evidence, null, 2) + '\n', { flag: 'wx' });
    console.log('Windows SCM stop preserved the admitted upload and closed the listener.');
  } catch (error) {
    console.error('Windows SCM admitted-upload probe failed.');
    console.error(JSON.stringify({
      message: String(error.message ?? '').slice(0, 2048),
      code: String(error.code ?? error.name ?? 'UNKNOWN').slice(0, 80),
      killed: error.killed === true,
      signal: String(error.signal ?? '').slice(0, 80),
      stderr: String(error.stderr ?? '').slice(0, 4096),
    }, null, 2));
    process.exitCode = 1;
  }
}
