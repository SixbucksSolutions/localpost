#!/usr/bin/env bash
# Run only on an isolated CI VM. All listeners, containers and temporary files are owned by this run.
set -euo pipefail

architecture=${CERTIFY_ARCHITECTURE:?}
platform=${CERTIFY_PLATFORM:?}
package="$RUNNER_TEMP/unpacked/localpost_${RELEASE_VERSION:?}_linux_$architecture"
binary="$package/localpost"
report="$RUNNER_TEMP/certification"
mkdir -p "$report"
export TMPDIR
TMPDIR=$(mktemp -d "$RUNNER_TEMP/localpost-certification.XXXXXX")
mkdir "$TMPDIR/mailboxes"
mailbox_root="$TMPDIR/mailboxes"
mkdir "$TMPDIR/default-mailboxes"
default_root="$TMPDIR/default-mailboxes"
name="localpost-certify-${GITHUB_RUN_ID:?}-${GITHUB_JOB:?}"
image=alpine:3.24.3
pids=()
containers=()
cleanup() {
  local pid container
  for pid in "${pids[@]}"; do kill -INT "$pid" 2>/dev/null || true; done
  for container in "${containers[@]}"; do
    docker logs "$container" >"$report/$container.log" 2>&1 || true
    docker rm --force "$container" >/dev/null 2>&1 || true
  done
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then kill -TERM "$pid" 2>/dev/null || true; fi
    wait "$pid" 2>/dev/null || true
  done
  rm -rf -- "$TMPDIR"
}
trap cleanup EXIT

case "$architecture" in
  amd64) test "$(uname -m)" = x86_64 ;;
  arm64) test "$(uname -m)" = aarch64 ;;
  *) exit 1 ;;
esac
test "$(id -u)" != 0
# shellcheck disable=SC1091
. /etc/os-release
test "$ID" = ubuntu
case "$platform" in
  ubuntu-24.04) test "$VERSION_ID" = 24.04 ;;
  ubuntu-26.04|alpine-3.24) test "$VERSION_ID" = 26.04 ;;
  *) exit 1 ;;
esac
{
  printf 'architecture=%s\nplatform=%s\n' "$architecture" "$platform"
  printf 'host-userspace=%s %s\nhost-uid=%s\n' "$ID" "$VERSION_ID" "$(id -u)"
  printf 'host-kernel='; uname -srmo
  printf 'filesystem='; stat -f -c '%T' "$TMPDIR"
  printf 'mount-filesystem=%s\n' "$(findmnt -n -o FSTYPE -T "$TMPDIR")"
  printf 'filesystem-device=%s\n' "$(stat -c %d "$TMPDIR")"
  printf 'source=%s\nbuilder=%s\n' "${SOURCE_COMMIT:?}" "$GITHUB_SHA"
  sha256sum "$binary" "$package/build-info.json"
} >"$report/platform.txt"

if [[ "$platform" == alpine-3.24 ]]; then
  docker pull "$image"
  test "$(docker image inspect --format '{{.Architecture}}' "$image")" = "$architecture"
  test "$(docker run --rm --entrypoint /bin/uname "$image" -m)" = "$(uname -m)"
  test "$(docker run --rm --entrypoint /bin/cat "$image" /etc/alpine-release)" = 3.24.3
  {
    printf 'container-userspace=Alpine 3.24.3\ncontainer-kernel=shared Ubuntu host kernel\n'
    docker image inspect --format 'image-id={{.Id}} image-architecture={{.Architecture}} repo-digests={{json .RepoDigests}}' "$image"
  } >>"$report/platform.txt"
fi

