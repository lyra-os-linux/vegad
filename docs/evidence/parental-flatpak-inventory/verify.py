#!/usr/bin/env python3
"""Verify saved Flatpak inventory experiment from a disposable VM."""

import json
from pathlib import Path

here = Path(__file__).resolve().parent
result = json.loads((here / 'result.json').read_text())
apps = result['apps']
assert set(apps) == {'org.lyra.TestApp', 'org.lyra.TestAppB'}
for app, item in apps.items():
    for field in ('ref', 'commit', 'origin', 'runtime', 'location'):
        assert item[field]['rc'] == 0 and not item[field]['stderr']
    assert item['ref']['value'] == f'app/{app}/x86_64/stable'
    assert len(item['commit']['value']) == 64
    assert item['active_symlink'] and item['active_target'] == item['commit']['value']
    assert item['deployment_dir_exists']
    assert item['runtime']['value'] == 'org.lyra.TestRuntime/x86_64/stable'
    assert item['deploy_size'] > 0
assert apps['org.lyra.TestApp']['commit']['value'] != apps['org.lyra.TestAppB']['commit']['value']
assert result['runtime_commit']['rc'] == 0 and len(result['runtime_commit']['value']) == 64
assert len(result['repository']['remotes']) == 2
for remote in result['repository']['remotes'].values():
    assert remote == {'gpg-verify': 'false', 'gpg-verify-summary': 'false'}
commands = [json.loads(line) for line in
            (here / 'lyra-flatpak-inventory-20261007-commands.jsonl').read_text().splitlines()]
assert any(item['argv'] == ['systemctl', 'poweroff'] and item['result']['rc'] == 0
           for item in commands)
print('PASS: distinct full commits, unsigned fixture remotes, active links; VM powered off')
