#!/usr/bin/env python3
"""Run FD override checks as the unprivileged fixture account."""

import json
import os
import pathlib
import subprocess
import sys

assert os.getuid() == 1003
assert len(sys.argv) == 3
assert 'lyra.virtualization-test=1' in pathlib.Path('/proc/cmdline').read_text().split()
cases = []
for name, directory in (('a-files', sys.argv[1]), ('b-files', sys.argv[2])):
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        command = ['dbus-run-session', '--', '/usr/bin/flatpak', 'run', '--system',
                   '--app-fd=' + str(fd), '--command=/app/bin/b-only', 'org.lyra.TestApp']
        process = subprocess.run(command, pass_fds=(fd,), capture_output=True,
                                 text=True, timeout=45)
        cases.append({'name': name, 'rc': process.returncode,
                      'stdout_tail': process.stdout[-500:],
                      'stderr_tail': process.stderr[-1000:]})
    finally:
        os.close(fd)
print(json.dumps(cases))
