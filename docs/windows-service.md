# Windows automatic service

## Install

Verify the published archive against `SHA256SUMS`, then unpack the whole Windows
amd64 archive into a new directory. Open an elevated PowerShell window and run
from that unpacked directory, replacing the example account with the Windows
account that consumes mailbox files.

```powershell
.\scripts\install-service.ps1 -AgentAccount 'COMPUTER\Terry'
```

The defaults install the `localpost` service under
`C:\Program Files\localpost\0.2.0`, provision
`C:\ProgramData\localpost\mailboxes`, and listen on `127.0.0.1:53147`.
`-ServiceName`, `-InstallRoot`, `-MailboxRoot`, and `-Port` support isolated
installations. Paths must be absolute local paths with no reparse-point ancestors.
The installation directory must be new or empty, or a matching interrupted
installation with its protected ownership record. Code directories and their
ancestors must have trusted SYSTEM, Administrators or TrustedInstaller ownership
and must prevent untrusted replacement. Missing ancestors are created with
protected administrator ownership. Existing service names and an
occupied port cause installation to stop. Existing mailbox contents are retained.
The installer replaces the mailbox root's explicit ACL with the private ACL
below. Select a dedicated root whose access is owned by this installation.

The service runs as the built-in `NT AUTHORITY\LocalService` account with no
password. Windows Service Control Manager starts it automatically at machine
boot, independently of an interactive login, Codex, or a scheduled task.
The installer requires administrator elevation and does not request elevation
itself. It verifies the executable SHA-256 against the release's
`build-info.json` before installation. Verify the archive checksum first because
that manifest and installer are part of the archive's trust boundary.

SYSTEM and Administrators receive full control. LocalService receives read and
execute access to code, Modify access to private rolling logs, and Modify access
to the mailbox root. The designated agent account receives Modify access to the
mailbox root. LocalService also needs read and traverse access to every
existing ancestor of a custom mailbox root so canonical path resolution can
complete. Provision those ancestor permissions before installation. New mailbox directories and message files inherit those permissions.
The service keeps up to four rotated 1 MiB child-process log files for each output
stream. WinSW also appends its own wrapper log. Inspect and archive that log
during stopped-service maintenance. Elevated SCM command output is kept in a
separate administrator-only `control-logs` directory.
Logs and ownership metadata stay under the installation directory.

## Lifecycle and clients

```powershell
.\scripts\install-service.ps1 -Action Stop
.\scripts\install-service.ps1 -Action Start
.\scripts\install-service.ps1 -Action Uninstall
```

Supply the same `-ServiceName`, `-InstallRoot`, and `-MailboxRoot` for a custom
installation. Ownership metadata, protected code permissions, executable and configuration
hashes, and the registered executable path must match
before lifecycle operations can proceed. Stop and uninstall retain all code,
metadata, logs, mailbox directories, and delivered files. After uninstall, an
administrator can archive or remove the old installation directory once its
contents are no longer needed. Install upgrades into a new empty directory.
Coordinate clients before stopping the old service and retain its archive for
rollback. Remove an older scheduled-task launch separately during the approved
host migration, before installing on the same port.

WinSW sends Ctrl+C to the daemon for graceful shutdown, with a 40-second stop
budget. Windows shutdown must allow that budget. WinSW can terminate a process
that exceeds it. Service failure triggers restart after 10 seconds. Registration
IDs are in memory and become invalid after any restart, while delivered data
remains on disk. Clients register again using `POST /api/2026-11-05/registrations`
with JSON `{}` and use the returned `id` and `directory`. Retirement with `DELETE`
retains files. Agents clean up only their own retired mailbox directories.

For failure diagnosis inspect `Get-Service localpost`, the Windows System event
log, and the bounded logs under the installation directory. If installation stops
partway before service creation, retry the same command with the same archive and
settings. A protected matching ownership record permits that retry. If the service
was already created, inspect the logs and use `-Action Start` or `-Action Uninstall`
with the same paths. A failure before the ownership record was written requires
a new empty installation path. The installer refuses unmatched existing data.

## Verification scope

`scripts/test-windows-service.ps1 -PackageDirectory <unpacked-directory>` requires
an elevated Windows session, Node, and the repository's `npm ci` dependencies.
It uses a unique service, separate temporary directories and an available port.
It checks SCM automatic-start configuration, LocalService identity, real
service-written delivery files, inherited agent Modify permissions, dated API
registration, stop/restart invalidation, retention, uninstall and the live OpenAPI
contract. It cleans up only the owned test service and temporary directories.

The `Windows service` workflow runs this test on `windows-2025` with a development
fixture identified by the workflow commit. That demonstrates service lifecycle
and boot configuration. It does not reboot the runner or qualify a published
release archive. Exact candidate qualification and an observed host reboot are
separate evidence.

## WinSW provenance and license

The installer downloads the official standalone x64 WinSW v2.12.0 asset once per
installation and verifies its fixed SHA-256 before execution. The asset includes
its runtime and needs no separate .NET installation.

- [Official release](https://github.com/winsw/winsw/releases/tag/v2.12.0)
- [Official executable](https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe)
- [Configuration reference at the pinned tag](https://github.com/winsw/winsw/blob/v2.12.0/doc/xmlConfigFile.md)
- [Original license](https://github.com/winsw/winsw/blob/v2.12.0/LICENSE.txt)

SHA-256 verified from the official asset on 2026-11-05

```text
05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da
```

This guide is copied beside the installed wrapper and retains its license.

```text
MIT License

Copyright (c) 2008-2020 Kohsuke Kawaguchi, Sun Microsystems, Inc., CloudBees, Inc., Oleg Nenashev and other contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
