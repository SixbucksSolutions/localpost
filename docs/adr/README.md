# Architecture decision record ledger

This ledger records project decisions and their authority. **Accepted** means
the design decision was approved, including choices explicitly delegated to the
maintainer; it does not mean the behavior has been implemented or tested.

| ADR | Decision | Status | Date |
| --- | --- | --- | --- |
| [0001](0001-central-shared-service.md) | One shared service with a mailbox per registration | Accepted | 2026-11-04 |
| [0002](0002-loopback-operation.md) | Unprivileged loopback listeners and zero configuration files | Accepted | 2026-11-04 |
| [0003](0003-opaque-file-delivery.md) | Opaque POST bodies and atomic delivery files | Accepted | 2026-11-04 |
| [0004](0004-best-effort-lifecycle.md) | Ephemeral registration and best-effort lifecycle | Accepted | 2026-11-04 |
| [0005](0005-trusted-localhost-cors.md) | Trusted localhost, plain HTTP, and permissive CORS | Accepted | 2026-11-04 |
| [0006](0006-go-implementation-language.md) | Implement localpost in Go | Accepted | 2026-11-04 |

## Open implementation decisions

These are not additional accepted requirements:

- Release packaging and initially supported operating systems. Development uses
  Go 1.27.3 as recorded in `.go-version` and the README.
- Remaining admin and mailbox routes and schemas. Registration now uses
  `POST /registrations`; the implemented JSON contract is documented in the README.
- Media-type parameter policy and supported HTTP body framing/content encodings.
- Concurrent-upload, registration, and aggregate temporary-storage limits; bounds
  for uploads that keep making slow progress.
- Exact shutdown timer behavior and temporary-file naming/cleanup.
- Directory write-access verification and platform-specific atomic publication.
- Operational logging that avoids recording payloads.

Record material resolutions in new ADRs. Keep IDs stable. When a decision changes,
add a superseding record and link it from the old record instead of erasing the
reasoning. Update this ledger and the architecture document together.
