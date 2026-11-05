// Publish only docker-save artifacts whose image IDs were qualified on native hosts.
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);
const image = 'ghcr.io/sixbuckssolutions/localpost';
const version = '0.1.0';
const digestPattern = /^sha256:[0-9a-f]{64}$/;
const docker = async (...args) => (await execute('docker', args, { timeout: 180000, maxBuffer: 2 * 1024 * 1024 })).stdout.trim();
export function isMissingManifest(error, reference) {
  const diagnostic = error.stderr ?? '';
  if (/manifest unknown|manifest_unknown/i.test(diagnostic)) return true;
  if (diagnostic.includes(`${reference}: not found`)) return true;
  const manifestUrl = `https://ghcr.io/v2/sixbuckssolutions/localpost/manifests/${reference.split(':').at(-1)}`;
  return diagnostic.includes(manifestUrl) && /404 Not Found/i.test(diagnostic);
}
const inspect = async (reference, optional = false) => {
  try {
    const raw = JSON.parse(await docker('buildx', 'imagetools', 'inspect', '--raw', reference));
    const summary = await docker('buildx', 'imagetools', 'inspect', reference);
    const digest = summary.match(/^Digest:\s+(sha256:[0-9a-f]{64})$/m)?.[1];
    assert.match(digest ?? '', digestPattern);
    return { digest, raw };
  } catch (error) {
    if (optional && isMissingManifest(error, reference)) return null;
    throw error;
  }
};
// Docker stores expose either the config, platform manifest, or pulled index as Id.
// Every accepted value must be tied to the exact registry descriptors being verified.
export function checkImageIdentity(imageId, { platformDigest, configDigest, indexDigest }) {
  assert.match(imageId, digestPattern);
  assert.match(platformDigest, digestPattern);
  assert.match(configDigest, digestPattern);
  const accepted = [platformDigest, configDigest];
  if (indexDigest !== undefined) {
    assert.match(indexDigest, digestPattern);
    accepted.push(indexDigest);
  }
  assert.ok(accepted.includes(imageId), 'Image ID does not match the verified config, platform manifest or index');
}

export const checkIndex = (index, platforms) => {
  assert.ok(['application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json'].includes(index.raw.mediaType));
  assert.equal(index.raw.manifests.length, 2);
  for (const architecture of ['amd64', 'arm64']) {
    const descriptor = index.raw.manifests.find((item) => item.platform?.os === 'linux' && item.platform?.architecture === architecture);
    assert.ok(descriptor);
    assert.equal(descriptor.digest, platforms[architecture].digest);
  }
};
export async function main(mode = process.argv[2]) {
  const root = path.join(process.env.RUNNER_TEMP, 'container-publication');
  await mkdir(root, { recursive: true });
  if (mode === 'publish') {
    const platforms = {};
    for (const architecture of ['amd64', 'arm64']) {
      const directory = path.join(process.env.RUNNER_TEMP, 'qualified', architecture);
      const identity = JSON.parse(await readFile(path.join(directory, 'identity.json')));
      assert.equal(identity.architecture, architecture);
      assert.match(identity.imageId, digestPattern);
      await docker('load', '--input', path.join(directory, 'image.tar'));
      const [local] = JSON.parse(await docker('image', 'inspect', `localpost-qualified:${architecture}`));
      assert.equal(local.Id, identity.imageId);
      assert.equal(local.Architecture, architecture);
      const tag = `${image}:candidate-${process.env.GITHUB_RUN_ID}-${architecture}`;
      await docker('tag', identity.imageId, tag);
      await docker('push', tag);
      const remote = await inspect(tag);
      const configDigest = remote.raw.config?.digest;
      checkImageIdentity(identity.imageId, { platformDigest: remote.digest, configDigest });
      platforms[architecture] = { digest: remote.digest, configDigest, imageId: identity.imageId };
    }
    const candidate = `${image}:candidate-${process.env.GITHUB_RUN_ID}`;
    await docker('buildx', 'imagetools', 'create', '--tag', candidate, ...Object.values(platforms).map((item) => `${image}@${item.digest}`));
    const index = await inspect(candidate);
    checkIndex(index, platforms);
    const existing = await inspect(`${image}:${version}`, true);
    if (existing) {
      assert.equal(existing.digest, index.digest, 'Refusing to overwrite a different version index');
      checkIndex(existing, platforms);
    } else {
      await docker('buildx', 'imagetools', 'create', '--tag', `${image}:${version}`, `${image}@${index.digest}`);
    }
    const published = await inspect(`${image}:${version}`);
    assert.equal(published.digest, index.digest);
    checkIndex(published, platforms);
    await writeFile(path.join(root, 'publication.json'), JSON.stringify({ image, version, indexDigest: index.digest, platforms }, null, 2));
    console.log(`Version index published: ${image}:${version}@${index.digest}`);
  } else if (mode === 'verify' || mode === 'promote') {
    const publication = JSON.parse(await readFile(path.join(root, 'publication.json')));
    assert.equal(publication.image, image);
    assert.equal(publication.version, version);
    assert.match(publication.indexDigest, digestPattern);
    const published = await inspect(`${image}:${version}`);
    assert.equal(published.digest, publication.indexDigest);
    checkIndex(published, publication.platforms);
    if (mode === 'verify') {
      const architecture = process.env.ARCHITECTURE;
      assert.ok(['amd64', 'arm64'].includes(architecture));
      assert.equal(process.arch, architecture === 'amd64' ? 'x64' : 'arm64');
      await docker('pull', `${image}@${publication.indexDigest}`);
      const [local] = JSON.parse(await docker('image', 'inspect', `${image}@${publication.indexDigest}`));
      assert.equal(local.Architecture, architecture);
      checkImageIdentity(local.Id, {
        platformDigest: publication.platforms[architecture].digest,
        configDigest: publication.platforms[architecture].configDigest,
        indexDigest: publication.indexDigest,
      });
      assert.ok(local.RepoDigests.includes(`${image}@${publication.indexDigest}`));
      if (process.env.GITHUB_ENV) await writeFile(process.env.GITHUB_ENV, `CONTAINER_IMAGE=${image}@${publication.indexDigest}\n`, { flag: 'a' });
      await writeFile(path.join(root, `anonymous-${architecture}.json`), JSON.stringify({ passed: true, architecture, indexDigest: publication.indexDigest, platformDigest: publication.platforms[architecture].digest, configDigest: publication.platforms[architecture].configDigest, imageId: local.Id }, null, 2));
      console.log(`Anonymous native pull verified for ${architecture}.`);
    } else {
      for (const architecture of ['amd64', 'arm64']) {
        const result = JSON.parse(await readFile(path.join(root, `anonymous-${architecture}.json`)));
        assert.equal(result.passed, true);
        assert.equal(result.architecture, architecture);
        assert.equal(result.indexDigest, publication.indexDigest);
        assert.equal(result.platformDigest, publication.platforms[architecture].digest);
        assert.equal(result.configDigest, publication.platforms[architecture].configDigest);
        checkImageIdentity(result.imageId, {
          platformDigest: result.platformDigest,
          configDigest: result.configDigest,
          indexDigest: result.indexDigest,
        });
      }
      await docker('buildx', 'imagetools', 'create', '--tag', `${image}:latest`, `${image}@${publication.indexDigest}`);
      assert.equal((await inspect(`${image}:latest`)).digest, publication.indexDigest);
      console.log(`Latest promoted to verified index ${publication.indexDigest}.`);
    }
  } else throw new Error('Expected publish, verify or promote');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
