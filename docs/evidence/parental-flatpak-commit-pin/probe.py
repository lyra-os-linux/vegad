#!/usr/bin/env python3
"""Test explicit app/runtime commit selection in a disposable Lyra VM."""

import json
import os
import pathlib
import pwd
import subprocess

assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'
assert pathlib.Path('/sys/fs/selinux/enforce').read_text().strip() == '0'

account = pwd.getpwnam('parentaltest')
runtime_dir = pathlib.Path(f'/run/user/{account.pw_uid}')
runtime_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
os.chown(runtime_dir, account.pw_uid, account.pw_gid)
runtime_dir.chmod(0o700)


def info(app, option):
    result = subprocess.run(['flatpak', 'info', '--system', option, app],
                            check=True, capture_output=True, text=True, timeout=20)
    return result.stdout.strip()


app_a = info('org.lyra.TestApp', '--show-commit')
app_b = info('org.lyra.TestAppB', '--show-commit')
runtime = info('org.lyra.TestRuntime', '--show-commit')
assert len(app_a) == len(app_b) == len(runtime) == 64 and app_a != app_b


def run(name, app, commit, runtime_commit):
    args = ['runuser', '-u', 'parentaltest', '--', 'env',
            'XDG_RUNTIME_DIR=/run/user/1003', 'dbus-run-session', '--',
            '/usr/bin/flatpak', 'run', '--system', '--commit=' + commit,
            '--runtime-commit=' + runtime_commit, app]
    process = subprocess.run(args, capture_output=True, text=True, timeout=45)
    return {'name': name, 'app': app, 'commit': commit, 'runtime_commit': runtime_commit,
            'rc': process.returncode, 'stdout': process.stdout[-600:],
            'stderr_tail': process.stderr[-1200:]}


cases = [
    run('a-correct', 'org.lyra.TestApp', app_a, runtime),
    run('b-correct', 'org.lyra.TestAppB', app_b, runtime),
    run('a-with-b-commit', 'org.lyra.TestApp', app_b, runtime),
    run('a-with-wrong-runtime', 'org.lyra.TestApp', app_a, '0' * 64),
]
result = {'app_a_commit': app_a, 'app_b_commit': app_b, 'runtime_commit': runtime,
          'cases': cases, 'enforcing': False, 'product_integrated': False}
pathlib.Path('/tmp/lyra-flatpak-commit-pin-result.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({'cases': [{key: case[key] for key in ('name', 'rc')} for case in cases]}))
assert [case['rc'] == 0 for case in cases] == [True, True, False, False]
