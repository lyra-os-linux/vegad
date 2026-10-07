#!/usr/bin/env python3
"""Check the saved SELinux/Unix peer admission VM evidence."""

import json
from pathlib import Path

here = Path(__file__).resolve().parent
result = json.loads((here / 'result.json').read_text())
assert result['rc'] == 0
assert result['enforcing'] is True
assert result['product_integrated'] is False
assert 'kernel UID=1003 SELinux=system_u:system_r:unconfined_service_t:s0' in result['stdout']
assert '--- PASS: TestFlatpakLaunchPeerInDisposableVM/valid' in result['stdout']
assert '--- PASS: TestFlatpakLaunchPeerInDisposableVM/spoof' in result['stdout']
assert not result['stderr']

commands = [json.loads(line) for line in
            (here / 'lyra-flatpak-peer-admission-20261007-commands.jsonl').read_text().splitlines()]
assert any(item['argv'] == ['python3', '/mnt/lyra-hostshare/run-vm.py']
           and item['result']['rc'] == 0 for item in commands)
assert any(item['argv'] == ['getenforce']
           and item['result']['stdout'].strip() == 'Permissive' for item in commands)
assert any(item['argv'] == ['systemctl', 'poweroff']
           and item['result']['rc'] == 0 for item in commands)

supervised = json.loads((here / 'supervised-result.json').read_text())
assert supervised['rc'] == 0
assert supervised['enforcing'] is True
assert supervised['product_integrated'] is False
assert not supervised['stderr']
for suffix, uid in (('a', 1003), ('b', 1011)):
    context = f'system_u:system_r:lyra_test_child_{suffix}_t:s0'
    for mode in ('valid', 'spoof'):
        assert f'UID={uid} SELinux={context} mode={mode}' in supervised['stdout']
        assert f'--- PASS: TestSupervisedFlatpakLaunchPeerInDisposableVM/{suffix}-{mode}' in supervised['stdout']

supervised_commands = [json.loads(line) for line in
    (here / 'lyra-flatpak-supervised-peer-20261007-commands.jsonl').read_text().splitlines()]
assert any(item['argv'] == ['python3', '/mnt/lyra-hostshare/run-supervised-vm.py']
           and item['result']['rc'] == 0 for item in supervised_commands)
assert any(item['argv'] == ['getenforce']
           and item['result']['stdout'].strip() == 'Permissive' for item in supervised_commands)
assert any(item['argv'] == ['systemctl', 'poweroff']
           and item['result']['rc'] == 0 for item in supervised_commands)
print('PASS: kernel peers in two SELinux domains; forged UIDs rejected; VMs restored and powered off')
