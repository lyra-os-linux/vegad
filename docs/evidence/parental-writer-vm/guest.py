"""Root catalog-fixture qualification: no real account or policy modification."""
import hashlib,json,os,shutil,subprocess,sys
from pathlib import Path
BASE=Path('/root/lyra-go-writer-test');FIXTURES=BASE/'fixtures'
def guard():
 assert os.geteuid()==0 and Path(__file__).resolve().parent==BASE
 assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split()
 assert Path('/sys/block/vda/serial').read_text().strip()=='lyra-admission-test'
guard();assert not FIXTURES.exists()
def state():
 paths=[Path('/etc')/n for n in ('passwd','group','gshadow','shadow','passwd-','group-','gshadow-','shadow-')]
 for d in ('/etc/pam.d','/usr/lib/pam.d','/etc/dbus-1/system.d','/usr/share/polkit-1/actions','/etc/polkit-1/rules.d'):
  paths.extend(sorted(p for p in Path(d).glob('*') if p.is_file()))
 return dict(files={str(p):hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else None for p in paths},selinux=subprocess.check_output(['getenforce'],text=True).strip())
before=state();passed=False;error=None;go=None
try:
 row=subprocess.run([str(BASE/'parental.test'),'-test.run','^TestCatalogWriterRootDisposableVM$','-test.v','-test.timeout','90s'],env=dict(os.environ,LYRA_PARENTAL_WRITER_VM='1'),capture_output=True,text=True,timeout=100)
 (BASE/'go-test.json').write_text(json.dumps(dict(returncode=row.returncode,stdout=row.stdout,stderr=row.stderr),indent=2)+'\n')
 assert row.returncode==0,(row.returncode,row.stdout,row.stderr)
 go=json.loads((BASE/'go-result.json').read_text());assert go['passed'] and len(go['cases'])==4
 assert not FIXTURES.exists(),'test failed to clean fixture state'
 passed=True
except Exception as exc:error=repr(exc)
finally:
 guard()
 if FIXTURES.exists():shutil.rmtree(FIXTURES)
 after=state();unchanged=before==after
 receipt=dict(passed=passed,error=error,go=go,state_before=before,state_after=after,restored=unchanged,system_state_unchanged=unchanged,cleanup=not FIXTURES.exists(),account_mutations=False,production_integrated=False)
 (BASE/'result.json').write_text(json.dumps(receipt,indent=2)+'\n');assert unchanged
print(json.dumps(dict(passed=passed,system_state_unchanged=unchanged,cleanup=receipt['cleanup'],error=error)))
sys.exit(0 if passed else 1)
