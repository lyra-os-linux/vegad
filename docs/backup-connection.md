# Backups when a destination is connected

An `on-connect` schedule starts a small watcher through its systemd `.path`
unit. The watcher waits for a mounted destination and stays alive while the
trigger exists. A device appearing under `/dev/disk/by-uuid` alone is not
enough to start restic. New repositories are initialized by the mounted job,
so creating a schedule does not write backup data beneath an absent volume.

The watcher identifies each connection by boot ID, unique mount ID, source and
mountpoint. A private, atomic record claims the attempt before restic runs;
a file lock excludes concurrent scheduled attempts. The same connection is
not retried automatically after failure, process interruption or service
restart. A remount or reboot allows another attempt. `RunBackupNow` remains
available for an explicit retry. A shutdown inhibitor is held during backup
work, not while the watcher waits for another connection.

Automatic connection detection requires Linux 6.8 or later, including the
Linux 6.12 baseline of Leap 16. The unique mount ID from `statx` is never
reused during a boot; the older ID in `/proc/self/mountinfo` can be reused
immediately after unmounting. Manual backup remains available on older kernels.

The repository directory is opened relative to a verified mount descriptor.
Restic accesses that descriptor through `/proc`, so an unmount cannot redirect
the job into the directory underneath the volume. Symlinks in the relative
repository path are rejected. An unavailable mount defers the scheduled job;
manual requests report that the destination is unavailable.

With a UUID, the configured repository path is relative to that volume. Without
a UUID, configure the schedule while the intended volume is mounted. Vega
remembers the source and mountpoint and refuses to use a fallback filesystem.
The root filesystem cannot be used as a non-UUID on-connect destination.

For a preexisting non-UUID schedule, mount its intended volume and run as root:

```sh
/usr/lib/vega/vegad backup prepare-target CONFIG_ID
```

This records the mount identity, regenerates the schedule units and reloads
systemd without changing the repository or credential. Until its identity is
prepared, the old non-UUID schedule reports an error and waits. The same
command can regenerate the units of an existing UUID schedule. Previously
failed path units may need `systemctl reset-failed vega-backup-CONFIG_ID.path`
before activation.

Deleting a configuration stops both its schedule and its watcher before
removing units, connection state and credentials. Stop failures are returned
to the caller and preserve those artifacts for another attempt.

The watcher remains alive because systemd reevaluates `PathExists` immediately
when its service exits, including after failure. See the
[systemd.path documentation](https://github.com/systemd/systemd/blob/main/man/systemd.path.xml).

Local regressions cover concurrent claims, failures, restart/remount identity,
delayed mounting, symlink rejection, a real restic repository, explicit retry,
and schedule removal failures. `TestBackupConnectionSystemdVM` additionally
requires a disposable VM with systemd as PID 1, the current daemon installed,
restic and util-linux, root, `/run/vega-backup-test-vm`, and
`VEGA_BACKUP_VM_TEST=1`. It creates temporary mounts and real system units;
ordinary `go test` skips it.
