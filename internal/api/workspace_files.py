# Workspace file operations. Runs on the session's own target (local, SSH or
# pct) through the executor, so every path check happens on the machine that
# owns the files. argv: root rel action mode [extra...]. Prints one JSON object;
# a refusal exits 1 with {"error": ...} (exit 3 marks a write conflict).
import base64, hashlib, io, json, os, re, shutil, stat, subprocess, sys, time, zipfile

CAP = 25 * 1024 * 1024          # read, write and download limit
ARCHIVE_CAP = 50 * 1024 * 1024  # compressed folder download limit
root = os.path.realpath(sys.argv[1])
rel = sys.argv[2]
action = sys.argv[3]
grouped = len(sys.argv) > 4 and sys.argv[4] == 'grouped'
extra = sys.argv[5:]
BOOK = ('.lectern-lock', '.lectern-state.json', '.lectern-process.json', '.lectern-state.next',
        '.agentdeck-lock', '.agentdeck-state.json', '.agentdeck-process.json', '.agentdeck-state.next')


class Refused(ValueError):
    pass


class Conflict(ValueError):
    pass


def inside(p):
    return os.path.commonpath([root, p]) == root


def bookkeeping(p):
    if not grouped:
        return False
    name = os.path.relpath(p, root)
    return name in BOOK or (os.sep not in name and (name.startswith('.lectern-write-') or name.startswith('.agentdeck-write-')))


def resolved(r):
    """The real path of r, which must stay beneath the workspace."""
    p = os.path.realpath(os.path.join(root, r))
    if not inside(p):
        raise Refused('path is outside this workspace')
    if bookkeeping(p):
        raise Refused('workspace bookkeeping is not a project file')
    return p


def entry(r):
    """The directory entry r itself (a symlink is not followed): its parent must
    resolve inside the workspace, and the entry cannot be the workspace root."""
    r = r.strip('/')
    if r in ('', '.') or os.path.normpath(r) in ('.', '..') or os.path.normpath(r).startswith('..' + os.sep):
        raise Refused('choose a file or folder inside this workspace')
    name = os.path.basename(os.path.normpath(r))
    if name in ('', '.', '..'):
        raise Refused('choose a file or folder inside this workspace')
    parent = resolved(os.path.dirname(os.path.normpath(r)) or '.')
    if not os.path.isdir(parent):
        raise Refused('the containing folder does not exist')
    p = os.path.join(parent, name)
    if bookkeeping(p):
        raise Refused('workspace bookkeeping is not a project file')
    return p


def digest(data):
    return hashlib.sha256(data).hexdigest()


def open_regular(p):
    fd = os.open(p, os.O_RDONLY | os.O_NONBLOCK)
    f = os.fdopen(fd, 'rb')
    st = os.fstat(f.fileno())
    # Re-check the opened descriptor: a symlink swapped in after resolved()
    # checked the name cannot redirect the read outside the workspace.
    if os.path.isdir('/proc/self/fd') and not inside(os.path.realpath('/proc/self/fd/' + str(f.fileno()))):
        f.close()
        raise Refused('path left workspace')
    if not stat.S_ISREG(st.st_mode):
        f.close()
        raise Refused('choose a regular file')
    return f, st


def read_capped(p):
    f, st = open_regular(p)
    with f:
        if st.st_size > CAP:
            raise Refused('file exceeds 25 MiB limit')
        data = f.read(CAP + 1)
    if len(data) > CAP:
        raise Refused('file exceeds 25 MiB limit')
    return data, st


