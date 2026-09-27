# Lectern computer use by element (docs/browser.md): read a live desktop's
# accessibility tree over AT-SPI, and act on an element from it.
#   a11y.py snapshot DIR          -> the tree, with [ref=N]; paths kept in DIR/a11y-refs.json
#   a11y.py act DIR REF ACTION [TEXT]  (ACTION: click | focus | type)
import json, os, sys
try:
    import gi
    gi.require_version('Atspi', '2.0')
    from gi.repository import Atspi
except Exception:
    print('MISSING python3-gi gir1.2-atspi-2.0'); sys.exit(0)

INTERACTIVE = {'push button', 'toggle button', 'check box', 'radio button', 'menu item', 'check menu item',
               'radio menu item', 'combo box', 'entry', 'password text', 'text', 'link', 'page tab', 'list item',
               'table cell', 'slider', 'spin button', 'tree item', 'icon', 'menu', 'toggle', 'button'}
LIMIT = 1200


def states(n):
    try:
        s = n.get_state_set()
    except Exception:
        return set()
    out = set()
    for name, st in (('showing', Atspi.StateType.SHOWING), ('focused', Atspi.StateType.FOCUSED),
                     ('checked', Atspi.StateType.CHECKED), ('editable', Atspi.StateType.EDITABLE),
                     ('disabled', None), ('selected', Atspi.StateType.SELECTED), ('expanded', Atspi.StateType.EXPANDED)):
        if st is not None and s.contains(st):
            out.add(name)
    if not s.contains(Atspi.StateType.ENABLED) and not s.contains(Atspi.StateType.SENSITIVE):
        out.add('disabled')
    return out


def extents(n):
    try:
        e = n.get_extents(Atspi.CoordType.SCREEN)
        return [e.x, e.y, e.width, e.height]
    except Exception:
        return None


def resolve(path):
    n = Atspi.get_desktop(0)
    for i in path:
        n = n.get_child_at_index(i)
        if n is None:
            return None
    return n


def snapshot(d):
    desk = Atspi.get_desktop(0)
    lines, refs = [], {}
    count = [0]

    def walk(n, path, depth):
        if count[0] >= LIMIT or depth > 40:
            return
        try:
            role, name = n.get_role_name(), (n.get_name() or '').strip()
            kids = n.get_child_count()
        except Exception:
            return
        st = states(n)
        if depth > 1 and 'showing' not in st and role not in ('application', 'frame', 'window'):
            return
        shown = bool(name) or role in INTERACTIVE or role in ('application', 'frame', 'window', 'dialog')
        child_depth = depth
        if shown:
            count[0] += 1
            line = '  ' * min(depth, 12) + '- ' + role
            if name:
                line += ' ' + json.dumps(' '.join(name.split())[:160])
            for flag in ('focused', 'checked', 'selected', 'expanded', 'disabled'):
                if flag in st:
                    line += ' [%s]' % flag
            if role not in ('application',):
                ref = len(refs) + 1
                refs[str(ref)] = {'path': path, 'role': role, 'name': name}
                line += ' [ref=%d]' % ref
                e = extents(n)
                if e and e[2] > 0 and e[3] > 0:
                    line += ' @%d,%d %dx%d' % tuple(e)
            if 'editable' in st:
                try:
                    t = n.get_text_iface()
                    if t:
                        line += ': ' + json.dumps(t.get_text(0, min(t.get_character_count(), 200)))
                except Exception:
                    pass
            lines.append(line)
            child_depth = depth + 1
        for i in range(min(kids, 400)):
            try:
                c = n.get_child_at_index(i)
            except Exception:
                continue
            if c is not None:
                walk(c, path + [i], child_depth)

    for i in range(desk.get_child_count()):
        app = desk.get_child_at_index(i)
        if app is not None:
            walk(app, [i], 0)
    with open(os.path.join(d, 'a11y-refs.json'), 'w') as f:
        json.dump(refs, f)
    if count[0] >= LIMIT:
        lines.append('- … (truncated)')
    print('TREE ' + json.dumps({'tree': '\n'.join(lines) + '\n', 'refs': len(refs)}))


def act(d, ref, action, text):
    try:
        refs = json.load(open(os.path.join(d, 'a11y-refs.json')))
    except Exception:
        print('ERROR take a computer snapshot first'); return
    info = refs.get(ref)
    if not info:
        print('ERROR ref %s is not in the latest snapshot; take a new one' % ref); return
    n = resolve(info['path'])
    try:
        same = n is not None and n.get_role_name() == info['role'] and (n.get_name() or '').strip() == info['name']
    except Exception:
        same = False
    if not same:
        print('ERROR the screen changed since the snapshot; take a new one'); return
    e = extents(n)
    point = [e[0] + e[2] // 2, e[1] + e[3] // 2] if e and e[2] > 0 and e[3] > 0 else None
    if action == 'type':
        try:
            et = n.get_editable_text_iface()
            if et and et.set_text_contents(text):
                print('OK ' + json.dumps({'via': 'accessibility'})); return
        except Exception:
            pass
        try:
            n.get_component_iface().grab_focus()
        except Exception:
            pass
        print('POINT ' + json.dumps({'point': point, 'then': 'type'})); return
    if action == 'focus':
        try:
            if n.get_component_iface().grab_focus():
                print('OK ' + json.dumps({'via': 'accessibility'})); return
        except Exception:
            pass
        print('POINT ' + json.dumps({'point': point})); return
    try:
        ai = n.get_action_iface()
        if ai:
            names = [ai.get_action_name(i) for i in range(ai.get_n_actions())]
            for want in ('click', 'press', 'activate', 'jump', 'toggle', 'open'):
                if want in names:
                    ai.do_action(names.index(want))
                    print('OK ' + json.dumps({'via': 'accessibility', 'action': want})); return
    except Exception:
        pass
    print('POINT ' + json.dumps({'point': point}))


if __name__ == '__main__':
    mode, d = sys.argv[1], sys.argv[2]
    if mode == 'snapshot':
        snapshot(d)
    else:
        act(d, sys.argv[3], sys.argv[4], sys.argv[5] if len(sys.argv) > 5 else '')
