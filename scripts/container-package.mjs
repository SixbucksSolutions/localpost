import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const architecture = process.env.ARCHITECTURE;
assert.ok(['amd64', 'arm64'].includes(architecture));
assert.equal(process.arch, architecture === 'amd64' ? 'x64' : 'arm64');
const candidate = JSON.parse(await readFile('release/0.2.0-candidate.json'));
assert.equal(candidate.version, '0.2.0');
assert.match(candidate.sourceCommit, /^[0-9a-f]{40}$/);
assert.ok(['candidate', 'published'].includes(candidate.publicationStatus));
if (process.env.PUBLISH_CONTAINERS === 'true') {
  assert.equal(candidate.publicationStatus, 'published');
  assert.equal(candidate.qualification.status, 'passed');
}
const directory = path.join(process.env.RUNNER_TEMP, 'candidate');
await mkdir(directory, { recursive: true });
const archive = `localpost_0.2.0_linux_${architecture}.tar.gz`;
if (candidate.publicationStatus === 'published') {
  for (const name of [archive, 'SHA256SUMS']) {
    execFileSync('curl', ['--fail', '--location', '--silent', '--show-error', '--connect-timeout', '10', '--max-time', '120', `https://github.com/SixbucksSolutions/localpost/releases/download/v0.2.0/${name}`, '-o', path.join(directory, name)], { timeout: 130000, stdio: 'inherit' });
  }
} else {
  assert.equal(candidate.artifactName, `localpost-0.2.0-${candidate.sourceCommit}`);
  assert.ok(Number.isSafeInteger(candidate.artifactId) && candidate.artifactId > 0);
  assert.ok(Number.isSafeInteger(candidate.artifactRunId) && candidate.artifactRunId > 0);
  const artifact = JSON.parse(execFileSync('gh', ['api', `repos/SixbucksSolutions/localpost/actions/artifacts/${candidate.artifactId}`], { timeout: 30000, encoding: 'utf8' }));
  assert.equal(artifact.name, candidate.artifactName);
  assert.equal(artifact.expired, false);
  assert.equal(artifact.workflow_run.id, candidate.artifactRunId);
  assert.equal(artifact.workflow_run.head_sha, candidate.sourceCommit);
  execFileSync('gh', ['run', 'download', String(candidate.artifactRunId), '--repo', 'SixbucksSolutions/localpost', '--name', candidate.artifactName, '--dir', directory], { timeout: 180000, stdio: 'inherit' });
}
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const checksum = await readFile(path.join(directory, 'SHA256SUMS'));
assert.equal(hash(checksum), candidate.checksumFile.sha256);
const expected = candidate.assets.find((asset) => asset.name === archive && asset.target === `linux/${architecture}`);
assert.ok(expected);
assert.equal(hash(await readFile(path.join(directory, archive))), expected.sha256);
assert.ok(checksum.toString().split('\n').includes(`${expected.sha256}  ${archive}`));
const unpacked = path.join(process.env.RUNNER_TEMP, 'unpacked');
await mkdir(unpacked, { recursive: true });
execFileSync('tar', ['-xzf', path.join(directory, archive), '-C', unpacked], { timeout: 30000 });
if (process.env.GITHUB_ENV) {
  const { appendFile } = await import('node:fs/promises');
  await appendFile(process.env.GITHUB_ENV, `SOURCE_COMMIT=${candidate.sourceCommit}\n`);
}
console.log('Candidate archive identity verified.');
