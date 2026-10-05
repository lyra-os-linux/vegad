import base64,json,subprocess,sys
from pathlib import Path
OUT=Path(__file__).resolve().parent
def guest(*args):
    p=subprocess.run(['python3',str(OUT/'vm.py'),'virtualization','cmd',*args],capture_output=True,text=True,timeout=920)
    result=json.loads(p.stdout)
    if result.get('rc',1):raise RuntimeError(result)
    return result['stdout']
def code(source):
    return guest('python3','-c',"from pathlib import Path; import subprocess,os; assert 'lyra.parental-identity-test=1' in Path('/proc/cmdline').read_text().split(); assert Path('/sys/block/vda/serial').read_text().strip()=='lyra-admission-test';\n"+source)
if sys.argv[1]=='code':print(code(Path(sys.argv[2]).read_text()))
elif sys.argv[1]=='push':
    data=Path(sys.argv[2]).read_bytes();target=sys.argv[3]
    code('Path('+repr(target)+').write_bytes(b"")')
    for offset in range(0,len(data),32768):
        chunk=base64.b64encode(data[offset:offset+32768]).decode()
        code('import base64\nwith Path('+repr(target)+').open("ab") as f:f.write(base64.b64decode('+repr(chunk)+'))')
elif sys.argv[1]=='pull':
    result=code('import base64; print(base64.b64encode(Path('+repr(sys.argv[2])+').read_bytes()).decode())')
    Path(sys.argv[3]).write_bytes(base64.b64decode(result))
