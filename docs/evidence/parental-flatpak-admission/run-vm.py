#!/usr/bin/env python3
"""Run the Go peer-credential test only in a marked disposable VM."""

import json
import pathlib
import shutil
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'
assert pathlib.Path('/usr/bin/setpriv').is_file()

binary = pathlib.Path('/tmp/lyra-flatpak-admission.test')
shutil.copy2('/mnt/lyra-hostshare/lyra-flatpak-admission.test', binary)
binary.chmod(0o755)
subprocess.run(['setenforce', '1'], check=True)
try:
    process = subprocess.run([str(binary), '-test.run', '^TestFlatpakLaunchPeerInDisposableVM$',
                              '-test.v'], capture_output=True, text=True, timeout=60)
    result = {'rc': process.returncode, 'stdout': process.stdout,
              'stderr': process.stderr, 'enforcing': True,
              'product_integrated': False}
    pathlib.Path('/tmp/lyra-flatpak-admission-result.json').write_text(
        json.dumps(result, indent=2) + '\n')
    print(json.dumps({'rc': process.returncode, 'stdout': process.stdout[-1500:],
                      'stderr': process.stderr[-500:]}))
    assert process.returncode == 0, result
finally:
    subprocess.run(['setenforce', '0'], check=True)
