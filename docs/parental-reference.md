# Trusted reference inspection for bootstrap and blocked recovery

The next storage gate is implemented as **read-only inspection**, not a bootstrap
writer or automatic repair. `DecodeRecoveryReference` validates an immutable
issuer statement; `RecoveryInspector` compares that statement with local state
and trusted identity observations after an explicit maintenance-capability check.
It returns only a diagnostic disposition. No plan, Polkit permission, catalog
publication, account mutation or admission follows from a successful inspection.

## Source contract

A ReferenceSource must authenticate the installation, retain the complete known
ownership/tombstone set, supply its current statement and distinguish first
installation from repair across local rollback. It is a trusted service adapter;
there is no production implementation or issuer enrollment in this change.
A GUI JSON payload, UID/NSS enumeration, directory absence or a backup stored in
the same snapshot domain cannot implement that contract. A valid JSON digest or
signature alone does not establish freshness. Sequence is issuer metadata;
local code cannot independently prove its monotonicity or completeness.

Schema 1 contains installation token, sequence, mode and the existing strict
catalog. Size is bounded to CatalogLimit+1024; unknown/missing/duplicate/null
fields, invalid UTF-8, unsupported modes, malformed catalog and duplicate
operation ownership are refused. Accessors return copied catalog data.

- first-install requires sequence/catalog revision 1 and only explicit ordinary,
  active, unblocked revision-1 records with no pending operation. These are
  declarations from the qualified issuer; code does not infer the class from NSS.
- blocked-recovery requires sequence and catalog revision at least 2. Every
  non-tombstoned account must be recovering with a barrier. Existing tombstones
  remain negative. Policy, identities and generations are not reset to defaults.

## Inspection and refusal

The bus-derived caller UID and explicit maintenance capability are checked before
source/local/target lookup; root has no implicit role. The configured reference
is validated again against its digest. The inspector reads the local snapshot,
checks live primary tuples for non-tombstoned reference accounts, rereads the
reference and rechecks caller UID/capability before returning. Historical
tombstones do not require a live NSS identity and never release their IDs.

When a valid local catalog differs, blocked recovery requires a strictly newer
catalog revision and preservation of every known association, generation,
identity, class, policy, adapter set and pending operation. A local tombstone
must be retained exactly, and repair cannot tombstone a live association; missing ownership or changed policy is Conflict.
Already matching snapshots return ReferenceMatches, which is diagnostic only.

Missing local state can return BootstrapRequired for a trusted first-install
statement or BlockedRestoreRequired for a blocked recovery statement. Invalid
catalog content permits only the latter; unsafe metadata, lock contention and
other read failures produce StorageFailure. Neither disposition confirms that
a directory is empty or a lock may be recreated: RecoveryRequired can include
missing directory, lock or canonical record. The future publisher must inspect
storage under its leases and refuse unexplained negative/journal/ownership state.

No local lease is held across all callbacks, no execution token is stored, and
local changes can occur after the snapshot. Therefore this assessment cannot be
used as authorization or as a mutation's final precondition. A future qualified
publisher/coordinator must resolve the current reference, authorizations, storage,
identity and negative overlays again before effects and confirm durability.

## Validation and continuation

Tests qualify strict modes/schema, round-trip digest, immutable accessors,
capability-first lookup, missing-source refusal, reference/caller/capability
changes, identity mismatch, unsafe storage, missing canonical state staying
absent, and refusal to drop ownership/tombstones/policy/pending operations or
replace with an older revision. Reference and capability sources remain fixtures.
Normal/race/vet/repository and Go1.22 tests passed; no new VM or host installation
belongs to this read-only step. Previous root-writer and Unix/Polkit VM receipts
remain separate qualifications.

Next: choose and qualify the actual reference issuer/storage and freshness model,
then implement publication under leases with explicit authorization and durable
negative recovery. First-install issuance, root storage provisioning, journal/
anchor transaction, independent rollback reference, authoritative PAM generation,
producer coverage and full lifecycle remain prerequisites before activation.

A [filesystem reference source](parental-reference-files.md) now reads a root-only
reference and its current pin with actual leases. Issuance and production storage-
boundary qualification remain pending; no automatic bootstrap has been enabled.
