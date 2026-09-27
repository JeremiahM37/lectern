"""Git operations for the review workspace, run on the session's target.

argv: <workspace> <action> <base64 JSON params>. Prints one JSON object; exits 1
with {"error": ...} when the operation is refused or git fails. Every path is
checked to stay inside the workspace, and nothing here runs repository-defined
diff or textconv programs.
"""
import base64, hashlib, json, os, re, stat, subprocess, sys, tempfile

LIMIT = 512 * 1024
BLOB_LIMIT = 8 * 1024 * 1024
workspace = os.path.realpath(sys.argv[1])
action = sys.argv[2]
params = json.loads(base64.b64decode(sys.argv[3]).decode() or '{}') if len(sys.argv) > 3 else {}
os.chdir(workspace)
env = dict(os.environ, GIT_OPTIONAL_LOCKS='0', GIT_TERMINAL_PROMPT='0', GIT_LITERAL_PATHSPECS='1')
ZERO = '0' * 40


class Refused(Exception):
    pass


def git(args, limit=LIMIT, allowed=(0,), stdin=None, timeout=30):
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
        p = subprocess.run(['git', '-c', 'core.fsmonitor=false', '-c', 'color.ui=false', *args],
                           stdout=out, stderr=err, env=env, timeout=timeout,
                           input=stdin if stdin is not None else None)
        out.seek(0); data = out.read(limit + 1)
        if p.returncode not in allowed:
            err.seek(0)
            raise Refused(err.read(4096).decode('utf-8', 'replace').strip() or 'Git command failed')
        return data[:limit], len(data) > limit


def text(data):
    return data.decode('utf-8', 'replace')


def out(args, **kw):
    return text(git(args, **kw)[0]).strip()


def toplevel():
    return os.path.realpath(out(['rev-parse', '--show-toplevel']))


def safe_path(rel):
    """A repository-relative path inside the workspace, or Refused."""
    if not isinstance(rel, str) or not rel or '\0' in rel:
        raise Refused('A file path is required')
    full = os.path.normpath(os.path.join(workspace, rel))
    if os.path.commonpath([workspace, full]) != workspace or full == workspace:
        raise Refused('That path is outside this workspace')
    parent = os.path.realpath(os.path.dirname(full))
    if os.path.commonpath([workspace, parent]) != workspace:
        raise Refused('That path is outside this workspace')
    return os.path.relpath(full, workspace)


def git_path(name):
    return os.path.join(workspace, out(['rev-parse', '--git-path', name]))


def hunk_fingerprint(hunk):
    return hashlib.sha1(hunk.encode('utf-8', 'surrogateescape')).hexdigest()[:16]


def split_hunks(patch):
    """(header, [hunk text...]) for a single-file patch."""
    header, hunks, cur = [], [], None
    for line in patch.splitlines(keepends=True):
        if line.startswith('@@'):
            cur = [line]; hunks.append(cur)
        elif cur is None:
            header.append(line)
        else:
            cur.append(line)
    return ''.join(header), [''.join(h) for h in hunks]


def status_entries():
    raw, cut = git(['status', '--porcelain=v1', '-z', '--untracked-files=all', '--', '.'], 2 * LIMIT)
    if cut:
        raise Refused('Too many changed paths to review')
    parts = raw.split(b'\0'); files = []; i = 0
    top = toplevel()
    while i < len(parts) and parts[i]:
        item = parts[i]; i += 1
        xy = text(item[:2]); name = os.fsdecode(item[3:]); old = None
        if xy[0] in 'RC':
            old = os.fsdecode(parts[i]); i += 1
        full = os.path.normpath(os.path.join(top, name))
        if os.path.commonpath([workspace, full]) != workspace:
            continue
        conflicted = xy in ('DD', 'AU', 'UD', 'UA', 'DU', 'AA', 'UU')
        untracked = xy == '??'
        # " A" is an intent-to-add entry (the live diff marks new files that
        # way); for staging it behaves exactly like an untracked file.
        new_file = untracked or xy == ' A'
        files.append(dict(path=os.path.relpath(full, workspace), status=xy,
                          previous_path=old, conflicted=conflicted, untracked=untracked,
                          new_file=new_file,
                          staged=not conflicted and xy[0] not in (' ', '?'),
                          unstaged=not conflicted and (xy[1] != ' ' or untracked)))
    files.sort(key=lambda f: f['path'].casefold())
    return files


