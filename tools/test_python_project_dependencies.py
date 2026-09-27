"""Pure-wheel admission and actual credential-free pip/import sandbox proofs."""
import base64
import csv
import hashlib
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import socketserver
import stat
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch
import uuid
import zipfile

HERE=Path(__file__).parent

def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
H=load('python_project_helper',HERE/'python-project-dependencies.py')
R=load('python_project_runner',HERE/'autonomy-runner.py')
REAL_IDENTITY=R.python_identity


def wheel(root,name,version='1.0',files=None,requires=()):
    dist=name.replace('-','_')+'-'+version+'.dist-info'
    data={name.replace('-','_')+'/__init__.py':b'VALUE=42\n',
          dist+'/METADATA':('Metadata-Version: 2.1\nName: '+name+'\nVersion: '+version+'\n'+''.join('Requires-Dist: '+r+'\n' for r in requires)).encode(),
          dist+'/WHEEL':b'Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n'}
    if files is not None:data.update(files)
    return wheel_data(root,name,version,data)


def wheel_data(root,name,version,data):
    data=dict(data);record=name.replace('-','_')+'-'+version+'.dist-info/RECORD';data.pop(record,None)
    buf=io.StringIO();writer=csv.writer(buf,lineterminator='\n')
    for key,value in sorted(data.items()):writer.writerow([key,'sha256='+base64.urlsafe_b64encode(hashlib.sha256(value).digest()).decode().rstrip('='),str(len(value))])
    writer.writerow([record,'','']);data[record]=buf.getvalue().encode()
    target=root/(name.replace('-','_')+'-'+version+'-py3-none-any.whl')
    with zipfile.ZipFile(target,'w',zipfile.ZIP_DEFLATED) as out:
        for key,value in data.items():out.writestr(key,value)
    return target


class WheelTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)
    def test_safe_install_and_tamper_detection(self):
        wheels=self.root/'wheels';wheels.mkdir();path=wheel(wheels,'example');info=H.wheel_info(path)
        (self.root/'lock.json').write_bytes(H.canonical({'policy':H.POLICY,'input_key':'a'*64,'wheels':[info]}))
        H.install(self.root);self.assertEqual(H.verify_install(self.root)['packages'],{'example':'1.0'})
        (self.root/'site-packages/example/__init__.py').write_text('changed')
        with self.assertRaisesRegex(ValueError,'bytes differ'):H.verify_install(self.root)
    def test_reject_traversal_links_record_tampering_and_hooks(self):
        for target in ('../escape','/escape','x/../escape','x.pth','sitecustomize.py','x.so','x.data/scripts/run'):
            with self.subTest(target=target):
                path=wheel(self.root,'example',files={target:b'bad'})
                with self.assertRaises(ValueError):H.wheel_info(path)
        path=wheel(self.root,'example')
        with zipfile.ZipFile(path,'a') as z:
            info=zipfile.ZipInfo('linked');info.external_attr=(stat.S_IFLNK|0o777)<<16;z.writestr(info,'/etc/passwd')
        with self.assertRaisesRegex(ValueError,'link'):H.wheel_info(path)
        path=wheel(self.root,'example');original=path.read_bytes()
        link=self.root/'hard.whl';os.link(path,link)
        with self.assertRaisesRegex(ValueError,'regular'):H.wheel_info(path)
        link.unlink()
        with zipfile.ZipFile(io.BytesIO(original)) as z:members={x:z.read(x) for x in z.namelist()}
        members['example/__init__.py']=b'tampered'
        with zipfile.ZipFile(path,'w') as z:
            for name,value in members.items():z.writestr(name,value)
        with self.assertRaisesRegex(ValueError,'checksum'):H.wheel_info(path)
    def test_collision_rejected(self):
        wheels=self.root/'wheels';wheels.mkdir()
        rows=[H.wheel_info(wheel(wheels,name,files={'shared.py':b'one'})) for name in ('one','two')]
        (self.root/'lock.json').write_bytes(H.canonical({'policy':H.POLICY,'wheels':rows}))
        with self.assertRaisesRegex(ValueError,'collision'):H.install(self.root)
    def test_snapshot_pip_checks_record(self):
        source=self.root/'source';source.mkdir();dist=source/'pip-1.0.dist-info';dist.mkdir();(source/'pip').mkdir()
        (source/'pip/__init__.py').write_text('')
        (dist/'METADATA').write_text('Name: pip\nVersion: 1.0\n')
        rows=[]
        for name in ('pip/__init__.py','pip-1.0.dist-info/METADATA'):
            data=(source/name).read_bytes();rows.append([name,'sha256='+base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip('='),str(len(data))])
        rows.append(['pip-1.0.dist-info/RECORD','',''])
        with (dist/'RECORD').open('w') as f:csv.writer(f).writerows(rows)
        H.snapshot_pip(source,self.root/'tooling')
        (source/'pip/__init__.py').write_text('malicious')
        with self.assertRaisesRegex(ValueError,'RECORD mismatch'):H.snapshot_pip(source,self.root/'other')


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name);self.job=str(uuid.uuid4());self.source=str(uuid.uuid4())
        (self.root/self.job).mkdir();(self.root/self.source).mkdir()
        self.request={'schema_version':1,'kind':'python_wheels','requirements':['example==1.0'],'imports':['example'],'source_job':self.source,'source_archive_sha256':'a'*64,'admission_sha256':'b'*64}
        self.requestpath=self.root/self.job/'python-requirement.json';self.requestpath.write_text(json.dumps(self.request));self.requestpath.chmod(0o600)
        self.identity={'input_key':'c'*64,'runtime_digest':'d'*64}
        for name,value in [('ROOT',self.root),('archive_identity',lambda job:{'sha256':'a'*64}),('status',lambda job:{'state':'prepared'}),('python_identity',lambda *args:self.identity),('write_new',self.write_new)]:
            pt=patch.object(R,name,value);pt.start();self.addCleanup(pt.stop)
    def write_new(self,path,data,mode=0o600):
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,mode)
        with os.fdopen(fd,'w') as out:out.write(data)
    def test_seal_and_source_binding(self):
        R.python_request(self.job)
        self.request['requirements']=['other==2.0'];self.requestpath.write_text(json.dumps(self.request))
        with self.assertRaisesRegex(ValueError,'changed after'):R.python_request(self.job)
        with patch.object(R,'archive_identity',return_value={'sha256':'f'*64}):
            with self.assertRaisesRegex(ValueError,'archive identity'):R.python_request(self.job)
    def test_missing_optional_toolkit_is_bound_unavailable_not_global_error(self):
        for failure in (FileNotFoundError('pytest runtime missing'),R.PythonUnsupported('trusted pip resolver unavailable')):
            with self.subTest(failure=str(failure)),patch.object(R,'python_identity',side_effect=failure),patch.object(R,'run') as launch:
                receipt=R.python_dependencies(self.job)
                self.assertEqual(receipt['state'],'unavailable');self.assertTrue(receipt['unsupported'])
                self.assertEqual(receipt['source_job'],self.source);self.assertEqual(receipt['source_archive_sha256'],'a'*64)
                self.assertNotIn('input_key',receipt);launch.assert_not_called()
        # Once capability initialization succeeds it can replace only this
        # diagnostic receipt, not an already sealed mismatched environment.
        with patch.object(R,'python_active',return_value=False),patch.object(R,'run'),patch.object(R.shutil,'disk_usage',return_value=shutil._ntuple_diskusage(100*1024**3,0,100*1024**3)):
            self.assertEqual(R.python_dependencies(self.job)['state'],'recovering')

    def test_async_launch_echoes_binding_and_inherits_lock(self):
        with patch.object(R,'python_active',return_value=False),patch.object(R,'run') as run,patch.object(R.shutil,'disk_usage',return_value=shutil._ntuple_diskusage(100*1024**3,0,100*1024**3)):
            receipt=R.python_dependencies(self.job)
        self.assertEqual(receipt['state'],'recovering');self.assertEqual(receipt['source_archive_sha256'],'a'*64)
        args,kw=run.call_args;self.assertIn('_python-dependencies',args[0]);self.assertIn('pass_fds',kw)
        self.assertIn('--property=RuntimeMaxSec=600',args[0])
    def test_uncertain_unit_status_is_not_inactive(self):
        with patch.object(R.subprocess,'run',return_value=subprocess.CompletedProcess([],1,'','')):
            with self.assertRaises(RuntimeError):R.python_active(self.job)
    def test_stop_failure_retains_recovery_and_retry_stops(self):
        stage,_=R.python_request(self.job);receipt=dict(R.python_binding(self.request,self.identity),state='recovering',attempts=1);R.completion_write(stage/'receipt.json',receipt)
        with patch.object(R,'python_active',return_value=True),patch.object(R,'run',side_effect=RuntimeError('stop failed')):
            with self.assertRaisesRegex(RuntimeError,'stop failed'):R.python_dependencies_stop(self.job)
        self.assertEqual(R.completion_json(stage/'receipt.json')['state'],'recovering')
        with patch.object(R,'python_active',side_effect=[True,False]),patch.object(R,'run'):
            self.assertEqual(R.python_dependencies_stop(self.job)['state'],'stopped')
        self.assertEqual(R.completion_json(stage/'receipt.json')['state'],'waiting')

    def test_deploy_between_provision_and_review_reuses_exact_environment(self):
        from types import SimpleNamespace
        deps=self.root/'dependencies';deps.mkdir();cache=deps/'python-project';cache.mkdir(mode=0o700)
        helper_data=(HERE/'python-project-dependencies.py').read_bytes()
        runtime={'python_sha256':R.digest_file(Path('/usr/bin/python3').resolve()),'python_version':'3.13.5','helper_sha256':hashlib.sha256(helper_data).hexdigest(),'pip_record_sha256':'b'*64,'pytest_key':'c'*64,'policy':H.POLICY}
        runtime_digest=hashlib.sha256(H.canonical(runtime)).hexdigest()
        semantic={'requirements':self.request['requirements'],'imports':self.request['imports'],'tooling_requirements':['pytest==9.1.1'],'runtime_digest':runtime_digest}
        key=hashlib.sha256(H.canonical(semantic)).hexdigest();identity=dict(semantic,input_key=key,runtime=runtime)
        content=b'VALUE=42\n';manifest={'schema_version':1,'kind':'python-project-runtime','input_key':key,'runtime_digest':runtime_digest,'checksum_verified':True,'files':[{'path':'example.py','size':len(content),'sha256':hashlib.sha256(content).hexdigest()}]}
        bundle_key=hashlib.sha256(H.canonical(manifest)).hexdigest();manifest['key']=bundle_key
        bundle=cache/bundle_key;site=bundle/'site-packages';site.mkdir(parents=True);(site/'example.py').write_bytes(content);(site/'example.py').chmod(0o444);site.chmod(0o555)
        (bundle/'manifest.json').write_bytes(H.canonical(manifest));(bundle/'manifest.json').chmod(0o444);bundle.chmod(0o555)
        (cache/(key+'.identity.json')).write_bytes(H.canonical(identity));(cache/(key+'.identity.json')).chmod(0o600)
        (cache/(key+'.helper.py')).write_bytes(helper_data);(cache/(key+'.helper.py')).chmod(0o444)
        (cache/(key+'.json')).write_bytes(H.canonical({'bundle_key':bundle_key,'lock_sha256':'d'*64,'proof_sha256':'e'*64}));(cache/(key+'.json')).chmod(0o600)
        self.request.update(expected_input_key=key,expected_bundle_key=bundle_key);self.requestpath.write_bytes(H.canonical(self.request))
        current=self.root/'new-installed-helper.py';current.write_text('raise RuntimeError("new helper must not execute inherited environment")\n')
        real_lstat=Path.lstat;real_read=Path.read_text
        def root_stat(path,*a,**kw):
            values=list(real_lstat(path,*a,**kw));values[4]=0;return os.stat_result(values)
        def read(path,*a,**kw):
            if str(path)=='/proc/self/cgroup':return '0::/system.slice/'+R.python_unit(self.job)
            return real_read(path,*a,**kw)
        with patch.object(R,'DEPENDENCIES',deps),patch.object(R,'PYTHON_HELPER',current),patch.object(R,'python_identity',REAL_IDENTITY),patch.object(Path,'lstat',root_stat),patch.object(Path,'read_text',read),patch.object(R.os,'geteuid',return_value=0),patch.object(R.os,'chown'),patch.object(R,'allocated_storage',return_value=0),patch.object(R.shutil,'disk_usage',return_value=SimpleNamespace(free=100*1024**3)),patch.object(R,'python_sandbox_run',side_effect=AssertionError('cached replay must not resolve or import again')),patch.object(R,'python_toolkit_identity',side_effect=AssertionError('current pytest pointer must not select inherited environment')):
            stage,request=R.python_request(self.job);chosen=R.python_identity(stage,request)
            self.assertEqual(chosen,identity);self.assertEqual((stage/'helper.py').read_bytes(),helper_data)
            R.completion_write(stage/'receipt.json',dict(R.python_binding(request,chosen),state='recovering'))
            self.assertEqual(R.python_dependencies_execute(self.job),0)
            receipt=R.completion_json(stage/'receipt.json');self.assertEqual(receipt['bundle_key'],bundle_key)
            self.assertEqual(R.python_project_bundle(self.job),bundle)

    def test_failure_evidence_is_bounded_and_labelled(self):
        path=self.root/'failure.log';path.write_text('x'*20000+'\nResolutionImpossible: project-dep needs transitive-dep<2\n');path.chmod(0o600)
        text=R.python_failure_diagnostic(path)
        self.assertTrue(text.startswith('Untrusted isolated prerequisite output'))
        self.assertIn('ResolutionImpossible',text);self.assertLess(len(text),12200)
        path.write_bytes(b'\xff'*12000)
        self.assertLess(len(R.python_failure_diagnostic(path).encode()),16384)
        path.unlink();path.symlink_to('/etc/passwd')
        with self.assertRaises(ValueError):R.python_failure_diagnostic(path)

    def test_probe_rejects_symlink_fifo_hardlink_and_oversize_without_read(self):
        path=self.root/'proof.json'
        self.identity['runtime']={'python_version':'3.13.5'}
        for kind in ('symlink','fifo','hardlink','oversize'):
            with self.subTest(kind=kind):
                if kind=='symlink':path.symlink_to('/etc/passwd')
                elif kind=='fifo':os.mkfifo(path)
                elif kind=='hardlink':os.link(self.requestpath,path)
                else:path.write_bytes(b'x'*32769)
                with patch.object(Path,'read_text',side_effect=AssertionError('must not read unsafe proof')):
                    with self.assertRaises(R.PythonUnsupported):R.python_probe_receipt(path,self.identity,self.request)
                path.unlink()

    def test_stop_waits_for_inherited_launch_client_after_parent_dies(self):
        import fcntl,time,sys
        stage,_=R.python_request(self.job);guard=stage/'guard';ready=self.root/'ready';release=self.root/'release'
        child="import pathlib,sys,time;pathlib.Path(sys.argv[1]).touch();deadline=time.monotonic()+8;\nwhile not pathlib.Path(sys.argv[2]).exists() and time.monotonic()<deadline:time.sleep(.01)"
        parent="import fcntl,sys,subprocess;f=open(sys.argv[1],'a');fcntl.flock(f,fcntl.LOCK_EX);p=subprocess.Popen([sys.executable,'-c',sys.argv[4],sys.argv[2],sys.argv[3]],pass_fds=(f.fileno(),));p.wait()"
        process=subprocess.Popen([sys.executable,'-c',parent,str(guard),str(ready),str(release),child])
        try:
            deadline=time.monotonic()+4
            while not ready.exists() and time.monotonic()<deadline:time.sleep(.01)
            self.assertTrue(ready.exists());process.kill();process.wait()
            with guard.open('a') as candidate:
                with self.assertRaises(BlockingIOError):fcntl.flock(candidate,fcntl.LOCK_EX|fcntl.LOCK_NB)
            result=[]
            with patch.object(R,'python_active',return_value=False):
                stopper=threading.Thread(target=lambda:result.append(R.python_dependencies_stop(self.job)))
                stopper.start();time.sleep(.05);self.assertTrue(stopper.is_alive())
                self.assertTrue((stage/'cancel.json').exists())
                release.touch();stopper.join(timeout=3);self.assertFalse(stopper.is_alive())
            self.assertEqual(result,[{'state':'stopped'}])
        finally:
            release.touch()
            if process.poll() is None:process.kill();process.wait()


