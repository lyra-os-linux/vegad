#!/usr/bin/env python3
"""Build/run a disposable initramfs VM using local Leap binaries, no host disks."""
import gzip
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

BASE = Path('/tmp/lyra-query-hardening-vm')
REPO = Path(__file__).resolve().parents[1]
ROOT = BASE / 'root'
KERNEL = Path('/usr/lib/modules/6.12.0-160099.49-default/vmlinuz')


def put(path, text, mode=0o644):
    dest = ROOT / path.lstrip('/')
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(text)
    dest.chmod(mode)


def binary(source, destination=None):
    dest = ROOT / (destination or source).lstrip('/')
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, dest)
    dest.chmod(0o755)
    result = subprocess.run(['ldd', source], capture_output=True, text=True)
    for soname, dep in re.findall(r'^\s*(\S+) => (/[^\s]+)', result.stdout, re.M):
        # Do not depend on the host's ld.so.cache or its hwcaps SONAME links.
        # Prefer generic libraries when available; expose their SONAME in the
        # guest loader's standard path, including dependencies from subdirs.
        generic = Path('/lib64') / soname
        source_dep = generic if generic.exists() else Path(dep)
        target = ROOT / 'lib64' / soname
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source_dep, target)
        target.chmod(0o755)
    loader = '/lib64/ld-linux-x86-64.so.2'
    target = ROOT / loader.lstrip('/')
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(loader, target)
    target.chmod(0o755)



