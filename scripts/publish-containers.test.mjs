import assert from 'node:assert/strict';
import test from 'node:test';
import { checkIndex, isMissingManifest } from './publish-containers.mjs';

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
