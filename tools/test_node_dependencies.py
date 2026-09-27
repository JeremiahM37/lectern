import base64
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import unittest

spec = importlib.util.spec_from_file_location('node_deps', Path(__file__).with_name('node-dependencies.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def packed(members):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode='w:gz') as tar:
        for name, content, kind in members:
            info = tarfile.TarInfo(name)
            info.type = kind
            info.size = len(content) if kind == tarfile.REGTYPE else 0
            if kind == tarfile.SYMTYPE:
                info.linkname = '/etc/shadow'
            tar.addfile(info, io.BytesIO(content) if kind == tarfile.REGTYPE else None)
    return out.getvalue()


class NodeMetadata(unittest.TestCase):
    def setUp(self):
        self.manifest = {'dependencies': {'@example/cli': '1.2.3'}}
        self.entry = {'version': '1.2.3', 'resolved': 'https://registry.npmjs.org/@example/cli/-/cli-1.2.3.tgz',
                      'integrity': 'sha512-' + base64.b64encode(bytes(64)).decode()}
        self.lock = {'lockfileVersion': 3, 'packages': {'': copy.deepcopy(self.manifest), 'node_modules/@example/cli': self.entry}}

    def validate(self):
        return m.validate_lock(json.dumps(self.manifest).encode(), json.dumps(self.lock).encode())

    def test_nested_scoped_peer_optional_inventory_retained(self):
        self.lock['packages']['node_modules/@example/cli/node_modules/child'] = dict(self.entry, version='2.0.0', resolved='https://registry.npmjs.org/child/-/child-2.0.0.tgz', peer=True, optional=True)
        self.assertEqual(len(self.validate()['packages']), 2)

    def test_manifest_drift(self):
        self.manifest['dependencies']['@example/cli'] = '1.2.4'
        with self.assertRaises(ValueError): self.validate()

    def test_urls_and_paths(self):
        for url in ('https://registry.npmjs.org.evil/x', 'https://user@registry.npmjs.org/@example/cli/-/cli-1.2.3.tgz', 'https://registry.npmjs.org/@example/cli/-/cli-1.2.3.tgz?q=x', 'file:///tmp/x', 'git+https://example.com/x'):
            with self.subTest(url=url), self.assertRaises(ValueError):
                m.registry_tarball(url, '@example/cli', '1.2.3')
        for path in ('node_modules/../escape', 'node_modules/a/../../b', 'node_modules/@bad', '/node_modules/a', 'node_modules/a/other/b'):
            with self.subTest(path=path), self.assertRaises(ValueError): m.package_path(path)

    def test_links_alias_and_duplicate_json(self):
        for changes in ({'link': True}, {'inBundle': True}, {'name': 'other'}, {'version': '^1.2.3'}, {'integrity': 'sha1-abc'}):
            old = dict(self.entry)
            self.entry.update(changes)
            with self.subTest(changes=changes), self.assertRaises(ValueError): self.validate()
            self.entry.clear(); self.entry.update(old)
        with self.assertRaises(ValueError): m.read_json(b'{"a":1,"a":2}')

    def inspect(self, members, identity=None):
        data = packed(members)
        entry = {'name': '@example/cli', 'version': '1.2.3', 'integrity': 'sha512-' + base64.b64encode(hashlib.sha512(data).digest()).decode()}
        if identity: entry.update(identity)
        return m.validate_tarball(data, entry)

    def package(self, **extra):
        return ('package/package.json', json.dumps(dict(name='@example/cli', version='1.2.3', **extra)).encode(), tarfile.REGTYPE)

    def test_lifecycle_reported_never_executed(self):
        proof = self.inspect([self.package(scripts={'postinstall': 'cat /etc/shadow', 'test': 'node test.js'}), ('package/binding.gyp', b'{}', tarfile.REGTYPE)])
        self.assertEqual(proof['lifecycle_scripts'], {'postinstall': 'cat /etc/shadow'})
        self.assertTrue(proof['implicit_node_gyp'])

    def test_malicious_tar_members_and_duplicate(self):
        for bad in (('package/../../escape', b'x', tarfile.REGTYPE), ('package/link', b'', tarfile.SYMTYPE), ('package/package.json', b'{}', tarfile.REGTYPE)):
            with self.subTest(bad=bad), self.assertRaises(ValueError): self.inspect([self.package(), bad])

    def test_identity_and_integrity(self):
        with self.assertRaises(ValueError): self.inspect([self.package()], {'name': 'other'})
        with self.assertRaises(ValueError): m.validate_tarball(b'wrong bytes', {'integrity': self.entry['integrity']})


if __name__ == '__main__': unittest.main()