def do_list():
    p = resolved(rel)
    entries = []
    with os.scandir(p) as scan:
        for e in scan:
            if len(entries) >= 2000:
                break
            dest = os.path.realpath(e.path)
            if not inside(dest) or bookkeeping(dest):
                continue
            try:
                st = e.stat()
                if not stat.S_ISREG(st.st_mode) and not stat.S_ISDIR(st.st_mode):
                    continue
                entries.append(dict(name=e.name, path=os.path.relpath(e.path, root), directory=stat.S_ISDIR(st.st_mode),
                                    size=st.st_size, mtime=int(st.st_mtime * 1000), link=e.is_symlink()))
            except OSError:
                continue
    entries.sort(key=lambda e: (not e['directory'], e['name'].lower()))
    return dict(entries=entries, path=os.path.relpath(p, root), limit=2000)


def do_read():
    data, st = read_capped(resolved(rel))
    return dict(data=base64.b64encode(data).decode(), sha256=digest(data), size=len(data), mtime=int(st.st_mtime * 1000))


def do_stat():
    p = resolved(rel)
    if not os.path.exists(p):
        return dict(exists=False)
    data, st = read_capped(p)
    return dict(exists=True, sha256=digest(data), size=len(data), mtime=int(st.st_mtime * 1000))


def do_exists():
    """Whether a workspace path names something, without reading it: terminal
    links check this before offering a bare file name."""
    p = resolved(rel)
    if not os.path.exists(p):
        return dict(exists=False, path=os.path.relpath(p, root))
    return dict(exists=True, directory=os.path.isdir(p), path=os.path.relpath(p, root))


def outside():
    """An absolute or ~/ path anywhere on the target, for a person to view
    read-only (the server allows only signed-in people to ask)."""
    p = os.path.expanduser(rel) if rel.startswith('~/') else rel
    if not os.path.isabs(p):
        raise Refused('give an absolute or ~/ path')
    return os.path.realpath(p)


def do_ext_stat():
    p = outside()
    if not os.path.exists(p):
        return dict(exists=False, path=p)
    st = os.stat(p)
    if not stat.S_ISREG(st.st_mode):
        return dict(exists=True, regular=False, path=p)
    return dict(exists=True, regular=True, path=p, size=st.st_size, mtime=int(st.st_mtime * 1000))


def do_ext_read():
    p = outside()
    fd = os.open(p, os.O_RDONLY | os.O_NONBLOCK)
    st = os.fstat(fd)
    if not stat.S_ISREG(st.st_mode):
        os.close(fd)
        raise Refused('choose a regular file')
    with os.fdopen(fd, 'rb') as f:
        if st.st_size > CAP:
            raise Refused('file exceeds 25 MiB limit')
        data = f.read(CAP + 1)
    if len(data) > CAP:
        raise Refused('file exceeds 25 MiB limit')
    return dict(data=base64.b64encode(data).decode(), sha256=digest(data), size=len(data),
                mtime=int(st.st_mtime * 1000), path=p)


def do_write():
    temp, expected = extra[0], extra[1]
    try:
        p = entry(rel)
        if os.path.islink(p):
            p = resolved(rel)
        current = None
        mode = 0o644
        if os.path.lexists(p):
            data, st = read_capped(p)
            current = digest(data)
            mode = stat.S_IMODE(st.st_mode)
        if expected == 'absent' and current is not None:
            raise Conflict(json.dumps(dict(sha256=current, detail='a file with this name already exists')))
        if expected not in ('absent', 'any') and expected != current:
            raise Conflict(json.dumps(dict(sha256=current, detail='the file changed on disk since you opened it')))
        if os.path.getsize(temp) > CAP:
            raise Refused('file exceeds 25 MiB limit')
        with open(temp, 'rb') as f:
            body = f.read(CAP + 1)
        staged = os.path.join(os.path.dirname(p), '.' + os.path.basename(p) + '.lectern-save-' + os.urandom(6).hex())
        fd = os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
        try:
            with os.fdopen(fd, 'wb') as out:
                out.write(body)
                out.flush()
                os.fsync(out.fileno())
            os.chmod(staged, mode)
            os.replace(staged, p)
        except BaseException:
            try:
                os.unlink(staged)
            except OSError:
                pass
            raise
        st = os.stat(p)
        return dict(sha256=digest(body), size=len(body), mtime=int(st.st_mtime * 1000), path=os.path.relpath(p, root))
    finally:
        try:
            os.unlink(temp)
        except OSError:
            pass


