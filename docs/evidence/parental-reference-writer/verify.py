from pathlib import Path
import hashlib,json
OUT=Path(__file__).resolve().parent;ROOT=OUT.parents[2]
def read(name):return json.loads((OUT/name).read_text())
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
m=read('manifest.json')
for name,want in m['sha256'].items():assert digest(ROOT/name)==want,name
r=read('result.json');g=read('go-result.json')
assert r['passed'] and r['cleanup'] and r['second_disk_unmounted'] and r['state_before']==r['state_after'] and r['system_state_unchanged']
assert r['go']==g and not r['account_mutations'] and not r['production_integrated']
for k in ('passed','actual_process_cuts','default_root_factory','actual_btrfs_qualifier','pending_reader_refused','unauthorized_resume_refused','wrong_operation_refused','same_lock_retained','exact_resume_completed','catalog_unchanged','journal_and_temps_cleared'):assert g[k],k
assert g['issuer_fixture'] and g['maintenance_fixture'] and not g['production_integrated'] and not g['provisioning_implemented']
assert g['cases']==['.reference-transaction.json:durable','reference.json:durable','current.json:durable']
for n in ('guard.json','execution.json','cleanup-check.json','poweroff.json'):assert read(n)['rc']==0,n
assert read('go-test.json')['returncode']==0
h=json.loads(read('source-hashes.json')['stdout']);assert h['parental.test']==m['guest_test_sha256'] and h['guest.py']==digest(OUT/'guest.py')
v=read('vm-off.json');assert v['vm_off'] and v['qemu_returncode']==0 and v['backing_before']==v['backing_after'] and len(v['backing_after'])==3
assert v['backing_after']['/tmp/lyra-parental-identity-20260928/system.raw']['sha256']=='e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
assert 'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in read('virtualization-command.json')
assert not read('prior-attempt.json')['passed']
print('PASS: exact sources, root writer/real process cuts/Btrfs qualification, pending refuses, exact resume, lock/catalog preserved, cleanup/state/backing chain and VM OFF')
