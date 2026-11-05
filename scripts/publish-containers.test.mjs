import assert from 'node:assert/strict';
import test from 'node:test';
import { checkImageIdentity, checkIndex, checkPromotionSource, isMissingManifest } from './publish-containers.mjs';

const reference = 'ghcr.io/sixbuckssolutions/localpost:0.1.0';
test('missing version permits only recognized manifest absence', () => {
  for (const stderr of [
    'ERROR: manifest unknown',
    'ERROR: MANIFEST_UNKNOWN: manifest unknown',
    `ERROR: ${reference}: not found`,
    'unexpected status from HEAD request to https://ghcr.io/v2/sixbuckssolutions/localpost/manifests/0.1.0: 404 Not Found',
  ]) assert.equal(isMissingManifest({ stderr }, reference), true);
  for (const stderr of [
    'unauthorized: authentication required',
    'denied: permission_denied',
    'unexpected status: 403 Forbidden',
    'dial tcp: lookup ghcr.io: no such host',
    'unexpected status: 500 Internal Server Error',
    'docker: not found',
    'unexpected status from HEAD request to https://example.test/manifest: 404 Not Found',
  ]) assert.equal(isMissingManifest({ stderr }, reference), false);
  assert.equal(isMissingManifest({}, reference), false);
});

test('index requires exactly the accepted native platform digests', () => {
  const platforms = { amd64: { digest: `sha256:${'a'.repeat(64)}` }, arm64: { digest: `sha256:${'b'.repeat(64)}` } };
  const index = { raw: { mediaType: 'application/vnd.oci.image.index.v1+json', manifests: Object.entries(platforms).map(([architecture, item]) => ({ platform: { os: 'linux', architecture }, digest: item.digest })) } };
  checkIndex(index, platforms);
  const changed = structuredClone(index);
  changed.raw.manifests[1].digest = `sha256:${'c'.repeat(64)}`;
  assert.throws(() => checkIndex(changed, platforms));
  const extra = structuredClone(index);
  extra.raw.manifests.push(extra.raw.manifests[0]);
  assert.throws(() => checkIndex(extra, platforms));
  const wrong = structuredClone(index);
  wrong.raw.manifests[0].platform.os = 'windows';
  assert.throws(() => checkIndex(wrong, platforms));
});

test('image identity accepts only verified Docker store representations', () => {
  const platformDigest = `sha256:${'a'.repeat(64)}`;
  const configDigest = `sha256:${'b'.repeat(64)}`;
  const indexDigest = `sha256:${'c'.repeat(64)}`;
  const unrelated = `sha256:${'d'.repeat(64)}`;
  const platform = { platformDigest, configDigest };
  checkImageIdentity(platformDigest, platform);
  checkImageIdentity(configDigest, platform);
  assert.throws(() => checkImageIdentity(indexDigest, platform));
  const pulled = { ...platform, indexDigest };
  for (const id of [platformDigest, configDigest, indexDigest]) checkImageIdentity(id, pulled);
  assert.throws(() => checkImageIdentity(unrelated, pulled));
  assert.throws(() => checkImageIdentity('not-a-digest', pulled));
  assert.throws(() => checkImageIdentity(platformDigest, { platformDigest }));
  assert.throws(() => checkImageIdentity(platformDigest, { ...platform, indexDigest: 'invalid' }));
});

test('promotion resume accepts only the approved fully verified source run', () => {
  const repository = 'SixbucksSolutions/localpost';
  const runId = '42034182097';
  const run = { id: Number(runId), repository: { full_name: repository }, path: '.github/workflows/publish-containers.yml', event: 'workflow_dispatch', status: 'completed', conclusion: 'failure' };
  const names = ['qualify (ubuntu-26.04, amd64)', 'qualify (ubuntu-26.04-arm, arm64)', 'publish', 'anonymous (ubuntu-26.04, amd64)', 'anonymous (ubuntu-26.04-arm, arm64)', 'promote'];
  const jobs = { total_count: 6, jobs: names.map((name) => ({ name, status: 'completed', conclusion: name === 'promote' ? 'failure' : 'success' })) };
  checkPromotionSource(run, jobs, repository, runId);
  for (const field of ['path', 'event', 'status', 'conclusion']) {
    assert.throws(() => checkPromotionSource({ ...run, [field]: 'wrong' }, jobs, repository, runId));
  }
  assert.throws(() => checkPromotionSource(run, jobs, 'another/repository', runId));
  assert.throws(() => checkPromotionSource(run, jobs, repository, '42034182098'));
  for (let index = 0; index < jobs.jobs.length; index++) {
    const changed = structuredClone(jobs);
    changed.jobs[index].conclusion = index === 5 ? 'success' : 'failure';
    assert.throws(() => checkPromotionSource(run, changed, repository, runId));
  }
  const duplicate = structuredClone(jobs);
  duplicate.jobs[1] = duplicate.jobs[0];
  assert.throws(() => checkPromotionSource(run, duplicate, repository, runId));
});
