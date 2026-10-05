from pathlib import Path
import hashlib,json,re
E=Path(__file__).resolve().parent;ROOT=E.parents[2]
def read(name):return json.loads((E/name).read_text())
m=read('manifest.json')
for name,digest in m['files'].items():
 assert '..' not in Path(name).parts
 assert hashlib.sha256((E/name).read_bytes()).hexdigest()==digest,name
for name,digest in m['sources'].items():assert hashlib.sha256((ROOT/name).read_bytes()).hexdigest()==digest,name
r=read('result.json');g=r['go']
assert r['passed'] is r['restored'] is r['cleanup'] is r['system_state_unchanged'] is True and r['error'] is None
assert r['state_before']==r['state_after']
assert r['account_mutations'] is r['production_integrated'] is False
assert g['passed'] is g['default_root_writer'] is g['real_process_contention'] is True
assert all(g[k] is False for k in ('account_mutations','bootstrap','positive_authority_recovery','production_integrated'))
assert [c['case'] for c in g['cases']]==['two-process-contention','file-synced','renamed','directory-synced']
c=g['cases'][0]
assert len(c['pids'])==2 and len(set(c['pids']))==2 and all(type(p) is int and p>1 for p in c['pids'])
assert all(c[k] is True for k in ('reader_busy','lock_inode_preserved','peers_preserved','root_metadata'))
assert re.fullmatch('[0-9a-f]{64}',c['same_expected_digest'])
assert c['winner']['code']=='Committed' and c['winner']['revision']==2 and c['winner']['digest']!=c['same_expected_digest']
assert re.fullmatch('[0-9a-f]{64}',c['winner']['digest'])
for key,code in [('loser','Busy'),('retry','Conflict')]:assert c[key]==dict(stage='result',code=code,digest='',revision=0)
for c in g['cases'][1:]:
 assert c['exit_code']==19 and c['peers_preserved'] is c['root_metadata'] is c['no_positive_recovery'] is True
 assert c['before_rename']==(c['case']=='file-synced') and c['orphan_temporary']==(1 if c['before_rename'] else 0)
assert read('go-test.json')['returncode']==read('execution.json')['rc']==0
assert read('cleanup-check.json')['rc']==read('poweroff.json')['rc']==read('guard.json')['rc']==0
assert json.loads(read('source-hashes.json')['stdout'])['parental.test']==m['binary_sha256']
off=read('vm-off.json');assert off['vm_off'] is True and off['qemu_returncode']==0 and off['backing_before']==off['backing_after']
disk=read('disk.json');assert disk['filename']==off['same_disk'] and disk['backing-filename-format']=='raw'
print('PASS: default-root writer, two actual processes Busy/Conflict, reader Busy, 3 process cuts, peers preserved; state unchanged, fixtures removed, backing unchanged, VM OFF. No bootstrap/admission recovery qualification.')
