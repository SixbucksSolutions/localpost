# ADR 0002: Unprivileged loopback listeners and zero configuration files

- **Status:** Accepted
- **Update:** per-registration listener allocation is superseded by [ADR 0007](0007-single-port-uuid-routing.md); the shared loopback port, override, and ordinary-user operation remain.
- **Date:** 2026-11-04

## Context

The service must run as an ordinary user with minimal setup. Agents need a known
admin endpoint, and the person running localpost needs a simple way to resolve
a port conflict.

## Decision

Bind the admin API to `127.0.0.1:53147`. Accept `--port` as its command-line
override. Require an unprivileged explicit admin port in the range 1024-65535.
Allocate mailbox listeners on available unprivileged loopback TCP ports.

Use no configuration file. Fail clearly if the requested admin port cannot be
bound; do not silently switch ports. Do not require privilege elevation.

If no localpost service is listening, agents report it to the person responsible
for running the daemon. They do not start localpost or a fallback automatically.

## Consequences

An override must be communicated to clients; discovery is not automatic. Ports
are reserved by opening the listener, not by a separate availability probe.
IPv6 listener support has not been selected for the first version.
