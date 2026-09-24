#!/usr/bin/env python3
"""Stop the packaged first-update unit in a diskless VM with controlled tools."""
import importlib.util
from pathlib import Path
import re
import shutil
import sys
spec=importlib.util.spec_from_file_location('base',Path(__file__).with_name('check-preparation-vm.py'))
vm=importlib.util.module_from_spec(spec)
spec.loader.exec_module(vm)
vm.BASE=Path('/tmp/lyra-preparation-stop-vm')
vm.ROOT=vm.BASE/'root'
if sys.argv[1]=='build':
    vm.build()
    vm.binary(shutil.which('cat'),'/usr/bin/cat')
    fingerprints=re.findall(r'"([A-F0-9]{40})"', (vm.REPO/'internal/distro/trusted_keys.go').read_text())
    vm.put('/usr/bin/gpg','#!/bin/bash\n'+''.join("echo 'pub::::::::::'\necho 'fpr:::::::::"+fp+":'\n" for fp in fingerprints),0o755)
    fixture='''#!/bin/bash
mode=$(cat /run/stop-mode 2>/dev/null)
if test "$mode" = import && test "$0" = /usr/bin/rpmkeys; then
 echo ready > /run/stop-ready
 sleep 1
 touch /run/drained
elif test "$mode" = refresh && test "$0" = /usr/bin/zypper; then
 case "$*" in
 *refresh*)
 trap '' TERM
 /bin/bash -c 'trap "" TERM; while :; do sleep 1; done' &
 echo $! > /run/stubborn-child
 echo ready > /run/stop-ready
 wait
 ;;
 esac
fi
exit 0
'''
    vm.put('/usr/bin/rpmkeys',fixture,0o755)
    vm.put('/usr/bin/zypper',fixture,0o755)
    vm.put('/etc/systemd/system/vegad-first-update.service',(vm.REPO/'packaging/vegad-first-update.service').read_text())
    vm.put('/test.sh','''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
mkdir -p /run/dbus
touch /run/preparation-test-vm
systemctl start systemd-journald.service dbus.service
VEGA_PREPARATION_STOP_VM=1 /usr/bin/preparation-tests -test.run '^TestPreparationStopSystemdVM$' -test.v -test.timeout=2m
result=$?
echo "LYRA_PREPARATION_VM_RESULT=$result"
systemctl --force --force poweroff
''',0o755)
    vm.pack()
elif sys.argv[1]=='pack':
    vm.pack()
elif sys.argv[1]=='run':
    vm.run()
else:raise SystemExit('usage: build|pack|run')
