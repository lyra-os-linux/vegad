# Filesystem leases for administrative dispatch

`FileLeases` supplies a concrete Linux backend for the [administrative guard](parental-guard.md).
It uses real descriptors, `openat`, metadata checks and nonblocking `flock`.
The service still does not activate parental management, install state, change
UsersService or execute Unix commands through this component.

## Read-only storage protocol

The factory always requires root ownership. Tests use an unexported owner field
to exercise real files without administrator privileges; there is no public
owner override. Path ancestry is trusted service configuration. The directory
and canonical entries themselves are opened with `O_NOFOLLOW`.

The backend obtains leases in this order:

1. Shared `catalog.lock`, followed by strict parsing of `catalog.json` and
   comparison to the server plan's complete catalog digest.
2. Shared `.inventory-UID.lock` in the inventory directory.
3. Shared `.gate-UID.lock`, or exclusive for the final effect, in the ledger.

Directories must be private and owned by the configured root identity. Locks
and records must be regular, single-link, owner-only 0600 and owned by root.
The lock entries are checked against the acquired inodes. Content reads are
bounded, reject metadata or entry changes during reading, and release their
separate record descriptors. Inventory/catalog records are limited to 64 KiB;
positive proof records are limited to 8192 bytes. Existing lock file contents
are not treated as records or authority.

There is no file creation, rename, adoption of temporary records, journal
reconstruction, enrollment, publication or recovery here. Missing canonical
records/locks return `RecoveryRequired`; contention returns `Busy`; unsafe
catalog state returns `UnsafeCatalog`, other unsafe storage `UnsafeState`.
Errors contain codes, not filesystem paths or private record contents.

The shared catalog Reader now reuses the same descriptor/lease helpers, retaining
its existing schema and ownership behavior. These helpers close descriptors on
all exits; leases close in reverse order. Visitor errors are preserved. A close
failure after an otherwise successful visit returns an error rather than success.

## Negative state precedes proof

Any canonical `.deny-UID`, `.withdrawal-UID.json` or `.operation-UID.json` entry
returns `Blocked` before inventory or positive-proof parsing. Corrupt data,
symlinks, FIFOs and empty negative entries remain negative; their presence is
not converted into absence. Only `ENOENT` establishes absence. Other stat errors
fail closed. Retired journals are not silently treated as active eligibility.

The absence checks are repeated after the proof codec and after the visitor.
Lock inodes and directory safety are rechecked before/after the visitor. The
leases remain held through the guard's final effect and post-effect identity
observation. This detects tested replacement/negative-publication mistakes; it
cannot make noncooperating privileged writers safe or undo an effect that has
already happened. Such failures leave the guard's plan consumed.

## Registered inventory and proof codec

The inventory parser requires schema 1, exact identity and positive revision,
plus exactly classification, cron, D-Bus activation, GDM, SSH, system units and
user-manager producer entries. Each must have `registered` state, a valid adapter
digest and 1–64 distinct bounded route identifiers/digests. Unknown fields,
duplicates, malformed numbers and unsupported/claimed-complete coverage fail.
This is conservative compared with the historical decoder, which could parse
unsupported producer entries for diagnostics. A registered file does not prove
actual namespace coverage or enforce supervision.

Inventory digests use the canonical JSON representation. Ledger seals use a
versioned, length-framed digest of the exact positive bytes and the absence of
all three negative entries. These local ephemeral seals detect conflicts; they
are not signatures, anti-rollback authority or compatibility promises with the
Python fixture's ledger fingerprint.

The proof decoder is an explicit trusted service callback. No JSON/PAM decoder
is supplied by the production factory; nil fails closed. Returned identity must
match the account in the plan. The callback receives a copy of the bounded bytes,
so it cannot alter the sealed original buffer. Tests use a strict synthetic JSON
identity proof only. The authoritative PAM/proof format, generation/lifecycle
binding and recovery still need qualification before product activation.

## Validation and continuation

Tests use actual temporary directories and kernel flocks: shared versus final
exclusive operation leases, contention at each step, release after errors and
panic, proof/lock replacement, unsafe metadata/links, missing canonical files,
negative precedence over corrupt positive data, unchanged files including a
neighboring UID, and full guard dispatch with leases released during authorization.
Root-only production ownership rejects the unprivileged fixture directories.
This is not a positive root-VM/PAM/login or actual Unix-effect qualification.

Targeted/race tests, repository regression, vet and Go 1.22 compatibility passed.
Next: fixed Unix effect adapter and guarded VM qualification with the Go flow,
then real D-Bus/Polkit integration. Writer/bootstrap/recovery and existing ordinary
account preservation remain prerequisites to activating UsersService. Gates
#2/#5/#6/#102, guardian roles, lifecycle, rollback, enforcement, GNOME, MAC and ISO
remain open. Local installation after completing parental controls is already
authorized for testing with the maintainer's new child/adolescent account.
