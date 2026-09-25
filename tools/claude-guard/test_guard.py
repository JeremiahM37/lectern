#!/usr/bin/env python3
"""Tests for the Claude Code tmux-kill guard hook.

Run with: python3 tools/claude-guard/test_guard.py
"""

import json
import subprocess
import sys
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("block-host-tmux-kill.py")

BLOCKED = [
    "tmux kill-server",
    "tmux -L x kill-session -t a",
    "pkill tmux",
]

ALLOWED = [
    "tmux ls",
    "echo tmux",
    "ADK_TEST_MODE=e2e tools/run-isolated-tests.sh .",
]


def run_hook(payload):
    return subprocess.run(
        [sys.executable, str(SCRIPT)],
        input=json.dumps(payload),
        capture_output=True,
        text=True,
        timeout=30,
    )


def bash(command):
    return {"tool_name": "Bash", "tool_input": {"command": command}}


class GuardTest(unittest.TestCase):
    def test_blocks_host_tmux_kills(self):
        for command in BLOCKED:
            with self.subTest(command=command):
                result = run_hook(bash(command))
                self.assertEqual(2, result.returncode, result.stderr)
                self.assertIn("tmux", result.stderr)

    def test_allows_safe_commands(self):
        for command in ALLOWED:
            with self.subTest(command=command):
                result = run_hook(bash(command))
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertEqual("", result.stderr)

    def test_allows_other_tools_and_empty_input(self):
        for payload in ({}, {"tool_name": "Read", "tool_input": {"file_path": "x"}}):
            with self.subTest(payload=payload):
                self.assertEqual(0, run_hook(payload).returncode)
        result = subprocess.run(
            [sys.executable, str(SCRIPT)],
            input="not json",
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(0, result.returncode)


if __name__ == "__main__":
    unittest.main()
