# Privilege boundaries (vegad #38)

Public reads execute as the real D-Bus caller's UID, obtained from the bus
credentials. The root coordinator retains ownership of the public name and
checks Polkit against the original sender for administrative actions. Hidden
`dbus.Sender` parameters preserve the existing wire signatures. The explicit
inventory is `internal/dbusserver/query_policy.go`; export fails for an
unclassified method, and its coverage is checked against every exported method.

| Operation class | Methods | Execution and authorization |
| --- | ---: | --- |
| Public queries | 47 | Caller UID; the dedicated `vegad-query` account for a root caller; no capabilities or elevation |
| Journal queries | 2 | Polkit `logs.read-admin`, then a non-root worker with the journal group |
| Brokered reads | 10 | Noninteractive Polkit session check; active local desktop allowed without a password; root coordinator reads the required system files and protected firewall information |
| Administrative operations | 53 | Existing per-operation Polkit and transaction handlers |
| Metadata refresh | 1 | Existing rate-limited background maintenance; shared with the scheduled refresh |
| Retired driver operations | 3 | Existing explicit unsupported-operation responses |

An administrative operation can already demote a subprocess when appropriate,
for example an authorized operation on the caller's user Flatpak installation.
The table classifies the entry point, not every subprocess as requiring UID 0.

Opening Vega and refreshing its dashboard must never request administrative
authentication. The GTK application loads backup, snapshot and boot information
during startup, including for pages that are not visible. These ten brokered
reads use `pkcheck` without `--allow-user-interaction`; their four read policies
allow the active local session and deny inactive/remote sessions. A stricter
administrator override returns an access error instead of opening a dialog.
The retained `*.read-admin` action identifiers refer to system information, not
permission to modify it. Mutation actions retain their separate authentication.
Administrative journal access retains its existing explicit-page authorization.

The initial local #38 package incorrectly made these startup reads interactive.
Tests of individual public queries did not catch the GTK startup regression.
The corrected gate must also launch the installed GTK application, verify the
dashboard and background refresh, and record the absence of interactive read
authorization requests.

The full GTK test also exposed firewalld's `config.info` authorization for
`getServices` and `getPorts`: those reads stay in the broker instead of prompting
from an unprivileged worker. Firewall status still runs as the ordinary user.
Wi-Fi listing uses NetworkManager's cached results (`nmcli --rescan no`), so a
background query does not trigger the separate scan authorization.

## Query worker

