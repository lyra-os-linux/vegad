# Official NVIDIA integration (Desktop and Server)

The Desktop/Server capability `nvidia-official-v1` enables the existing v1
`Software.InstallNvidia(confirmed: bool) -> transaction_id` endpoint for a
**new, restricted flow**. Earlier daemons retired this endpoint; clients must
check capabilities before offering installation. The wire signature and
`NvidiaStatus` tuple are unchanged. Generic driver switching and nonfree
firmware installation remain retired. `nvidia-recovery-v1` adds a typed public
recovery query without changing the existing `NvidiaStatus` tuple.

## Public diagnostics

`NvidiaStatus` and `CheckNvidia` run in the original caller's unprivileged query
worker. They never authorize, refresh repositories, change power policy or
install packages. Each external diagnostic command has a five-second limit;
the worker also has the existing overall limit. Opening Vega calls only reads.
For these two methods only, module files must remain visible: systemd's
`ProtectKernelModules=yes` also masks `/usr/lib/modules` and the target of
`/boot/vmlinuz`. Replace that masking with `SystemCallFilter=~@module`, retaining
caller UID, no capabilities, NoNewPrivileges and a strictly read-only system.
Other queries keep their existing module masking. Test through the actual
brokered worker, not only the standalone `vegad query` executable.
Suspend quarantine reconciliation remains a separate startup task, preserving
administrator drop-ins and retaining a guard if RPM state cannot be identified.

The qualified integration is currently NVIDIA **610.57.04**, a SUSE-signed
`nvidia-open-driver-G07-signed-cuda-kmp-default` and `lyra-nvidia` of the same
version. Detect all NVIDIA display devices against the version-specific official
PCI table, not a numeric ID range. Unknown devices and mixed unsupported GPUs
cannot be installed automatically. Match required RPM leaves, all G07 driver
leaves, loaded module, NVML GPU addresses/versions, module on disk, SUSE signer
and RPM ownership. Independent EGL bridge libraries have independent versions.

`/boot/vmlinuz` is checked as well as the running kernel. This does **not** prove
that a custom GRUB entry will boot that image or validate every installed
fallback kernel. A future kernel without a corresponding module blocks this
flow; the integration does not lock kernel updates. BIOS is not rejected merely
for lacking EFI variables. An unknown Secure Boot state on an EFI machine fails
closed.

| State | Meaning |
| --- | --- |
| `no-gpu`, `unsupported-gpu`, `unsupported-system` | No compatible automatic installation |
| `unknown-secure-boot`, `unknown-boot-kernel` | Required diagnostic is unavailable |
| `conflict` | Legacy/alternative provider or unowned module needs migration review |
| `inconsistent` | Missing or mismatched driver packages |
| `kernel-missing`, `unsigned-module` | Module/kernel/signing ownership gate failed |
| `available` | Compatible clean system; optional first installation |
| `unmanaged` | Coherent official stack; optional installation of the Lyra contract |
| `reboot-required` | Coherent installed stack, corresponding module not loaded |
| `driver-error` | Corresponding module is loaded, but GPU/NVML check failed |
| `active` | Matching guarded stack, current/default modules and NVML checks pass |

`Detail` contains technical identifiers, not a localized product explanation.
GTK, CLI and Web translate installation explanations in PT/EN/ES. `RecoverySnapshot` is the last
snapshot reference recorded by this flow, not a guarantee against later
administrator deletion or retention cleanup. It lives in its own root-owned
`/var/lib/vegad-nvidia`, readable by query workers.

## Installation and trust

1. Mandatory review in each client, even if optional confirmations are disabled.
   Closing/cancelling sends no administrative request. `confirmed=false` is
   rejected by the daemon before any authorization or system dependency.
2. Existing `software.install` Polkit action against the **original bus sender**.
   No authentication during status/refresh. Serialize NVIDIA operations.
3. Recheck hardware, base (Leap-release 16.1/x86_64), installed packages and
   repository policy. Preserve disabled/modified repositories; reject competing
   enabled NVIDIA channels. No automatic removal or legacy G06 migration.
4. Require a qualified recovery strategy: Snapper root snapshot on Btrfs, or
   verified offline OS recovery on the simple Server ext4 layout. Abort if
   recovery cannot be prepared; do not silently install without it.
5. Refresh three fixed sources in a private repository/cache directory:
   Lyra NVIDIA OBS, official NVIDIA SUSE16 and Leap 16.1 OSS. Require repository
   and RPM signatures. Import only the approved full fingerprints when Zypper
   asks; an unknown OS key or any other question aborts. No blanket key import.
6. Inspect the actual structured solver summary **inside the same Zypper process
   holding the package lock**, and recheck prerequisites before answering its
   commit prompt. Create and verify the recovery point inside that callback,
   after metadata refresh and before RPM commit. Accept only additions of the ten qualified packages, fixed
   versions, expected architectures and sources. No kernel changes, removals,
   upgrades, downgrades, DKMS, file-conflict overrides, solver choices or implicit
   EULA acceptance. Unexpected dependencies require a reviewed follow-up.
