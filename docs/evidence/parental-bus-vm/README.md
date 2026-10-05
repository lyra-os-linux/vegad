# Real system D-Bus/Polkit with the Go administrative guard

Thirteen scenarios passed in a new disposable overlay: profile/groups, wheel
grant plus consumed replay refusal, repeated grant no-op, wheel revoke, repeated
revoke no-op, explicit denial, authentication-agent cancellation, capability
revocation after authorization, catalog revision change, new denial marker,
replay through another connection, unauthorized caller and invalid profile.

There were ten actual Polkit checks, three real Unix commands and two no-ops.
Three invalid/unauthorized/replayed requests never consulted Polkit. The guard
observed all leases free during authorization; dispatch kept them held and Unix
commands observed the exclusive operation lease. Primary identity and the
neighbor's identity/profile/memberships were preserved in every successful test
case. The controller restored account hashes, shadow and non-target passwd lines,
PAM/state hashes and existing bus/Polkit configuration hashes. Temporary policy,
rule and client were removed; QEMU exit0, VM OFF, backing hash/inode/size/mtime
unchanged. Whole-file restoration is for an exclusive lab, not product recovery.

```sh
python3 docs/evidence/parental-bus-vm/verify-evidence.py
```

Only ordinaryuser UID1002 was the mutation target. installeduser UID1000 was a
client allowed by a narrow temporary Polkit rule plus a synthetic capability
callback. Authorization used the actual system bus and direct PolicyKit1 calls;
cancellation used a real registered agent that refuses authentication and never
handles a password. Polkit returned an ordinary denial for that cancellation.
No positive password authentication or GNOME dialog is qualified.

The compiled test executable used Go1.22.12. manifest.json seals its hash and
source/driver/receipt hashes; the executable remains in the local analysis folder.
The receipt's dbus.Variant value serializes as an empty JSON object: the subject
kind is recorded, but the receipt does not independently record the subject name.
Exact sender binding is verified by the unit test and archived Go implementation;
client sender names/UIDs and the actual Polkit results are recorded separately.

The first attempt failed while parsing combined client stdout/stderr: the fixture
had not exported introspection, so a diagnostic preceded otherwise valid JSON.
Real operations occurred in that attempt; account/config restoration and cleanup
passed. failed-client-output preserves those negative receipts and the old test
source as .go.txt (not a Go package). No positive qualification is claimed from
that attempt. The retry exports the experimental introspection explicitly,
parses stdout separately and expects the observed cancellation denial; it uses
a fresh overlay. No production transport behavior was relaxed to make it pass.

Proof codec is still synthetic identity JSON; capability is not a persisted
guardian association. Registered producers are not an enforcement certificate.
SELinux was Permissive. No authoritative PAM/generation/lifecycle, crash recovery,
MAC/GNOME login, independent anti-rollback or production deployment is claimed.
This component is not exported or installed by normal vegad startup. Next:
ordinary-account catalog bootstrap/writer/recovery and remaining product gates.
