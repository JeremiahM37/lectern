import importlib.util,json,os,subprocess,tempfile,unittest
from pathlib import Path
s=importlib.util.spec_from_file_location('provision',Path(__file__).with_name('provision-node-runtime.py'));P=importlib.util.module_from_spec(s);s.loader.exec_module(P)
class NodeToolingRegistrationTests(unittest.TestCase):
 @unittest.skipUnless(os.geteuid()==0,'actual root registration and unprivileged read proof')
 def test_existing_private_cache_becomes_readable_only_after_registration(self):
  with tempfile.TemporaryDirectory(dir='/mnt/bulk') as d:
   root=Path(d);root.chmod(0o755);source=root/'selected';(source/'bin').mkdir(parents=True);(source/'lib/node_modules/npm/bin').mkdir(parents=True)
   # Administrator-selected fixture executable; no package code or host tools.
   node=source/'bin/node';node.write_text('#!/bin/sh\nif [ "$1" = "--version" ]; then echo v24.13.1; else echo 11.8.0; fi\n');node.chmod(0o755)
   (source/'lib/node_modules/npm/bin/npm-cli.js').write_text('fixture only\n')
   destination=root/'node-tooling';destination.mkdir(mode=0o700)
   manifest=P.provision(source,destination)
   self.assertEqual(destination.stat().st_mode&0o777,0o755)
   self.assertEqual((destination/manifest['key']).stat().st_mode&0o777,0o555)
   command=['/usr/sbin/runuser','-u','nobody','--','/usr/bin/python3','-c','import json,sys; a=json.load(open(sys.argv[1]+"/active.json")); m=json.load(open(sys.argv[1]+"/"+a["key"]+"/manifest.json")); assert m["key"]==a["key"]',str(destination)]
   subprocess.run(command,check=True,capture_output=True)
   self.assertEqual(P.provision(source,destination)['key'],manifest['key'])
if __name__=='__main__':unittest.main()
