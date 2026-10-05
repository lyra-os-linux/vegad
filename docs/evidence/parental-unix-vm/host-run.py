from pathlib import Path
import hashlib,json,os,signal,subprocess,time
A=Path(__file__).resolve().parent
OUT=Path('/home/rodrigo/Git/LyraOS/worktrees/vegad-parental-catalog/docs/evidence/parental-unix-vm')
BASE=Path('/tmp/lyra-parental-go-unix-retry-20261005')
BACK=Path('/tmp/lyra-parental-identity-20260928/system.raw')
BASE.mkdir(mode=0o700,exist_ok=False)
assert BACK.is_file()
def hash_file(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for block in iter(lambda:f.read(4*1024*1024),b''):h.update(block)
 return h.hexdigest()
before=dict(sha256=hash_file(BACK),size=BACK.stat().st_size,inode=BACK.stat().st_ino,mtime_ns=BACK.stat().st_mtime_ns)
subprocess.run(['qemu-img','create','-f','qcow2','-F','raw','-b',str(BACK),str(BASE/'system.qcow2')],check=True,capture_output=True)
info=json.loads(subprocess.check_output(['qemu-img','info','--output=json',str(BASE/'system.qcow2')],text=True))
assert info['full-backing-filename']==str(BACK) and info['backing-filename-format']=='raw'
(OUT/'disk.json').write_text(json.dumps(info,indent=2)+'\n')
def guest(*args):
 p=subprocess.run(['python3',str(A/'vm.py'),'virtualization','cmd',*args],capture_output=True,text=True,timeout=180)
 if not p.stdout:raise RuntimeError(p.stderr)
 return json.loads(p.stdout)
def checked(name,*args):
 row=guest(*args);(OUT/name).write_text(json.dumps(row,indent=2)+'\n');assert row['rc']==0,row;return row
vm=subprocess.Popen(['python3',str(A/'vm.py'),'virtualization','launch'],stdout=(A/'qemu-output.txt').open('wb'),stderr=subprocess.STDOUT)
try:
 deadline=time.monotonic()+60
 while True:
  try:
   if guest('true')['rc']==0:break
  except Exception:pass
  assert vm.poll() is None and time.monotonic()<deadline
  time.sleep(.2)
 command=Path('/proc',str(vm.pid),'cmdline').read_bytes().split(b'\0')
 assert command[0].endswith(b'qemu-system-x86_64') and ('file='+str(BASE/'system.qcow2')+',format=qcow2,if=none,id=root').encode() in command
 checked('guard.json','python3','-c',"from pathlib import Path;import os,json;assert os.geteuid()==0;assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split();assert Path('/sys/block/vda/serial').read_text().strip()=='lyra-admission-test';p=Path('/root/lyra-go-unix-test');assert not p.exists();p.mkdir(mode=0o700);print(json.dumps(dict(guard=True)))")
 files=[(A/'parental.test','parental.test'),(OUT/'guest.py','guest.py')]
 hashes={}
 for source,name in files:
  hashes[name]=hashlib.sha256(source.read_bytes()).hexdigest()
  subprocess.run(['python3',str(A/'host.py'),'push',str(source),'/root/lyra-go-unix-test/'+name],check=True,capture_output=True)
 checked('source-hashes.json','python3','-c',"from pathlib import Path;import hashlib,json;expected="+repr(hashes)+";actual={n:hashlib.sha256((Path('/root/lyra-go-unix-test')/n).read_bytes()).hexdigest() for n in expected};assert actual==expected;Path('/root/lyra-go-unix-test/parental.test').chmod(0o700);print(json.dumps(actual))")
 row=guest('python3','/root/lyra-go-unix-test/guest.py');(OUT/'execution.json').write_text(json.dumps(row,indent=2)+'\n')
 subprocess.run(['python3',str(A/'host.py'),'pull','/root/lyra-go-unix-test/result.json',str(OUT/'result.json')],check=True,capture_output=True)
 subprocess.run(['python3',str(A/'host.py'),'pull','/root/lyra-go-unix-test/go-test.json',str(OUT/'go-test.json')],check=True,capture_output=True)
 assert row['rc']==0,row
 checked('cleanup-check.json','python3','-c',"from pathlib import Path;import json;assert not Path('/root/lyra-go-unix-test/fixtures').exists();r=json.loads(Path('/root/lyra-go-unix-test/result.json').read_text());assert r['passed'] and r['restored'] and r['cleanup'] and r['state_before']==r['state_after'] and not Path('/root/lyra-go-unix-test/account-backup').exists();print(json.dumps(dict(passed=True,cleanup=True)))")
 checked('poweroff.json','systemctl','poweroff')
 status=vm.wait(timeout=45);assert status==0
 after=dict(sha256=hash_file(BACK),size=BACK.stat().st_size,inode=BACK.stat().st_ino,mtime_ns=BACK.stat().st_mtime_ns)
 assert after==before
 (OUT/'vm-off.json').write_text(json.dumps(dict(vm_off=True,qemu_returncode=status,backing_before=before,backing_after=after,same_disk=str(BASE/'system.qcow2')),indent=2)+'\n')
 print('PASS: Go guard/root filesystem leases/real Unix effects; cleanup confirmed, backing unchanged, VM OFF',flush=True)
finally:
 if vm.poll() is None:
  fd=os.pidfd_open(vm.pid)
  try:signal.pidfd_send_signal(fd,signal.SIGKILL)
  finally:os.close(fd)
  vm.wait(timeout=15)
