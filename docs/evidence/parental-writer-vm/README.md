# Default-root catalog writer in a disposable VM

Four cases passed: a controlled contention between two actual writer processes,
and process exits after file fsync, rename and directory fsync. Both contenders
received the same original snapshot. The first held the existing exclusive lock;
the second returned Busy, the first committed revision 2, and a fresh attempt
using the original snapshot returned Conflict. The default-root reader returned
Busy during the exclusive lease. The catalog.lock inode was preserved.

Three actual child exits (code 19) released their kernel leases. Before rename,
the original catalog remained canonical and one root-only temporary file remained
ignored. After rename, the canonical state remained recovering with a barrier;
the old snapshot could not replay. A retry of the exact current blocked record
acknowledged only that negative state after another directory fsync. No admission
was reopened. These tests are process interruption, not power-loss/reboot proof.

All writes used NewCatalogWriter's default owner 0, with no owner override.
Canonical files were checked as root-owned regular 0600 single-link entries.
Ordinary, supervised and tombstoned peer records/policies were preserved. These
are fictitious catalog records, not proof of eligibility or enforcement for real
local users. No account/NSS/Unix/PAM/policy mutation occurred.

```sh
python3 docs/evidence/parental-writer-vm/verify-evidence.py
```

The controller verified a new disposable overlay/backing chain, actual QEMU
command, root identity, kernel test marker and disk serial before running the
binary. Account (including shadow/backups), PAM and existing D-Bus/Polkit files
were compared by hash, and SELinux state was unchanged. Test fixtures, including
orphan temporary files, were removed by the laboratory controller. The legacy
receipt field restored means unchanged in this step; no account restoration was
needed. QEMU exited normally, VM OFF, raw backing hash/size/inode/mtime unchanged.

The executable used Go1.22.12. manifest.json seals sources, drivers, receipts and
its hash; the binary itself stays in the local analysis directory. Normal/race/
vet/repository and minimum-Go tests passed separately. No failed VM attempt was
needed. No Polkit/capability/proof codec is exercised here, no independent
anti-rollback reference is claimed and SELinux was Permissive. This is a storage
qualification; normal startup does not activate the component. The writer still
requires qualified authorization and a lifecycle coordinator. Bootstrap, positive
authority recovery and product/local installation remain pending.
