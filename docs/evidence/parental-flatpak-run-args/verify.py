#!/usr/bin/env python3
"""Verify exact Flatpak run argv test in a disposable VM."""

import json
from pathlib import Path

here = Path(__file__).resolve().parent
result = json.loads((here / 'result.json').read_text())
assert result['rc'] == 0 and not result['stderr']
assert result['enforcing'] is False and result['product_integrated'] is False
for name, success in (('a-valid', True), ('b-valid', True),
                      ('a-with-b-commit', False), ('a-with-wrong-runtime', False)):
    assert f'--- PASS: TestFlatpakRunArgsInDisposableVM/{name}' in result['stdout']
    assert f'expected={str(success).lower()} rc-success={str(success).lower()}' in result['stdout']
commands = [json.loads(line) for line in
            (here / 'lyra-flatpak-run-args-20261007-commands.jsonl').read_text().splitlines()]
assert any(item['argv'] == ['python3', '/mnt/lyra-hostshare/run-vm.py']
           and item['result']['rc'] == 0 for item in commands)
assert any(item['argv'] == ['systemctl', 'poweroff'] and item['result']['rc'] == 0
           for item in commands)
print('PASS: server-built Flatpak argv launches exact pairs and denies wrong commits; VM powered off')
