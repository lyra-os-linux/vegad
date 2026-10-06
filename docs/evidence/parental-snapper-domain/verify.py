from pathlib import Path
import hashlib,json
OUT=Path(__file__).resolve().parent;ROOT=OUT.parents[2]
def read(n):return json.loads((OUT/n).read_text())
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
m=read('manifest.json')
for name,want in m['sha256'].items():assert digest(ROOT/name)==want,name
r=read('result.json');g=read('go-result.json')
assert r['passed'] and r['cleanup'] and r['second_disk_unmounted']
assert r['state_before']==r['state_after'] and r['system_state_unchanged']
assert not r['account_mutations'] and not r['production_integrated'] and r['go']==g
for k in ('passed','actual_btrfs_snapshot','snapper_create','snapper_config_bound','catalog_rolled_back','authority_current_preserved','negative_recovery_diagnosis','same_subvolume_refused','nested_catalog_refused','readonly_scope_refused','readonly_authority_refused','old_reference_refused','missing_pin_refused','default_root_source'):
 assert g[k],k
assert not g['boot_rollback_qualified'] and not g['production_integrated'] and g['issuer_fixture']
assert g['scope']=='single-subvolume-only' and g['before_witness']!=g['after_witness']
for n in ('prerequisites.json','guard.json','execution.json','cleanup-check.json','poweroff.json'):assert read(n)['rc']==0,n
assert read('go-test.json')['returncode']==0
hashes=json.loads(read('source-hashes.json')['stdout']);assert hashes['parental.test']==m['guest_test_sha256'] and hashes['guest.py']==digest(OUT/'guest.py')
v=read('vm-off.json');assert v['vm_off'] and v['qemu_returncode']==0 and v['backing_before']==v['backing_after']
assert v['backing_after']['/tmp/lyra-parental-identity-20260928/system.raw']['sha256']=='e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
assert 'snapper 0.12.1' in read('snapper-version.json')['stdout']
assert 'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in read('virtualization-command.json')
print('PASS: exact sources, actual Snapper creation/config-bound source, single-subvolume restore, negative gates, state/cleanup/backings and VM OFF; boot qualification remains pending')
