#!/usr/bin/env python3
"""Unit tests for tools/conflict_replay.py against tiny synthetic Go repositories.

Each scenario is its own repository with exactly one two-parent merge, so the labels
do not depend on how the merges would have stacked. Run with:
    python3 -I tools/test_conflict_replay.py
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.join(HERE, 'conflict_replay.py')
GO_BIN = '/usr/local/go/bin'

GO_MOD = 'module example.com/replay\n\ngo 1.21\n'
CALC = 'package calc\n\n// Double returns twice n.\nfunc Double(n int) int { return n * 2 }\n'
CALC_TEST = (
    'package calc\n\nimport "testing"\n\n'
    'func TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("Double(2)")\n\t}\n}\n'
)
CALC_TWICE = CALC.replace('Double', 'Twice')
CALC_TEST_TWICE = CALC_TEST.replace('Double', 'Twice')


def git_env():
    env = dict(os.environ)
    env.update({
        'GIT_AUTHOR_NAME': 'replay-test', 'GIT_AUTHOR_EMAIL': 'replay@example.invalid',
        'GIT_COMMITTER_NAME': 'replay-test', 'GIT_COMMITTER_EMAIL': 'replay@example.invalid',
    })
    return env


def git(repo, *args, check=True):
    return subprocess.run(['git', '-C', repo, *args], capture_output=True, text=True,
                          env=git_env(), check=check)


def write(repo, rel, text):
    path = os.path.join(repo, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(text)


def commit(repo, message):
    git(repo, 'add', '-A')
    git(repo, 'commit', '-q', '-m', message)


def base_repo(root):
    """Repository with a module, a calc package and one base commit on main."""
    repo = os.path.join(root, 'repo')
    os.makedirs(repo)
    git(repo, 'init', '-q', '-b', 'main')
    write(repo, 'go.mod', GO_MOD)
    write(repo, 'calc/calc.go', CALC)
    write(repo, 'calc/calc_test.go', CALC_TEST)
    commit(repo, 'base: calc package')
    return repo


def scenario_clean(root):
    """Two sides add different new packages, so the merge is clean and both builds pass."""
    repo = base_repo(root)
    base = git(repo, 'rev-parse', 'HEAD').stdout.strip()
    git(repo, 'checkout', '-q', '-b', 'side-a')
    write(repo, 'alpha/alpha.go', 'package alpha\n\nfunc Alpha() int { return 1 }\n')
    commit(repo, 'side a: add alpha')
    git(repo, 'checkout', '-q', '-b', 'side-b', base)
    write(repo, 'beta/beta.go', 'package beta\n\nfunc Beta() int { return 2 }\n')
    commit(repo, 'side b: add beta')
    git(repo, 'checkout', '-q', 'side-a')
    git(repo, 'merge', '-q', '--no-ff', '-m', 'merge clean', 'side-b')
    return repo


def scenario_textual(root):
    """Both sides edit the same line of calc.go differently, so git reports a conflict."""
    repo = base_repo(root)
    base = git(repo, 'rev-parse', 'HEAD').stdout.strip()
    git(repo, 'checkout', '-q', '-b', 'side-a')
    write(repo, 'calc/calc.go', CALC.replace('n * 2', 'n + n'))
    commit(repo, 'side a: rewrite Double body')
    git(repo, 'checkout', '-q', '-b', 'side-b', base)
    write(repo, 'calc/calc.go', CALC.replace('n * 2', '2 * n'))
    commit(repo, 'side b: rewrite Double body differently')
    git(repo, 'checkout', '-q', 'side-a')
    merged = git(repo, 'merge', '--no-ff', '-m', 'merge textual', 'side-b', check=False)
    if merged.returncode == 0:
        raise RuntimeError('expected a textual conflict in the textual scenario')
    # Resolve by keeping side a, so the merge commit exists for the tool to replay.
    write(repo, 'calc/calc.go', CALC.replace('n * 2', 'n + n'))
    git(repo, 'add', '-A')
    git(repo, 'commit', '-q', '--no-edit')
    return repo


def scenario_semantic(root):
    """Side a renames Double to Twice; side b adds a caller of Double. Git merges cleanly,
    but the combined tree no longer builds."""
    repo = base_repo(root)
    base = git(repo, 'rev-parse', 'HEAD').stdout.strip()
    git(repo, 'checkout', '-q', '-b', 'side-a')
    write(repo, 'calc/calc.go', CALC_TWICE)
    write(repo, 'calc/calc_test.go', CALC_TEST_TWICE)
    commit(repo, 'side a: rename Double to Twice')
    git(repo, 'checkout', '-q', '-b', 'side-b', base)
    write(repo, 'user/user.go',
          'package user\n\nimport "example.com/replay/calc"\n\n'
          'func Four() int { return calc.Double(2) }\n')
    commit(repo, 'side b: add caller of Double')
    git(repo, 'checkout', '-q', 'side-a')
    git(repo, 'merge', '-q', '--no-ff', '-m', 'merge semantic', 'side-b')
    return repo


class ConflictReplayTest(unittest.TestCase):

    @classmethod
    def setUpClass(cls):
        cls.root = tempfile.mkdtemp(prefix='conflict-replay-test-')
        cls.tmp_root = os.path.join(cls.root, 'scratch')
        os.makedirs(cls.tmp_root)
        env_path = os.environ.get('PATH', '')
        if os.path.isdir(GO_BIN) and GO_BIN not in env_path.split(os.pathsep):
            os.environ['PATH'] = GO_BIN + os.pathsep + env_path

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.root, ignore_errors=True)

    def replay(self, name, builder):
        repo = builder(os.path.join(self.root, name))
        out = os.path.join(self.root, name + '.jsonl')
        proc = subprocess.run(
            [sys.executable, '-I', TOOL, '--repo', repo, '--out', out, '--limit', '5',
             '--tmp-root', self.tmp_root],
            capture_output=True, text=True, env=dict(os.environ))
        self.assertEqual(proc.returncode, 0, proc.stderr)
        with open(out, encoding='utf-8') as fh:
            rows = [json.loads(line) for line in fh if line.strip()]
        self.assertEqual(len(rows), 1, 'expected exactly one replayable merge in %s' % name)
        leftovers = [d for d in os.listdir(self.tmp_root)]
        self.assertEqual(leftovers, [], 'temporary worktrees were not cleaned up')
        return rows[0]

    def test_clean_merge(self):
        row = self.replay('clean', scenario_clean)
        self.assertFalse(row['textual_conflict'])
        self.assertTrue(row['p1_ok'])
        self.assertTrue(row['p2_ok'])
        self.assertTrue(row['merged_ok'])
        self.assertFalse(row['semantic_conflict'])
        self.assertFalse(row['file_overlap'])

    def test_textual_conflict(self):
        row = self.replay('textual', scenario_textual)
        self.assertTrue(row['textual_conflict'])
        self.assertIn('calc/calc.go', row['conflict_paths'])
        self.assertIsNone(row['merged_ok'])
        self.assertFalse(row['semantic_conflict'])
        self.assertTrue(row['file_overlap'])

    def test_semantic_conflict(self):
        row = self.replay('semantic', scenario_semantic)
        self.assertFalse(row['textual_conflict'])
        self.assertTrue(row['p1_ok'])
        self.assertTrue(row['p2_ok'])
        self.assertFalse(row['merged_ok'])
        self.assertTrue(row['semantic_conflict'])
        self.assertFalse(row['file_overlap'])


if __name__ == '__main__':
    unittest.main()
