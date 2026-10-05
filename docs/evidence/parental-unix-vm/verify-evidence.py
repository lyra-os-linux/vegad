from pathlib import Path
import hashlib,json
E=Path(__file__).resolve().parent;ROOT=E.parents[2]
def read(name):return json.loads((E/name).read_text())
m=read('manifest.json')
for name,digest in m['files'].items():
 assert '..' not in Path(name).parts
 assert hashlib.sha256((E/name).read_bytes()).hexdigest()==digest
for name,digest in m['sources'].items():assert hashlib.sha256((ROOT/name).read_bytes()).hexdigest()==digest
r=read('result.json');g=r['go']
assert r['passed'] is r['restored'] is r['cleanup'] is True and r['error'] is None
assert r['account_hashes_before']==r['account_hashes_after'] and r['state_before']==r['state_after']
assert r['real_polkit'] is r['production_integrated'] is False
assert all(g[k] is True for k in ('passed','identity_preserved','peer_preserved','real_unix_effects','root_owned_backend'))
assert g['authoritative_pam_proof'] is g['real_polkit'] is False
assert len(g['checks'])==len(set(g['checks']))==7 and g['noops']==2
assert g['commands']==[
 dict(path='/usr/sbin/usermod',argv=['-c','Lyra Go $(literal)','-G','users','--','ordinaryuser'],success=True),
 dict(path='/usr/sbin/usermod',argv=['-aG','wheel','--','ordinaryuser'],success=True),
 dict(path='/usr/bin/gpasswd',argv=['-d','ordinaryuser','wheel'],success=True)]
assert read('go-test.json')['returncode']==read('execution.json')['rc']==0
assert read('cleanup-check.json')['rc']==read('poweroff.json')['rc']==read('guard.json')['rc']==0
assert json.loads(read('source-hashes.json')['stdout'])['parental.test']==m['binary_sha256']
off=read('vm-off.json');assert off['vm_off'] is True and off['qemu_returncode']==0 and off['backing_before']==off['backing_after']
assert read('disk.json')['filename']==off['same_disk']
failed=read('failed-identity-setup/result.json');assert failed['passed'] is False and failed['go'] is None and failed['restored'] is failed['cleanup'] is True
assert read('failed-identity-setup/go-test.json')['returncode']==1
print('PASS: Go guard + root-owned real file leases + three Unix commands/two no-ops; restored, cleaned, backing unchanged, VM OFF; failed setup preserved. No real Polkit/PAM/product claim.')
