# #0043 — In-memory messaging trade study

Study date 2026-11-05. Terry authorized up to 60 minutes, starting at
12:42:26 EST. This is a build-versus-adopt assessment with bounded experiments,
not production qualification or a load benchmark.

## Recommendation

**Use Valkey plus Webdis as the first adoption candidate — RECOMMENDED.**
It demonstrated retained multi-message storage, repeatable HTTP reads, browser
access and CLI access with persistence disabled. It avoids implementing a new
queue. The cost is two components, Linux/container deployment, and a
command-oriented HTTP interface.

Mosquitto is the stronger alternative when native Windows and a single broker
matter more than HTTP request/response semantics. Its current release passed a
single-WebSocket-port browser/CLI test. Its offline queues require an established
session and consume deliveries, and its native Windows publisher has a byte-fidelity
gap. These are explicit choices to accept before adoption.

The survey establishes that existing software can provide retained RAM-only
messaging. It does not establish a drop-in replacement satisfying every packaging,
API and CLI preference. Building another queue should follow a concrete rejection
of these tradeoffs.

## Requirements and preferences

The required outcome is small informational messages retained in RAM for a
subscription, accessible to agents and browsers, with explicit storage limits,
stdout-friendly tooling, loopback binding, and loss of unexported data on restart.
The intended runtime creates no application state files. Console output and
operator-owned exports are allowed.

Plain HTTP/JSON, non-destructive inspection, one portable executable, native
Windows, human-readable flags and CGO-disabled builds are important preferences.
They are assessed separately from message semantics. A Docker deployment can
encapsulate native dependencies, so the implementation language alone is not a
reason to reject mature software.

## Comparison

| Candidate | Retained RAM messages | Filesystem finding | Browser and CLI fit | Principal tradeoff |
| --- | --- | --- | --- | --- |
| Valkey + Webdis | Streams support history and non-destructive reads | No regular-file writes observed with the tested standalone profile | HTTP/JSON browser access and CLI requests passed | Two Linux services, command-shaped API, response-body error checks |
| Mosquitto | Offline QoS 1 queues after session setup, or latest retained value per topic | Windows working directory unchanged. Linux 2.0.22 traces showed no regular-file writes | Current 2.1.2 browser and native CLI share one WebSocket listener | MQTT lifecycle and consumption semantics, plus Windows stdin CRLF translation |
| NATS Core | Connected subscribers only | No persistence required | Mature native CLI and browser WebSocket client | Fails absent-consumer retention |
| NATS JetStream memory storage | Yes, including delayed consumption | Startup creates/probes its store directory even for memory streams | Native CLI replay tested, browser support documented | Fails the strict runtime-file rule in the tested release |
| ActiveMQ Artemis | Yes with configuration | Stock launcher creates lock/log files. PAGE can write with persistence disabled | Protocol clients and CLI exist | JVM/configuration footprint and custom profile needed |
| Beanstalkd | Yes, volatile work queues when binlogging is off | RAM-only configuration documented, not runtime-tested here | TCP protocol, additional browser/CLI integration | Weaker interface fit than the shortlisted options |
| Custom localpost memory service | Proposed | Could be designed without filesystem access | Exact HTTP/CLI contract could be built | New retention, error, concurrency and lifecycle implementation to maintain |

