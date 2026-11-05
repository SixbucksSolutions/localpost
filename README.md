# localpost

> **Project status — Abandoned / OBE (2026-11-05)**
>
> localpost was explored and then abandoned as overtaken by events. The exploration
> led to reusing established messaging tools.
> This repository is retained as a historical record. Its code, releases and
> instructions document that exploration, and active development is discontinued.

See the [trade study](docs/studies/0043-in-memory-message-brokers.md) for the
findings behind this direction.

## Historical project documentation

A localhost post office for agents.

**Latest release: [v0.2.1](https://github.com/SixbucksSolutions/localpost/releases/tag/v0.2.1)** —
[Windows x64](https://github.com/SixbucksSolutions/localpost/releases/download/v0.2.1/localpost_0.2.1_windows_amd64.zip) ·
[Linux x64](https://github.com/SixbucksSolutions/localpost/releases/download/v0.2.1/localpost_0.2.1_linux_amd64.tar.gz) ·
[Linux ARM64](https://github.com/SixbucksSolutions/localpost/releases/download/v0.2.1/localpost_0.2.1_linux_arm64.tar.gz) ·
[Container guide](docs/containers.md) ·
[SHA256SUMS](https://github.com/SixbucksSolutions/localpost/releases/download/v0.2.1/SHA256SUMS)

The [v0.2.1 OpenAPI contract](https://github.com/SixbucksSolutions/localpost/blob/5b5361b8d2afed46d25be4674fd670e9b3687513/openapi.json) describes the released API.
The v0.1.0 release uses its [immutable API contract](https://github.com/SixbucksSolutions/localpost/blob/v0.1.0/openapi.json).

localpost is one shared service that receives HTTP POST bodies and delivers them
unchanged as files in a service-created directory. An agent registers a mailbox,
gets a UUIDv4 identity on the shared listener, and reads the resulting files with
its normal filesystem tools.

**Status:** v0.2.1 provides the shared loopback listener, service-created
mailboxes, message delivery, unregistration and permissive CORS. Its qualified
archives cover Windows and Linux on the environments listed in the
[release notes](docs/releases/v0.2.1.md). The dated API is incompatible with
v0.1.0. The headless Chrome pilot exercised a browser feedback loop on Windows.
localpost is free and open-source software under the MIT license, provided as is
without warranty. These good-faith tests carry no support commitment.
[GitHub issues](https://github.com/SixbucksSolutions/localpost/issues) are welcome,
with no guarantee of a response or fix.

**Startup cleanup:** v0.2.1 changes startup retention: every daemon start warns if the
dedicated mailbox root contains stale entries and removes all of its children
before serving HTTP. The daemon reserves its port before cleanup, so an occupied
port fails before any deletion. This applies after a normal stop or forced termination.
The root itself remains. Copy any needed data elsewhere before starting or
restarting. Never put unrelated data in the root. The operator must ensure only
one daemon uses a root, including across different ports. There is no root lock.
DELETE still leaves files until the next start. The API date and routes remain unchanged. See the
[v0.2.1 release notes](docs/releases/v0.2.1.md). The released v0.2.0 retains
mailbox contents across restart.

**Implementation language:** Go, with a standard-library-first approach and
standalone executables. [ADR 0006](docs/adr/0006-go-implementation-language.md)
records the decision and the tradeoff against Rust.

## Why it exists

An agent testing a web UI needs a direct way to receive browser diagnostics.
Instead of building a separate collector in every project, the browser sends
messages to localpost. The agent reads those messages from disk, makes a scoped
fix, and repeats the test.

The same delivery service can carry other payloads. localpost does not need to
understand them.

## Intended workflow

1. An operator provisions an existing absolute mailbox root and starts localpost with `--mailbox-root`.
2. An agent sends `{}` to the dated registration route.
3. localpost creates a UUIDv4-named child and returns its ID and absolute directory.
4. A sender posts to that ID. Each complete message becomes `localpost_[UUIDv7].dat`.
5. The agent reads the files, retires its registration, and cleans up its directory.

If localpost is unavailable, the agent tells the person running it. Agents do
not start a replacement daemon or fall back to project-local collectors.

## Container deployment

The published v0.2.1 image `ghcr.io/sixbuckssolutions/localpost:0.2.1`
provides Linux AMD64 and ARM64 deployments. See the [container guide](docs/containers.md)
for its operating instructions and qualification status. The
[v0.1.0 container guide](https://github.com/SixbucksSolutions/localpost/blob/6889c8c/docs/containers.md)
remains available for the earlier release.

## Build and run the foundation

Development uses Go **1.27.3**, recorded in `.go-version`, and golangci-lint
**2.15.0**, recorded in `.golangci-version`. Install the pinned development linter
once, then run the complete check:

```powershell
./scripts/install-tools.ps1
npm ci --ignore-scripts --no-audit --no-fund
./scripts/check.ps1
./bin/localpost.exe --help
```

The current development check runs on Windows with PowerShell 7. A Go installation
must be available; Go may download the selected toolchain if it is not installed.
The built executable does not require a Go installation to run.

Development checks also use Node **26.13.0**, pinned in `.node-version`. The
browser pilot uses its built-in test runner; pinned npm development dependencies
validate the OpenAPI contract. Node and those dependencies are not required to
run the localpost daemon.

The tool installer downloads the official Windows x64 linter release, verifies
its published SHA256 checksum, and keeps it under ignored `.tools/`. It does not
change global tools. Installation needs network access; the check requires the
pinned local linter and does not silently skip missing tooling.

### Quality bar

`scripts/check.ps1` runs formatting, the selected lint rules, tests, `go vet`, and
the executable build. Go's compiler checks types; `govet` and Staticcheck add
analysis beyond type correctness. `.golangci.yml` enables a deliberate set of
linters with `default: none`:

| Checks | Purpose |
| --- | --- |
| `govet`, `staticcheck` | Suspicious constructs, API misuse, and likely bugs |
| `errcheck`, `errorlint` | Unhandled errors and incorrect error wrapping/comparison |
| `bodyclose` | HTTP response-body leaks |
| `unused`, `ineffassign`, `unconvert` | Dead code, ineffective assignments, and unnecessary conversions |
| `revive`, `gocritic` | Selected/default maintainability and diagnostic checks |

The policy favors checks that earn their maintenance cost. It does not enable
every available linter or arbitrary function-length and complexity quotas.
Expected best-effort cleanup may explicitly discard an error with an explanation;
blanket exclusions are not the way to make a failing check pass.

The [race workflow](.github/workflows/race.yml) runs the suite three times with
`go test -race` on an Ubuntu runner with GCC and the same pinned Go version.
This supplies race-instrumented verification where the Windows development
environment lacks a C compiler; it does not establish a new release platform.
The detector covers executed paths, not every possible schedule. The bounded
qualification tests also check mailbox isolation, capacity recovery, preserved
files, and cleanup across repeated concurrent lifecycle cycles.
Shared-listener coverage includes invalid and retired IDs, connection reuse,
rejection/preflight body cleanup, and overlapping mailbox deletion with process
shutdown. These HTTP tests are separate from the recorded browser pilot proof.

The same check runs syntax and behavior tests for the
[development-only browser diagnostics pilot](examples/browser-diagnostics/README.md).
CI runs those tests alongside Go race verification. The pilot serves static
assets on a separate loopback port and sends diagnostics to the shared daemon;
it does not write diagnostic files or start a replacement service. The example's
README records the M04.02 headless-browser evidence and its limits.

### Validate the API contract

The normal check validates [openapi.json](openapi.json) against versioned official
OAS 3.2 schema snapshots, resolves references, and checks modeled JSON examples.
The opaque upload bodies deliberately have no JSON schema. Validation tooling
tests include rejected malformed documents; this is not a claim that every
third-party OpenAPI renderer supports every OAS 3.2 feature.
The [schema and validator notes](scripts/openapi-schemas/README.md) record the
pinned sources, fixed-dialect compatibility binding, and supported reference
scope. Unsupported external or dynamic application-schema references fail
explicitly rather than being treated as validated.

To also check an already-running localpost service with temporary, isolated
registrations and files:

```powershell
./scripts/check.ps1 -ServiceUrl http://127.0.0.1:53147 -MailboxRoot C:\localpost\mailboxes
```

This leaves the shared daemon running. CI runs the same service check against
its own temporary daemon. The canonical contract is maintained in one place;
these README examples explain its use rather than defining a second contract.

### Run

The operator can start localpost with:

```powershell
./bin/localpost.exe --mailbox-root C:\localpost\mailboxes
./bin/localpost.exe --mailbox-root C:\localpost\mailboxes --port 53148
```

Stop it with Ctrl+C. Shutdown stops new work, allows a shared grace period of up
to 30 seconds, then interrupts unfinished requests. The startup message confirms
the bound address. Control and message routes use this same listener.

The root must already exist and be an absolute directory accessible to the service.
There is no implicit daemon root. `--help` and `--version` need no root.
The Windows installer is planned to provision `C:\ProgramData\localpost\mailboxes`
and pass it explicitly. The shared listener uses an unprivileged TCP
port on `127.0.0.1`. An occupied port causes startup to fail clearly; localpost
does not silently choose another port.

### Build release candidates

The original [first-release scope](docs/adr/0008-first-release-scope.md) targeted
Windows 11 x64 and Ubuntu 26.04 x64. Additional Linux environments and ARM64 were
tested using the exact archives. The current test matrix
is in the [release notes](docs/releases/v0.2.1.md).

From a clean checkout, build the versioned candidates into a new or empty directory:

```powershell
$candidateDir = Join-Path $env:TEMP ('localpost-candidate-' + [guid]::NewGuid())
go run ./tools/release --version 0.2.1 --output $candidateDir
```

Preserve published artifact bytes. A fresh local build does not replace a
published asset. The v0.2.1 release contains the exact qualified archives.

The builder exports the identified Git commit, uses the pinned Go toolchain,
and by default produces Windows AMD64, Linux AMD64 and Linux ARM64 archives
plus `SHA256SUMS`. Use
`--source-ref` and `--targets` to select a source revision and build targets.
Each archive includes its
binary, offline instructions, licenses, canonical API contract and build
manifest. `localpost --version` reports binary identity without opening a port;
ordinary development builds report `dev` and `unknown`.

The [candidate workflow](.github/workflows/release-candidate.yml) builds twice,
compares checksums and retains one exact artifact set for later qualification.
It does not create a public release or replace the running shared daemon.

### Additional Linux certification

The [platform certification workflow](.github/workflows/certify-platforms.yml)
has two manually selected waves. Run either from a clean, reviewed commit on main.

```sh
gh workflow run certify-platforms.yml --field architecture=amd64
gh workflow run certify-platforms.yml --field architecture=arm64
```

| Wave | Environments |
| --- | --- |
| `amd64` | Ubuntu 24.04, Ubuntu 26.04 and Alpine 3.24.3 |
| `arm64` | Ubuntu 24.04, Ubuntu 26.04 and Alpine 3.24.3 |

Ubuntu checks run directly on native-architecture GitHub-hosted VMs. Alpine
checks run in native-architecture containers on Ubuntu 26.04, sharing the host
kernel and a bind-mounted filesystem. Evidence records the actual OS, kernel,
filesystem and image digest. This scope covers Alpine userspace compatibility.

Both waves use the frozen `release/0.2.1-candidate.json` artifact identity.
They verify the candidate artifacts before publication or the matching public
assets after publication.

Both waves check package identity, ordinary-user startup with an empty `PATH`,
default and overridden ports, occupied-port rejection, API/CORS behavior,
byte-exact publication, disconnect cleanup, storage failures and graceful shutdown.
CI retains reports and checksums for 90 days. Successful runs provide review
evidence of testing in the recorded environments.

The v0.2.1 [AMD64](https://github.com/SixbucksSolutions/localpost/actions/runs/42072611097)
and [ARM64](https://github.com/SixbucksSolutions/localpost/actions/runs/42072617048)
certification runs passed. The [release notes](docs/releases/v0.2.1.md) describe
the tested environments and qualification evidence.

## Registration API

The control API uses JSON for requests, success responses, and
application errors. Register with an empty object on the dated route.

```powershell
$base = 'http://127.0.0.1:53147/api/2026-11-05'
$registration = Invoke-RestMethod -Method Post -Uri "$base/registrations" `
    -ContentType application/json -Body '{}'
$inbox = $registration.directory
```

A successful response contains `id` and `directory`. The ID is a UUIDv4 and
is the basename of the absolute directory created beneath the configured root.
There is no registration-specific port or process ID. Registrations disappear
on restart. In released v0.2.0, directories and files remain. v0.2.1 removes all root children at the next startup.

On POSIX, mailbox directories use `0700` and files use `0600`. Run the service
and consuming agent under the same UID. On Windows, new directories and files
inherit the root's ACL. The operator must grant the service and agent the access
they need. The daemon does not write a startup probe. Write failures can surface
during cleanup, mailbox creation, or delivery.

Registration requests accept one JSON object, at most 64 KiB, with media-type
parameters such as `charset=utf-8` permitted. Control-request reads have a
30-second budget; upload reads use their separate idle deadline. These limits
do not impose a deadline on filesystem operations.
Application errors return an `error` object containing a stable `code` and a
readable `message`, for example:

```json
{"error":{"code":"invalid_request","message":"invalid request"}}
```

| HTTP status | Error code |
| --- | --- |
| `400` | `invalid_request` or `invalid_registration_id` |
| `404` | `not_found`, `registration_not_found`, or `unsupported_api_version` |
| `405` | `method_not_allowed` (with `Allow: POST`) |
| `413` | `request_too_large` |
| `415` | `unsupported_media_type` |
| `500` | `mailbox_allocation_failed` |
| `503` | `unavailable` or `registration_limit` |

A lost response does not undo an established registration. Control requests
use JSON; mailbox payloads remain opaque bytes on the same listener.
Browser callers use the permissive CORS policy described below.

## Unregistration API

Use `DELETE /api/2026-11-05/registrations/{id}` to retire one mailbox:

```powershell
Invoke-RestMethod -Method Delete `
    -Uri "http://127.0.0.1:53147/api/2026-11-05/registrations/$($registration.id)"
```

Successful completion returns JSON:

```json
{"id":"4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b","unregistered":true,"forced":false}
```

The mailbox first stops accepting new uploads. Accepted uploads can finish
during its 30-second grace period. After the deadline, remaining uploads are
interrupted; `forced: true` indicates that this cutoff was needed. Progress does
not extend the shutdown deadline. Completed `.dat` files remain untouched, and
other mailboxes remain usable.
Deleting a registration does not shut down the shared HTTP listener.

Completion waits for local request cleanup. The 30 seconds bounds network drain,
not operating-system filesystem calls that may take longer to return.

Concurrent deletion requests for a mailbox that is still closing share its
operation. Once the mailbox is gone, deletion returns HTTP `404` with
`registration_not_found`. Disconnecting the deletion client does not cancel
cleanup. Whole-daemon shutdown uses one shared deadline across its mailboxes,
rather than granting 30 seconds to each in turn. All registrations disappear
when the process exits.

## Message API

Send a message with `POST /api/2026-11-05/registrations/{id}/messages` on the shared port. Use
`application/octet-stream` or `application/json`; media-type parameters are
accepted. Both bodies are opaque bytes, so JSON-labelled content is not parsed
or repaired. HTTP transfer framing is removed by the HTTP server; content
encoding is not decompressed.

For example, after registering an inbox:

```powershell
$delivery = Invoke-RestMethod -Method Post `
    -Uri "http://127.0.0.1:53147/api/2026-11-05/registrations/$($registration.id)/messages" `
    -ContentType application/json -Body '{"event":"test"}'
Get-Content -LiteralPath (Join-Path $inbox $delivery.filename) -Raw
```

HTTP `200` returns JSON containing `filename` and `bytes`. Success means a
complete file was published. Empty bodies are valid; bodies above 10 MiB are
rejected. An upload may take longer than 30 seconds while making progress, but
a 30-second stalled read ends it. There is no power-loss durability guarantee
or automatic retry. A lost response can leave delivery uncertain.

Files are first written under a temporary name. The closed, complete file is
published with a same-directory hard link, then its temporary name is removed
on a best-effort basis. This avoids overwriting an existing delivery without
filename reservation machinery. The destination filesystem must support hard
links; unsupported publication fails instead of falling back to partial-file
visibility. Consumers read only final `localpost_[UUIDv7].dat` names.

Message errors use the same JSON envelope as registration errors: `400`
`invalid_request`, `404` `not_found`, `405` `method_not_allowed`, `408`
`request_timeout`, `413` `request_too_large`, `415` `unsupported_media_type`, or
`500` `delivery_failed`, or `503` `upload_limit` / `unavailable`.

## Resource limits

The fixed defaults are **64 live registrations** and **16 active uploads across
all mailboxes**. Closing registrations keep their place until cleanup finishes.
An upload keeps its place through publication and its temporary-file cleanup
attempt. Saturation returns HTTP `503` with `registration_limit` or `upload_limit`;
localpost does not queue or retry the request.

With the 10 MiB message limit, the 16 admitted uploads account for at most
160 MiB of active payload. This is not a limit on all process memory, retained
deliveries, or temporary files left after failed cleanup. Delivered-file retention
is the agent's responsibility until the next startup cleanup in v0.2.1.
No extra runtime configuration is required.

## Browser CORS

Control and message responses, including application errors, send
`Access-Control-Allow-Origin: *`. The shared listener allows the `Content-Type`
request header and advertises `POST, DELETE, OPTIONS`. No credentials, tokens, or origin registration are
needed. Supported-prefix `OPTIONS` preflights return `204` without creating a
registration or file. Unsupported-version preflights return `404` with
`unsupported_api_version`. Other unknown paths return `404 not_found`.

After the agent registers a mailbox and supplies its registration ID,
browser JavaScript can send a JSON diagnostic and read the JSON receipt:

```javascript
const response = await fetch(`http://127.0.0.1:53147/api/2026-11-05/registrations/${registrationId}/messages`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  credentials: "omit",
  body: JSON.stringify({ event: "example", phase: "rendered" }),
});
const receipt = await response.json();
if (!response.ok) throw new Error(receipt.error.message);
```

The core HTTP checks cover preflights, error headers, file delivery, removal,
and sibling isolation. The separate browser pilot records a headless Chrome
demonstration of the diagnostic feedback loop. An application's CSP and the
browser's local-network permissions still apply.

## Delivery contract

| Item | Decision |
| --- | --- |
| Message boundary | One complete HTTP POST body produces one file |
| Accepted content types | `application/octet-stream` and `application/json` |
| Payload handling | Preserve body bytes; no JSON validation, decoding, or added newline |
| Filename | `localpost_[UUIDv7].dat` |
| Publication | Write a temporary file, close it, then publish a complete final name without overwriting |
| Success | `200` after publication, returning the filename and byte count |
| Empty body | Accepted; produces a zero-byte file |
| Maximum body | 10 MiB (10,485,760 bytes) |
| Stalled upload | 30-second timeout |
| Ordering | No ordering guarantee across concurrent requests |
| Browser access | Permissive CORS: `Access-Control-Allow-Origin: *` |
| Trust | Plain HTTP, no authentication, no origin allowlists |
| State | Registrations live in memory and disappear on restart |
| Reliability | Best effort; no durable queue, automatic retries, recovery, or replay |

Success means the file has been published, not that it is guaranteed to survive
a power loss. A lost HTTP response can leave delivery uncertain. Retrying may
create a duplicate. Senders that need sequence information include it in their
own payloads.

Only message POSTs create delivery files. Browser `OPTIONS` preflights do not.
Agents consume final `.dat` files and ignore temporary files. Filesystem
notifications are hints; agents should also reconcile directory contents.

## Responsibilities

- **localpost:** register and close mailboxes, receive bounded HTTP bodies, and
  publish complete files without changing the payload.
- **Agent:** discover, interpret, retain, and remove its delivered files and
  mailbox directory after retiring the registration. Report an unavailable daemon.
- **Sender/application:** choose useful content and remove sensitive information
  before sending it. Browser instrumentation stays in the application.
- **Person running localpost:** provision the root and its permissions, then start
  and stop the shared service.

Unregistering stops new uploads, lets accepted uploads finish within the timeout,
and removes that logical mailbox. Delivered files remain in place. localpost
creates each destination directory. Released v0.2.0 retains these directories on
restart. v0.2.1 reserves its port, deletes all root children, then
serves HTTP.

## Migrating from v0.1.0

The v0.1.0 binaries use unversioned routes and caller-created directories.
The v0.2.0 release uses dated routes and service-created directories.

- Provision an existing absolute root and pass `--mailbox-root` at startup.
- Send `{}` when registering. Do not send `directory` or a process ID.
- Read both `id` and the service-created absolute `directory` from the response.
- Prefix registration, message and deletion routes with `/api/2026-11-05`.
  Retired unversioned routes and unsupported API dates return `404` with
  `unsupported_api_version` and name the supported prefix. There is no latest alias.
- Re-register after restart. Old IDs do not survive. Retire a registration before
  removing its directory. In v0.2.0, deletion and restart preserve directories
  and files. In v0.2.1, deletion preserves them only until startup.
  Account for orphaned directories after lost responses and manage retention.

The planned rollout includes migration notices to FGA Dev M08, localswim V2 Dev
M07 and Drive Stats Dev.

## Project documents

- [Project vision](docs/vision.md): purpose, ownership, and scope.
- [High-level architecture](docs/architecture.md): components, lifecycle, and
  delivery behavior.
- [ADR ledger](docs/adr/README.md): accepted decisions and open implementation details.
- [MIT license](LICENSE).

The public repository is [SixbucksSolutions/localpost](https://github.com/SixbucksSolutions/localpost).

## Valkey and Webdis adoption candidate

An isolated in-memory candidate has passed qualification. The released v0.2.1
service above remains the current shared service. See the [candidate operations
guide](docs/operations/valkey-webdis.md) for port 53170, native client semantics,
and cutover preparation. The [qualification receipt](docs/qualification/valkey-webdis-adoption-r1.md) links the exact tested bundle. Build the unpublished `adoption-r1` bundle from reviewed, frozen HEAD with
`./scripts/build-adoption-candidate.ps1 -OutputDirectory <absolute-private-empty-folder>`.
The builder checks reproducibility with two builds. Preserve the exact bundle
for qualification under #0049. No shared-service cutover is authorized by this candidate
documentation.