Root callers use a static service account because DynamicUser can be rejected
by dbus-daemon's UID lookup. This occurred in the qualification VM and is a
[documented upstream compatibility issue](https://github.com/systemd/systemd/issues/22737).
Both service accounts are created by the packaged sysusers configuration and
have no login shell. A root caller's temporary cache stays in the unit's private
`/tmp`; no root home or environment is inherited.

The coordinator launches the installed `vegad query` through a transient systemd
service. The worker refuses root, accepts only the public/journal allowlist and
executes the complete query, including backend parsers. Input is limited to
64 KiB, output to 16 MiB, stderr to 32 KiB, with four concurrent launches, a
60-second runtime, 256 MiB memory and 32 tasks per service. Queueing has its own
64-second deadline; after acquiring a launch slot, the supervisor has 75 seconds
for service startup, execution and teardown. Launcher failure triggers a stop of
the complete randomly named cgroup before releasing the slot. The service runtime
limit remains the fallback if the system manager itself cannot process that stop. Failure is returned
to the client; there is no retry as root. No command, executable, environment or
UID is supplied by the D-Bus caller.

`NoNewPrivileges`, empty bounding/ambient capability sets, a read-only system,
private temporary files, and protection of kernel controls/modules/logs, clock,
hostname and cgroups apply to each worker. The original caller's normal process
visibility and device permissions remain available for the monitor and GPU
queries. A root caller's dedicated worker has a private device tree and hidden
homes. User Flatpak metadata can write only the existing `.cache` and
`.local/share/flatpak` directories of the resolved caller; ordinary DAC still
applies. These exceptions are not permission to change system packages.

The launcher forwards only a fixed PATH/locale, active Vega profile, caller
HOME/runtime directory, and the administrator-configured update-state path.
Root's full environment is never forwarded. Backend subprocesses inherit the
worker's UID and restrictions. A read of protected `/proc` or hardware fields
can be unavailable to the caller; that does not trigger privilege escalation.

Successful update-list responses are cached by UID and method for five minutes,
with a limit of 16 entries of at most 2 MiB each. Invalidation generations prevent
a query started before a transaction from reinserting an old result. The root
cache retains the serialized response; package parsing happens in the worker.

Native queries use local repository metadata with `--no-refresh`. They do not
persist the shared update state or change its refresh timestamp. Missing package
matches (Zypper 104) remain empty results; other errors, including permissions,
locks and invalid metadata, are returned. Repository refresh remains an explicit
maintenance operation because writing the shared cache requires elevation.

## Files, capabilities and syscall families

| Operations | Main resources | Necessary access / remaining exception |
| --- | --- | --- |
| System and metadata | `/etc/os-release`, installed branding, `statfs`/`df` | Ordinary reads, process execution; no capabilities |
| Package discovery, details, installed and pending lists, repositories | RPM database, `/etc/zypp`, shared metadata, caller Flatpak metadata | Ordinary reads; user cache writes; filesystem/process and network syscalls for Flatpak |
| Monitor and hardware | `/proc`, readable `/sys`, ordinary-user GPU devices, diagnostic CLIs | Ordinary reads and device queries (`ioctl`); no `CAP_SYS_PTRACE`, `CAP_SYS_ADMIN` or device permission changes |
| User/group listings | NSS, public passwd/group records | Ordinary reads; NSS may contact its configured service |
| Services, network, firewall, storage, Bluetooth and date/time status | System bus, sysfs, ordinary diagnostic CLIs | Unix sockets; netlink and IP sockets where backends need them; no capabilities |
| Journal queries and exporter | `/run/log/journal`, `/var/log/journal` | Journal-group read access after Polkit (interactive queries); exporter has a dedicated account |
| Backup/snapshot brokered reads | Root backup configuration/credentials, restic repository, Snapper/Btrfs metadata | Root retained for required file/backend access; active local session checked without prompting; repositories can be local or remote |
| Boot configuration reads | `/etc/default/grub`, GRUB state, EFI/loader entries | Root retained because these paths may be root-only; active local session checked without prompting |
| Package/kernel transactions and maintenance | System RPM/cache/configuration, package scripts, boot artifacts | Root retained with existing authorization; package scripts have broad system requirements |
| User, service, network, firewall, storage, boot and backup changes | Account files, system managers, mountpoints/devices, boot files, configured backup destinations | Existing authorization; UID changes, mounts/ioctls and backend-specific IPC where required |

This matrix identifies required syscall families, not an empirically complete
seccomp allowlist. The query worker restricts native architecture and address
families but does not claim to block all other syscall groups. The root
coordinator still has broad administrative privileges and decodes bounded IPC;
this is a reduction of query execution privileges, not complete isolation of
all administrative parsers. A global capability removal or filesystem sandbox
would break its package scripts, account management, storage and backup roles.

## Journal export

`vegad-log-export.service` runs as `vegad-log`, with journal-group access and no
capabilities. It writes only `/var/log/vega`, has no network, cannot access the
system bus or user runtime directories, and uses a system-service syscall filter.
`/run/systemd/journal` remains available for systemd to connect standard output;
the private manager socket retains its normal root-only Unix permissions.
The group can read the whole journal; filtering to `vegad.service` is performed
by `journalctl`, not an OS ACL per unit.

`LogsDirectory=vega` handles both directory creation and migration of existing
root-owned log files. The tmpfiles fragment deliberately does not pre-change the
directory owner, which would make systemd skip the ownership migration of its
contents. Logrotate uses the same dedicated account. Local administrator changes
to a `%config(noreplace)` logrotate file still need normal `.rpmnew` review.

## SELinux and qualification

The existing dedicated `vegad_t` domain remains permissive. It avoids giving
bootloader-write access to every `init_t` process, but does not provide enforcing
containment for Vega. The local host reports SELinux globally permissive
(`/sys/fs/selinux/enforce = 0`). The initial disposable VM uses `selinux=0`.
A second disposable VM loads the official Leap targeted policy and enables
enforcing mode before the test matrix. The existing per-domain permissive
exception for Vega remains in place: this tests compatibility with an enforcing
base, not enforcing containment of Vega itself. Do not treat permissive AVCs
as blocked operations or as evidence of a complete SELinux allowlist.

That gate identified three missing permissions in the transitional module:
communication between local D-Bus domains and Vega; relaying its bounded pipes
through dbus-daemon/PID 1; and the `init_t` to `vegad_t` transition under
`NoNewPrivileges`. The fixes do not grant other domains bootloader writes.
Public query UIDs and capabilities remain restricted by systemd; enabling
the SELinux transition does not restore Unix root privileges. The exporter
hides runtime directories instead of mounting over individual D-Bus sockets,
which the base SELinux policy rejected.

With systemd 257.13, offline exposure scores are 7.9 for the administrative
coordinator (unchanged), 4.1 for the desktop query profile, and 0.8 for the log
exporter (previously 4.3). These are heuristic unit scores; they do not measure
application authorization correctness or prove resistance to attacks.

Unit/race tests cover inventory and signatures, allowlist rejection, response
bounds, caller environment, cache isolation/invalidation and read-only state.
`scripts/check-query-vm.py` exercises the actual systemd execution boundary,
D-Bus/Polkit, caller/service UIDs, capabilities, prohibited reads/writes and log
migration. Its VM has no host disks, shared folders or network interface.
Root container CI runs the private wire-contract daemon as `nobody`; full root
routing belongs to the systemd VM gate. The private bus has no Polkit authority:
three public Rust contracts run there, and protected backup reads are checked
for rejection. The successful backup-read contract is exercised with real
Polkit in the VM, rather than bypassing authorization in the daemon.

The VM is a purpose-built ext4 fixture, not a full installed ISO. It has no
logind shutdown inhibitor and does not qualify firmware boot or Btrfs/Snapper
pre/post-transaction integration. Its copied Snapper commit plugin produced
coredumps during fixture package transactions; RPM transactions themselves
completed, and those logs are retained as an explicit fixture limitation.
The root transaction sandbox was not tightened by this change.

The local qualification on 2026-09-14 passed the Go suite/vet, selected race
tests, private D-Bus contracts and systemd execution probes. The final RPM gate
passed package install/update/removal, user administration, protected reads,
ordinary-user queries, log migration and launcher/cgroup cleanup with the base
SELinux policy enforcing. Real backup/boot and Flatpak authorization regressions
also passed in the disposable VM. Native installation and release/publication
are separate steps and must be recorded independently; these gates do not claim
that the complete Vega domain is enforcing or that a new ISO has been tested.

References: [Zypper return codes](https://manpages.opensuse.org/Tumbleweed/zypper/zypper.8.en.html),
[systemd execution properties](https://github.com/systemd/systemd/blob/v257/man/systemd.exec.xml).
