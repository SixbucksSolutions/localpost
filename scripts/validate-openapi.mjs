import { readFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';

const vendored = name => JSON.parse(readFileSync(new URL(`./openapi-schemas/${name}.json`, import.meta.url), 'utf8'));
const dialect = vendored('dialect');
const escape = key => key.replaceAll('~', '~0').replaceAll('/', '~1');
const child = (path, key) => `${path}/${escape(String(key))}`;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const methods = new Set(['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace', 'query']);
const schemaMaps = new Set(['properties', 'patternProperties', '$defs', 'dependentSchemas']);
const schemaArrays = new Set(['allOf', 'anyOf', 'oneOf', 'prefixItems']);
const schemaSingles = new Set(['items', 'contains', 'additionalProperties', 'unevaluatedProperties', 'unevaluatedItems', 'propertyNames', 'not', 'if', 'then', 'else', 'contentSchema']);

function ajv() {
  // OAS permits annotations, union types, and unconstrained tuple tails. Keep
  // strict keyword checking; relax only the corresponding schema-author lint.
  // The official schema also deliberately overlaps properties/patternProperties.
  const instance = new Ajv2020({ allErrors: true, strictTypes: false, strictTuples: false, allowMatchingProperties: true });
  addFormats(instance);
  // These OAS formats are annotations, not validation vocabularies. In
  // particular, do not pretend that a URI regexp validates every Unicode IRI.
  instance.addFormat('iri', true);
  instance.addFormat('media-range', true);
  for (const keyword of ['discriminator', 'xml', 'externalDocs', 'example']) {
    instance.addKeyword({ keyword, valid: true });
  }
  instance.addMetaSchema(vendored('meta'));
  instance.addMetaSchema(dialect);
  return instance;
}

const documentAjv = ajv();
const baseSchema = vendored('base');
const structureSchema = vendored('schema');
// Ajv 8 resolves a $dynamicRef whose anchor lives only in $defs against the
// document root. Bind the official schema-base override explicitly in memory;
// the vendored sources remain byte-for-byte intact and Schema Objects still
// validate against the official OAS 3.2 dialect (draft 2020-12).
function bindSchemaDialect(value) {
  if (!object(value)) return;
  if (value.$dynamicRef === '#meta') {
    delete value.$dynamicRef;
    value.$ref = `${baseSchema.$id}#/$defs/schema`;
  }
  for (const nested of Object.values(value)) {
    if (Array.isArray(nested)) nested.forEach(bindSchemaDialect);
    else bindSchemaDialect(nested);
  }
}
bindSchemaDialect(structureSchema);
documentAjv.addSchema(structureSchema);
const validateStructure = documentAjv.compile(baseSchema);

function assertValid(validate, value, label) {
  if (!validate(value)) throw new Error(`${label}: ${documentAjv.errorsText(validate.errors, { separator: '; ' })}`);
}

function resolve(document, ref) {
  if (typeof ref !== 'string' || !ref.startsWith('#/')) throw new Error(`Only local JSON Pointer references are supported: ${ref}`);
  let path;
  try { path = decodeURIComponent(ref.slice(2)); } catch { throw new Error(`Malformed reference: ${ref}`); }
  let value = document;
  for (const token of path.split('/')) {
    if (/~(?![01])/u.test(token)) throw new Error(`Malformed reference: ${ref}`);
    const key = token.replaceAll('~1', '/').replaceAll('~0', '~');
    if (!object(value) && !Array.isArray(value) || !Object.hasOwn(value, key)) throw new Error(`Unresolved reference: ${ref}`);
    value = value[key];
  }
  return value;
}

function inventory(document) {
  const schemas = new Map();
  const examples = [];
  const references = [];
  const ref = node => { if (object(node) && Object.hasOwn(node, '$ref')) references.push(node.$ref); };
  const schema = (node, path) => {
    schemas.set(path, node);
    if (!object(node)) return;
    ref(node);
    if (Object.hasOwn(node, '$dynamicRef')) throw new Error(`Application-schema $dynamicRef is unsupported: ${path}`);
    if (Object.hasOwn(node, 'example')) examples.push([path, node.example, `${path}/example`]);
    for (const [index, value] of (node.examples ?? []).entries()) examples.push([path, value, `${path}/examples/${index}`]);
    for (const key of schemaMaps) {
      for (const [name, value] of Object.entries(node[key] ?? {})) schema(value, child(child(path, key), name));
    }
    for (const key of schemaArrays) {
      for (const [index, value] of (node[key] ?? []).entries()) schema(value, child(child(path, key), index));
    }
    for (const key of schemaSingles) {
      if (Object.hasOwn(node, key)) schema(node[key], child(path, key));
    }
  };
  const visit = (node, path) => {
    if (Array.isArray(node)) return node.forEach((value, index) => visit(value, child(path, index)));
    if (!object(node)) return;
    ref(node);
    for (const schemaKey of ['schema', 'itemSchema']) if (Object.hasOwn(node, schemaKey)) {
      schema(node[schemaKey], child(path, schemaKey));
      if (schemaKey !== 'schema') continue;
      if (Object.hasOwn(node, 'example')) examples.push([child(path, schemaKey), node.example, child(path, 'example')]);
      for (const [name, entry] of Object.entries(node.examples ?? {})) {
        ref(entry);
        const example = entry.$ref ? resolve(document, entry.$ref) : entry;
        if (Object.hasOwn(example, 'value')) examples.push([child(path, 'schema'), example.value, child(child(path, 'examples'), name)]);
        if (Object.hasOwn(example, 'dataValue')) examples.push([child(path, 'schema'), example.dataValue, child(child(path, 'examples'), name)]);
      }
    }
    for (const [key, value] of Object.entries(node)) {
      if (['schema', 'itemSchema', 'example', 'examples', 'value', 'dataValue'].includes(key) || key.startsWith('x-')) continue;
      if (path === '#/components' && key === 'schemas') {
        for (const [name, entry] of Object.entries(value)) schema(entry, child(child(path, key), name));
      } else visit(value, child(path, key));
    }
  };
  visit(document, '#');
  for (const reference of references) resolve(document, reference);
  return { schemas, examples, references };
}

function buildValueValidator(document, schemas) {
  const instance = ajv();
  const ids = new Map([...schemas.keys()].map((path, index) => [path, `urn:localpost:schema:${index}`]));
  const rewrite = value => {
    if (!object(value)) return value;
    return Object.fromEntries(Object.entries(value).map(([key, entry]) => {
      if (key === '$ref') {
        const target = resolve(document, entry);
        const path = [...schemas].find(([, schema]) => schema === target)?.[0];
        if (!path) throw new Error(`Reference does not target a Schema Object: ${entry}`);
        return [key, ids.get(path)];
      }
      // Only schema keywords introduce schemas. Keys within property maps are
      // user-defined names, including legal names such as '$ref' and 'default'.
      if (schemaMaps.has(key)) return [key, Object.fromEntries(Object.entries(entry).map(([name, schema]) => [name, rewrite(schema)]))];
      if (schemaArrays.has(key)) return [key, entry.map(rewrite)];
      if (schemaSingles.has(key)) return [key, rewrite(entry)];
      // Annotation/example payloads and other keyword values are untouched.
      return [key, entry];
    }));
  };
  for (const [path, schema] of schemas) instance.addSchema(rewrite(schema), ids.get(path));
  // Compile every schema even when it has no example or service fixture.
  for (const id of ids.values()) instance.getSchema(id);
  return (ref, value) => {
    const target = resolve(document, ref);
    const path = [...schemas].find(([, schema]) => schema === target)?.[0];
    if (!path) throw new Error(`Reference does not target a modeled Schema Object: ${ref}`);
    assertValid(instance.getSchema(ids.get(path)), value, `Value for ${ref}`);
  };
}

export function createValueValidator(document) {
  return buildValueValidator(document, inventory(document).schemas);
}

function checkSemantics(document) {
  const operationIds = new Set();
  const pathShapes = new Set();
  const parameters = entries => {
    const seen = new Set();
    return (entries ?? []).map(entry => {
      const value = entry.$ref ? resolve(document, entry.$ref) : entry;
      const key = `${value.in}:${value.name}`;
      if (seen.has(key)) throw new Error(`Duplicate parameter: ${key}`);
      seen.add(key);
      return [key, value];
    });
  };
  for (const [path, rawItem] of Object.entries(document.paths ?? {})) {
    if (path.startsWith('x-')) continue;
    const shape = path.replace(/\{[^{}]+\}/gu, '{}');
    if (pathShapes.has(shape)) throw new Error(`Duplicate templated path: ${path}`);
    pathShapes.add(shape);
    const item = rawItem.$ref ? resolve(document, rawItem.$ref) : rawItem;
    const base = parameters(item.parameters);
    for (const [method, operation] of Object.entries(item)) {
      if (!methods.has(method)) continue;
      if (operation.operationId) {
        if (operationIds.has(operation.operationId)) throw new Error(`Duplicate operationId: ${operation.operationId}`);
        operationIds.add(operation.operationId);
      }
      const merged = new Map([...base, ...parameters(operation.parameters)]);
      const names = [...path.matchAll(/\{([^{}]+)\}/gu)].map(match => match[1]);
      for (const name of names) if (!merged.has(`path:${name}`)) throw new Error(`Missing path parameter ${name}: ${method} ${path}`);
      for (const value of merged.values()) if (value.in === 'path' && !names.includes(value.name)) throw new Error(`Unused path parameter ${value.name}: ${method} ${path}`);
    }
  }
}

export function validateDocument(document) {
  assertValid(validateStructure, document, 'Invalid OpenAPI 3.2 document');
  const found = inventory(document);
  checkSemantics(document);
  const validate = buildValueValidator(document, found.schemas);
  for (const [ref, value, label] of found.examples) {
    try { validate(ref, value); } catch (error) { throw new Error(`Invalid example ${label}: ${error.message}`); }
  }
  return { references: found.references.length, examples: found.examples.length };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const path = process.argv[2] ?? fileURLToPath(new URL('../openapi.json', import.meta.url));
    const result = validateDocument(JSON.parse(readFileSync(path, 'utf8')));
    console.log(`OpenAPI 3.2 validation passed (${result.references} local references; ${result.examples} modeled examples).`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