RabbitMQ was screened out of the narrow shortlist because its current queues
actively move messages to disk. That does not diminish its suitability as a
general broker. [RabbitMQ queue documentation](https://www.rabbitmq.com/docs/queues)

## Observed experiments

### Valkey 9.0.4 and Webdis 0.1.22

Ubuntu 26.04 packages were unpacked into an isolated WSL directory without
installing services. These package versions differ from the tagged sources reviewed, Valkey 9.0.7
and Webdis 0.1.25. Current-version adoption requires a new qualification run.

- Persistence, daemonization and PID output were disabled. Valkey used
  `maxmemory 128mb` with `noeviction`. Webdis logged through `/dev/stderr`.
- A producer stored two messages before reading. Repeated reads returned both.
- Valkey CLI stdin/raw output preserved a binary payload in the tested framing.
- HTTP PUT accepted messages and binary bytes. JSON reads exposed stream
  records, and raw RESP output preserved binary bytes.
- Real Chrome completed cross-origin HTTP send and repeated read through Webdis.
  Browser test diagnostics were exported through the existing localpost service.
- An 8 MiB SET-based capacity test rejected additional storage, preserved all
  accepted values byte for byte, and accepted writes again after explicit deletion.
  Stream-specific capacity remains untested. Valkey accounts its own data memory.
  The combined process RSS and Webdis request buffers have separate overhead.
- Webdis returned HTTP 200 with a JSON error result for the memory rejection.
  Scripts must inspect the body. `curl --fail` alone does not detect that error.
- Linux syscall traces covering startup, messaging, reads and shutdown showed no
  regular-file writes. Webdis opened `/dev/stderr`, which resolved to a pipe,
  and attempted `fsync` on that pipe. Broker working directories remained empty.
- Valkey restarted empty.

Valkey streams offer the desired retained/read/delete operations. Webdis provides
an existing generic HTTP interface. A mailbox-specific REST contract would still
need an adapter. Its stock Docker command starts an embedded Redis with file
logging, so an adoption profile must use the documented external-backend mode. Its HTTP API
exposes backend commands, including mutation through GET. A mailbox-oriented
REST contract and conventional HTTP error statuses would require an adapter.
[Valkey streams](https://valkey.io/topics/streams-intro/),
[Valkey persistence](https://valkey.io/topics/persistence/),
[Webdis command format](https://github.com/nicolasff/webdis#command-format),
[Webdis external-backend deployment](https://github.com/nicolasff/webdis/blob/master/docs/webdis-docker-external-redis.md)

### Mosquitto 2.1.2 and 2.0.22

The current official Windows x64 installer was extracted without installation.
Its SHA-256 matched Microsoft's version-pinned WinGet metadata. Linux syscall
experiments used Ubuntu's 2.0.22 package. The two versions are reported separately.

- Native Windows 2.1.2 used one loopback WebSocket listener with persistence off,
  console logs and a numeric memory limit. Its working-directory watcher observed
  no mutations. This observation is narrower than an operating-system syscall trace.
- An initial CLI subscription established an offline QoS 1 session. Two messages
  published while disconnected were delivered on reconnect.
- Two retained publications to one topic yielded the latest value, not both values.
- Real Chrome with MQTT.js 5.16.1 sent and received binary bytes unchanged and
  received a native CLI publication on the same listener.
- The native Windows publisher's stdin path changed `00 ff 0d 0a 80` into
  `00 ff 0a 80`. The receiver uses binary stdout. Tagged publisher source reads
  text-mode stdin. A stock byte-exact Windows stdin contract is therefore unmet.
- Linux 2.0.22 retained/queued tests passed with no observed regular-file writes,
  and a restart lost the retained data as intended.

Queue and heap limits can drop messages or disconnect clients. They do not offer
Valkey's tested reject-new-write behavior. The memory limit uses numeric bytes,
so a human-readable launcher would be additional convenience code.
[Broker configuration](https://mosquitto.org/man/mosquitto-conf-5.html),
[Current CLI](https://mosquitto.org/man/mosquitto_sub-1.html),
[Windows publisher input](https://github.com/eclipse-mosquitto/mosquitto/blob/v2.1.2/client/pub_shared.c),
[MQTT retained/session semantics](https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html)

### NATS 2.15.2 and ActiveMQ Artemis 2.57.0

NATS CLI 0.5.1 demonstrated delayed retrieval from a memory stream. The same
native Windows run recorded temporary-file and directory creation/deletion on
JetStream startup. An empty final directory would have concealed those writes.
Tagged source corroborates the behavior. Memory-backed messages do not imply a
filesystem-free runtime. [NATS source](https://github.com/nats-io/nats-server/blob/v2.15.2/server/jetstream.go#L447-L462),
[Core NATS delivery](https://docs.nats.io/learn/core-nats/)

Zero persistence alone does not give the stock Artemis launcher a no-file
guarantee. The isolated Windows profile created `cli.lock` and file logs.
Pinned code also warns that PAGE policies still persist pages with persistence
disabled, conflicting with the documentation on that narrower point. A custom profile can remove paging and logs, while a custom launcher
would need to avoid the CLI lock. Live delivery was not qualified because the
host JVM failed during transport startup. This is a failed local experiment,
not a claim that Artemis cannot run on Windows.
[Persistence documentation](https://artemis.apache.org/components/artemis/documentation/latest/persistence.html#zero-persistence),
[Paging implementation](https://github.com/apache/artemis/blob/2.57.0/artemis-server/src/main/java/org/apache/activemq/artemis/core/server/impl/ActiveMQServerImpl.java),
[CLI lock implementation](https://github.com/apache/artemis/blob/2.57.0/artemis-cli/src/main/java/org/apache/activemq/artemis/cli/commands/tools/LockAbstract.java)

## Decision and qualification boundary

Favor adopting Valkey plus Webdis if two Linux/container components are acceptable.
Choose Mosquitto for a native-Windows-first deployment only after accepting MQTT
queue consumption and settling the CLI byte-fidelity requirement. Require a
specific operational or contract advantage before returning to a custom broker.

Before deployment, qualify selected current versions together, pin the images
and configuration, and test failure/output handling in real agent commands.
The proof of concept already demonstrates feasibility, but it is not a release
qualification. No candidate replaced the shared localpost daemon.

The no-write finding covers observed application-initiated regular-file writes
in the tested profile and workload. It excludes operator provisioning, test
harness artifacts, console capture, container-engine metadata, operating-system
paging and crash dumps. It does not promise that every administrative command,
plugin or alternate configuration is filesystem-free.

All study broker processes were stopped. Detailed traces, package identities,
source evidence and test harnesses are retained in the private #0043 study folder.