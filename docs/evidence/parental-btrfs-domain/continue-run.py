"""Resume guarded, unformatted lab disks after supplying missing guest tools."""
from pathlib import Path
import hashlib,json,os,subprocess,time
OUT=Path(__file__).resolve().parent
A=Path('/home/rodrigo/Git/LyraOS/analysis/2026-10-06/vegad-parental-btrfs-domain')
BASE=Path('/tmp/lyra-parental-go-domain-20261006-retry2')
BACK=Path('/tmp/lyra-parental-identity-20260928/system.raw')
def hash_file(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(4*1024*1024),b''):h.update(b)
 return h.hexdigest()
def backing():return dict(sha256=hash_file(BACK),size=BACK.stat().st_size,inode=BACK.stat().st_ino,mtime_ns=BACK.stat().st_mtime_ns)
before=backing();assert before['sha256']=='e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
# Deliberately reuse the second attempt's still-blank vdb. The failed dependency
# check never reached mkfs; guest guard independently confirms blankness again.
pid=int((BASE/'virtualization/pid').read_text())
command=Path('/proc',str(pid),'cmdline').read_bytes().split(b'\0')
assert command[0].endswith(b'qemu-system-x86_64')
assert ('file='+str(BASE/'system.qcow2')+',format=qcow2,if=none,id=root').encode() in command
assert ('file='+str(BASE/'domain.qcow2')+',format=qcow2,if=none,id=domain').encode() in command
assert b'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in command
def guest(*args):
 p=subprocess.run(['python3',str(A/'vm.py'),'virtualization','cmd',*args],capture_output=True,text=True,timeout=180)
 r=json.loads(p.stdout);return r
def checked(name,*args):
 r=guest(*args);(OUT/name).write_text(json.dumps(r,indent=2)+'\n');assert r['rc']==0,r;return r
def push(source,target):subprocess.run(['python3',str(A/'host.py'),'push',str(source),target],capture_output=True,check=True)
def pull(source,target):subprocess.run(['python3',str(A/'host.py'),'pull',source,str(target)],capture_output=True,check=True)
checked('resume-guard.json','python3','-c',"from pathlib import Path;import os,json;assert os.geteuid()==0;assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split();assert Path('/sys/block/vdb/serial').read_text().strip()=='lyra-ref-domain-test';assert '/dev/vdb' not in Path('/proc/self/mountinfo').read_text();print(json.dumps(dict(guard=True)))")
packages=sorted((A/'packages').glob('*.rpm'));assert len(packages)==3
hashes={p.name:hash_file(p) for p in packages}
for p in packages:push(p,'/root/lyra-go-domain-test/'+p.name)
checked('packages-verified.json','python3','-c',"from pathlib import Path;import hashlib,json,subprocess;expected="+repr(hashes)+";actual={n:hashlib.sha256((Path('/root/lyra-go-domain-test')/n).read_bytes()).hexdigest() for n in expected};assert expected==actual;subprocess.run(['rpmkeys','--checksig',*[str(Path('/root/lyra-go-domain-test')/n) for n in expected]],check=True);print(json.dumps(actual))")
checked('guest-prerequisites.json','zypper','--non-interactive','--no-refresh','install','--no-recommends',*[str(Path('/root/lyra-go-domain-test')/p.name) for p in packages])
checked('btrfs-version.json','/usr/sbin/btrfs','--version')
push(A/'parental.test.gz','/root/lyra-go-domain-test/parental.test.gz')
push(OUT/'guest.py','/root/lyra-go-domain-test/guest.py')
expected={'parental.test':hash_file(A/'parental.test'),'guest.py':hash_file(OUT/'guest.py')}
checked('source-hashes.json','python3','-c',"from pathlib import Path;import gzip,hashlib,json;root=Path('/root/lyra-go-domain-test');(root/'parental.test').write_bytes(gzip.decompress((root/'parental.test.gz').read_bytes()));expected="+repr(expected)+";actual={n:hashlib.sha256((root/n).read_bytes()).hexdigest() for n in expected};assert actual==expected;(root/'parental.test').chmod(0o700);print(json.dumps(actual))")
r=guest('python3','/root/lyra-go-domain-test/guest.py');(OUT/'execution.json').write_text(json.dumps(r,indent=2)+'\n')
pull('/root/lyra-go-domain-test/result.json',OUT/'result.json')
pull('/root/lyra-go-domain-test/go-test.json',OUT/'go-test.json')
assert r['rc']==0,r
pull('/root/lyra-go-domain-test/go-result.json',OUT/'go-result.json')
checked('cleanup-check.json','python3','-c',"from pathlib import Path;import json;assert not Path('/root/lyra-go-domain-test/volume').exists();r=json.loads(Path('/root/lyra-go-domain-test/result.json').read_text());assert r['passed'] and r['cleanup'] and r['state_before']==r['state_after'];print(json.dumps(dict(passed=True,cleanup=True)))")
checked('poweroff.json','systemctl','poweroff')
deadline=time.monotonic()+45
while Path('/proc',str(pid)).exists():
 assert time.monotonic()<deadline
 time.sleep(.2)
after=backing();assert after==before
(OUT/'backing-check.json').write_text(json.dumps(dict(vm_off=True,pid=pid,backing_before=before,backing_after=after),indent=2)+'\n')
print('PASS: real Btrfs domain/read-only source, cleanup/state/backing unchanged, guest powered off',flush=True)
