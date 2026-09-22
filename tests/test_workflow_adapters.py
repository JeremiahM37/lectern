"""Exercise the bundled adapter and real upstream scripts in temporary projects."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "internal/workflows/bundled/spec-kit/workflow.py"
spec = importlib.util.spec_from_file_location("speckit_adapter", HELPER)
adapter = importlib.util.module_from_spec(spec)
sys.dont_write_bytecode = True
spec.loader.exec_module(adapter)


class WorkflowAdapterTests(unittest.TestCase):
    def test_all_modes_render_without_install_tokens(self):
        for mode in adapter.MODES:
            with self.subTest(mode=mode):
                text = adapter.render(mode)
                for token in ("{SCRIPT}", "{ARGS}", "$ARGUMENTS", "__SPECKIT_COMMAND_"):
                    self.assertNotIn(token, text)
                self.assertTrue(text.startswith("# Lectern Spec Kit: " + mode))
                self.assertNotIn("Read /memory/constitution.md", text)
                self.assertNotIn(".specify.specify", text)

    def test_prepare_preserves_user_files_and_real_scripts_work(self):
        with tempfile.TemporaryDirectory(prefix="spec-kit adapter ") as directory:
            root = Path(directory)
            (root / "AGENTS.md").write_text("existing project instructions\n")
            first = adapter.prepare(root)
            self.assertGreater(first, 5)
            template = root / ".specify/templates/plan-template.md"
            template.write_text("# User's existing plan template\n")
            self.assertEqual(adapter.prepare(root), 0)
            self.assertEqual(template.read_text(), "# User's existing plan template\n")
            self.assertEqual((root / "AGENTS.md").read_text(), "existing project instructions\n")
            result = subprocess.run(["bash", ".specify/scripts/bash/resolve-template.sh", "spec-template", "--json"], cwd=root, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Feature Specification", json.loads(result.stdout)["TEMPLATE_CONTENT"])
            feature = root / "specs/001-adapter-test"
            feature.mkdir(parents=True)
            (feature / "spec.md").write_text("# Test specification\n")
            (root / ".specify/feature.json").write_text(json.dumps({"feature_directory": "specs/001-adapter-test"}))
            result = subprocess.run(["bash", ".specify/scripts/bash/setup-plan.sh", "--json"], cwd=root, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((feature / "plan.md").read_text(), template.read_text())
            (feature / "plan.md").write_text("# Existing generated plan\n")
            result = subprocess.run(["bash", ".specify/scripts/bash/setup-plan.sh", "--json"], cwd=root, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((feature / "plan.md").read_text(), "# Existing generated plan\n")

    def test_prepare_refuses_symlink_destinations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "project"
            outside = Path(directory) / "outside"
            root.mkdir()
            outside.mkdir()
            (root / ".specify").symlink_to(outside, target_is_directory=True)
            with self.assertRaises(OSError):
                adapter.prepare(root)
            self.assertEqual(list(outside.iterdir()), [])

    def test_prepare_refuses_support_file_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / ".specify/templates"
            target.mkdir(parents=True)
            outside = root / "original.md"
            outside.write_text("preserve\n")
            (target / "plan-template.md").symlink_to(outside)
            with self.assertRaises(ValueError):
                adapter.prepare(root)
            self.assertEqual(outside.read_text(), "preserve\n")

    def test_unknown_mode_does_not_create_support(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(["python3", str(HELPER), "unknown", "--project", directory], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((Path(directory) / ".specify").exists())


if __name__ == "__main__":
    unittest.main()
