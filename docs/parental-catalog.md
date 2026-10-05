# Read-only parental catalog foundation

`internal/parental` implements the first Go component of the parental service
contract. It is deliberately not connected to `UsersService`, D-Bus, PAM, or
package installation. Existing user management behavior is unchanged. Bootstrap,
recovery, operation overlays and complete mutation coverage must be qualified
before enabling managed classification for existing accounts.

The implementation follows the catalog and eligibility model qualified in the
Desktop laboratory (`parental-service-classifier`, reference SHA256
`3e9f166e4af7d59c08c3a4881b818c8348c316878ad2e79e23f16ca3924a45a4`).
`testdata/classifier-vectors.json` carries three canonical digest vectors generated
by that unchanged decoder: ordinary, supervised and negative barrier. There is
no Python runtime dependency.

## Format and trust boundary

Schema 1 contains a global revision and up to 128 accounts, within 64 KiB. Every
account has association, generation, exact local identity (name/UID/GID/group),
revision, class, state, operation, barrier, policy and adapter digests. There are
no birth dates, age claims or guardian roles in this format. Revisions are
positive signed-64-bit integers, identities exclude root and the reserved
all-ones UID/GID. Identity/group resolution is a trusted service callback and is
not implemented through the GUI or inherited from a desktop-user fallback.

The decoder rejects unknown/missing fields, duplicate JSON keys at every level,
nulls or wrong field types, floats/exponents where integers are required,
unsupported versions, excessive nesting, invalid UTF-8, conflicting ownership
and incompatible state/operation/policy combinations. Duplicate association,
generation, UID or username invalidates the entire catalog, including tombstones;
UID reuse is not implemented by deduplicating records. Returned account lists
and nested adapters are copied, so consumers cannot modify a decoded catalog.

The canonical digest is compatible with the laboratory model. It detects local
snapshot changes; it cannot detect coordinated restoration of an old filesystem
snapshot and must never be described as anti-rollback authority.

## Filesystem reading

`NewReader(directory)` always requires root ownership. The caller must supply a
trusted directory ancestry; `O_NOFOLLOW` protects the directory itself and its
canonical entries, not arbitrary symlinks higher in an untrusted path.

The reader opens an existing private directory, `catalog.lock` and `catalog.json`
without creating anything. Files must be regular, owner-only 0600, root-owned,
and have exactly one link. It obtains a nonblocking shared flock on the existing
lock inode, checks that the directory entry still identifies that inode, and
holds it until the complete bounded snapshot is parsed. An exclusive lease
returns `Busy`; absence returns `RecoveryRequired`; unsafe metadata, oversized
storage and read failures return `UnsafeCatalog`. Invalid schema returns
`InvalidCatalog`. Error values contain only codes, never paths or account data.
The lease and descriptors are closed on both success and failure.

Cooperating writers must take an exclusive lease on that same stable inode,
write/fsync a new record, rename and fsync the directory before releasing it.
The reader checks inode/metadata stability and file size, but cannot certify
noncooperating privileged writers or guarantee adversarial root integrity.
There is no writer, initializer, migration or recovery implementation yet.

## Classification

An account without an explicit valid record returns `RecoveryRequired`; missing
or invalid catalog data never implies ordinary status. A barrier or non-active
state returns `Blocked` before identity lookup. Each primary identity component
must match the trusted observation. Ordinary accounts return `OrdinaryEligible`.

Supervised accounts need a nonempty exact required-adapter set and a trusted
observation matching generation, policy and every adapter digest; observations
are compared in canonical adapter order. Unsupported, missing, duplicate or
stale coverage returns `UnsupportedProducer`. Observers receive copies.
`SupervisedEligible` is eligibility only: it does not establish producer coverage,
authorize Unix mutations or enforce application limits. Durable operation
barriers still need to overlay this catalog at each actual admission boundary.

## Validation and continuation

The tests cover malformed/ambiguous JSON, conflicting identities, canonical
vectors, copied snapshots, stale policy/identity/coverage, absent canonical files,
wrong ownership/perms, symlinks/hardlinks/FIFOs/directories, flock contention and
lease release without writes. Normal repository tests, vet and the new package's
race checks passed locally. VM-only and optional integration gates remain
separate; this change does not claim their completion.

The [server-owned planner](parental-planner.md) now binds plans to live caller,
capability, identity, revision and catalog digest, with expiry and fresh
revalidation. The [execution guard](parental-guard.md) now seals parameters and consumes plans
before effects through a trusted backend interface. Next: concrete filesystem
leases and Unix adapter; qualify writer/bootstrap/recovery before activation.
Gates #2/#5 and #6/#102 remain open. No RPM publication
or local product installation belongs to this read-only step. The maintainer has
already authorized local installation after parental controls are complete and
will create a new child/adolescent account for testing.
