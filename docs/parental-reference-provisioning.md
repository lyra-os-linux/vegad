# First parental reference enrollment (Linux)

`ReferenceProvisioner` creates the first reference in an explicitly prepared
authority directory. It is a library implementation, not an installer, service,
guardian authorization flow or permission to admit a supervised account.

The trusted installer must prepare a private root-owned directory and its
existing `reference.lock`, qualify its storage independently of the catalog
snapshot domain, supply a complete issuer reference, and attest that this
installation has never finished enrollment. An empty directory, root, wheel
or NSS entries do not establish freshness.

The Desktop installer emits a one-use permission at
`/boot/efi/EFI/lyra/parental-enrollment/<machine-id>.json` after all install
operations and mount cleanup succeed. It binds `machine-id`, a random install
operation ID and state `available`. The ESP is a separately mounted VFAT
filesystem, outside the root Btrfs Snapper snapshot domain.
`ESPInstallerPermission` verifies the ESP filesystem and device are separate
from the Btrfs authority and catalog, then accepts only a safe root-owned record
with matching IDs. `NewESPBackedReferenceProvisioner` connects its check and
consumer to first-reference issuance and returns the permission reader so a
trusted issuer adapter can obtain the operation token for the current
`/etc/machine-id`. Before publishing the local enrollment
commit marker, the consumer atomically changes the ESP record to `consumed` and
binds the complete initial-reference digest.

This is a reusable library integration point, not yet wired to vegad's running
daemon or account-admission flow. A signed HTTPS client and a first-enrollment
coordinator now exist as libraries, but no production issuer configuration or
Polkit maintenance adapter is supplied. Until the caller and installer/vegad
package pair are integrated and qualified together, the running product does
not issue or consume this permit.

This is local root-managed lifecycle evidence, not a signed installer
certificate. It survives ordinary Btrfs/Snapper rollback; a whole-device restore
that also restores the ESP, or a privileged actor rewriting the ESP, remains
outside the guarantee. It does not bootstrap a catalog, choose account owners,
or replace maintenance authorization. Issuer completeness and the fresh
maintenance/Polkit callback remain separate trusted service inputs.

`NewReferenceProvisioner(directory, catalogDirectory, issuer, qualify,
proveFresh)` uses the same root ownership and bounded topology witness contract
as [the reference writer](parental-reference-writer.md). `proveFresh` is a
trusted, bounded, read-only installer check bound to installation ID, operation
ID and the entire initial reference digest. It must remain valid during an
interrupted enrollment and permanently reject completed installations, including
when all local enrollment evidence has been lost. That external lifecycle
contract is essential; retaining local files alone cannot detect restoration of
the complete filesystem image including the ESP.

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
operation and initial digest plus fresh maintenance authorization. It accepts
either an available installer permission or an ESP permission already consumed
for that exact digest, allowing recovery from a process cut after durable
consumption. It can complete only the reachable prefixes: seal only,
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

## Fixed service paths and remaining activation boundary

`NewESPBackedReferenceProvisioner` now fixes its service paths in code:
`/var/lib/lyra/parental` is the Snapper/Btrfs catalog, inventory and ledger
scope, while `/var/lib/lyra/parental-authority` is the separate authority
subvolume. The caller cannot redirect installer evidence or authority storage.
The ESP permission directory is also fixed to
`/boot/efi/EFI/lyra/parental-enrollment`.

The internal vegad bootstrap factory now expects root-owned issuer trust
configuration at `/etc/vega/parental-issuer.conf` with schema, HTTPS endpoint,
issuer ID and pinned primary fingerprint, plus the dedicated public keyring at
`/usr/share/vega/keys/lyra-parental-issuer.gpg`. The parser rejects unknown or
duplicate fields and endpoints outside the fixed first-reference route; keyring
preflight requires one safe primary key matching the configured fingerprint.
Neither config nor keyring is shipped yet because the real endpoint, key custody
and fingerprint have not been established. Missing trust config keeps the
bootstrap unavailable; the repository package signing key is not reused.

The installer change now creates two Btrfs subvolumes: the Snapper-managed
`/var/lib/lyra/parental` scope and sibling `/var/lib/lyra/parental-authority`.
It prepares private `catalog`, `inventory` and `ledger` directories in the
scope, plus stable catalog/reference locks. The vegad readiness gate remains
closed until the resulting mount, Snapper config and rollback behavior pass VM
qualification. Merely creating these folders under `/var/lib` on existing
installations would not satisfy the independent-domain check.

