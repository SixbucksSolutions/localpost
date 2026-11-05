# Container deployment

The published `ghcr.io/sixbuckssolutions/localpost:0.2.1` image supports Linux
AMD64 and ARM64. Its multi-platform index digest is
`sha256:91b9ce019dbd86181c2616a973af7200e1cfba7f8da9222593d06e9e8fe267f1`.
The `latest` tag points to the same index. The v0.1.0 and v0.2.0 tags remain available.
See the [v0.1.0 container guide](https://github.com/SixbucksSolutions/localpost/blob/6889c8c/docs/containers.md)
for that release's unversioned API and recorded test matrix.

## Linux host

The daemon listens on loopback. Linux Docker host networking lets host clients
reach `127.0.0.1:53147`. Use a trusted host because the API has no authentication
and allows all origins. The filesystem must support hard links.

Provision an existing absolute mailbox root with private permissions. Mount it
at the same absolute path in the container and on the host. Run the daemon under
the consuming agent's UID and GID. Mailbox directories use `0700` and files use
`0600`. The root is supplied explicitly and has no implicit daemon default.

The example uses the published v0.2.1 image.

```sh
umask 077
mailbox_root="$HOME/.local/share/localpost/mailboxes"
mkdir -p "$mailbox_root"
chmod 700 "$mailbox_root"
docker run -d --name localpost --network host --read-only --stop-timeout 40 \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$mailbox_root,dst=$mailbox_root" \
  ghcr.io/sixbuckssolutions/localpost:0.2.1 --mailbox-root "$mailbox_root"
```

Use a path without commas because Docker's `--mount` syntax uses commas as
separators. Keep the shared service running across agent runs. The service
creates a new UUIDv4-named child for each registration.

```sh
base=http://127.0.0.1:53147/api/2026-11-05
registration=$(curl --fail-with-body -sS -H 'Content-Type: application/json' \
  --data-binary '{}' "$base/registrations")
id=$(printf '%s' "$registration" | jq -r '.id')
inbox=$(printf '%s' "$registration" | jq -r '.directory')
curl --fail-with-body -sS -H 'Content-Type: application/json' \
  --data-binary '{"event":"example"}' "$base/registrations/$id/messages"
ls -l "$inbox"/localpost_*.dat
curl --fail-with-body -sS -X DELETE "$base/registrations/$id"
```

Inspect and remove only the files and directory owned by the agent after
retiring its registration. With the published v0.2.0 image, delete and restart
preserve directories and files. v0.2.1 deletes every child of its
dedicated root at startup, including container restart. Copy needed data outside the root first. The root itself remains.
Registrations are in memory and must be created again after restart. Lost
registration responses can leave orphaned directories that need retention and
cleanup. Do not restart the shared service for each run.

Pass `--port 53148` after `--mailbox-root` to choose another port. Docker's
40-second stop timeout allows for localpost's 30-second network grace period.
Filesystem operations can take longer. See [Docker host networking](https://docs.docker.com/engine/network/drivers/host/)
and [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/).

## Docker Desktop

Docker Desktop host networking, path sharing and permissions need separate
verification on Windows and macOS. The returned directory must be accessible at
the same absolute path to the host agent. Do not infer this from Linux testing.

localpost is MIT-licensed and provided as is without warranty. The recorded test
matrix is evidence of the environments tested and carries no support commitment.


## Qualification and publication

The [native image qualification run](https://github.com/SixbucksSolutions/localpost/actions/runs/42072628475)
passed on both architectures before publication. The versioned index is published.
The [publication run](https://github.com/SixbucksSolutions/localpost/actions/runs/42073388928)
passed anonymous pull and runtime checks on both architectures and promoted
`latest`.

The `Publish qualified containers` workflow defaults `publish` to `false`.
That mode obtains the exact candidate artifact identified by
`release/0.2.1-candidate.json`, verifies the archive and checksum-file hashes,
and packages and tests each image on native Linux amd64 and arm64 runners.
The retained `qualified-amd64` and `qualified-arm64` artifacts contain the
exact tested images. Binary and packaged legal/API metadata are compared with
the verified archives. Image labels distinguish the release source commit from
the workflow packaging commit.

The image declares UID/GID `65532:65532`. Qualification checks that image
configuration and runs `--version` with the default user. Runtime delivery,
private mailbox permissions, the API contract, port conflicts and graceful
stop are tested with the host agent's UID/GID and a same-path private bind
mount, as in the Linux example above. The host agent must be able to read its
own `0700` mailboxes and `0600` delivered files.

Publication requires an explicit `publish=true` dispatch, a manifest marked
`publicationStatus=published`, and `qualification.status=passed`. The workflow
loads the saved native images, checks their recorded identities, publishes a
versioned multi-platform index, and verifies anonymous pulls and runtime
behavior on both architectures before promoting `latest`. An existing
`0.2.1` index must have the identical digest. Previous version tags are retained.

If only the final promotion failed, dispatch the identical workflow revision
with `publish=true` and `promote_run_id` set to that failed run. Recovery checks
that both native qualifications, publication and both anonymous qualifications
succeeded, then reuses their retained receipts without rebuilding images.
Other failures require their failed workflow jobs to be retried and verified.