def file_patch(entry, scope):
    """The patch for one file in one scope ('staged' or 'unstaged')."""
    rel = entry['path']
    args = ['diff', '--no-ext-diff', '--no-textconv', '--no-color', '--unified=3']
    if scope == 'unstaged' and entry['untracked']:
        full = os.path.join(workspace, rel)
        st = os.lstat(full)
        if not stat.S_ISREG(st.st_mode) or os.path.realpath(full) != full:
            return 'Untracked link or special file: content preview unavailable.\n', False
        data, cut = git(args + ['--no-index', '--', '/dev/null', rel], allowed=(0, 1))
        return text(data), cut
    if scope == 'staged':
        args.append('--cached')
    paths = [rel]
    if entry.get('previous_path') and scope == 'staged':
        paths.append(entry['previous_path'])
    data, cut = git(args + ['--', *paths])
    return text(data), cut


def hooks():
    found = []
    try:
        base = git_path('hooks')
    except Refused:
        return found
    for name in ('pre-commit', 'prepare-commit-msg', 'commit-msg', 'post-commit', 'pre-push'):
        p = os.path.join(base, name)
        if os.path.isfile(p) and os.access(p, os.X_OK):
            found.append(name)
    return found


def operation():
    for marker, name in (('MERGE_HEAD', 'merge'), ('rebase-merge', 'rebase'), ('rebase-apply', 'rebase'),
                         ('CHERRY_PICK_HEAD', 'cherry-pick'), ('REVERT_HEAD', 'revert')):
        if os.path.exists(git_path(marker)):
            return name
    return ''


def branch_state():
    branch = out(['symbolic-ref', '--short', '-q', 'HEAD'], allowed=(0, 1))
    head = out(['rev-parse', '-q', '--verify', 'HEAD'], allowed=(0, 1))
    upstream = out(['rev-parse', '--abbrev-ref', '--symbolic-full-name', '@{u}'], allowed=(0, 128))
    remote_sha = ''
    if branch:
        remote_sha = out(['rev-parse', '-q', '--verify', 'refs/remotes/origin/' + branch], allowed=(0, 1))
    ahead = behind = 0
    if upstream and head:
        counts = out(['rev-list', '--left-right', '--count', 'HEAD...@{u}'], allowed=(0, 128)).split()
        if len(counts) == 2:
            ahead, behind = int(counts[0]), int(counts[1])
    pushed = False
    if head:
        pushed = bool(out(['branch', '-r', '--contains', 'HEAD'], allowed=(0, 129)))
    message = ''
    if head:
        message = out(['log', '-1', '--format=%B', 'HEAD'], allowed=(0, 128))
    merge_msg = ''
    try:
        with open(git_path('MERGE_MSG'), encoding='utf-8', errors='replace') as f:
            merge_msg = '\n'.join(l for l in f.read().splitlines() if not l.startswith('#')).strip()
    except OSError:
        pass
    return dict(branch=branch, head=head, upstream=upstream, remote_sha=remote_sha, ahead=ahead,
                behind=behind, head_pushed=pushed, head_message=message, operation=operation(),
                merge_message=merge_msg, hooks=hooks())