def do_mkdir():
    p = entry(rel)
    os.mkdir(p, 0o755)
    return dict(path=os.path.relpath(p, root))


def do_create():
    p = entry(rel)
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    os.close(fd)
    return dict(path=os.path.relpath(p, root), sha256=digest(b''))


def do_rename():
    src = entry(rel)
    dst = entry(extra[0])
    if not os.path.lexists(src):
        raise Refused('that file no longer exists')
    if os.path.lexists(dst):
        raise Refused('something with that name already exists')
    if os.path.isdir(src) and not os.path.islink(src) and inside_of(dst, src):
        raise Refused('a folder cannot move into itself')
    if os.path.relpath(src, root) == '.git':
        raise Refused('the repository folder cannot be moved')
    os.rename(src, dst)
    return dict(path=os.path.relpath(dst, root))


def inside_of(p, folder):
    return os.path.commonpath([folder, p]) == folder


def do_delete():
    p = entry(rel)
    if not os.path.lexists(p):
        raise Refused('that file no longer exists')
    if os.path.relpath(p, root) == '.git':
        raise Refused('the repository folder cannot be deleted here')
    if os.path.isdir(p) and not os.path.islink(p):
        shutil.rmtree(p)
    else:
        os.unlink(p)
    return dict(path=rel)


def do_archive():
    p = resolved(rel)
    if not os.path.isdir(p):
        raise Refused('choose a folder')
    buf = io.BytesIO()
    total = 0
    with zipfile.ZipFile(buf, 'w', zipfile.ZIP_DEFLATED) as z:
        for base, dirs, files in os.walk(p):
            dirs[:] = [d for d in dirs if inside(os.path.realpath(os.path.join(base, d))) and not os.path.islink(os.path.join(base, d))]
            for name in files:
                full = os.path.join(base, name)
                real = os.path.realpath(full)
                if not inside(real) or bookkeeping(real):
                    continue
                try:
                    st = os.stat(real)
                except OSError:
                    continue
                if not stat.S_ISREG(st.st_mode):
                    continue
                total += st.st_size
                if total > 4 * ARCHIVE_CAP:
                    raise Refused('folder exceeds the 200 MiB download limit')
                z.write(real, os.path.relpath(full, p))
                if buf.tell() > ARCHIVE_CAP:
                    raise Refused('folder exceeds the 50 MiB download limit')
    data = buf.getvalue()
    return dict(data=base64.b64encode(data).decode(), size=len(data))


def git(*args, timeout=20):
    return subprocess.run(['git', '-C', root] + list(args), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout)


def is_repo():
    if not shutil.which('git'):
        return False
    try:
        return git('rev-parse', '--is-inside-work-tree', timeout=10).stdout.strip() == b'true'
    except (OSError, subprocess.SubprocessError):
        return False


def keep(name):
    return name and not (grouped and bookkeeping(os.path.join(root, name)))


