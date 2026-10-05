# Plans bound to the service and live caller

`internal/parental.Planner` extends the read-only [catalog foundation](parental-catalog.md).
It is not connected to the production D-Bus service or UsersService. Existing
Unix mutations and admission paths still require integration; no package or
policy is installed by this change.

`NewPlanner` takes a trusted snapshot provider, live unique-name resolver,
capability checker and identity observer. These service adapters must be safe
for concurrent calls, perform bounded work, and never ask for interactive
permission here. Root requires an explicit positive capability just like every
other UID. No desktop-user fallback or guardian role is inferred.

`Prepare(sender, association, expectedRevision)` checks the actual caller's
capability before target lookup. It requires an ordinary active account with
an exact identity, the expected account revision, and no catalog barrier.
Supervised lifecycle plans return `UnsupportedOperation`. The caller is checked
again after storage and identity observations before the plan is published.
Missing or invalid records cannot turn into an ordinary-account plan.

Plans have 128-bit random identifiers and bind the sender, resolved caller UID,
association, generation, account revision, canonical catalog digest and complete
identity. State stays private in the service. Account accessors return copies;
the client cannot supply a replacement binding to `Validate`. The plan's fixed
base effect is `deny-new-admission-only`, as in the qualified laboratory planner;
a future administrative guard must additionally bind its method and parameters.
This is not an authorization token or an executable withdrawal operation.

The instance holds at most 128 plans. Each expires after 60 seconds measured
with Go's monotonic elapsed time, not wall-clock timestamps. Expired entries are
pruned when preparing another plan. Backwards, negative or overflowing test
clock values fail without extending a deadline. Live identifier collisions have
a bounded retry and cannot replace an existing plan; entropy failure returns
a safe code. Identifier uniqueness across expired entries or process restarts
relies on the trusted cryptographic entropy source.

`Validate(sender, identifier)` takes only the server-issued identifier. It
rejects a different unique sender even when its UID matches. It resolves the
caller and capability again, checks the deadline, reads the current catalog,
compares the entire digest and observes the target's identity. After those
observations it repeats caller/capability and deadline checks. A callback that
revokes access, changes UID or crosses expiry cannot return a valid plan.
Changing any catalog account invalidates the whole catalog digest; this is
intentional coarse conflict detection, not an anti-rollback guarantee.

Plans are local memory only. A new instance cannot recover an old plan as
authority. Validation does not consume the plan and may be repeated while its
bindings remain valid. Map and monotonic clock access are protected by a mutex;
no resolver, capability, storage or identity callback runs with that mutex held.
The private entropy/clock functions run only inside the short memory section.

Errors from transport/capability/identity callbacks and unexpected storage
failures are reduced to fixed codes. Catalog errors retain their specific safe
codes without wrapped paths or private messages. No catalog file or proof is
written, no Polkit interaction occurs, and no Unix command runs in this module.

## Execution boundary still required

A successful validation describes the snapshot just observed. It does not hold
a catalog, inventory or operation lease; concurrent durable state may change
immediately afterward. It cannot be used directly as permission to invoke
`usermod`, `gpasswd`, `userdel` or a parental admission action.

The [administrative guard](parental-guard.md) now binds method, parameters,
inventory and negative operation state through a trusted lease backend interface.
The [filesystem backend](parental-files.md) now has real file/lock tests.
Unix effects and the complete Go flow in a guarded VM still need qualification.
It releases leases for fresh authorization, revalidates under final leases,
and consumes both plans before dispatch. Durable writer/bootstrap/recovery must
preserve existing ordinary accounts before production activation. Create/Remove/
password/photo, guardian roles, enforcement and rollback qualification remain
separate open gates (#2/#5/#6/#102).

Tests cover denied-before-lookup behavior, sender replacement, caller UID and
identity changes, catalog revisions/generation/barrier changes, expiry during
identity observation, bounded capacity, entropy failure/collisions, restart,
private errors and concurrent preparation/validation. Targeted tests, race
checks, normal repository tests, vet and Go 1.22 compatibility passed. Default
VM-only and optional integration gates are not implied by those unit checks.
