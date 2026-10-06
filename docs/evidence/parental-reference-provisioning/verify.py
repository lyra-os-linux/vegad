from pathlib import Path
import hashlib
import json

OUT = Path(__file__).resolve().parent
ROOT = OUT.parents[2]


def read(name):
    return json.loads((OUT / name).read_text())


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


m = read('manifest.json')
for name, expected in m['sha256'].items():
    assert digest(ROOT / name) == expected, name
r = read('result.json')
g = read('go-result.json')
assert r['passed'] and r['error'] is None and r['cleanup']
assert r['second_disk_unmounted'] and r['state_before'] == r['state_after']
assert r['system_state_unchanged'] and r['go'] == g
assert not r['account_mutations'] and not r['production_integrated']
for key in ('passed', 'actual_process_cuts', 'default_root_factory',
            'actual_btrfs_qualifier', 'pending_reader_refused',
            'unauthorized_resume_refused', 'wrong_operation_refused',
            'same_lock_retained', 'exact_resume_completed', 'catalog_unchanged',
            'only_canonical_entries', 'completed_loss_not_reseeded',
            'permanent_markers_retained', 'revoked_installer_proof_refused',
            'provisioning_implemented'):
    assert g[key], key
assert g['issuer_fixture'] and g['maintenance_fixture'] and g['installer_proof_fixture']
assert not g['production_integrated'] and not m['production_integrated']
assert g['cases'] == ['enrollment.json:durable', 'reference.json:durable',
                      'current.json:durable', 'enrollment-committed.json:durable']
for name in ('guard.json', 'execution.json', 'cleanup-check.json', 'poweroff.json'):
    assert read(name)['rc'] == 0, name
assert read('go-test.json')['returncode'] == 0
h = json.loads(read('source-hashes.json')['stdout'])
assert h['parental.test'] == m['guest_test_sha256']
assert h['guest.py'] == digest(OUT / 'guest.py')
v = read('vm-off.json')
assert v['vm_off'] and v['qemu_exit_observed'] and v['qemu_returncode'] == 0
assert v['backing_before'] == v['backing_after'] and len(v['backing_after']) == 3
assert v['backing_after']['/tmp/lyra-parental-identity-20260928/system.raw']['sha256'] == 'e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
command = read('virtualization-command.json')
assert 'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in command
assert 'file=/tmp/lyra-parental-provision-20261006/domain.qcow2,format=qcow2,if=none,id=domain' in command
assert read('domain-disk.json')['returncode'] == 0
assert read('system-disk.json')['returncode'] == 0
print('PASS: exact sources; root/Btrfs initial enrollment, four process cuts, exact resume, no reseed after completed loss; unchanged catalog/policies/backings, cleanup and VM OFF')
