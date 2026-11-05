# ADR 0009 — Service-created mailboxes for 0.2.0

- **v0.2.1 update:** startup retention is superseded by [ADR 0011](0011-startup-mailbox-cleanup.md).
- Status — Accepted for the authorized 0.2.0 implementation, pending verification
- Date — 2026-11-05
- Supersedes the caller-created directory responsibility in ADR 0001

## Authority

Terry approved a managed-mailbox release, the story estimates, and an AFK run
through independent review, testing, publication and deployment. Terry explicitly
approved omitting process IDs from registration. Independent review remains a
gate between dependent stories. Terry retains control of Completed cards.

## Registration

The route names are retained under the explicit `/api/2026-11-05` prefix defined
in [ADR 0010](0010-date-versioned-api.md). `POST /api/2026-11-05/registrations` requires
`Content-Type: application/json` and one empty JSON object `{}`. Empty bodies,
`null`, arrays, multiple objects and objects containing fields are rejected with
`400 invalid_request`. In particular, the former `directory` field is rejected.
The existing 64 KiB control-body limit and content-type handling continue.

The service generates one lowercase canonical RFC-variant UUIDv4, creates a new
directory with that basename under its configured root, and returns `200` with
exactly two fields, `id` and `directory`. `directory` is the absolute service-side
path. The returned ID selects the existing message and deletion routes.

Creation must complete before registration becomes visible. An existing path is
never adopted or overwritten. Allocation retries UUID collisions a bounded
number of times. Filesystem or exhausted-collision failures return
`500 mailbox_allocation_failed`; exhausted registration capacity remains
`503 registration_limit`, and service shutdown remains `503 unavailable`.
An allocation failure leaves no active registration or consumed capacity.

## Root and permissions

Server startup requires `--mailbox-root` with an existing absolute directory.
The service resolves the root to an absolute canonical path and verifies its
write/remove capability before opening the listener. The operator provisions the
root and its permissions. `--help` and `--version` continue to work independently
of a mailbox root. The listener defaults to `127.0.0.1:53147` with `--port` as its
override. There is no configuration file.

The Windows service installer provisions `C:\ProgramData\localpost\mailboxes`
and supplies it through the flag. Its inherited ACL grants the service identity
write access and the intended agent account read, write and cleanup access.
Ownership alone does not establish those access rights. New Windows mailbox
directories and delivery files inherit the configured ACL.

POSIX mailbox directories use private `0700` permissions, and delivery files
retain `0600`. Linux and Docker deployments run the daemon under the same UID
as their consuming agent. This preserves the existing private-file model.
Docker exposes the root at the same absolute path inside and outside the
container so the returned path can be used directly by the host agent.

## Lifecycle

Message uploads preserve the existing opaque bodies, UUIDv7 filenames, limits,
atomic publication and best-effort behavior. Wildcard CORS and the trusted
loopback model continue. Registration contains no process or account identity.

`DELETE /api/2026-11-05/registrations/{id}` stops admission and drains accepted work using the
existing deadline. The mailbox directory and its delivered files remain.
Agents retire their registration before deleting their own mailbox files and
directory. The service does not purge old directories or restore registrations
from them. A restart invalidates IDs while retaining directories for consumption
and cleanup. Lost registration responses can therefore leave orphaned mailboxes;
operators and agents manage retention.

## Release and migration

0.2.0 is a breaking registration contract. Existing 0.1.0 release artifacts and
versioned evidence remain unchanged. Current documentation, OpenAPI, examples,
validators and deployment tooling move together to 0.2.0.

The release notifies FGA Dev M08, localswim V2 Dev M07 and Drive Stats Dev with
the new contract, migration instructions and rollout timing. The shared host
moves only after exact candidate artifacts pass independent qualification.
