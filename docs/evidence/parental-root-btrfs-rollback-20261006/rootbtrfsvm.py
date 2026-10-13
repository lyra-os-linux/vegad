#!/usr/bin/python3
"""Launch/control disposable GDM guests with local-only QMP/evidence sockets."""
import argparse,json,os,socket,subprocess,sys,time,uuid
from pathlib import Path
OUT=Path(__file__).resolve().parent
ASSETS=OUT/'assets';BASE=Path('/tmp/lyra-parental-layout-20261006')
p=argparse.ArgumentParser();p.add_argument('variant',choices=['virtualization']);p.add_argument('action',choices=['launch','cmd','key','shot','qmp']);p.add_argument('rest',nargs=argparse.REMAINDER);a=p.parse_args()
RUN=BASE/(a.variant+'-root-btrfs');RUN.mkdir(exist_ok=True);RUN.chmod(0o700)
if a.action=='launch':

 for socket_name in ['qmp.sock','agent.sock','vnc.sock']:
  path=RUN/socket_name
  if path.exists():
   probe=socket.socket(socket.AF_UNIX)
   try: probe.connect(str(path))
   except ConnectionRefusedError: path.unlink()
   else: raise RuntimeError('VM already launched')
   finally: probe.close()
 disk=BASE/'root-btrfs.qcow2';assert disk.is_file()
 overlay=disk
 command=['qemu-system-x86_64','-name','lyra-parental-root-btrfs-test','-machine','q35','-accel','kvm','-cpu','host','-smp','2','-m','3072',
 '-kernel','/usr/lib/modules/6.12.0-160100.4-default/vmlinuz','-initrd','/tmp/lyra-parental-layout-20261006/initrd-btrfs.gz',
 '-append','rdinit=/init-btrfs root=/dev/vda rw console=tty0 console=ttyS0 panic=1 lyra.virtualization-test=1 lyra.parental-selinux-test=1 lyra.parental-identity-test=1 enforcing=0',
 '-drive',f'file={overlay},format=qcow2,if=none,id=root','-device','virtio-blk-pci,drive=root,serial=lyra-parental-root-t',
 '-drive',f'file={BASE}/domain.qcow2,format=qcow2,if=none,id=domain','-device','virtio-blk-pci,drive=domain,serial=lyra-ref-domain-test',
 '-device','virtio-vga','-device','virtio-keyboard-pci','-device','virtio-tablet-pci',
 '-device','virtio-serial-pci','-chardev',f'socket,id=agent,path={RUN}/agent.sock,server=on,wait=off',
 '-device','virtserialport,chardev=agent,name=org.lyra.vmtest',
 '-qmp',f'unix:{RUN}/qmp.sock,server=on,wait=off','-vnc',f'unix:{RUN}/vnc.sock',
 '-serial',f'file:{OUT}/{a.variant}-root-btrfs-serial.log','-monitor','none','-nic','user,model=virtio-net-pci','-no-reboot']
 (OUT/(a.variant+'-root-btrfs-command.json')).write_text(json.dumps(command,indent=2)+'\n')
 (RUN/'pid').write_text(str(os.getpid()))
 os.execvp(command[0],command)

def connect(name):
 s=socket.socket(socket.AF_UNIX);s.settimeout(910);s.connect(str(RUN/name));return s,s.makefile('rwb',buffering=65536)
def qmp(execute,arguments=None):
 s,f=connect('qmp.sock')
 with s,f:
  json.loads(f.readline())
  f.write(b'{"execute":"qmp_capabilities"}\n');f.flush()
  while 'return' not in json.loads(f.readline()):pass
  f.write((json.dumps({'execute':execute,'arguments':arguments or {},'id':'request'})+'\n').encode());f.flush()
  while True:
   r=json.loads(f.readline())
   if r.get('id')=='request':return r
if a.action=='cmd':
 assert a.rest
 s,f=connect('agent.sock');request_id=uuid.uuid4().hex
 with s,f:
  f.write((json.dumps({'id':request_id,'argv':a.rest,'timeout':900})+'\n').encode());f.flush()
  while True:
   line=f.readline()
   if not line:raise RuntimeError('Guest channel closed')
   r=json.loads(line)
   if r.get('id')==request_id:break
 with (OUT/(a.variant+'-root-btrfs-commands.jsonl')).open('a') as log:log.write(json.dumps({'time':time.time(),'argv':a.rest,'result':r})+'\n')
 print(json.dumps(r,ensure_ascii=False,indent=2));sys.exit(r.get('rc',1))
elif a.action=='key':
 for item in a.rest:
  print(qmp('send-key',{'keys':[{'type':'qcode','data':key} for key in item.split('+')],'hold-time':80}))
  time.sleep(.15)
elif a.action=='shot':
 target=OUT/(a.variant+'-'+(a.rest[0] if a.rest else 'screen')+'.png')
 print(qmp('screendump',{'filename':str(target),'format':'png'}));print(target)
else:print(qmp(a.rest[0],json.loads(a.rest[1]) if len(a.rest)>1 else None))
