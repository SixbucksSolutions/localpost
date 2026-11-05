# ADR 0010 — Date-versioned API paths

- Status — Accepted for the authorized 0.2.0 implementation
- Date — 2026-11-05

## Authority

Terry requested date-based API paths during the authorized 0.2.0 release run so
customers can identify breaking changes and unsupported versions fail clearly.

## Decision

The 0.2.0 API prefix is `/api/2026-11-05`. This date identifies a fixed contract,
independent of the daemon release number or the current calendar date. Future
breaking contracts receive a new explicit date prefix.
Terry accepts the limit of one breaking contract revision per date. Additional
changes under an already-issued date must preserve that contract.

The public routes are:

- `POST /api/2026-11-05/registrations`
- `POST /api/2026-11-05/registrations/{id}/messages`
- `DELETE /api/2026-11-05/registrations/{id}`

Only the supported prefix is routed. Requests under unsupported `/api/`
prefixes and retired unversioned `/registrations` routes return HTTP `404` with
the structured error code `unsupported_api_version`. The error message names
`/api/2026-11-05` so clients can locate the current contract. No implicit latest
version alias or fallback is provided.

The wildcard CORS headers accompany version errors. Unsupported-version
`OPTIONS` requests fail in the same way; they must not report successful
preflight for a retired API. Supported-version preflight retains the existing
`204` behavior. Unrelated unknown paths retain `404 not_found`.

The OpenAPI paths, examples, client tooling, browser proof and release/migration
notices use the date prefix explicitly. Historical 0.1.0 evidence continues to
describe its original unversioned API.
