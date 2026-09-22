"""Read-only target-side validation of a grouped allocation before persistence.

Creation must repeat these checks under its operation lock; this is a resolved
plan, not a reservation of paths, branches or refs.
"""
import json
import os
import subprocess
import sys
import pathlib
import stat


def git(repo, *args):
    result = subprocess.run(
        ['git', '--no-optional-locks', '-C', repo, *args],
        capture_output=True, text=True, timeout=10,
    )
    if result.returncode:
        raise ValueError(result.stderr.strip() or result.stdout.strip() or 'Git command failed')
    return result.stdout.strip()


def preflight(plan, existing=0):
    repositories = plan.get('repositories', [])
    if not 1 <= len(repositories) <= 8:
        raise ValueError('A workspace needs between one and eight repositories')
    root = plan['path']
    if not os.path.isabs(root):
        raise ValueError('Workspace paths must be absolute')
    if not existing and os.path.lexists(root):
        raise ValueError('Workspace path already exists; nothing was changed')
    root = os.path.realpath(root)
    paths, common_dirs, tokens = set(), set(), {plan['token']}
    if not plan['token']:
        raise ValueError('Workspace ownership is missing')
    for index, entry in enumerate(repositories):
        child = entry['worktree']
        if child.get('repositories'):
            raise ValueError('Nested repository groups are not supported')
        if not os.path.isabs(child['repo']) or not os.path.isabs(child['path']):
            raise ValueError('Repository paths must be absolute')
        dest = os.path.realpath(child['path'])
        if os.path.dirname(dest) != root or dest in paths:
            raise ValueError('Repository paths must be distinct children of the workspace')
        if index >= existing and os.path.lexists(child['path']):
            raise ValueError('Repository allocation already exists')
        if not child['token'] or child['token'] in tokens:
            raise ValueError('Each repository needs separate ownership')
        if child['branch'] != plan['branch']:
            raise ValueError('Repository branch must match the workspace branch')
        repo = os.path.realpath(child['repo'])
        common = os.path.realpath(git(repo, 'rev-parse', '--path-format=absolute', '--git-common-dir'))
        if common in common_dirs:
            raise ValueError('The same Git repository was selected more than once')
        if index < existing:
            if os.path.islink(child['path']) or git(dest,'rev-parse','--show-toplevel') != dest:
                raise ValueError('An existing checkout path was replaced')
            if os.path.realpath(git(dest,'rev-parse','--path-format=absolute','--git-common-dir')) != common:
                raise ValueError('An existing checkout repository changed')
            if git(dest,'symbolic-ref','--quiet','--short','HEAD') != child['branch']:
                raise ValueError('An existing checkout branch changed')
            owner=pathlib.Path(git(dest,'rev-parse','--absolute-git-dir'))/'lectern-owner'
            fd=os.open(owner,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
            with os.fdopen(fd) as file:
                if not stat.S_ISREG(os.fstat(file.fileno()).st_mode) or file.read()!=child['token']:
                    raise ValueError('An existing checkout owner changed')
            common_dirs.add(common);paths.add(dest);tokens.add(child['token'])
            continue
        git(repo, 'check-ref-format', '--branch', child['branch'])
        git(repo, 'check-ref-format', 'refs/heads/' + child['branch'])
        # rev-parse --verify is read-only; show-ref --verify distinguishes a
        # missing branch from an invalid repository/other unexpected error.
        branch = subprocess.run(
            ['git', '--no-optional-locks', '-C', repo, 'show-ref', '--verify', '--quiet',
             'refs/heads/' + child['branch']], capture_output=True, text=True, timeout=10,
        )
        if branch.returncode == 0:
            raise ValueError('Workspace branch already exists in repository: ' + entry['name'])
        if branch.returncode != 1:
            raise ValueError(branch.stderr.strip() or 'Could not check workspace branch')
        base = child['base']
        if not base or base.startswith('-'):
            raise ValueError('Choose a branch, tag or commit as the base')
        commit = git(repo, 'rev-parse', '--verify', '--end-of-options', base + '^{commit}')
        child.update(repo=repo, path=dest, commit=commit)
        common_dirs.add(common)
        paths.add(dest)
        tokens.add(child['token'])
    plan.update(path=root, repo=repositories[0]['worktree']['repo'],
                commit=repositories[0]['worktree']['commit'])
    return plan


if __name__ == "__main__":
    try:
        if sys.argv[1] != 'check-create':
            raise ValueError('Unknown workspace preflight operation')
        print(json.dumps({'workspace': preflight(json.loads(sys.argv[2]))}))
    except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        print(json.dumps({'error': str(error)}))
        sys.exit(1)
