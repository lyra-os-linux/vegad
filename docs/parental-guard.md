# Administrative execution guard

`internal/parental.Guard` adds the method/parameter envelope and execution
sequence to the [server-owned planner](parental-planner.md). This is internal Go
code without a D-Bus API, Polkit action, Unix effect adapter installed in the product. The [filesystem backend](parental-files.md)
now implements leases, including guard tests with real files and flocks. Effects
still record dispatch; no host user account is modified.

## Closed methods and parameters

Only ordinary-account `UpdateUser` and `SetAdmin` are supported. Create, removal,
password and photo have no representation in the typed input. A future transport
must still reject missing/wrong fields; Go's typed bool is not a wire decoder.

`UpdateUser` requires a nonempty UTF-8 name up to 128 code points without colon,
ASCII controls or DEL, and up to 64 distinct valid supplemental group names.
Adding `wheel` for administrative status counts toward that bound. A false
administrative flag together with an explicit `wheel` group is rejected.
`SetAdmin` accepts only the administrative flag and seals a `wheel` delta; it
never becomes a replacement of all supplemental groups.

Group names resolve to positive nonreserved GIDs before sealing. Revalidation
rejects missing groups or changed GIDs. Returned groups, preview parameters,
authorization requests and adapter parameters are copied. Changing the original
input or request cannot redirect the stored method, profile or group effect.
The digest uses deterministic Go JSON over the private parameter fields. The
ASCII profile fixture matches the qualified laboratory digest; complete Unicode
and HTML escaping interoperability with Python is not claimed. This ephemeral
digest is neither a published ABI nor a signed manifest.

Capability is checked before group or target lookup. The administrative plan
seals its base plan, method, parameter values and inventory/ledger digests.
Failed preparation discards both memory plans. Expired entries are pruned as
the underlying planner prunes them, preserving its bound.

## Execution sequence

1. Validate the live sender and stored plan. Reserve its association in the
   shared Planner so multiple guards cannot execute that account concurrently.
2. Under shared storage leases, verify the catalog snapshot, exact proof
   identity, absence of barrier/journal/owner, full inventory/ledger digests,
   group bindings, current identity/capability and deadline.
3. Release storage leases and call fresh interactive authorization using a
   detached request. No planner/guard memory mutex crosses this callback.
4. Revalidate the planner after authorization. Obtain the final storage leases,
   with the operation lease exclusive, and repeat the snapshot and binding
   checks. Capability/identity/deadline are checked again after group resolution.
5. Delete both plans before entering the effect callback, including when that
   callback later fails. Keep final leases held through dispatch and the
   subsequent primary-identity observation.
6. Release leases and reservation on return. Successful dispatch returns
   `OrdinaryMutationDispatched`, not a durable transaction certificate.

Denial or authorization failure leaves an unexpired plan available only for
another fresh authorization. An effect failure returns `MutationFailure`, an
identity change returns `InvalidIdentity`, and a later lease cleanup failure
returns a storage error. Those post-consumption failures cannot replay the same
plan. No automatic rollback or destructive compensation is attempted; a partial
Unix effect needs the separately qualified recovery protocol.

## Backend contract and remaining gates

`MutationLeases.WithLease` is a trusted synchronous backend interface. It must
invoke the visitor exactly once with immutable observations while holding
catalog, inventory and operation leases in that order, preserve visitor errors,
and release resources on every exit. The final operation lease must be
exclusive. Unknown/corrupt negative entries cannot be hidden by false booleans.
The guard refuses missing/duplicate visits, swallowed visitor failures and
unexpected backend errors; this does not turn an untrusted backend into proof
that real locks are held.

The memory fixtures qualify sequencing and concurrency. The concrete
[filesystem backend](parental-files.md) additionally validates kernel locks,
negative precedence, metadata, canonical inventory and explicit proof decoding.
The authoritative PAM proof/generation format, root-VM effects and producer
coverage still need qualification. NSS group recreation with the same tuple,
external noncooperating writers and guardian-role synchronization remain open.

The production service remains unchanged until writer/bootstrap/recovery and
ordinary-account preservation are qualified. Gates #2/#5/#6/#102, supervised
lifecycle, enforcement, privacy, rollback, GNOME, MAC and ISO qualification remain
open. The maintainer already authorized installation on the local machine after
parental controls are complete, using a new local child/adolescent test account.

Targeted tests, race checks, normal repository tests, vet and Go 1.22 compatibility
passed. Optional VM and system integration checks remain separate. Next: implement
the fixed Unix adapter, qualify the Go flow in a guarded VM, and integrate real
D-Bus/Polkit composition before enabling UsersService.


The [fixed Unix adapter and Go VM qualification](parental-unix.md) now cover
three real commands and two no-ops with root-owned leases. Real Go D-Bus/Polkit
and authoritative proof/bootstrap/recovery remain prerequisites to activation.
