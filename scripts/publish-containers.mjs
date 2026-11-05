// Publish only docker-save artifacts whose image IDs were qualified on native hosts.
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);
const image = 'ghcr.io/sixbuckssolutions/localpost';
const version = '0.2.0';
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
export function checkPromotionSource(run, jobs, repository, runId, packagingRevision) {
  assert.match(runId, /^[1-9][0-9]*$/);
  assert.match(packagingRevision, /^[0-9a-f]{40}$/);
  assert.equal(run.head_sha, packagingRevision, 'Resume requires the identical workflow revision');
  assert.equal(String(run.id), runId);
  assert.equal(run.repository.full_name.toLowerCase(), repository.toLowerCase());
  assert.equal(run.path, '.github/workflows/publish-containers.yml');
  assert.equal(run.event, 'workflow_dispatch');
  assert.equal(run.status, 'completed');
  assert.equal(run.conclusion, 'failure');
  const expected = new Map([
    ['publish', 'success'],
    ['anonymous (ubuntu-26.04, amd64)', 'success'],
    ['anonymous (ubuntu-26.04-arm, arm64)', 'success'],
    ['promote', 'failure'],
  ]);
  if (jobs.jobs.some((job) => job.name === 'qualify')) {
    expected.set('qualify', 'skipped');
  } else {
    expected.set('qualify (ubuntu-26.04, amd64)', 'success');
    expected.set('qualify (ubuntu-26.04-arm, arm64)', 'success');
  }
  assert.equal(jobs.total_count, expected.size);
  assert.equal(jobs.jobs.length, expected.size);
  for (const job of jobs.jobs) {
    assert.equal(job.status, 'completed');
    assert.equal(job.conclusion, expected.get(job.name), 'Source run must have only its promotion job failed');
    assert.ok(expected.delete(job.name), 'Unexpected or duplicate source job');
  }
  assert.equal(expected.size, 0);
}

export function checkQualifiedRecord(candidate, runId) {
  assert.match(runId, /^[1-9][0-9]*$/);
  const record = candidate.containerQualification;
  assert.ok(record, 'Accepted container qualification is required');
  assert.equal(String(record.runId), runId);
  assert.match(record.packagingCommit, /^[0-9a-f]{40}$/);
  for (const architecture of ['amd64', 'arm64']) assert.match(record.imageIds?.[architecture], digestPattern);
  return record;
}

export function checkQualificationSource(run, jobs, repository, runId, record) {
  assert.equal(repository.toLowerCase(), 'sixbuckssolutions/localpost');
  assert.equal(String(record.runId), runId);
  assert.equal(run.head_sha, record.packagingCommit);
  assert.equal(String(run.id), runId);
  assert.equal(run.repository.full_name.toLowerCase(), repository.toLowerCase());
  assert.equal(run.path, '.github/workflows/publish-containers.yml');
  assert.equal(run.event, 'workflow_dispatch');
  assert.equal(run.status, 'completed');
  assert.equal(run.conclusion, 'success');
  const expected = new Map([
    ['qualify (ubuntu-26.04, amd64)', 'success'],
    ['qualify (ubuntu-26.04-arm, arm64)', 'success'],
    ['publish', 'skipped'],
    ['anonymous', 'skipped'],
    ['promote', 'skipped'],
  ]);
  assert.equal(jobs.total_count, expected.size);
  assert.equal(jobs.jobs.length, expected.size);
  for (const job of jobs.jobs) {
    assert.equal(job.status, 'completed');
    assert.equal(job.conclusion, expected.get(job.name), 'Source must be a successful qualification-only run');
    assert.ok(expected.delete(job.name), 'Unexpected or duplicate source job');
  }
  assert.equal(expected.size, 0);
}

export function checkQualifiedImage(local, identity, architecture, packagingCommit, sourceCommit, acceptedImageId) {
  assert.equal(identity.architecture, architecture);
  assert.match(identity.imageId, digestPattern);
  if (acceptedImageId !== undefined) assert.equal(identity.imageId, acceptedImageId);
  assert.equal(local.Id, identity.imageId);
  assert.equal(local.Architecture, architecture);
  assert.equal(local.Os, 'linux');
  assert.equal(local.Config.Labels['org.opencontainers.image.version'], version);
  assert.equal(local.Config.Labels['org.opencontainers.image.revision'], sourceCommit);
  assert.equal(local.Config.Labels['io.localpost.packaging-revision'], packagingCommit);
}

export function checkPublicationPolicy(candidate, optIn) {
  assert.equal(optIn, 'true', 'Explicit publication opt-in is required');
  assert.equal(candidate.version, version);
  assert.equal(candidate.publicationStatus, 'published');
  assert.equal(candidate.qualification.status, 'passed');
}

