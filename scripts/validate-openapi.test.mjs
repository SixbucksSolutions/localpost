import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { createValueValidator, validateDocument } from './validate-openapi.mjs';

const source = JSON.parse(readFileSync(new URL('../openapi.json', import.meta.url), 'utf8'));
const copy = () => structuredClone(source);
const registrationMedia = document => document.paths['/api/2026-11-05/registrations'].post.requestBody.content['application/json'];

test('vendored schema and license bytes match recorded provenance', () => {
  const sources = JSON.parse(readFileSync(new URL('./openapi-schemas/sources.json', import.meta.url), 'utf8'));
  for (const source of sources) {
    const bytes = readFileSync(new URL(`./openapi-schemas/${source.file}`, import.meta.url));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), source.sha256, source.file);
    assert.match(source.url, /^https:\/\//u);
  }
});

test('canonical OpenAPI 3.2.1 contract, refs and modeled examples pass', () => {
  assert.equal(source.openapi, '3.2.1');
  assert.deepEqual(validateDocument(source), { references: 96, examples: 10 });
  assert.equal(source.info.version, '0.2.1');
  const validate = createValueValidator(source);
  validate('#/components/schemas/RegistrationRequest', {});
  assert.throws(() => validate('#/components/schemas/RegistrationRequest', { directory: '' }), /additional properties|maxProperties/);
  assert.throws(() => validate('#/components/schemas/RegistrationRequest', { directory: '/tmp', extra: 1 }), /additional properties/);
  assert.throws(() => validate('#/components/schemas/RegistrationId', 'not-a-uuid'), /pattern|uuid/);
});

