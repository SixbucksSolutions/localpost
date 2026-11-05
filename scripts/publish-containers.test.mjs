import assert from 'node:assert/strict';
import test from 'node:test';
import childProcess from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { syncBuiltinESMExports } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import { checkImageIdentity, checkIndex, checkPromotionSource, checkPublicationPolicy, checkQualifiedRecord, checkQualificationSource, checkQualifiedImage, isMissingManifest } from './publish-containers.mjs';

const reference = 'ghcr.io/sixbuckssolutions/localpost:0.2.0';
test('missing version permits only recognized manifest absence', () => {
  for (const stderr of [
    'ERROR: manifest unknown',
    'ERROR: MANIFEST_UNKNOWN: manifest unknown',
    `ERROR: ${reference}: not found`,
    'unexpected status from HEAD request to https://ghcr.io/v2/sixbuckssolutions/localpost/manifests/0.2.0: 404 Not Found',
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

test('promotion resume requires an identical revision and fully verified source run', () => {
  const repository = 'SixbucksSolutions/localpost';
  const runId = '42055362444';
  const packagingRevision = 'a'.repeat(40);
  const run = { head_sha: packagingRevision, id: Number(runId), repository: { full_name: repository }, path: '.github/workflows/publish-containers.yml', event: 'workflow_dispatch', status: 'completed', conclusion: 'failure' };
  const names = ['qualify (ubuntu-26.04, amd64)', 'qualify (ubuntu-26.04-arm, arm64)', 'publish', 'anonymous (ubuntu-26.04, amd64)', 'anonymous (ubuntu-26.04-arm, arm64)', 'promote'];
  const jobs = { total_count: 6, jobs: names.map((name) => ({ name, status: 'completed', conclusion: name === 'promote' ? 'failure' : 'success' })) };
  checkPromotionSource(run, jobs, repository, runId, packagingRevision);
  for (const field of ['head_sha', 'path', 'event', 'status', 'conclusion']) {
    assert.throws(() => checkPromotionSource({ ...run, [field]: 'wrong' }, jobs, repository, runId, packagingRevision));
  }
  assert.throws(() => checkPromotionSource(run, jobs, 'another/repository', runId, packagingRevision));
  assert.throws(() => checkPromotionSource(run, jobs, repository, '42055362445', packagingRevision));
  for (let index = 0; index < jobs.jobs.length; index++) {
    const changed = structuredClone(jobs);
    changed.jobs[index].conclusion = index === 5 ? 'success' : 'failure';
    assert.throws(() => checkPromotionSource(run, changed, repository, runId, packagingRevision));
  }
  const duplicate = structuredClone(jobs);
  duplicate.jobs[1] = duplicate.jobs[0];
  assert.throws(() => checkPromotionSource(run, duplicate, repository, runId, packagingRevision));
});


test('publication requires explicit opt-in and the published qualified candidate', () => {
  const candidate = { version: '0.2.0', publicationStatus: 'published', qualification: { status: 'passed' } };
  checkPublicationPolicy(candidate, 'true');
  for (const optIn of [undefined, '', 'false', true]) assert.throws(() => checkPublicationPolicy(candidate, optIn));
  assert.throws(() => checkPublicationPolicy({ ...candidate, version: '0.1.0' }, 'true'));
  assert.throws(() => checkPublicationPolicy({ ...candidate, publicationStatus: 'candidate' }, 'true'));
  assert.throws(() => checkPublicationPolicy({ ...candidate, qualification: { status: 'development' } }, 'true'));
});

test('promotion recovery accepts retained-image publication and rejects mixed job shapes', () => {
  const repository = 'SixbucksSolutions/localpost';
  const runId = '42055362444';
  const packagingRevision = 'a'.repeat(40);
  const run = { head_sha: packagingRevision, id: Number(runId), repository: { full_name: repository }, path: '.github/workflows/publish-containers.yml', event: 'workflow_dispatch', status: 'completed', conclusion: 'failure' };
  const jobs = { total_count: 5, jobs: ['qualify', 'publish', 'anonymous (ubuntu-26.04, amd64)', 'anonymous (ubuntu-26.04-arm, arm64)', 'promote'].map((name) => ({ name, status: 'completed', conclusion: name === 'qualify' ? 'skipped' : name === 'promote' ? 'failure' : 'success' })) };
  const check = (sourceJobs) => checkPromotionSource(run, sourceJobs, repository, runId, packagingRevision);
  check(jobs);
  for (let index = 0; index < jobs.jobs.length; index++) {
    for (const field of ['name', 'status', 'conclusion']) {
      const changed = structuredClone(jobs);
      changed.jobs[index][field] = 'wrong';
      assert.throws(() => check(changed));
    }
  }
  const mixed = structuredClone(jobs);
  mixed.jobs.push({ name: 'qualify (ubuntu-26.04, amd64)', status: 'completed', conclusion: 'success' });
  mixed.total_count++;
  assert.throws(() => check(mixed));
  const duplicate = structuredClone(jobs);
  duplicate.jobs[3] = duplicate.jobs[2];
  assert.throws(() => check(duplicate));
  assert.throws(() => check({ ...jobs, total_count: 6 }));
  assert.throws(() => check({ ...jobs, jobs: jobs.jobs.slice(1) }));
});

const acceptedQualification = {
  runId: '42056849583',
  packagingCommit: 'a'.repeat(40),
  imageIds: { amd64: `sha256:${'b'.repeat(64)}`, arm64: `sha256:${'c'.repeat(64)}` },
};

test('qualification reuse requires the accepted run, commit and both exact image IDs', () => {
  const candidate = { containerQualification: acceptedQualification };
  assert.equal(checkQualifiedRecord(candidate, acceptedQualification.runId), acceptedQualification);
  assert.throws(() => checkQualifiedRecord({}, acceptedQualification.runId));
  for (const runId of ['', '0', '../runs', '42056849584']) assert.throws(() => checkQualifiedRecord(candidate, runId));
  for (const packagingCommit of [undefined, '', 'bad', 'a'.repeat(39)]) {
    assert.throws(() => checkQualifiedRecord({ containerQualification: { ...acceptedQualification, packagingCommit } }, acceptedQualification.runId));
  }
  for (const imageIds of [undefined, {}, { amd64: acceptedQualification.imageIds.amd64 }, { ...acceptedQualification.imageIds, arm64: 'invalid' }]) {
    assert.throws(() => checkQualifiedRecord({ containerQualification: { ...acceptedQualification, imageIds } }, acceptedQualification.runId));
  }
});

test('qualification source is a successful same-repository manual qualification-only run', () => {
  const repository = 'SixbucksSolutions/localpost';
  const record = acceptedQualification;
  const run = { id: Number(record.runId), head_sha: record.packagingCommit, repository: { full_name: repository }, path: '.github/workflows/publish-containers.yml', event: 'workflow_dispatch', status: 'completed', conclusion: 'success' };
  const jobs = { total_count: 5, jobs: ['qualify (ubuntu-26.04, amd64)', 'qualify (ubuntu-26.04-arm, arm64)', 'publish', 'anonymous', 'promote'].map((name) => ({ name, status: 'completed', conclusion: name.startsWith('qualify ') ? 'success' : 'skipped' })) };
  const check = (source = run, sourceJobs = jobs, repo = repository, id = record.runId) => checkQualificationSource(source, sourceJobs, repo, id, record);
  check();
  for (const field of ['head_sha', 'path', 'event', 'status', 'conclusion', 'id']) assert.throws(() => check({ ...run, [field]: 'wrong' }));
  assert.throws(() => check({ ...run, repository: { full_name: 'another/repository' } }));
  assert.throws(() => check(run, jobs, 'another/repository'));
  assert.throws(() => check(run, jobs, repository, '42056849584'));
  for (let index = 0; index < jobs.jobs.length; index++) {
    for (const field of ['name', 'status', 'conclusion']) {
      const changed = structuredClone(jobs);
      changed.jobs[index][field] = 'wrong';
      assert.throws(() => check(run, changed));
    }
  }
  const duplicate = structuredClone(jobs);
  duplicate.jobs[1] = duplicate.jobs[0];
  assert.throws(() => check(run, duplicate));
  assert.throws(() => check(run, { ...jobs, total_count: 6 }));
  assert.throws(() => check(run, { ...jobs, jobs: jobs.jobs.slice(1) }));
});

test('retained image identity and labels bind to accepted packaging and source commits', () => {
  const { packagingCommit, imageIds } = acceptedQualification;
  const sourceCommit = 'd'.repeat(40);
  const identity = { architecture: 'amd64', imageId: imageIds.amd64 };
  const local = { Id: identity.imageId, Architecture: 'amd64', Os: 'linux', Config: { Labels: { 'org.opencontainers.image.version': '0.2.0', 'org.opencontainers.image.revision': sourceCommit, 'io.localpost.packaging-revision': packagingCommit } } };
  const check = (loaded = local, saved = identity, revision = packagingCommit, accepted = imageIds.amd64) => checkQualifiedImage(loaded, saved, 'amd64', revision, sourceCommit, accepted);
  check();
  for (const field of ['Id', 'Architecture', 'Os']) assert.throws(() => check({ ...local, [field]: 'wrong' }));
  for (const field of Object.keys(local.Config.Labels)) {
    const changed = structuredClone(local);
    changed.Config.Labels[field] = 'wrong';
    assert.throws(() => check(changed));
  }
  assert.throws(() => check(local, { ...identity, architecture: 'arm64' }));
  assert.throws(() => check(local, { ...identity, imageId: imageIds.arm64 }));
  assert.throws(() => check(local, identity, 'e'.repeat(40)));
  assert.throws(() => check(local, identity, packagingCommit, imageIds.arm64));
  // Development publication still validates freshly qualified image labels without a retained record.
  checkQualifiedImage(local, identity, 'amd64', packagingCommit, sourceCommit);
});

test('publication entry point loads retained images and exports their packaging revision', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'localpost-publish-test-'));
  const previousDirectory = process.cwd();
  const previousEnvironment = { ...process.env };
  const repositoryImage = 'ghcr.io/sixbuckssolutions/localpost';
  const nativeArchitecture = process.arch === 'arm64' ? 'arm64' : 'amd64';
  const sourceCommit = 'd'.repeat(40);
  const indexDigest = `sha256:${'e'.repeat(64)}`;
  const platformDigests = { amd64: `sha256:${'f'.repeat(64)}`, arm64: `sha256:${'a'.repeat(64)}` };
  const commands = [];
  let versionPublished = false;
  const index = { mediaType: 'application/vnd.oci.image.index.v1+json', manifests: Object.entries(platformDigests).map(([architecture, digest]) => ({ platform: { os: 'linux', architecture }, digest })) };
  const execute = () => { throw new Error('Only promisified execution is expected'); };
  execute[promisify.custom] = async (command, args) => {
    assert.equal(command, 'docker');
    commands.push(args);
    if (['load', 'tag', 'push', 'pull'].includes(args[0])) return { stdout: '' };
    if (args[0] === 'image' && args[1] === 'inspect') {
      const architecture = args[2].startsWith('localpost-qualified:') ? args[2].split(':').at(-1) : nativeArchitecture;
      return { stdout: JSON.stringify([{ Id: acceptedQualification.imageIds[architecture], Architecture: architecture, Os: 'linux', RepoDigests: [`${repositoryImage}@${indexDigest}`], Config: { Labels: { 'org.opencontainers.image.version': '0.2.0', 'org.opencontainers.image.revision': sourceCommit, 'io.localpost.packaging-revision': acceptedQualification.packagingCommit } } }]) };
    }
    assert.deepEqual(args.slice(0, 2), ['buildx', 'imagetools']);
    if (args[2] === 'create') {
      if (args[4] === `${repositoryImage}:0.2.0`) versionPublished = true;
      return { stdout: '' };
    }
    assert.equal(args[2], 'inspect');
    const reference = args.at(-1);
    if (reference === `${repositoryImage}:0.2.0` && !versionPublished) throw Object.assign(new Error('missing version'), { stderr: 'manifest unknown' });
    const architecture = reference.endsWith('-amd64') ? 'amd64' : reference.endsWith('-arm64') ? 'arm64' : undefined;
    const digest = architecture ? platformDigests[architecture] : indexDigest;
    return { stdout: args.includes('--raw') ? JSON.stringify(architecture ? { config: { digest: acceptedQualification.imageIds[architecture] } } : index) : `Digest: ${digest}` };
  };
  const originalExecFile = childProcess.execFile;
  childProcess.execFile = execute;
  syncBuiltinESMExports();
  try {
    process.chdir(directory);
    Object.assign(process.env, { RUNNER_TEMP: directory, GITHUB_ENV: path.join(directory, 'environment'), GITHUB_SHA: '9'.repeat(40), GITHUB_RUN_ID: '999', QUALIFIED_RUN_ID: acceptedQualification.runId, PROMOTE_RUN_ID: '', PUBLISH_CONTAINERS: 'true', ARCHITECTURE: nativeArchitecture });
    await mkdir('release');
    await writeFile('release/0.2.0-candidate.json', JSON.stringify({ version: '0.2.0', publicationStatus: 'published', qualification: { status: 'passed' }, sourceCommit, containerQualification: acceptedQualification }));
    for (const architecture of ['amd64', 'arm64']) {
      const qualified = path.join(directory, 'qualified', architecture);
      await mkdir(qualified, { recursive: true });
      await writeFile(path.join(qualified, 'identity.json'), JSON.stringify({ architecture, imageId: acceptedQualification.imageIds[architecture] }));
    }
    const { main } = await import('./publish-containers.mjs?publication-entry-test');
    await main('publish');
    const publication = JSON.parse(await readFile(path.join(directory, 'container-publication', 'publication.json')));
    assert.equal(publication.packagingCommit, acceptedQualification.packagingCommit);
    assert.deepEqual(Object.fromEntries(Object.entries(publication.platforms).map(([architecture, platform]) => [architecture, platform.imageId])), acceptedQualification.imageIds);
    await main('verify');
    assert.match(await readFile(process.env.GITHUB_ENV, 'utf8'), new RegExp(`PACKAGING_REVISION=${acceptedQualification.packagingCommit}`));
    assert.equal(commands.filter((args) => args[0] === 'load').length, 2);
    assert.equal(commands.filter((args) => args[0] === 'push').length, 2);
    assert.equal(commands.some((args) => args.includes('build')), false);
    versionPublished = false;
    commands.length = 0;
    process.env.QUALIFIED_RUN_ID = '';
    process.env.GITHUB_SHA = acceptedQualification.packagingCommit;
    await main('publish');
    assert.equal(commands.filter((args) => args[0] === 'load').length, 2);
  } finally {
    childProcess.execFile = originalExecFile;
    syncBuiltinESMExports();
    process.chdir(previousDirectory);
    for (const key of Object.keys(process.env)) if (!(key in previousEnvironment)) delete process.env[key];
    Object.assign(process.env, previousEnvironment);
    await rm(directory, { recursive: true, force: true });
  }
});
