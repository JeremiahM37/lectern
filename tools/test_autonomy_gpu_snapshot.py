import importlib.util,tempfile,unittest,tarfile
from pathlib import Path
from unittest import mock
s=importlib.util.spec_from_file_location('gpu_snapshot',Path(__file__).with_name('autonomy-gpu-snapshot.py'));H=importlib.util.module_from_spec(s);s.loader.exec_module(H)
class Tests(unittest.TestCase):
 def setUp(self):
  self.t=tempfile.TemporaryDirectory();self.root=Path(self.t.name);self.work=self.root/'work';self.work.mkdir();(self.work/'fix.py').write_text('current fix')
 def tearDown(self):self.t.cleanup()
 def test_captures_current_bytes_separately_from_old_archive(self):
  (self.root/'artifact.tar.gz').write_bytes(b'old retained archive')
  receipt=H.capture(self.work,self.root/'snapshot')
  (self.work/'fix.py').write_text('later change')
  with tarfile.open(self.root/'snapshot/source.tar.gz') as archive:self.assertEqual(archive.extractfile('work/fix.py').read(),b'current fix')
  self.assertEqual((self.root/'artifact.tar.gz').read_bytes(),b'old retained archive');self.assertEqual(receipt['state'],'ready');self.assertIn('not an atomic',receipt['scope'])
 def test_symlink_and_hardlink_refused_without_outside_read(self):
  secret=self.root/'outside';secret.write_text('not captured');(self.work/'link').symlink_to(secret)
  with self.assertRaisesRegex(ValueError,'links'):H.capture(self.work,self.root/'bad')
  (self.work/'link').unlink();import os;os.link(secret,self.work/'link')
  with self.assertRaisesRegex(ValueError,'links'):H.capture(self.work,self.root/'hard')
 def test_mutating_source_has_no_ready_receipt(self):
  original=H.scan
  def scan(source,destination=None,*args,**kwargs):
   rows=original(source,destination,*args,**kwargs)
   if destination is not None:(self.work/'fix.py').write_text('changed mid capture')
   return rows
  with mock.patch.object(H,'scan',side_effect=scan):
   with self.assertRaisesRegex(ValueError,'changed across'):H.capture(self.work,self.root/'racy')
 def test_explicit_selection_omits_runtime_links_with_manifest(self):
  runtime=self.work/'.venv';runtime.mkdir();(runtime/'python').symlink_to('/usr/bin/python3')
  result=H.capture(self.work,self.root/'selected',['fix.py'])
  self.assertEqual(result['selected_paths'],['fix.py']);self.assertEqual(result['excluded_paths'],['.venv'])
  with tarfile.open(self.root/'selected/source.tar.gz') as archive:self.assertEqual(archive.getnames(),['work','work/fix.py'])
  self.assertEqual(len(result['manifest_sha256']),64)
 def test_selection_escape_overlap_and_missing_rejected(self):
  for paths in (['../secret'],['/etc'],['.','fix.py'],['a','a/b'],['missing']):
   with self.subTest(paths=paths):
    with self.assertRaises(ValueError):H.scan(self.work,source_paths=paths)
if __name__=='__main__':unittest.main()
