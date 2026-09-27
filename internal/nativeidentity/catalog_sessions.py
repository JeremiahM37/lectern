"""List a catalog agent's saved conversations for one workspace, read-only.

Each CLI keeps sessions differently, so the agent definition says where (its
"sessions" object): a listing command that prints JSON, session files whose
first lines carry the metadata, or a SQLite table. Paths and commands come
from the saved agent definition on the Lectern side, never from HTTP; the
selected conversation id is only compared, never used to build a path.

argv: spec_json workspace bin [selected]
out:  {"conversations": [{id, title, created, modified, agent}], "scan_limited": bool}
"""
import datetime, glob, json, os, re, sqlite3, subprocess, sys

spec, workspace, binary = json.loads(sys.argv[1]), sys.argv[2], sys.argv[3]
selected = sys.argv[4] if len(sys.argv) > 4 else ''
workspace = os.path.realpath(workspace)
ID = re.compile(r'^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')
LIMIT = 2 * 1024 * 1024
MAX = 2000


SLUG = re.sub(r'[^a-zA-Z0-9]', '-', workspace)


def expand(pattern):
    """~, $VAR and {slug} expanded; None when a variable is unset, so the next
    candidate (usually the CLI's own default location) is used instead."""
    value = os.path.expandvars(os.path.expanduser(pattern.replace('{slug}', SLUG)))
    return None if '$' in value else value


def field(obj, name):
    for part in name.split('.'):
        if not isinstance(obj, dict):
            return None
        obj = obj.get(part)
    return obj


def seconds(value):
    """Epoch seconds from epoch s/ms/us numbers or an ISO-8601 string."""
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, (int, float)):
        v = float(value)
        while v > 1e11:  # ms or us
            v /= 1000.0
        return v
    if isinstance(value, str) and value:
        text = value.strip().replace('Z', '+00:00')
        try:
            stamp = datetime.datetime.fromisoformat(text)
        except ValueError:
            try:
                return seconds(float(text))
            except ValueError:
                return None
        if stamp.tzinfo is None:
            stamp = stamp.replace(tzinfo=datetime.timezone.utc)
        return stamp.timestamp()
    return None


def entry(obj, path=None):
    name = spec.get('id') or 'id'
    if name == '@stem' and path:
        cid = os.path.splitext(os.path.basename(path))[0]
    else:
        cid = field(obj, name)
    if not isinstance(cid, str) or not ID.fullmatch(cid):
        return None
    directory = field(obj, spec['dir']) if spec.get('dir') else None
    if spec.get('dir'):
        if not isinstance(directory, str) or not directory:
            return None
        if os.path.realpath(directory) != workspace:
            return None
    created = seconds(field(obj, spec.get('created') or 'created'))
    modified = seconds(field(obj, spec['updated'])) if spec.get('updated') else None
    if path and modified is None:
        modified = os.stat(path).st_mtime
    title = field(obj, spec['title']) if spec.get('title') else None
    title = title.strip().replace('\n', ' ')[:160] if isinstance(title, str) and title.strip() else 'Saved conversation'
    return dict(id=cid, title=title, created=created, modified=modified or created)


def from_command():
    command = spec['command'].replace('{bin}', binary)
    out = subprocess.run(['bash', '-c', command], cwd=workspace, capture_output=True, timeout=25,
                         stdin=subprocess.DEVNULL)
    if out.returncode != 0:
        raise ValueError('the session listing command failed')
    text = out.stdout.decode('utf-8', 'replace').strip()
    rows = []
    try:
        doc = json.loads(text) if text else []
        if isinstance(doc, dict):
            doc = next((v for v in doc.values() if isinstance(v, list)), [])
        rows = doc if isinstance(doc, list) else []
    except ValueError:
        for line in text.splitlines():
            try:
                rows.append(json.loads(line))
            except ValueError:
                continue
    return [e for e in (entry(r) for r in rows[:MAX] if isinstance(r, dict)) if e]


def from_files():
    paths = []
    for pattern in spec['files']:
        pattern = expand(pattern)
        if pattern is None:
            continue
        paths = glob.glob(pattern)
        break
    paths.sort(key=lambda p: os.path.getmtime(p) if os.path.exists(p) else 0, reverse=True)
    out = []
    for path in paths[:MAX]:
        if not os.path.isfile(path) or os.path.getsize(path) > 64 * LIMIT:
            continue
        merged = {}
        try:
            with open(path, 'rb') as f:
                if spec.get('whole'):
                    doc = json.loads(f.read(LIMIT))
                    merged = doc if isinstance(doc, dict) else {}
                else:
                    for _ in range(40):
                        line = f.readline(LIMIT)
                        if not line:
                            break
                        try:
                            row = json.loads(line)
                        except ValueError:
                            continue
                        if not isinstance(row, dict):
                            continue
                        if spec.get('header') and row.get('type') != spec['header']:
                            continue
                        for key, value in row.items():
                            merged.setdefault(key, value)
                        if spec.get('header'):
                            break
        except (OSError, ValueError, UnicodeError):
            continue
        e = entry(merged, path)
        if e:
            out.append(e)
    return out


def from_sqlite():
    path = None
    for candidate in spec['sqlite']:
        path = expand(candidate)
        if path is not None:
            break
    if not path or not os.path.isfile(path):
        return []
    db = sqlite3.connect('file:' + path + '?mode=ro', uri=True, timeout=5)
    try:
        cursor = db.execute(spec['query'])
        names = [d[0] for d in cursor.description]
        return [e for e in (entry(dict(zip(names, row))) for row in cursor.fetchmany(MAX)) if e]
    finally:
        db.close()


try:
    if selected and not ID.fullmatch(selected):
        raise ValueError('Choose a saved conversation ID')
    if spec.get('command'):
        rows = from_command()
    elif spec.get('files'):
        rows = from_files()
    elif spec.get('sqlite'):
        rows = from_sqlite()
    else:
        raise ValueError('this agent does not say where it keeps sessions')
    seen, conversations = set(), []
    for row in sorted(rows, key=lambda r: r.get('modified') or 0, reverse=True):
        if row['id'] in seen:
            continue
        seen.add(row['id'])
        row['agent'] = spec.get('agent', '')
        conversations.append(row)
    if selected:
        chosen = next((c for c in conversations if c['id'] == selected), None)
        if chosen is None:
            raise ValueError('Conversation not found in this workspace on this target')
        print(json.dumps(dict(conversation=chosen, messages=[], before=None, messages_readable=False)))
    else:
        print(json.dumps(dict(conversations=conversations[:500], scan_limited=len(conversations) > 500,
                              current=dict(state='unavailable'))))
except (OSError, ValueError, TypeError, KeyError, sqlite3.Error, subprocess.SubprocessError) as error:
    print(json.dumps(dict(error=str(error))))
    sys.exit(1)
