"""Shared visible-message decoding for native history and search."""
import json, os, re
UUID = re.compile(r'^[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$')
LIMIT = 2 * 1024 * 1024

def content(value):
    if isinstance(value, str): return value
    if not isinstance(value, list): return ''
    out = []
    for block in value:
        if not isinstance(block, dict): continue
        kind = block.get('type', '')
        if kind in ('text', 'input_text', 'output_text'):
            text = block.get('text', '')
            if isinstance(text, str): out.append(text)
        elif kind == 'tool_use': out.append('Tool: ' + str(block.get('name', '')) + '\n' + json.dumps(block.get('input', {}), ensure_ascii=False))
        elif kind == 'tool_result': out.append('Tool result:\n' + content(block.get('content', '')))
        elif kind in ('image', 'input_image'): out.append('[Image attachment]')
    return '\n'.join(out)


def native_record(row, agent, limit=64000):
    if agent == 'codex':
        p = row.get('payload', {})
        if not isinstance(p,dict): return None
        if row.get('type') != 'response_item': return None
        kind = p.get('type')
        if p.get('channel') not in (None, '', 'final', 'commentary'): return None
        if kind == 'message': role, body = p.get('role'), content(p.get('content'))
        elif kind in ('function_call', 'custom_tool_call'):
            role, body = 'tool', str(p.get('name', 'Tool')) + '\n' + str(p.get('arguments', p.get('input', '')))
        elif kind in ('function_call_output', 'custom_tool_call_output'):
            role, body = 'tool', str(p.get('output', ''))
        else: return None
    else:
        if row.get('type') not in ('user', 'assistant'): return None
        if row.get('isSidechain'): return None
        msg = row.get('message', {})
        if not isinstance(msg, dict): return None
        role, body = msg.get('role'), content(msg.get('content'))
        if isinstance(msg.get('content'),list) and msg['content'] and all(b.get('type')=='tool_result' for b in msg['content'] if isinstance(b,dict)): role='tool'
    # System/developer context and private reasoning are not conversation prose.
    if role not in ('user', 'assistant', 'tool') or not body: return None
    return dict(role=role, text=body if limit is None else body[:limit], truncated=limit is not None and len(body)>limit, timestamp=row.get('timestamp', ''))


def _text_block(role, kind, text, limit, timestamp):
    if not isinstance(text, str) or not text.strip(): return None
    return dict(role=role, kind=kind, text=text[:limit], truncated=len(text) > limit, timestamp=timestamp)


def _codex_reasoning_text(payload):
    # Codex rollouts carry a reasoning item's visible text as a list of
    # summary blocks; fall back to a raw content list on older shapes.
    for key in ('summary', 'content'):
        blocks = payload.get(key)
        if isinstance(blocks, list):
            parts = [b.get('text', '') for b in blocks if isinstance(b, dict) and isinstance(b.get('text'), str)]
            if parts: return '\n'.join(parts)
    return ''


