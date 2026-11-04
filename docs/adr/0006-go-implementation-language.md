# ADR 0006: Implement localpost in Go

- **Status:** Accepted
- **Date:** 2026-11-04
- **Authority:** Terry delegated the final language choice between Rust and Go
  to the project maintainer and requested that it be recorded as an ADR.

## Context

localpost is a small, long-running HTTP-to-file service. It opens an admin
listener and a listener per mailbox, streams bounded request bodies to disk,
publishes complete files, and shuts listeners down predictably. It has no
application UI, database, durable queue, or payload-processing engine.

The choice should favor code that is straightforward to maintain, a small
operating burden, and clear handling of concurrent requests. Maximum throughput,
hard real-time behavior, and eliminating every garbage-collection pause are not
requirements. This decision is an engineering judgment, not a benchmark result.

## Decision

Implement the localpost daemon in **Go**, using the standard library first.
Use its HTTP server and ordinary networking, I/O, filesystem, context, and
synchronization facilities. Do not introduce a web framework merely to route
this small API.

Build a standalone executable for each supported operating system and
architecture. A person running a released executable should not need a separate
Go installation. Keep native dependencies out unless a concrete platform
requirement justifies them.

The implementation remains one shared service. Browser senders and thin
application adapters do not need to be written in Go and do not acquire their
own file-delivery implementation.

## Why Go

- **The service fits the standard library.** `net/http` provides HTTP serving,
  request-body limits, and context-bounded graceful shutdown. `net`, `io`, and
  `os` provide the remaining basic building blocks. The project can concentrate
  on mailbox ownership and delivery semantics rather than an HTTP stack.
- **Concurrency can stay explicit and small.** Goroutines, contexts, and
  synchronization are suitable for independent mailbox listeners and uploads.
  Registration, unregistration, and shutdown still need carefully defined
  ownership and admission rules.
- **Distribution matches the intended experience.** Go builds executables for
  the target platform. Shipping an executable fits an ordinary-user daemon
  with a command-line port override and no configuration file.
- **Maintenance cost is the deciding factor.** This bounded I/O service benefits
  more from a compact implementation and a narrow dependency surface than from
  additional control over memory management.

## Rust considered

Rust is a credible choice. Its ownership model provides compile-time memory
safety without a garbage collector, and its type system can prevent important
classes of concurrency mistakes. It can also produce a standalone executable.

For this project, the extra ownership and lifetime design across listener
lifecycles and asynchronous tasks, plus async-runtime and HTTP-crate choices,
would add implementation and maintenance work. Rust does not require async for
every server; a threaded implementation is possible. The comparison is with
the likely async design, not a claim that Rust cannot be simple.

Those costs could be justified by hard latency or memory constraints. localpost
has no such requirement. The borrow checker is not a reason to reject Rust in
general; the expected benefit here does not outweigh Go's simpler fit.

## Consequences and limits

- Go's garbage-collected runtime is part of the executable. No hard real-time
  latency or comparative performance claim follows from this choice.
- Go does not prove the absence of data races or make shutdown correct
  automatically. Exercise mailbox registration/unregistration, concurrent
  delivery, cancellation, and error paths with behavioral tests and the race
  detector where supported.
- File publication remains a platform-specific correctness obligation. Go's
  `os.Rename` documentation does not promise atomicity on non-Unix platforms,
  even within one directory, and replacement behavior differs. Do not assume
  that calling it alone satisfies the existing atomic, no-overwrite contract.
  Select and verify the necessary publication mechanism for each supported OS.
- UUIDv7 generation, the exact Go version, release targets, packaging, and any
  narrowly required dependencies remain separate implementation decisions.
- The existing plain-HTTP, permissive-CORS, opaque-body, agent-owned-directory,
  and best-effort decisions remain unchanged.

This ADR selects the language. It does not start implementation or introduce a
toolchain pin, module, dependency, executable, or release pipeline.

## References

- [Go HTTP server, body limits, and shutdown](https://pkg.go.dev/net/http).
- [Compiling a Go executable](https://go.dev/doc/tutorial/compile-install).
- [Go target operating systems and architectures](https://go.dev/doc/install/source#environment).
- [Go rename guarantees and limitations](https://pkg.go.dev/os#Rename).
- [Rust ownership](https://doc.rust-lang.org/book/ch04-00-understanding-ownership.html).
- [Rust asynchronous programming and runtime choices](https://doc.rust-lang.org/book/ch17-00-async-await.html).
