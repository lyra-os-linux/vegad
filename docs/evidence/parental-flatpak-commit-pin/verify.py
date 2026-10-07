#!/usr/bin/env python3
"""Verify Flatpak commit pin and app-fd mismatch evidence."""

import json
from pathlib import Path

here = Path(__file__).resolve().parent
commit = json.loads((here / 'commit-result.json').read_text())
fd = json.loads((here / 'fd-result.json').read_text())
assert commit['enforcing'] is False and commit['product_integrated'] is False
assert fd['enforcing'] is False and fd['product_integrated'] is False
assert len(commit['app_a_commit']) == len(commit['app_b_commit']) == len(commit['runtime_commit']) == 64
assert commit['app_a_commit'] != commit['app_b_commit']
assert [(case['name'], case['rc'] == 0) for case in commit['cases']] == [
    ('a-correct', True), ('b-correct', True),
    ('a-with-b-commit', False), ('a-with-wrong-runtime', False)]
assert 'not installed' in commit['cases'][2]['stderr_tail']
assert 'not installed' in commit['cases'][3]['stderr_tail']
assert fd['requested_app'] == 'org.lyra.TestApp'
assert [(case['name'], case['rc'] == 0) for case in fd['cases']] == [
    ('a-files', False), ('b-files', True)]
assert 'No such file or directory' in fd['cases'][0]['stderr_tail']
assert '/org.lyra.TestAppB/' in fd['b_files']
commands = [json.loads(line) for line in
            (here / 'lyra-flatpak-commit-pin-20261007-commands.jsonl').read_text().splitlines()]
assert any(item['argv'] == ['python3', '/mnt/lyra-hostshare/probe.py']
           and item['result']['rc'] == 0 for item in commands)
assert any(item['argv'] == ['python3', '/mnt/lyra-hostshare/fd-probe.py']
           and item['result']['rc'] == 0 for item in commands)
assert any(item['argv'] == ['systemctl', 'poweroff'] and item['result']['rc'] == 0
           for item in commands)
print('PASS: explicit commit pair selected, wrong commits denied; foreign app-fd executes')
