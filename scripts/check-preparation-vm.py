#!/usr/bin/env python3
"""Qualify Preparation.GetStatus/Retry in a disposable VM with no host disks."""
import gzip
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

BASE = Path('/tmp/lyra-preparation-vm')
ROOT = BASE / 'root'
REPO = Path(__file__).resolve().parents[1]
KERNEL = Path('/usr/lib/modules/6.12.0-160099.49-default/vmlinuz')

def put(path, text, mode=0o644):
    dest = ROOT / path.lstrip('/')
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(text)
    dest.chmod(mode)

def binary(source, target=None):
    if not source:
        raise RuntimeError('missing required host executable')
    dest = ROOT / (target or source).lstrip('/')
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, dest)
    dest.chmod(0o755)
    result = subprocess.run(['ldd', source], capture_output=True, text=True)
    for soname, dependency in re.findall(r'^\s*(\S+) => (/[^\s]+)', result.stdout, re.M):
        generic = Path('/lib64') / soname
        dep = generic if generic.exists() else Path(dependency)
        library = ROOT / 'lib64' / soname
        library.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(dep, library)
    loader = ROOT / 'lib64/ld-linux-x86-64.so.2'
    loader.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile('/lib64/ld-linux-x86-64.so.2', loader)
    loader.chmod(0o755)

def build():
    if ROOT.exists():
        raise RuntimeError('use a fresh /tmp/lyra-preparation-vm/root to avoid stale VM fixtures')
    for directory in ['dev', 'proc', 'sys', 'run', 'tmp', 'root', 'home/alice', 'var/lib/polkit', 'var/lib/vega', 'etc/polkit-1/rules.d', 'usr/share/polkit-1/rules.d', 'usr/lib/systemd/system']:
        (ROOT / directory).mkdir(parents=True, exist_ok=True)
    (ROOT / 'tmp').chmod(0o1777)
    for name in ['bash', 'mount', 'mkdir', 'touch', 'sleep', 'systemctl', 'systemd-run', 'pkcheck', 'dbus-daemon', 'env']:
        binary(shutil.which(name), '/usr/bin/' + name)
    for source in ['/usr/lib/systemd/systemd', '/usr/lib/systemd/systemd-executor', '/usr/lib/systemd/systemd-shutdown', '/usr/lib/systemd/systemd-journald', '/usr/libexec/polkit-1/polkitd', '/usr/lib64/libnss_systemd.so.2']:
        binary(source)
    binary(str(BASE / 'vegad'), '/usr/lib/vega/vegad')
    binary(str(BASE / 'tests'), '/usr/bin/preparation-tests')
    (ROOT / 'bin').symlink_to('usr/bin')
    (ROOT / 'usr/bin/sh').symlink_to('bash')
    put('/etc/passwd', 'root:x:0:0:root:/root:/bin/bash\nalice:x:1001:1001:alice:/home/alice:/bin/bash\npolkitd:x:999:999:polkit:/var/lib/polkit:/bin/false\nvegad-query:x:997:997:query:/var/empty:/bin/false\n')
    put('/etc/group', 'root:x:0:\nalice:x:1001:\npolkitd:x:999:\nvegad-query:x:997:\n')
    put('/etc/nsswitch.conf', 'passwd: files\ngroup: files\nshadow: files\n')
    put('/etc/os-release', 'ID=opensuse-leap\nPRETTY_NAME="Lyra disposable preparation VM"\n')
    put('/usr/lib/lyra-os/release', 'test-vm\n')
    put('/usr/share/polkit-1/actions/org.lyraos.vega.policy', (REPO / 'packaging/org.lyraos.vega.policy').read_text())
    put('/etc/dbus-1/system.conf', '<busconfig><type>system</type><listen>unix:path=/run/dbus/system_bus_socket</listen><auth>EXTERNAL</auth><policy context="default"><allow user="*"/><allow own="*"/><allow send_destination="*"/><allow receive_sender="*"/></policy></busconfig>')
    put('/etc/systemd/system/dbus.socket', '[Unit]\nDefaultDependencies=no\n[Socket]\nListenStream=/run/dbus/system_bus_socket\n')
    put('/etc/systemd/system/dbus.service', '[Unit]\nDefaultDependencies=no\nRequires=dbus.socket\nAfter=dbus.socket\n[Service]\nType=notify\nExecStart=/usr/bin/dbus-daemon --config-file=/etc/dbus-1/system.conf --address=systemd: --nofork --nopidfile --systemd-activation\n')
    put('/etc/systemd/system/polkit.service', '[Unit]\nAfter=dbus.service\nRequires=dbus.service\n[Service]\nType=dbus\nBusName=org.freedesktop.PolicyKit1\nExecStart=/usr/libexec/polkit-1/polkitd --no-debug\n')
    put('/etc/systemd/system/vegad.service', '[Unit]\nAfter=dbus.service\nRequires=dbus.service\n[Service]\nType=dbus\nBusName=org.lyraos.Vega1\nEnvironment=VEGAD_PROFILE=desktop\nExecStart=/usr/lib/vega/vegad\nStandardOutput=tty\nStandardError=inherit\nTTYPath=/dev/console\n')
    # A controlled job exercises the real retry/start semantics without touching
    # repositories or installing packages. Its PID detects accidental restarts.
    put('/etc/systemd/system/vegad-first-update.service', '[Service]\nType=exec\nExecStart=/bin/bash -c "touch /run/preparation-started; sleep 120"\n')
    for unit in ['systemd-journald.service', 'systemd-journald.socket', 'systemd-journald-dev-log.socket']:
        put('/etc/systemd/system/' + unit, Path('/usr/lib/systemd/system', unit).read_text())
    for target in ['sysinit', 'basic', 'multi-user', 'paths', 'timers', 'sockets', 'shutdown']:
        put('/usr/lib/systemd/system/' + target + '.target', '[Unit]\nDefaultDependencies=no\n')
    put('/etc/systemd/system/preparation-test.target', '[Unit]\nDefaultDependencies=no\nWants=basic.target sysinit.target preparation-test.service\n')
    put('/etc/systemd/system/preparation-test.service', '[Unit]\nDefaultDependencies=no\nAfter=basic.target sysinit.target\n[Service]\nType=oneshot\nExecStart=/bin/bash /test.sh\nStandardOutput=tty\nStandardError=inherit\nTTYPath=/dev/console\nTimeoutStartSec=infinity\n')
    put('/test.sh', '''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
mkdir -p /run/dbus /run/user/1001
touch /run/preparation-test-vm
systemctl start systemd-journald.service dbus.service
VEGA_PREPARATION_VM=1 /usr/bin/preparation-tests -test.run '^TestPreparationSystemdVM$' -test.v -test.timeout=3m
result=$?
echo "LYRA_PREPARATION_VM_RESULT=$result"
systemctl --force --force poweroff
''', 0o755)
    put('/init', '''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
mount -t tmpfs tmpfs /run
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup
exec /usr/lib/systemd/systemd --system --log-level=warning --log-target=console --unit=preparation-test.target
''', 0o755)
    pack()