The vegad daemon and D-Bus server still do not import or invoke this package.
Before adding a public D-Bus method, the service needs a trusted issuer protocol
and pinned key/configuration for a complete first reference, a root-only bootstrap
that does not accept catalog/reference bytes from clients, a post-dialog fresh
Polkit maintenance revalidation, and a resumable enrollment operation. The ESP
operation token must be bound to the issuer's exact initial reference digest.
Without these inputs, activation fails closed and no enrollment API is exported.

## Ordered completion plan

1. **Qualify the persistent storage topology.** The installer plan now mounts a
   dedicated Snapper scope outside `/@` plus a separate authority subvolume on
   the same Btrfs filesystem; catalog/inventory/ledger live as private
   directories inside the scope. Stable catalog/reference locks are prepared.
   Two preliminary Leap 16.1 guests confirmed Snapper config creation,
   automatic `SNAPPER_CONFIGS` registration, and persistence of sibling mounts
   and nested `.snapshots` after reboot. In a manually prepared Btrfs-root
   guest, explicit classic rollback moved `/` to a writable snapshot; after
   reboot, a post-snapshot root marker was gone while the parental scope and
   authority hashes and mounts were unchanged. One probe used a separate Btrfs
   data disk and ext4 guest root; the root-rollback probe used a handmade Btrfs
   image populated from a disposable ext4 guest. Neither ran the Rust installer
   sequence.

   Snapper 0.12.1 explicitly refuses `snapper rollback` for a non-root config
   (`/var/lib/lyra/parental`). Do not use that command as the parental-catalog
   recovery model. Qualify catalog recovery as restoration of snapshot data
   followed by vegad's pin/witness mismatch detection. The `/@` rollback
   mechanism has passed on the handmade Btrfs image, but must still pass on
   actual installer output. Also qualify install, upgrade and reinstall before
   setting `installedParentalLayoutReady()` true.
   Until those checks pass, the constructor returns no provisioner.
2. **Define the signed issuer contract.** Use a detached OpenPGP signature over
   the exact bounded UTF-8 JSON bytes and verify with `gpgv` against a dedicated
   parental-issuer keyring whose complete fingerprint is pinned by vegad. Do not
   reuse the RPM/repository signing key. The signed closed-schema envelope must
   carry issuer ID/key fingerprint, installation machine-id, ESP operation
   token, schema, and the complete validated `RecoveryReference` (sequence 1,
   catalog revision 1, full ordinary active identities); the computed digest
   must match the reference as parsed. Pin the public key in the package or
   through an independently authenticated update channel. Reject unsigned,
   bad-signature, wrong-fingerprint, stale-schema, incomplete, oversized,
   mismatched or trailing-data responses. Do not accept the artifact, owner map
   or key from D-Bus clients.

   The issuer endpoint must be idempotent for `(machine-id, operation-token)`:
   repeated requests return the same signed bytes and reference, and the tuple
   cannot be issued for a second reference. Do not make an already issued
   response expire while its local enrollment is pending; the ESP one-use token
   and permanent consumed digest govern completion. A local cache may preserve
   the signed bytes for retry, but its presence alone is not freshness proof.
   Issuer storage and its operation registry must survive local Snapper restore
   and must have an independently managed backup/rollback policy. Package key,
   server key custody, protocol transport/pinning, registry implementation and
   operational recovery remain undecided and block implementation.

   A library verifier now exists in `internal/parental/issuer_linux.go`. It
   verifies the detached signature over the exact envelope bytes with `gpgv`,
   pins the primary-key fingerprint supplied by trusted service configuration,
   rejects unsafe keyring files, and strictly validates the closed envelope and
   first-install reference against the requested installation/operation tuple.
   `internal/parental/issuer_client_linux.go` adds a bounded HTTPS client for the
   provisional `POST /v1/first-reference` contract: request schema 1 carries
   only installation and operation IDs; response schema 1 carries base64 of the
   exact signed envelope and detached signature. Redirects, non-200 responses,
   non-JSON media types, malformed/oversized responses and invalid signatures
   are refused. HTTPS authenticates transport; the detached signature remains
   the authority for the returned reference. The wire contract is not deployed:
   there is no endpoint configuration, parental keyring/fingerprint package,
   issuer operation registry, idempotency guarantee, retry cache or D-Bus
   caller. The client and verifier do not authorize enrollment. These remain
   explicit integration blockers.

   A retry cache is intentionally not added yet. Its caller must persist the
   exact signed bytes before consuming the ESP permit, verify the signature
   again on every read, and bind the cache entry to installation, operation,
   issuer and initial-reference digest. Cache loss must be recoverable by
   repeating the idempotent issuer request; cache contents never prove freshness
   or authorize enrollment. The package has not defined a cache path or its
   cleanup/recovery lifecycle, and vegad has no caller to enforce that ordering.
   Keep the cache out of the authority directory, whose closed layout permits
   only its lock and enrollment state. Until these caller and package contracts
   exist, do not persist issuer responses locally.
