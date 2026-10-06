"""Format only the guarded blank second guest disk; no host storage operations."""
import hashlib,json,os,subprocess,sys
from pathlib import Path
BASE=Path('/root/lyra-go-reference-test');VOLUME=BASE/'volume'
def guard():
 assert os.geteuid()==0 and Path(__file__).resolve().parent==BASE
 assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split()
 assert Path('/sys/block/vda/serial').read_text().strip()=='lyra-admission-test'
 assert Path('/sys/block/vdb/serial').read_text().strip()=='lyra-ref-domain-test'
 assert int(Path('/sys/block/vdb/size').read_text())*512==512*1024*1024
 assert Path('/dev/vdb').is_block_device()
 assert '/dev/vdb' not in Path('/proc/self/mountinfo').read_text()
guard();assert not VOLUME.exists()
def state():
 paths=[Path('/etc')/n for n in ('passwd','group','gshadow','shadow','passwd-','group-','gshadow-','shadow-')]
 for d in ('/etc/pam.d','/usr/lib/pam.d','/etc/dbus-1/system.d','/usr/share/polkit-1/actions','/etc/polkit-1/rules.d'):
  paths.extend(sorted(p for p in Path(d).glob('*') if p.is_file()))
 paths.extend(Path('/etc/snapper/configs').glob('*'));paths.append(Path('/etc/sysconfig/snapper'));return dict(files={str(p):hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else None for p in paths},selinux=subprocess.check_output(['getenforce'],text=True).strip())
before=state();passed=False;error=None;go=None;mounted=False
try:
 assert Path('/usr/sbin/mkfs.btrfs').is_file() and Path('/usr/sbin/btrfs').is_file()
 blank=subprocess.run(['wipefs','--no-act','/dev/vdb'],capture_output=True,text=True,check=True)
 assert not blank.stdout.strip(),'second disk is not blank'
 # Confirm target immediately before the only destructive command.
 guard()
 mkfs=subprocess.run(['/usr/sbin/mkfs.btrfs','/dev/vdb'],capture_output=True,text=True,check=True)
 (BASE/'mkfs.json').write_text(json.dumps(dict(stdout=mkfs.stdout,stderr=mkfs.stderr),indent=2)+'\n')
 VOLUME.mkdir(mode=0o700)
 subprocess.run(['mount','-t','btrfs','-o','subvolid=5','/dev/vdb',str(VOLUME)],check=True,capture_output=True);mounted=True
 row=subprocess.run([str(BASE/'parental.test'),'-test.run','^TestReferenceWriterRootDisposableVM$','-test.v','-test.timeout','90s'],env=dict(os.environ,LYRA_PARENTAL_REFERENCE_VM='1'),capture_output=True,text=True,timeout=100)
 (BASE/'go-test.json').write_text(json.dumps(dict(returncode=row.returncode,stdout=row.stdout,stderr=row.stderr),indent=2)+'\n')
 assert row.returncode==0,(row.returncode,row.stdout,row.stderr)
 go=json.loads((BASE/'go-result.json').read_text());assert go['passed'] and go['actual_process_cuts']
 passed=True
except Exception as exc:error=repr(exc)
finally:
 # Storage is retained only on the disposable second disk for inspection.
 # No host or guest account/policy files need restoring.
 if mounted:
  for child in ('root','authority-0','authority-1','authority-2'):
   target=VOLUME/child
   if os.path.ismount(target):subprocess.run(['umount',str(target)],check=True,capture_output=True)
  subprocess.run(['umount',str(VOLUME)],check=True,capture_output=True)
 if VOLUME.exists():VOLUME.rmdir()
 assert '/dev/vdb' not in Path('/proc/self/mountinfo').read_text()
 after=state();unchanged=before==after
 receipt=dict(passed=passed,error=error,go=go,state_before=before,state_after=after,restored=unchanged,system_state_unchanged=unchanged,cleanup=not VOLUME.exists(),second_disk_unmounted=True,account_mutations=False,production_integrated=False)
 (BASE/'result.json').write_text(json.dumps(receipt,indent=2)+'\n');assert unchanged
print(json.dumps(dict(passed=passed,system_state_unchanged=unchanged,cleanup=receipt['cleanup'],error=error)))
sys.exit(0 if passed else 1)
