"""Account restoration controller; destructive effects restricted to disposable guest."""
import hashlib,json,os,pwd,shutil,subprocess,sys
from pathlib import Path
BASE=Path('/root/lyra-go-bus-test')
def guard():
 assert os.geteuid()==0 and Path(__file__).resolve().parent==BASE
 assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split()
 assert Path('/sys/block/vda/serial').read_text().strip()=='lyra-admission-test'
guard()
assert pwd.getpwuid(1002).pw_name=='ordinaryuser' and pwd.getpwuid(1000).pw_name=='installeduser'
for entry in Path('/proc').iterdir():
 if entry.name.isdigit():
  try:uids=(entry/'status').read_text().split('Uid:')[1].splitlines()[0].split()
  except (FileNotFoundError,IndexError,ProcessLookupError):continue
  assert '1002' not in uids,'target already running'
PATHS=[Path('/etc')/n for n in ('passwd','group','gshadow','shadow','passwd-','group-','gshadow-','shadow-')]
original={str(p):dict(data=p.read_bytes(),uid=p.stat().st_uid,gid=p.stat().st_gid,mode=p.stat().st_mode&0o777) if p.exists() else None for p in PATHS}
def hashes():return {str(p):hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else None for p in PATHS}
def state():
 paths=[Path('/etc/passwd'),Path('/etc/group')]
 for d in ('/etc/pam.d','/usr/lib/pam.d'):paths.extend(sorted(p for p in Path(d).glob('*') if p.is_file()))
 return dict(files={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in paths},selinux=subprocess.check_output(['getenforce'],text=True).strip())

import time,dbus
RUN=Path('/run/lyra-parental-go-bus')
CONF=Path('/etc/dbus-1/system.d/lyra-parental-gofixture.conf')
POLICY=Path('/usr/share/polkit-1/actions/org.lyraos.parental.gofixture.policy')
RULE=Path('/etc/polkit-1/rules.d/00-lyra-parental-gofixture.rules')
assert not any(p.exists() or p.is_symlink() for p in (RUN,CONF,POLICY,RULE))
BUS=dbus.SystemBus(private=True)
authority=dbus.Interface(BUS.get_object('org.freedesktop.PolicyKit1','/org/freedesktop/PolicyKit1/Authority'),'org.freedesktop.PolicyKit1.Authority')
def configs():
 return {str(p):hashlib.sha256(p.read_bytes()).hexdigest() for d in ('/etc/dbus-1/system.d','/usr/share/polkit-1/actions','/etc/polkit-1/rules.d') for p in sorted(Path(d).glob('*')) if p.is_file()}
def reload():BUS.get_object('org.freedesktop.DBus','/org/freedesktop/DBus').ReloadConfig(dbus_interface='org.freedesktop.DBus')
def wait_actions(present):
 deadline=time.monotonic()+15
 expected={'org.lyraos.parental.gofixture.'+n for n in ('allow','deny','challenge')}
 while True:
  found=expected <= {str(row[0]) for row in authority.EnumerateActions('C')}
  if found==present:return
  assert time.monotonic()<deadline,'policy reload timeout'
  time.sleep(.1)
config_before=configs()

initial=hashes();before=state();backup=BASE/'account-backup';backup.mkdir(mode=0o700)
for path,row in original.items():
 if row is not None:p=backup/Path(path).name;p.write_bytes(row['data']);p.chmod(0o600)
passed=False;error=None;go=None
try:

 RUN.mkdir(mode=0o755)
 shutil.copyfile(BASE/'client.py',RUN/'client.py');(RUN/'client.py').chmod(0o644)
 CONF.write_text('<busconfig><policy user="root"><allow own="org.lyraos.ParentalGoFixture1"/></policy><policy context="default"><allow send_destination="org.lyraos.ParentalGoFixture1"/></policy></busconfig>\n')
 defaults=lambda v:'<defaults><allow_any>'+v+'</allow_any><allow_inactive>'+v+'</allow_inactive><allow_active>'+v+'</allow_active></defaults>'
 POLICY.write_text('<?xml version="1.0"?><policyconfig>'+''.join('<action id="org.lyraos.parental.gofixture.'+n+'"><description>Disposable Go fixture</description><message>Disposable Go authentication</message>'+defaults('auth_self' if n=='challenge' else 'no')+'</action>' for n in ('allow','deny','challenge'))+'</policyconfig>\n')
 RULE.write_text('polkit.addRule(function(action, subject) { if (subject.user === "installeduser" && action.id === "org.lyraos.parental.gofixture.allow") return polkit.Result.YES; if (action.id === "org.lyraos.parental.gofixture.deny") return polkit.Result.NO; });\n')
 for p in (CONF,POLICY,RULE):p.chmod(0o644)
 reload();wait_actions(True)

 env=dict(os.environ,LYRA_PARENTAL_BUS_VM='1')
 row=subprocess.run([str(BASE/'parental.test'),'-test.run','^TestAdministrativeBusDisposableVM$','-test.v','-test.timeout','180s'],env=env,capture_output=True,text=True,timeout=190)
 (BASE/'go-test.json').write_text(json.dumps(dict(returncode=row.returncode,stdout=row.stdout,stderr=row.stderr),indent=2)+'\n')
 assert row.returncode==0,(row.returncode,row.stdout,row.stderr)
 go=json.loads((BASE/'go-result.json').read_text());assert go['passed'] and len(go['cases'])==13
 assert Path('/etc/shadow').read_bytes()==original['/etc/shadow']['data']
 assert [x for x in Path('/etc/passwd').read_bytes().splitlines() if not x.startswith(b'ordinaryuser:')]==[x for x in original['/etc/passwd']['data'].splitlines() if not x.startswith(b'ordinaryuser:')]
 passed=True
except Exception as exc:error=repr(exc)
finally:

 for p in (CONF,POLICY,RULE):p.unlink(missing_ok=True)
 reload();wait_actions(False)
 if RUN.exists():shutil.rmtree(RUN)
 assert configs()==config_before

 guard()
 for path,row in original.items():
  p=Path(path)
  if row is None:p.unlink(missing_ok=True)
  elif not p.exists() or p.read_bytes()!=row['data']:
   p.write_bytes(row['data']);os.chown(p,row['uid'],row['gid']);p.chmod(row['mode']);fd=os.open(p,os.O_RDONLY);os.fsync(fd);os.close(fd)
 fd=os.open('/etc',os.O_RDONLY|os.O_DIRECTORY);os.fsync(fd);os.close(fd)
 shutil.rmtree(backup)
 restored=hashes()==initial and before==state()
 receipt=dict(passed=passed,error=error,go=go,account_hashes_before=initial,account_hashes_after=hashes(),state_before=before,state_after=state(),restored=restored,cleanup=not backup.exists(),configs_before=config_before,configs_after=configs(),real_polkit=True,production_integrated=False)
 (BASE/'result.json').write_text(json.dumps(receipt,indent=2)+'\n');assert restored
print(json.dumps(dict(passed=passed,restored=restored,cleanup=receipt['cleanup'],error=error)))
sys.exit(0 if passed else 1)
