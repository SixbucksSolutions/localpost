# ADR 0003: Opaque POST bodies and atomic delivery files

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

TCP alone does not provide message boundaries. Browser clients already support
HTTP POST. Agents need complete message files they can discover in a directory.

## Decision

One complete HTTP POST body produces one `localpost_[UUIDv7].dat` file in the
mailbox directory. Initially accept `application/octet-stream` and
`application/json`; both are opaque payloads. Preserve the body bytes without
parsing JSON, changing encoding, decompressing content, adding metadata, or
inserting a newline. HTTP headers and transfer framing are not saved.

Accept empty bodies. Limit each body to 10 MiB (10,485,760 bytes) and time out an
upload after 30 seconds without progress.

Write to a temporary file in the destination directory, close the complete file,
then atomically rename it to the final name without replacing an existing file.
Return `200` after publication with the filename and byte count. Failed or
incomplete uploads produce no final `.dat` file.

## Consequences

Consumers ignore temporary files and reconcile final filenames. A process crash
may leave temporary files; cleanup is best effort. Concurrent uploads have no
ordering guarantee. Consumers needing ordering or correlation use information
supplied by their senders. Media-type parameters and HTTP encoding policy remain
open, but no policy may silently transform the payload.

## Implementation note (M01.03)

Complete-file publication uses a same-directory hard link of the closed
temporary file, followed by best-effort removal of its temporary name. This
realizes the complete-file handoff without relying on platform-dependent rename
replacement behavior. It requires filesystem hard-link support; failures remain
explicit. UUIDv7 generation uses Go's standard `uuid` package.
