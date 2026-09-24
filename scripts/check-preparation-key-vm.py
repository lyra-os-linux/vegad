#!/usr/bin/env python3
"""Build/run the persistent key approval fixture inside a diskless VM."""
import importlib.util
from pathlib import Path
import shutil
import sys
spec = importlib.util.spec_from_file_location('base', Path(__file__).with_name('check-preparation-vm.py'))
vm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vm)
vm.BASE = Path('/tmp/lyra-preparation-key-vm')
vm.ROOT = vm.BASE / 'root'
if sys.argv[1] == 'build':
    vm.build()
    vm.binary(shutil.which('cat'), '/usr/bin/cat')
    vm.binary(shutil.which('script'), '/usr/bin/script')
    vm.put('/usr/bin/zypper', '''#!/bin/bash
case "$*" in
 *repos*)
  source=one
  test ! -e /run/source-changed || source=two
  echo "<stream><repo-list><repo alias=\\"fixture\\" name=\\"Fixture\\" enabled=\\"1\\" gpgcheck=\\"1\\"><url>https://example.invalid/$source</url></repo></repo-list></stream>"
  ;;
 *--xmlout*refresh*)
  echo '<stream><gpgkey-info><repository>Fixture</repository><key-name>Fixture signer</key-name><key-fingerprint>0123456789ABCDEF0123456789ABCDEF01234567</key-fingerprint></gpgkey-info><prompt id="14"><description>Trust</description><option value="r"/><option value="t"/><option value="a"/></prompt>'
  read -r answer
  if test "$answer" != a; then exit 1; fi
  touch /run/key-imported
  echo '</stream>'
  ;;
 *refresh*)
  echo 'Repository: Fixture'
  echo 'Key Name: Fixture signer'
  echo 'Key Fingerprint: 0123456789ABCDEF0123456789ABCDEF01234567'
  exit 1
  ;;
 *) exit 1;;
esac
''', 0o755)
    vm.put('/test.sh', '''#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/bin
mkdir -p /run/dbus /run/user/1001 /dev/pts
mount -t devpts devpts /dev/pts
touch /run/preparation-test-vm
systemctl start systemd-journald.service dbus.service
VEGA_PREPARATION_KEY_VM=1 /usr/bin/preparation-tests -test.run '^TestPreparationKeySystemdVM$' -test.v -test.timeout=2m
result=$?
echo "LYRA_PREPARATION_VM_RESULT=$result"
systemctl --force --force poweroff
''', 0o755)
    vm.pack()
elif sys.argv[1] == 'pack':
    vm.pack()
elif sys.argv[1] == 'run':
    vm.run()
else:
    raise SystemExit('usage: build|pack|run')
