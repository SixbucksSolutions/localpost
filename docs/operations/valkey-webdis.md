# Valkey and Webdis candidate operations

This is an **isolated adoption candidate**, not the released v0.2.1 service.
The independent Windows scheduled task continues to serve v0.2.1 on
`127.0.0.1:53147`. Do not replace it, notify customer agents, or cut over until
the solution is locked and Terry separately authorizes the cutover. See the
[client contract](../contracts/valkey-webdis.md) and
[deployment profile](../../deploy/valkey-webdis/README.md).

## Qualified candidate

The [qualification receipt](https://github.com/SixbucksSolutions/localpost/blob/main/docs/qualification/valkey-webdis-adoption-r1.md) links
the exact tested bundle from source `91f19e6` and records platform coverage.
Use that artifact for
review. Its frozen build manifest keeps the original pending status, while the
separate receipt records completed qualification. Shared cutover remains deferred.

## Build the private candidate bundle

Once the source is reviewed and frozen, build from a clean checkout of the
identified commit and a new or empty absolute private directory outside the
repository. Use PowerShell 7 or later with Go 1.27.3 from the repository root.

```powershell
./scripts/build-adoption-candidate.ps1 -OutputDirectory C:\private\localpost-adoption-r1
```

The builder exports clean `HEAD`, builds the clients twice from separate source
extractions, compares their hashes, and writes an unpublished `adoption-r1`
bundle. Qualification remains pending until evidence is recorded. Preserve the
exact resulting files for qualification and any subsequent publication. Do not substitute a rebuilt set. Story #0049 must qualify the resulting bundle before handoff.

The bundle contains Windows AMD64, Linux AMD64 and Linux ARM64 clients under
`bin/`, the Compose profile and image metadata under `deploy/valkey-webdis/`,
the contract and this guide under `docs/`, licenses, `manifest.json`, and
`SHA256SUMS`. Review the manifest and verify the checksums before use.

## Start and stop an isolated candidate

Docker Compose v2 and the pinned images in the deployment profile are required.
Confirm port 53170 is unused and choose a unique Compose project name. Run these PowerShell commands from the candidate bundle root.

```powershell
$env:LOCALPOST_PORT = '53170'
$env:LOCALPOST_MAXMEMORY = '128mb'
docker compose -p localpost-candidate -f deploy/valkey-webdis/compose.yaml up -d --wait
curl.exe http://127.0.0.1:53170/PING
```

The native response should contain `{"PING":[true,"PONG"]}`. Stop only this
candidate with the same project name and Compose file.

```powershell
docker compose -p localpost-candidate -f deploy/valkey-webdis/compose.yaml down
```

Never run `down` against another project's containers. The configured Valkey
`maxmemory` defaults to `128mb`, with `noeviction`. It is not a combined process
RSS limit. Webdis and client buffers, base64 expansion and other allocations
need additional memory. Inspect the JSON body for backend errors even on HTTP
200. Accepted messages remain until cleared, deleted, or Valkey exits. There is
no per-mailbox quota, expiry, automatic trimming or retry.

## Client workflow

Use a separately built `localpost-client` for the candidate. The following
PowerShell example uses the bundled Windows binary and an operator-owned
`input.bin` in the current directory. `get` writes raw bytes. Redirect its output
only with a byte-preserving mechanism. In particular,
do not use PowerShell text pipelines for arbitrary binary data.

```powershell
$client = '.\bin\windows-amd64\localpost-client.exe'
$base = 'http://127.0.0.1:53170'
$registration = & $client --url $base register | ConvertFrom-Json
$id = $registration.id
cmd /c "`"$client`" --url $base send $id < input.bin"
$page = & $client --url $base read $id --count 16 | ConvertFrom-Json
$messageId = $page.messages[0].id
cmd /c "`"$client`" --url $base get $id $messageId > output.bin"
& $client --url $base clear $id
& $client --url $base delete $id
```

`register`, `send`, `read`, `clear` and `delete` produce JSON on stdout.
`get` produces only the original bytes. `read` is non-destructive: each message
includes a base64 `data` value without the stored `b64:` prefix. Use the last
`next_after` with `--after` for a bounded next page. Check exit status and the
structured stderr error before relying on output. Mutations with lost responses
may have succeeded, so retries can duplicate or remove later messages. The
[contract](../contracts/valkey-webdis.md) defines the complete CLI, bounds and
error semantics.

On Linux, downloaded or archived artifacts may lose executable permissions.
From the bundle root, select the client for the host architecture and restore
its execute bit. Standard shell redirection preserves binary input and output:

```sh
chmod +x bin/linux-amd64/localpost-client
client=bin/linux-amd64/localpost-client
base=http://127.0.0.1:53170
# Set id and message_id from successful register and read JSON responses.
"$client" --url "$base" send "$id" < input.bin
"$client" --url "$base" get "$id" "$message_id" > output.bin
```

On ARM64, use `bin/linux-arm64/localpost-client` in both lines. Check command
exit status and stderr before consuming output.

A browser uses the same native Webdis endpoint and client-generated UUIDv4.
This illustrates send framing for a small ASCII diagnostic. Production senders
must implement the full validation and size limits in the contract:

```javascript
const id = crypto.randomUUID();
const key = `localpost:v1:${id}`;
const payload = new TextEncoder().encode('{"phase":"rendered"}');
const data = 'b64:' + btoa(String.fromCharCode(...payload));
const url = 'http://127.0.0.1:53170/XADD/' + encodeURIComponent(key) + '/*/data';
const response = await fetch(url, {
  method: 'PUT', headers: { 'Content-Type': 'text/plain;charset=UTF-8' },
  credentials: 'omit', redirect: 'error', cache: 'no-store', body: data,
});
const result = await response.json();
// Validate HTTP status and the XADD result, including Webdis backend errors.
```

This is generic command-oriented HTTP, not the v0.2.1 registration/message
REST API. Direct clients must obey the contract. Webdis does not validate the
payload schema or decoded size. Browser CSP and local-network permissions still
apply. Real-browser send/read qualification is required before handoff.

IDs are reusable addresses, not retired registrations. Delete removes the
stream, but a later send to the same ID recreates it. Clear removes current
entries while retaining the stream's last-generated ID. Concurrent later sends
may remain after either operation. Generate a fresh ID when finished and discard
old pagination positions after deletion or Valkey restart. Stream IDs can recur
across stream lifetimes. Valkey restart loses all retained messages. Restarting
only Webdis preserves them while Valkey remains running.

The profile disables application persistence, PID output and daemonization and
has no writable application mounts or runtime application state files. The
Webdis configuration is mounted read-only at `/etc/webdis.prod.json`. Container
engine metadata, image storage, operating-system paging/crash dumps, and
operator-managed console capture and exports are outside that scope. The #0043
study in the source repository records narrower observed no-write evidence.
Qualification must establish the candidate profile's behavior.

## Future cutover and rollback checklist

This checklist is preparation only. It does not authorize cutover or notifications.

1. Lock the solution and obtain Terry's explicit cutover authorization. Complete
   independent review and the contract's qualification obligations on the exact
   candidate artifacts, including Windows binary I/O, real-browser access,
   capacity and no-runtime-file-write evidence. Record versions, checksums,
   configuration and results.
2. Inventory the current scheduled task, executable, configuration and ownership.
   Retain the v0.2.1 executable and task configuration for rollback. Agree on a
   maintenance window and a migration plan for each client. No API compatibility
   is implied. Copy any needed old mailbox data outside its root before any
   v0.2.1 restart, which clears root children.
3. Only after authorization, coordinate clients and stop the current task
   gracefully. Confirm port 53147 is free, start the locked candidate on loopback,
   and verify its effective configuration, health and bounded end-to-end delivery.
   Record the outcome and communicate the approved client migration instructions.
4. If verification fails or rollback is directed, stop only the candidate using
   its exact Compose project. Restore and start the retained scheduled task and
   verify independent ownership, HTTP and file delivery. Re-register v0.2.1
   mailboxes and restore any needed retained data from the operator's copy.
   Neither candidate memory nor old registration IDs transfer automatically.
