from pathlib import Path
import hashlib,json,os,subprocess,time
OUT=Path(__file__).resolve().parent
A=Path('/home/rodrigo/Git/LyraOS/analysis/2026-10-06/vegad-parental-reference-provisioning-vm')
BASE=Path('/tmp/lyra-parental-provision-20261006')
BACKS=[Path('/tmp/lyra-parental-snapper-20261006/system.qcow2'),Path('/tmp/lyra-parental-go-domain-20261006-retry2/system.qcow2'),Path('/tmp/lyra-parental-identity-20260928/system.raw')]
def digest(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(4*1024*1024),b''):h.update(b)
 return h.hexdigest()
def backing():return {str(p):dict(sha256=digest(p),size=p.stat().st_size,inode=p.stat().st_ino,mtime_ns=p.stat().st_mtime_ns) for p in BACKS}
before=backing();assert before[str(BACKS[-1])]['sha256']=='e1ed2ce143aa00331c1f5c15036516eb2fe313d1844dd1ceeee8b5cddf6e5c1d'
pid=int((BASE/'virtualization/pid').read_text());command=Path('/proc',str(pid),'cmdline').read_bytes().split(b'\0')
assert command[0].endswith(b'qemu-system-x86_64') and ('file='+str(BASE/'system.qcow2')+',format=qcow2,if=none,id=root').encode() in command and ('file='+str(BASE/'domain.qcow2')+',format=qcow2,if=none,id=domain').encode() in command
assert b'virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test' in command
def guest(*args):
 p=subprocess.run(['python3',str(A/'vm.py'),'virtualization','cmd',*args],capture_output=True,text=True,timeout=150)
 return json.loads(p.stdout)
def checked(name,*args):
 r=guest(*args);(OUT/name).write_text(json.dumps(r,indent=2)+'\n');assert r['rc']==0,r;return r
def push(source,target):subprocess.run(['python3',str(A/'host.py'),'push',str(source),target],check=True,capture_output=True)
def pull(source,target):subprocess.run(['python3',str(A/'host.py'),'pull',source,str(target)],check=True,capture_output=True)
checked('guard.json','python3','-c',"from pathlib import Path;import os,json;assert os.geteuid()==0;assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split();assert Path('/sys/block/vdb/serial').read_text().strip()=='lyra-ref-domain-test';p=Path('/root/lyra-go-provision-test');assert not p.exists();p.mkdir(mode=0o700);print(json.dumps(dict(guard=True)))")
checked('btrfs-version.json','/usr/sbin/btrfs','--version')
push(A/'parental.test.gz','/root/lyra-go-provision-test/parental.test.gz');push(OUT/'guest.py','/root/lyra-go-provision-test/guest.py')
hashes={'parental.test':digest(A/'parental.test'),'guest.py':digest(OUT/'guest.py')}
checked('source-hashes.json','python3','-c',"from pathlib import Path;import gzip,hashlib,json;root=Path('/root/lyra-go-provision-test');(root/'parental.test').write_bytes(gzip.decompress((root/'parental.test.gz').read_bytes()));expected="+repr(hashes)+";actual={n:hashlib.sha256((root/n).read_bytes()).hexdigest() for n in expected};assert expected==actual;(root/'parental.test').chmod(0o700);print(json.dumps(actual))")
try:
 r=guest('python3','/root/lyra-go-provision-test/guest.py');(OUT/'execution.json').write_text(json.dumps(r,indent=2)+'\n')
 pull('/root/lyra-go-provision-test/result.json',OUT/'result.json');pull('/root/lyra-go-provision-test/go-test.json',OUT/'go-test.json');assert r['rc']==0,r
 pull('/root/lyra-go-provision-test/go-result.json',OUT/'go-result.json')
 checked('cleanup-check.json','python3','-c',"from pathlib import Path;import json;assert not Path('/root/lyra-go-provision-test/volume').exists();r=json.loads(Path('/root/lyra-go-provision-test/result.json').read_text());assert r['passed'] and r['cleanup'] and r['state_before']==r['state_after'];print(json.dumps(dict(passed=True,cleanup=True)))")
finally:
 checked('poweroff.json','systemctl','poweroff')
 deadline=time.monotonic()+45
 while Path('/proc',str(pid)).exists():
  assert time.monotonic()<deadline
  time.sleep(.2)
 after=backing();assert before==after
 (OUT/'backing-check.json').write_text(json.dumps(dict(vm_off=True,pid=pid,backing_before=before,backing_after=after),indent=2)+'\n')
print('PASS: root reference provisioner/actual process cuts and authorized exact resume; cleanup/state/backing unchanged; VM OFF')
