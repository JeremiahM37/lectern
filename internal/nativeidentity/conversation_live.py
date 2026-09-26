"""Read a saved conversation as structured turns, forward from a byte cursor.

Companion to conversations.py: that script pages an operator-chosen
conversation BACKWARD from the end for the history picker; this one reads
ONE already-identified conversation FORWARD from a cursor, so the phone can
poll for only what was appended since its last look. Paths never come from
HTTP — cid is validated as a UUID and the resolved file must stay under the
agent's own history directory, exactly like conversations.py.
"""
import glob, json, os, re, sys
agent, workspace, cid, since = sys.argv[1:5]
workspace = os.path.realpath(workspace)
UUID = re.compile(r'^[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$')
LIMIT = 2 * 1024 * 1024
PAGE = 500  # cap items per poll so a huge backlog cannot stall a request

# Embedded remotely; importing also supports direct execution during diagnostics.
if 'native_metadata' not in globals():
    from native_records import native_metadata, structured_records

try:
    if agent not in ('codex', 'claude'): raise ValueError('Structured chat is available for Claude and Codex; use terminal text for this agent')
    if not UUID.fullmatch(cid): raise ValueError('Choose a saved conversation ID')
    if agent == 'codex':
        home = os.path.expanduser(os.environ.get('CODEX_HOME', '~/.codex'))
        base = os.path.join(home, 'sessions')
        pattern = os.path.join(base, '**', '*.jsonl')
    else:
        home = os.path.expanduser(os.environ.get('CLAUDE_CONFIG_DIR', '~/.claude'))
        slug = re.sub(r'[^a-zA-Z0-9]', '-', workspace)
        base = os.path.join(home, 'projects', slug)
        pattern = os.path.join(base, '*.jsonl')
    base = os.path.realpath(base)
    found = None
    for file in glob.iglob(pattern, recursive=agent == 'codex'):
        real = os.path.realpath(file)
        if os.path.commonpath([base, real]) != base or not os.path.isfile(real): continue
        if cid not in os.path.basename(file): continue
        try:
            info = native_metadata(real, agent, workspace)
        except (OSError, ValueError):
            continue
        if info and info['id'] == cid:
            found = real
            break
    if not found: raise ValueError('Conversation not found in this workspace on this target')
    size = os.path.getsize(found)
    # No cursor yet: start from a recent tail window rather than the whole
    # history, exactly like conversations.py's first page.
    start = int(since) if since else max(0, size - LIMIT)
    if start < 0 or start > size: start = 0
    items = []
    cursor = start
    with open(found, 'rb') as f:
        f.seek(start)
        while f.tell() < size and len(items) < PAGE:
            pos = f.tell()
            line = f.readline(min(LIMIT + 1, size - pos))
            if not line or not line.endswith(b'\n'):
                break  # nothing more, or a trailing in-progress write
            cursor = f.tell()
            try:
                row = json.loads(line)
            except (ValueError, UnicodeError):
                continue
            if not isinstance(row, dict): continue
            for i, block in enumerate(structured_records(row, agent)):
                block['id'] = '%d-%d' % (pos, i)
                items.append(block)
    print(json.dumps(dict(items=items, cursor=cursor, truncated=len(items) >= PAGE)))
except (OSError, ValueError, TypeError) as error:
    print(json.dumps(dict(error=str(error))))
    sys.exit(1)
