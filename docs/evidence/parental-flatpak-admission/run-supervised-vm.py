#!/usr/bin/env python3
"""Run two SELinux child-domain socket probes in a marked disposable VM."""

import json
import os
import pathlib
import shutil
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'

binary = pathlib.Path('/tmp/lyra-flatpak-supervised-peer.test')
shutil.copy2('/mnt/lyra-hostshare/lyra-flatpak-supervised-peer.test', binary)
binary.chmod(0o755)
environment = os.environ.copy()
environment['LYRA_TEST_SUPERVISED_PEER'] = '1'
subprocess.run(['setenforce', '1'], check=True)
try:
    process = subprocess.run([str(binary), '-test.run',
                              '^TestSupervisedFlatpakLaunchPeerInDisposableVM$', '-test.v'],
                             env=environment, capture_output=True, text=True, timeout=75)
    result = {'rc': process.returncode, 'stdout': process.stdout,
              'stderr': process.stderr, 'enforcing': True,
              'product_integrated': False}
    pathlib.Path('/tmp/lyra-flatpak-supervised-peer-result.json').write_text(
        json.dumps(result, indent=2) + '\n')
    print(json.dumps({'rc': process.returncode,
                      'stdout_tail': process.stdout[-2500:],
                      'stderr_tail': process.stderr[-800:]}))
    assert process.returncode == 0, result
finally:
    subprocess.run(['setenforce', '0'], check=True)
