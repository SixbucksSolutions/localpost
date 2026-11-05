import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const architecture = process.env.ARCHITECTURE;
assert.ok(['amd64', 'arm64'].includes(architecture));
assert.equal(process.arch, architecture === 'amd64' ? 'x64' : 'arm64');
const candidate = JSON.parse(await readFile('release/0.1.0-candidate.json'));
assert.equal(candidate.publicationStatus, 'published');
assert.equal(candidate.version, '0.1.0');
const directory = path.join(process.env.RUNNER_TEMP, 'candidate');
await mkdir(directory, { recursive: true });
const archive = `localpost_0.1.0_linux_${architecture}.tar.gz`;
for (const name of [archive, 'SHA256SUMS']) {
  execFileSync('curl', ['--fail', '--location', '--silent', '--show-error', '--connect-timeout', '10', '--max-time', '120', `https://github.com/SixbucksSolutions/localpost/releases/download/v0.1.0/${name}`, '-o', path.join(directory, name)], { timeout: 130000, stdio: 'inherit' });
}
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const checksum = await readFile(path.join(directory, 'SHA256SUMS'));
assert.equal(hash(checksum), candidate.checksumFile.sha256);
const expected = candidate.assets.find((asset) => asset.name === archive);
assert.equal(hash(await readFile(path.join(directory, archive))), expected.sha256);
assert.ok(checksum.toString().split('\n').includes(`${expected.sha256}  ${archive}`));
const unpacked = path.join(process.env.RUNNER_TEMP, 'unpacked');
await mkdir(unpacked, { recursive: true });
execFileSync('tar', ['-xzf', path.join(directory, archive), '-C', unpacked], { timeout: 30000 });
console.log('Published archive identity verified.');