test('rejects malformed JSON through the actual CLI', () => {
  const directory = mkdtempSync(join(tmpdir(), 'localpost-openapi-json-'));
  try {
    const path = join(directory, 'invalid.json');
    writeFileSync(path, '{');
    const result = spawnSync(process.execPath, ['scripts/validate-openapi.mjs', path], { encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /JSON|property name/);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test('rejects wrong versions and invalid document structure', () => {
  for (const version of ['3.1.1', '3.2', 'invalid']) {
    const document = copy();
    document.openapi = version;
    assert.throws(() => validateDocument(document), /Invalid OpenAPI/);
  }
  const document = copy();
  delete document.info.title;
  assert.throws(() => validateDocument(document), /title/);
});

test('rejects unresolved, nonlocal, malformed and nonschema refs', () => {
  for (const ref of ['#/components/schemas/Missing', 'https://example.test/schema', '#/components/schemas/~2bad']) {
    const document = copy();
    registrationMedia(document).schema.$ref = ref;
    assert.throws(() => validateDocument(document), /reference|Reference/);
  }
  const document = copy();
  registrationMedia(document).schema.$ref = '#/info';
  assert.throws(() => validateDocument(document), /Schema Object/);
});

test('official dialect rejects invalid top-level and nested Schema Objects', () => {
  for (const invalid of [{ type: 'not-a-type' }, { type: 'object', properties: { nested: { minLength: -1 } } }, { type: 'array', prefixItems: 'wrong' }]) {
    const document = copy();
    document.components.schemas.Bad = invalid;
    assert.throws(() => validateDocument(document), /Invalid OpenAPI/);
  }
  const document = copy();
  document.components.schemas.Bad = { $schema: 'https://json-schema.org/draft-07/schema#', type: 'string' };
  assert.throws(() => validateDocument(document), /Invalid OpenAPI/);
});

test('draft 2020-12 prefixItems and unevaluatedProperties are enforced', () => {
  const document = copy();
  document.components.schemas.Tuple = {
    type: 'array', prefixItems: [{ type: 'string' }, { type: 'integer' }], items: false,
    examples: [['first', 2]],
  };
  document.components.schemas.Composed = {
    type: 'object', allOf: [{ properties: { known: { type: 'string' } } }], unevaluatedProperties: false,
    examples: [{ known: 'yes' }],
  };
  validateDocument(document);
  const validate = createValueValidator(document);
  assert.throws(() => validate('#/components/schemas/Tuple', ['first', 'wrong']), /integer/);
  assert.throws(() => validate('#/components/schemas/Tuple', ['first', 2, 3]), /items/);
  assert.throws(() => validate('#/components/schemas/Composed', { extra: 1 }), /unevaluated/);
});

test('OpenAPI 3.2 itemSchema is validated, not ignored', () => {
  const document = copy();
  registrationMedia(document).itemSchema = { type: 'not-a-type' };
  assert.throws(() => validateDocument(document), /Invalid OpenAPI/);
});

test('schemas without examples are compiled and false schemas reject values', () => {
  const document = copy();
  document.components.schemas.Unused = { type: 'string', pattern: '[' };
  assert.throws(() => validateDocument(document), /regular expression/);
  document.components.schemas.Unused = false;
  validateDocument(document);
  const validate = createValueValidator(document);
  assert.throws(() => validate('#/components/schemas/Unused', null), /boolean schema is false/);
});

test('rejects invalid media and nested schema examples', () => {
  const document = copy();
  registrationMedia(document).examples.empty.value.directory = 123;
  assert.throws(() => validateDocument(document), /Invalid example/);
  const nested = copy();
  nested.components.schemas.DestinationDirectory.examples = ['relative/path'];
  assert.throws(() => validateDocument(nested), /Invalid example/);
});

test('OpenAPI 3.2 dataValue examples are checked, including referenced examples', () => {
  const document = copy();
  registrationMedia(document).examples.empty = { dataValue: {} };
  assert.equal(validateDocument(document).examples, validateDocument(source).examples);
  registrationMedia(document).examples.empty.dataValue.directory = 123;
  assert.throws(() => validateDocument(document), /Invalid example/);
  document.components.examples = { InvalidDirectory: { dataValue: { directory: 123 } } };
  registrationMedia(document).examples.empty = { $ref: '#/components/examples/InvalidDirectory' };
  assert.throws(() => validateDocument(document), /Invalid example/);
});

test('schema map names are data while their values remain schemas', () => {
  const document = copy();
  const id = source.components.schemas.RegistrationId.examples[0];
  document.components.schemas.KeywordProperties = {
    type: 'object',
    properties: {
      $ref: { type: 'string' },
      default: { $ref: '#/components/schemas/RegistrationId' },
      examples: { $ref: '#/components/schemas/RegistrationId' },
      properties: { $ref: '#/components/schemas/RegistrationId' },
    },
    examples: [{ $ref: 'literal data', default: id, examples: id, properties: id }],
  };
  validateDocument(document);
  const validate = createValueValidator(document);
  validate('#/components/schemas/KeywordProperties', { $ref: 'text', default: id });
  for (const name of ['default', 'examples', 'properties']) {
    assert.throws(() => validate('#/components/schemas/KeywordProperties', { [name]: 'bad-id' }), /pattern|uuid/);
  }
});

test('application-schema dynamic refs fail explicitly without changing semantics', () => {
  const document = copy();
  document.components.schemas.Dynamic = { $dynamicAnchor: 'node', $dynamicRef: '#node' };
  assert.throws(() => validateDocument(document), /Application-schema \$dynamicRef is unsupported/);
});

test('example JSON and opaque upload annotations remain opaque', () => {
  const document = copy();
  document.components.schemas.Arbitrary = { type: 'object', examples: [{ $ref: 'not-a-schema-ref' }] };
  const content = document.paths['/api/2026-11-05/registrations/{id}/messages'].post.requestBody.content;
  for (const media of Object.values(content)) {
    assert.equal(media.schema, undefined);
    media.example = 'opaque bytes, not JSON: {';
  }
  validateDocument(document);
});

test('rejects duplicate operation IDs, parameter identities and templated paths', () => {
  const duplicate = copy();
  duplicate.paths['/api/2026-11-05/registrations'].options.operationId = 'registerMailbox';
  assert.throws(() => validateDocument(duplicate), /Duplicate operationId/);
  const parameter = copy();
  parameter.paths['/api/2026-11-05/registrations/{id}'].parameters.push({ $ref: '#/components/parameters/RegistrationId' });
  assert.throws(() => validateDocument(parameter), /Duplicate parameter/);
  const path = copy();
  path.paths['/api/2026-11-05/registrations/{other}'] = {};
  assert.throws(() => validateDocument(path), /Duplicate templated path/);
});

test('rejects missing path parameter declarations', () => {
  const document = copy();
  document.paths['/api/2026-11-05/registrations/{id}'].parameters = [];
  assert.throws(() => validateDocument(document), /Missing path parameter/);
});
