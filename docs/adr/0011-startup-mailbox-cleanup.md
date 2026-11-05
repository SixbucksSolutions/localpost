# ADR 0011 — Clear the dedicated mailbox root on startup

- Status — Accepted for authorized v0.2.1 implementation
- Date — 2026-11-05
- Supersedes — Startup retention portions of [ADR 0004](0004-best-effort-lifecycle.md), [ADR 0008](0008-first-release-scope.md), and [ADR 0009](0009-service-created-mailboxes.md)

## Decision

The mailbox root is dedicated to one localpost daemon. On every startup, reserve
the port first. An occupied port fails before deletion. Before serving HTTP, warn
if the root contains stale entries and delete all its children.
Preserve the root itself. This applies after normal shutdown and forced termination.
The operator must ensure only one daemon uses a root, even across different
ports. The daemon does not lock the root.
Failure to complete startup cleanup must prevent the service from opening HTTP.

Unregistering a mailbox still leaves its files and directory until the next
startup. Agents may remove their own retired mailboxes sooner. Registrations
remain ephemeral. Operators must move data they need outside the root before a
restart. The HTTP API and its `/api/2026-11-05` date are unchanged.

The daemon MUST write only `.dat` message files for active registrations. Hidden
`.dat` staging files contain message bytes only. Mailbox directory creation is
permitted as needed. The daemon writes no probes, lock files, metadata, state,
or log files. Daemon diagnostics go to the console. Cleanup, mailbox creation,
and delivery surface write failures as they occur.

## Consequences

The startup root no longer accumulates orphaned mailboxes across runs, but
restart destroys unread deliveries and all other root children. Published v0.2.0
behavior and its release history remain unchanged.
