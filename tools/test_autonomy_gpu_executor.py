"""Data/command boundary tests; real sandbox proof is a separate root fixture."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock
import stat

s=importlib.util.spec_from_file_location('gpu_executor',Path(__file__).with_name('autonomy-gpu-executor.py'))
E=importlib.util.module_from_spec(s);s.loader.exec_module(E)

class ExecutorTests(unittest.TestCase):
    def setUp(self):
        disk_patch=mock.patch.object(E.shutil,'disk_usage',return_value=type('Disk',(),{'free':2*E.L.STORAGE_FLOOR})());disk_patch.start();self.addCleanup(disk_patch.stop)
        self.helper_bytes={'executor':b'executor fixture','lease':b'lease fixture'}
        self.helpers={k+'_sha256':hashlib.sha256(v).hexdigest() for k,v in self.helper_bytes.items()}
        actual_sources=E.L.helper_sources
        actual_identity=E.L.helper_identity
        self.source_patch=mock.patch.object(E.L,'helper_sources',return_value=dict(self.helper_bytes));self.source_mock=self.source_patch.start();self.addCleanup(self.source_patch.stop)
        self.source_mock.side_effect=lambda directory=None,owner=0: actual_sources(directory,owner) if directory is not None else self.source_mock.return_value
        self.helper_patch=mock.patch.object(E.L,'helper_identity',return_value=dict(self.helpers));self.helper_mock=self.helper_patch.start();self.addCleanup(self.helper_patch.stop)
        self.helper_mock.side_effect=lambda directory=None,owner=0: actual_identity(directory,owner) if directory is not None else self.helper_mock.return_value
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.owner=os.getuid()
        source=self.root/'runtime-source';(source/'usr/bin').mkdir(parents=True)
        (source/'usr/bin/python3').write_bytes(b'fixture executable only');(source/'usr/bin/python3').chmod(0o755)
        self.manifest=E.register_runtime(source,self.root/'runtimes',self.owner)
        self.archive=self.root/'source.tar.gz'
        with tarfile.open(self.archive,'w:gz') as tf:
            m=tarfile.TarInfo('work/project.py');m.size=8;tf.addfile(m,io.BytesIO(b'VALUE=7\n'))
        self.request={'source_archive_sha256':E.sha(self.archive),'runtime_key':self.manifest['key'],'profile':'device-free30','script':'print(7)','argv':[],'helpers':dict(self.helpers)}
        self.request['binding']={'schema_version':1,'target':E.L.TARGET,'run_id':'a'*64,'request_sha256':E.L.digest(self.request),'generation':1,'guest_boot_id':E.L.boot_id()}
    def tearDown(self):
        for p in self.root.rglob('*'):
            if p.is_dir():p.chmod(0o700)
        self.tmp.cleanup()
    def prepare(self):return E.prepare(self.request,self.archive,self.root/'state',self.root/'runtimes',self.owner)
    def test_exact_runtime_source_and_fixed_socket_free_command(self):
        run=self.prepare();self.assertEqual(self.prepare(),run)
        rootfs,m=E.runtime(self.manifest['key'],self.root/'runtimes',self.owner)
        command=E.sandbox(run,self.request,rootfs,run/'scratch')
        self.assertIn('--unshare-all',command);self.assertIn('--clearenv',command)
        self.assertNotIn('--dev-bind',command);self.assertNotIn('--share-net',command)
        self.assertEqual(command[-3:],['/usr/bin/python3','-I','/entry.py'])
        self.assertEqual((run/'source/project.py').read_text(),'VALUE=7\n')
        self.assertEqual((run/'source/project.py').stat().st_mode&0o777,0o444)
    def test_gpu_discovery_is_readonly_and_excludes_process_and_driver_controls(self):
        run=self.prepare();request=dict(self.request,profile='gpu-screen600')
        actual_stat=os.stat
        def device(path,*args,**kwargs):
            if path not in ('/dev/kfd','/dev/dri/renderD128'):return actual_stat(path,*args,**kwargs)
            major,minor=(234,0) if path=='/dev/kfd' else (226,128)
            return type('Device',(),{'st_mode':stat.S_IFCHR|0o666,'st_rdev':os.makedev(major,minor)})()
        with mock.patch.object(E.os,'stat',side_effect=device), mock.patch.object(E.os.path,'realpath',return_value=E.GPU_PCI+'/drm/renderD128'):
            command=E.sandbox(run,request,self.root/'runtimes'/self.manifest['key']/'rootfs',run/'scratch')
        with mock.patch.object(E.os.path,'realpath',return_value='/sys/devices/foreign'):
            with self.assertRaisesRegex(ValueError,'render PCI mapping differs'):
                E.sandbox(run,request,self.root/'runtimes'/self.manifest['key']/'rootfs',run/'scratch')
        mounts=[command[i+1:i+3] for i,x in enumerate(command) if x=='--ro-bind']
        self.assertIn(['/sys/devices/virtual/kfd/kfd/topology']*2,mounts)
        self.assertIn(['/sys/devices/system/cpu']*2,mounts)
        self.assertIn(['/sys/devices/system/node']*2,mounts)
        for forbidden in ('/sys/class/kfd','/sys/devices/virtual/kfd/kfd/proc','/sys/kernel','/dev/dri/card0'):
            self.assertNotIn(forbidden,command)
        self.assertNotIn(['/sys','/sys'],mounts)
        self.assertIn(['--remount-ro','/sys'],[command[i:i+2] for i in range(len(command)-1)])
        for leaf in ('config','resource','resource0','rom','reset'):
            self.assertNotIn(E.GPU_PCI+'/'+leaf,command)
        self.assertIn([E.GPU_PCI+'/vendor']*2,mounts)
        self.assertIn(E.GPU_PCI+'/drm/renderD128/device',command)
        ordinary=E.sandbox(run,self.request,self.root/'rootfs',run/'scratch')
        self.assertFalse(any(x.startswith('/sys/') for x in ordinary))
    def test_gpu_mountpoints_are_frozen_and_part_of_runtime_identity(self):
        rootfs,_=E.runtime(self.manifest['key'],self.root/'runtimes',self.owner)
        for path in E.GPU_DISCOVERY:
            dest=rootfs/path.lstrip('/')
            self.assertTrue(dest.is_dir())
            self.assertEqual(dest.stat().st_mode&0o777,0o555)
            self.assertTrue(any(r['path']==path.lstrip('/') for r in self.manifest['files']))
        dest=rootfs/E.GPU_DISCOVERY[0].lstrip('/')
        dest.parent.chmod(0o755);dest.rmdir();dest.parent.chmod(0o555)
        with self.assertRaisesRegex(ValueError,'runtime content differs'):
            E.runtime(self.manifest['key'],self.root/'runtimes',self.owner)
    def test_helper_drift_refuses_preparation_and_execution(self):
        self.source_mock.return_value=dict(self.helper_bytes,executor=b'changed')
        with self.assertRaisesRegex(ValueError,'helper identity differs'):self.prepare()
        self.assertFalse((self.root/'state/runs').exists())
        self.source_mock.return_value=dict(self.helper_bytes);run=self.prepare()
        self.helper_mock.return_value=dict(self.helpers,lease_sha256='d'*64)
        with self.assertRaisesRegex(ValueError,'helper identity differs'):
            E.execute('a'*64,self.root/'state',self.root/'runtimes',self.owner,require_unit=False)
        self.assertFalse((run/'execution-started.json').exists())
    def test_prepared_helpers_preserve_selected_bytes_across_global_replacement(self):
        run=self.prepare()
        self.source_mock.return_value={'executor':b'next release','lease':b'next lease'}
        self.helper_mock.return_value={'executor_sha256':'a'*64,'lease_sha256':'b'*64}
        self.assertEqual(self.prepare(),run)
        for name,raw in self.helper_bytes.items():
            path=run/'helpers'/('lectern-autonomy-gpu-'+name+'.py')
            self.assertEqual(path.read_bytes(),raw)
            self.assertEqual(path.stat().st_mode&0o777,0o555)
            self.assertEqual(E.sha(path),self.request['helpers'][name+'_sha256'])
        self.assertEqual((run/'helpers').stat().st_mode&0o777,0o555)
        path=run/'helpers/lectern-autonomy-gpu-executor.py';path.chmod(0o755);path.write_bytes(b'tampered');path.chmod(0o555)
        with self.assertRaisesRegex(ValueError,'prepared GPU helper identity differs'):self.prepare()
    def test_storage_floor_blocks_new_copy_but_not_prepared_replay(self):
        low=type('Disk',(),{'free':E.L.STORAGE_FLOOR-1})()
        with mock.patch.object(E.shutil,'disk_usage',return_value=low):
            with self.assertRaisesRegex(ValueError,'storage headroom'):self.prepare()
        self.assertFalse((self.root/'state/runs').exists())
        run=self.prepare()
        with mock.patch.object(E.shutil,'disk_usage',return_value=low):self.assertEqual(self.prepare(),run)
    def test_wrong_archive_or_request_never_prepares(self):
        self.archive.write_bytes(b'changed')
        with self.assertRaises(ValueError):self.prepare()
        self.assertFalse((self.root/'state/runs').exists())
        self.request['source_archive_sha256']=E.sha(self.archive)
        with self.assertRaises(ValueError):self.prepare()
    def test_runtime_tamper_fails_before_execution(self):
        binary=self.root/'runtimes'/self.manifest['key']/'rootfs/usr/bin/python3'
        binary.chmod(0o755);binary.write_bytes(b'corrupt')
        with self.assertRaises(ValueError):self.prepare()
    def test_runtime_directory_permissions_are_authenticated(self):
        rootfs=self.root/'runtimes'/self.manifest['key']/'rootfs'
        self.assertTrue(any(r.get('path')=='.' and r.get('kind')=='directory' for r in self.manifest['files']))
        rootfs.chmod(0o777)
        with self.assertRaises(ValueError):self.prepare()
        rootfs.chmod(0o555)
        nested=rootfs/'usr';nested.chmod(0o755)
        with self.assertRaises(ValueError):self.prepare()
        nested.chmod(0o555)
        registry=self.root/'runtimes';registry.chmod(0o777)
        with self.assertRaises(ValueError):self.prepare()
        registry.chmod(0o755)
        bundle=registry/self.manifest['key'];moved=registry/'elsewhere';bundle.rename(moved);bundle.symlink_to(moved,target_is_directory=True)
        with self.assertRaises(ValueError):self.prepare()
        bundle.unlink();moved.rename(bundle)

    def test_archive_links_and_traversal_rejected(self):
        for name,typ,target in [('work/../../outside',tarfile.REGTYPE,''),('work/link',tarfile.SYMTYPE,'/etc/passwd'),('work/link',tarfile.LNKTYPE,'work/project.py')]:
            with self.subTest(name=name,type=typ):
                archive=self.root/'bad.tar.gz'
                with tarfile.open(archive,'w:gz') as tf:
                    row=tarfile.TarInfo(name);row.type=typ;row.linkname=target;tf.addfile(row)
                with self.assertRaises(ValueError):E.extract(archive,self.root/('extract-'+str(int(typ))))
    def test_execution_requires_owned_unit(self):
        self.prepare()
        with self.assertRaisesRegex(ValueError,'outside owned unit'):E.execute('a'*64,self.root/'state',self.root/'runtimes',self.owner)
    def test_relative_runtime_file_and_directory_links_preserved(self):
        source=self.root/'linked-good';(source/'usr/bin').mkdir(parents=True);(source/'lib/llvm/amdgcn').mkdir(parents=True)
        (source/'usr/bin/python3.11').write_bytes(b'ELF fixture');(source/'usr/bin/python3.11').chmod(0o755)
        (source/'usr/bin/python3').symlink_to('python3.11')
        (source/'lib/llvm/amdgcn/code').write_bytes(b'GPU data')
        (source/'llvm').symlink_to('lib/llvm',target_is_directory=True)
        (source/'amdgcn').symlink_to('lib/llvm/amdgcn',target_is_directory=True)
        manifest=E.register_runtime(source,self.root/'link-runtime',self.owner)
        rootfs,_=E.runtime(manifest['key'],self.root/'link-runtime',self.owner)
        self.assertEqual(os.readlink(rootfs/'llvm'),'lib/llvm')
        links=[r for r in manifest['files'] if r.get('kind')=='symlink']
        self.assertEqual(len(links),3)
        self.assertFalse(any(r['path'].startswith('llvm/') for r in manifest['files']))
        self.assertEqual((rootfs/'amdgcn/code').read_bytes(),b'GPU data')
        rootfs.chmod(0o755);(rootfs/'llvm').unlink();(rootfs/'llvm').symlink_to('lib/llvm/amdgcn');rootfs.chmod(0o555)
        with self.assertRaises(ValueError):E.runtime(manifest['key'],self.root/'link-runtime',self.owner)
    def test_runtime_links_escape_dangling_and_cycles_rejected(self):
        cases={'absolute':'/etc/passwd','escape':'../outside','dangling':'missing','self':'link'}
        for label,target in cases.items():
            with self.subTest(label=label):
                source=self.root/label;source.mkdir();(source/'link').symlink_to(target)
                with self.assertRaises(ValueError):E.register_runtime(source,self.root/('cache-'+label),self.owner)
        source=self.root/'dircycle';(source/'a').mkdir(parents=True);(source/'b').mkdir()
        (source/'a/link').symlink_to('../b',target_is_directory=True)
        (source/'b/link').symlink_to('../a',target_is_directory=True)
        with self.assertRaises(ValueError):E.register_runtime(source,self.root/'cache-cycle',self.owner)
        source=self.root/'ancestor';source.mkdir();(source/'link').symlink_to('.',target_is_directory=True)
        with self.assertRaises(ValueError):E.register_runtime(source,self.root/'cache-ancestor',self.owner)

    def test_runtime_source_symlink_does_not_copy_host(self):
        source=self.root/'linked';source.mkdir();(source/'host').symlink_to('/etc/passwd')
        with self.assertRaises(ValueError):E.register_runtime(source,self.root/'other-runtime',self.owner)

if __name__=='__main__':unittest.main()