class SandboxTests(unittest.TestCase):
    def test_real_pip_transitive_lock_offline_imports_and_pytest(self):
        if not shutil.which('bwrap') or not R.PYTHON_PIP_SOURCE.exists():self.skipTest('sandbox or installed trusted pip unavailable')
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);fetch=root/'fetch';fetch.mkdir();tooling=root/'tooling';proof=root/'proof';proof.mkdir();wheels=root/'registry';wheels.mkdir()
            H.snapshot_pip(R.PYTHON_PIP_SOURCE,tooling)
            # Build exact wheels from the already checksum-verified local pytest
            # runtime. No network, project builds or package imports on the host.
            base=load('pytest_provision',HERE/'provision-python-test-bundle.py')
            versions,contents=base.collect(R.PYTHON_PIP_SOURCE)
            for name,version in versions.items():
                matches=list(R.PYTHON_PIP_SOURCE.glob(name.replace('-','_')+'-'+version+'.dist-info/RECORD'))
                self.assertEqual(len(matches),1)
                selected={row[0]:contents[row[0]][0] for row in csv.reader(io.StringIO(matches[0].read_text())) if row[0] in contents}
                wheel_data(wheels,name,version,selected)
            wheel(wheels,'project_dep',files={'project_dep/__init__.py':b'import transitive_dep\nVALUE=transitive_dep.VALUE\nimport os\nassert not os.path.exists("/dependency.sock")\nassert not os.path.exists("/home/admin")\n'},requires=['transitive-dep>=1.0,<2'])
            wheel(wheels,'transitive_dep')
            wheel(wheels,'transitive_dep','2.0')
            infos=[H.wheel_info(path) for path in wheels.iterdir()]
            routes={row['route']:(wheels/row['filename']).read_bytes() for row in infos};calls=[];faults={}
            class Handler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    calls.append(self.path)
                    if self.path in faults:self.send_error(faults[self.path]);return
                    if self.path.startswith('/python/simple/'):
                        name=self.path.rstrip('/').rsplit('/',1)[-1]
                        body=('\n'.join('<a href="'+row['route']+'#sha256='+row['sha256']+'">'+row['filename']+'</a>' for row in infos if row['name']==name)).encode()
                        if not body and name!='native-only':
                            self.send_response(404);self.send_header('X-Lectern-Python-Registry','package-not-found');self.end_headers();return
                        self.send_response(200);self.send_header('Content-Type','text/html');self.end_headers();self.wfile.write(body)
                    elif self.path in routes:
                        body=routes[self.path];self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
                    else:self.send_error(404)
                def log_message(self,*args):pass
            class Server(socketserver.ThreadingMixIn,socketserver.UnixStreamServer):daemon_threads=True
            sock=root/'registry.sock'
            server=Server(str(sock),Handler);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            self.addCleanup(server.server_close);self.addCleanup(server.shutdown)
            request={'input_key':'a'*64,'requirements':['project-dep==1.0'],'imports':['project_dep'], 'tooling_requirements':[name+'=='+version for name,version in versions.items()]}
            (fetch/'input.json').write_bytes(H.canonical(request))
            replacements={'/tmp/python-fetch':str(fetch),'/tmp/python-tooling':str(tooling),'/tmp/python-helper':str((HERE/'python-project-dependencies.py').resolve()),'/tmp/python-proof':str(proof),'/tmp/python-dependency.sock':str(sock)}
            def execute(command,probe=False,custom=None):
                args=[replacements.get(x,x) for x in R.python_sandbox_command(command,probe)]
                if custom is not None:args[-1]=custom
                result=subprocess.run(args,capture_output=True,text=True,timeout=120)
                return result
            result=execute('resolve');self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            frozen=(fetch/'lock.json').read_bytes();lock=json.loads(frozen)
            self.assertEqual(len(lock['wheels']),7)
            self.assertIn('transitive-dep',[row['name'] for row in lock['wheels']])
            (fetch/'wheels'/next(row['filename'] for row in lock['wheels'] if row['name']=='transitive-dep')).unlink()
            calls.clear();result=execute('resolve');self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertEqual((fetch/'lock.json').read_bytes(),frozen)
            self.assertTrue(calls);self.assertFalse(any('/simple/' in call for call in calls),'frozen lock must not re-resolve')
            result=execute('install');self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            H.verify_install(fetch)
            (fetch/'manifest.json').write_text('{"fixture":"unified"}')
            alias_code='import pytest,project_dep,os,json;assert project_dep.VALUE==42;assert os.stat("/opt/python-project/project_dep/__init__.py").st_ino==os.stat("/opt/python-test/project_dep/__init__.py").st_ino;assert json.load(open("/opt/python-test-runtime.json"))==json.load(open("/opt/python-project-runtime.json"));assert os.environ["LECTERN_PYTHON_TEST_RUNTIME_STATUS"]=="verified";assert not os.access("/opt/python-test",os.W_OK)'
            alias_command=['/usr/bin/bwrap','--unshare-all','--die-with-parent','--ro-bind','/usr','/usr','--ro-bind','/lib','/lib','--ro-bind','/lib64','/lib64','--proc','/proc','--dev','/dev','--tmpfs','/tmp','--clearenv']+R.python_project_mount(fetch)+['--setenv','PYTHONPATH','/opt/python-test','--','/usr/bin/python3','-B','-c',alias_code]
            alias=subprocess.run(alias_command,capture_output=True,text=True,timeout=30)
            self.assertEqual(alias.returncode,0,alias.stdout+alias.stderr)
            # Real worker interpreter integration: project PYTHONPATH cannot
            # remove the verified site, including absolute/isolated child Python.
            work=root/'worker';pkg=work/'pkg';pkg.mkdir(parents=True)
            (pkg/'project_dep.py').write_text('VALUE=99\n')
            (work/'test_runtime.py').write_text('import pytest,project_dep\ndef test_project_first(): assert project_dep.VALUE==99\n')
            worker_base=['/usr/bin/bwrap','--unshare-all','--die-with-parent','--clearenv','--ro-bind','/usr','/usr','--ro-bind','/bin','/bin','--ro-bind','/lib','/lib','--ro-bind','/lib64','/lib64','--proc','/proc','--dev','/dev','--tmpfs','/tmp','--tmpfs','/home','--ro-bind',str(work),'/work','--chdir','/work','--setenv','HOME','/tmp','--setenv','PATH','/usr/bin:/bin']
            for runtime_kind in ('pytest','project'):
                with self.subTest(runtime_kind=runtime_kind):
                    if runtime_kind=='pytest':
                        with patch.object(R,'python_test_bundle',return_value=fetch):mounts=R.python_test_runtime_mount()
                    else:mounts=R.python_project_mount(fetch)
                    self.assertNotIn('PYTHONPATH',mounts)
                    code='import sys,os,subprocess,importlib.util,pytest,project_dep,packaging,pygments,apt_pkg;assert project_dep.VALUE==99;assert apt_pkg.__file__.startswith("/usr/lib/python3/dist-packages/");assert packaging.__file__.startswith("/usr/local/lib/python3.13/dist-packages/");assert pygments.__file__.startswith("/usr/local/lib/python3.13/dist-packages/");assert not os.path.exists("/home/admin");assert not os.access("/usr/local/lib/python3.13/dist-packages",os.W_OK);child="import pytest,project_dep;assert project_dep.VALUE==42";subprocess.run([sys.executable,"-I","-c",child],check=True);subprocess.run(["/usr/bin/python3","-c",child],env={},check=True);assert pytest.__file__.startswith("/usr/local/lib/python3.13/dist-packages/")'
                    common=worker_base+mounts+['--setenv','PYTHONPATH','/work/pkg']
                    check=subprocess.run(common+['--','python3','-B','-c',code],capture_output=True,text=True,timeout=30)
                    self.assertEqual(check.returncode,0,check.stdout+check.stderr)
                    check=subprocess.run(common+['--','python3','-m','pytest','-q','-p','no:cacheprovider','test_runtime.py'],capture_output=True,text=True,timeout=30)
                    self.assertEqual(check.returncode,0,check.stdout+check.stderr)
                    isolated=subprocess.run(common+['--','/usr/bin/python3','-I','-m','pytest','--version'],capture_output=True,text=True,timeout=30)
                    self.assertEqual(isolated.returncode,0,isolated.stdout+isolated.stderr)
                    self.assertIn('pytest ',isolated.stdout)
                    optout=subprocess.run(common+['--','/usr/bin/python3','-I','-S','-c','import importlib.util;assert importlib.util.find_spec("pytest") is None'],capture_output=True,text=True,timeout=30)
                    self.assertEqual(optout.returncode,0,optout.stdout+optout.stderr)
            result=execute('probe',True);self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertEqual(json.loads((proof/'probe.json').read_text())['network'],'unshared-no-socket')
            (fetch/'input.json').write_bytes(H.canonical(dict(request,imports=['missing_project_module'])))
            result=execute('probe',True);self.assertEqual(result.returncode,2,result.stdout+result.stderr)
            self.assertIn('import_unavailable',result.stderr);self.assertIn('missing_project_module',result.stderr)
            (fetch/'input.json').write_bytes(H.canonical(request))
            code='import sys;sys.path.insert(0,"/fetch/site-packages");import pytest;sys.exit(pytest.main(["-q","-p","no:cacheprovider","/fetch/test_offline.py"]))'
            import shlex
            command='/usr/bin/python3 -I -S -B -c '+shlex.quote(code)
            (fetch/'test_offline.py').write_text('from project_dep import VALUE\ndef test_value(): assert VALUE == 42\n')
            result=execute('probe',True,command);self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            (fetch/'test_offline.py').write_text('def test_value(): assert False\n')
            result=execute('probe',True,command);self.assertEqual(result.returncode,1,result.stdout+result.stderr)
            for case,requirements in [('unknownpackage',['not-a-real-package==1.0']),('nonexistentpin',['project-dep==99.0']),('no_pure_wheel',['native-only==1.0']),('conflictingdeps',['project-dep==1.0','transitive-dep==2.0'])]:
                with self.subTest(case=case):
                    (fetch/'lock.json').unlink(missing_ok=True)
                    (fetch/'input.json').write_bytes(H.canonical(dict(request,requirements=requirements)))
                    result=execute('resolve');self.assertEqual(result.returncode,2,result.stdout+result.stderr)
                    self.assertIn('no compatible pure-wheel resolution',result.stderr)
            (fetch/'input.json').write_bytes(H.canonical(request));faults['/python/simple/project-dep/']=502
            result=execute('resolve');self.assertEqual(result.returncode,1,result.stdout+result.stderr)
            self.assertIn('Registry transport unavailable',result.stderr)
            print('Real isolated pip: transitive closure PASS; frozen lock reuse PASS; offline imports and pytest PASS; deliberate assertion failure detected; nonexistent pin/native-only/conflicts diagnosed; transport remains retryable')


if __name__=='__main__':unittest.main()
