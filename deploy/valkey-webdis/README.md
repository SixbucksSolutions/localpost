# Valkey and Webdis runtime

This opt-in Linux-container profile uses the native Webdis command API and
Valkey streams. The existing shared file-delivery service remains the active
service until a separately authorized cutover.

From the repository root, start an isolated candidate on an unused loopback port.

```sh
LOCALPOST_PORT=53170 docker compose -f deploy/valkey-webdis/compose.yaml up -d --wait
curl http://127.0.0.1:53170/PING
LOCALPOST_PORT=53170 docker compose -f deploy/valkey-webdis/compose.yaml down
```

In PowerShell set `$env:LOCALPOST_PORT = '53170'` before the same Compose commands.
The default port is 53147. Check its ownership before starting there. Compose
uses project name `localpost-memory`; pass `-p <unique-name>` for each independent
candidate. Run `down` only with the candidate's exact project name and file.
Stopping or recreating Valkey loses all messages. Restarting Webdis alone retains
them while Valkey remains running. There is no host mailbox directory.

`LOCALPOST_MAXMEMORY` defaults to `128mb` and accepts Valkey's human-readable
memory sizes, for example `256mb`. `noeviction` rejects new writes at the backend
limit. This limit covers Valkey data accounting, with process memory and Webdis
request/response buffers additional. Webdis accepts HTTP requests up to 16 MiB;
the client contract limits decoded messages to 10 MiB. Inspect Webdis JSON error
bodies even when HTTP status is 200.

Only Webdis publishes a host port, bound to IPv4 loopback. The unauthenticated
command API allows only `PING`, `XADD`, `XRANGE`, `XTRIM` and `DEL` through a
Webdis allowlist and retains Webdis's browser CORS behavior. The backend
network is internal, with Valkey attached only to that network. Webdis also
uses a frontend bridge so Docker can publish its loopback port. That bridge
provides Webdis outbound routing. Docker requires a default-gateway network
for port publishing. The profile runs both processes under numeric UID/GID 65532
with read-only roots, including Valkey's `/data`.
There are no writable application mounts or anonymous data volumes. RDB, AOF,
daemonization and PID output are disabled. Webdis writes console diagnostics to
stderr, and Valkey writes console diagnostics to stdout. Docker's own logs,
image storage and host runtime metadata are operator-managed artifacts.

## Image provenance

Resolved through the Docker Hub registry v2 API on 2026-11-05. Compose pins the
multi-platform index, so tag movement cannot change the deployed image.

| Image | Index SHA-256 | Linux AMD64 manifest | Linux ARM64 manifest |
| --- | --- | --- | --- |
| `valkey/valkey:9.0.7-alpine` | `2d04268dfa26ab31cfa3fdc538866e76438b2d00e41db41b8cb86d780ac5a416` | `ba88e813493c4c449b0127b571240323ce862c12c98a78ec25da53859bc0a5b1` | `a4dc1d68c0257d0e8feecc4322321525931ac33dc2ab0dd68686665b3b55725d` |
| `nicolas/webdis:0.1.25` | `59549310212dd905019e249642c46de4928d6787e18fad336feaed6f4fbcf8c2` | `c7d7c0c22e99e49f38be9113f6b88e5939d42326cd6659e7c83572d19382d490` | `4fe7aa522c153e3f00ed38b462aae7a221f94cd81a0d95654b55a43da53a91d5` |

The Webdis image labels identify 0.1.25 and source revision
`56d0a9285bd04ff06d02f42e4896cf1a0a5cf548`. Its upstream Dockerfile selects the
latest source tag during its build. The immutable image pin and runtime version
qualification therefore matter separately from the Dockerfile tag. Valkey's
image environment identifies version 9.0.7. The resolved Valkey image config has
no declared volume, and its working directory is `/data`.

The profile bypasses Webdis's embedded-Redis command and Valkey's entrypoint.
Upstream source references are the
[Webdis Dockerfile](https://github.com/nicolasff/webdis/blob/0.1.25/Dockerfile),
[Webdis configuration parser](https://github.com/nicolasff/webdis/blob/0.1.25/src/conf.c),
[Webdis external backend guide](https://github.com/nicolasff/webdis/blob/0.1.25/docs/webdis-docker-external-redis.md),
and [Valkey container sources](https://github.com/valkey-io/valkey-container).

## Isolated qualification

`node deploy/valkey-webdis/qualify.mjs` creates a unique Compose project on port
53170 by default, inspects effective process/mount settings, exercises retained
reads, empty `b64:` payloads, graceful stops and restart loss, and removes only that project in a `finally` block.
Set `LOCALPOST_PORT` to another unused port if needed. It requires Docker Compose
v2 and Node.js. The workflow runs it on native AMD64 and ARM64 Linux runners.
The broader client and capacity qualification remains part of the adoption suite.


Set `LOCALPOST_EVIDENCE_DIR` to retain a JSON result and container console logs
from this isolated fixture. The workflow uploads these on success or failure.
Qualification checks Valkey's executable version and Webdis's startup banner
against the pinned versions, in addition to image labels and effective mounts.

The Docker networking behavior is documented in the
[Moby maintainer explanation](https://github.com/moby/moby/discussions/53256).
