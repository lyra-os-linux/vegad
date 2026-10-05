# Go administrative flow with real Unix effects

The compiled Go test passed seven checks using the guard, root-owned catalog,
registered inventory, operation leases and Unix adapter in a disposable guest.
Three commands succeeded (profile/groups, wheel grant and wheel revoke); two
repeated SetAdmin operations dispatched no command. Denial and a new barrier
during authorization prevented commands. Primary identity and the fictitious
neighbor were preserved. Authorization observed all locks free; command execution
observed all three locks held and the operation lock exclusive.

```sh
python3 docs/evidence/parental-unix-vm/verify-evidence.py
```

The controller verified the disposable overlay/backing chain and guest disk
serial before root actions. Only ordinaryuser UID1002 was mutated, with no
initial process belonging to it. Account/PAM hashes, shadow and non-target
passwd lines were checked. Account files and backups were saved only inside the
guest under root 0700/0600, restored and fsynced; hashes returned to their original
values. This whole-file restoration is an exclusive laboratory procedure, not
product recovery under concurrent writers. No password/shadow contents are
exported. QEMU exited normally, VM OFF, raw backing hash/inode/size/mtime unchanged.

The first attempt, in `failed-identity-setup/`, aborted before commands because
its generic fixture group differed from the actual target's primary group.
Restoration passed; its overlay was stopped on failure. The retry used a fresh
overlay and fixture records bound to the actual local tuple. Failed receipts are
not counted as successful qualification.

Proof is strict synthetic JSON identity, not the authoritative PAM/lifecycle
format. Authorization is a trusted callback, not real system D-Bus/Polkit or a
persisted guardian role. Registered producer data is not a coverage certificate.
SELinux was Permissive before/after; no GNOME login, MAC enforcement, rollback,
I/O-error or crash-during-Unix-effect scenario is claimed. Production service and
host installation remain unchanged.

Drivers are archived in host-run.py/host.py/vm.py and guest.py. The exact Go test
binary hash and source hashes are in manifest.json; the binary itself remains
in the local analysis directory. Next: Go system-bus and real Polkit composition.

The archived vm.py has trailing whitespace removed; execution semantics are unchanged.
