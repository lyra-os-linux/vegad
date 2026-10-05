# Fixed Unix adapter for ordinary account administration

`UnixAdapter.Apply` is an effect callback for `Guard.Execute`, not an independent
authorization boundary. It supports ordinary `UpdateUser` and `SetAdmin` only,
rejects root/invalid identities and validates sealed parameters again. Storage,
capability, interactive permission and plan consumption remain the guard's work.
The observer and group resolver are trusted service callbacks; no production
UsersService or NSS fallback is wired by this change.

Commands use absolute executable paths and fixed argument arrays with no shell:

- Profile/groups: `/usr/sbin/usermod -c NAME -G GROUPS -- USER`.
- Grant administrator: `/usr/sbin/usermod -aG wheel -- USER`.
- Revoke administrator: `/usr/bin/gpasswd -d USER wheel`.

The server catalog supplies the username. Names and group tuples are validated,
groups/GIDs are resolved again and primary identity is checked before and after
the command. Postconditions verify the exact requested profile/groups; SetAdmin
preserves other supplemental memberships. Already applied SetAdmin is a no-op,
so a repeated removal never invokes gpasswd for an absent membership. A primary
wheel membership cannot be removed with these supplemental-group commands and
returns `UnsupportedOperation` before execution.

Each command has a ten-second context deadline. No stdin or captured private
stdout/stderr is returned; command failure produces a safe code. There is no
automatic retry or compensation after a possibly partial command. A postcondition
failure still leaves the guard's plan consumed. External NSS/group writers,
group aliases/recreation and every primary/supplemental configuration require
qualification before promising general product coverage.

## Real Go flow in a disposable VM

The [VM evidence](evidence/parental-unix-vm/README.md) qualifies the compiled Go
guard, default root-owned filesystem leases and real commands together. A
synthetic proof codec and trusted authorization callback remain laboratory-only;
this is not real Polkit or PAM/supervised login qualification.

Targeted/race tests, repository regression, vet and Go 1.22 tests passed. The
exact VM test executable was built with Go 1.22.12 and source hashes accompany
the evidence. The first setup failed safely before commands because a generic
fixture identity differed from the guest's real primary group. The corrected
fixture binds its records to the observed identity without changing that identity.
The failed receipt is retained separately.

Next: real system D-Bus/Polkit composition in Go. Bootstrap/writer/recovery,
authoritative proof/generation format, guardian roles, lifecycle, enforcement,
rollback and existing ordinary-account preservation still gate activation and
local installation. No RPM or host install occurred here.

The [Go D-Bus/Polkit composition](parental-bus.md) now qualifies the experimental
transport with actual authorization in a separate disposable VM. Bootstrap,
proof/lifecycle and other product gates remain open.
