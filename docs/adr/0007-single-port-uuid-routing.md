# ADR 0007: One listener with UUIDv4 registration routing

- **Status:** Accepted
- **Date:** 2026-11-04
- **Supersedes:** the per-registration listener/port portions of ADRs 0001 and 0002.
- **Authority:** Terry selected the single-port M03 refactor after reviewing the
  completed prototype.

## Decision

Use one loopback HTTP listener for control and message requests. Keep the
default port `53147` and the `--port` override. A registration receives a UUIDv4
identity rather than a dedicated port:

- `POST /registrations` returns HTTP `200` with `id`.
- `POST /registrations/{id}/messages` delivers the opaque body.
- `DELETE /registrations/{id}` drains and removes that logical mailbox.

The UUID is a routing identity, not authentication. Remove the prototype's
per-registration HTTP servers, listener allocation, `port` response field, and
bare message route. Do not maintain a second compatibility service.

## Rationale

The old ports served as routing keys. Trusted localhost HTTP requests can carry
the same identity in a path, with less listener ownership and connection
management. The explicit ID keeps routing metadata outside the stored body.

## Consequences

Per-registration admission, drain, cancellation, and publication cutoff remain
necessary. Deleting one registration must not stop the shared listener or
unrelated registrations. Control-read limits and upload-idle limits remain
distinct; shared connections must not retain another route's deadline or abort.

Keep UUIDv7 filenames, byte preservation, resource limits, wildcard CORS, the
agent-owned directory, and best-effort stateless behavior. Existing registrations
disappear on restart as before; senders re-register and use the new paths. No
delivered-file migration is needed. This is an unreleased API cutover.