def structured_records(row, agent, limit=64000):
    """Decode one JSONL row into zero or more structured chat turns, same
    shape regardless of agent: {role, kind, timestamp, ...}. kind is one of
    text/thinking/tool_use/tool_result. Unlike native_record (which flattens
    everything to a single display string for the plain-text reader), this
    keeps tool_use input and tool_result output as separate, still-typed
    fields so the phone can render a card instead of a JSON dump. Private
    reasoning is now surfaced too (as kind='thinking', collapsed by the
    client) rather than silently dropped."""
    out = []
    ts = row.get('timestamp', '')
    if agent == 'codex':
        p = row.get('payload', {})
        if not isinstance(p, dict) or row.get('type') != 'response_item': return out
        kind = p.get('type')
        channel = p.get('channel')
        if kind == 'reasoning':
            text = _codex_reasoning_text(p)
            item = _text_block('assistant', 'thinking', text, limit, ts)
            if item: out.append(item)
            return out
        if channel not in (None, '', 'final', 'commentary'): return out
        if kind == 'message':
            role = p.get('role')
            for block in p.get('content') if isinstance(p.get('content'), list) else []:
                if not isinstance(block, dict): continue
                if block.get('type') in ('text', 'input_text', 'output_text'):
                    item = _text_block(role, 'text', block.get('text', ''), limit, ts)
                    if item: out.append(item)
        elif kind in ('function_call', 'custom_tool_call'):
            raw = p.get('arguments', p.get('input', {}))
            out.append(dict(role='assistant', kind='tool_use', timestamp=ts,
                             tool_name=str(p.get('name', 'Tool')), tool_use_id=str(p.get('call_id', p.get('id', ''))),
                             input=_as_input(raw)))
        elif kind in ('function_call_output', 'custom_tool_call_output'):
            text = str(p.get('output', ''))
            out.append(dict(role='tool', kind='tool_result', timestamp=ts,
                             tool_use_id=str(p.get('call_id', p.get('id', ''))),
                             output=text[:limit], truncated=len(text) > limit, is_error=bool(p.get('is_error'))))
    else:
        if row.get('type') not in ('user', 'assistant') or row.get('isSidechain'): return out
        msg = row.get('message', {})
        if not isinstance(msg, dict): return out
        role, body = msg.get('role'), msg.get('content')
        if isinstance(body, str):
            item = _text_block(role, 'text', body, limit, ts)
            if item: out.append(item)
        elif isinstance(body, list):
            for block in body:
                if not isinstance(block, dict): continue
                bkind = block.get('type')
                if bkind in ('text', 'input_text', 'output_text'):
                    item = _text_block(role, 'text', block.get('text', ''), limit, ts)
                    if item: out.append(item)
                elif bkind == 'thinking':
                    item = _text_block('assistant', 'thinking', block.get('thinking', block.get('text', '')), limit, ts)
                    if item: out.append(item)
                elif bkind == 'tool_use':
                    out.append(dict(role='assistant', kind='tool_use', timestamp=ts,
                                     tool_name=str(block.get('name', 'Tool')), tool_use_id=str(block.get('id', '')),
                                     input=_as_input(block.get('input', {}))))
                elif bkind == 'tool_result':
                    text = content(block.get('content', ''))
                    out.append(dict(role='tool', kind='tool_result', timestamp=ts,
                                     tool_use_id=str(block.get('tool_use_id', '')),
                                     output=text[:limit], truncated=len(text) > limit, is_error=bool(block.get('is_error'))))
                elif bkind in ('image', 'input_image'):
                    item = _text_block(role, 'text', '[Image attachment]', limit, ts)
                    if item: out.append(item)
    return out


def _as_input(raw):
    # Tool input arrives as a dict already, or (some codex shapes) a JSON
    # string; either way the client wants an object it can render field by
    # field, never a string it has to re-parse.
    if isinstance(raw, dict): return raw
    if isinstance(raw, str):
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, dict): return parsed
        except (ValueError, TypeError): pass
    return dict(value=raw) if raw not in (None, '') else {}


def native_metadata(file, agent, workspace=None, max_bytes=LIMIT):
    cid = cwd = title = ''
    codex_header_seen = False
    with open(file, 'rb') as f:
        for _ in range(80):
            if f.tell() >= max_bytes: break
            line = f.readline(max_bytes + 1)
            if not line or len(line)>max_bytes: break
            try: row = json.loads(line)
            except (ValueError, UnicodeError): continue
            if not isinstance(row, dict): continue
            if agent == 'codex' and row.get('type') == 'session_meta' and not codex_header_seen:
                # Forks copy the parent's header into their history. The first
                # native header belongs to this file; later headers are context.
                codex_header_seen = True
                p = row.get('payload', {})
                if not isinstance(p,dict): continue
                cid = p.get('id', p.get('session_id', '')); cwd = p.get('cwd', '')
            elif agent == 'claude':
                cid = cid or row.get('sessionId', ''); cwd = cwd or row.get('cwd', '')
            item = native_record(row, agent)
            if item and item['role'] == 'user' and not title: title = item['text'].strip().replace('\n', ' ')[:160]
            if cid and cwd and title: break
    if not UUID.fullmatch(str(cid)) or not isinstance(cwd, str) or not cwd: return None
    cwd = os.path.realpath(cwd)
    if workspace is not None and cwd != workspace: return None
    return dict(id=cid, title=title or 'Saved conversation', modified=os.stat(file).st_mtime, agent=agent, cwd=cwd)
