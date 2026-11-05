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
| [0007](0007-single-port-uuid-routing.md) | One listener with UUIDv4 registration routing | Accepted | 2026-11-04 |
| [0008](0008-first-release-scope.md) | First-release scope, artifacts and publication gate | Accepted | 2026-11-04 |
| [0009](0009-service-created-mailboxes.md) | Service-created UUIDv4 mailboxes and the 0.2.0 migration | Accepted for authorized implementation | 2026-11-05 |
| [0010](0010-date-versioned-api.md) | Explicit date-versioned API paths and retired-version errors | Accepted for authorized implementation | 2026-11-05 |

## Open implementation decisions

These are not additional accepted requirements:

- First-release packaging and target qualification follow
  [ADR 0008](0008-first-release-scope.md); artifact qualification is pending. Development
  uses Go 1.27.3 as recorded in `.go-version` and the README.
- Remaining API refinements. Registration uses `POST /registrations`, removal
  uses `DELETE /registrations/{id}`, and delivery uses `POST /registrations/{id}/messages`;
  their contracts are documented in the README.
- Broader qualification of the implemented registration/upload limits and
  slow-progress behavior. Current fixed defaults are documented in the README.
- Broader temporary-file recovery policy beyond best-effort cleanup.
- Directory write-access verification and platform-specific atomic publication.
- Operational logging that avoids recording payloads.

Record material resolutions in new ADRs. Keep IDs stable. When a decision changes,
add a superseding record and link it from the old record instead of erasing the
reasoning. Update this ledger and the architecture document together.
