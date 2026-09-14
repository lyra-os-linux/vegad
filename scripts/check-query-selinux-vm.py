#!/usr/bin/env python3
"""Qualify the query boundary with Leap's policy on a disposable ext4 disk.

Requires the staged official SELinux tools/policy plus vegad module in
/tmp/lyra-selinux-tools/root, and the same compiled binaries as check-query-vm.py.
The guest labels its own disk; no host label, service or policy is changed.
"""
import gzip
import importlib.util
from pathlib import Path
import shutil
import subprocess
import sys

sys.dont_write_bytecode = True

BASE = Path('/tmp/lyra-query-selinux-vm')
TOOLS = Path('/tmp/lyra-selinux-tools/root')
REPO = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('query_vm', REPO / 'scripts/check-query-vm.py')
vm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vm)
vm.BASE = BASE
vm.ROOT = BASE / 'root'


def build():
    vm.build()
    full = vm.ROOT
    shutil.copytree(TOOLS / 'etc/selinux', full / 'etc/selinux', dirs_exist_ok=True)
    # Keep only the linked policy containing Vega. load_policy can downgrade
    # the stock policy.35 and otherwise select it ahead of our policy.33.
    for policy in (full / 'etc/selinux/targeted/policy').glob('policy.*'):
        if policy.name != 'policy.33':
            policy.unlink()
    (full / 'etc/selinux/config').write_text('SELINUX=permissive\nSELINUXTYPE=targeted\n')
    for name in ['load_policy', 'setfiles', 'restorecon']:
        vm.binary(str(TOOLS / 'usr/sbin' / name), '/usr/sbin/'+name)
    vm.binary('/usr/bin/dmesg')
    # Match a normal unconfined local administrator/client rather than the
    # init-script domain inferred for this synthetic test runner.
    vm.put('/etc/systemd/system/backup-test.service.d/selinux.conf',
           '[Service]\nSELinuxContext=system_u:system_r:unconfined_t:s0\n')
    # The first boot loads and labels while permissive. Before the actual tests
    # the guest enables enforcing mode and records it with the test results.
    script = (full / 'test.sh').read_text()
    script = script.replace('VEGA_BACKUP_VM_TEST=1 /usr/bin/backup-tests',
                            'echo 1 > /sys/fs/selinux/enforce\n'
                            'echo "LYRA_SELINUX_ENFORCING=$(cat /sys/fs/selinux/enforce)"\n'
                            'VEGA_BACKUP_VM_TEST=1 /usr/bin/backup-tests')
    script = script.replace('echo "LYRA_BACKUP_VM_RESULT=$result"',
                            'echo "LYRA_BACKUP_VM_RESULT=$result"\n'
                            'journalctl --no-pager -u dbus.service -u vegad.service -u vegad-log-export.service\n'
                            'echo "LYRA_SELINUX_AVC_BEGIN"\ndmesg\n'
                            'echo "LYRA_SELINUX_AVC_END"')
    (full / 'test.sh').write_text(script)
    image = BASE / 'root.ext4'
    with image.open('wb') as output:
        output.truncate(1536 * 1024 * 1024)
    subprocess.run(['mkfs.ext4', '-q', '-F', '-d', str(full), str(image)], check=True)

    vm.ROOT = BASE / 'boot'
    for name in ['dev', 'proc', 'sys', 'run', 'newroot', 'tmp', 'usr/bin']:
        (vm.ROOT / name).mkdir(parents=True, exist_ok=True)
    for name in ['bash', 'mount', 'mkdir', 'insmod', 'switch_root', 'chroot']:
        vm.binary(shutil.which(name))
    (vm.ROOT / 'bin').symlink_to('usr/bin') if not (vm.ROOT / 'bin').exists() else None
    for module in ['loop', 'jbd2', 'mbcache', 'crc16', 'ext4']:
        shutil.copyfile(full / (module+'.ko'), vm.ROOT / (module+'.ko'))
    with (vm.ROOT / 'virtio_blk.ko').open('wb') as output:
        subprocess.run(['zstd', '-d', '-c', str(vm.KERNEL.parent / 'kernel/drivers/block/virtio_blk.ko.zst')], stdout=output, check=True)
    vm.put('/init', '''#!/bin/bash
set -eu
export PATH=/usr/sbin:/usr/bin:/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
mount -t tmpfs tmpfs /run
mkdir -p /sys/fs/cgroup /sys/fs/selinux
mount -t cgroup2 cgroup2 /sys/fs/cgroup
mount -t selinuxfs selinuxfs /sys/fs/selinux
for module in loop jbd2 mbcache crc16 ext4 virtio_blk; do insmod /$module.ko; done
mount -t ext4 /dev/vda /newroot
for fs in proc sys dev run; do mount --move /$fs /newroot/$fs; done
chroot /newroot /usr/sbin/load_policy -i
chroot /newroot /usr/sbin/setfiles -F -e /proc -e /sys -e /dev -e /run /etc/selinux/targeted/contexts/files/file_contexts /
exec switch_root /newroot /usr/lib/systemd/systemd --system --log-level=warning --log-target=console --unit=backup-test.target
''', 0o755)
    files = b'\0'.join(str(p.relative_to(vm.ROOT)).encode() for p in vm.ROOT.rglob('*')) + b'\0'
    packed = subprocess.run(['cpio', '--null', '-o', '--format=newc', '--owner=0:0'], cwd=vm.ROOT, input=files, capture_output=True, check=True)
    with gzip.open(BASE / 'boot.cpio.gz', 'wb', compresslevel=1) as output:
        output.write(packed.stdout)
    print('Disposable SELinux disk and boot initramfs prepared:', BASE)


def run():
    command = ['qemu-system-x86_64', '-accel', 'tcg', '-cpu', 'max', '-smp', '2', '-m', '3072',
               '-kernel', str(vm.KERNEL), '-initrd', str(BASE / 'boot.cpio.gz'),
               '-drive', 'file='+str(BASE / 'root.ext4')+',format=raw,if=virtio',
               '-append', 'rdinit=/init console=ttyS0 quiet panic=1 selinux=1 enforcing=0 systemd.log_level=warning',
               '-display', 'none', '-serial', 'stdio', '-monitor', 'none', '-no-reboot', '-nic', 'none']
    log = BASE / 'serial.log'
    with log.open('w') as stream:
        result = subprocess.run(command, stdout=stream, stderr=subprocess.STDOUT, timeout=1000)
    text = log.read_text()
    print(text[-22000:])
    if result.returncode or 'LYRA_SELINUX_ENFORCING=1' not in text or 'LYRA_BACKUP_VM_RESULT=0' not in text:
        raise SystemExit('SELinux qualification failed; see '+str(log))


if __name__ == '__main__':
    {'build': build, 'run': run}[sys.argv[1]]()
