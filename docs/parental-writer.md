# Negative-only catalog writer

`CatalogWriter.BlockOrdinary` implements one bounded part of recovery preparation:
turn an explicitly known active ordinary association into `recovering`, with
barrier and operation ID. It never initializes or reconstructs missing authority,
enrolls an account, changes primary identity/policy/generation, removes tombstones,
or reopens admission. This library is not activated by normal vegad startup.

The caller must have completed qualified authorization and fresh lifecycle checks.
This writer itself is not a permission boundary. It acquires the exclusive
pre-existing catalog.lock, so it must not be called inside the administrative
Guard's shared catalog lease. A lifecycle coordinator still needs an independent
negative overlay, producer inhibition/revocation and operation/proof integration.
The writer does not claim that a catalog change alone revokes existing activity.

The complete expected catalog revision and digest must match the actual locked
snapshot. Missing/invalid/unsafe canonical storage is refused without creating
locks or records. Only an active ordinary record may change. A supervised or
already transitioning/tombstoned association cannot be converted to ordinary or
replaced. Existing operation IDs, including those on peer tombstones, remain
reserved. The global revision increments with overflow refusal, and only the
selected account's revision/state/barrier/operation change. All other records and
policy fields are preserved. Local revisions/digests are not anti-rollback proof.

Publication uses a random, exclusive root-only 0600 temporary file in the trusted
private directory, write/fsync/close, canonical-content and lock/inode checks,
rename and directory fsync. The lock inode remains unchanged. Ancestors and
cooperating privileged writers are trusted; this does not defend against an
adversarial root or prove external NSS writers safe.

Before rename, failures leave the canonical record unchanged (except a separately
injected privileged writer's own changes) and clean this writer's temporary
entry when its inode is still identifiable. After rename, any failure returns
`PublicationUncertain`: the new blocked record may already be visible. It never
rolls back to an old active snapshot. The caller must freshly read and reconcile
that state; no success or durable transaction completion is inferred from the
failed response. Replay of the pre-commit snapshot conflicts. Supplying the
current exact blocked snapshot for the same operation is a no-op only after a
new directory fsync confirms durability; it cannot clear the barrier or open
admission. That is acknowledgement of an existing negative state, not recovery
of a supervised lifecycle operation.

Tests use real temporary files/flocks and an unexported owner override. They
cover selected/ordinary/supervised/tombstoned peers, contention, missing/unsafe
storage, stale revision/digest, operation reuse, overflow, fsync failures,
canonical/lock replacement, and actual subprocess exits after file fsync, rename
and directory fsync. A crash before rename can leave a random temporary file;
readers and writers ignore it instead of adopting or automatically deleting it.
These are process-interruption tests, not reboot or physical power-loss tests.
The default root factory rejects an unprivileged fixture. A separate
[root-owned VM qualification](evidence/parental-writer-vm/README.md) now passes
actual process contention, reader Busy, three process cuts and peer preservation. Normal/race/vet/repository
and Go1.22 checks passed. Earlier Unix/Polkit VM receipts remain historical gates,
not qualification of this new writer.

Next: define trusted first-install/bootstrap and explicit recovery inputs. Directory absence, UID identity or an old local backup alone must never
create positive ordinary authority. Authoritative proof/generation, independent
rollback reference, producer coverage and lifecycle integration remain required
before any product activation or host installation.
