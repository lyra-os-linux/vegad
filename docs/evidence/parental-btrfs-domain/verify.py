"""Verify receipts and exact source/driver hashes; does not launch a VM."""
from pathlib import Path
import hashlib,json
OUT=Path(__file__).resolve().parent
ROOT=OUT.parents[2]
def read(name):return json.loads((OUT/name).read_text())
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
m=read('manifest.json')
for name,want in m['sha256'].items():
 p=ROOT/name
 assert p.is_file() and digest(p)==want, name
execution=read('execution.json');assert execution['rc']==0
result=read('result.json');go=read('go-result.json')
assert result['passed'] and result['cleanup'] and result['second_disk_unmounted']
assert result['state_before']==result['state_after'] and result['system_state_unchanged']
assert not result['account_mutations'] and not result['production_integrated']
for key in ('passed','actual_btrfs_snapshot','catalog_rolled_back','authority_current_preserved','negative_recovery_diagnosis','same_subvolume_refused','nested_catalog_refused','readonly_scope_refused','readonly_authority_refused','old_reference_refused','missing_pin_refused','default_root_source','issuer_fixture'):
 assert go[key] is True,key
assert not go['production_integrated'] and go['scope']=='single-subvolume-only'
assert go['before_witness']!=go['after_witness']
assert result['go']==go
assert read('go-test.json')['returncode']==0
assert read('cleanup-check.json')['rc']==0 and read('poweroff.json')['rc']==0
vm=read('vm-off.json')
assert vm['vm_off'] and vm['qemu_returncode']==0
assert vm['backing_before']==vm['backing_after']
assert vm['backing_after']['sha256']=='e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
command=read('virtualization-command.json')
assert 'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in command
assert any(x.endswith('/domain.qcow2,format=qcow2,if=none,id=domain') for x in command)
hashes=json.loads(read('source-hashes.json')['stdout'])
assert hashes['guest.py']==digest(OUT/'guest.py') and hashes['parental.test']==m['guest_test_sha256']
assert read('guard.json')['rc']==0 and read('resume-guard.json')['rc']==0
assert read('packages-verified.json')['rc']==0 and read('packages-verified-initial.json')['rc']==0
assert read('guest-prerequisites-initial.json')['rc']==0 and read('btrfs-version.json')['rc']==0
packages=read('packages.json')
actual=json.loads(read('packages-verified.json')['stdout'].splitlines()[-1])
assert {n:v['sha256'] for n,v in packages.items()}==actual
assert all(not r['passed'] for r in read('prior-attempts.json'))
print('PASS: exact sources, real single-subvolume rollback, independent current reference, negative gates, cleanup/state/backing and VM OFF')
