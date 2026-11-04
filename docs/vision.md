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

The person starts localpost once. An agent prepares a private working directory
and requests a mailbox. localpost returns a dedicated listening port. Browser
JavaScript or another HTTP client posts messages to that port. Complete files
appear in the directory, ready for the agent to inspect.

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
- **Agent-owned destinations.** The requesting agent creates the directory,
  supplies an absolute path, and manages permissions and delivered files.
- **Simple operation.** One process, plain HTTP on loopback, no authentication,
  permissive CORS, no configuration file, and unprivileged ports.
- **Best effort.** Registrations are ephemeral. Delivery has visible success or
  failure, without persistent queues, retry machinery, or durability claims.
- **Complete-file handoff.** A final `.dat` filename denotes a complete message,
  making directory-based consumption straightforward.

## First-version scope

- An admin listener on TCP port `53147`, overridable with `--port`.
- Registration and unregistration of agent-selected directories.
- A dedicated loopback HTTP listener for each registration.
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

The M01.01 foundation provides a Go executable and loopback admin listener.
Mailbox registration and delivery are still to be implemented; there is no
packaged release yet. **Go is the selected implementation language**, as recorded
in [ADR 0006](adr/0006-go-implementation-language.md), with Go 1.27.3 selected for
development. Release packaging and targets, exact API routes, and remaining
protocol details are open; see the [ADR ledger](adr/README.md).
