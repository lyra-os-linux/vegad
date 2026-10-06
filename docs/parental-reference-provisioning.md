# First parental reference enrollment (Linux)

`ReferenceProvisioner` creates the first reference in an explicitly prepared
authority directory. It is a library implementation, not an installer, service,
guardian authorization flow or permission to admit a supervised account.

The trusted installer must prepare a private root-owned directory and its
existing `reference.lock`, qualify its storage independently of the catalog
snapshot domain, supply a complete issuer reference, and attest that this
installation has never finished enrollment. An empty directory, root, wheel,
NSS entries or the absence of files do not establish freshness. No production
installer attestation adapter is included yet.

`NewReferenceProvisioner(directory, catalogDirectory, issuer, qualify,
proveFresh)` uses the same root ownership and bounded topology witness contract
as [the reference writer](parental-reference-writer.md). `proveFresh` is a
trusted, bounded, read-only installer check bound to installation ID, operation
ID and the entire initial reference digest. It must remain valid during an
interrupted enrollment and permanently reject completed installations, including
when all local enrollment evidence has been lost. That external lifecycle
contract is essential; retaining files alone cannot detect deletion of all
authority state or restoration of the entire filesystem.

`Initialize(initial, operation, revalidate)` only accepts validated
`first-install` references (sequence 1, catalog revision 1, explicit ordinary
active identities). Maintenance authorization must already be established;
`revalidate` is mandatory before storage access and repeatedly under EX. No
dialogs, mutating callbacks or acquisition of this authority's SH lease may
occur inside these callbacks. Issuer completeness, caller binding, permission
and attestation provenance are trusted caller obligations, not properties of
JSON or hashes.

The authority directory must contain only `reference.lock`. Initialization
never creates or repairs directories or locks and refuses legacy stores,
unknown entries and orphan temporary files. Under the stable exclusive lock:

1. Publish and synchronize permanent `enrollment.json`, containing the issuer,
   operation and complete initial reference.
2. Publish and synchronize `reference.json`, then its `current.json` pin.
3. Recheck the pair, seal, domain and authorization/freshness, then publish and
   synchronize permanent `enrollment-committed.json`. This binds the installation,
   issuer, operation, initial digest and SHA-256 of the exact seal bytes.

Files use private staged writes, file fsync, `RENAME_NOREPLACE` and directory
fsync. Canonical entries are never overwritten by enrollment. The lock inode
stays unchanged. Both enrollment records use closed schema 1 with strict UTF-8,
duplicate, type, size, unknown-field and trailing-content checks. They remain
after ordinary reference advancement and are never removed by this API.

Before the commit marker exists, the reader refuses the pending enrollment even
if the reference and pin agree. `ResumeInitial` requires the exact stored issuer,
operation and initial digest, fresh maintenance authorization and a still-valid
installer attestation. It can complete only the reachable prefixes: seal only,
seal/reference, or seal/reference/pin. Unknown content, a pin without a reference,
an update transaction, unsafe files or a different operation remain refused.

Any entry named `enrollment-committed.json` forbids `ResumeInitial`, including an
unsafe entry and even if reference/pin have subsequently disappeared. Missing
canonical files or locks are not repaired in a completed store. A loss of the
commit marker is ambiguous and must not regain freshness through the installer.
Neither the seal nor a hash is a global monotonic counter or cryptographic issuer
authentication. Hostile root, whole-authority rollback and deletion of all
evidence require independent lifecycle authority beyond this library.

After publication of the first seal, an error returns `PublicationUncertain`
and no reference result, with no automatic rollback. The caller must inspect
the state through authorized maintenance rather than infer that publication
failed. A commit rename whose directory fsync fails can be visible to the reader
while the writer still reports uncertainty; the commit marker continues to forbid
reenrollment. These are fsync protocol guarantees, not physical power-loss tests.

For legacy compatibility, existing reference stores without either enrollment
record remain readable and updatable. This compatibility is **never** evidence
that `Initialize` is allowed. New stores require both valid records, matching
issuer/installation and sequence at least the initial sequence; sequence 1 also
requires the exact initial digest.

Validation in this change: unit and fixture process-interruption tests cover
four real child exits without defers (seal, reference, pin and commit durability),
exact resume, eight injected fsync failures, missing/unsafe markers, refusal of
completed loss, unchanged lock and markers across advancement, denial before
topology access, revocation and incompatible prefixes. Process tests use private
owner overrides and fixture topology/installer checks. A separate disposable Leap 16.1 VM qualification now covers this provisioner
with default root ownership and the real Btrfs domain qualifier: four child
process exits, pending-reader refusal, exact authorized resume, refusal of a
revoked installer proof, advancement retaining the markers and lock, and loss
of reference/pin/both/lock without reseeding. Installer/issuer/maintenance checks
remain fixtures. No production attestation integration or physical power-loss,
reboot, Snapper rollback or SELinux enforcement is claimed. The guest was
unmounted and shut down; accounts/policy state and all three backing images
were unchanged. Exact sources, drivers and receipts are pinned in
[evidence/parental-reference-provisioning/README.md](evidence/parental-reference-provisioning/README.md).
Earlier VM receipts qualify the existing writer at their recorded commit,
not this new source. Local installation and OBS publication
remain gated on the complete parental product and its integration checks.
