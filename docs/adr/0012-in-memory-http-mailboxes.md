# ADR 0012 — Prefer existing components for in-memory HTTP mailboxes

- Status — Proposed for Terry review under #0043
- Date — 2026-11-05

## Recommendation

Use ephemeral, per-subscription in-memory messages with HTTP retrieval, favoring
an established storage engine over a new queue implementation. The authorized
[trade study](../studies/0043-in-memory-message-brokers.md) identifies Valkey plus
Webdis as the first adoption candidate, conditional on accepting two Linux or
container components and a command-oriented API. **RECOMMENDED.** Mosquitto is
the native-Windows alternative if its MQTT consumption model is acceptable.

Treat asynchronous push as a separate capability whose value can be established
by an actual receiving workflow. A small client or API adapter may be justified
for ergonomics, while queue behavior stays with the selected established engine.

The move from filesystem delivery is a breaking product and API change. The proposal establishes the direction
before selecting routes, transport details, limits or release version.

## Why the direction changed

Notification-only push offers a portable wake-up, but the consumer still needs
the file contents. Filesystem watchers also require reconciliation when events
are lost or coalesced. An HTTP hint would carry a similar recovery obligation.
[Node watch documentation](https://nodejs.org/api/fs.html#caveats), [Linux inotify manual](https://man7.org/linux/man-pages/man7/inotify.7.html)

Removing filesystem delivery addresses the underlying consumer dependency.
For the HTTP candidate, browser JavaScript and native clients use the same
retrieval interface. The service no longer needs mailbox roots, permission provisioning,
shared container paths, temporary message files, hard-link publication,
filesystem cleanup or the startup mailbox reaper.

The inspected pilot waits for its browser action to finish and then reads a
bounded snapshot. Other inspected development integrations follow similar
bounded action-and-read workflows. HTTP retrieval fits that usage without
requiring a persistent push receiver. This was a bounded inspection, not an
exhaustive census of possible consumers. [Checked-in browser proof](../../examples/browser-diagnostics/browser-check.mjs)

A browser dashboard receiving native-tool diagnostics is a credible additional
consumer. Plain HTTP retrieval already removes its filesystem requirement.
Asynchronous delivery would add value when a real workflow needs immediate,
unattended updates.

## Tradeoffs to accept explicitly

- Unexported messages disappear immediately when the process exits or crashes.
  v0.2.1 already deletes old files at the next startup, but files can currently
  be recovered after a crash and before restart.
- Clients must export evidence that needs to outlive the service. Localpost will
  no longer create the reviewable `.dat` artifacts for them.
- Upload success will mean acceptance into volatile memory. It currently means
  complete-file publication.
- Current DELETE retains files. A memory-only subscription needs an explicit
  rule for discarding its messages when it is removed.
- Filesystem exposure is reduced. HTTP read access expands the existing trusted
  localhost interface, and a mailbox identifier becomes a read capability under
  the current unauthenticated policy.

## Simplicity constraints for the next decision

Expected traffic is small informational messages, and Terry's host has 64 GB of
RAM. There is no observed capacity problem motivating elaborate quotas. Terry
approved a default message-storage budget with a `--max-mem` command-line override
accepting human-readable values such as `128M` and `4G`. Select the default,
define the units and bound retained-message overhead during the API design.
Preserve bounded uploads.

For the HTTP candidate, prefer non-destructive retrieval with an explicit clear
or subscription removal. Choosing Mosquitto requires accepting its consuming
session queues or a separately specified retained-topic convention.
A failed read response can then be retried without silently consuming a message.
Avoid per-reader cursors, exclusive claims, application-level delivery acknowledgements, durable
queues and automatic recovery unless a concrete use case requires them.

Keep message bytes opaque. Prefer JSON control responses on the HTTP path. Preserve the
operator-owned lifecycle and instance policy. The revised daemon would write no
files at all. Operator wrappers and clients remain responsible for their own
logs and exported evidence.

## CLI client in scope

Terry requested a CLI client alongside the in-memory HTTP service. It should be
a thin client of the same public HTTP API used by browsers on the HTTP path.
Adopting MQTT means explicitly accepting its protocol and client-library model.
Existing CLI tools
should be evaluated before adding a custom command wrapper. The selected product
may expose different flag names and output framing, which require acceptance.
This includes accepting an equivalent native storage-budget setting in place of
a custom `--max-mem` wrapper.

- Cover registration, sending, reading, clearing messages and removing a subscription.
- Accept message bytes from standard input and use stdout for initial output.
- Provide machine-readable control results, diagnostic errors on stderr and stable exit codes.
- Let callers redirect or pipe stdout into their own files or directories. Exported artifacts belong to the caller and survive daemon restart.

Exact commands, output framing, packaging and read/removal behavior belong in
the API and CLI contract design. The CLI uses the daemon as the shared source
of message state.

## Handoff

Approve or reject the memory-only direction, the immediate-loss tradeoff and
the adoption candidate. Review the trade study before authorizing implementation.
After approval, define the small retrieval, retention and removal contract,
including a new API date if retaining the localpost HTTP contract, before
implementation. A direct adoption uses its native broker protocol. Assess push against that
contract only when a concrete latency or unattended-consumer need appears.