start() {
  local suffix=$1
  local service_root=$2
  shift 2
  if [[ "$platform" == alpine-3.24 ]]; then
    local container="$name-$suffix"
    containers+=("$container")
    docker run --detach --name "$container" --network host \
      --user "$(id -u):$(id -g)" --env PATH= --env "TMPDIR=$TMPDIR" \
      --mount "type=bind,source=$package,target=/package,readonly" \
      --mount "type=bind,source=$TMPDIR,target=$TMPDIR" \
      --entrypoint /package/localpost "$image" --mailbox-root "$service_root" "$@" >/dev/null
  else
    env PATH= "$binary" --mailbox-root "$service_root" "$@" >"$report/$suffix.log" 2>&1 &
    pids+=("$!")
  fi
}
wait_ready() {
  local port=$1
  for _ in {1..50}; do
    if curl --silent --output /dev/null --connect-timeout 1 "http://127.0.0.1:$port/"; then return; fi
    sleep 0.1
  done
  echo "Listener on $port did not become ready" >&2
  return 1
}
assert_running() {
  local pid container
  for pid in "${pids[@]}"; do kill -0 "$pid"; done
  if [[ "$platform" == alpine-3.24 ]]; then
    for container in "$name-override" "$name-default"; do
      test "$(docker inspect --format '{{.State.Running}}' "$container")" = true
    done
  fi
}

# Both ports must be free before creating this run's listeners.
for port in 53147 53151; do
  if curl --silent --output /dev/null --connect-timeout 1 "http://127.0.0.1:$port/"; then
    echo "Isolated CI port $port was already occupied" >&2
    exit 1
  fi
done
start override "$mailbox_root" --port 53151
start default "$default_root"
wait_ready 53151
wait_ready 53147
assert_running
if [[ "$platform" == alpine-3.24 ]]; then
  for container in "${containers[@]}"; do
    container_uid=$(docker exec "$container" /usr/bin/id -u)
    container_path=$(docker exec "$container" /bin/sh -c 'printf %s "$PATH"')
    container_device=$(docker exec "$container" /bin/stat -c %d "$TMPDIR")
    test "$container_uid" = "$(id -u)"
    test "$container_path" = ''
    test "$container_device" = "$(stat -c %d "$TMPDIR")"
    test "$(docker exec "$container" /bin/uname -m)" = "$(uname -m)"
    {
      printf 'container=%s uid=%s PATH=%s tmp-device=%s\n' "$container" "$container_uid" "$container_path" "$container_device"
      docker exec "$container" /package/localpost --version
      docker inspect --format 'user={{.Config.User}} network={{.HostConfig.NetworkMode}} mounts={{json .Mounts}}' "$container"
    } >>"$report/platform.txt"
  done
fi
for port in 53147 53151; do
  args=()
  conflict_root="$TMPDIR/conflict-$port"
  mkdir "$conflict_root"
  if [[ "$port" == 53151 ]]; then args=(--port "$port"); fi
  if [[ "$platform" == alpine-3.24 ]]; then
    containers+=("$name-conflict-$port")
    if docker run --name "$name-conflict-$port" --network host --user "$(id -u):$(id -g)" \
      --env PATH= --mount "type=bind,source=$package,target=/package,readonly" \
      --mount "type=bind,source=$TMPDIR,target=$TMPDIR" \
      --entrypoint /package/localpost "$image" --mailbox-root "$conflict_root" "${args[@]}" >"$report/conflict-$port.log" 2>&1; then
      echo "Occupied port $port was accepted" >&2; exit 1
    fi
  else
    if env PATH= "$binary" --mailbox-root "$conflict_root" "${args[@]}" >"$report/conflict-$port.log" 2>&1; then
      echo "Occupied port $port was accepted" >&2; exit 1
    fi
  fi
  grep -q "127.0.0.1:$port" "$report/conflict-$port.log"
done
node scripts/qualify-packaged-service.mjs --package-dir "$package" \
  --base-url http://127.0.0.1:53151 --expected-commit "$SOURCE_COMMIT" --mailbox-root "$mailbox_root" \
  --evidence "$report/qualification.json"
assert_running

