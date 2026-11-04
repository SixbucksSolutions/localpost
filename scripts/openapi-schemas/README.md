# Vendored OpenAPI 3.2 schemas

These unmodified JSON files are published by the OpenAPI Initiative in the
[OpenAPI Specification repository](https://github.com/OAI/OpenAPI-Specification).
They are licensed under Apache License 2.0; the upstream license is retained in
`LICENSE`. `sources.json` records the source URLs and SHA-256 of every vendored
file. The schema revision is 2026-08-30; the OAS dialect/meta revision is
2026-02-26. These are the official 3.2 schemas, supporting the contract's 3.2.1
version without conversion to 3.1.

`validate-openapi.mjs` uses Ajv's draft 2020-12 implementation, `ajv-formats`,
and the official OAS dialect/meta schema. It checks document structure, Schema
Objects, local JSON Pointer references, operation IDs, path/parameter identity,
and examples attached to modeled schemas (including OAS 3.2 `dataValue`). It also compiles every modeled schema,
including schemas without examples. Arbitrary example payloads are data, and
schema-less opaque uploads are not parsed or assigned an invented JSON schema.

## Ajv compatibility binding

Ajv 8.17.1 does not correctly select the `meta` dynamic anchor when that anchor
is defined only inside the official schema-base's `$defs/schema`: its generated
fallback validates against the surrounding root instead. This causes valid OAS
Schema Objects to be rejected as if they were Header/Parameter objects.

The loader therefore binds the structure schema's `$dynamicRef: "#meta"` sites
to the exact `$ref` target specified by schema-base's override:
`https://spec.openapis.org/oas/3.2/schema-base/2026-08-30#/$defs/schema`.
Only the in-memory structure schema is adapted; the vendored files and official
dialect/meta schemas are unchanged. This is a fixed-dialect binding, not a
general implementation of arbitrary dynamic-anchor overrides. The base schema
continues to enforce the official OAS dialect URL, including on Schema Objects.
Tests prove that invalid nested schemas are rejected and that 2020-12
`prefixItems`/`unevaluatedProperties` constraints are enforced on example values.

Ajv's strict keyword checks remain enabled. The official schemas require
`allowMatchingProperties`, and legal JSON Schema union types and unconstrained
tuple tails require relaxing `strictTypes` and `strictTuples`. The OAS annotation
keywords are registered explicitly. The OAS `iri` and `media-range` formats are
treated as annotations; other supported formats use `ajv-formats` assertions.

The repository check intentionally accepts only local JSON Pointer references
and the fixed OAS 3.2 dialect. External reference fetching, custom dialects and
dynamic-anchor application-schema references are outside this check's scope.