def build():
    pattern = sys.argv[2] if len(sys.argv) > 2 else '^TestQueryHardeningSystemdVM$'
    for directory in ['dev', 'proc', 'sys', 'run', 'tmp', 'etc/systemd/system', 'usr/lib/systemd/system', 'var/log', 'root']:
        (ROOT / directory).mkdir(parents=True, exist_ok=True)
    (ROOT / 'tmp').chmod(0o1777)
    for name in ['bash', 'mount', 'umount', 'findmnt', 'systemctl', 'mkdir', 'touch', 'restic', 'mkfs.ext4', 'losetup', 'insmod', 'pkcheck', 'dbus-daemon', 'runuser', 'bootctl', 'systemd-run', 'systemd-sysusers', 'systemd-tmpfiles', 'systemd-analyze', 'journalctl', 'df', 'lsblk', 'lscpu', 'rpm', 'zypper', 'rpmdb2solv', 'repo2solv', 'useradd', 'usermod', 'userdel', 'groupadd', 'groupdel', 'id', 'chmod', 'chpasswd', 'cp', 'mv', 'rm', 'ln', 'cat', 'sleep']:
        binary(shutil.which(name))
    binary('/usr/libexec/polkit-1/polkitd')
    binary('/lib64/security/pam_permit.so')
    binary('/lib64/security/pam_unix.so')
    # pam_unix uses external helpers when SELinux is loaded, including for
    # root password changes. unix_update is root-readable on the host; stage
    # its official RPM under /tmp rather than copying a protected host file.
    for name, mode in [('unix_update', 0o700), ('unix_chkpwd', 0o4755)]:
        binary('/tmp/lyra-selinux-tools/root/usr/sbin/'+name, '/usr/sbin/'+name)
        (ROOT / 'usr/sbin' / name).chmod(mode)
    for plugin in Path('/usr/lib64/rpm-plugins').glob('*.so'): binary(str(plugin))
    for module in ['drivers/block/loop', 'fs/jbd2/jbd2', 'fs/mbcache', 'lib/crc16', 'fs/ext4/ext4']:
        source = KERNEL.parent / 'kernel' / (module + '.ko.zst')
        dest = ROOT / (Path(module).name + '.ko')
        with dest.open('wb') as output:
            subprocess.run(['zstd', '-d', '-c', str(source)], stdout=output, check=True)
    (ROOT / 'bin').symlink_to('usr/bin') if not (ROOT / 'bin').exists() else None
    (ROOT / 'sbin').symlink_to('usr/sbin') if not (ROOT / 'sbin').exists() else None
    (ROOT / 'usr/bin/sh').symlink_to('bash') if not (ROOT / 'usr/bin/sh').exists() else None
    binary('/usr/lib/systemd/systemd')
    binary('/usr/lib/systemd/systemd-update-helper')
    binary('/usr/bin/env')
    binary('/usr/bin/sed')
    binary('/usr/lib/systemd/systemd-journald')
    binary('/usr/lib64/libnss_systemd.so.2')
    for path in ['/usr/lib64/rpm', '/usr/lib/rpm', '/usr/lib/zypp', '/usr/lib64/zypp', '/usr/libexec/zypp', '/usr/lib/sysimage/rpm']:
        if Path(path).exists(): shutil.copytree(path, ROOT / path.lstrip('/'), dirs_exist_ok=True)
    for directory in ['/usr/lib/zypp', '/usr/lib64/zypp', '/usr/libexec/zypp']:
        for program in Path(directory).rglob('*'):
            if program.is_file() and program.read_bytes()[:4] == b'\x7fELF': binary(str(program))
    (ROOT / 'var/lib/rpm').parent.mkdir(parents=True, exist_ok=True)
    if not (ROOT / 'var/lib/rpm').is_symlink(): (ROOT / 'var/lib/rpm').symlink_to('/usr/lib/sysimage/rpm')
    for path in ['/etc/zypp/zypp.conf', '/etc/zypp/zypper.conf', '/etc/rpmrc']:
        if Path(path).exists(): put(path, Path(path).read_text())
    binary('/usr/lib/systemd/systemd-executor')
    binary('/usr/lib/systemd/systemd-shutdown')
    # The host uses ld.so.cache for this optional directory; the guest has no
    # host cache, so expose zlib in the loader's standard search directory.
    binary('/usr/lib64/zlib-ng-compat/libz.so.1', '/lib64/libz.so.1')
    binary('/tmp/lyra-hardening-vegad', '/usr/lib/vega/vegad')
    binary('/tmp/lyra-hardening-tests', '/usr/bin/backup-tests')
    put('/etc/passwd', 'root:x:0:0:root:/root:/bin/bash\npolkitd:x:999:999:polkit:/var/lib/polkit:/bin/false\nalice:x:1001:1001:alice:/home/alice:/bin/bash\nvegad-log:x:998:998:logs:/var/log/vega:/usr/sbin/nologin\nvegad-query:x:997:997:queries:/var/empty:/usr/sbin/nologin\n')
    put('/etc/group', 'root:x:0:\npolkitd:x:999:\nalice:x:1001:\nvegad-log:x:998:\nvegad-query:x:997:\nsystemd-journal:x:190:\n')
    put('/etc/pam.d/runuser', 'auth sufficient /lib64/security/pam_permit.so\naccount sufficient /lib64/security/pam_permit.so\nsession sufficient /lib64/security/pam_permit.so\n')
    (ROOT / 'var/run').symlink_to('/run') if not (ROOT / 'var/run').is_symlink() else None
    for directory in ['etc/polkit-1/rules.d', 'usr/share/polkit-1/rules.d', 'var/lib/polkit', 'home/alice']:
        (ROOT / directory).mkdir(parents=True, exist_ok=True)
    policy = REPO / 'packaging/org.lyraos.vega.policy'
    put('/usr/share/polkit-1/actions/org.lyraos.vega.policy', policy.read_text())
    put('/etc/dbus-1/system.conf', '''<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>system</type><listen>unix:path=/run/dbus/system_bus_socket</listen><auth>EXTERNAL</auth>
<policy context="default"><allow user="*"/><allow own="*"/><deny own="org.lyraos.Vega1"/><allow send_destination="*"/><allow receive_sender="*"/></policy><policy user="root"><allow own="*"/></policy></busconfig>
''')
    put('/etc/pam.d/chpasswd', 'password required /lib64/security/pam_unix.so sha512\n')
    put('/etc/shadow', 'root:!:20000:0:99999:7:::\nalice:!:20000:0:99999:7:::\n', 0o600)
    put('/etc/gshadow', 'root:!::\nalice:!::\n', 0o600)
    put('/etc/login.defs', 'UID_MIN 1000\nUID_MAX 60000\nGID_MIN 1000\nGID_MAX 60000\nCREATE_HOME yes\n')
    # Two unsigned, local fixture RPMs; never installed on the host.
    rpm_top = BASE / 'fixture-build'
    for name in ['BUILD', 'BUILDROOT', 'RPMS', 'SOURCES', 'SPECS', 'SRPMS']:
        (rpm_top / name).mkdir(parents=True, exist_ok=True)
    spec = rpm_top / 'SPECS/fixture.spec'
    spec.write_text("""Name: lyra-query-vm-fixture
Version: %{fixture_version}
Release: 1
Summary: Disposable Vega privilege-boundary fixture
License: MIT
BuildArch: noarch
%description
Used only in an isolated disposable VM.
%install
mkdir -p %{buildroot}/usr/share/lyra-query-vm-fixture
printf '%s\\n' '%{version}' > %{buildroot}/usr/share/lyra-query-vm-fixture/version
%post
mkdir -p /var/lib/lyra-query-vm-fixture
id -u > /var/lib/lyra-query-vm-fixture/scriptlet-uid
%files
/usr/share/lyra-query-vm-fixture
""")
    for version in ['1', '2']:
        build = subprocess.run(['rpmbuild', '-bb', '--define', '_topdir '+str(rpm_top), '--define', 'fixture_version '+version,
                                '--define', '__os_install_post %{nil}', '--define', '_build_id_links none', str(spec)],
                               capture_output=True, text=True)
        if build.returncode: raise RuntimeError(build.stdout + build.stderr)
        dest = ROOT / ('fixtures/v'+version)
        dest.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(rpm_top / ('RPMS/noarch/lyra-query-vm-fixture-'+version+'-1.noarch.rpm'), dest / 'fixture.rpm')
    put('/etc/zypp/repos.d/query-fixture-v1.repo', '[query-fixture-v1]\nname=Query fixture v1\nenabled=1\nautorefresh=0\nbaseurl=dir:/fixtures/v1\ntype=plaindir\ngpgcheck=0\n')
    # Match the journal group/SGID setup normally supplied by systemd tmpfiles.
    put('/usr/lib/tmpfiles.d/query-journal.conf', 'd /run/log/journal 2755 root systemd-journal -\nd /var/log/journal 2755 root systemd-journal -\n')
    put('/etc/zypp/repos.d/query-fixture.repo', '[query-fixture]\nname=Query fixture\nenabled=1\nautorefresh=0\nbaseurl=dir:/fixtures/v2\ntype=plaindir\ngpgcheck=0\n')
    put('/etc/nsswitch.conf', 'passwd: files systemd\ngroup: files systemd\nhosts: files\n')
    put('/etc/machine-id', 'a756b1d851a7494eb039f8fa274e0061\n')
    put('/etc/systemd/system/vegad-log-export.service', (REPO / 'packaging/vegad-log-export.service').read_text())
    put('/etc/systemd/system/vegad.service', (REPO / 'packaging/vegad.service').read_text())
    put('/etc/systemd/system/vegad.service.d/vm.conf', '[Service]\nEnvironmentFile=\nEnvironment=VEGAD_PROFILE=server\n')
    for unit in ['systemd-journald.service', 'systemd-journald.socket', 'systemd-journald-dev-log.socket']:
        put('/etc/systemd/system/'+unit, Path('/usr/lib/systemd/system',unit).read_text())
    put('/etc/systemd/journald.conf', '[Journal]\nStorage=volatile\n')
    put('/etc/systemd/system/dbus.socket', '[Unit]\nDefaultDependencies=no\n[Socket]\nListenStream=/run/dbus/system_bus_socket\n')
    put('/etc/systemd/system/dbus.service', '[Unit]\nDefaultDependencies=no\nRequires=dbus.socket\nAfter=dbus.socket\n[Service]\nType=notify\nExecStart=/usr/bin/dbus-daemon --config-file=/etc/dbus-1/system.conf --address=systemd: --nofork --nopidfile --systemd-activation\n')
    put('/etc/os-release', 'ID=opensuse-leap\nPRETTY_NAME="Lyra disposable backup test VM"\n')
    for target in ['sysinit', 'basic', 'multi-user', 'paths', 'timers', 'sockets', 'shutdown']:
        put(f'/usr/lib/systemd/system/{target}.target', f'[Unit]\nDescription=Test {target}\nDefaultDependencies=no\n')
    put('/etc/systemd/system/backup-test.target', '[Unit]\nDescription=Disposable backup qualification\nDefaultDependencies=no\nWants=basic.target sysinit.target backup-test.service\n')
    put('/etc/systemd/system/backup-test.service', '''[Unit]
Description=Backup integration tests
DefaultDependencies=no
After=basic.target sysinit.target
[Service]
Type=oneshot
ExecStart=/bin/bash /test.sh
StandardOutput=tty
StandardError=inherit
TTYPath=/dev/console
TimeoutStartSec=infinity
''')
    put('/test.sh', '''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
touch /run/vega-backup-test-vm
mkdir -p /run/dbus /home/alice/.cache /home/alice/.local/share/flatpak /run/user/1001 /var/cache/zypp /etc/zypp/repos.d /etc/vega
chmod 0755 /home/alice
systemd-tmpfiles --create /usr/lib/tmpfiles.d/query-journal.conf
systemctl start systemd-journald.service
systemctl start dbus.service
/usr/libexec/polkit-1/polkitd --no-debug > /tmp/polkit-vm.log 2>&1 &
VEGA_BACKUP_VM_TEST=1 /usr/bin/backup-tests -test.run '@TEST_PATTERN@' -test.v -test.timeout=12m
result=$?
echo "LYRA_BACKUP_VM_RESULT=$result"
systemctl --force --force poweroff
'''.replace('@TEST_PATTERN@', pattern), 0o755)
    # Optional final gate: install the real RPM, including its sysusers and
    # scriptlets, and use its units rather than the source fixture copies.
    if package := os.environ.get('VEGA_QUERY_VM_RPM'):
        shutil.copyfile(package, ROOT / 'fixtures/vegad-local.rpm')
        for name in ['passwd', 'group']:
            file = ROOT / 'etc' / name
            file.write_text(''.join(line for line in file.read_text().splitlines(True)
                                    if not line.startswith(('vegad-log:', 'vegad-query:'))))
        for name in ['vegad.service', 'vegad-log-export.service']:
            (ROOT / 'etc/systemd/system' / name).unlink()
        for name in ['vegad-update-check.timer', 'vegad-log-export.timer']:
            file = ROOT / 'etc/systemd/system' / name
            if not file.is_symlink(): file.symlink_to('/dev/null')
        script = ROOT / 'test.sh'
        script.write_text(script.read_text().replace(
            'VEGA_BACKUP_VM_TEST=1 /usr/bin/backup-tests',
            'if ! rpm -U --oldpackage --replacepkgs /fixtures/vegad-local.rpm || ! rpm -V vegad; then\n'
            '  echo LYRA_RPM_INSTALL_FAILED\n  systemctl --force --force poweroff\n  exit 1\nfi\n'
            'echo LYRA_RPM_INSTALL_VERIFIED\n'
            'VEGA_BACKUP_VM_TEST=1 /usr/bin/backup-tests'))
    put('/init', '''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
mount -t tmpfs tmpfs /run
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup
for module in loop jbd2 mbcache crc16 ext4; do insmod /$module.ko; done
exec /usr/lib/systemd/systemd --system --log-level=warning --log-target=console --unit=backup-test.target
''', 0o755)
    files = b'\0'.join(str(p.relative_to(ROOT)).encode() for p in ROOT.rglob('*')) + b'\0'
    result = subprocess.run(['cpio', '--null', '-o', '--format=newc', '--owner=0:0'], cwd=ROOT, input=files, capture_output=True, check=True)
    with gzip.open(BASE / 'initramfs.cpio.gz', 'wb', compresslevel=1) as archive:
        archive.write(result.stdout)
    print('initramfs:', (BASE / 'initramfs.cpio.gz').stat().st_size, 'bytes')


def run():
    log = BASE / 'serial.log'
    command = ['qemu-system-x86_64', '-accel', 'tcg', '-cpu', 'max', '-smp', '2', '-m', '2048',
               '-kernel', str(KERNEL), '-initrd', str(BASE / 'initramfs.cpio.gz'),
               '-append', 'rdinit=/init console=ttyS0 quiet panic=1 selinux=0 systemd.log_level=warning',
               '-display', 'none', '-serial', 'stdio', '-monitor', 'none', '-no-reboot', '-nic', 'none']
    with log.open('w') as stream:
        result = subprocess.run(command, stdout=stream, stderr=subprocess.STDOUT, timeout=1000)
    content = log.read_text()
    print(content[-22000:])
    if result.returncode or 'LYRA_BACKUP_VM_RESULT=0' not in content:
        raise SystemExit('VM validation failed; see ' + str(log))


if __name__ == '__main__':
    {'build': build, 'run': run}[sys.argv[1]]()
