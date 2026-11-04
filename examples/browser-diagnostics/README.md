# Browser diagnostic pilot

This development-only example serves a synthetic **Add one** counter. It uses
the existing localpost daemon to deliver bounded JSON diagnostics. It contains
no collector, file writer, daemon launcher, dependency install, or background
watcher. The real-browser fault/fix demonstration belongs to M04.02; the checked-in
example and automated checks alone do not establish that proof.

## Prepare and run

The operator starts localpost separately (see the root README). If it is
unavailable, report that condition; do not launch a replacement collector.
From the repository root, create a fresh private inbox **outside this repository**
and register it. These commands use a per-run temporary directory; select a more
restricted private location if the local environment requires it.

```powershell
$daemon = 'http://127.0.0.1:53147'
$inbox = Join-Path $env:TEMP ('localpost-browser-' + [guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $inbox | Out-Null
$registration = Invoke-RestMethod -Method Post -Uri "$daemon/registrations" `
    -ContentType application/json -Body (@{ directory = $inbox } | ConvertTo-Json -Compress)
$registration.id
go run ./examples/browser-diagnostics
```

The pilot binds only `127.0.0.1:53148`, distinct from the default daemon port.
Use `-port 53149` after the package path if necessary. It serves only `/` and
`/pilot.mjs`, from embedded assets. Stop it with Ctrl+C; shutdown has a five-second
grace period. It does not stop localpost or retire the registration.

Open [the pilot](http://127.0.0.1:53148/), enter the daemon base URL and registration
ID, and enable diagnostics. The URL must resolve as an HTTP root at `127.0.0.1`
or `[::1]`; credentials, paths, query strings and fragments are rejected. No
credentials or private filesystem paths belong in the page. Browser local-network
permission and application CSP rules still apply.

Each click updates the count synchronously and schedules one POST. Logging is
off initially. Disabled logging, invalid configuration, HTTP failure, transport
failure and the 1.5-second timeout leave the counter working. There are no retries;
a timeout or lost response can still mean that the message was delivered. An old
delivery result cannot replace the status of a newer action. Reloading creates a
fresh run ID; each run permits at most 1000 actions.

## Read and reconcile

Copy the displayed synthetic run UUID into `$runId`. Read only final delivery
names, checking size before parsing. This bounded snapshot prints at most 100
records and reports whether more reconciliation is needed; repeat after quiescence
or inspect a deliberate later batch if there are more files. Treat content as
untrusted data, never commands. Do not rely on filesystem notification counts.

```powershell
$runId = '<displayed run UUID>'
$files = @(Get-ChildItem -LiteralPath $inbox -File -Filter 'localpost_*.dat' |
    Where-Object { $_.Name -match '^localpost_[0-9a-f-]{36}\.dat$' })
"Final files in snapshot: $($files.Count)"
$records = @($files | Select-Object -First 100 | ForEach-Object {
    if ($_.Length -gt 0 -and $_.Length -le 1024) {
        try { Get-Content -LiteralPath $_.FullName -Raw | ConvertFrom-Json -ErrorAction Stop }
        catch { Write-Warning 'Invalid JSON delivery; inspect privately.' }
    } else { Write-Warning 'Out-of-bound delivery; inspect privately.' }
})
$records | Where-Object { $_.runId -eq $runId } |
    Sort-Object actionSequence |
    Select-Object runId, correlationId, actionSequence, operation, expectedCount, observedCount, status
if ($files.Count -gt 100) { Write-Warning 'Snapshot truncated; reconcile remaining files.' }
```

Reconcile the run ID and action sequence against the UI's latest action count.
Messages can arrive out of order. Missing, duplicate, malformed or extra records
need explicit accounting; a successful UI status alone is not file evidence.
Keep private evidence and delivery paths out of the public repository. The
schema allows only a version, UUID run/correlation, bounded action/count integers,
`add_one`, and `match`/`mismatch`. It excludes raw objects, errors, stacks, URLs,
page content, credentials and user prose. localpost stores bytes unchanged and
does not validate this application schema.

When finished, disable logging, stop the pilot, then retire only this mailbox:

```powershell
Invoke-RestMethod -Method Delete -Uri "$daemon/registrations/$($registration.id)"
```

Unregistration preserves delivered files. Retain or remove the private inbox
according to the run's evidence needs; never delete unrelated deliveries.

## Controlled fault and checks

`COUNTER_STEP = 1` in `pilot.mjs` is the sole intended fault point. A separately
authorized M04.02 run may temporarily set it to `2`, restart the pilot server,
reload, click once and inspect
`expectedCount: 1`, `observedCount: 2`, `status: mismatch`. Restore `1`, restart
the pilot server, reload for a new run ID, and verify `1`, `1`, `match`. Assets
are embedded at build time; a browser reload alone does not pick up source edits.
Keep both runs distinct in private
evidence. Do not commit the injected fault.

```powershell
node --test examples/browser-diagnostics/pilot.test.mjs
go test ./examples/browser-diagnostics
```

Node uses built-in modules only; the repository check pins the supported version.
Tests cover schema/config bounds, exact body projection, the wrong delta,
transport isolation, timeout abort, one-send behavior, run limits, stale results,
and the static server allowlist. They do not exercise a real browser.
