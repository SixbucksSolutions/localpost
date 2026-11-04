# localpost

A localhost post office for agents.

localpost is one shared service that receives HTTP POST bodies and delivers them
unchanged as files in an agent-selected directory. An agent registers a mailbox,
gets a UUIDv4 identity on the shared listener, and reads the resulting files with
its normal filesystem tools.

**Status:** the Go executable, shared loopback listener, mailbox registration,
message delivery, unregistration, and permissive CORS are implemented. Bounded
robustness checks and race-test automation accompany the core. The browser pilot
has completed a headless Chrome feedback-loop proof on Windows. OpenAPI
documentation and release packaging remain in M05-M06.
The M03 API uses one listener and UUIDv4-addressed registrations; the old
per-registration ports are removed.

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

1. A person starts localpost as an ordinary user.
2. An agent creates its destination directory and ensures localpost can write to it.
3. The agent registers that directory through the admin API.
4. localpost returns a UUIDv4 registration ID in a `200` response.
5. A sender posts a message to that registration's route. localpost publishes one complete file:
   `localpost_[UUIDv7].dat`.
6. The agent reads the file. It unregisters the mailbox when finished.

If localpost is unavailable, the agent tells the person running it. Agents do
not start a replacement daemon or fall back to project-local collectors.

## Build and run the foundation

Development uses Go **1.27.3**, recorded in `.go-version`, and golangci-lint
**2.15.0**, recorded in `.golangci-version`. Install the pinned development linter
once, then run the complete check:

```powershell
./scripts/install-tools.ps1
./scripts/check.ps1
./bin/localpost.exe --help
```

The current development check runs on Windows with PowerShell 7. A Go installation
must be available; Go may download the selected toolchain if it is not installed.
The built executable does not require a Go installation to run.

The browser pilot's development checks also use Node **26.13.0**, pinned in
`.node-version`, with its built-in test runner and no npm dependencies. Node is
not required to run the localpost daemon.

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

### Run

The operator can start localpost with:

```powershell
./bin/localpost.exe                 # Shared API at http://127.0.0.1:53147
./bin/localpost.exe --port 53148    # Override the shared port
```

Stop it with Ctrl+C. Shutdown stops new work, allows a shared grace period of up
to 30 seconds, then interrupts unfinished requests. The startup message confirms
the bound address. Control and message routes use this same listener. Full
release packaging belongs to a later milestone.

There is no configuration file. The shared listener uses an unprivileged TCP
port on `127.0.0.1`. An occupied port causes startup to fail clearly; localpost
does not silently choose another port.

## Registration API

The control API uses JSON for requests, success responses, and application errors.
Register an existing absolute directory with `POST /registrations` on the shared
port. The caller creates the directory and arranges its permissions:

```powershell
$inbox = Join-Path $env:TEMP 'localpost-inbox'
New-Item -ItemType Directory -Force -Path $inbox | Out-Null
$body = @{ directory = $inbox } | ConvertTo-Json
$registration = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:53147/registrations `
    -ContentType application/json -Body $body
```

A successful registration returns HTTP `200` with the assigned UUIDv4 identity:

```json
{"id":"4e23a5f1-2d7f-4d2b-9d5a-1d47120a493b"}
```

There is no registration-specific port. The ID identifies a logical mailbox,
not an authentication credential. Multiple registrations remain independent,
and all are forgotten when the daemon exits. Existing files are unchanged.

localpost checks writability with a temporary probe file, which it closes and
removes before accepting the directory. It does not create directories or change
their permissions. A successful probe is not a guarantee that later writes will
succeed.

Registration requests accept one JSON object, at most 64 KiB, with media-type
parameters such as `charset=utf-8` permitted. Control-request reads have a
30-second budget; upload reads use their separate idle deadline. These limits
do not impose a deadline on filesystem operations.
Application errors return an `error` object containing a stable `code` and a
readable `message`, for example:

```json
{"error":{"code":"invalid_directory","message":"directory must exist and be accessible"}}
```

| HTTP status | Error code |
| --- | --- |
| `400` | `invalid_request`, `invalid_directory`, or `invalid_registration_id` |
| `404` | `not_found` or `registration_not_found` |
| `405` | `method_not_allowed` (with `Allow: POST`) |
| `413` | `request_too_large` |
| `415` | `unsupported_media_type` |
| `503` | `unavailable` or `registration_limit` |

A lost response does not undo an established registration. Control requests
use JSON; mailbox payloads remain opaque bytes on the same listener.
Browser callers use the permissive CORS policy described below.

## Unregistration API

Use `DELETE /registrations/{id}` to retire one mailbox:

```powershell
Invoke-RestMethod -Method Delete `
    -Uri "http://127.0.0.1:53147/registrations/$($registration.id)"
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

Send a message with `POST /registrations/{id}/messages` on the shared port. Use
`application/octet-stream` or `application/json`; media-type parameters are
accepted. Both bodies are opaque bytes, so JSON-labelled content is not parsed
or repaired. HTTP transfer framing is removed by the HTTP server; content
encoding is not decompressed.

For example, after registering an inbox:

```powershell
$delivery = Invoke-RestMethod -Method Post `
    -Uri "http://127.0.0.1:53147/registrations/$($registration.id)/messages" `
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
remains the agent's responsibility. No extra runtime configuration is required.

## Browser CORS

Control and message responses, including application errors, send
`Access-Control-Allow-Origin: *`. The shared listener allows the `Content-Type`
request header and advertises `POST, DELETE, OPTIONS`. No credentials, tokens, or origin registration are
needed. `OPTIONS` preflights return `204` without creating a registration or file.

After the agent prepares a directory and supplies its registration ID,
browser JavaScript can send a JSON diagnostic and read the JSON receipt:

```javascript
const response = await fetch(`http://127.0.0.1:53147/registrations/${registrationId}/messages`, {
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
- **Agent:** prepare the directory and permissions; discover, interpret, retain,
  and remove delivered files; report an unavailable daemon.
- **Sender/application:** choose useful content and remove sensitive information
  before sending it. Browser instrumentation stays in the application.
- **Person running localpost:** start and stop the shared service.

Unregistering stops new uploads, lets accepted uploads finish within the timeout,
and removes that logical mailbox. Delivered files remain in place. localpost
never creates or repairs the destination directory and never deletes deliveries.

## Migrating from the per-port prototype

Restart localpost and register again to obtain UUIDv4 IDs. Registrations were
already ephemeral, so no registration-state migration is needed. Change senders
to use the shared port and ID-bearing message path; the registration response no
longer contains `port`. The old bare `/messages` route is not a compatibility API.
Previously delivered UUIDv7 files remain unchanged.

## Project documents

- [Project vision](docs/vision.md): purpose, ownership, and scope.
- [High-level architecture](docs/architecture.md): components, lifecycle, and
  delivery behavior.
- [ADR ledger](docs/adr/README.md): accepted decisions and open implementation details.
- [MIT license](LICENSE).

The public repository is [SixbucksSolutions/localpost](https://github.com/SixbucksSolutions/localpost).