if [[ "$platform" == alpine-3.24 ]]; then
  for suffix in override default; do docker kill --signal INT "$name-$suffix" >/dev/null; done
  for _ in {1..50}; do
    if [[ "$(docker inspect --format '{{.State.Running}}' "$name-override")" == false && \
          "$(docker inspect --format '{{.State.Running}}' "$name-default")" == false ]]; then break; fi
    sleep 0.1
  done
  for suffix in override default; do
    test "$(docker inspect --format '{{.State.Running}}' "$name-$suffix")" = false
    test "$(docker inspect --format '{{.State.ExitCode}}' "$name-$suffix")" = 0
  done
else
  for pid in "${pids[@]}"; do kill -INT "$pid"; done
  for _ in {1..50}; do
    alive=0
    for pid in "${pids[@]}"; do if kill -0 "$pid" 2>/dev/null; then alive=1; fi; done
    if [[ "$alive" == 0 ]]; then break; fi
    sleep 0.1
  done
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then echo 'SIGINT shutdown exceeded bound' >&2; exit 1; fi
    wait "$pid"
  done
  pids=()
fi
printf 'runtime-PATH=empty\nnonroot=verified\ndefault-and-override=passed\ndefault-and-override-conflicts=passed\nSIGINT-shutdown=passed\n' >>"$report/platform.txt"

# Exercise restart cleanup under the certified userspace as well as the Node probe.
mkdir -p "$mailbox_root/stale-mailbox/nested"
printf '\000\377stale' >"$mailbox_root/stale-mailbox/nested/stale.dat"
printf 'stray bytes' >"$mailbox_root/stray-file"
start restart "$mailbox_root" --port 53151
wait_ready 53151
test -z "$(ls -A "$mailbox_root")"
mkdir -p "$mailbox_root/crash-mailbox"
printf 'crash bytes' >"$mailbox_root/crash-mailbox/stale.dat"
if [[ "$platform" == alpine-3.24 ]]; then
  docker logs "$name-restart" >"$report/restart.log" 2>&1
  docker kill --signal KILL "$name-restart" >/dev/null
  test "$(docker inspect --format '{{.State.Running}}' "$name-restart")" = false
else
  kill -KILL "${pids[-1]}"
  wait "${pids[-1]}" 2>/dev/null || true
  pids=()
fi
grep -q 'WARNING' "$report/restart.log"
test "$(cat "$mailbox_root/crash-mailbox/stale.dat")" = 'crash bytes'
start crash-restart "$mailbox_root" --port 53151
wait_ready 53151
test -z "$(ls -A "$mailbox_root")"
if [[ "$platform" == alpine-3.24 ]]; then
  docker logs "$name-crash-restart" >"$report/crash-restart.log" 2>&1
  docker stop --signal SIGINT --time 10 "$name-crash-restart" >/dev/null
  test "$(docker inspect --format '{{.State.ExitCode}}' "$name-crash-restart")" = 0
else
  kill -INT "${pids[-1]}"
  wait "${pids[-1]}"
  pids=()
fi
grep -q 'WARNING' "$report/crash-restart.log"
printf 'normal-and-SIGKILL-restart-cleanup=passed\n' >>"$report/platform.txt"

# The workflow has verified all cmd/localpost sources against the original source commit.
# Compile a static test executable so Alpine exercises its own native filesystem/runtime.
CGO_ENABLED=0 GOARM64=v8.0 GOAMD64=v1 go test -c -o "$TMPDIR/collision.test" ./cmd/localpost
if [[ "$platform" == alpine-3.24 ]]; then
  docker run --rm --network none --user "$(id -u):$(id -g)" --env "TMPDIR=$TMPDIR" \
    --mount "type=bind,source=$TMPDIR,target=$TMPDIR" --entrypoint "$TMPDIR/collision.test" \
    "$image" -test.run '^TestMessageStorageFailures$' -test.count=1 -test.v >"$report/collision.txt" 2>&1
else
  "$TMPDIR/collision.test" -test.run '^TestMessageStorageFailures$' -test.count=1 -test.v >"$report/collision.txt" 2>&1
fi
printf 'source-collision-tests=passed\n' >>"$report/platform.txt"