export async function main(mode = process.argv[2]) {
  const root = path.join(process.env.RUNNER_TEMP, 'container-publication');
  await mkdir(root, { recursive: true });
  const candidateManifest = JSON.parse(await readFile('release/0.2.0-candidate.json'));
  if (['publish', 'promote', 'resume-check', 'qualified-check'].includes(mode)) {
    checkPublicationPolicy(candidateManifest, process.env.PUBLISH_CONTAINERS);
  }
  const qualifiedRunId = process.env.QUALIFIED_RUN_ID || '';
  const qualification = qualifiedRunId ? checkQualifiedRecord(candidateManifest, qualifiedRunId) : undefined;
  if (qualifiedRunId) assert.equal(process.env.PROMOTE_RUN_ID || '', '', 'Qualification reuse and promotion recovery are separate modes');
  const packagingCommit = qualification?.packagingCommit ?? process.env.GITHUB_SHA;
  if (mode === 'qualified-check') {
    assert.ok(qualification, 'A qualified run ID is required');
    const repository = process.env.GITHUB_REPOSITORY;
    assert.equal(repository.toLowerCase(), 'sixbuckssolutions/localpost');
    const endpoint = `repos/${repository}/actions/runs/${qualifiedRunId}`;
    const query = async (route) => JSON.parse((await execute('gh', ['api', route], { timeout: 30000, maxBuffer: 1024 * 1024 })).stdout);
    const run = await query(endpoint);
    const jobs = await query(`${endpoint}/jobs?filter=latest&per_page=100`);
    checkQualificationSource(run, jobs, repository, qualifiedRunId, qualification);
    console.log(`Verified accepted qualification source ${qualifiedRunId}.`);
    return;
  }
  if (mode === 'resume-check') {
    const runId = process.env.PROMOTE_RUN_ID;
    assert.match(runId, /^[1-9][0-9]*$/);
    const repository = process.env.GITHUB_REPOSITORY;
    assert.equal(repository.toLowerCase(), 'sixbuckssolutions/localpost');
    const endpoint = `repos/${repository}/actions/runs/${runId}`;
    const query = async (route) => JSON.parse((await execute('gh', ['api', route], { timeout: 30000, maxBuffer: 1024 * 1024 })).stdout);
    const run = await query(endpoint);
    const jobs = await query(`${endpoint}/jobs?filter=latest&per_page=100`);
    checkPromotionSource(run, jobs, repository, runId, process.env.GITHUB_SHA);
    console.log(`Verified completed qualification, publication and anonymous jobs for promotion source ${runId}.`);
    return;
  }
  if (mode === 'publish') {
    const platforms = {};
    for (const architecture of ['amd64', 'arm64']) {
      const directory = path.join(process.env.RUNNER_TEMP, 'qualified', architecture);
      const identity = JSON.parse(await readFile(path.join(directory, 'identity.json')));
      assert.equal(identity.architecture, architecture);
      assert.match(identity.imageId, digestPattern);
      await docker('load', '--input', path.join(directory, 'image.tar'));
      const [local] = JSON.parse(await docker('image', 'inspect', `localpost-qualified:${architecture}`));
      checkQualifiedImage(local, identity, architecture, packagingCommit, candidateManifest.sourceCommit, qualification?.imageIds[architecture]);
      const tag = `${image}:candidate-${process.env.GITHUB_RUN_ID}-${architecture}`;
      await docker('tag', identity.imageId, tag);
      await docker('push', tag);
      const remote = await inspect(tag);
      const configDigest = remote.raw.config?.digest;
      checkImageIdentity(identity.imageId, { platformDigest: remote.digest, configDigest });
      platforms[architecture] = { digest: remote.digest, configDigest, imageId: identity.imageId };
    }
    const candidateReference = `${image}:candidate-${process.env.GITHUB_RUN_ID}`;
    await docker('buildx', 'imagetools', 'create', '--tag', candidateReference, ...Object.values(platforms).map((item) => `${image}@${item.digest}`));
    const index = await inspect(candidateReference);
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
    await writeFile(path.join(root, 'publication.json'), JSON.stringify({ image, version, packagingCommit, indexDigest: index.digest, platforms }, null, 2));
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
      assert.equal(publication.packagingCommit, packagingCommit);
      assert.match(publication.packagingCommit, /^[0-9a-f]{40}$/);
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
      if (process.env.GITHUB_ENV) await writeFile(process.env.GITHUB_ENV, `CONTAINER_IMAGE=${image}@${publication.indexDigest}\nPACKAGING_REVISION=${publication.packagingCommit}\n`, { flag: 'a' });
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
