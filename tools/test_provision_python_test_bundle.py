"""Curated runtime integrity and real network-isolated pytest acceptance."""
import base64
import csv
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("python_bundle", Path(__file__).with_name("provision-python-test-bundle.py"))
BUNDLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUNDLE)


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / "site-packages"
        self.source.mkdir()
        for name, modules in BUNDLE.PACKAGES.items():
            dist = self.source / (name + "-1.0.dist-info")
            dist.mkdir()
            files = {dist / "METADATA": f"Metadata-Version: 2.1\nName: {name}\nVersion: 1.0\n".encode()}
            for module in modules:
                path = self.source / module if module.endswith(".py") else self.source / module / "__init__.py"
                path.parent.mkdir(exist_ok=True)
                files[path] = b"raise AssertionError('package code must never run while provisioning')\n"
            rows = []
            for path, data in files.items():
                path.write_bytes(data)
                rows.append([path.relative_to(self.source).as_posix(), "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip("="), str(len(data))])
            rows.append([dist.relative_to(self.source).as_posix() + "/RECORD", "", ""])
            self.record(dist / "RECORD", rows)

    def record(self, path, rows):
        with path.open("w", newline="") as stream:
            csv.writer(stream).writerows(rows)

    def change_record(self, mutate):
        path = self.source / "pytest-1.0.dist-info/RECORD"
        with path.open(newline="") as stream:
            rows = list(csv.reader(stream))
        mutate(rows)
        self.record(path, rows)

    def test_deterministic_key_and_modes_without_package_execution(self):
        first, manifest = BUNDLE.provision(self.source, self.root / "one", stage=True)
        second, other = BUNDLE.provision(self.source, self.root / "two", stage=True)
        self.assertEqual(manifest, other)
        body = dict(manifest)
        key = body.pop("key")
        self.assertEqual(key, hashlib.sha256(BUNDLE.canonical(body)).hexdigest())
        self.assertEqual(first.name, second.name)
        for path in first.rglob("*"):
            self.assertEqual(path.stat().st_mode & 0o777, 0o555 if path.is_dir() else 0o444)
        for entry in manifest["files"]:
            actual = first / "site-packages" / entry["path"]
            self.assertEqual(entry["sha256"], hashlib.sha256(actual.read_bytes()).hexdigest())
            self.assertEqual(entry["record_verified"], not entry["path"].endswith("/RECORD"))
        with self.assertRaises(BUNDLE.BundleError):
            BUNDLE.provision(self.source, self.root / "one", stage=True)

    def test_tampered_file(self):
        path = self.source / "pytest/__init__.py"
        path.write_bytes(b"x" * path.stat().st_size)
        with self.assertRaisesRegex(BUNDLE.BundleError, "checksum"):
            BUNDLE.collect(self.source)

    def test_unsupported_checksum(self):
        self.change_record(lambda rows: rows[0].__setitem__(1, "md5=abc"))
        with self.assertRaisesRegex(BUNDLE.BundleError, "checksum"):
            BUNDLE.collect(self.source)

    def test_missing_checksum(self):
        self.change_record(lambda rows: rows[0].__setitem__(1, ""))
        with self.assertRaisesRegex(BUNDLE.BundleError, "checksum"):
            BUNDLE.collect(self.source)

    def test_traversal(self):
        self.change_record(lambda rows: rows.append(["../../../outside.py", "sha256=abc", "1"]))
        with self.assertRaisesRegex(BUNDLE.BundleError, "invalid RECORD path"):
            BUNDLE.collect(self.source)

    def test_unknown_record_file(self):
        self.change_record(lambda rows: rows.append(["evil.py", "sha256=abc", "1"]))
        with self.assertRaisesRegex(BUNDLE.BundleError, "unexpected package"):
            BUNDLE.collect(self.source)

    def test_unrecorded_module_file(self):
        (self.source / "pytest/evil.py").write_text("bad")
        with self.assertRaisesRegex(BUNDLE.BundleError, "unrecorded"):
            BUNDLE.collect(self.source)

    def test_symlink(self):
        source = self.source / "pytest/__init__.py"
        contents = source.read_bytes()
        source.unlink()
        outside = self.root / "outside.py"
        outside.write_bytes(contents)
        source.symlink_to(outside)
        with self.assertRaisesRegex(BUNDLE.BundleError, "symlink"):
            BUNDLE.collect(self.source)

    def test_hardlink(self):
        os.link(self.source / "pytest/__init__.py", self.root / "other.py")
        with self.assertRaisesRegex(BUNDLE.BundleError, "independent regular"):
            BUNDLE.collect(self.source)

    def test_dist_info_link(self):
        dist = self.source / "pytest-1.0.dist-info"
        external = self.root / "external"
        dist.rename(external)
        dist.symlink_to(external, target_is_directory=True)
        with self.assertRaisesRegex(BUNDLE.BundleError, "symlink"):
            BUNDLE.collect(self.source)

    def test_duplicate_record_entry(self):
        self.change_record(lambda rows: rows.append(rows[0]))
        with self.assertRaisesRegex(BUNDLE.BundleError, "duplicate"):
            BUNDLE.collect(self.source)

    def test_absolute_record_entry(self):
        self.change_record(lambda rows: rows.append(["/etc/passwd", "sha256=abc", "1"]))
        with self.assertRaisesRegex(BUNDLE.BundleError, "invalid RECORD path"):
            BUNDLE.collect(self.source)

    def test_linked_destination(self):
        real = self.root / "real"
        real.mkdir()
        linked = self.root / "link"
        linked.symlink_to(real, target_is_directory=True)
        with self.assertRaisesRegex(BUNDLE.BundleError, "symlink"):
            BUNDLE.provision(self.source, linked, stage=True)

    def test_production_trust_stops_at_dependency_tree(self):
        mount = self.root / "bulk"
        destination = mount / "lectern-autonomy/dependencies/python"
        destination.mkdir(parents=True)
        trusted = {destination, destination.parent, destination.parent.parent}
        original_stat = Path.stat
        untrusted = set()

        def ownership(path, *args, **kwargs):
            fields = list(original_stat(path, *args, **kwargs))
            fields[4] = 0 if path in trusted and path not in untrusted else 1000
            return os.stat_result(fields)

        with patch.object(Path, "stat", ownership), patch.object(BUNDLE.os, "geteuid", return_value=0):
            # The mountpoint and every upper ancestor are admin-owned here;
            # none should become a root-ownership requirement.
            BUNDLE.validate_production_destination(destination)
            for directory in trusted:
                with self.subTest(directory=directory):
                    untrusted.add(directory)
                    with self.assertRaisesRegex(BUNDLE.BundleError, "untrusted production"):
                        BUNDLE.validate_production_destination(destination)
                    untrusted.clear()

    def test_never_changes_active_marker(self):
        destination = self.root / "destination"
        destination.mkdir()
        marker = destination / "active.json"
        marker.write_text('{"key":"untouched"}')
        BUNDLE.provision(self.source, destination, stage=True)
        self.assertEqual(marker.read_text(), '{"key":"untouched"}')

    def test_expected_entrypoint_and_generated_cache_skipped(self):
        self.change_record(lambda rows: rows.extend([["../../../bin/pytest", "sha256=ignored", "10"], ["pytest/__pycache__/__init__.cpython-313.pyc", "", ""]]))
        _, files = BUNDLE.collect(self.source)
        self.assertFalse(any("__pycache__" in name or "../" in name for name in files))


class OfflineAcceptance(unittest.TestCase):
    def test_real_bwrap_pytest_pass_and_fail(self):
        if not shutil.which("bwrap") or not BUNDLE.DEFAULT_SOURCE.exists():
            self.skipTest("installed curated runtime/bwrap unavailable")
        with tempfile.TemporaryDirectory(prefix="pytest-offline-") as temporary:
            root = Path(temporary)
            bundle, manifest = BUNDLE.provision(destination=root / "bundles", stage=True)
            work = root / "work"
            work.mkdir()
            test_file = work / "test_offline.py"
            base = ["bwrap", "--die-with-parent", "--unshare-net", "--unshare-pid", "--new-session", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--ro-bind", str(bundle), "/opt/python", "--ro-bind", str(work), "/work", "--chdir", "/work", "--clearenv", "--setenv", "PATH", "/usr/bin", "--setenv", "HOME", "/tmp", "--setenv", "PYTHONPATH", "/opt/python/site-packages", "--setenv", "PYTHONDONTWRITEBYTECODE", "1", "--setenv", "PYTEST_DISABLE_PLUGIN_AUTOLOAD", "1", "/usr/bin/python3", "-m", "pytest", "-p", "no:cacheprovider", "-q", "test_offline.py"]
            for expression, expected in [("2 + 2 == 4", 0), ("2 + 2 == 5", 1)]:
                test_file.write_text("import socket\nimport pytest\ndef test_offline():\n    assert " + expression + "\n    assert pytest.__file__.startswith('/opt/python/')\n    with pytest.raises(OSError):\n        socket.create_connection(('1.1.1.1', 443), timeout=0.1)\n")
                result = subprocess.run(base, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
                self.assertEqual(result.returncode, expected, result.stdout)
            print("Offline bwrap pytest: positive PASS; deliberate failing assertion detected; bundle " + manifest["key"])


if __name__ == "__main__":
    unittest.main()