def do_index():
    limit = 50000
    started = time.time()
    files, ignored, source, truncated_ignored = [], [], 'walk', []
    if is_repo():
        source = 'git'
        out = git('ls-files', '-z', '--cached', '--others', '--exclude-standard').stdout
        seen = set()
        for name in out.decode('utf-8', 'replace').split('\0'):
            if keep(name) and name not in seen:
                seen.add(name)
                files.append(name)
        # Ignored files are a second pass. git reports an ignored folder once;
        # list what is inside it, capped per folder so that one dependency
        # tree cannot crowd out the rest.
        out = git('ls-files', '-z', '--others', '--ignored', '--exclude-standard', '--directory').stdout
        for name in out.decode('utf-8', 'replace').split('\0'):
            if not keep(name.rstrip('/')):
                continue
            if not name.endswith('/'):
                ignored.append(name)
                continue
            found = 0
            for base, dirs, names in os.walk(os.path.join(root, name)):
                dirs[:] = sorted(d for d in dirs if d != '.git' and not os.path.islink(os.path.join(base, d)))
                for n in sorted(names):
                    ignored.append(os.path.relpath(os.path.join(base, n), root))
                    found += 1
                if found >= 5000 or len(ignored) > limit:
                    truncated_ignored.append(name)
                    break
    elif shutil.which('rg'):
        source = 'ripgrep'
        out = subprocess.run(['rg', '--files', '--hidden', '--glob', '!.git'], cwd=root, stdout=subprocess.PIPE,
                             stderr=subprocess.DEVNULL, timeout=20).stdout
        files = [n for n in out.decode('utf-8', 'replace').split('\n') if keep(n)]
    else:
        for base, dirs, names in os.walk(root):
            dirs[:] = sorted(d for d in dirs if d not in ('.git', 'node_modules') and not os.path.islink(os.path.join(base, d)))
            for n in names:
                name = os.path.relpath(os.path.join(base, n), root)
                if keep(name):
                    files.append(name)
            if len(files) > limit:
                break
    truncated = len(files) > limit or len(ignored) > limit or bool(truncated_ignored)
    return dict(files=files[:limit], ignored=ignored[:limit], truncated=truncated, source=source, partial=truncated_ignored,
                elapsed_ms=int((time.time() - started) * 1000))


def do_git_status():
    if not is_repo():
        return dict(repository=False, status={})
    out = git('status', '--porcelain=v1', '-z', '--ignored=matching', '--untracked-files=normal').stdout
    parts = out.decode('utf-8', 'replace').split('\0')
    status = {}
    i = 0
    while i < len(parts):
        item = parts[i]
        i += 1
        if len(item) < 4:
            continue
        code, name = item[:2], item[3:]
        if code[0] in 'RC':
            i += 1  # the original path follows a rename or copy
        if code == '!!':
            kind = 'ignored'
        elif code == '??':
            kind = 'untracked'
        elif 'U' in code or code in ('AA', 'DD'):
            kind = 'conflict'
        elif 'D' in code:
            kind = 'deleted'
        elif code[0] == 'A':
            kind = 'added'
        elif code[0] in 'RC':
            kind = 'renamed'
        else:
            kind = 'modified'
        if keep(name.rstrip('/')):
            status[name] = kind
        if len(status) >= 20000:
            break
    prefix = git('rev-parse', '--show-prefix', timeout=10).stdout.decode().strip()
    return dict(repository=True, status=status, prefix=prefix)


