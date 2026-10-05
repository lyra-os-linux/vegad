"""Unprivileged test client; cancellation agent never handles a password."""
import json,os,sys
from pathlib import Path
import dbus,dbus.service,dbus.mainloop.glib
from gi.repository import GLib
NAME='org.lyraos.ParentalGoFixture1';PATH='/org/lyraos/ParentalGoFixture1'
dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
bus=dbus.SystemBus(private=True)
mode=sys.argv[1];agent_calls=[]
result=dict(uid=os.getuid(),sender=str(bus.get_unique_name()),agent_calls=agent_calls)
class Agent(dbus.service.Object):
 @dbus.service.method('org.freedesktop.PolicyKit1.AuthenticationAgent',
   in_signature='sssa{ss}sa(sa{sv})',out_signature='')
 def BeginAuthentication(self,action,message,icon,details,cookie,identities):
  agent_calls.append(str(action))
  raise dbus.exceptions.DBusException('fixture cancellation',name='org.freedesktop.PolicyKit1.Error.Cancelled')
 @dbus.service.method('org.freedesktop.PolicyKit1.AuthenticationAgent',in_signature='s',out_signature='')
 def CancelAuthentication(self,cookie):pass

agent=None;authority=None;subject=None
if mode=='cancel':
 agent=Agent(bus,'/org/lyraos/FixtureAgent')
 start=int(Path('/proc/self/stat').read_text().rsplit(')',1)[1].split()[19])
 subject=dbus.Struct(('unix-process',dbus.Dictionary({'pid':dbus.UInt32(os.getpid()),
  'start-time':dbus.UInt64(start),'uid':dbus.Int32(os.getuid())},signature='sv')),signature='sa{sv}')
 authority=dbus.Interface(bus.get_object('org.freedesktop.PolicyKit1','/org/freedesktop/PolicyKit1/Authority'),'org.freedesktop.PolicyKit1.Authority')
 authority.RegisterAuthenticationAgent(subject,'C','/org/lyraos/FixtureAgent')
try:
 proxy=dbus.Interface(bus.get_object(NAME,PATH),NAME)
 if mode=='resume':
  result['result']=str(proxy.Resume('a'*32,sys.argv[2],timeout=25))
 elif mode=='own':result['result']=str(proxy.OwnState(timeout=25))
 else:
  method='UpdateUser' if mode in ('profile','invalid') else 'SetAdmin'
  values=dict(full_name='Lyra fixture $(literal)',groups=['users'],is_admin=False) if method=='UpdateUser' else dict(is_admin=mode not in ('remove','noop-remove'))
  if mode=='invalid':values['full_name']='invalid:profile'
  identifier=str(proxy.PrepareUpdateUser('a'*32,dbus.UInt64(1),values['full_name'],dbus.Array(values['groups'],signature='s'),values['is_admin'],timeout=25) if method=='UpdateUser' else proxy.PrepareSetAdmin('a'*32,dbus.UInt64(1),values['is_admin'],timeout=25));result['plan']=identifier
  values['is_admin']=False;values['full_name']='Changed after plan' # no execute parameters
  if mode=='replay':
   bus.close();bus=dbus.SystemBus(private=True)
   result['new_sender']=str(bus.get_unique_name());proxy=dbus.Interface(bus.get_object(NAME,PATH),NAME)
  if mode=='cancel':
   loop=GLib.MainLoop()
   def ok(value):result['result']=str(value);loop.quit()
   def error(exc):result['error']=exc.get_dbus_name();loop.quit()
   proxy.Execute(identifier,reply_handler=ok,error_handler=error,timeout=25)
   GLib.timeout_add_seconds(30,lambda:loop.quit() or False)
   loop.run()
  else:
   result['result']=str(proxy.Execute(identifier,timeout=25))
   if mode=='consumed':
    try:proxy.Execute(identifier,timeout=25)
    except dbus.DBusException as exc:result['repeat_error']=exc.get_dbus_name()
    else:raise AssertionError('consumed plan replayed')
except dbus.DBusException as exc:result['error']=exc.get_dbus_name()
finally:
 if authority is not None:authority.UnregisterAuthenticationAgent(subject,'/org/lyraos/FixtureAgent')
 bus.close()
print(json.dumps(result))