3. **Prepare first-boot authority transactionally.** A library coordinator now
   performs interactive authorization before reading the ESP permit or
   contacting the issuer, requests the operation-bound signed reference, and
   chooses `Initialize` or `ResumeInitial` according to the permit's available/
   consumed state. The provisioner still owns the durable publication order,
   exact-digest resume and repeated maintenance revalidation. The coordinator is
   not exported by D-Bus and has no production dependencies configured. Next,
   add the package-owned construction and recovery status, then qualify cuts,
   denied callers and ambiguous fsync on the approved installed topology; never
   infer freshness from empty directories or NSS.
4. **Add maintenance authorization.** The dedicated
   `org.lyraos.vega.parental.enroll` Polkit action now restricts access to an
   active session and administrator authentication, with English, Portuguese
   and Spanish prompt strings. `internal/dbusserver/parental_authorization.go`
   binds the initial interactive check and later noninteractive checks to the
   same D-Bus sender. Unit tests cover cancellation/denial, silent recheck and
   policy defaults; the disposable real-Polkit test now also checks root access
   and ordinary-user denial. The adapter is not wired to an exported method.
   Integration must still confirm active session/caller after the prompt and
   repeatedly under the reference lock, including topology and capability.
   Match the experimental bus adapter's API and error contracts before use.
5. **Integrate and qualify the vegad caller.** Add startup/service composition
   and a minimal diagnostic/bootstrap API with no caller-supplied authority
   data. Verify denied callers cause zero storage writes, correct receipt is
   consumed once, concurrent calls serialize, and process cuts resume only the
   exact operation. Qualify on disposable Leap 16.1 with actual system bus,
   Polkit, ESP VFAT, Btrfs subvolumes and VM power-cycle/rollback cases.
6. **Package and install for the requested child-account trial.** Only after
   the signed issuer, enrollment, account lifecycle, PAM proof, policy and
   enforcement gates in `parental-controls-completion-plan.md` pass, publish
   packages for Leap 16.1 and install on the local machine. Preserve the
   existing administrative account and create a separate child/adolescent test
   account as already authorized by the maintainer.

Steps 1–5 above close the current first-enrollment integration gap; they do not
finish parental controls. Guardian roles, supervised account lifecycle,
authoritative PAM generation proof, GNOME/Flatpak enforcement, privacy review,
recovery/withdrawal, MAC policy and release qualification remain separate gates.

## Retomada de 06/10/2026

O construtor privado de readiness agora valida configuração, keyring/pin e
composição de dependências e retorna apenas `ready`/`unavailable`; não chama a
rede nem é exposto no D-Bus. Testes focados, suíte Go completa, race detector
parental/D-Bus, vet e diff-check passaram com toolchain temporária em
`/tmp/lyra-parental-go-toolchain/go/bin`.

O instalador local `worktrees/desktop-parental-install-enrollment` prepara o
schema 4 com scope `@lyra-parental`, authority irmã e permissão one-shot na ESP.
A operação agora sincroniza diretórios e locks antes do finalizador ESP. Suíte
Cargo passou (117 core, 9 UI e 1 integração; 2 ignorados), formatação do pacote
core passou, e Clippy passou exceto que foi necessário permitir dois avisos
`collapsible_if` preexistentes. A prova continua unitária: o root Btrfs do
instalador ainda não foi executado em VM e o gate de layout no vegad continua
fechado.

Um build de ISO local está pendente de autenticação pkexec em um diretório
novo; não representa pacote ou ISO de release. Ainda faltam issuer oficial,
configuração/keyring, integração e exposição D-Bus, qualificação completa em
VM/installer e as demais gates de enforcement/identidade/privacy. Não instalar
na máquina ou publicar enquanto estes gates estiverem abertos.