def pack():
    runtime = ROOT / 'var/run'
    if not runtime.is_symlink(): runtime.symlink_to('/run')
    put('/etc/systemd/system/vegad.service', '[Unit]\nAfter=dbus.service\nRequires=dbus.service\n[Service]\nType=dbus\nBusName=org.lyraos.Vega1\nEnvironment=VEGAD_PROFILE=desktop\nExecStart=/usr/lib/vega/vegad\nStandardOutput=tty\nStandardError=inherit\nTTYPath=/dev/console\n')
    binary(str(BASE / 'vegad'), '/usr/lib/vega/vegad')
    binary(str(BASE / 'tests'), '/usr/bin/preparation-tests')
    put('/etc/systemd/system/dbus.socket', '[Unit]\nDefaultDependencies=no\n[Socket]\nListenStream=/run/dbus/system_bus_socket\n')
    put('/etc/systemd/system/dbus.service', '[Unit]\nDefaultDependencies=no\nRequires=dbus.socket\nAfter=dbus.socket\n[Service]\nType=notify\nExecStart=/usr/bin/dbus-daemon --config-file=/etc/dbus-1/system.conf --address=systemd: --nofork --nopidfile --systemd-activation\n')
    (ROOT / 'lib64/ld-linux-x86-64.so.2').chmod(0o755)
    files = b'\0'.join(str(p.relative_to(ROOT)).encode() for p in ROOT.rglob('*')) + b'\0'
    archive = subprocess.run(['cpio', '--null', '-o', '--format=newc', '--owner=0:0'], cwd=ROOT, input=files, capture_output=True, check=True)
    with gzip.open(BASE / 'initramfs.cpio.gz', 'wb', compresslevel=1) as stream:
        stream.write(archive.stdout)

def run():
    command = ['qemu-system-x86_64', '-accel', 'tcg', '-cpu', 'max', '-smp', '2', '-m', '1536', '-kernel', str(KERNEL), '-initrd', str(BASE / 'initramfs.cpio.gz'), '-append', 'rdinit=/init console=ttyS0 quiet panic=1 selinux=0', '-display', 'none', '-serial', 'stdio', '-monitor', 'none', '-no-reboot', '-nic', 'none']
    log = BASE / 'serial.log'
    with log.open('w') as stream:
        result = subprocess.run(command, stdout=stream, stderr=subprocess.STDOUT, timeout=300)
    text = log.read_text()
    print(text[-16000:])
    if result.returncode or 'LYRA_PREPARATION_VM_RESULT=0' not in text:
        raise SystemExit('VM validation failed: ' + str(log))

if __name__ == '__main__':
    {'build': build, 'pack': pack, 'run': run}[sys.argv[1]]()
