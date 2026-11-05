# Project vision

localpost gives agents a shared localhost post office: send bytes over HTTP and
find the same bytes in a file.

## The problem

An agent can operate a web UI yet still lack direct access to useful browser
diagnostics. Asking a person to copy errors or relay screenshots interrupts the
test-and-fix loop. Building a separate browser-to-disk collector in every project
duplicates transport, file handling, and lifecycle code.

localpost supplies that common delivery service. Browser diagnostics are the first
use case, but the payload contract is general.

## The intended experience

The person starts localpost once. An operator provisions a mailbox root and an agent requests a mailbox.
localpost creates a private child and returns its UUIDv4 ID and absolute path. Browser
JavaScript or another HTTP client posts messages to its route on the shared
listener. Complete files appear in the directory, ready for the agent to inspect.

The agent can repeat a bounded browser action, read the resulting diagnostics,
make a correction, and retest. Screenshots and application tests still serve
their own purposes; receiving a message does not prove a UI is correct.

When work ends, the agent unregisters the mailbox. The person remains responsible
for running the shared daemon. If it is unavailable, the agent reports that fact.

## Product principles

- **One implementation.** Projects use the same localpost service. Thin clients
  or forwarding adapters may help applications send messages, but they do not
  implement another collector or disk writer.
- **Opaque payloads.** localpost accepts bytes. It does not parse JSON, sanitize
  diagnostics, interpret errors, or execute received content.
- **Service-created mailboxes.** The operator provisions the root and permissions.
  The service creates each mailbox. The agent manages its delivered files and cleanup.
- **Simple operation.** One process, plain HTTP on loopback, no authentication,
  permissive CORS, no configuration file, and unprivileged ports.
- **Best effort.** Registrations are ephemeral. Delivery has visible success or
  failure, without persistent queues, retry machinery, or durability claims.
- **Complete-file handoff.** A final `.dat` filename denotes a complete message,
  making directory-based consumption straightforward.

## v0.2.0 scope

- A shared listener on TCP port `53147`, overridable with `--port`.
- Registration and unregistration of service-created directories under a configured root.
- One loopback HTTP listener with UUIDv4-addressed registrations.
- `application/octet-stream` and `application/json` message bodies.
- Byte-preserving delivery to `localpost_[UUIDv7].dat` files.
- Bounded uploads, clear HTTP outcomes, and bounded listener shutdown.
- Browser preflight handling with `Access-Control-Allow-Origin: *`.

## Outside the scope

localpost does not provide application instrumentation, a browser driver,
filesystem watchers, chat notifications, message interpretation, JSON schemas,
retention policies, delivery-file deletion, automatic daemon startup by agents,
or a production telemetry system.

Authentication, TLS, origin allowlists, durable registration recovery, replay,
and exactly-once delivery are also outside this version's agreed scope.

## What success looks like

- Multiple projects can use one daemon without copying its implementation.
- Each registered mailbox delivers only to its associated directory.
- A sender's accepted body can be compared byte for byte with its delivered file.
- A consuming agent never needs to read a partially published `.dat` file.
- Localpost runs under an ordinary user account without configuration files.
- Failure is explicit: unavailable daemon, invalid directory, oversized upload,
  stalled upload, or failed disk write does not become a false success.

## Current stage

The current foundation provides a Go executable, shared loopback listener, and
JSON mailbox registration, opaque message delivery, mailbox unregistration,
and permissive browser CORS. The core has HTTP integration, bounded robustness,
and race-instrumented verification. A headless Chrome demonstration on Windows
has exercised diagnosis, correction, and rechecking through delivered files.
The published [v0.1.0 release](releases/v0.1.0.md) has a recorded test matrix.
The dated v0.2.0 API and managed mailboxes are in development pending release.
**Go is the selected implementation language**, as recorded
in [ADR 0006](adr/0006-go-implementation-language.md), with Go 1.27.3 selected for
development. The accepted release packaging and target scope are recorded in
[ADR 0008](adr/0008-first-release-scope.md); the v0.2.0 decisions are recorded in [ADR 0009](adr/0009-service-created-mailboxes.md) and [ADR 0010](adr/0010-date-versioned-api.md).
