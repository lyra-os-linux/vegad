#!/usr/bin/env python3
"""Execute the pinned Flatpak argv test in a marked disposable VM."""

import json
import os
import pathlib
import shutil
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'

binary = pathlib.Path('/tmp/lyra-flatpak-run-args.test')
shutil.copy2('/mnt/lyra-hostshare/lyra-flatpak-run-args.test', binary)
binary.chmod(0o755)
env = os.environ.copy()
env['LYRA_TEST_FLATPAK_RUN_ARGS'] = '1'
process = subprocess.run([str(binary), '-test.run', '^TestFlatpakRunArgsInDisposableVM$', '-test.v'],
                         env=env, capture_output=True, text=True, timeout=240)
result = {'rc': process.returncode, 'stdout': process.stdout, 'stderr': process.stderr,
          'enforcing': False, 'product_integrated': False}
pathlib.Path('/tmp/lyra-flatpak-run-args-result.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({'rc': process.returncode, 'stdout_tail': process.stdout[-2500:],
                  'stderr_tail': process.stderr[-800:]}))
assert process.returncode == 0, result