7. Download before RPM commit; verify installed state. Only then persist the OBS
   channel. The metapackage supplies its upstream repo. If a failed first commit
   leaves that new repo without its guard, disable the managed definition; never
   alter a pre-existing administrator file. Preserve errors and snapshot ID.
8. Finalize the recovery record (and post-snapshot on Btrfs) and refresh
   diagnostics. A driver change needs boot
   validation; adopting the guard on a working driver does not imply a reboot.

The initial native KMP kernel is `6.12.0-160100.4-default`. Installing a clean
stack on a different kernel waits for qualification; existing official stacks
can be adopted only when `modinfo` resolves corresponding signed modules.

Trust identities reviewed 2026-09-16:

- OBS: `399218A6E088C4053F4533BE58097F767EDCA82E`
- NVIDIA: `CF65941A859D4B4E6870F188736A284B3A8B5622`

## Server ext4 recovery

The first supported layout is one ext4 root containing `/usr`, `/etc`, `/boot`,
`/var` and the RPM database at `/usr/lib/sysimage/rpm`; the ESP is separate and
excluded. Reject separate system filesystems, unexpected mounts and symlinked
critical directories. No RAID/LVM/layout expansion is implied.

Before confirming the reviewed Zypper commit, vegad checks free space and uses
Restic >= 0.17 to back up `/usr`, `/etc`, `/boot` without the ESP, and
`/var/lib/alternatives`. The active RPMdb files are excluded; `rpmdb --exportdb`
exports consistent headers and the package inventory is checked again after
backup. Restic verifies all repository data before installation is allowed.
Each point has an encrypted repository and root-only random key, export and
manifest beneath `/var/lib/vegad-nvidia/points/REFERENCE`. Encryption protects
access by ordinary users; a root administrator can read the key. This local
copy protects against installation failure, not physical disk failure.

`Software.NvidiaRecovery() -> (bssss)` returns availability, kind, opaque
reference, state and technical detail. `available` means preflight succeeded;
it does not claim an existing backup. Kinds are `snapper` and `restic-offline`.
States are `unprepared`, `ready`, `installed`, `failed`, `restoring`, `restored`.
The public `recovery.json` contains no OS files, key or package inventory.
`NvidiaStatus.RecoverySnapshot` remains a numeric Snapper ID only.

For ext4 recovery, boot compatible Lyra Server rescue media containing vegad,
Restic and RPM, identify and mount the original root at `/mnt`, and leave ESP,
proc, sys, dev and any other subordinate filesystems unmounted there. Review the
reference shown by Vega and the point's creation time in its private manifest.
Then run as the rescue administrator (replace `REFERENCE`):

```sh
sudo /usr/lib/vega/vegad nvidia-recover --target /mnt --reference REFERENCE --confirm
```

The command rejects the running root, another filesystem UUID/machine identity,
duplicate/subordinate mounts, unsafe paths, corrupt exports and unverified
points. It restores each fixed snapshot subtree, verifying files and deleting
new files only within that subtree; preserves ESP and the current RPMdb until a
fresh database can be imported and atomically exchanged; and verifies the final
package inventory. Service data, home, logs and the recovery repository are
not restored. This is an **OS rollback**, not just replacement of one driver:
later unrelated OS updates would also be reverted, so do not reuse an old
point without reviewing its effect on applications and their current data.

An interrupted restore leaves `recovery-required`; do not boot that system until
recovery completes. Repeat the same offline command. The Restic child terminates
with the recovery process; only locks identified as stale by Restic are removed.
A remaining repository lock requires investigating its owner; do not force-unlock
an active operation. Detailed Restic failures are retained root-only as
`last-error.txt` in the point directory. There is no Web endpoint for live OS
rollback, disk selection, formatting or arbitrary command execution.

## Tests and remaining qualification

Unit tests cover mismatched/partial leaves, mixed GPUs, future/unsigned kernels,
loaded-version mismatch, NVML errors, BIOS, public query purity, authorization
refusal, cancellation, solver-plan rejection, trust prompts and preservation of
administrator settings. GTK native tests use a private bus and compositor:
startup, refresh, mandatory review, cancel/close, denial, success, partial failure
and daemon owner loss. A real Zypper dry-run in a disposable RPM database tests
adoption, clean-driver resolution and cancellation without host changes.

The physical GTX 1650 reference passes read-only status. These tests do not
complete legacy migration, real full-driver installation/recovery on additional
hardware, new-kernel boot, PackageKit, rendering/suspend/CUDA qualification or
local ISO testing. Keep #82 open for those gates. Do not roll back the running
filesystem automatically on failure; review the snapshot and coherent previous
kernel through the existing recovery tooling.

Sources: [NVIDIA supported GPUs](https://download.nvidia.com/XFree86/Linux-x86_64/610.57.04/README/supportedchips.html),
[NVIDIA SUSE packaging](https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/suse.html).
