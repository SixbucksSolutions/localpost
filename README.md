# localpost

A localhost post office for agents.

localpost is one shared service that receives HTTP POST bodies and delivers them
unchanged as files in an agent-selected directory. An agent registers a mailbox,
gets a dedicated listening port, and reads the resulting files with its normal
filesystem tools.

**Status:** M01.01 implements the Go executable and loopback admin listener.
Mailbox registration, message delivery, and their API routes are not implemented
yet. The delivery workflow and contract below describe the target service.

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
4. localpost opens a dedicated loopback port and returns it in a `200` response.
5. A sender posts a message to that port. localpost publishes one complete file:
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

### Run

The operator can start the foundation with:

```powershell
./bin/localpost.exe                 # Admin listener at http://127.0.0.1:53147
./bin/localpost.exe --port 53148    # Override the admin port
```

Stop it with Ctrl+C. Until the mailbox stories are implemented, HTTP requests
receive a not-found response; there is no registration or upload endpoint yet.
The startup message confirms the bound address. Full release packaging belongs
to a later milestone.

There is no configuration file. The admin listener and all mailbox listeners
will use unprivileged TCP ports on `127.0.0.1`. An occupied admin port causes startup
to fail clearly; localpost does not silently choose a different admin port.

## Delivery contract

| Item | Decision |
| --- | --- |
| Message boundary | One complete HTTP POST body produces one file |
| Accepted content types | `application/octet-stream` and `application/json` |
| Payload handling | Preserve body bytes; no JSON validation, decoding, or added newline |
| Filename | `localpost_[UUIDv7].dat` |
| Publication | Write a temporary file, close it, then rename to the final name |
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
and closes that mailbox's listener. Delivered files remain in place. localpost
never creates or repairs the destination directory and never deletes deliveries.

## Project documents

- [Project vision](docs/vision.md): purpose, ownership, and scope.
- [High-level architecture](docs/architecture.md): components, lifecycle, and
  delivery behavior.
- [ADR ledger](docs/adr/README.md): accepted decisions and open implementation details.
- [MIT license](LICENSE).

The public repository is [SixbucksSolutions/localpost](https://github.com/SixbucksSolutions/localpost).