def do_status():
    files = status_entries()
    budget = {'staged': LIMIT, 'unstaged': LIMIT}
    truncated = False
    for f in files:
        for scope in ('staged', 'unstaged'):
            f[scope + '_patch'] = ''
            f[scope + '_hunks'] = []
            if not f[scope] or len(files) > 400:
                continue
            if budget[scope] <= 0:
                truncated = True
                continue
            patch, cut = file_patch(f, scope)
            budget[scope] -= len(patch)
            truncated = truncated or cut
            f[scope + '_patch'] = patch
            # The client names a hunk by index and this fingerprint; the
            # hunk action recomputes both before applying anything.
            f[scope + '_hunks'] = [hunk_fingerprint(h) for h in split_hunks(patch)[1]]
    return dict(branch_state(), files=files, truncated=truncated)


def entry_for(rel):
    rel = safe_path(rel)
    for f in status_entries():
        if f['path'] == rel:
            return f
    raise Refused('This file has no changes any more; refresh the list')


def paths_param():
    paths = params.get('paths') or []
    if not paths or len(paths) > 2000:
        raise Refused('Choose one or more files')
    return [safe_path(p) for p in paths]


def do_stage():
    git(['add', '-A', '--', *paths_param()])
    return dict(ok=True)


def do_unstage():
    paths = paths_param()
    if out(['rev-parse', '-q', '--verify', 'HEAD'], allowed=(0, 1)):
        git(['reset', '-q', 'HEAD', '--', *paths])
    else:
        git(['rm', '-q', '--cached', '-r', '--', *paths])
    return dict(ok=True)


def do_discard():
    discarded = []
    for rel in paths_param():
        f = entry_for(rel)
        if f['conflicted']:
            raise Refused(rel + ' has a merge conflict; resolve it instead')
        full = os.path.join(workspace, rel)
        if f['new_file'] and not f['staged']:
            if f['status'] == ' A':
                git(['rm', '-q', '--cached', '--', rel])
            st = os.lstat(full)
            if stat.S_ISDIR(st.st_mode):
                raise Refused(rel + ' is a directory')
            os.unlink(full)
        elif f['status'][1] == 'D' or f['unstaged']:
            git(['checkout', '-q', '--', rel])
        discarded.append(rel)
    return dict(ok=True, discarded=discarded)


def do_hunk():
    """Stage, unstage or discard one hunk, found again by index and checked
    against the fingerprint the client saw, so a stale click never applies a
    different hunk than the one on screen."""
    op = params.get('op')
    scope = {'stage': 'unstaged', 'discard': 'unstaged', 'unstage': 'staged'}.get(op)
    if not scope:
        raise Refused('Choose stage, unstage or discard')
    f = entry_for(params.get('path'))
    if f['conflicted']:
        raise Refused('This file has a merge conflict; resolve it instead')
    if f['new_file'] and scope == 'unstaged':
        raise Refused('A new file is staged or discarded as a whole')
    patch, cut = file_patch(f, scope)
    if cut:
        raise Refused('This diff is too large to stage by hunk')
    header, hunks = split_hunks(patch)
    index = params.get('index')
    if not isinstance(index, int) or index < 0 or index >= len(hunks):
        raise Refused('That hunk is no longer in the diff; refresh the list')
    hunk = hunks[index]
    if params.get('fingerprint') != hunk_fingerprint(hunk):
        raise Refused('The file changed since this diff was shown; refresh and try again')
    if 'Binary files' in header or 'GIT binary patch' in header:
        raise Refused('Binary changes are staged as a whole file')
    single = (header + hunk).encode('utf-8', 'surrogateescape')
    args = ['apply', '--whitespace=nowarn', '--recount']
    if op in ('stage', 'unstage'):
        args.append('--cached')
    if op in ('unstage', 'discard'):
        args.append('-R')
    git(args + ['-'], stdin=single)
    return dict(ok=True)


# ---- conflicts ------------------------------------------------------------

def stage_blob(rel, n):
    data, cut = git(['show', ':%d:%s' % (n, rel)], limit=BLOB_LIMIT, allowed=(0, 128))
    return None if cut else data


