# Container deployment

The v0.2.0 container workflow below is in development and pending release and
qualification. The published v0.1.0 image and its tested digest remain available.
See the [v0.1.0 container guide](https://github.com/SixbucksSolutions/localpost/blob/v0.1.0/docs/containers.md)
for that release's unversioned API and recorded test matrix.

## Linux host

The daemon listens on loopback. Linux Docker host networking lets host clients
reach `127.0.0.1:53147`. Use a trusted host because the API has no authentication
and allows all origins. The filesystem must support hard links.

Provision an existing absolute mailbox root with private permissions. Mount it
at the same absolute path in the container and on the host. Run the daemon under
the consuming agent's UID and GID. Mailbox directories use `0700` and files use
`0600`. The root is supplied explicitly and has no implicit daemon default.

The example expects a locally built and qualified v0.2.0 image tagged
`localpost:0.2.0`. It does not imply a published image.

```sh
umask 077
mailbox_root="$HOME/.local/share/localpost/mailboxes"
mkdir -p "$mailbox_root"
chmod 700 "$mailbox_root"
docker run -d --name localpost --network host --read-only --stop-timeout 40 \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$mailbox_root,dst=$mailbox_root" \
  localpost:0.2.0 --mailbox-root "$mailbox_root"
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
retiring its registration. Delete and restart preserve directories and files.
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
