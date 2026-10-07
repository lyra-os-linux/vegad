#!/usr/bin/env python3
"""Show whether app-fd is bound to the requested app ID in a disposable VM."""

import json
import pathlib
import shutil
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'


def location(app):
    process = subprocess.run(['flatpak', 'info', '--system', '--show-location', app],
                             check=True, capture_output=True, text=True)
    return pathlib.Path(process.stdout.strip()) / 'files'


a, b = location('org.lyra.TestApp'), location('org.lyra.TestAppB')
marker = b / 'bin/b-only'
assert not (a / 'bin/b-only').exists() and not marker.exists()
shutil.copy2(b / 'bin/probe', marker)
try:
    process = subprocess.run(['runuser', '-u', 'parentaltest', '--', 'python3',
                              '/mnt/lyra-hostshare/fd-child.py', str(a), str(b)],
                             capture_output=True, text=True, timeout=100)
    assert process.returncode == 0, process.stderr
    cases = json.loads(process.stdout)
    result = {'cases': cases, 'requested_app': 'org.lyra.TestApp',
              'a_files': str(a), 'b_files': str(b),
              'enforcing': False, 'product_integrated': False}
    pathlib.Path('/tmp/lyra-flatpak-fd-result.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({'cases': [{key: case[key] for key in ('name', 'rc')} for case in cases]}))
    assert [case['rc'] == 0 for case in cases] == [False, True]
finally:
    marker.unlink()
