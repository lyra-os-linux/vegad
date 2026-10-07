#!/usr/bin/env python3
"""Install only disposable child-domain peer fixtures in a marked VM."""

import pathlib
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'
assert 'lyra-two-accounts' in subprocess.check_output(['semodule', '-l'], text=True)

share = pathlib.Path('/mnt/lyra-hostshare')
target = pathlib.Path('/opt/lyra-parental-probe')
subprocess.run(['semodule', '-i', str(share / 'supervised-peer.cil')], check=True)
for suffix in ('a', 'b'):
    client = target / f'peer-client-{suffix}'
    subprocess.run(['gcc', '-Wall', '-Wextra', '-Werror', '-O2',
                    str(share / 'supervised-peer-client.c'), '-o', str(client)], check=True)
    subprocess.run(['chcon', '-t', f'lyra_test_app_{suffix}_exec_t', str(client)], check=True)
    print(subprocess.check_output(['ls', '-Z', str(client)], text=True).strip())
print('READY: separate client labels and narrow socket fixture')
