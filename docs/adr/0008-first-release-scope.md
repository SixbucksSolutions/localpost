# ADR 0008: First-release scope and publication gate

- **v0.2.1 update:** startup retention is superseded by [ADR 0011](0011-startup-mailbox-cleanup.md).
- **Status:** Accepted through Terry's completion of M06.01.
- **Date:** 2026-11-04
- **Authority:** M06.01 defines the release proposal. This record does not build,
  qualify, tag, publish, or upgrade anything.

## Release scope

Release **0.1.0**, tagged **v0.1.0**, through
[GitHub Releases](https://github.com/SixbucksSolutions/localpost/releases).
This is the initial 0.x release, not a 1.0 API-stability commitment. Describe any
later incompatible changes and migration steps explicitly. Published version
contents are immutable; corrections require a new version.

| Build target | Qualification environment | Archive |
| --- | --- | --- |
| `windows/amd64` | Windows 11 x64, local NTFS directory | `localpost_0.1.0_windows_amd64.zip` |
| `linux/amd64` | Ubuntu 26.04 LTS x64, ext4 | `localpost_0.1.0_linux_amd64.tar.gz` |

The current development host is Windows 11 Pro, build 26200, with NTFS storage.
Earlier CI ran on Ubuntu 24.04 LTS x64. Terry updated the release qualification target to Ubuntu 26.04 LTS on 2026-11-04; current CI targets Ubuntu 26.04. These are available qualification
environments, not evidence that release archives have already been tested.
M06.03 recorded ext4 for the Linux qualification and NTFS for Windows.
The detailed observations are in the release notes. Terry accepted the candidate
and v0.1.0 was published under this gate. Support claims are limited to environments
actually qualified.
Other Windows versions, Linux distributions, architectures, and filesystems are
unverified rather than implicitly supported by cross-compilation.

No installer, package-manager repository, code-signing service, automatic
updater, or additional architecture is included in this first release.

## Build and package contract

Use pinned Go **1.27.3**, `CGO_ENABLED=0`, and the baseline amd64 target
(`GOAMD64=v1`). Build from a clean, identified source commit. Strip checkout
paths with `-trimpath`; stamp the approved version and full source commit
explicitly instead of depending on ambient checkout metadata. Record effective
build settings, including VCS stamping policy, in the artifact manifest.

Add `localpost --version`: it must report release version, full source commit,
Go version, and OS/architecture, then exit without opening a listener. Ordinary
development builds must identify themselves as development/unknown rather than
pretending to be a release. Set the packaged OpenAPI `info.version` to `0.1.0`;
the OpenAPI specification version remains `3.2.1`.

Each archive has one versioned top-level directory containing:

- `localpost.exe` or `localpost`, with executable permissions on Linux;
- a concise offline `README.md` for unpacking, running, stopping and upgrading;
- the project's MIT `LICENSE` and applicable bundled-code license/notices,
  including the compiled Go runtime;
- the canonical `openapi.json`, copied without maintaining another contract;
- `build-info.json` recording version, full source commit, target, toolchain and
  effective build settings, plus the binary's SHA-256.

Publish `SHA256SUMS` alongside the two archives. Normalize archive ordering,
permissions and timestamps so repeat builds with the same pinned inputs produce
the same archive bytes; verify this in M06.02. The manifest must not include
wall-clock build timestamps, private paths or environment secrets.

Only the daemon and its operating documentation belong in runtime packages.
Go, Node/npm, validators, schema snapshots, the browser pilot, test inboxes and
development caches are not runtime dependencies or bundled tools.

## Qualification and publication

1. M06.02 produces one immutable candidate artifact set with hashes and source
   identity. No public release or tag is created by the packaging step.
2. M06.03 unpacks and runs those exact artifacts as an ordinary user on both
   target environments. Record OS/build, architecture, destination filesystem,
   artifact hash and result. Verify operation without development tools on the
   runtime path, API/port/lifecycle behavior, and complete-file/no-overwrite
   publication on the tested filesystems. Prepare a reviewable release draft
   with the exact assets, checksums, support matrix and migration notes.
3. Terry's acceptance of M06.03 approves that specific candidate for publication.
   M06.04 then publishes it under the authorized milestone work: create the
   annotated `v0.1.0` tag at the qualified source commit, publish the release and
   exact assets, download them again and verify hashes and version identity.
   A changed artifact requires qualification and acceptance again, not a silent
   replacement. Update public download links; do not rebuild during publication.

Keep the release draft unpublished until that gate is satisfied. The first
release is a normal published 0.x release, with its initial-development status
clear in the notes. No additional confirmation is needed for an action already
covered by accepted candidate and milestone authorization.

## Shared-service upgrade and rollback

Publishing a release does not replace or restart the shared development daemon.
For a separately authorized local upgrade, coordinate completion/unregistration
of active runs, stop the owned daemon with Ctrl+C/SIGINT, and wait for exit.
The documented network grace does not impose a hard filesystem-operation limit.
Unpack into a new versioned directory and start the selected executable on the
same intended port. Verify readiness before agents register again. Registrations
do not survive restart; delivered files do.

Retain the prior executable/archive and checksum for rollback. Rollback selects
that executable and requires fresh registrations; it does not recover registry
state or erase delivered files. Never overwrite a running executable or silently
interrupt unrelated users while qualifying a package.

## References

- [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html)
- [Go implementation decision](0006-go-implementation-language.md)
- [Current runtime and API contract](../../README.md)
- [Existing qualification workflow](../../.github/workflows/race.yml)
