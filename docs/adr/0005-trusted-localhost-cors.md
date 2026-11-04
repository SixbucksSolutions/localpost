# ADR 0005: Trusted localhost, plain HTTP, and permissive CORS

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

The owner explicitly trusts localhost and prioritizes simple browser integration.
Different localhost ports still count as different browser origins. The agreed
JSON and binary media types require CORS preflight handling for cross-origin
browser POSTs.

## Decision

Use plain HTTP on loopback. Do not add authentication, upload tokens, TLS, or
origin allowlists. Send `Access-Control-Allow-Origin: *` on API responses,
including errors.

For mailbox listeners, permit `POST, OPTIONS` and the `Content-Type` request
header. Handle preflight `OPTIONS` without creating a message file. The admin
API uses the same permissive origin policy with methods matching its final
routes. Clients need no cookies or HTTP credentials.

## Consequences

Any local caller, and any webpage allowed to reach the service by its browser,
can use these endpoints. The service does not claim to isolate hostile callers.
It runs with the permissions of its ordinary user account.

CORS cannot override an application's CSP or browser local-network permission
requirements. A thin application-side forwarding route remains possible while
keeping file delivery centralized.

## Implementation note (M01.05)

The shared CORS wrapper advertises `POST, DELETE, OPTIONS` on the admin listener
and `POST, OPTIONS` on mailbox listeners. Preflights return `204` without a
registration or message write. Application errors retain the same wildcard
origin policy. The wrapper preserves the HTTP response controller used for
upload timeouts and cleanup.
