#!/usr/bin/env python3
"""Read-only Flatpak metadata probe for a marked disposable Lyra VM."""

import json
import pathlib
import subprocess
import configparser

cmdline = pathlib.Path('/proc/cmdline').read_text().split()
assert 'lyra.virtualization-test=1' in cmdline
assert pathlib.Path('/sys/block/vda/serial').read_text().strip() == 'lyra-admission-test'

result = {'apps': {}, 'repository': {}}
for app in ('org.lyra.TestApp', 'org.lyra.TestAppB'):
    item = {}
    for option in ('--show-ref', '--show-commit', '--show-origin', '--show-runtime', '--show-location'):
        process = subprocess.run(['flatpak', 'info', '--system', option, app],
                                 capture_output=True, text=True, timeout=20)
        item[option[7:]] = {'rc': process.returncode, 'value': process.stdout.strip(),
                            'stderr': process.stderr.strip()}
    location = pathlib.Path(item['location']['value'])
    item['deployment_dir_exists'] = location.is_dir()
    item['deployment_files'] = sorted(p.name for p in location.iterdir()) if location.is_dir() else []
    active = location.parent / 'active'
    item['active_symlink'] = active.is_symlink()
    item['active_target'] = active.readlink().as_posix() if active.is_symlink() else ''
    deploy = location / 'deploy'
    if deploy.is_file():
        item['deploy_size'] = deploy.stat().st_size
        item['deploy_first_bytes_hex'] = deploy.read_bytes()[:8].hex()
    result['apps'][app] = item

runtime = subprocess.run(['flatpak', 'info', '--system', '--show-commit',
                          'org.lyra.TestRuntime'], capture_output=True, text=True, timeout=20)
result['runtime_commit'] = {'rc': runtime.returncode, 'value': runtime.stdout.strip()}

repo = pathlib.Path('/var/lib/flatpak/repo/config')
if repo.is_file():
    text = repo.read_text(errors='replace')
    result['repository']['section_names'] = [line for line in text.splitlines() if line.startswith('[')]
    result['repository']['field_names'] = [line.split('=', 1)[0] for line in text.splitlines()
                                           if '=' in line and not line.startswith('#')]
    config = configparser.ConfigParser()
    config.read_string(text)
    result['repository']['remotes'] = {
        section: {'gpg-verify': config.get(section, 'gpg-verify', fallback='missing'),
                  'gpg-verify-summary': config.get(section, 'gpg-verify-summary', fallback='missing')}
        for section in config.sections() if section.startswith('remote ')
    }

print(json.dumps(result, indent=2))
