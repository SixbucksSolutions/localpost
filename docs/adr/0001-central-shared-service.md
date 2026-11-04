# ADR 0001: One shared service with a mailbox per registration

- **Status:** Accepted
- **Update:** per-registration listener/port details are superseded by [ADR 0007](0007-single-port-uuid-routing.md); the original decision below is retained as history.
- **Date:** 2026-11-04

## Context

Several projects need browser-to-disk diagnostics. Independent collectors duplicate
the transport and writer. The requested product is a shared localhost post office
that delivers messages where agents can find them.

## Decision

One localpost process hosts an admin API and a dedicated HTTP listening port for
each approved registration. The agent supplies an existing absolute directory
and remains responsible for its creation, permissions, and delivered files.

Registration succeeds with `200` only after the dedicated listener is open.
The response identifies the registration and port. The admin API also unregisters
mailboxes. localpost validates directory usability but never creates or repairs
the destination.

Applications can have thin senders or forwarding adapters. Collection and file
publication remain in localpost; projects do not maintain competing copies.

## Consequences

Mailbox routing is held in memory. Each mailbox owns its destination mapping and
listener lifecycle. Exact API paths and identity formats remain open. A directory
can become unusable after registration, so delivery-time failures stay explicit.
