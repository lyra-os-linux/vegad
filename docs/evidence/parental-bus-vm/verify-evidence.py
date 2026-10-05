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
assert r['passed'] is r['restored'] is r['cleanup'] is True and r['error'] is None
assert r['account_hashes_before']==r['account_hashes_after'] and r['state_before']==r['state_after'] and r['configs_before']==r['configs_after']
assert r['real_polkit'] is True and r['production_integrated'] is False
assert all(g[k] is True for k in ('passed','real_dbus','real_polkit','real_unix_effects','root_owned_backend'))
assert all(g[k] is False for k in ('authoritative_pam_proof','guardian_binding','password_authentication','production_integrated'))
expected=['profile','consumed','noop-grant','remove','noop-remove','deny','cancel','revoke','catalog-change','barrier','replay','unauthorized','invalid']
assert [c['case'] for c in g['cases']]==expected
all_commands=[]
for c in g['cases']:
 mode=c['case'];resp=c['response'];events=c['events']
 assert c['identity_preserved'] is c['peer_preserved'] is True
 assert resp['uid']==(1002 if mode=='unauthorized' else 1000)
 assert re.fullmatch(r':[0-9]+\.[0-9]+',resp['sender'])
 p=[v for v in events if v['event']=='polkit'];u=[v for v in events if v['event']=='unix'];d=[v for v in events if v['event']=='dispatch']
 assert c['polkit_checks']==len(p) and c['commands']==len(u) and c['dispatches']==len(d)
 assert len(p)==(0 if mode in ('replay','unauthorized','invalid') else 1)
 positive=mode in expected[:5]
 assert len(d)==int(positive) and len(u)==int(mode in ('profile','consumed','remove'))
 for v in p:
  assert v['locks_free'] is True and v['subject']['Kind']=='system-bus-name'
  action='challenge' if mode=='cancel' else 'deny' if mode=='deny' else 'allow'
  assert v['action']=='org.lyraos.parental.gofixture.'+action and v['error'] is False
  assert v['authorized']==(mode not in ('deny','cancel'))
  details=v['details'];assert details['target-uid']=='1002' and details['revision']=='1' and details['association']=='a'*32 and details['generation']=='b'*32
  assert details['method']==('UpdateUser' if mode=='profile' else 'SetAdmin') and re.fullmatch('[0-9a-f]{64}',details['parameter-digest'])
 for v in d:assert v['locks_held'] is True
 for v in u:
  assert v['locks_held'] is v['operation_exclusive'] is v['success'] is True
  all_commands.append((v['path'],v['argv']))
 if positive:assert resp['result']=='OrdinaryMutationDispatched'
 else:
  code='Conflict' if mode=='catalog-change' else 'Blocked' if mode=='barrier' else 'InvalidParameters' if mode=='invalid' else 'Denied'
  assert resp['error']=='org.lyraos.Parental.Error.'+code
 if mode=='consumed':assert resp['repeat_error']=='org.lyraos.Parental.Error.Denied'
 if mode=='cancel':assert resp['agent_calls']==['org.lyraos.parental.gofixture.challenge']
 if mode=='replay':assert resp['new_sender']!=resp['sender']
assert sum(c['polkit_checks'] for c in g['cases'])==10
assert all_commands==[('/usr/sbin/usermod',['-c','Lyra fixture $(literal)','-G','users','--','ordinaryuser']),('/usr/sbin/usermod',['-aG','wheel','--','ordinaryuser']),('/usr/bin/gpasswd',['-d','ordinaryuser','wheel'])]
assert read('go-test.json')['returncode']==read('execution.json')['rc']==0
assert read('cleanup-check.json')['rc']==read('poweroff.json')['rc']==read('guard.json')['rc']==0
assert json.loads(read('source-hashes.json')['stdout'])['parental.test']==m['binary_sha256']
off=read('vm-off.json');assert off['vm_off'] is True and off['qemu_returncode']==0 and off['backing_before']==off['backing_after']
assert read('disk.json')['filename']==off['same_disk']
f=read('failed-client-output/result.json');assert f['passed'] is False and f['go'] is None and f['restored'] is f['cleanup'] is True
assert f['configs_before']==f['configs_after'] and read('failed-client-output/go-test.json')['returncode']==1
print('PASS: 13 real system-bus/Polkit Go cases, 10 checks, 3 Unix commands/2 no-ops; restored, configuration cleaned, backing unchanged, VM OFF. No production/PAM/guardian qualification.')
