# ADR 0012 — Replace filesystem delivery with in-memory HTTP mailboxes

- Status — Proposed for Terry review under #0043
- Date — 2026-11-05

## Recommendation

Move localpost to ephemeral, per-subscription in-memory messages with HTTP
retrieval. **RECOMMENDED.** Treat asynchronous push as a separate follow-up
capability whose value can be established by an actual receiving workflow.

This is a breaking product and API change. The proposal establishes the direction
before selecting routes, transport details, limits or release version.

## Why the direction changed

Notification-only push offers a portable wake-up, but the consumer still needs
the file contents. Filesystem watchers also require reconciliation when events
are lost or coalesced. An HTTP hint would carry a similar recovery obligation.
[Node watch documentation](https://nodejs.org/api/fs.html#caveats), [Linux inotify manual](https://man7.org/linux/man-pages/man7/inotify.7.html)

Removing filesystem delivery addresses the underlying consumer dependency.
Browser JavaScript can retrieve messages, and native clients use the same HTTP
workflow. The service no longer needs mailbox roots, permission provisioning,
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

Prefer non-destructive retrieval with an explicit clear or subscription removal.
A failed read response can then be retried without silently consuming a message.
Avoid per-reader cursors, exclusive claims, delivery acknowledgements, durable
queues and automatic recovery unless a concrete use case requires them.

Keep message bytes opaque and keep control responses JSON-based. Preserve the
operator-owned lifecycle and instance policy. The revised daemon would write no
files at all. Operator wrappers and clients remain responsible for their own
logs and exported evidence.

## Handoff

Approve or reject the memory-only direction and the immediate-loss tradeoff.
After approval, define the small retrieval, retention and removal contract,
including a new API date, before implementation. Assess push against that
contract only when a concrete latency or unattended-consumer need appears.