def is_binary(data):
    return data is not None and b'\0' in data[:8000]


def do_conflicts():
    raw, _ = git(['ls-files', '-u', '-z', '--', '.'])
    stages = {}
    for item in raw.split(b'\0'):
        if not item:
            continue
        meta, _, name = item.partition(b'\t')
        stages.setdefault(os.fsdecode(name), set()).add(int(meta.split()[2]))
    files = []
    top = toplevel()
    for name in sorted(stages, key=str.casefold):
        full = os.path.normpath(os.path.join(top, name))
        if os.path.commonpath([workspace, full]) != workspace:
            continue
        s = stages[name]
        files.append(dict(path=os.path.relpath(full, workspace), base=1 in s, ours=2 in s, theirs=3 in s))
    result = dict(branch_state(), files=files)
    want = params.get('path')
    if want:
        rel = safe_path(want)
        match = next((f for f in files if f['path'] == rel), None)
        if not match:
            raise Refused('That file is no longer in conflict; refresh the list')
        blobs = {k: stage_blob(rel, n) if match[k] else None for k, n in (('base', 1), ('ours', 2), ('theirs', 3))}
        full = os.path.join(workspace, rel)
        working = None
        if os.path.isfile(full) and not os.path.islink(full):
            with open(full, 'rb') as fh:
                working = fh.read(BLOB_LIMIT + 1)
        binary = any(is_binary(b) for b in (*blobs.values(), working))
        detail = dict(path=rel, binary=binary, **{k: v is not None for k, v in blobs.items()})
        if not binary:
            for k, v in blobs.items():
                detail[k + '_text'] = text(v) if v is not None else ''
            detail['working_text'] = text(working) if working is not None and len(working) <= BLOB_LIMIT else ''
            if all(v is not None for v in blobs.values()):
                # A diff3-style rendering of the pristine conflict, so every
                # region carries its common ancestor even when the checkout
                # wrote plain two-way markers.
                with tempfile.TemporaryDirectory() as d:
                    names = []
                    for k in ('ours', 'base', 'theirs'):
                        p = os.path.join(d, k); names.append(p)
                        with open(p, 'wb') as fh:
                            fh.write(blobs[k])
                    theirs_label = 'incoming'
                    if os.path.exists(git_path('MERGE_HEAD')):
                        theirs_label = out(['name-rev', '--name-only', '--always', 'MERGE_HEAD'], allowed=(0, 128)) or theirs_label
                    merged, _ = git(['merge-file', '-p', '--diff3', '-L', branch_state()['branch'] or 'HEAD',
                                     '-L', 'base', '-L', theirs_label, *names], limit=BLOB_LIMIT,
                                    allowed=tuple(range(0, 128)))
                    detail['merged_text'] = text(merged)
        result['file'] = detail
    return result


MARKER = re.compile(r'^(<{7}|>{7})( |$)', re.M)


