# Container deployment

The public image is `ghcr.io/sixbuckssolutions/localpost`, with `0.1.0` and
`latest` tags for Linux AMD64 and ARM64. Pulls work without signing in. Use the
version tag for a stable release or pin the multi-architecture index digest.

```sh
docker pull ghcr.io/sixbuckssolutions/localpost@sha256:b1df918f00f3da9298a51ba148edb9dbdb9f438b2052651e6fcd7c18f147fab4
```

The image uses `scratch` and the exact published v0.1.0 Linux release binaries.
Its default user is `65532:65532`, its entrypoint is `/localpost`, and its stop
signal is `SIGINT`.

## Linux host

The service listens on loopback. Linux Docker host networking lets host clients
reach `127.0.0.1:53147`. Bridge networking with a published port cannot reach
this loopback listener. Use a trusted host because the API has no authentication
and allows all origins. See [Docker host networking](https://docs.docker.com/engine/network/drivers/host/).

Create a stable private inbox parent before starting the shared service. Mount it
at the same absolute path inside the container so agents can register paths they
also read on the host. Run as the host user's numeric UID and GID so that user
can create inboxes and read deliveries. The filesystem must support hard links.
This example uses a Linux shell and assumes `curl` and `jq` are installed.

```sh
umask 077
inbox_parent="$HOME/.local/share/localpost/inboxes"
mkdir -p "$inbox_parent"
chmod 700 "$inbox_parent"
docker pull ghcr.io/sixbuckssolutions/localpost:0.1.0
docker run -d --name localpost --network host --read-only --stop-timeout 40 \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$inbox_parent,dst=$inbox_parent" \
  ghcr.io/sixbuckssolutions/localpost:0.1.0
```

Use a parent path without commas because Docker's `--mount` syntax uses commas
as separators. Keep this shared service running across agent runs. Each agent
creates its own private directory under the mounted parent and registers that
absolute path. A delivered file remains on the host after the container stops.
See [Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/).

```sh
inbox=$(mktemp -d "$inbox_parent/run.XXXXXXXX")
registration=$(jq -n --arg directory "$inbox" '{directory: $directory}' |
  curl --fail-with-body -sS -H 'Content-Type: application/json' \
    --data-binary @- http://127.0.0.1:53147/registrations)
id=$(printf '%s' "$registration" | jq -r '.id')
curl --fail-with-body -sS -H 'Content-Type: application/json' \
  --data-binary '{"event":"example"}' \
  "http://127.0.0.1:53147/registrations/$id/messages"
ls -l "$inbox"/localpost_*.dat
curl --fail-with-body -sS -X DELETE "http://127.0.0.1:53147/registrations/$id"
```

Inspect and remove only the files and run directory owned by that agent when
finished. Retiring a registration leaves deliveries in place. Registrations are
in memory and must be created again after the daemon restarts. Do not restart the
shared service for each run or remove other agents' inboxes.

To use a different port, start the service with arguments after the image name.
The following is an alternative to the first `docker run`, using a different
container name and port.

```sh
docker run -d --name localpost-custom --network host --read-only --stop-timeout 40 \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$inbox_parent,dst=$inbox_parent" \
  ghcr.io/sixbuckssolutions/localpost:0.1.0 --port 53148
```

Use `http://127.0.0.1:53148` for that instance's API requests. Stop an owned
instance with `docker stop localpost` or `docker stop localpost-custom` as
appropriate. The 40-second Docker timeout allows for localpost's 30-second
network grace period. Filesystem operations may take longer. See
[Docker stop](https://docs.docker.com/reference/cli/docker/container/stop/).

## Docker Desktop

Docker Desktop 4.34 and later offers host networking as an opt-in feature. Its
behavior, path sharing, permissions and this image need separate verification on
Windows and macOS. Translate shared Windows paths to absolute Linux paths visible
inside the container before registering them. The existing v0.1.0 platform test
matrix records archive and Linux Docker testing. Docker Desktop verification
remains outside the recorded results.
See [Docker host networking](https://docs.docker.com/engine/network/drivers/host/).

localpost is MIT-licensed and provided as is without warranty. There is no support
commitment or guarantee of a response or fix for an issue.

## Image verification

Both architectures were built and tested on native Ubuntu 26.04.1 hosts with
Docker Engine 29.4.2 and ext4 inbox mounts. The checks cover the exact embedded
binary and licenses, default nonroot ownership, read-only root filesystem,
default and custom ports, API/CORS behavior, byte-exact host-visible files,
disconnect cleanup and an admitted upload completing during `docker stop`.
Anonymous pulls received the same tested image identities and passed the full
container checks again. The [native and anonymous-pull jobs](https://github.com/SixbucksSolutions/localpost/actions/runs/42034182097)
retain the image and verification evidence. The [promotion run](https://github.com/SixbucksSolutions/localpost/actions/runs/42035184223)
applied that verified index to `latest` using the retained results.