def do_search():
    query, regex, case, word, include, ignored = extra[0], extra[1] == '1', extra[2] == '1', extra[3] == '1', extra[4], extra[5] == '1'
    if not query:
        raise Refused('type something to search for')
    limit, results, source, truncated = 2000, [], 'python', False
    deadline = time.time() + 20
    if regex:
        try:
            re.compile(query)
        except re.error as e:
            raise Refused('invalid regular expression: %s' % e)

    def add(path, line, col, text, end=None):
        nonlocal truncated
        if len(results) >= limit:
            truncated = True
            return False
        if keep(path):
            text = text.rstrip('\r\n')
            results.append(dict(path=path, line=line, column=col, end=end if end is not None else col, text=text[:400]))
        return True

    if shutil.which('rg'):
        source = 'ripgrep'
        args = ['rg', '--json', '--hidden', '--glob', '!.git', '--max-columns', '400', '--max-count', '200']
        args += ['--case-sensitive'] if case else ['--ignore-case']
        if word:
            args.append('--word-regexp')
        if not regex:
            args.append('--fixed-strings')
        if ignored:
            args.append('--no-ignore')
        if include:
            for g in include.split(','):
                if g.strip():
                    args += ['--glob', g.strip()]
        args += ['-e', query, '--', '.']
        proc = subprocess.Popen(args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            for raw in proc.stdout:
                if time.time() > deadline:
                    truncated = True
                    break
                event = json.loads(raw)
                if event.get('type') != 'match':
                    continue
                d = event['data']
                path = d['path'].get('text', '')
                path = path[2:] if path.startswith('./') else path
                text = d['lines'].get('text', '')
                sub = d['submatches'][0] if d['submatches'] else dict(start=0, end=0)
                line_bytes = text.encode('utf-8', 'replace')
                col = len(line_bytes[:sub['start']].decode('utf-8', 'replace')) + 1
                end = len(line_bytes[:sub['end']].decode('utf-8', 'replace')) + 1
                if not add(path, d['line_number'], col, text, end):
                    break
        finally:
            proc.kill()
            proc.wait()
    elif is_repo() and not ignored:
        source = 'git grep'
        # PCRE matches ripgrep's syntax most closely; a git built without it
        # exits 128 before printing anything, and ERE is the fallback.
        for flavour in (['-P'] if regex else ['-F']) + (['-E'] if regex else []):
            args = ['git', '-C', root, 'grep', '-n', '--column', '-I', '--untracked', '--no-color', flavour]
            args += [] if case else ['-i']
            if word:
                args.append('-w')
            args += ['-e', query, '--']
            if include:
                args += [':(glob)' + (g.strip() if '/' in g else '**/' + g.strip()) for g in include.split(',') if g.strip()]
            proc = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            try:
                for raw in proc.stdout:
                    if time.time() > deadline:
                        truncated = True
                        break
                    line = raw.decode('utf-8', 'replace')
                    m = re.match(r'^(.*?):(\d+):(\d+):(.*)$', line.rstrip('\n'))
                    if not m:
                        continue
                    text = m.group(4)
                    col = byte_to_char(text, int(m.group(3)))
                    if not add(m.group(1), int(m.group(2)), col, text, col + match_length(query, regex, case, word, text, col)):
                        break
            finally:
                proc.kill()
                rc = proc.wait()
            if results or rc != 128:
                break
    else:
        pattern = re.compile((r'\b(?:%s)\b' if word else '%s') % (query if regex else re.escape(query)), 0 if case else re.IGNORECASE)
        globs = [g.strip() for g in include.split(',') if g.strip()]
        import fnmatch
        for base, dirs, names in os.walk(root):
            if time.time() > deadline:
                truncated = True
                break
            dirs[:] = sorted(d for d in dirs if d != '.git' and (ignored or d != 'node_modules') and not os.path.islink(os.path.join(base, d)))
            for n in sorted(names):
                full = os.path.join(base, n)
                name = os.path.relpath(full, root)
                if globs and not any(fnmatch.fnmatch(n, g) or fnmatch.fnmatch(name, g) for g in globs):
                    continue
                try:
                    if os.path.islink(full) or os.path.getsize(full) > 4 * 1024 * 1024:
                        continue
                    with open(full, 'rb') as f:
                        data = f.read()
                except OSError:
                    continue
                if b'\0' in data[:8192]:
                    continue
                for number, text in enumerate(data.decode('utf-8', 'replace').split('\n'), 1):
                    m = pattern.search(text)
                    if m and not add(name, number, m.start() + 1, text, m.end() + 1):
                        break
                if truncated:
                    break
            if truncated:
                break
    return dict(results=results, truncated=truncated, source=source)


def byte_to_char(text, col):
    return len(text.encode('utf-8', 'replace')[:max(0, col - 1)].decode('utf-8', 'replace')) + 1


def match_length(query, regex, case, word, text, col):
    try:
        pattern = re.compile((r'\b(?:%s)\b' if word else '%s') % (query if regex else re.escape(query)), 0 if case else re.IGNORECASE)
        m = pattern.search(text, col - 1)
        return len(m.group(0)) if m else len(query)
    except re.error:
        return len(query)


IN_EVENTS = 0x2 | 0x4 | 0x8 | 0x40 | 0x80 | 0x100 | 0x200 | 0x400 | 0x800  # modify attrib close_write moved create delete self


def watch_signature(dirs):
    parts = []
    for d in dirs:
        try:
            p = resolved(d)
        except ValueError:
            continue
        try:
            with os.scandir(p) as scan:
                rows = []
                for e in scan:
                    if len(rows) >= 2000:
                        break
                    try:
                        st = e.stat(follow_symlinks=False)
                    except OSError:
                        continue
                    rows.append((e.name, st.st_mtime_ns, st.st_size))
            parts.append((d, sorted(rows)))
        except OSError:
            parts.append((d, None))
    for name in ('HEAD', 'index'):
        try:
            st = os.stat(os.path.join(root, '.git', name))
            parts.append(('.git/' + name, st.st_mtime_ns, st.st_size))
        except OSError:
            pass
    return hashlib.sha1(json.dumps(parts).encode()).hexdigest()[:20]


def inotify(dirs):
    """An inotify descriptor watching dirs (and .git), or None where the
    kernel or libc does not offer it."""
    try:
        import ctypes, ctypes.util
        libc = ctypes.CDLL(ctypes.util.find_library('c') or 'libc.so.6', use_errno=True)
        fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
        if fd < 0:
            return None
        added = 0
        for d in list(dirs) + ['.git']:
            try:
                p = resolved(d)
            except ValueError:
                continue
            if os.path.isdir(p) and libc.inotify_add_watch(fd, p.encode(), IN_EVENTS) >= 0:
                added += 1
        if not added:
            os.close(fd)
            return None
        return fd
    except (OSError, AttributeError):
        return None


def do_watch():
    """Long-poll: answer as soon as a watched folder (or git's HEAD/index)
    differs from the token the client last saw, or when the time is up."""
    import select
    dirs = [d for d in json.loads(extra[0])[:64] if isinstance(d, str)] or ['.']
    token, limit = extra[1], min(max(float(extra[2]), 1), 50)
    # Watch first, then look: a change between the two is still an event.
    fd = inotify(dirs)
    current = watch_signature(dirs)
    mode = 'inotify' if fd is not None else 'poll'
    if not token or current != token:
        if fd is not None:
            os.close(fd)
        return dict(token=current, changed=bool(token), mode=mode)
    deadline = time.time() + limit
    try:
        while time.time() < deadline:
            wait = deadline - time.time()
            if fd is not None:
                ready = select.select([fd], [], [], wait)[0]
                if not ready:
                    break
                time.sleep(0.15)  # let a burst of writes settle
                try:
                    while os.read(fd, 65536):
                        pass
                except OSError:
                    pass
            else:
                time.sleep(min(0.5, wait))
            now = watch_signature(dirs)
            if now != token:
                return dict(token=now, changed=True, mode=mode)
        return dict(token=token, changed=False, mode=mode)
    finally:
        if fd is not None:
            os.close(fd)


ACTIONS = dict(list=do_list, read=do_read, stat=do_stat, write=do_write, mkdir=do_mkdir, create=do_create,
               rename=do_rename, delete=do_delete, archive=do_archive, index=do_index, gitstatus=do_git_status,
               search=do_search, watch=do_watch, exists=do_exists,
               ext_stat=do_ext_stat, ext_read=do_ext_read)

try:
    if action not in ACTIONS:
        raise Refused('unknown file action')
    print(json.dumps(ACTIONS[action]()))
except Conflict as e:
    print(json.dumps(dict(conflict=json.loads(str(e)))))
    sys.exit(3)
except (OSError, ValueError, subprocess.SubprocessError) as e:
    message = e.strerror if isinstance(e, OSError) and e.strerror else str(e)
    print(json.dumps(dict(error=message)))
    sys.exit(1)
