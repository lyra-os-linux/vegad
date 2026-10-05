# Administrative D-Bus/Polkit composition

`BusAuthority` uses an explicitly supplied, service-owned system bus. Bind its
`ResolveSender` to the planner, and independently supply the qualified capability
source; Polkit is never a guardian role or an implicit root capability.
`GetConnectionUnixUser` resolves only unique names and has a five-second deadline.

`AdministrativeService` composes that authority with the existing guard and
qualified effect. Construction does not export an object, claim a bus name,
install policy or activate UsersService. The experimental methods are
PrepareUpdateUser, PrepareSetAdmin and Execute. godbus injects `dbus.Sender` from
the method header; no caller UID, action ID, username or execution payload is a
wire argument. Execute takes only the stored plan identifier. Production API,
read/privacy policy, activation and lifecycle contracts remain pending.

The guard checks capability before target lookup; seals parameters; authorizes
outside all leases; then checks sender/capability/identity/digests/groups/deadline
again under the final leases. It consumes the plan before dispatch. Each method
maps to a server-configured Polkit action. CheckAuthorization uses the caller's
system-bus-name subject and AllowUserInteraction, with details from the sealed
request (method, association, generation, revision, target UID, parameter digest).
It sends no full name, supplemental names, password, photo or client-chosen action.
Only an authorized, non-challenge result permits continuation. Transport/decoding
errors become safe codes; they never expose a private D-Bus error message.

Authorization waits at most 45 seconds. Timeout also attempts a bounded
CancelCheckAuthorization using the random ID belonging to that exact request.
Cancellation failure still denies; this is not a guarantee that an unreachable
Polkit agent disappeared. The 60-second planner deadline remains independent:
late or expired plans cannot execute. Authorization denial does not consume a
plan, but retry must repeat fresh capability/state and interactive checks.

Unit tests exercise fixed subject/action/details, failures/malformed replies,
root without automatic authorization, challenge/contradictory results, timeout
cancellation and guard replay/revocation/barrier checks. The
[disposable VM evidence](evidence/parental-bus-vm/README.md) records actual system
bus, Polkit and Unix effects. It uses a synthetic capability source and identity
proof; this is not a persisted guardian or authoritative supervised PAM flow.

Protocol reference: [D-Bus specification](https://dbus.freedesktop.org/doc/dbus-specification.html).
The opt-in test exports introspection separately, through godbus's introspect
package. Deployment must publish a reviewed contract explicitly.

Next: qualify ordinary-account catalog bootstrap/writer/recovery and maintenance,
choose authoritative proof/generation binding, then finish guardian roles,
lifecycle, enforcement, rollback, GNOME/MAC/ISO and packaging before activation
or local installation. Existing UsersService behavior has not been replaced.
