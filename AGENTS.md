# localpost agent instructions

## Project scope

localpost is the shared localhost HTTP-to-file delivery service described in
`README.md`, `docs/vision.md`, `docs/architecture.md`, and `docs/adr/README.md`.
Keep accepted decisions distinct from open implementation choices. Do not build
project-local copies of shared services.

Implement the daemon in **Go**, following `docs/adr/0006-go-implementation-language.md`.
Use the standard library first; do not reopen the language choice without a new
explicit decision. Go 1.27.3 is the development toolchain, recorded in `.go-version`
and selected explicitly by `scripts/check.ps1`. Run that script before handing off
code changes. The first-release scope is accepted in
`docs/adr/0008-first-release-scope.md`; packaged-artifact qualification is still
required. The language ADR does not authorize implementation work.

The mandatory check includes pinned golangci-lint from `.golangci-version` and
the curated rules in `.golangci.yml`, plus formatting, tests, vet, and build.
Use `scripts/install-tools.ps1` for the local development linter. Keep the rule set
strict but useful: do not enable all linters, add arbitrary size/style quotas,
blanket-exclude tests, or suppress findings merely to obtain a green result.
Fix substantive issues; explain any narrow intentional error discard or exception.

Install pinned OpenAPI validation development dependencies with
`npm ci --ignore-scripts --no-audit --no-fund`. The normal check includes contract
validation and its tests; `-ServiceUrl http://127.0.0.1:53147` additionally checks
an existing shared daemon using only temporary owned registrations. Preserve
official schema snapshots and their recorded provenance when updating tooling.

Build release candidates only from a clean, identified commit with
`go run ./tools/release --version 0.1.0 --output <new-or-empty-directory>`.
Preserve the exact candidate archives and checksums for qualification and later
publication; do not rebuild or substitute artifacts after candidate acceptance.
The candidate workflow builds twice to check reproducibility and does not publish
a release or replace the shared daemon.

Keep control APIs REST-oriented and JSON-based, including structured application
errors with stable codes. Message-upload bodies remain opaque bytes; do not wrap
or rewrite them to apply the control API's JSON conventions.

## Shared daemon ownership

This localpost development chat owns keeping the shared localpost daemon running.
Terry clarified this on 2026-11-04. Check the loopback listener and owned process
before starting another instance; investigate unexpected absence or exit and
restore service. Do not ask Terry to start it as though this chat were an external
client project.

Terry clarified on 2026-11-05 that "start localpost" means launch the shared host
service on its default `127.0.0.1:53147` endpoint, fully detached from Codex and
the current agent. Use a host-owned background launch mechanism that survives
Codex exiting, with a hidden window and private stdout/stderr logs. Verify both
HTTP/file delivery and the independent process ownership before reporting it
running. Reuse a healthy independently launched instance.

On this Windows host, use the user-level scheduled task `localpost Shared
Service`. It launches through system Windows PowerShell, has no execution time
limit, and records ownership in the private `daemon-runtime.json`. Start that
task with `Start-ScheduledTask` when its listener is absent. Keep its launcher,
installed release executable and logs under the private runtime directory.

Keep daemon ownership metadata and stdout/stderr logs under the private runtime
directory listed below. The default listener is `127.0.0.1:53147`. Verify service
with an HTTP request and, when needed, an isolated registration/delivery/teardown
probe. Preserve the shared daemon when a pilot or proof run ends; stop only owned
temporary pilot processes and retire their registrations. Other client projects
still report unavailable localpost instead of launching substitute collectors.

## Development board

Use the shared localswim implementation at `C:\Projects\localswim`.

- Board: **localpost Dev**.
- Private state: `C:\Projects\localswim-state-store\localpost\localpost-dev.json`.
- Board URL: `http://127.0.0.1:8797/` (verify the live service before reuse).
- Private Git repository: `C:\Projects\localswim-state-store`.
- Runtime files: `C:\Projects\localswim-state-store\localpost\.runtime`.

**Always launch this board with `--autopush`.** Terry explicitly requires its
state to be committed and pushed to the private state-store repository. A healthy
listener alone is insufficient: verify that `/api/v001/status` reports publishing
enabled and inspect any publishing failure before declaring the board ready.

Use the shared package with its frozen environment:

```powershell
$env:UV_CACHE_DIR = 'C:\Temp\localswim-uv-cache'
uv run --frozen --project C:\Projects\localswim localswim --autopush C:\Projects\localswim-state-store\localpost\localpost-dev.json
```

Reuse the correct healthy writer when it already has autopush enabled. If this
board needs a restart, use the shared authenticated CLI lifecycle:

```powershell
uv run --frozen --project C:\Projects\localswim localswim-cli C:\Projects\localswim-state-store\localpost\localpost-dev.json board shutdown
```

Wait for graceful shutdown before relaunching with `--autopush`. Never force-stop
another service, create a duplicate writer, edit board JSON directly, or operate
on another board to repair this one. Launch background processes hidden and keep
their logs under the private runtime directory. Keep board data, credentials,
service descriptors, and runtime logs out of this public source repository.

## Board monitor

Whenever this project's board is running, keep one shared `localswim-monitor`
watching its JSON state and targeting the active localpost chat. Resolve the
actual current chat ID; do not reuse an old chat ID blindly. Use:

- `--board-path C:\Projects\localswim-state-store\localpost\localpost-dev.json`
- `--board-label "localpost Dev"`
- `--thread-id <current-chat-id>`
- `--port 8797` (verify the actual current board port)
- `--automation-actor bot`
- `--quiet-seconds 15`
- `--status-path C:\Projects\localswim-state-store\localpost\.runtime\monitor-status.json`
- `--target-path C:\Projects\localswim-state-store\localpost\.runtime\monitor-target.json`
- `--lock-path C:\Projects\localswim-state-store\localpost\.runtime\monitor.lock`

Run it through `uv run --frozen --extra monitor --project C:\Projects\localswim
localswim-monitor`, with `UV_CACHE_DIR` as above and `PYTHONIOENCODING=utf-8`.
Use separate private stdout/stderr files for the monitor and board service.

After the quiet window, the shared monitor queries sanitized CLI activity. It
suppresses a nonempty interval only when every event is by `bot`. Human, mixed,
unknown, empty, malformed, and failed results must still alert. Do not compare
filesystem-write counts with activity-event counts. Do not copy filtering code
into this repository or put card prose or credentials in monitor logs.

Use supported retargeting and cooperative `--reload` only when the existing
monitor advertises support. Preserve its pending activity and persistent locks;
do not delete runtime metadata or force-kill a monitor to bypass recovery.
Verify the owner process, current target, and `actorFilterVersion`, not merely
the presence of a saved status file.

## Ticket work

Use P3 (Normal) for new work unless concrete urgency justifies another priority.
Move a card to In Progress before work, tests, documentation, or delegation on
that card. Preserve dependencies and prerequisite acceptance gates. Keep work
In Progress through independent review; finished work awaiting Terry's approval
belongs in Ready for Review. Terry controls Completed.

Blocked means neither Bot nor Terry can advance the work until an external
condition changes. Needs Terry means a specific input or action from Terry is
required. Finished work awaiting review stays Ready for Review. Ordinary
prerequisite dependencies remain in Ready for Work and do not waive acceptance
gates for starting dependent work.

Keep an active epic In Progress while any child story is below Ready for Review.
Only when every child is Ready for Review or Completed may the epic move to
Ready for Review. This does not authorize Bot to move the epic to Completed.
