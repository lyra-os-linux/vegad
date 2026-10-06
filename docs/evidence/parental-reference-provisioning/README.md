# Initial reference enrollment qualification — 2026-10-06

Qualification of `ReferenceProvisioner`, following its implementation at
`c1ecec8`. This evidence records a new disposable Leap 16.1 VM with a new
512 MiB second disk and a new system overlay over the previously prepared
Snapper/Btrfs/raw backing chain. Historical evidence is not rewritten.

The guest test uses default root-owned factories and the actual
`BtrfsSnapshotDomain` qualifier. Four child processes exit 73 without defers
after the enrollment seal, reference, pin and commit marker are synchronized.
The first three prefixes refuse readers, nil authorization, a different
operation and a revoked installer proof, then complete the exact resume.
The fourth is already committed and refuses resume. Every case advances to
sequence 2 while retaining the original lock and permanent markers.

Completed stores then lose respectively the reference, pin, both, or lock.
Readers refuse; neither Initialize nor ResumeInitial recreates the lost files.
The catalog bytes remain unchanged. The issuer, maintenance permission and
installer freshness proof are explicit fixtures; a production installer and
guardian/Polkit integration are still absent. This test does not qualify
physical power loss, reboot/GRUB, Snapper rollback, global antirollback,
hostile root or SELinux enforcement (guest is Permissive).

Safety guards check the disposable kernel marker, root privileges, root disk
serial, second disk serial/size, no existing mount and blank `wipefs --no-act`
output before formatting only guest `/dev/vdb`. Subvolume bind mounts and the
volume are unmounted afterward. Account files and backups, PAM, DBus, Polkit,
Snapper configuration and SELinux state are identical before/after. QEMU exited
0. All three backing image hashes, sizes, inodes and mtimes are unchanged.
The local workstation was not mounted, repartitioned, configured or installed.

`manifest.json` pins the Go sources (including this VM test), module dependencies,
contract documentation, harness and receipts, plus the guest Go 1.22.12 binary
hash. Verify this evidence at its recorded sources with:

```
python3 docs/evidence/parental-reference-provisioning/verify.py
```

Drivers record absolute paths from this run. A rerun requires new disposable
disks/paths and a newly compiled binary; never overwrite these historical
receipts to represent a later source revision. Binary/console transfer logs
are kept outside Git in `analysis/2026-10-06/vegad-parental-reference-provisioning-vm`.
