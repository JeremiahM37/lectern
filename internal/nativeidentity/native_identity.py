"""Read-only native identity evidence from the selected tmux pane on Linux."""
import json, os, re, stat, subprocess
from pathlib import Path


def native_identity(agent, workspace, home, name, expected, discovery=False):
    unknown = dict(state='unavailable')
    if not name or (expected and not re.fullmatch(r'[a-f0-9]{32}', expected)): return unknown
    if not Path('/proc/self/stat').exists(): return unknown
    workspace = os.path.realpath(workspace)

    if not home:
        home = os.environ.get('CODEX_HOME' if agent == 'codex' else 'CLAUDE_CONFIG_DIR', '')
    if not home:
        home = os.path.expanduser('~/.codex' if agent == 'codex' else '~/.claude')

    def pane():
        return subprocess.check_output(['tmux', 'display-message', '-p', '-t', '=' + name + ':',
            '#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}\t#{?#{@lectern-tracking-identity},#{@lectern-tracking-identity},#{@agentdeck-tracking-identity}}'],
            timeout=2, stderr=subprocess.DEVNULL, text=True).rstrip('\n').split('\t')

    def process(pid):
        # comm may contain spaces or parentheses. Fields after its final ')' start at 3.
        fields = Path('/proc', str(pid), 'stat').read_text().rsplit(')', 1)[1].split()
        return int(fields[1]), fields[19]

    def document(path):
        if not stat.S_ISREG(path.stat().st_mode) or path.stat().st_size > 2*1024*1024: return None
        with path.open() as f: return json.load(f)

    def process_env(pid, key, fallback):
        try:
            for item in Path('/proc', str(pid), 'environ').read_bytes().split(b'\0'):
                if item.startswith(key.encode() + b'='):
                    return item.split(b'=', 1)[1].decode()
        except (OSError, UnicodeError):
            pass
        return fallback

    try:
        before = pane()
        if len(before) != 5 or (expected and before[4] != expected): return dict(state='changed')
        # Older active records have no persisted marker. Their current pane can
        # still be observed read-only; do not claim it is the original session
        # or retain a binding after this observation.
        root = int(before[3]); root_start = process(root)[1]
        boot_id = Path('/proc/sys/kernel/random/boot_id').read_text().strip()
        # Snapshot ancestry once, and verify each evidence-bearing process again.
        census = {}
        for path in Path('/proc').glob('[0-9]*/stat'):
            try: census[int(path.parent.name)] = process(int(path.parent.name))
            except (OSError, ValueError, IndexError): pass
        members = {root}
        for _ in range(32):
            found = {pid for pid, (parent, _) in census.items() if parent in members}
            if found <= members: break
            members |= found
            if len(members) > 256: return unknown
        else: return unknown
        candidates = set()
        evidence = {}
        for pid in members:
            if pid not in census: continue
            start = census[pid][1]
            if agent == 'claude':
                path = Path(process_env(pid, 'CLAUDE_CONFIG_DIR', home), 'sessions', str(pid) + '.json')
                try: row = document(path)
                except (OSError, ValueError): continue
                if not isinstance(row, dict): continue
                if row.get('pid') != pid or str(row.get('procStart')) != start: continue
                if row.get('kind') != 'interactive' or row.get('entrypoint') != 'cli': continue
                if row.get('pidDomain'):
                    domain = 'linux:' + Path('/etc/machine-id').read_text().strip() + ':' + os.readlink('/proc/' + str(pid) + '/ns/pid')
                    if row['pidDomain'] != domain: continue
                try: actual_cwd = os.path.realpath(os.readlink('/proc/' + str(pid) + '/cwd'))
                except OSError: continue
                if not discovery and os.path.realpath(row.get('cwd', '')) != workspace: continue
                cid = row.get('sessionId', '')
                if re.fullmatch(r'[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}', str(cid)) and process(pid)[1] == start:
                    candidates.add(cid)
                    evidence[cid] = dict(pid=pid, proc_start=start, workspace=actual_cwd, native_home=str(Path(process_env(pid, 'CLAUDE_CONFIG_DIR', home)).resolve()), boot_id=boot_id, root_pid=root, root_start=root_start, pane_id=before[2])
            elif agent == 'codex':
                # A shell tool opening history is not the native agent.
                try:
                    if Path('/proc', str(pid), 'exe').resolve(strict=True).name != 'codex': continue
                except OSError: continue
                base = Path(process_env(pid, 'CODEX_HOME', home), 'sessions').resolve()
                for fd in Path('/proc', str(pid), 'fd').glob('*'):
                    try:
                        path = fd.resolve(strict=True)
                        if base not in path.parents or path.suffix != '.jsonl': continue
                        if not stat.S_ISREG(path.stat().st_mode): continue
                        with path.open('rb') as f: line = f.readline(2*1024*1024+1)
                        if len(line) > 2*1024*1024: continue
                        row = json.loads(line); meta = row.get('payload', {})
                        if row.get('type') != 'session_meta' or not isinstance(meta, dict): continue
                        # Codex launched by the VS Code extension writes the
                        # same interactive JSONL transcript and keeps it open
                        # on the real codex process. Both normal CLI and VS Code
                        # launches are valid; source alone is never sufficient,
                        # and subagents that do not hold the transcript FD remain
                        # excluded.
                        try: actual_cwd = os.path.realpath(os.readlink('/proc/' + str(pid) + '/cwd'))
                        except OSError: continue
                        if meta.get('source') not in ('cli', 'vscode') or (not discovery and os.path.realpath(meta.get('cwd', '')) != workspace): continue
                        cid = meta.get('id', '')
                        if not re.fullmatch(r'[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}', str(cid)): continue
                        if fd.resolve(strict=True) == path and process(pid)[1] == start:
                            candidates.add(cid)
                            evidence[cid] = dict(pid=pid, proc_start=start, workspace=actual_cwd, native_home=str(Path(process_env(pid, 'CODEX_HOME', home)).resolve()), boot_id=boot_id, root_pid=root, root_start=root_start, pane_id=before[2])
                    except (OSError, ValueError, TypeError, IndexError): continue
        if pane() != before or process(root)[1] != root_start: return dict(state='changed')
        if len(candidates) > 1 or len(evidence) > 1: return dict(state='ambiguous')
        if not candidates: return unknown
        cid = next(iter(candidates))
        if discovery:
            return dict(state='identified', id=cid, evidence=evidence.get(cid, {}))
        return dict(state='identified', id=cid)
    except (OSError, ValueError, TypeError, IndexError, subprocess.SubprocessError):
        return unknown
