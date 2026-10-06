# Filesystem source for a current maintenance reference

FileReferenceSource reads a pre-provisioned reference authority through actual
root-only filesystem leases. It composes with the read-only RecoveryInspector;
it does not provision an authority, issue statements, write a current pin,
initialize a catalog, repair storage or reopen an account. Normal startup still
does not install or export parental control.

## Storage and issuer boundary

The factory takes trusted absolute authority/catalog directories, a configured
opaque issuer token and a mandatory domain qualifier. Public construction always
requires owner 0; only tests have a private owner override. The authority uses:

- reference.lock: existing, stable inode, shared nonblocking flock;
- current.json: strict schema 1, installation, issuer, sequence, reference_digest;
- reference.json: the [strict recovery statement](parental-reference.md).

All entries must be regular, single-link, private0600 and root-owned in a private
root-owned directory. Reads use the existing NOFOLLOW/nonblocking/bounded and
metadata/inode checks. The current record is bounded to4KiB. Installation and
issuer must match service configuration; statement sequence and complete digest
must match the current pin. A reference rolled back alone is refused. Missing,
unsafe, corrupted, ambiguous or contended storage is refused without creating or
adopting any record/lock. Errors never disclose paths or private file contents.

Within the explicitly trusted-root maintenance model, root-owned private storage
is the privilege boundary for issuance. Issuer token and digest are bindings,
not cryptographic signatures. Issuer enrollment, capabilities/Polkit, preservation
of ALL ownership/tombstones and qualified publication are still separate work.
An arbitrary root writer is not made trustworthy by a token matching this file.

Both records are read under one shared authority lease; current.json is read
again and compared exactly. A cooperating issuer must use the same lock inode
exclusively. Future multi-record issuance needs durable journal/reconciliation;
a partial update today yields a mismatch and refusal, not reconstruction. There
is no issuance writer or automatic rollback of these records in this change.

## Rollback scope and qualification

The qualifier is a trusted bounded/concurrent-safe service adapter that must
prove the configured authority is outside the catalog's SUPPORTED rollback domain
and return a SHA256 fingerprint of that qualified boundary. It is called before
opening storage and again under the lease; a changed boundary refuses the
snapshot. Nil, error or malformed fingerprint always refuses. No production
qualifier is implemented or configured here; tests use an explicit fixture.
Different paths or mount IDs and a root-owned file alone do not prove the boundary.
Restoring BOTH the reference and current pin is undetectable to this reader if
the qualifier's contract is violated or the entire authority domain is restored.
There is no whole-disk/whole-VM anti-rollback claim.

The host observation on06/10/2026 puts /var/lib in the same mounted Btrfs root
subvolume /@ as /. Only selected /var children are separate in this installer
layout. Therefore an authority simply placed in /var/lib would not satisfy the
proposed separation. This observation is read-only; no subvolume, mount or fstab
was changed. The [openSUSE Snapper documentation](https://doc.opensuse.org/documentation/leap/reference/html/book-reference/cha-snapper.html)
and [Snapper FAQ](https://snapper.io/faq.html) explain subvolume exclusion; path
names and comparison filters alone are insufficient. Their documented layouts
are not evidence that this particular Lyra installation matches them.

## Qualification and continuation

Tests use real files and flocks: pinned snapshot, immutable/read-only inode and
content behavior, held/released leases, malformed pin schema, missing/unsafe
storage, issuer/installation mismatch, stale statement, changed domain witness,
contention and default-root rejection of an unprivileged fixture. Integration
with RecoveryInspector is read-only and requires the witness. Normal/race/vet/
repository and Go1.22 checks passed. Positive root VM qualification and actual
storage-boundary qualification of this new source remain pending. Earlier VM
receipts do not supply them.

Next: implement a bounded Btrfs/Snapper domain observer for a clearly declared
rollback scope and qualify snapshot restoration in a disposable VM. Refuse an
unsupported/same-domain layout. Then qualify authority provisioning and issuance
journal/current-reference publication before connecting authorized bootstrap or
blocked recovery. No client scan or NSS list may invent ordinary declarations.

A qualificação conservadora de um único subvolume Btrfs está descrita em
[parental-btrfs-domain.md](parental-btrfs-domain.md). Ela não qualifica automaticamente
o Snapper/configuração de produção nem permite restaurar a autoridade junto ao catálogo.