def do_resolve():
    rel = safe_path(params.get('path'))
    conflicted = {f['path'] for f in do_conflicts()['files']}
    if rel not in conflicted:
        raise Refused('That file is no longer in conflict; refresh the list')
    full = os.path.join(workspace, rel)
    take = params.get('take')
    if take in ('ours', 'theirs'):
        if stage_blob(rel, 2 if take == 'ours' else 3) is None:
            git(['rm', '-q', '--', rel])
        else:
            git(['checkout', '--' + take, '--', rel])
            git(['add', '--', rel])
        return dict(ok=True, path=rel)
    staged = params.get('staged_file') or ''
    if not re.fullmatch(r'/tmp/lectern-resolve-[0-9]+-[0-9]+', staged):
        raise Refused('No resolved content was supplied')
    with open(staged, 'rb') as fh:
        content = fh.read(BLOB_LIMIT + 1)
    os.unlink(staged)
    if len(content) > BLOB_LIMIT:
        raise Refused('The resolved file is too large')
    if not params.get('allow_markers') and MARKER.search(text(content)):
        raise Refused('The result still contains conflict markers')
    if os.path.lexists(full) and not stat.S_ISREG(os.lstat(full).st_mode):
        raise Refused('Only a regular file can be resolved here')
    mode = stat.S_IMODE(os.lstat(full).st_mode) if os.path.lexists(full) else 0o644
    fd = os.open(full, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as fh:
        fh.write(content)
    git(['add', '--', rel])
    return dict(ok=True, path=rel)


def do_abort():
    op = operation()
    if op not in ('merge', 'rebase', 'cherry-pick', 'revert'):
        raise Refused('No merge or rebase is in progress')
    git([op, '--abort'])
    return dict(ok=True, aborted=op)


# ---- blobs and blame ------------------------------------------------------

def do_blob():
    rel = safe_path(params.get('path'))
    if params.get('side') == 'old':
        data, cut = git(['show', '%s:%s' % (params.get('ref') or 'HEAD', rel)], limit=BLOB_LIMIT, allowed=(0, 128))
        if not data and not cut:
            return dict(missing=True)
    else:
        full = os.path.join(workspace, rel)
        if not os.path.isfile(full) or os.path.islink(full):
            return dict(missing=True)
        with open(full, 'rb') as fh:
            data = fh.read(BLOB_LIMIT + 1)
        cut = len(data) > BLOB_LIMIT
    if cut:
        return dict(too_large=True)
    return dict(data=base64.b64encode(data).decode())


AGENT_TRAILER = re.compile(r'(co-authored-by|generated[- ]with|generated[- ]by)[^\n]*(claude|anthropic|codex|openai|gemini|copilot|cursor|aider|opencode)', re.I)
AGENT_AUTHOR = re.compile(r'(claude|anthropic|codex|openai|gemini|copilot|cursor|aider|opencode|\[bot\])', re.I)


def do_blame():
    """Which commit last touched each requested line, and which commits since
    the base look agent-made (an agent co-author trailer or an agent author)."""
    base = params.get('base') or ''
    agent_commits = []
    if base:
        raw = out(['log', '--format=%H%x00%an%x00%ae%x00%B%x1e', base + '..HEAD'], allowed=(0, 128))
        for rec in raw.split('\x1e'):
            parts = rec.strip('\n').split('\0')
            if len(parts) < 4:
                continue
            sha, name, email, body = parts[0].strip(), parts[1], parts[2], parts[3]
            if AGENT_TRAILER.search(body) or AGENT_AUTHOR.search(name + ' ' + email):
                agent_commits.append(sha)
    files = {}
    for rel, ranges in list((params.get('files') or {}).items())[:300]:
        rel = safe_path(rel)
        if not os.path.isfile(os.path.join(workspace, rel)):
            continue
        args = ['blame', '--porcelain']
        for a, b in ranges[:200]:
            args += ['-L', '%d,%d' % (a, b)]
        raw, _ = git(args + ['--', rel], allowed=(0, 128))
        lines = {}
        for line in text(raw).splitlines():
            m = re.match(r'^([0-9a-f]{40}) \d+ (\d+)', line)
            if m:
                lines[m.group(2)] = m.group(1)
        files[rel] = lines
    return dict(agent_commits=agent_commits, files=files, zero=ZERO)


ACTIONS = dict(status=do_status, stage=do_stage, unstage=do_unstage, discard=do_discard, hunk=do_hunk,
               conflicts=do_conflicts, resolve=do_resolve, abort=do_abort, blob=do_blob, blame=do_blame)

try:
    if action not in ACTIONS:
        raise Refused('Unknown action')
    print(json.dumps(ACTIONS[action]()))
except (OSError, Refused, subprocess.TimeoutExpired, ValueError) as error:
    print(json.dumps(dict(error=str(error))))
    sys.exit(1)
