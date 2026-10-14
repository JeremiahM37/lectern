#!/usr/bin/env python3
"""Replay two-parent merges to build a ground-truth set for conflict prediction.

Each two-parent merge on the first-parent history is two lines of work that were
developed concurrently from a merge-base. This tool redoes that merge and records
whether the textual merge conflicts and whether the combined tree still builds and
passes the tests of the packages both sides touched, while each side alone did.
Output is JSON Lines, one row per merge that was not skipped.

Method, per merge M with parents P1 and P2 and base B = merge-base(P1, P2):
  - Skip unless M has exactly two parents, a merge-base exists, both sides change
    at least one .go file, and neither side diff exceeds 3000 changed lines.
  - side_a / side_b are the full text of `git diff B P1` and `git diff B P2`.
  - `git merge-tree --write-tree` decides textual_conflict and conflict_paths.
  - Only when there is no textual conflict are the trees built and tested. Each of
    P1, P2 and the merged tree is checked out in a detached temporary worktree and
    run through `go build ./...`, `go vet` and `go test -count=1 -p 2` restricted
    to the directories touched by either side. The merged tree is only tested when
    both sides passed alone, since semantic_conflict cannot be true otherwise.
  - semantic_conflict = p1_ok and p2_ok and not merged_ok.

Limits, read before trusting a label:
  - Merges are the repository owner's own; the sample says nothing about other
    people's branches.
  - Only packages touched by a side are tested, so a breakage in an untouched
    package that the merge reaches is missed. `go build ./...` still covers the
    whole module.
  - Packages whose _test.go files mention tmux are excluded from `go test`, because
    the real-process tests refuse to run outside the reviewed isolated runner. They
    are still built and vetted. The exclusion is recorded in notes.
  - Only the first-parent history of the current checkout is walked, so merges made
    on other lines of work are not represented.

The repository is only read. All builds happen in worktrees under --tmp-root, which
are removed in a finally block and pruned afterwards.

Usage: python3 -I tools/conflict_replay.py --repo PATH --out FILE.jsonl --limit N
       [--timeout 600] [--since 2026-01-01] [--tmp-root DIR]
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

MAX_SIDE_LINES = 3000
DEFAULT_TIMEOUT = 600
DEFAULT_TMP_ROOT = '/mnt/bulk/lectern-research/replay-tmp'


class Skip(Exception):
    """A merge that is outside the sample definition; the reason is reported."""


def git(repo, *args, check=True):
    return subprocess.run(['git', '-C', repo, *args], capture_output=True, text=True,
                          encoding='utf-8', errors='replace', check=check)


def parents_of(repo, merge):
    fields = git(repo, 'rev-list', '--parents', '-n', '1', merge).stdout.split()
    return fields[1:]


def changed_files(repo, a, b):
    out = git(repo, 'diff', '--no-renames', '--name-only', '-z', a, b).stdout
    return [f for f in out.split('\0') if f]


def changed_lines(repo, a, b):
    total = 0
    for line in git(repo, 'diff', '--no-renames', '--numstat', a, b).stdout.splitlines():
        added, deleted, _ = (line.split('\t', 2) + ['', ''])[:3]
        # Binary files report '-' for both counts and contribute nothing.
        total += int(added) if added.isdigit() else 0
        total += int(deleted) if deleted.isdigit() else 0
    return total


def diff_text(repo, a, b):
    return git(repo, 'diff', '--no-renames', '--no-color', a, b).stdout


def pkg_dir(path):
    """Directory of a Go file, '' for the module root."""
    return os.path.dirname(path)


def pattern_for(directory):
    return './' + directory + '/...' if directory else '.'


def merge_tree(repo, p1, p2):
    """Return (tree, conflicted_paths). Raises Skip when merge-tree itself errors."""
    proc = git(repo, 'merge-tree', '--write-tree', '--name-only', '--no-messages',
               p1, p2, check=False)
    if proc.returncode not in (0, 1):
        raise Skip('merge-tree failed (rc %d): %s' % (proc.returncode, proc.stderr.strip()[-200:]))
    lines = proc.stdout.splitlines()
    tree = lines[0].strip()
    conflicts = []
    for line in lines[1:]:
        if not line.strip():
            break
        conflicts.append(line.strip())
    return tree, conflicts, proc.returncode == 1


def run_cmd(argv, cwd, timeout):
    try:
        proc = subprocess.run(argv, cwd=cwd, capture_output=True, text=True,
                              encoding='utf-8', errors='replace', timeout=timeout)
    except subprocess.TimeoutExpired:
        return None, 'timed out after %ds' % timeout
    tail = (proc.stdout + proc.stderr).strip().splitlines()
    return proc.returncode, (tail[-1] if tail else '')


def uses_tmux(directory):
    for name in os.listdir(directory):
        if name.endswith('_test.go'):
            with open(os.path.join(directory, name), encoding='utf-8', errors='replace') as fh:
                if 'tmux' in fh.read():
                    return True
    return False


def evaluate_tree(tree_path, dirs, timeout, label, notes):
    """Build, vet and test one checked-out tree. Returns True when it passes."""
    rc, tail = run_cmd(['go', 'build', './...'], tree_path, timeout)
    if rc != 0:
        notes.append('%s: go build failed: %s' % (label, tail))
        return False

    testable, excluded = [], []
    for directory in dirs:
        full = os.path.join(tree_path, directory)
        if not os.path.isdir(full) or not any(n.endswith('.go') for n in os.listdir(full)):
            continue
        (excluded if uses_tmux(full) else testable).append(directory)
    if excluded:
        notes.append('%s: tmux tests excluded from go test: %s' % (label, ', '.join(
            pattern_for(d) for d in excluded)))
    if not testable:
        notes.append('%s: no testable packages in this tree, build only' % label)
        return True

    patterns = [pattern_for(d) for d in testable]
    rc, tail = run_cmd(['go', 'vet', *patterns], tree_path, timeout)
    if rc != 0:
        notes.append('%s: go vet failed: %s' % (label, tail))
        return False
    rc, tail = run_cmd(['go', 'test', '-count=1', '-p', '2', *patterns], tree_path, timeout)
    if rc != 0:
        notes.append('%s: go test failed: %s' % (label, tail))
        return False
    return True


def check_commit(repo, commit, label, dirs, tree, tmp_root, timeout, notes):
    """Check out commit (and optionally force the tree) in a temp worktree and test it."""
    scratch = tempfile.mkdtemp(prefix=label + '-', dir=tmp_root)
    wt = os.path.join(scratch, 'wt')
    started = time.monotonic()
    try:
        git(repo, 'worktree', 'add', '--detach', wt, commit)
        if tree:
            git(wt, 'read-tree', '--reset', '-u', tree)
            if git(wt, 'write-tree').stdout.strip() != tree:
                raise RuntimeError('worktree does not match merged tree %s' % tree)
        ok = evaluate_tree(wt, dirs, timeout, label, notes)
    finally:
        git(repo, 'worktree', 'remove', '--force', wt, check=False)
        git(repo, 'worktree', 'prune', check=False)
        shutil.rmtree(scratch, ignore_errors=True)
    return ok, round(time.monotonic() - started, 1)


def replay(repo, merge, args, stats):
    parents = parents_of(repo, merge)
    if len(parents) != 2:
        raise Skip('not a two-parent merge')
    p1, p2 = parents
    base_proc = git(repo, 'merge-base', p1, p2, check=False)
    if base_proc.returncode != 0 or not base_proc.stdout.strip():
        raise Skip('no merge-base')
    base = base_proc.stdout.strip()

    files_a = changed_files(repo, base, p1)
    files_b = changed_files(repo, base, p2)
    go_a = [f for f in files_a if f.endswith('.go')]
    go_b = [f for f in files_b if f.endswith('.go')]
    if not go_a or not go_b:
        raise Skip('a side changes no .go files')
    if changed_lines(repo, base, p1) > MAX_SIDE_LINES or changed_lines(repo, base, p2) > MAX_SIDE_LINES:
        raise Skip('a side diff exceeds %d lines' % MAX_SIDE_LINES)

    notes = []
    tree, conflicts, textual = merge_tree(repo, p1, p2)
    dirs = sorted({pkg_dir(f) for f in go_a + go_b})
    row = {
        'repo': repo,
        'merge': merge,
        'p1': p1,
        'p2': p2,
        'base': base,
        'side_a': diff_text(repo, base, p1),
        'side_b': diff_text(repo, base, p2),
        'files_a': files_a,
        'files_b': files_b,
        'file_overlap': bool(set(files_a) & set(files_b)),
        'textual_conflict': textual,
        'conflict_paths': conflicts,
        'p1_ok': None,
        'p2_ok': None,
        'merged_ok': None,
        'semantic_conflict': False,
        'notes': notes,
        'timing_s': {},
    }

    if textual:
        notes.append('textual conflict; semantic checks not run')
    else:
        p1_ok, row['timing_s']['p1'] = check_commit(
            repo, p1, 'p1', dirs, None, args.tmp_root, args.timeout, notes)
        p2_ok, row['timing_s']['p2'] = check_commit(
            repo, p2, 'p2', dirs, None, args.tmp_root, args.timeout, notes)
        row['p1_ok'], row['p2_ok'] = p1_ok, p2_ok
        if p1_ok and p2_ok:
            merged_ok, row['timing_s']['merged'] = check_commit(
                repo, p1, 'merged', dirs, tree, args.tmp_root, args.timeout, notes)
            row['merged_ok'] = merged_ok
            row['semantic_conflict'] = not merged_ok
        else:
            notes.append('merged tree not tested: a side failed alone')
    return row


def parse_args(argv):
    ap = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    ap.add_argument('--repo', required=True)
    ap.add_argument('--out', required=True)
    ap.add_argument('--limit', type=int, required=True,
                    help='number of merges to consider, newest first')
    ap.add_argument('--timeout', type=int, default=DEFAULT_TIMEOUT,
                    help='seconds allowed per go command')
    ap.add_argument('--since', help='only merges after this date, passed to git rev-list')
    ap.add_argument('--tmp-root', default=DEFAULT_TMP_ROOT)
    ap.add_argument('--max-side-lines', type=int, default=MAX_SIDE_LINES,
                    help='skip merges where either side changes more lines than this')
    return ap.parse_args(argv)


def main(argv):
    args = parse_args(argv)
    global MAX_SIDE_LINES
    MAX_SIDE_LINES = args.max_side_lines
    repo = os.path.abspath(args.repo)
    os.makedirs(args.tmp_root, exist_ok=True)

    rev_args = ['rev-list', '--first-parent', '--merges', 'HEAD']
    if args.since:
        rev_args.insert(1, '--since=' + args.since)
    merges = git(repo, *rev_args).stdout.split()[:args.limit]

    stats = {'considered': 0, 'pairs': 0, 'textual': 0, 'semantic': 0, 'file_overlap': 0,
             'skipped': {}, 'seconds': 0.0}
    with open(args.out, 'w', encoding='utf-8') as out:
        for merge in merges:
            stats['considered'] += 1
            started = time.monotonic()
            try:
                row = replay(repo, merge, args, stats)
            except Skip as skip:
                stats['skipped'][str(skip)] = stats['skipped'].get(str(skip), 0) + 1
                continue
            except subprocess.CalledProcessError as err:
                reason = 'git error: %s' % (err.stderr or '').strip()[-120:]
                stats['skipped'][reason] = stats['skipped'].get(reason, 0) + 1
                continue
            elapsed = time.monotonic() - started
            stats['seconds'] += elapsed
            row['timing_s']['total'] = round(elapsed, 1)
            out.write(json.dumps(row) + '\n')
            out.flush()
            stats['pairs'] += 1
            stats['textual'] += int(row['textual_conflict'])
            stats['semantic'] += int(row['semantic_conflict'])
            stats['file_overlap'] += int(row['file_overlap'])
            print('%s textual=%s semantic=%s overlap=%s %.1fs' % (
                merge[:12], row['textual_conflict'], row['semantic_conflict'],
                row['file_overlap'], elapsed), file=sys.stderr)

    per_pair = stats['seconds'] / stats['pairs'] if stats['pairs'] else 0.0
    print(json.dumps({'considered': stats['considered'], 'pairs': stats['pairs'],
                      'textual': stats['textual'], 'semantic': stats['semantic'],
                      'file_overlap': stats['file_overlap'],
                      'mean_seconds_per_pair': round(per_pair, 1),
                      'skipped': stats['skipped']}, indent=2), file=sys.stderr)
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
