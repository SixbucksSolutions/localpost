# ADR 0004: Ephemeral registration and best-effort lifecycle

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

localpost is a simple delivery helper. The requested service is best effort and
stateless across restarts, rather than a durable message broker.

## Decision

Keep registrations only in memory. Do not recover them on restart. Do not add
persistent queues, automatic retries, replay, or deduplication.

A successful delivery response means the complete file was published. It does
not guarantee survival of a power loss. If a response is lost, the sender may
not know whether the file exists. A retry can create a second delivery.

Unregistration stops new uploads, gives accepted uploads a bounded opportunity
to finish, then closes the listener and removes its registration. Abort remaining
incomplete uploads without publishing them. Preserve completed files. Use a
shutdown window of at most 30 seconds; specify exact timer behavior before
implementation.

## Consequences

"Stateless" permits in-memory routing and active request state but excludes
persistent service recovery. Agents register again after a restart and own
delivered-file retention. A crash can interrupt requests without a response.
Normal shutdown should perform bounded cleanup without claiming crash recovery.
